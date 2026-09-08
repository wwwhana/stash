package brain

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/alash3al/stash/internal/models"
)

func TestGoalMapAttentionUsesCurrentStateAndOrdersActions(t *testing.T) {
	now := time.Now()
	expired := now.Add(-time.Minute)
	value := &models.GoalMap{
		GoalTree: models.GoalMapTree{Goals: []models.GoalMapGoal{
			{ID: 1, Status: "active", ReadyToComplete: true},
			{ID: 2, Status: "completed", CompletionMismatch: true},
			{ID: 3, Status: "completed", ReadyToComplete: true},
		}},
		WorkItems: []models.GoalMapWork{
			{ID: 4, Status: "review"},
			{ID: 5, Status: "doing", AttemptStatus: "active", LeaseExpires: &expired},
			{ID: 6, Status: "done", AttemptStatus: "expired"},
			{ID: 7, Status: "ready", ExecutionProgress: &models.WorkProgress{Status: "done"}},
			{ID: 9, Status: "blocked", ExecutionProgress: &models.WorkProgress{Total: 1, Blocked: 1, Status: "blocked"}},
		},
		UnassignedWork: []models.GoalMapWork{{ID: 8, Status: "blocked"}},
	}
	want := []models.GoalMapAttention{
		{Key: "goal:2", Reason: "goal_mismatch"}, {Key: "work:5", Reason: "work_expired"},
		{Key: "work:8", Reason: "work_blocked"}, {Key: "work:4", Reason: "work_review"}, {Key: "goal:1", Reason: "goal_ready"},
	}
	if got := goalMapAttention(value, now); !reflect.DeepEqual(got, want) {
		t.Fatalf("attention = %+v", got)
	}
	value.WorkItems[1].Status = "done"
	if got := goalMapAttention(value, now); len(got) != 4 {
		t.Fatalf("completed work retained: %+v", got)
	}
}

func TestWorkItemPlanContextIsScopedAndDoesNotExpireWorkPostgres(t *testing.T) {
	b, ctx, ns := newWorkExecutionTestBrain(t)
	component, err := b.CreateWorkPlanComponent(ctx, ns, WorkPlanComponentInput{Title: "문서를 검토한다", Description: "최신 설명과 일치한다", OwnedPaths: []string{"docs"}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := b.CreateWorkPlanTask(ctx, ns, WorkPlanTaskInput{ComponentID: component.ID, Title: "설명 오류를 찾는다", Description: "잘못된 설명을 기록한다"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.PrepareWork(ctx, task.ID, "설명 확인", []CompletionConditionInput{{Kind: "test", Description: "설명을 확인한다", Required: true, Verification: json.RawMessage(`{"command":"check docs"}`)}}, "prepare-context-read"); err != nil {
		t.Fatal(err)
	}
	lease := startWorkExecutionAttempt(t, b, ctx, task.ID, "review-agent")
	if _, err = b.pool.Exec(ctx, `UPDATE work_attempts SET lease_expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, lease.Attempt.ID); err != nil {
		t.Fatal(err)
	}
	scope, err := b.WorkItemPlanContext(ctx, task.ID, ns)
	if err != nil || scope == nil || scope.Component.ID != component.ID || !reflect.DeepEqual(scope.OwnedScopes, []string{"docs"}) {
		t.Fatalf("context = %+v, %v", scope, err)
	}
	wrong, err := b.WorkItemPlanContext(ctx, task.ID, ns+999999)
	if err != nil || wrong != nil {
		t.Fatalf("wrong namespace context = %+v, %v", wrong, err)
	}
	var status string
	if err = b.pool.QueryRow(ctx, `SELECT status FROM work_attempts WHERE id=$1`, lease.Attempt.ID).Scan(&status); err != nil || status != "active" {
		t.Fatalf("UI read changed attempt: %s, %v", status, err)
	}
	projection, err := b.GetGoalMap(ctx, ns, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range projection.Attention {
		if entry.Key == goalMapNodeKey("work", task.ID) && entry.Reason == "work_expired" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expired work absent from map: %+v", projection.Attention)
	}
}
