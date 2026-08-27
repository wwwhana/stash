package brain

import (
	"testing"

	"github.com/alash3al/stash/internal/models"
)

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
