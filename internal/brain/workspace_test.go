package brain

import (
	"encoding/json"
	"testing"
)

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
