package brain

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alash3al/stash/internal/db"
	"github.com/alash3al/stash/internal/models"
)

func TestWorkExecutionValidation(t *testing.T) {
	tests := []struct {
		name    string
		check   func() error
		wantErr error
	}{
		{
			name: "required condition is mandatory",
			check: func() error {
				_, err := validateCompletionConditionInputs([]CompletionConditionInput{{
					Kind: "test", Description: "optional", Required: false,
					Verification: json.RawMessage(`{"command":"go test ./..."}`),
				}})
				return err
			},
			wantErr: ErrWorkConditionsMissing,
		},
		{
			name: "verification needs evidence",
			check: func() error {
				_, err := normalizeEvidenceIDs(nil)
				return err
			},
		},
		{
			name: "pending required condition prevents completion",
			check: func() error {
				return validateWorkCompletion(1, 1, false)
			},
			wantErr: ErrWorkConditionsIncomplete,
		},
		{
			name: "unfinished blocker prevents completion",
			check: func() error {
				return validateWorkCompletion(1, 0, true)
			},
			wantErr: ErrWorkBlockersUnfinished,
		},
		{
			name: "handoff needs next action",
			check: func() error {
				_, err := validateCheckpointInput(WorkCheckpointInput{Summary: "paused", Result: "partial result"}, true)
				return err
			},
			wantErr: ErrEmptyContent,
		},
		{
			name: "evidence payload must be JSON",
			check: func() error {
				_, err := validateWorkEvidenceInput(WorkEvidenceInput{
					EvidenceType: "test",
					Summary:      "failed to parse",
					Payload:      json.RawMessage(`{"unterminated":`),
				})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.check()
			if err == nil {
				t.Fatal("validation accepted invalid state")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// Docker smoke contract: set STASH_TEST_POSTGRES_DSN to a disposable pgvector
// PostgreSQL database. This test must exercise prepared -> active -> checkpointed
// -> handed_off/expired -> active -> completed, reject every invalid transition
// below, and leave each rejected call without a partial state change.
func TestWorkExecutionStateMachinePostgres(t *testing.T) {
	b, ctx, namespaceID := newWorkExecutionTestBrain(t)

	t.Run("valid transitions", func(t *testing.T) {
		tests := []struct {
			name       string
			endAttempt func(*testing.T, *Brain, context.Context, *models.WorkAttemptLease) *models.WorkAttemptLease
		}{
			{
				name: "handoff",
				endAttempt: func(t *testing.T, b *Brain, ctx context.Context, first *models.WorkAttemptLease) *models.WorkAttemptLease {
					t.Helper()
					attempt, err := b.HandoffWorkAttempt(ctx, first.Attempt.ID, first.LeaseToken, WorkCheckpointInput{
						Summary: "first agent paused", Result: "checkpoint stored", NextAction: "continue verification",
					}, fmt.Sprintf("handoff-%d", first.Attempt.ID))
					if err != nil {
						t.Fatalf("HandoffWorkAttempt: %v", err)
					}
					if attempt.Status != "handed_off" {
						t.Fatalf("handoff status = %q, want handed_off", attempt.Status)
					}
					return startWorkExecutionAttempt(t, b, ctx, first.Attempt.WorkItemID, "agent-b")
				},
			},
			{
				name: "expired lease",
				endAttempt: func(t *testing.T, b *Brain, ctx context.Context, first *models.WorkAttemptLease) *models.WorkAttemptLease {
					t.Helper()
					if _, err := b.pool.Exec(ctx,
						`UPDATE work_attempts SET lease_expires_at = now() - interval '1 second' WHERE id = $1`,
						first.Attempt.ID,
					); err != nil {
						t.Fatalf("expire work attempt: %v", err)
					}
					second := startWorkExecutionAttempt(t, b, ctx, first.Attempt.WorkItemID, "agent-b")
					if status := workAttemptStatus(t, b, ctx, first.Attempt.ID); status != "expired" {
						t.Fatalf("expired attempt status = %q, want expired", status)
					}
					if _, err := b.CheckpointWorkAttempt(ctx, first.Attempt.ID, first.LeaseToken, WorkCheckpointInput{
						Summary: "expired token", Result: "must be rejected", NextAction: "use the replacement attempt",
					}, time.Minute, fmt.Sprintf("expired-token-checkpoint-%d", first.Attempt.ID)); !errors.Is(err, ErrWorkAttemptLease) {
						t.Fatalf("expired token checkpoint error = %v, want %v", err, ErrWorkAttemptLease)
					}
					assertWorkExecutionRowCount(t, b, ctx, 0,
						`SELECT count(*) FROM work_attempt_lease_tokens WHERE attempt_id = $1 AND revoked_at IS NULL`,
						first.Attempt.ID,
					)
					return second
				},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				item, condition := prepareWorkExecutionItem(t, b, ctx, namespaceID, "valid "+tt.name)
				first := startWorkExecutionAttempt(t, b, ctx, item.ID, "agent-a")
				checkpoint, err := b.CheckpointWorkAttempt(ctx, first.Attempt.ID, first.LeaseToken, WorkCheckpointInput{
					Summary: "implementation reached checkpoint", Result: "build passed", NextAction: "verify result",
				}, time.Minute, fmt.Sprintf("checkpoint-%d", first.Attempt.ID))
				if err != nil {
					t.Fatalf("CheckpointWorkAttempt: %v", err)
				}
				if checkpoint.Checkpoint.AttemptID != first.Attempt.ID {
					t.Fatalf("checkpoint attempt = %d, want %d", checkpoint.Checkpoint.AttemptID, first.Attempt.ID)
				}

				second := tt.endAttempt(t, b, ctx, first)
				evidence := submitWorkExecutionEvidence(t, b, ctx, second, []int64{condition.ID}, "completion proof", fmt.Sprintf("evidence-%d", second.Attempt.ID))
				verified, err := b.VerifyWorkCondition(ctx, second.Attempt.ID, second.LeaseToken, condition.ID, "passed", []int64{evidence.ID}, "", fmt.Sprintf("verify-%d", second.Attempt.ID))
				if err != nil {
					t.Fatalf("VerifyWorkCondition: %v", err)
				}
				if verified.Status != "passed" {
					t.Fatalf("condition status = %q, want passed", verified.Status)
				}
				completed, err := b.FinishWorkAttempt(ctx, second.Attempt.ID, second.LeaseToken, WorkFinishInput{
					Summary: "work finished", Result: "all required checks passed",
				}, fmt.Sprintf("finish-%d", second.Attempt.ID))
				if err != nil {
					t.Fatalf("FinishWorkAttempt: %v", err)
				}
				if completed.Status != "completed" {
					t.Fatalf("completed attempt status = %q, want completed", completed.Status)
				}
				assertWorkExecutionRowCount(t, b, ctx, 1,
					`SELECT count(*) FROM work_item_memory_links link
					 JOIN work_events event ON event.work_item_id = link.work_item_id
					  AND event.attempt_id = $2 AND event.event_type = 'work.memory.auto_saved'
					  AND event.payload->>'episode_id' = link.memory_id::text
					 WHERE link.work_item_id = $1 AND link.relation = 'result'`,
					item.ID, second.Attempt.ID,
				)

				bundle, err := b.GetWorkResumeBundle(ctx, item.ID, 100)
				if err != nil {
					t.Fatalf("GetWorkResumeBundle: %v", err)
				}
				if bundle.WorkItem.Status != "done" || bundle.LatestAttempt == nil || bundle.LatestAttempt.ID != second.Attempt.ID {
					t.Fatalf("completed bundle = %#v", bundle)
				}
			})
		}
	})

	t.Run("invalid transitions", func(t *testing.T) {
		t.Run("active attempt rejects competing and stale mutations", func(t *testing.T) {
			item, condition := prepareWorkExecutionItem(t, b, ctx, namespaceID, "invalid active mutations")
			attempt := startWorkExecutionAttempt(t, b, ctx, item.ID, "agent-a")

			if _, err := b.StartWorkAttempt(ctx, item.ID, "agent-b", nil, time.Minute, fmt.Sprintf("competing-start-%d", item.ID)); !errors.Is(err, ErrActiveWorkAttempt) {
				t.Fatalf("double claim error = %v, want %v", err, ErrActiveWorkAttempt)
			}
			if _, err := b.CheckpointWorkAttempt(ctx, attempt.Attempt.ID, "wrong-token", WorkCheckpointInput{
				Summary: "invalid", Result: "none", NextAction: "retry with the right token",
			}, time.Minute, fmt.Sprintf("wrong-token-%d", attempt.Attempt.ID)); !errors.Is(err, ErrWorkAttemptLease) {
				t.Fatalf("wrong token error = %v, want %v", err, ErrWorkAttemptLease)
			}
			if _, err := b.PrepareWork(ctx, item.ID, "replacement action", []CompletionConditionInput{{
				Kind: "test", Description: "replacement", Required: true,
				Verification: json.RawMessage(`{"command":"go test ./..."}`),
			}}, fmt.Sprintf("replacement-%d", item.ID)); !errors.Is(err, ErrActiveWorkAttempt) {
				t.Fatalf("condition replacement error = %v, want %v", err, ErrActiveWorkAttempt)
			}
			if _, err := b.VerifyWorkCondition(ctx, attempt.Attempt.ID, attempt.LeaseToken, condition.ID, "passed", nil, "", fmt.Sprintf("empty-evidence-%d", attempt.Attempt.ID)); err == nil || !strings.Contains(err.Error(), "requires evidence") {
				t.Fatalf("verification without evidence error = %v", err)
			}
			if _, err := b.FinishWorkAttempt(ctx, attempt.Attempt.ID, attempt.LeaseToken, WorkFinishInput{Summary: "too early", Result: "condition is pending"}, fmt.Sprintf("early-finish-%d", attempt.Attempt.ID)); !errors.Is(err, ErrWorkConditionsIncomplete) {
				t.Fatalf("pending condition completion error = %v, want %v", err, ErrWorkConditionsIncomplete)
			}
			assertWorkExecutionRowCount(t, b, ctx, 1, `SELECT count(*) FROM work_attempts WHERE work_item_id = $1 AND status = 'active'`, item.ID)
			assertWorkExecutionRowCount(t, b, ctx, 0, `SELECT count(*) FROM work_checkpoints WHERE attempt_id = $1`, attempt.Attempt.ID)
			assertWorkExecutionRowCount(t, b, ctx, 1, `SELECT count(*) FROM work_completion_conditions WHERE work_item_id = $1 AND superseded_at IS NULL AND id = $2 AND status = 'pending'`, item.ID, condition.ID)

			if _, err := b.HandoffWorkAttempt(ctx, attempt.Attempt.ID, attempt.LeaseToken, WorkCheckpointInput{
				Summary: "handoff", Result: "not finished", NextAction: "continue later",
			}, fmt.Sprintf("handoff-invalid-case-%d", attempt.Attempt.ID)); err != nil {
				t.Fatalf("HandoffWorkAttempt: %v", err)
			}
			if _, err := b.CheckpointWorkAttempt(ctx, attempt.Attempt.ID, attempt.LeaseToken, WorkCheckpointInput{
				Summary: "after handoff", Result: "must fail", NextAction: "none",
			}, time.Minute, fmt.Sprintf("checkpoint-after-handoff-%d", attempt.Attempt.ID)); !errors.Is(err, ErrWorkAttemptLease) {
				t.Fatalf("checkpoint after handoff error = %v, want %v", err, ErrWorkAttemptLease)
			}
			assertWorkExecutionRowCount(t, b, ctx, 1, `SELECT count(*) FROM work_checkpoints WHERE attempt_id = $1`, attempt.Attempt.ID)
		})

		t.Run("transaction start time cannot preserve an expired lease", func(t *testing.T) {
			item, _ := prepareWorkExecutionItem(t, b, ctx, namespaceID, "wall clock lease expiry")
			attempt, err := b.StartWorkAttempt(ctx, item.ID, "clock-agent", nil, 200*time.Millisecond, fmt.Sprintf("start-clock-%d", item.ID))
			if err != nil {
				t.Fatalf("StartWorkAttempt: %v", err)
			}

			tx, err := b.pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin pre-expiry transaction: %v", err)
			}
			defer tx.Rollback(ctx)
			var transactionStartedAt time.Time
			if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&transactionStartedAt); err != nil {
				t.Fatalf("pin transaction timestamp: %v", err)
			}
			if !transactionStartedAt.Before(attempt.Attempt.LeaseExpiresAt) {
				t.Fatalf("test transaction started at %s after lease deadline %s", transactionStartedAt, attempt.Attempt.LeaseExpiresAt)
			}
			if wait := time.Until(attempt.Attempt.LeaseExpiresAt) + 50*time.Millisecond; wait > 0 {
				time.Sleep(wait)
			}
			leased, err := lockWorkAttemptForAction(ctx, tx, attempt.Attempt.ID, attempt.LeaseToken)
			if err != nil {
				t.Fatalf("lockWorkAttemptForAction: %v", err)
			}
			if leased.LeaseValid {
				t.Fatal("transaction timestamp kept an expired lease valid")
			}
		})

		t.Run("manual status changes cannot bypass or override execution", func(t *testing.T) {
			item, _ := prepareWorkExecutionItem(t, b, ctx, namespaceID, "manual completion bypass")
			attempt := startWorkExecutionAttempt(t, b, ctx, item.ID, "status-agent")
			current, err := b.GetWorkItem(ctx, item.ID)
			if err != nil {
				t.Fatalf("GetWorkItem: %v", err)
			}
			input := workItemInputFromExisting(*current)
			input.Status = "ready"
			if _, err := b.UpdateWorkItemWithDetails(ctx, item.ID, input); err == nil || !strings.Contains(err.Error(), "active attempt") {
				t.Fatalf("public status override error = %v, want active-attempt enforcement", err)
			}
			if _, err := b.pool.Exec(ctx, `UPDATE work_items SET status = 'blocked' WHERE id = $1`, item.ID); err == nil || !strings.Contains(err.Error(), "active attempt") {
				t.Fatalf("direct status override error = %v, want active-attempt enforcement", err)
			}
			if current, err = b.GetWorkItem(ctx, item.ID); err != nil || current.Status != "doing" {
				t.Fatalf("rejected status changes changed item: item=%#v err=%v", current, err)
			}

			input = workItemInputFromExisting(*current)
			input.Status = "done"
			if _, err := b.UpdateWorkItemWithDetails(ctx, item.ID, input); err == nil || !strings.Contains(err.Error(), "active attempt") {
				t.Fatalf("manual completion bypass error = %v, want active-attempt enforcement", err)
			}
			if _, err := b.CheckpointWorkAttempt(ctx, attempt.Attempt.ID, attempt.LeaseToken, WorkCheckpointInput{
				Summary: "after rejected override", Result: "lease remains usable", NextAction: "handoff safely",
			}, time.Minute, fmt.Sprintf("checkpoint-status-guard-%d", attempt.Attempt.ID)); err != nil {
				t.Fatalf("checkpoint after rejected status override: %v", err)
			}
			if _, err := b.HandoffWorkAttempt(ctx, attempt.Attempt.ID, attempt.LeaseToken, WorkCheckpointInput{
				Summary: "status guard verified", Result: "out-of-band changes were rejected", NextAction: "continue later",
			}, fmt.Sprintf("handoff-status-guard-%d", attempt.Attempt.ID)); err != nil {
				t.Fatalf("handoff after status guard: %v", err)
			}
		})

		t.Run("verification rejects evidence linked to another condition", func(t *testing.T) {
			item := createWorkExecutionItem(t, b, ctx, namespaceID, "unlinked condition evidence")
			prepared, err := b.PrepareWork(ctx, item.ID, "verify both conditions", []CompletionConditionInput{
				{
					Kind: "test", Description: "first check", Required: true,
					Verification: json.RawMessage(`{"command":"go test ./first"}`),
				},
				{
					Kind: "test", Description: "second check", Required: true,
					Verification: json.RawMessage(`{"command":"go test ./second"}`),
				},
			}, fmt.Sprintf("prepare-unlinked-%d", item.ID))
			if err != nil {
				t.Fatalf("PrepareWork: %v", err)
			}
			attempt := startWorkExecutionAttempt(t, b, ctx, item.ID, "unlinked-agent")
			firstCondition := prepared.CompletionConditions[0]
			secondCondition := prepared.CompletionConditions[1]
			evidence := submitWorkExecutionEvidence(t, b, ctx, attempt, []int64{firstCondition.ID}, "first condition only", fmt.Sprintf("unlinked-evidence-%d", attempt.Attempt.ID))
			actionKey := fmt.Sprintf("verify-unlinked-%d", attempt.Attempt.ID)
			if _, err := b.VerifyWorkCondition(ctx, attempt.Attempt.ID, attempt.LeaseToken, secondCondition.ID, "passed", []int64{evidence.ID}, "", actionKey); !errors.Is(err, ErrWorkEvidenceNotFound) {
				t.Fatalf("unlinked evidence error = %v, want %v", err, ErrWorkEvidenceNotFound)
			}
			assertWorkExecutionRowCount(t, b, ctx, 1, `SELECT count(*) FROM work_completion_conditions WHERE id = $1 AND status = 'pending'`, secondCondition.ID)
			assertWorkExecutionRowCount(t, b, ctx, 0, `SELECT count(*) FROM work_action_receipts WHERE work_item_id = $1 AND action_key = $2`, item.ID, workActionKeyDigest(actionKey))
		})

		t.Run("condition and evidence cannot cross work items", func(t *testing.T) {
			itemA, conditionA := prepareWorkExecutionItem(t, b, ctx, namespaceID, "cross item a")
			itemB, conditionB := prepareWorkExecutionItem(t, b, ctx, namespaceID, "cross item b")
			attemptA := startWorkExecutionAttempt(t, b, ctx, itemA.ID, "agent-a")
			attemptB := startWorkExecutionAttempt(t, b, ctx, itemB.ID, "agent-b")
			evidenceA := submitWorkExecutionEvidence(t, b, ctx, attemptA, []int64{conditionA.ID}, "evidence a", fmt.Sprintf("evidence-a-%d", attemptA.Attempt.ID))
			evidenceB := submitWorkExecutionEvidence(t, b, ctx, attemptB, []int64{conditionB.ID}, "evidence b", fmt.Sprintf("evidence-b-%d", attemptB.Attempt.ID))
			if _, err := b.pool.Exec(ctx,
				`INSERT INTO work_condition_evidence
				    (condition_id, evidence_id, work_item_id, linked_by_attempt_id)
				 VALUES ($1, $2, $3, $4)`,
				conditionA.ID, evidenceB.ID, itemA.ID, attemptA.Attempt.ID,
			); err == nil {
				t.Fatal("database accepted evidence from another work item")
			}

			if _, err := b.VerifyWorkCondition(ctx, attemptA.Attempt.ID, attemptA.LeaseToken, conditionB.ID, "passed", []int64{evidenceA.ID}, "", fmt.Sprintf("foreign-condition-%d", attemptA.Attempt.ID)); !errors.Is(err, ErrWorkConditionNotFound) {
				t.Fatalf("foreign condition error = %v, want %v", err, ErrWorkConditionNotFound)
			}
			if _, err := b.VerifyWorkCondition(ctx, attemptA.Attempt.ID, attemptA.LeaseToken, conditionA.ID, "passed", []int64{evidenceB.ID}, "", fmt.Sprintf("foreign-evidence-%d", attemptA.Attempt.ID)); !errors.Is(err, ErrWorkEvidenceNotFound) {
				t.Fatalf("foreign evidence error = %v, want %v", err, ErrWorkEvidenceNotFound)
			}
			assertWorkExecutionRowCount(t, b, ctx, 1, `SELECT count(*) FROM work_completion_conditions WHERE id = $1 AND status = 'pending'`, conditionA.ID)
			assertWorkExecutionRowCount(t, b, ctx, 1, `SELECT count(*) FROM work_completion_conditions WHERE id = $1 AND status = 'pending'`, conditionB.ID)
		})

		t.Run("resume bundle excludes foreign namespace links", func(t *testing.T) {
			item, _ := prepareWorkExecutionItem(t, b, ctx, namespaceID, "namespace isolated resume")
			const localMemoryContent = "authorized memory snapshot"
			var localEpisodeID int64
			if err := b.pool.QueryRow(ctx,
				`INSERT INTO episodes (namespace_id, content) VALUES ($1, $2) RETURNING id`,
				namespaceID, localMemoryContent,
			).Scan(&localEpisodeID); err != nil {
				t.Fatalf("create local episode: %v", err)
			}
			if _, err := b.LinkWorkItemMemory(ctx, item.ID, "episode", localEpisodeID, "context"); err != nil {
				t.Fatalf("link local memory: %v", err)
			}
			foreignSlug := fmt.Sprintf("/tests/work-execution-foreign-%d", time.Now().UnixNano())
			foreignNamespaceID, err := b.CreateNamespace(ctx, foreignSlug, "foreign execution test", "")
			if err != nil {
				t.Fatalf("CreateNamespace foreign: %v", err)
			}
			defer func() {
				_, _ = b.pool.Exec(context.Background(), `DELETE FROM namespaces WHERE id = $1`, foreignNamespaceID)
			}()

			foreignTree, err := b.RegisterWorktree(ctx, foreignNamespaceID, "foreign", "/tmp/foreign-worktree", "foreign", "deadbeef", "clean", "foreign-agent", json.RawMessage(`{}`))
			if err != nil {
				t.Fatalf("RegisterWorktree foreign: %v", err)
			}
			foreignBlocker := createWorkExecutionItem(t, b, ctx, foreignNamespaceID, "foreign blocker")
			var foreignEpisodeID int64
			if err := b.pool.QueryRow(ctx,
				`INSERT INTO episodes (namespace_id, content) VALUES ($1, 'foreign memory') RETURNING id`,
				foreignNamespaceID,
			).Scan(&foreignEpisodeID); err != nil {
				t.Fatalf("create foreign episode: %v", err)
			}
			if _, err := b.pool.Exec(ctx,
				`INSERT INTO work_item_worktrees (work_item_id, worktree_id, relation) VALUES ($1, $2, 'related')`,
				item.ID, foreignTree.ID,
			); err != nil {
				t.Fatalf("inject foreign worktree link: %v", err)
			}
			if _, err := b.pool.Exec(ctx,
				`INSERT INTO work_item_edges (namespace_id, from_item_id, to_item_id, edge_type) VALUES ($1, $2, $3, 'blocks')`,
				foreignNamespaceID, foreignBlocker.ID, item.ID,
			); err != nil {
				t.Fatalf("inject foreign blocker link: %v", err)
			}
			if _, err := b.pool.Exec(ctx,
				`INSERT INTO work_item_memory_links (work_item_id, memory_type, memory_id, relation) VALUES ($1, 'episode', $2, 'context')`,
				item.ID, foreignEpisodeID,
			); err != nil {
				t.Fatalf("inject foreign memory link: %v", err)
			}
			if _, err := b.pool.Exec(ctx,
				`INSERT INTO work_events (namespace_id, work_item_id, event_type, payload) VALUES ($1, $2, 'foreign.event', '{}')`,
				foreignNamespaceID, item.ID,
			); err != nil {
				t.Fatalf("inject foreign work event: %v", err)
			}

			bundle, err := b.GetWorkResumeBundle(ctx, item.ID, 100)
			if err != nil {
				t.Fatalf("GetWorkResumeBundle: %v", err)
			}
			if len(bundle.WorkItem.WorktreeIDs) != 0 || len(bundle.WorktreeLinks) != 0 || len(bundle.MemoryLinks) != 1 || len(bundle.Blockers) != 0 {
				t.Fatalf("resume bundle exposed foreign links: worktree_ids=%v worktrees=%v memories=%v blockers=%v",
					bundle.WorkItem.WorktreeIDs, bundle.WorktreeLinks, bundle.MemoryLinks, bundle.Blockers)
			}
			memory := bundle.MemoryLinks[0]
			if memory.Content != localMemoryContent || memory.Status != "recorded" || memory.Relation != "context" || bundle.Totals.MemoryLinks != 1 {
				t.Fatalf("authorized memory snapshot = %#v totals=%#v", memory, bundle.Totals)
			}
			encoded, err := json.Marshal(bundle)
			if err != nil {
				t.Fatalf("marshal resume bundle: %v", err)
			}
			if bytes.Contains(encoded, []byte("foreign memory")) {
				t.Fatalf("resume bundle exposed foreign memory content: %s", encoded)
			}
			for _, event := range bundle.RecentEvents {
				if event.EventType == "foreign.event" || event.NamespaceID != namespaceID {
					t.Fatalf("resume bundle exposed foreign event: %#v", event)
				}
			}
		})

		t.Run("resume bundle includes bounded content for every memory type", func(t *testing.T) {
			item := createWorkExecutionItem(t, b, ctx, namespaceID, "all resume memory types")
			type memoryExpectation struct {
				id      int64
				content string
				status  string
			}
			expected := map[string]memoryExpectation{
				"episode":    {content: "episode snapshot", status: "recorded"},
				"fact":       {content: "fact snapshot", status: "active"},
				"hypothesis": {content: "hypothesis snapshot", status: "testing"},
				"failure":    {content: "failure snapshot", status: "recorded"},
				"goal":       {content: "goal snapshot", status: "active"},
			}
			queries := map[string]string{
				"episode":    `INSERT INTO episodes (namespace_id, content) VALUES ($1, $2) RETURNING id`,
				"fact":       `INSERT INTO facts (namespace_id, content) VALUES ($1, $2) RETURNING id`,
				"hypothesis": `INSERT INTO hypotheses (namespace_id, content, status) VALUES ($1, $2, 'testing') RETURNING id`,
				"failure":    `INSERT INTO failures (namespace_id, content) VALUES ($1, $2) RETURNING id`,
				"goal":       `INSERT INTO goals (namespace_id, content, status) VALUES ($1, $2, 'active') RETURNING id`,
			}
			for memoryType, expectation := range expected {
				if err := b.pool.QueryRow(ctx, queries[memoryType], namespaceID, expectation.content).Scan(&expectation.id); err != nil {
					t.Fatalf("create %s memory: %v", memoryType, err)
				}
				expected[memoryType] = expectation
				if _, err := b.LinkWorkItemMemory(ctx, item.ID, memoryType, expectation.id, "context"); err != nil {
					t.Fatalf("link %s memory: %v", memoryType, err)
				}
			}

			bundle, err := b.GetWorkResumeBundle(ctx, item.ID, 20)
			if err != nil {
				t.Fatalf("GetWorkResumeBundle all memory types: %v", err)
			}
			if len(bundle.MemoryLinks) != len(expected) || bundle.Totals.MemoryLinks != len(expected) || bundle.Truncated.MemoryLinks {
				t.Fatalf("all memory snapshots = %#v totals=%#v truncated=%#v", bundle.MemoryLinks, bundle.Totals, bundle.Truncated)
			}
			for _, snapshot := range bundle.MemoryLinks {
				expectation, ok := expected[snapshot.MemoryType]
				if !ok || snapshot.MemoryID != expectation.id || snapshot.Content != expectation.content || snapshot.Status != expectation.status || snapshot.Relation != "context" {
					t.Fatalf("unexpected %s snapshot: %#v, want %#v", snapshot.MemoryType, snapshot, expectation)
				}
			}
		})

		t.Run("unfinished blocker prevents completion", func(t *testing.T) {
			target, condition := prepareWorkExecutionItem(t, b, ctx, namespaceID, "blocked target")
			attempt := startWorkExecutionAttempt(t, b, ctx, target.ID, "agent-a")
			evidence := submitWorkExecutionEvidence(t, b, ctx, attempt, []int64{condition.ID}, "target evidence", fmt.Sprintf("blocker-evidence-%d", attempt.Attempt.ID))
			if _, err := b.VerifyWorkCondition(ctx, attempt.Attempt.ID, attempt.LeaseToken, condition.ID, "passed", []int64{evidence.ID}, "", fmt.Sprintf("blocker-verify-%d", attempt.Attempt.ID)); err != nil {
				t.Fatalf("VerifyWorkCondition: %v", err)
			}
			blocker := createWorkExecutionItem(t, b, ctx, namespaceID, "unfinished blocker")
			if _, err := b.AddWorkItemEdge(ctx, namespaceID, blocker.ID, target.ID, "blocks"); err != nil {
				t.Fatalf("AddWorkItemEdge: %v", err)
			}
			if _, err := b.FinishWorkAttempt(ctx, attempt.Attempt.ID, attempt.LeaseToken, WorkFinishInput{Summary: "blocked finish", Result: "blocker remains"}, fmt.Sprintf("blocked-finish-%d", attempt.Attempt.ID)); !errors.Is(err, ErrWorkBlockersUnfinished) {
				t.Fatalf("blocked completion error = %v, want %v", err, ErrWorkBlockersUnfinished)
			}
			assertWorkExecutionRowCount(t, b, ctx, 0, `SELECT count(*) FROM work_checkpoints WHERE attempt_id = $1`, attempt.Attempt.ID)
			assertWorkExecutionRowCount(t, b, ctx, 1, `SELECT count(*) FROM work_attempts WHERE id = $1 AND status = 'active'`, attempt.Attempt.ID)
		})

		t.Run("graph update and completion use one lock order", func(t *testing.T) {
			target, condition := prepareWorkExecutionItem(t, b, ctx, namespaceID, "concurrent blocker target")
			attempt := startWorkExecutionAttempt(t, b, ctx, target.ID, "graph-lock-agent")
			evidence := submitWorkExecutionEvidence(t, b, ctx, attempt, []int64{condition.ID}, "ready before blocker", fmt.Sprintf("graph-lock-evidence-%d", attempt.Attempt.ID))
			if _, err := b.VerifyWorkCondition(ctx, attempt.Attempt.ID, attempt.LeaseToken, condition.ID, "passed", []int64{evidence.ID}, "", fmt.Sprintf("graph-lock-verify-%d", attempt.Attempt.ID)); err != nil {
				t.Fatalf("VerifyWorkCondition: %v", err)
			}
			blocker := createWorkExecutionItem(t, b, ctx, namespaceID, "concurrent blocker")

			graphTx, err := b.pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin graph transaction: %v", err)
			}
			defer graphTx.Rollback(ctx)
			if err := lockWorkGraph(ctx, graphTx, namespaceID); err != nil {
				t.Fatalf("lock graph transaction: %v", err)
			}

			finishResult := make(chan error, 1)
			go func() {
				_, finishErr := b.FinishWorkAttempt(ctx, attempt.Attempt.ID, attempt.LeaseToken, WorkFinishInput{
					Summary: "concurrent finish", Result: "must observe the blocker",
				}, fmt.Sprintf("graph-lock-finish-%d", attempt.Attempt.ID))
				finishResult <- finishErr
			}()
			time.Sleep(50 * time.Millisecond)
			if _, err := graphTx.Exec(ctx,
				`INSERT INTO work_item_edges (namespace_id, from_item_id, to_item_id, edge_type)
				 VALUES ($1, $2, $3, 'blocks')`,
				namespaceID, blocker.ID, target.ID,
			); err != nil {
				t.Fatalf("insert blocker while completion waits: %v", err)
			}
			if err := graphTx.Commit(ctx); err != nil {
				t.Fatalf("commit graph transaction: %v", err)
			}

			select {
			case err := <-finishResult:
				if !errors.Is(err, ErrWorkBlockersUnfinished) {
					t.Fatalf("concurrent finish error = %v, want %v", err, ErrWorkBlockersUnfinished)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("concurrent finish did not resume after graph commit")
			}
			assertWorkExecutionRowCount(t, b, ctx, 1, `SELECT count(*) FROM work_attempts WHERE id = $1 AND status = 'active'`, attempt.Attempt.ID)
		})
	})

	t.Run("principal-bound concurrent start replays and evidence provenance", func(t *testing.T) {
		item, condition := prepareWorkExecutionItem(t, b, ctx, namespaceID, "principal-bound replay")
		worktree, err := b.RegisterWorktree(ctx, namespaceID, "stash", fmt.Sprintf("/tmp/principal-%d", item.ID), "principal-test", "feedface", "clean", "agent-a", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("RegisterWorktree: %v", err)
		}
		const (
			rawPrincipalID   = "oidc-subject-a"
			principalID      = "u_11111111111111111111111111111111"
			otherPrincipalID = "u_22222222222222222222222222222222"
		)
		actionKey := fmt.Sprintf("concurrent-start-replay-%d-principal-a", item.ID)
		type startResult struct {
			lease *models.WorkAttemptLease
			err   error
		}
		gate := make(chan struct{})
		results := make(chan startResult, 2)
		for range 2 {
			go func() {
				<-gate
				lease, startErr := b.StartWorkAttemptForPrincipal(ctx, item.ID, "agent-a", principalID, &worktree.ID, time.Minute, actionKey)
				results <- startResult{lease: lease, err: startErr}
			}()
		}
		close(gate)
		first := <-results
		second := <-results
		if first.err != nil || second.err != nil {
			t.Fatalf("concurrent start results = %v / %v", first.err, second.err)
		}
		if first.lease.Attempt.ID != second.lease.Attempt.ID || first.lease.LeaseToken == second.lease.LeaseToken {
			t.Fatalf("concurrent start leases = %#v / %#v", first.lease, second.lease)
		}
		if first.lease.Attempt.PrincipalID != principalID || second.lease.Attempt.PrincipalID != principalID {
			t.Fatalf("attempt principals = %q / %q", first.lease.Attempt.PrincipalID, second.lease.Attempt.PrincipalID)
		}
		if _, err := b.StartWorkAttemptForPrincipal(ctx, item.ID, "agent-with-different-payload", principalID, &worktree.ID, time.Minute, actionKey); !errors.Is(err, ErrWorkActionConflict) {
			t.Fatalf("changed start replay error = %v, want %v", err, ErrWorkActionConflict)
		}
		assertWorkExecutionRowCount(t, b, ctx, 2,
			`SELECT count(*) FROM work_attempt_lease_tokens WHERE attempt_id = $1 AND revoked_at IS NULL`,
			first.lease.Attempt.ID,
		)

		checkpointInput := WorkCheckpointInput{Summary: "first response token", Result: "accepted", NextAction: "submit evidence"}
		if _, err := b.CheckpointWorkAttemptForPrincipal(ctx, first.lease.Attempt.ID, first.lease.LeaseToken, otherPrincipalID, checkpointInput, time.Minute, fmt.Sprintf("wrong-principal-%d", item.ID)); !errors.Is(err, ErrWorkAttemptLease) {
			t.Fatalf("wrong principal checkpoint error = %v, want %v", err, ErrWorkAttemptLease)
		}
		if _, err := b.CheckpointWorkAttemptForPrincipal(ctx, first.lease.Attempt.ID, first.lease.LeaseToken, principalID, checkpointInput, time.Minute, fmt.Sprintf("principal-checkpoint-%d", item.ID)); err != nil {
			t.Fatalf("first concurrent token checkpoint: %v", err)
		}

		evidenceInput := WorkEvidenceInput{
			EvidenceType: "test", Summary: "second response token remained valid", Reference: "principal test",
			Payload: json.RawMessage(`{"passed":true}`),
		}
		evidence, err := b.SubmitWorkEvidenceForPrincipal(ctx, second.lease.Attempt.ID, second.lease.LeaseToken, principalID, evidenceInput, []int64{condition.ID}, fmt.Sprintf("principal-evidence-%d", item.ID))
		if err != nil {
			t.Fatalf("second concurrent token evidence: %v", err)
		}
		wantDigest, err := workEvidenceContentDigest(evidenceInput)
		if err != nil {
			t.Fatalf("workEvidenceContentDigest: %v", err)
		}
		if evidence.PrincipalID != principalID || evidence.WorktreeHeadSHA != "feedface" || evidence.ContentDigest != wantDigest || evidence.SubmittedAt.IsZero() {
			t.Fatalf("evidence provenance = %#v, want principal=%q head=feedface digest=%q", evidence, principalID, wantDigest)
		}
		if _, err := b.pool.Exec(ctx,
			`INSERT INTO work_evidence
			    (work_item_id, attempt_id, evidence_type, summary, payload, content_digest, principal_id)
			 VALUES ($1, $2, 'test', 'forged principal', '{}', $3, $4)`,
			item.ID, first.lease.Attempt.ID, "sha256:"+strings.Repeat("0", 64), otherPrincipalID,
		); err == nil {
			t.Fatal("database accepted evidence with a principal different from its attempt")
		}
		if _, err := b.pool.Exec(ctx,
			`INSERT INTO work_action_receipts (work_item_id, action_key, action_type, request_hash)
			 VALUES ($1, 'raw-action-key', 'test', $2)`,
			item.ID, bytes.Repeat([]byte{1}, sha256.Size),
		); err == nil {
			t.Fatal("database accepted a raw action key")
		}

		handoffInput := WorkCheckpointInput{Summary: "principal handoff", Result: "both tokens accepted", NextAction: "continue later"}
		handedOff, err := b.HandoffWorkAttemptForPrincipal(ctx, first.lease.Attempt.ID, first.lease.LeaseToken, principalID, handoffInput, fmt.Sprintf("principal-handoff-%d", item.ID))
		if err != nil {
			t.Fatalf("HandoffWorkAttemptForPrincipal: %v", err)
		}
		if replay, err := b.HandoffWorkAttemptForPrincipal(ctx, second.lease.Attempt.ID, second.lease.LeaseToken, principalID, handoffInput, fmt.Sprintf("principal-handoff-%d", item.ID)); err != nil || replay.ID != handedOff.ID {
			t.Fatalf("handoff replay through second token = %#v, %v", replay, err)
		}
		if _, err := b.HandoffWorkAttemptForPrincipal(ctx, second.lease.Attempt.ID, second.lease.LeaseToken, otherPrincipalID, handoffInput, fmt.Sprintf("principal-handoff-%d", item.ID)); !errors.Is(err, ErrWorkAttemptLease) {
			t.Fatalf("wrong principal handoff replay error = %v, want %v", err, ErrWorkAttemptLease)
		}
		assertWorkExecutionRowCount(t, b, ctx, 0,
			`SELECT count(*) FROM work_attempt_lease_tokens WHERE attempt_id = $1 AND revoked_at IS NULL`,
			first.lease.Attempt.ID,
		)
		bundle, err := b.GetWorkResumeBundle(ctx, item.ID, 100)
		if err != nil {
			t.Fatalf("GetWorkResumeBundle principal provenance: %v", err)
		}
		encoded, err := json.Marshal(bundle)
		if err != nil {
			t.Fatalf("marshal principal provenance: %v", err)
		}
		if bytes.Contains(encoded, []byte(rawPrincipalID)) || !bytes.Contains(encoded, []byte(principalID)) {
			t.Fatalf("principal provenance exposed raw subject or lost stable key: %s", encoded)
		}
	})

	t.Run("completed attempt cannot be reused after reopening", func(t *testing.T) {
		item, condition := prepareWorkExecutionItem(t, b, ctx, namespaceID, "reopened completion")
		first := startWorkExecutionAttempt(t, b, ctx, item.ID, "first-completer")
		evidence := submitWorkExecutionEvidence(t, b, ctx, first, []int64{condition.ID}, "first completion evidence", fmt.Sprintf("first-completion-evidence-%d", item.ID))
		if _, err := b.VerifyWorkCondition(ctx, first.Attempt.ID, first.LeaseToken, condition.ID, "passed", []int64{evidence.ID}, "", fmt.Sprintf("first-completion-verify-%d", item.ID)); err != nil {
			t.Fatalf("first VerifyWorkCondition: %v", err)
		}
		completed, err := b.FinishWorkAttempt(ctx, first.Attempt.ID, first.LeaseToken, WorkFinishInput{Summary: "first completion", Result: "done"}, fmt.Sprintf("first-completion-finish-%d", item.ID))
		if err != nil {
			t.Fatalf("first FinishWorkAttempt: %v", err)
		}
		if _, err := b.pool.Exec(ctx, `UPDATE work_items SET status = 'ready', completed_at = NULL, updated_at = now() WHERE id = $1`, item.ID); err != nil {
			t.Fatalf("reopen completed work: %v", err)
		}
		var requiredAfter time.Time
		var requiredAttemptNumber int
		if err := b.pool.QueryRow(ctx,
			`SELECT completion_required_after, completion_required_attempt_number FROM work_execution_states WHERE work_item_id = $1`,
			item.ID,
		).Scan(&requiredAfter, &requiredAttemptNumber); err != nil {
			t.Fatalf("read completion generation: %v", err)
		}
		if completed.EndedAt == nil || !requiredAfter.After(*completed.EndedAt) || requiredAttemptNumber != completed.AttemptNumber {
			t.Fatalf("completion generation = %s / %d, first = ended %v / attempt %d", requiredAfter, requiredAttemptNumber, completed.EndedAt, completed.AttemptNumber)
		}
		if _, err := b.pool.Exec(ctx, `UPDATE work_items SET status = 'done', completed_at = now(), updated_at = now() WHERE id = $1`, item.ID); err == nil || !strings.Contains(err.Error(), "finish_work") {
			t.Fatalf("old completed attempt reuse error = %v, want finish_work enforcement", err)
		}
		if current, err := b.GetWorkItem(ctx, item.ID); err != nil || current.Status != "ready" {
			t.Fatalf("rejected old completion changed work: %#v, %v", current, err)
		}

		prepared, err := b.PrepareWork(ctx, item.ID, "complete a new attempt", []CompletionConditionInput{testCompletionCondition("new attempt verifies completion")}, fmt.Sprintf("reopen-prepare-%d", item.ID))
		if err != nil {
			t.Fatalf("PrepareWork after reopen: %v", err)
		}
		second := startWorkExecutionAttempt(t, b, ctx, item.ID, "second-completer")
		secondEvidence := submitWorkExecutionEvidence(t, b, ctx, second, []int64{prepared.CompletionConditions[0].ID}, "new completion evidence", fmt.Sprintf("reopen-evidence-%d", item.ID))
		if _, err := b.VerifyWorkCondition(ctx, second.Attempt.ID, second.LeaseToken, prepared.CompletionConditions[0].ID, "passed", []int64{secondEvidence.ID}, "", fmt.Sprintf("reopen-verify-%d", item.ID)); err != nil {
			t.Fatalf("second VerifyWorkCondition: %v", err)
		}
		if secondCompleted, err := b.FinishWorkAttempt(ctx, second.Attempt.ID, second.LeaseToken, WorkFinishInput{Summary: "second completion", Result: "new attempt done"}, fmt.Sprintf("reopen-finish-%d", item.ID)); err != nil || secondCompleted.Status != "completed" {
			t.Fatalf("second FinishWorkAttempt = %#v, %v", secondCompleted, err)
		}
	})

	t.Run("active attempt prevents work item deletion", func(t *testing.T) {
		item, _ := prepareWorkExecutionItem(t, b, ctx, namespaceID, "active delete protection")
		attempt := startWorkExecutionAttempt(t, b, ctx, item.ID, "delete-agent")
		if err := b.DeleteWorkItem(ctx, item.ID); err == nil || !strings.Contains(err.Error(), "active attempt") {
			t.Fatalf("active work soft deletion error = %v", err)
		}
		if _, err := b.pool.Exec(ctx, `DELETE FROM work_items WHERE id = $1`, item.ID); err == nil || !strings.Contains(err.Error(), "active attempt") {
			t.Fatalf("active work deletion error = %v", err)
		}
		if _, err := b.HandoffWorkAttempt(ctx, attempt.Attempt.ID, attempt.LeaseToken, WorkCheckpointInput{Summary: "release before delete", Result: "not needed", NextAction: "delete safely"}, fmt.Sprintf("delete-handoff-%d", item.ID)); err != nil {
			t.Fatalf("handoff before delete: %v", err)
		}
		if err := b.DeleteWorkItem(ctx, item.ID); err != nil {
			t.Fatalf("soft-delete handed-off work: %v", err)
		}

		parent := createWorkExecutionItem(t, b, ctx, namespaceID, "delete parent with active child")
		parentID := parent.ID
		child, err := b.CreateWorkItem(ctx, namespaceID, nil, &parentID, "active child", "recursive delete test", "ready", 1, 0, "", nil)
		if err != nil {
			t.Fatalf("CreateWorkItem child: %v", err)
		}
		if _, err := b.PrepareWork(ctx, child.ID, "protect the child lease", []CompletionConditionInput{testCompletionCondition("child remains visible")}, fmt.Sprintf("prepare-delete-child-%d", child.ID)); err != nil {
			t.Fatalf("PrepareWork child: %v", err)
		}
		childAttempt := startWorkExecutionAttempt(t, b, ctx, child.ID, "child-delete-agent")
		if err := b.DeleteWorkItem(ctx, parent.ID); err == nil || !strings.Contains(err.Error(), "active attempt") {
			t.Fatalf("recursive deletion with active child error = %v", err)
		}
		assertWorkExecutionRowCount(t, b, ctx, 2,
			`SELECT count(*) FROM work_items WHERE id = ANY($1) AND deleted_at IS NULL`,
			[]int64{parent.ID, child.ID},
		)
		if _, err := b.HandoffWorkAttempt(ctx, childAttempt.Attempt.ID, childAttempt.LeaseToken, WorkCheckpointInput{
			Summary: "release child before recursive delete", Result: "delete guard verified", NextAction: "delete parent safely",
		}, fmt.Sprintf("delete-child-handoff-%d", child.ID)); err != nil {
			t.Fatalf("handoff child before recursive delete: %v", err)
		}
		if err := b.DeleteWorkItem(ctx, parent.ID); err != nil {
			t.Fatalf("recursive soft-delete after child handoff: %v", err)
		}
		assertWorkExecutionRowCount(t, b, ctx, 2,
			`SELECT count(*) FROM work_items WHERE id = ANY($1) AND deleted_at IS NOT NULL`,
			[]int64{parent.ID, child.ID},
		)
	})

	t.Run("resume collections are bounded with exact totals", func(t *testing.T) {
		item := createWorkExecutionItem(t, b, ctx, namespaceID, "bounded resume collections")
		inputs := []CompletionConditionInput{
			testCompletionCondition("bounded condition one"),
			testCompletionCondition("bounded condition two"),
			testCompletionCondition("bounded condition three"),
		}
		prepared, err := b.PrepareWork(ctx, item.ID, "populate bounded collections", inputs, fmt.Sprintf("bounded-prepare-%d", item.ID))
		if err != nil {
			t.Fatalf("PrepareWork: %v", err)
		}
		attempt := startWorkExecutionAttempt(t, b, ctx, item.ID, "bounded-agent")
		for i := 0; i < 3; i++ {
			submitWorkExecutionEvidence(t, b, ctx, attempt, []int64{prepared.CompletionConditions[0].ID}, fmt.Sprintf("bounded evidence %d", i), fmt.Sprintf("bounded-evidence-%d-%d", item.ID, i))
			worktree, err := b.RegisterWorktree(ctx, namespaceID, "stash", fmt.Sprintf("/tmp/bounded-%d-%d", item.ID, i), fmt.Sprintf("bounded-%d", i), fmt.Sprintf("head-%d", i), "clean", "bounded-agent", json.RawMessage(`{}`))
			if err != nil {
				t.Fatalf("RegisterWorktree %d: %v", i, err)
			}
			if err := b.AttachWorktreeToItem(ctx, item.ID, worktree.ID, "related"); err != nil {
				t.Fatalf("AttachWorktreeToItem %d: %v", i, err)
			}
			memoryContent := fmt.Sprintf("bounded memory %d", i)
			if i == 2 {
				memoryContent = strings.Repeat("m", resumeMemoryContentLimit+128)
			}
			var episodeID int64
			if err := b.pool.QueryRow(ctx, `INSERT INTO episodes (namespace_id, content) VALUES ($1, $2) RETURNING id`, namespaceID, memoryContent).Scan(&episodeID); err != nil {
				t.Fatalf("create episode %d: %v", i, err)
			}
			if _, err := b.LinkWorkItemMemory(ctx, item.ID, "episode", episodeID, "context"); err != nil {
				t.Fatalf("LinkWorkItemMemory %d: %v", i, err)
			}
		}
		for i := 0; i < 3; i++ {
			blocker := createWorkExecutionItem(t, b, ctx, namespaceID, fmt.Sprintf("bounded blocker %d", i))
			if _, err := b.AddWorkItemEdge(ctx, namespaceID, blocker.ID, item.ID, "blocks"); err != nil {
				t.Fatalf("AddWorkItemEdge %d: %v", i, err)
			}
		}

		previousMax := b.config.MaxResultSize
		b.config.MaxResultSize = 2
		defer func() { b.config.MaxResultSize = previousMax }()
		bundle, err := b.GetWorkResumeBundle(ctx, item.ID, 10)
		if err != nil {
			t.Fatalf("GetWorkResumeBundle: %v", err)
		}
		if len(bundle.CompletionConditions) != 2 || len(bundle.Evidence) != 2 || len(bundle.WorktreeLinks) != 2 || len(bundle.WorkItem.WorktreeIDs) != 2 || len(bundle.MemoryLinks) != 2 || len(bundle.Blockers) != 2 || len(bundle.RecentEvents) != 2 {
			t.Fatalf("bounded resume sizes = conditions:%d evidence:%d worktrees:%d ids:%d memories:%d blockers:%d events:%d",
				len(bundle.CompletionConditions), len(bundle.Evidence), len(bundle.WorktreeLinks), len(bundle.WorkItem.WorktreeIDs), len(bundle.MemoryLinks), len(bundle.Blockers), len(bundle.RecentEvents))
		}
		if bundle.Totals.CompletionConditions != 3 || bundle.Totals.Evidence != 3 || bundle.Totals.WorktreeLinks != 3 || bundle.Totals.MemoryLinks != 3 || bundle.Totals.Blockers != 3 || bundle.Totals.RecentEvents <= 2 {
			t.Fatalf("resume totals = %#v", bundle.Totals)
		}
		if !bundle.Truncated.CompletionConditions || !bundle.Truncated.Evidence || !bundle.Truncated.WorktreeLinks || !bundle.Truncated.MemoryLinks || !bundle.Truncated.Blockers || !bundle.Truncated.RecentEvents {
			t.Fatalf("resume truncation flags = %#v", bundle.Truncated)
		}
		memoryContentWasTruncated := false
		for _, memory := range bundle.MemoryLinks {
			if len(memory.Content) > resumeMemoryContentLimit {
				t.Fatalf("memory snapshot content length = %d, limit = %d", len(memory.Content), resumeMemoryContentLimit)
			}
			memoryContentWasTruncated = memoryContentWasTruncated || memory.ContentTruncated
		}
		if !memoryContentWasTruncated {
			t.Fatalf("bounded resume did not report truncated memory content: %#v", bundle.MemoryLinks)
		}
	})

	t.Run("replayed action keys", func(t *testing.T) {
		tests := []struct {
			name string
			run  func(*testing.T, *Brain, context.Context, int64)
		}{
			{
				name: "checkpoint",
				run: func(t *testing.T, b *Brain, ctx context.Context, namespaceID int64) {
					item, _ := prepareWorkExecutionItem(t, b, ctx, namespaceID, "replay checkpoint")
					attempt := startWorkExecutionAttempt(t, b, ctx, item.ID, "checkpoint-agent")
					input := WorkCheckpointInput{Summary: "checkpoint", Result: "build passed", NextAction: "run verification"}
					actionKey := fmt.Sprintf("replay-checkpoint-%d", attempt.Attempt.ID)
					first, err := b.CheckpointWorkAttempt(ctx, attempt.Attempt.ID, attempt.LeaseToken, input, time.Minute, actionKey)
					if err != nil {
						t.Fatalf("first CheckpointWorkAttempt: %v", err)
					}
					second, err := b.CheckpointWorkAttempt(ctx, attempt.Attempt.ID, attempt.LeaseToken, input, time.Minute, actionKey)
					if err != nil {
						t.Fatalf("replayed CheckpointWorkAttempt: %v", err)
					}
					if first.Checkpoint.ID != second.Checkpoint.ID || !first.LeaseExpiresAt.Equal(second.LeaseExpiresAt) {
						t.Fatalf("checkpoint replay changed result: first=%#v second=%#v", first, second)
					}
					assertWorkExecutionRowCount(t, b, ctx, 1, `SELECT count(*) FROM work_checkpoints WHERE attempt_id = $1`, attempt.Attempt.ID)
					assertWorkActionReplay(t, b, ctx, item.ID, attempt.Attempt.ID, actionKey, "work.attempt.checkpointed")

					changed := input
					changed.Result = "different result"
					if _, err := b.CheckpointWorkAttempt(ctx, attempt.Attempt.ID, attempt.LeaseToken, changed, time.Minute, actionKey); !errors.Is(err, ErrWorkActionConflict) {
						t.Fatalf("changed replay error = %v, want %v", err, ErrWorkActionConflict)
					}
					assertWorkExecutionRowCount(t, b, ctx, 1, `SELECT count(*) FROM work_checkpoints WHERE attempt_id = $1`, attempt.Attempt.ID)
				},
			},
			{
				name: "evidence",
				run: func(t *testing.T, b *Brain, ctx context.Context, namespaceID int64) {
					item, condition := prepareWorkExecutionItem(t, b, ctx, namespaceID, "replay evidence")
					attempt := startWorkExecutionAttempt(t, b, ctx, item.ID, "evidence-agent")
					input := WorkEvidenceInput{
						EvidenceType: "test", Summary: "evidence replay", Reference: "test-output",
						Payload: json.RawMessage(`{"passed":true}`),
					}
					actionKey := fmt.Sprintf("replay-evidence-%d", attempt.Attempt.ID)
					first, err := b.SubmitWorkEvidence(ctx, attempt.Attempt.ID, attempt.LeaseToken, input, []int64{condition.ID}, actionKey)
					if err != nil {
						t.Fatalf("first SubmitWorkEvidence: %v", err)
					}
					second, err := b.SubmitWorkEvidence(ctx, attempt.Attempt.ID, attempt.LeaseToken, input, []int64{condition.ID}, actionKey)
					if err != nil {
						t.Fatalf("replayed SubmitWorkEvidence: %v", err)
					}
					if first.ID != second.ID || len(second.ConditionIDs) != 1 || second.ConditionIDs[0] != condition.ID {
						t.Fatalf("evidence replay changed result: first=%#v second=%#v", first, second)
					}
					assertWorkExecutionRowCount(t, b, ctx, 1, `SELECT count(*) FROM work_evidence WHERE attempt_id = $1`, attempt.Attempt.ID)
					assertWorkExecutionRowCount(t, b, ctx, 1, `SELECT count(*) FROM work_condition_evidence WHERE evidence_id = $1`, first.ID)
					assertWorkActionReplay(t, b, ctx, item.ID, attempt.Attempt.ID, actionKey, "work.evidence.submitted")
				},
			},
			{
				name: "verification",
				run: func(t *testing.T, b *Brain, ctx context.Context, namespaceID int64) {
					item, condition := prepareWorkExecutionItem(t, b, ctx, namespaceID, "replay verification")
					attempt := startWorkExecutionAttempt(t, b, ctx, item.ID, "verify-agent")
					evidence := submitWorkExecutionEvidence(t, b, ctx, attempt, []int64{condition.ID}, "verify replay evidence", fmt.Sprintf("verify-evidence-%d", attempt.Attempt.ID))
					actionKey := fmt.Sprintf("replay-verify-%d", attempt.Attempt.ID)
					first, err := b.VerifyWorkCondition(ctx, attempt.Attempt.ID, attempt.LeaseToken, condition.ID, "passed", []int64{evidence.ID}, "", actionKey)
					if err != nil {
						t.Fatalf("first VerifyWorkCondition: %v", err)
					}
					second, err := b.VerifyWorkCondition(ctx, attempt.Attempt.ID, attempt.LeaseToken, condition.ID, "passed", []int64{evidence.ID}, "", actionKey)
					if err != nil {
						t.Fatalf("replayed VerifyWorkCondition: %v", err)
					}
					if first.ID != second.ID || first.VerifiedAt == nil || second.VerifiedAt == nil || !first.VerifiedAt.Equal(*second.VerifiedAt) {
						t.Fatalf("verification replay changed result: first=%#v second=%#v", first, second)
					}
					assertWorkActionReplay(t, b, ctx, item.ID, attempt.Attempt.ID, actionKey, "work.condition.verified")
				},
			},
			{
				name: "handoff",
				run: func(t *testing.T, b *Brain, ctx context.Context, namespaceID int64) {
					item, _ := prepareWorkExecutionItem(t, b, ctx, namespaceID, "replay handoff")
					attempt := startWorkExecutionAttempt(t, b, ctx, item.ID, "handoff-agent")
					input := WorkCheckpointInput{Summary: "handoff", Result: "partial result", NextAction: "continue work"}
					actionKey := fmt.Sprintf("replay-handoff-%d", attempt.Attempt.ID)
					first, err := b.HandoffWorkAttempt(ctx, attempt.Attempt.ID, attempt.LeaseToken, input, actionKey)
					if err != nil {
						t.Fatalf("first HandoffWorkAttempt: %v", err)
					}
					second, err := b.HandoffWorkAttempt(ctx, attempt.Attempt.ID, attempt.LeaseToken, input, actionKey)
					if err != nil {
						t.Fatalf("replayed HandoffWorkAttempt: %v", err)
					}
					if first.ID != second.ID || first.Status != "handed_off" || second.Status != "handed_off" {
						t.Fatalf("handoff replay changed result: first=%#v second=%#v", first, second)
					}
					changed := input
					changed.Result = "different terminal result"
					if _, err := b.HandoffWorkAttempt(ctx, attempt.Attempt.ID, attempt.LeaseToken, changed, actionKey); !errors.Is(err, ErrWorkActionConflict) {
						t.Fatalf("changed handoff replay error = %v, want %v", err, ErrWorkActionConflict)
					}
					assertWorkExecutionRowCount(t, b, ctx, 1, `SELECT count(*) FROM work_checkpoints WHERE attempt_id = $1`, attempt.Attempt.ID)
					assertWorkExecutionRowCount(t, b, ctx, 1,
						`SELECT count(*) FROM work_events WHERE attempt_id = $1 AND event_type = 'work.memory.auto_saved'`,
						attempt.Attempt.ID,
					)
					assertWorkActionReplay(t, b, ctx, item.ID, attempt.Attempt.ID, actionKey, "work.attempt.handed_off")
				},
			},
			{
				name: "finish",
				run: func(t *testing.T, b *Brain, ctx context.Context, namespaceID int64) {
					item, condition := prepareWorkExecutionItem(t, b, ctx, namespaceID, "replay finish")
					attempt := startWorkExecutionAttempt(t, b, ctx, item.ID, "finish-agent")
					evidence := submitWorkExecutionEvidence(t, b, ctx, attempt, []int64{condition.ID}, "finish replay evidence", fmt.Sprintf("finish-evidence-%d", attempt.Attempt.ID))
					if _, err := b.VerifyWorkCondition(ctx, attempt.Attempt.ID, attempt.LeaseToken, condition.ID, "passed", []int64{evidence.ID}, "", fmt.Sprintf("finish-verify-%d", attempt.Attempt.ID)); err != nil {
						t.Fatalf("VerifyWorkCondition: %v", err)
					}
					input := WorkFinishInput{Summary: "finished", Result: "all checks passed"}
					actionKey := fmt.Sprintf("replay-finish-%d", attempt.Attempt.ID)
					first, err := b.FinishWorkAttempt(ctx, attempt.Attempt.ID, attempt.LeaseToken, input, actionKey)
					if err != nil {
						t.Fatalf("first FinishWorkAttempt: %v", err)
					}
					second, err := b.FinishWorkAttempt(ctx, attempt.Attempt.ID, attempt.LeaseToken, input, actionKey)
					if err != nil {
						t.Fatalf("replayed FinishWorkAttempt: %v", err)
					}
					if first.ID != second.ID || first.Status != "completed" || second.Status != "completed" {
						t.Fatalf("finish replay changed result: first=%#v second=%#v", first, second)
					}
					changed := input
					changed.Result = "different terminal result"
					if _, err := b.FinishWorkAttempt(ctx, attempt.Attempt.ID, attempt.LeaseToken, changed, actionKey); !errors.Is(err, ErrWorkActionConflict) {
						t.Fatalf("changed finish replay error = %v, want %v", err, ErrWorkActionConflict)
					}
					assertWorkExecutionRowCount(t, b, ctx, 1, `SELECT count(*) FROM work_checkpoints WHERE attempt_id = $1`, attempt.Attempt.ID)
					assertWorkActionReplay(t, b, ctx, item.ID, attempt.Attempt.ID, actionKey, "work.attempt.completed")
				},
			},
			{
				name: "explicit memory avoids automatic duplicate",
				run: func(t *testing.T, b *Brain, ctx context.Context, namespaceID int64) {
					item, _ := prepareWorkExecutionItem(t, b, ctx, namespaceID, "explicit memory")
					attempt := startWorkExecutionAttempt(t, b, ctx, item.ID, "memory-agent")
					b.embedder = failingWorkEmbedder{}
					if _, err := b.RememberForWork(ctx, item.ID, "explicit decision", "decision", fmt.Sprintf("remember-%d", attempt.Attempt.ID)); err != nil {
						t.Fatalf("RememberForWork: %v", err)
					}
					if _, err := b.HandoffWorkAttempt(ctx, attempt.Attempt.ID, attempt.LeaseToken, WorkCheckpointInput{
						Summary: "paused", Result: "explicit memory exists", NextAction: "continue from the decision",
					}, fmt.Sprintf("handoff-explicit-%d", attempt.Attempt.ID)); err != nil {
						t.Fatalf("HandoffWorkAttempt: %v", err)
					}
					assertWorkExecutionRowCount(t, b, ctx, 1,
						`SELECT count(*) FROM work_item_memory_links WHERE work_item_id = $1`, item.ID)
					assertWorkExecutionRowCount(t, b, ctx, 0,
						`SELECT count(*) FROM work_events WHERE attempt_id = $1 AND event_type = 'work.memory.auto_saved'`,
						attempt.Attempt.ID)
				},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				tt.run(t, b, ctx, namespaceID)
			})
		}
	})
}

func newWorkExecutionTestBrain(t *testing.T) (*Brain, context.Context, int64) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("STASH_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set STASH_TEST_POSTGRES_DSN to a disposable pgvector PostgreSQL database to run the work-execution Docker smoke test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	pool, err := db.Open(ctx, dsn, "work-execution-test", 3)
	if err != nil {
		t.Fatalf("open work-execution test database: %v", err)
	}
	b := &Brain{pool: pool, config: DefaultConfig()}
	t.Cleanup(pool.Close)

	slug := fmt.Sprintf("/tests/work-execution-%d", time.Now().UnixNano())
	namespaceID, err := b.CreateNamespace(ctx, slug, "work execution test", "")
	if err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx,
			`UPDATE work_attempts SET status = 'expired', ended_at = clock_timestamp(), updated_at = now()
			 WHERE status = 'active' AND work_item_id IN (SELECT id FROM work_items WHERE namespace_id = $1)`,
			namespaceID,
		)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM namespaces WHERE id = $1`, namespaceID)
	})
	return b, ctx, namespaceID
}

func createWorkExecutionItem(t *testing.T, b *Brain, ctx context.Context, namespaceID int64, title string) *models.WorkItem {
	t.Helper()
	item, err := b.CreateWorkItem(ctx, namespaceID, nil, nil, title, "state-machine test", "ready", 1, 0, "", nil)
	if err != nil {
		t.Fatalf("CreateWorkItem: %v", err)
	}
	return item
}

func prepareWorkExecutionItem(t *testing.T, b *Brain, ctx context.Context, namespaceID int64, title string) (*models.WorkItem, models.WorkCompletionCondition) {
	t.Helper()
	item := createWorkExecutionItem(t, b, ctx, namespaceID, title)
	prepared, err := b.PrepareWork(ctx, item.ID, "implement and verify the result", []CompletionConditionInput{{
		Kind: "test", Description: "observable result is verified", Required: true,
		Verification: json.RawMessage(`{"command":"go test ./..."}`),
	}}, fmt.Sprintf("prepare-%d", item.ID))
	if err != nil {
		t.Fatalf("PrepareWork: %v", err)
	}
	if prepared.NextAction == "" || len(prepared.CompletionConditions) != 1 || prepared.CompletionConditions[0].Status != "pending" {
		t.Fatalf("prepared work = %#v", prepared)
	}
	return item, prepared.CompletionConditions[0]
}

func startWorkExecutionAttempt(t *testing.T, b *Brain, ctx context.Context, workItemID int64, agentID string) *models.WorkAttemptLease {
	t.Helper()
	attempt, err := b.StartWorkAttempt(ctx, workItemID, agentID, nil, time.Minute, fmt.Sprintf("start-%d-%s", workItemID, agentID))
	if err != nil {
		t.Fatalf("StartWorkAttempt: %v", err)
	}
	if attempt.Attempt.Status != "active" || attempt.LeaseToken == "" {
		t.Fatalf("started attempt = %#v", attempt)
	}
	return attempt
}

func submitWorkExecutionEvidence(t *testing.T, b *Brain, ctx context.Context, attempt *models.WorkAttemptLease, conditionIDs []int64, summary, actionKey string) *models.WorkEvidence {
	t.Helper()
	evidence, err := b.SubmitWorkEvidence(ctx, attempt.Attempt.ID, attempt.LeaseToken, WorkEvidenceInput{
		EvidenceType: "test", Summary: summary, Reference: "state-machine", Payload: json.RawMessage(`{"passed":true}`),
	}, conditionIDs, actionKey)
	if err != nil {
		t.Fatalf("SubmitWorkEvidence: %v", err)
	}
	return evidence
}

func workAttemptStatus(t *testing.T, b *Brain, ctx context.Context, attemptID int64) string {
	t.Helper()
	var status string
	if err := b.pool.QueryRow(ctx, `SELECT status FROM work_attempts WHERE id = $1`, attemptID).Scan(&status); err != nil {
		t.Fatalf("read work attempt status: %v", err)
	}
	return status
}

func assertWorkActionReplay(t *testing.T, b *Brain, ctx context.Context, workItemID, attemptID int64, actionKey, eventType string) {
	t.Helper()
	keyDigest := workActionKeyDigest(actionKey)
	assertWorkExecutionRowCount(t, b, ctx, 1,
		`SELECT count(*) FROM work_action_receipts WHERE work_item_id = $1 AND attempt_id = $2 AND action_key = $3`,
		workItemID, attemptID, keyDigest,
	)
	assertWorkExecutionRowCount(t, b, ctx, 1,
		`SELECT count(*) FROM work_events WHERE work_item_id = $1 AND attempt_id = $2 AND event_key = $3 AND event_type = $4`,
		workItemID, attemptID, keyDigest, eventType,
	)
	assertWorkExecutionRowCount(t, b, ctx, 0,
		`SELECT count(*) FROM work_action_receipts WHERE work_item_id = $1 AND action_key = $2`,
		workItemID, actionKey,
	)
	assertWorkExecutionRowCount(t, b, ctx, 0,
		`SELECT count(*) FROM work_events WHERE work_item_id = $1 AND event_key = $2`,
		workItemID, actionKey,
	)
}

func assertWorkExecutionRowCount(t *testing.T, b *Brain, ctx context.Context, want int, query string, args ...any) {
	t.Helper()
	var got int
	if err := b.pool.QueryRow(ctx, query, args...).Scan(&got); err != nil {
		t.Fatalf("count work execution rows: %v", err)
	}
	if got != want {
		t.Fatalf("row count = %d, want %d", got, want)
	}
}
