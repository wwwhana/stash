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
