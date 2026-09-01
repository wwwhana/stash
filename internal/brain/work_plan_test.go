package brain

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/alash3al/stash/internal/models"
)

func TestRejectActiveWorkAttempt(t *testing.T) {
	if err := rejectActiveWorkAttempt(false); err != nil {
		t.Fatalf("inactive work attempt guard = %v, want nil", err)
	}
	if err := rejectActiveWorkAttempt(true); !errors.Is(err, ErrActiveWorkAttempt) {
		t.Fatalf("active work attempt guard = %v, want %v", err, ErrActiveWorkAttempt)
	}
}

func TestWorkPlanMutationsRejectActiveDescendantAttempt(t *testing.T) {
	b, ctx, namespaceID := newWorkExecutionTestBrain(t)
	component, err := b.CreateWorkPlanComponent(ctx, namespaceID, WorkPlanComponentInput{
		Title: "서비스 구성", Description: "작업을 묶는다", Status: "ready",
	})
	if err != nil {
		t.Fatalf("CreateWorkPlanComponent: %v", err)
	}
	relatedComponent, err := b.CreateWorkPlanComponent(ctx, namespaceID, WorkPlanComponentInput{
		Title: "연결할 구성", Description: "관계 보호를 확인한다", Status: "ready",
	})
	if err != nil {
		t.Fatalf("CreateWorkPlanComponent related: %v", err)
	}
	task, err := b.CreateWorkPlanTask(ctx, namespaceID, WorkPlanTaskInput{
		ComponentID: component.ID, Title: "API 구현", Description: "API를 구현한다",
	})
	if err != nil {
		t.Fatalf("CreateWorkPlanTask: %v", err)
	}
	if _, err := b.PrepareWork(ctx, task.ID, "API를 구현하고 확인한다", []CompletionConditionInput{
		testCompletionCondition("API 검사가 통과한다"),
	}, fmt.Sprintf("prepare-plan-%d", task.ID)); err != nil {
		t.Fatalf("PrepareWork: %v", err)
	}
	if _, err := b.StartWorkAttempt(ctx, task.ID, "plan-agent", nil, time.Minute, fmt.Sprintf("start-plan-%d", task.ID)); err != nil {
		t.Fatalf("StartWorkAttempt: %v", err)
	}

	updatedTitle := "실행 중 바꾸려는 제목"
	checks := []struct {
		name string
		run  func() error
	}{
		{name: "task wording", run: func() error {
			_, err := b.UpdateWorkPlanTask(ctx, task.ID, WorkPlanTaskUpdate{Title: &updatedTitle})
			return err
		}},
		{name: "component wording", run: func() error {
			_, err := b.UpdateWorkPlanComponent(ctx, component.ID, WorkPlanComponentUpdate{Title: &updatedTitle})
			return err
		}},
		{name: "component paths", run: func() error {
			_, err := b.SetWorkPlanComponentPaths(ctx, component.ID, []string{"internal/web/**"})
			return err
		}},
		{name: "task transition", run: func() error {
			_, err := b.CompleteWorkPlanTask(ctx, task.ID, "plan-agent")
			return err
		}},
		{name: "task delete", run: func() error { return b.DeleteWorkPlanTask(ctx, task.ID) }},
		{name: "component delete", run: func() error { return b.DeleteWorkPlanComponent(ctx, component.ID) }},
		{name: "component dependency", run: func() error {
			_, err := b.LinkWorkPlanComponents(ctx, namespaceID, component.ID, relatedComponent.ID, "needs")
			return err
		}},
		{name: "component conceptual link", run: func() error {
			_, err := b.LinkWorkPlanComponents(ctx, namespaceID, relatedComponent.ID, component.ID, "links")
			return err
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.run(); !errors.Is(err, ErrActiveWorkAttempt) {
				t.Fatalf("mutation error = %v, want %v", err, ErrActiveWorkAttempt)
			}
		})
	}
}

func TestWorkPlanGoalUpdateRepairsInactiveLegacyAssignment(t *testing.T) {
	b, ctx, namespaceID := newWorkExecutionTestBrain(t)

	legacyGoal, err := b.CreateGoal(ctx, namespaceID, "이전 목표", nil, 1)
	if err != nil {
		t.Fatalf("CreateGoal legacy: %v", err)
	}
	component, err := b.CreateWorkPlanComponent(ctx, namespaceID, WorkPlanComponentInput{
		GoalID: &legacyGoal.ID, Title: "구성 요소", Description: "기존 목표를 가리키는 구성 요소", Status: "ready",
	})
	if err != nil {
		t.Fatalf("CreateWorkPlanComponent: %v", err)
	}
	task, err := b.CreateWorkPlanTask(ctx, namespaceID, WorkPlanTaskInput{
		ComponentID: component.ID, Title: "실행 작업", Description: "기존 목표를 가리키는 작업",
	})
	if err != nil {
		t.Fatalf("CreateWorkPlanTask: %v", err)
	}

	newGoal, err := b.CreateGoal(ctx, namespaceID, "현재 목표", nil, 2)
	if err != nil {
		t.Fatalf("CreateGoal current: %v", err)
	}
	if _, err := b.SetProjectGoalRoot(ctx, namespaceID, newGoal.ID, "test"); err != nil {
		t.Fatalf("SetProjectGoalRoot: %v", err)
	}
	if _, err := b.AbandonGoal(ctx, legacyGoal.ID, "계획을 새 목표로 옮김"); err != nil {
		t.Fatalf("AbandonGoal legacy: %v", err)
	}
	legacyPlan, err := b.GetWorkPlan(ctx, namespaceID)
	if err != nil {
		t.Fatalf("GetWorkPlan with legacy goal: %v", err)
	}
	legacyWarnings := map[string]bool{}
	for _, warning := range legacyPlan.Warnings {
		legacyWarnings[warning.Code] = true
	}
	if !legacyWarnings["component_goal_outside_tree"] || !legacyWarnings["component_goal_inactive"] ||
		!legacyWarnings["task_goal_outside_tree"] || !legacyWarnings["task_goal_inactive"] {
		t.Fatalf("legacy goal warnings = %#v", legacyPlan.Warnings)
	}

	updatedComponent, err := b.UpdateWorkPlanComponent(ctx, component.ID, WorkPlanComponentUpdate{GoalID: &newGoal.ID})
	if err != nil {
		t.Fatalf("UpdateWorkPlanComponent should repair the legacy goal: %v", err)
	}
	if updatedComponent.GoalID == nil || *updatedComponent.GoalID != newGoal.ID {
		t.Fatalf("component goal = %#v, want %d", updatedComponent.GoalID, newGoal.ID)
	}

	updatedTask, err := b.UpdateWorkPlanTask(ctx, task.ID, WorkPlanTaskUpdate{GoalID: &newGoal.ID})
	if err != nil {
		t.Fatalf("UpdateWorkPlanTask should repair the legacy goal: %v", err)
	}
	if updatedTask.GoalID == nil || *updatedTask.GoalID != newGoal.ID {
		t.Fatalf("task goal = %#v, want %d", updatedTask.GoalID, newGoal.ID)
	}
}

func TestWorkPlanReportsInactiveGoalAssignments(t *testing.T) {
	b, ctx, namespaceID := newWorkExecutionTestBrain(t)
	root, err := b.CreateGoal(ctx, namespaceID, "현재 목표", nil, 1)
	if err != nil {
		t.Fatalf("CreateGoal root: %v", err)
	}
	child, err := b.CreateGoal(ctx, namespaceID, "중단된 하위 목표", &root.ID, 1)
	if err != nil {
		t.Fatalf("CreateGoal child: %v", err)
	}
	if _, err := b.SetProjectGoalRoot(ctx, namespaceID, root.ID, "test"); err != nil {
		t.Fatalf("SetProjectGoalRoot: %v", err)
	}
	component, err := b.CreateWorkPlanComponent(ctx, namespaceID, WorkPlanComponentInput{
		GoalID: &child.ID, Title: "구성 요소", Description: "비활성 목표 연결 확인", Status: "ready",
	})
	if err != nil {
		t.Fatalf("CreateWorkPlanComponent: %v", err)
	}
	if _, err := b.CreateWorkPlanTask(ctx, namespaceID, WorkPlanTaskInput{
		ComponentID: component.ID, Title: "실행 작업", Description: "비활성 목표 연결 확인",
	}); err != nil {
		t.Fatalf("CreateWorkPlanTask: %v", err)
	}
	if _, err := b.AbandonGoal(ctx, child.ID, "계획에서 제외"); err != nil {
		t.Fatalf("AbandonGoal child: %v", err)
	}

	plan, err := b.GetWorkPlan(ctx, namespaceID)
	if err != nil {
		t.Fatalf("GetWorkPlan: %v", err)
	}
	seen := map[string]bool{}
	for _, warning := range plan.Warnings {
		seen[warning.Code] = true
	}
	if !seen["component_goal_inactive"] || !seen["task_goal_inactive"] {
		t.Fatalf("inactive goal warnings = %#v", plan.Warnings)
	}
}

func TestPrepareWorkRejectsInactiveSharedRoot(t *testing.T) {
	b, ctx, namespaceID := newWorkExecutionTestBrain(t)
	root, err := b.CreateGoal(ctx, namespaceID, "종료된 프로젝트", nil, 1)
	if err != nil {
		t.Fatalf("CreateGoal: %v", err)
	}
	if _, err := b.SetProjectGoalRoot(ctx, namespaceID, root.ID, "test"); err != nil {
		t.Fatalf("SetProjectGoalRoot: %v", err)
	}
	item, err := b.CreateWorkItem(ctx, namespaceID, &root.ID, nil, "실행 작업", "공유 목표 상태를 확인한다", "ready", 1, 0, "", nil)
	if err != nil {
		t.Fatalf("CreateWorkItem: %v", err)
	}
	if _, err := b.AbandonGoal(ctx, root.ID, "프로젝트를 닫음"); err != nil {
		t.Fatalf("AbandonGoal root: %v", err)
	}
	_, err = b.PrepareWork(ctx, item.ID, "활성 목표를 다시 선택한다", []CompletionConditionInput{
		testCompletionCondition("목표가 다시 활성화된다"),
	}, fmt.Sprintf("prepare-inactive-root-%d", item.ID))
	if !errors.Is(err, ErrProjectGoalInactive) {
		t.Fatalf("PrepareWork error = %v, want %v", err, ErrProjectGoalInactive)
	}
}

func TestPrepareWorkRejectsInactiveGoalWithoutSharedRoot(t *testing.T) {
	b, ctx, namespaceID := newWorkExecutionTestBrain(t)
	goal, err := b.CreateGoal(ctx, namespaceID, "중단된 목표", nil, 1)
	if err != nil {
		t.Fatalf("CreateGoal: %v", err)
	}
	item, err := b.CreateWorkItem(ctx, namespaceID, &goal.ID, nil, "실행 작업", "목표 상태를 확인한다", "ready", 1, 0, "", nil)
	if err != nil {
		t.Fatalf("CreateWorkItem: %v", err)
	}
	if _, err := b.AbandonGoal(ctx, goal.ID, "작업 취소"); err != nil {
		t.Fatalf("AbandonGoal: %v", err)
	}
	_, err = b.PrepareWork(ctx, item.ID, "활성 목표를 다시 선택한다", []CompletionConditionInput{
		testCompletionCondition("목표가 활성 상태다"),
	}, fmt.Sprintf("prepare-inactive-goal-%d", item.ID))
	if !errors.Is(err, ErrWorkGoalInvalid) {
		t.Fatalf("PrepareWork error = %v, want %v", err, ErrWorkGoalInvalid)
	}
}

func TestWorkPlanMetadataValidation(t *testing.T) {
	paths, err := normalizeWorkPlanPaths([]string{" src/audio/** ", "config.py", "src/audio/**", ""})
	if err != nil {
		t.Fatalf("normalizeWorkPlanPaths: %v", err)
	}
	if len(paths) != 2 || paths[0] != "src/audio/**" || paths[1] != "config.py" {
		t.Fatalf("paths = %#v", paths)
	}
	if _, err := normalizeWorkPlanPaths([]string{"src/foo\nbar"}); err == nil {
		t.Fatal("path with newline was accepted")
	}

	for _, provenance := range []string{"", "agent", "roadmap"} {
		actual, err := normalizeWorkPlanProvenance(provenance)
		if err != nil || actual != provenance {
			t.Errorf("provenance %q = %q, %v", provenance, actual, err)
		}
	}
	if _, err := normalizeWorkPlanProvenance("backlog"); err == nil {
		t.Fatal("unknown provenance was accepted")
	}
}

func TestWorkPlanDigestTracksSemanticContent(t *testing.T) {
	plan := &models.WorkPlan{
		Components: []models.WorkPlanComponent{{
			WorkItem:         models.WorkItem{ID: 1, IssueKey: "W-000001", Title: "Show current work", Description: "The owner can see active tasks.", Status: "ready"},
			TechnicalDetails: "Render task state.",
			OwnedPaths:       []string{"internal/web/**"},
			Tasks: []models.WorkPlanTask{{
				WorkItem: models.WorkItem{ID: 2, Title: "Render active tasks", Status: "ready"},
			}},
			Needs: []models.WorkPlanReference{},
			Links: []models.WorkPlanReference{},
		}},
		Decisions: []models.WorkPlanDecision{},
	}
	initial, err := workPlanDigest(plan)
	if err != nil {
		t.Fatalf("workPlanDigest: %v", err)
	}

	plan.Components[0].Status = "doing"
	plan.Components[0].Tasks[0].Status = "done"
	statusOnly, err := workPlanDigest(plan)
	if err != nil {
		t.Fatalf("workPlanDigest after status change: %v", err)
	}
	if initial != statusOnly {
		t.Fatal("workflow-only state made semantic validation stale")
	}

	plan.Components[0].Tasks[0].Title = "Show only active tasks"
	changed, err := workPlanDigest(plan)
	if err != nil {
		t.Fatalf("workPlanDigest after semantic change: %v", err)
	}
	if changed == initial {
		t.Fatal("task outcome change did not make semantic validation stale")
	}
}
