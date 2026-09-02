package brain

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testCompletionCondition(description string) CompletionConditionInput {
	return CompletionConditionInput{
		Kind: "test", Description: description, Required: true,
		Verification: json.RawMessage(`{"command":"go test ./internal/brain"}`),
	}
}

type failingWorkEmbedder struct{}

func (failingWorkEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, errors.New("test embedding unavailable")
}

func (failingWorkEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	return nil, errors.New("test embedding unavailable")
}

func (failingWorkEmbedder) Model() string { return "test/work-memory" }
func (failingWorkEmbedder) Dims() int     { return 1 }

func TestWorkExecutionInputValidation(t *testing.T) {
	conditions, err := validateCompletionConditionInputs([]CompletionConditionInput{
		{Kind: "test", Description: "  focused tests pass  ", Required: true, Verification: json.RawMessage(`{"command":"go test ./..."}`)},
		{Kind: "user", Description: "optional review", Required: false, Verification: json.RawMessage(`{"prompt":"review"}`)},
	})
	if err != nil {
		t.Fatalf("validateCompletionConditionInputs: %v", err)
	}
	if conditions[0].Description != "focused tests pass" {
		t.Fatalf("condition was not normalized: %#v", conditions[0])
	}
	if _, err := validateCompletionConditionInputs([]CompletionConditionInput{{Kind: "user", Description: "optional", Required: false, Verification: json.RawMessage(`{"prompt":"review"}`)}}); !errors.Is(err, ErrWorkConditionsMissing) {
		t.Fatalf("optional-only conditions error = %v, want %v", err, ErrWorkConditionsMissing)
	}

	if got, err := normalizeWorkLeaseDuration(0); err != nil || got != DefaultWorkLeaseDuration {
		t.Fatalf("default lease = %s, %v", got, err)
	}
	if _, err := normalizeWorkLeaseDuration(MaxWorkLeaseDuration + time.Second); err == nil {
		t.Fatal("oversized work lease was accepted")
	}
	if _, err := validateCheckpointInput(WorkCheckpointInput{}, true); !errors.Is(err, ErrEmptyContent) {
		t.Fatalf("empty handoff next action error = %v, want wrapped %v", err, ErrEmptyContent)
	}
	if _, err := validateWorkEvidenceInput(WorkEvidenceInput{
		EvidenceType: "test", Summary: "passed", Payload: []byte(`{"broken"`),
	}); err == nil {
		t.Fatal("invalid evidence JSON was accepted")
	}

	ids, err := normalizeEvidenceIDs([]int64{7, 7, 9})
	if err != nil {
		t.Fatalf("normalizeEvidenceIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != 7 || ids[1] != 9 {
		t.Fatalf("evidence IDs = %#v, want [7 9]", ids)
	}
	if _, err := normalizeEvidenceIDs(nil); err == nil {
		t.Fatal("condition verification without evidence was accepted")
	}
}

func TestAutomaticWorkOutcomeTextIsBounded(t *testing.T) {
	content := automaticWorkOutcomeText("요약", strings.Repeat("결과", automaticWorkOutcomeLimit), "다음 행동")
	if len(content) > automaticWorkOutcomeLimit {
		t.Fatalf("automatic outcome length = %d, want <= %d", len(content), automaticWorkOutcomeLimit)
	}
	if !utf8.ValidString(content) {
		t.Fatal("automatic outcome is not valid UTF-8")
	}
	if !strings.HasPrefix(content, "Summary: 요약\nResult: ") || !strings.HasSuffix(content, "…") {
		t.Fatalf("automatic outcome = %q", content)
	}
}

func TestWorkLeaseTokenIsOpaqueAndHashed(t *testing.T) {
	token, hash, err := newWorkLeaseToken()
	if err != nil {
		t.Fatalf("newWorkLeaseToken: %v", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("lease token is not URL-safe opaque data: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("lease token has %d random bytes, want 32", len(raw))
	}
	wantHash := sha256.Sum256([]byte(token))
	if string(hash) != string(wantHash[:]) {
		t.Fatal("stored lease hash does not match the returned token")
	}
	if token == base64.RawURLEncoding.EncodeToString(hash) {
		t.Fatal("returned lease token exposed its stored hash")
	}
	second, _, err := newWorkLeaseToken()
	if err != nil {
		t.Fatalf("second newWorkLeaseToken: %v", err)
	}
	if second == token {
		t.Fatal("two work attempts received the same lease token")
	}
}

func TestWorkCompletionRules(t *testing.T) {
	tests := []struct {
		name               string
		required           int64
		incompleteRequired int64
		unfinishedBlocker  bool
		want               error
	}{
		{name: "ready", required: 2},
		{name: "no required condition", want: ErrWorkConditionsMissing},
		{name: "condition lacks result or evidence", required: 2, incompleteRequired: 1, want: ErrWorkConditionsIncomplete},
		{name: "unfinished blocker", required: 1, unfinishedBlocker: true, want: ErrWorkBlockersUnfinished},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateWorkCompletion(test.required, test.incompleteRequired, test.unfinishedBlocker)
			if !errors.Is(err, test.want) {
				t.Fatalf("validateWorkCompletion() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestWorkExecutionDatabaseFlow(t *testing.T) {
	dsn := os.Getenv("STASH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("STASH_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("database ping: %v", err)
	}
	b := &Brain{pool: pool, config: DefaultConfig()}

	slug := fmt.Sprintf("/work-execution-%d", time.Now().UnixNano())
	var namespaceID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO namespaces (slug, name, description) VALUES ($1, 'work execution test', '') RETURNING id`,
		slug,
	).Scan(&namespaceID); err != nil {
		t.Fatalf("create test namespace: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(),
			`UPDATE work_attempts SET status = 'expired', ended_at = clock_timestamp(), updated_at = now()
			 WHERE status = 'active' AND work_item_id IN (SELECT id FROM work_items WHERE namespace_id = $1)`,
			namespaceID,
		)
		if _, err := pool.Exec(context.Background(), `DELETE FROM namespaces WHERE id = $1`, namespaceID); err != nil {
			t.Errorf("delete test namespace: %v", err)
		}
	}()
	createWorkItem := func(title string) int64 {
		t.Helper()
		var id int64
		if err := pool.QueryRow(ctx,
			`INSERT INTO work_items (namespace_id, title, description, status) VALUES ($1, $2, '', 'ready') RETURNING id`,
			namespaceID, title,
		).Scan(&id); err != nil {
			t.Fatalf("create work item %q: %v", title, err)
		}
		return id
	}

	workItemID := createWorkItem("finish with evidence")
	if _, err := pool.Exec(ctx,
		`INSERT INTO work_plan_items (work_item_id, kind) VALUES ($1, 'task')`,
		workItemID,
	); err != nil {
		t.Fatalf("create plan task metadata: %v", err)
	}
	worktree, err := b.RegisterWorktree(ctx, namespaceID, "stash", "/tmp/work-execution", "test", "deadbeef", "clean", "agent-a", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("RegisterWorktree: %v", err)
	}
	prepared, err := b.PrepareWork(ctx, workItemID, "implement and run the database check", []CompletionConditionInput{
		testCompletionCondition("focused database check passes"),
	}, "prepare-main")
	if err != nil {
		t.Fatalf("PrepareWork: %v", err)
	}
	preparedReplay, err := b.PrepareWork(ctx, workItemID, "implement and run the database check", []CompletionConditionInput{
		testCompletionCondition("focused database check passes"),
	}, "prepare-main")
	if err != nil || preparedReplay.CompletionConditions[0].ID != prepared.CompletionConditions[0].ID {
		t.Fatalf("PrepareWork replay = %#v, %v", preparedReplay, err)
	}
	lease, err := b.StartWorkAttempt(ctx, workItemID, "agent-a", &worktree.ID, time.Minute, "start-main")
	if err != nil {
		t.Fatalf("StartWorkAttempt: %v", err)
	}
	firstToken := lease.LeaseToken
	lease, err = b.StartWorkAttempt(ctx, workItemID, "agent-a", &worktree.ID, time.Minute, "start-main")
	if err != nil {
		t.Fatalf("StartWorkAttempt replay: %v", err)
	}
	if lease.Attempt.ID == 0 || lease.LeaseToken == firstToken || lease.Attempt.WorktreeID == nil || *lease.Attempt.WorktreeID != worktree.ID {
		t.Fatalf("start replay did not reuse the attempt and rotate its token: %#v", lease)
	}
	if _, err := b.RenewWorkAttemptLease(ctx, lease.Attempt.ID, firstToken, time.Minute, "renew-with-first-replay-token"); err != nil {
		t.Fatalf("first start replay token was not kept valid: %v", err)
	}
	var activeTokenCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM work_attempt_lease_tokens WHERE attempt_id = $1 AND revoked_at IS NULL`,
		lease.Attempt.ID,
	).Scan(&activeTokenCount); err != nil || activeTokenCount != 2 {
		t.Fatalf("active replay token count = %d, %v; want 2", activeTokenCount, err)
	}
	var startEventKey string
	if err := pool.QueryRow(ctx,
		`SELECT event_key FROM work_events WHERE attempt_id = $1 AND event_type = 'work.attempt.started'`,
		lease.Attempt.ID,
	).Scan(&startEventKey); err != nil {
		t.Fatalf("read start event key: %v", err)
	}
	if startEventKey == "start-main" || startEventKey != "attempt.started" {
		t.Fatalf("start event exposed its recovery key: %q", startEventKey)
	}
	var storedStartActionKey, startReceipt string
	if err := pool.QueryRow(ctx,
		`SELECT action_key, response::text FROM work_action_receipts WHERE work_item_id = $1 AND action_type = 'start'`,
		workItemID,
	).Scan(&storedStartActionKey, &startReceipt); err != nil {
		t.Fatalf("read start action receipt: %v", err)
	}
	if storedStartActionKey != workActionKeyDigest("start-main") || strings.Contains(storedStartActionKey, "start-main") {
		t.Fatalf("start action receipt stored a raw recovery key: %q", storedStartActionKey)
	}
	if strings.Contains(startReceipt, firstToken) || strings.Contains(startReceipt, lease.LeaseToken) || strings.Contains(startReceipt, "lease_token") {
		t.Fatalf("start action receipt exposed a lease token: %s", startReceipt)
	}
	var startedBy string
	if err := pool.QueryRow(ctx, `SELECT started_by FROM work_plan_items WHERE work_item_id = $1`, workItemID).Scan(&startedBy); err != nil || startedBy != "agent-a" {
		t.Fatalf("plan task started_by = %q, %v", startedBy, err)
	}
	if _, err := b.StartWorkAttempt(ctx, workItemID, "agent-b", nil, time.Minute, "start-other"); !errors.Is(err, ErrActiveWorkAttempt) {
		t.Fatalf("second StartWorkAttempt error = %v, want %v", err, ErrActiveWorkAttempt)
	}
	if _, err := b.CompleteWorkPlanTask(ctx, workItemID, "agent-b"); err == nil || !strings.Contains(err.Error(), "active attempt") {
		t.Fatalf("legacy plan completion bypass error = %v, want active-attempt enforcement", err)
	}
	if item, err := b.GetWorkItem(ctx, workItemID); err != nil || item.Status != "doing" {
		t.Fatalf("work item changed after rejected legacy completion: item=%#v err=%v", item, err)
	}
	validCheckpoint := WorkCheckpointInput{Summary: "implementation is in place", Result: "unit checks pass", NextAction: "run database check"}
	if _, err := b.CheckpointWorkAttempt(ctx, lease.Attempt.ID, "wrong-token", validCheckpoint, time.Minute, "checkpoint-wrong"); !errors.Is(err, ErrWorkAttemptLease) {
		t.Fatalf("wrong-token checkpoint error = %v, want %v", err, ErrWorkAttemptLease)
	}
	checkpoint, err := b.CheckpointWorkAttempt(ctx, lease.Attempt.ID, lease.LeaseToken, validCheckpoint, 2*time.Minute, "checkpoint-main")
	if err != nil {
		t.Fatalf("CheckpointWorkAttempt: %v", err)
	}
	checkpointReplay, err := b.CheckpointWorkAttempt(ctx, lease.Attempt.ID, lease.LeaseToken, validCheckpoint, 2*time.Minute, "checkpoint-main")
	if err != nil || checkpointReplay.Checkpoint.ID != checkpoint.Checkpoint.ID {
		t.Fatalf("checkpoint replay = %#v, %v", checkpointReplay, err)
	}
	if _, err := b.CheckpointWorkAttempt(ctx, lease.Attempt.ID, lease.LeaseToken, WorkCheckpointInput{
		Summary: validCheckpoint.Summary, Result: "different result", NextAction: validCheckpoint.NextAction,
	}, 2*time.Minute, "checkpoint-main"); !errors.Is(err, ErrWorkActionConflict) {
		t.Fatalf("reused action key error = %v, want %v", err, ErrWorkActionConflict)
	}
	renewed, err := b.RenewWorkAttemptLease(ctx, lease.Attempt.ID, lease.LeaseToken, 3*time.Minute, "renew-main")
	if err != nil {
		t.Fatalf("RenewWorkAttemptLease: %v", err)
	}
	renewReplay, err := b.RenewWorkAttemptLease(ctx, lease.Attempt.ID, lease.LeaseToken, 3*time.Minute, "renew-main")
	if err != nil || !renewReplay.LeaseExpiresAt.Equal(renewed.LeaseExpiresAt) {
		t.Fatalf("renew replay = %#v, %v", renewReplay, err)
	}
	conditionID := prepared.CompletionConditions[0].ID
	evidenceInput := WorkEvidenceInput{
		EvidenceType: "test", Summary: "database flow passed", Reference: "go test ./internal/brain",
		Payload: json.RawMessage(`{"passed":true}`),
	}
	evidence, err := b.SubmitWorkEvidence(ctx, lease.Attempt.ID, lease.LeaseToken, evidenceInput, []int64{conditionID}, "evidence-main")
	if err != nil {
		t.Fatalf("SubmitWorkEvidence: %v", err)
	}
	wantDigest, err := workEvidenceContentDigest(evidenceInput)
	if err != nil {
		t.Fatalf("workEvidenceContentDigest: %v", err)
	}
	if evidence.ContentDigest != wantDigest || evidence.WorktreeHeadSHA != "deadbeef" || evidence.PrincipalID != "" || evidence.SubmittedAt.IsZero() {
		t.Fatalf("evidence provenance = %#v, want digest=%q head=deadbeef local principal", evidence, wantDigest)
	}
	evidenceReplay, err := b.SubmitWorkEvidence(ctx, lease.Attempt.ID, lease.LeaseToken, evidenceInput, []int64{conditionID}, "evidence-main")
	if err != nil || evidenceReplay.ID != evidence.ID {
		t.Fatalf("evidence replay = %#v, %v", evidenceReplay, err)
	}
	verified, err := b.VerifyWorkCondition(ctx, lease.Attempt.ID, lease.LeaseToken, conditionID, "passed", []int64{evidence.ID}, "", "verify-main")
	if err != nil {
		t.Fatalf("VerifyWorkCondition: %v", err)
	}
	verifiedReplay, err := b.VerifyWorkCondition(ctx, lease.Attempt.ID, lease.LeaseToken, conditionID, "passed", []int64{evidence.ID}, "", "verify-main")
	if err != nil || verifiedReplay.ID != verified.ID {
		t.Fatalf("condition replay = %#v, %v", verifiedReplay, err)
	}

	blockerID := createWorkItem("unfinished blocker")
	if _, err := b.AddWorkItemEdge(ctx, namespaceID, blockerID, workItemID, "blocks"); err != nil {
		t.Fatalf("AddWorkItemEdge: %v", err)
	}
	finishInput := WorkFinishInput{Summary: "all required checks are done", Result: "completed"}
	if _, err := b.FinishWorkAttempt(ctx, lease.Attempt.ID, lease.LeaseToken, finishInput, "finish-main"); !errors.Is(err, ErrWorkBlockersUnfinished) {
		t.Fatalf("blocked FinishWorkAttempt error = %v, want %v", err, ErrWorkBlockersUnfinished)
	}
	var goalID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO goals (namespace_id, content, status, priority, notes) VALUES ($1, 'finish work', 'active', 1, '') RETURNING id`,
		namespaceID,
	).Scan(&goalID); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if _, err := b.LinkWorkItemMemory(ctx, workItemID, "goal", goalID, "context"); err != nil {
		t.Fatalf("LinkWorkItemMemory: %v", err)
	}
	bundle, err := b.GetWorkResumeBundle(ctx, workItemID, 100)
	if err != nil {
		t.Fatalf("GetWorkResumeBundle before completion: %v", err)
	}
	if bundle.NextAction != validCheckpoint.NextAction || len(bundle.Blockers) != 1 || len(bundle.WorktreeLinks) != 1 || len(bundle.MemoryLinks) != 1 {
		t.Fatalf("resume state: next=%q blockers=%d worktrees=%d memories=%d", bundle.NextAction, len(bundle.Blockers), len(bundle.WorktreeLinks), len(bundle.MemoryLinks))
	}
	if memory := bundle.MemoryLinks[0]; memory.Content != "finish work" || memory.Status != "active" || memory.Relation != "context" {
		t.Fatalf("resume memory snapshot = %#v", memory)
	}
	encodedResume, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal resume bundle: %v", err)
	}
	for _, rawActionKey := range []string{"start-main", "checkpoint-main", "evidence-main", "verify-main"} {
		if strings.Contains(string(encodedResume), rawActionKey) {
			t.Fatalf("resume bundle exposed raw action key %q: %s", rawActionKey, encodedResume)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE work_items SET status = 'done', completed_at = now() WHERE id = $1`, blockerID); err != nil {
		t.Fatalf("complete blocker: %v", err)
	}
	completed, err := b.FinishWorkAttempt(ctx, lease.Attempt.ID, lease.LeaseToken, finishInput, "finish-main")
	if err != nil {
		t.Fatalf("FinishWorkAttempt: %v", err)
	}
	completedReplay, err := b.FinishWorkAttempt(ctx, lease.Attempt.ID, lease.LeaseToken, finishInput, "finish-main")
	if err != nil || completedReplay.ID != completed.ID {
		t.Fatalf("finish replay = %#v, %v", completedReplay, err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM work_attempt_lease_tokens WHERE attempt_id = $1 AND revoked_at IS NULL`,
		lease.Attempt.ID,
	).Scan(&activeTokenCount); err != nil || activeTokenCount != 0 {
		t.Fatalf("active tokens after completion = %d, %v; want 0", activeTokenCount, err)
	}
	var completedBy string
	if err := pool.QueryRow(ctx, `SELECT completed_by FROM work_plan_items WHERE work_item_id = $1`, workItemID).Scan(&completedBy); err != nil || completedBy != "agent-a" {
		t.Fatalf("plan task completed_by = %q, %v", completedBy, err)
	}
	bundle, err = b.GetWorkResumeBundle(ctx, workItemID, 100)
	if err != nil {
		t.Fatalf("GetWorkResumeBundle after completion: %v", err)
	}
	if bundle.NextAction != "" || bundle.WorkItem.Status != "done" || bundle.LatestAttempt == nil || bundle.LatestAttempt.Status != "completed" {
		t.Fatalf("completed resume state = next %q item %q attempt %#v", bundle.NextAction, bundle.WorkItem.Status, bundle.LatestAttempt)
	}
	if bundle.LatestCheckpoint == nil || bundle.LatestCheckpoint.Result != "completed" || len(bundle.CompletionConditions[0].EvidenceIDs) != 1 {
		t.Fatalf("completed checkpoint/condition = %#v / %#v", bundle.LatestCheckpoint, bundle.CompletionConditions)
	}
	completionEventFound := false
	for _, event := range bundle.RecentEvents {
		if event.EventType == "work.attempt.completed" && event.AttemptID != nil && *event.AttemptID == lease.Attempt.ID {
			completionEventFound = true
		}
	}
	if !completionEventFound {
		t.Fatal("attempt-aware completion event was not returned")
	}

	handoffItemID := createWorkItem("handoff with next action")
	if _, err := b.PrepareWork(ctx, handoffItemID, "analyze the remaining branch", []CompletionConditionInput{testCompletionCondition("handoff target finishes")}, "prepare-handoff"); err != nil {
		t.Fatalf("prepare handoff item: %v", err)
	}
	handoffLease, err := b.StartWorkAttempt(ctx, handoffItemID, "agent-a", nil, time.Minute, "start-handoff")
	if err != nil {
		t.Fatalf("start handoff attempt: %v", err)
	}
	handoffBlockerID := createWorkItem("handoff blocker")
	if _, err := b.AddWorkItemEdge(ctx, namespaceID, handoffBlockerID, handoffItemID, "blocks"); err != nil {
		t.Fatalf("add handoff blocker: %v", err)
	}
	handoffInput := WorkCheckpointInput{Summary: "analysis finished", Result: "one branch remains", NextAction: "implement the remaining branch"}
	handedOff, err := b.HandoffWorkAttempt(ctx, handoffLease.Attempt.ID, handoffLease.LeaseToken, handoffInput, "handoff-main")
	if err != nil {
		t.Fatalf("HandoffWorkAttempt: %v", err)
	}
	handoffReplay, err := b.HandoffWorkAttempt(ctx, handoffLease.Attempt.ID, handoffLease.LeaseToken, handoffInput, "handoff-main")
	if err != nil || handoffReplay.ID != handedOff.ID {
		t.Fatalf("handoff replay = %#v, %v", handoffReplay, err)
	}
	handoffBundle, err := b.GetWorkResumeBundle(ctx, handoffItemID, 20)
	if err != nil {
		t.Fatalf("resume handed-off work: %v", err)
	}
	if handoffBundle.WorkItem.Status != "blocked" || handoffBundle.NextAction != handoffInput.NextAction ||
		handoffBundle.LatestCheckpoint == nil || handoffBundle.LatestCheckpoint.NextAction != handoffInput.NextAction {
		t.Fatalf("handoff resume state = item %q next %q checkpoint %#v", handoffBundle.WorkItem.Status, handoffBundle.NextAction, handoffBundle.LatestCheckpoint)
	}
	if _, err := pool.Exec(ctx, `UPDATE work_items SET status = 'done', completed_at = now() WHERE id = $1`, handoffBlockerID); err != nil {
		t.Fatalf("complete handoff blocker: %v", err)
	}
	resumedLease, err := b.StartWorkAttempt(ctx, handoffItemID, "agent-b", nil, time.Minute, "start-resumed")
	if err != nil {
		t.Fatalf("start resumed attempt: %v", err)
	}
	if _, err := b.HandoffWorkAttempt(ctx, resumedLease.Attempt.ID, resumedLease.LeaseToken, WorkCheckpointInput{
		Summary: "resumed", Result: "waiting", NextAction: "continue later",
	}, "handoff-resumed"); err != nil {
		t.Fatalf("end resumed attempt: %v", err)
	}

	staleItemID := createWorkItem("replace stale lease")
	if _, err := b.PrepareWork(ctx, staleItemID, "continue stale work", []CompletionConditionInput{testCompletionCondition("new owner continues")}, "prepare-stale"); err != nil {
		t.Fatalf("prepare stale item: %v", err)
	}
	staleLease, err := b.StartWorkAttempt(ctx, staleItemID, "agent-stale", nil, time.Millisecond, "start-stale")
	if err != nil {
		t.Fatalf("start stale attempt: %v", err)
	}
	time.Sleep(25 * time.Millisecond)
	replacement, err := b.StartWorkAttempt(ctx, staleItemID, "agent-replacement", nil, time.Minute, "start-replacement")
	if err != nil {
		t.Fatalf("replace stale attempt: %v", err)
	}
	var staleStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM work_attempts WHERE id = $1`, staleLease.Attempt.ID).Scan(&staleStatus); err != nil {
		t.Fatalf("read stale attempt status: %v", err)
	}
	if staleStatus != "expired" {
		t.Fatalf("stale attempt status = %q, want expired", staleStatus)
	}
	if _, err := b.HandoffWorkAttempt(ctx, replacement.Attempt.ID, replacement.LeaseToken, WorkCheckpointInput{
		Summary: "replacement started", Result: "lease recovered", NextAction: "continue after stale owner",
	}, "handoff-replacement"); err != nil {
		t.Fatalf("end replacement attempt: %v", err)
	}

	blockedItemID := createWorkItem("reject blocked start")
	blockedByID := createWorkItem("blocks start")
	if _, err := b.AddWorkItemEdge(ctx, namespaceID, blockedByID, blockedItemID, "blocks"); err != nil {
		t.Fatalf("add start blocker: %v", err)
	}
	if _, err := b.PrepareWork(ctx, blockedItemID, "wait for blocker", []CompletionConditionInput{testCompletionCondition("blocker is done")}, "prepare-blocked"); err != nil {
		t.Fatalf("prepare blocked item: %v", err)
	}
	if _, err := b.StartWorkAttempt(ctx, blockedItemID, "agent-blocked", nil, time.Minute, "start-blocked"); !errors.Is(err, ErrWorkBlockersUnfinished) {
		t.Fatalf("blocked start error = %v, want %v", err, ErrWorkBlockersUnfinished)
	}
	var blockedStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM work_items WHERE id = $1`, blockedItemID).Scan(&blockedStatus); err != nil || blockedStatus != "blocked" {
		t.Fatalf("blocked work item status = %q, %v", blockedStatus, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE work_items SET status = 'done', completed_at = now() WHERE id = $1`, blockedByID); err != nil {
		t.Fatalf("complete start blocker: %v", err)
	}
	unblockedLease, err := b.StartWorkAttempt(ctx, blockedItemID, "agent-blocked", nil, time.Minute, "start-blocked")
	if err != nil {
		t.Fatalf("retry blocked start: %v", err)
	}
	if _, err := b.HandoffWorkAttempt(ctx, unblockedLease.Attempt.ID, unblockedLease.LeaseToken, WorkCheckpointInput{
		Summary: "blocker cleared", Result: "work can start", NextAction: "continue work",
	}, "handoff-unblocked"); err != nil {
		t.Fatalf("end unblocked attempt: %v", err)
	}

	b.embedder = failingWorkEmbedder{}
	remembered, err := b.RememberForWork(ctx, workItemID, "The database flow remains the required acceptance check.", "result", "remember-main")
	if err != nil {
		t.Fatalf("RememberForWork: %v", err)
	}
	rememberedReplay, err := b.RememberForWork(ctx, workItemID, "The database flow remains the required acceptance check.", "result", "remember-main")
	if err != nil || rememberedReplay.ID != remembered.ID || remembered.IndexingStatus != "pending" || remembered.Link.Relation != "result" {
		t.Fatalf("work memory replay = %#v, original %#v, %v", rememberedReplay, remembered, err)
	}
}
