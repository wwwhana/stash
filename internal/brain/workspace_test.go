package brain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestResumeWorkspaceKeepsCurrentWorkInsidePrincipal(t *testing.T) {
	b, ctx, namespaceID := newWorkExecutionTestBrain(t)
	var namespaceSlug string
	if err := b.pool.QueryRow(ctx, `SELECT slug FROM namespaces WHERE id = $1`, namespaceID).Scan(&namespaceSlug); err != nil {
		t.Fatalf("read namespace slug: %v", err)
	}
	item, _ := prepareWorkExecutionItem(t, b, ctx, namespaceID, "principal-owned workspace task")
	if _, err := b.StartWorkAttemptForPrincipal(
		ctx, item.ID, "agent-b", "principal-b", nil, time.Minute, "resume-workspace-principal-b",
	); err != nil {
		t.Fatalf("StartWorkAttemptForPrincipal: %v", err)
	}

	other, err := b.ResumeWorkspace(ctx, namespaceSlug, namespaceID, nil, "principal-a", 8)
	if err != nil {
		t.Fatalf("ResumeWorkspace principal-a: %v", err)
	}
	if other.CurrentWork != nil {
		t.Fatalf("principal-a received another principal's work: %#v", other.CurrentWork.WorkItem)
	}

	owner, err := b.ResumeWorkspace(ctx, namespaceSlug, namespaceID, nil, "principal-b", 8)
	if err != nil {
		t.Fatalf("ResumeWorkspace principal-b: %v", err)
	}
	if owner.CurrentWork == nil || owner.CurrentWork.WorkItem.ID != item.ID {
		t.Fatalf("principal-b current work = %#v, want %d", owner.CurrentWork, item.ID)
	}
}

func TestNormalizeWorkspaceRemoteURLUnifiesGitTransports(t *testing.T) {
	want := "github.com/example/project"
	for _, raw := range []string{
		"git@github.com:example/project.git",
		"ssh://git@github.com:22/example/project.git",
		"https://token:secret@GitHub.com/example/project.git?ignored=yes",
	} {
		got, err := NormalizeWorkspaceRemoteURL(raw)
		if err != nil {
			t.Fatalf("NormalizeWorkspaceRemoteURL(%q): %v", raw, err)
		}
		if got != want {
			t.Errorf("NormalizeWorkspaceRemoteURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestWorkspaceWorktreeKeySurvivesPathMove(t *testing.T) {
	slotBefore, err := WorkspaceWorktreeSlot("/repo/.git", "/repo/.git/worktrees/feature")
	if err != nil {
		t.Fatalf("slot before move: %v", err)
	}
	slotAfter, err := WorkspaceWorktreeSlot("/moved/repo/.git", "/moved/repo/.git/worktrees/feature")
	if err != nil {
		t.Fatalf("slot after move: %v", err)
	}
	before, err := WorkspaceWorktreeKey("clone-a", slotBefore)
	if err != nil {
		t.Fatalf("key before move: %v", err)
	}
	after, err := WorkspaceWorktreeKey("clone-a", slotAfter)
	if err != nil {
		t.Fatalf("key after move: %v", err)
	}
	if before != after {
		t.Fatalf("moved worktree key changed: %q != %q", before, after)
	}
	otherClone, err := WorkspaceWorktreeKey("clone-b", slotAfter)
	if err != nil {
		t.Fatalf("other clone key: %v", err)
	}
	if otherClone == after {
		t.Fatal("separate clones received the same worktree key")
	}
}

func TestNormalizeWorkspaceIdentityRequiresGitManagedSlot(t *testing.T) {
	_, err := normalizeWorkspaceIdentity(WorkspaceIdentityInput{
		CWD:                  "/repo",
		RepositoryInstanceID: "clone-a",
		GitCommonDir:         "/repo/.git",
		GitDir:               "/somewhere/else",
		WorktreePath:         "/repo",
		Status:               "clean",
		Metadata:             json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("worktree outside the Git common directory was accepted")
	}
}

func TestWorkspaceStatusAcceptsStale(t *testing.T) {
	if err := validateWorktreeStatus("stale"); err != nil {
		t.Fatalf("stale worktree status rejected: %v", err)
	}
}
