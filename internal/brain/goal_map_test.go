package brain

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/alash3al/stash/internal/models"
)

func TestBuildGoalProgressRollsLeafWorkIntoSharedRoot(t *testing.T) {
	rootID := int64(1)
	goals := []models.GoalProgress{
		{Goal: models.Goal{ID: rootID, Content: "A", Status: "active"}, Depth: 0, Path: []int64{rootID}},
		{Goal: models.Goal{ID: 2, ParentID: &rootID, Content: "A-1", Status: "completed"}, Depth: 1, Path: []int64{rootID, 2}},
		{Goal: models.Goal{ID: 3, ParentID: &rootID, Content: "A-2", Status: "active"}, Depth: 1, Path: []int64{rootID, 3}},
	}
	progress := buildGoalProgress(goals, map[int64]goalWorkCount{
		2: {Total: 2, Done: 2},
		3: {Total: 2, Done: 1},
	})

	if got := progress[0]; got.SubtreeWorkTotal != 4 || got.SubtreeWorkDone != 3 || got.ChildGoalTotal != 2 || got.ChildGoalCompleted != 1 {
		t.Fatalf("root roll-up = %#v", got)
	} else if math.Abs(got.Progress-0.75) > 0.0001 || got.ReadyToComplete {
		t.Fatalf("root progress = %v ready=%v", got.Progress, got.ReadyToComplete)
	}
	if got := progress[1]; got.Progress != 1 || got.CompletionMismatch {
		t.Fatalf("completed child = %#v", got)
	}
	if got := progress[2]; got.Progress != 0.5 || got.ReadyToComplete {
		t.Fatalf("active child = %#v", got)
	}
}

func TestBuildGoalProgressFlagsPrematureCompletion(t *testing.T) {
	progress := buildGoalProgress([]models.GoalProgress{
		{Goal: models.Goal{ID: 1, Content: "A", Status: "completed"}},
	}, map[int64]goalWorkCount{1: {Total: 2, Done: 1}})
	if len(progress) != 1 || !progress[0].CompletionMismatch || progress[0].Progress != 0.5 {
		t.Fatalf("premature completion = %#v", progress)
	}
}

func TestGoalMapMemoryDirection(t *testing.T) {
	owner, memory := "work:8", "memory:fact:3"
	if from, to := goalMapEdgeDirection(owner, memory, "constraint"); from != memory || to != owner {
		t.Fatalf("constraint direction = %s -> %s", from, to)
	}
	if from, to := goalMapEdgeDirection(owner, memory, "result"); from != owner || to != memory {
		t.Fatalf("result direction = %s -> %s", from, to)
	}
}

func TestCompactGoalMapWorkBoundsOwnerProjection(t *testing.T) {
	item := models.WorkItem{
		ID: 9, IssueKey: strings.Repeat("K", 120), Title: strings.Repeat("작업", 300),
		Description: strings.Repeat("large internal detail", 100), Status: "doing", Owner: strings.Repeat("agent", 80),
	}
	compact := compactGoalMapWork(item)
	if len([]rune(compact.IssueKey)) != 96 || len([]rune(compact.Title)) != 256 || len([]rune(compact.Owner)) != 128 {
		t.Fatalf("compact lengths = key:%d title:%d owner:%d", len([]rune(compact.IssueKey)), len([]rune(compact.Title)), len([]rune(compact.Owner)))
	}
	payload, err := json.Marshal(compact)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "large internal detail") || strings.Contains(string(payload), "description") {
		t.Fatalf("owner projection leaked work details: %s", payload)
	}
}

func TestGoalMapWorkCompletionRollsIntoProjectGoalPostgres(t *testing.T) {
	b, ctx, namespaceID := newWorkExecutionTestBrain(t)
	root, err := b.CreateGoal(ctx, namespaceID, "A", nil, 10)
	if err != nil {
		t.Fatalf("CreateGoal root: %v", err)
	}
	childOne, err := b.CreateGoal(ctx, namespaceID, "A-1", &root.ID, 5)
	if err != nil {
		t.Fatalf("CreateGoal A-1: %v", err)
	}
	childTwo, err := b.CreateGoal(ctx, namespaceID, "A-2", &root.ID, 4)
	if err != nil {
		t.Fatalf("CreateGoal A-2: %v", err)
	}
	otherNamespaceID, err := b.CreateNamespace(ctx, fmt.Sprintf("/tests/goal-map-other-%d", time.Now().UnixNano()), "other project", "")
	if err != nil {
		t.Fatalf("CreateNamespace other project: %v", err)
	}
	t.Cleanup(func() {
		_, _ = b.pool.Exec(ctx, `DELETE FROM namespaces WHERE id = $1`, otherNamespaceID)
	})
	otherRoot, err := b.CreateGoal(ctx, otherNamespaceID, "other project root", nil, 10)
	if err != nil {
		t.Fatalf("CreateGoal other project root: %v", err)
	}
	withoutRoot, err := b.GetGoalMap(ctx, namespaceID, true)
	if err != nil {
		t.Fatalf("GetGoalMap before shared root: %v", err)
	}
	if len(withoutRoot.RootCandidates) != 1 || withoutRoot.RootCandidates[0].ID != root.ID || withoutRoot.RootCandidates[0].ID == otherRoot.ID {
		t.Fatalf("exact namespace root candidates = %#v", withoutRoot.RootCandidates)
	}
	if _, err := b.SetProjectGoalRoot(ctx, namespaceID, root.ID, "owner"); err != nil {
		t.Fatalf("SetProjectGoalRoot: %v", err)
	}
	outside, err := b.CreateGoal(ctx, namespaceID, "unrelated outcome", nil, 1)
	if err != nil {
		t.Fatalf("CreateGoal outside shared tree: %v", err)
	}
	if _, err := b.CreateWorkItem(ctx, namespaceID, &outside.ID, nil, "detached work", "must be rejected", "ready", 1, 0, "", nil); err == nil {
		t.Fatal("CreateWorkItem accepted a goal outside the shared tree")
	}
	var unassignedID int64
	if err := b.pool.QueryRow(ctx,
		`INSERT INTO work_items (namespace_id, title, status) VALUES ($1, 'legacy unassigned work', 'ready') RETURNING id`,
		namespaceID,
	).Scan(&unassignedID); err != nil {
		t.Fatalf("insert legacy unassigned work: %v", err)
	}
	if _, err := b.pool.Exec(ctx, `UPDATE work_items SET status = 'doing' WHERE id = $1`, unassignedID); err == nil {
		t.Fatal("database accepted active work without a shared goal")
	}
	if _, err := b.pool.Exec(ctx, `DELETE FROM work_items WHERE id = $1`, unassignedID); err != nil {
		t.Fatalf("delete legacy unassigned work: %v", err)
	}

	finishChild := func(goalID int64, title, agent string) int64 {
		t.Helper()
		item, err := b.CreateWorkItem(ctx, namespaceID, &goalID, nil, title, "observable child result", "ready", 1, 0, "", nil)
		if err != nil {
			t.Fatalf("CreateWorkItem %s: %v", title, err)
		}
		prepared, err := b.PrepareWork(ctx, item.ID, "verify the child result", []CompletionConditionInput{{
			Kind: "test", Description: "child result is observed", Required: true,
			Verification: json.RawMessage(`{"command":"true"}`),
		}}, fmt.Sprintf("prepare-goal-map-%d", item.ID))
		if err != nil {
			t.Fatalf("PrepareWork %s: %v", title, err)
		}
		lease, err := b.StartWorkAttempt(ctx, item.ID, agent, nil, time.Minute, fmt.Sprintf("start-goal-map-%d", item.ID))
		if err != nil {
			t.Fatalf("StartWorkAttempt %s: %v", title, err)
		}
		if lease.GoalContext == nil || lease.GoalContext.RootGoalID == nil || *lease.GoalContext.RootGoalID != root.ID || lease.GoalContext.CurrentGoalID == nil || *lease.GoalContext.CurrentGoalID != goalID || len(lease.GoalContext.Path) != 2 {
			t.Fatalf("goal context %s = %#v", title, lease.GoalContext)
		}
		evidence, err := b.SubmitWorkEvidence(ctx, lease.Attempt.ID, lease.LeaseToken, WorkEvidenceInput{
			EvidenceType: "test", Summary: title + " passed", Payload: json.RawMessage(`{"passed":true}`),
		}, []int64{prepared.CompletionConditions[0].ID}, fmt.Sprintf("evidence-goal-map-%d", item.ID))
		if err != nil {
			t.Fatalf("SubmitWorkEvidence %s: %v", title, err)
		}
		if _, err := b.VerifyWorkCondition(ctx, lease.Attempt.ID, lease.LeaseToken, prepared.CompletionConditions[0].ID, "passed", []int64{evidence.ID}, "", fmt.Sprintf("verify-goal-map-%d", item.ID)); err != nil {
			t.Fatalf("VerifyWorkCondition %s: %v", title, err)
		}
		if _, err := b.FinishWorkAttempt(ctx, lease.Attempt.ID, lease.LeaseToken, WorkFinishInput{Summary: title + " done", Result: title + " observed"}, fmt.Sprintf("finish-goal-map-%d", item.ID)); err != nil {
			t.Fatalf("FinishWorkAttempt %s: %v", title, err)
		}
		return item.ID
	}

	firstWorkID := finishChild(childOne.ID, "A-1 result", "agent-one")
	if completed, err := b.GetGoal(ctx, childOne.ID); err != nil || completed.Status != "completed" {
		t.Fatalf("A-1 status = %#v err=%v", completed, err)
	}
	if currentRoot, err := b.GetGoal(ctx, root.ID); err != nil || currentRoot.Status != "active" {
		t.Fatalf("root after A-1 = %#v err=%v", currentRoot, err)
	}
	secondWorkID := finishChild(childTwo.ID, "A-2 result", "agent-two")
	if completedRoot, err := b.GetGoal(ctx, root.ID); err != nil || completedRoot.Status != "completed" {
		t.Fatalf("root after both children = %#v err=%v", completedRoot, err)
	}

	goalMap, err := b.GetGoalMap(ctx, namespaceID, true)
	if err != nil {
		t.Fatalf("GetGoalMap: %v", err)
	}
	if len(goalMap.GoalTree.Goals) != 3 || len(goalMap.WorkItems) != 2 {
		t.Fatalf("goal map counts = goals:%d work:%d", len(goalMap.GoalTree.Goals), len(goalMap.WorkItems))
	}
	seen := map[int64]string{}
	for _, work := range goalMap.WorkItems {
		seen[work.ID] = work.AgentID
		if work.LatestResult == "" {
			t.Fatalf("work %d has no latest result: %#v", work.ID, work)
		}
	}
	if seen[firstWorkID] != "agent-one" || seen[secondWorkID] != "agent-two" {
		t.Fatalf("map agents = %#v", seen)
	}
}
