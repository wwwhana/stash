package brain

import "testing"

func TestWorkGraphInputValidation(t *testing.T) {
	for _, status := range []string{"backlog", "ready", "doing", "blocked", "review", "done", "canceled"} {
		if err := validateWorkItemStatus(status); err != nil {
			t.Errorf("status %q rejected: %v", status, err)
		}
	}
	if err := validateWorkItemStatus("unknown"); err == nil {
		t.Fatal("unknown work item status was accepted")
	}
	if err := validateWorktreeStatus("dirty"); err != nil {
		t.Fatalf("dirty worktree status rejected: %v", err)
	}
	if err := validateWorktreeStatus("active"); err == nil {
		t.Fatal("unknown worktree status was accepted")
	}
	if err := validatePosition(0.5); err != nil {
		t.Fatalf("finite position rejected: %v", err)
	}
	for _, issueType := range []string{"task", "bug", "feature", "chore", "question", "component"} {
		if err := validateWorkItemType(issueType); err != nil {
			t.Errorf("issue type %q rejected: %v", issueType, err)
		}
	}
	if err := validateWorkItemType("incident"); err == nil {
		t.Fatal("unknown issue type was accepted")
	}
	labels, err := normalizeWorkItemLabels([]string{" bug ", "ui", "bug", ""})
	if err != nil {
		t.Fatalf("labels rejected: %v", err)
	}
	if len(labels) != 2 || labels[0] != "bug" || labels[1] != "ui" {
		t.Fatalf("labels = %#v, want deduplicated labels", labels)
	}
}
