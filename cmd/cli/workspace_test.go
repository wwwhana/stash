package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/alash3al/stash/internal/brain"
)

func runWorkspaceTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	gitArgs := append([]string{"-c", "commit.gpgSign=false"}, args...)
	command := exec.Command("git", gitArgs...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func TestCollectWorkspaceFactsInitializesStableCloneIdentity(t *testing.T) {
	repo := t.TempDir()
	runWorkspaceTestGit(t, repo, "init", "-q")
	runWorkspaceTestGit(t, repo, "config", "user.email", "stash-test@example.invalid")
	runWorkspaceTestGit(t, repo, "config", "user.name", "Stash Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o600); err != nil {
		t.Fatalf("write test repository file: %v", err)
	}
	runWorkspaceTestGit(t, repo, "add", "README.md")
	runWorkspaceTestGit(t, repo, "commit", "-qm", "initial")
	runWorkspaceTestGit(t, repo, "remote", "add", "origin", "git@github.com:example/project.git")

	first, err := collectWorkspaceFacts(context.Background(), repo, "codex", "/projects/example", true)
	if err != nil {
		t.Fatalf("collect first workspace facts: %v", err)
	}
	second, err := collectWorkspaceFacts(context.Background(), repo, "codex", "", true)
	if err != nil {
		t.Fatalf("collect second workspace facts: %v", err)
	}
	if first.RepositoryInstanceID == "" || first.RepositoryInstanceID != second.RepositoryInstanceID {
		t.Fatalf("repository instance ID is not stable: %q, %q", first.RepositoryInstanceID, second.RepositoryInstanceID)
	}
	if second.ProjectNamespace != "/projects/example" {
		t.Fatalf("stored project namespace = %q", second.ProjectNamespace)
	}
	canonicalRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolve test repository symlinks: %v", err)
	}
	if first.GitCommonDir != first.GitDir || first.WorktreePath != canonicalRepo {
		t.Fatalf("unexpected main worktree paths: %#v", first)
	}
	if first.Provider != "github" || first.RemoteURL != "git@github.com:example/project.git" {
		t.Fatalf("unexpected remote facts: %#v", first)
	}
}

func TestCollectWorkspaceFactsAllowsRepositoryWithoutFirstCommit(t *testing.T) {
	repo := t.TempDir()
	runWorkspaceTestGit(t, repo, "init", "-q")

	facts, err := collectWorkspaceFacts(context.Background(), repo, "codex", "/projects/empty", true)
	if err != nil {
		t.Fatalf("collect empty repository facts: %v", err)
	}
	if facts.HeadSHA != "" {
		t.Fatalf("empty repository head SHA = %q", facts.HeadSHA)
	}
	if facts.RepositoryInstanceID == "" {
		t.Fatal("empty repository did not receive a stable identity")
	}
}

func TestCollectWorkspaceFactsKeepsLinkedWorktreeIdentityAfterMove(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatalf("create repository directory: %v", err)
	}
	runWorkspaceTestGit(t, repo, "init", "-q")
	runWorkspaceTestGit(t, repo, "config", "user.email", "stash-test@example.invalid")
	runWorkspaceTestGit(t, repo, "config", "user.name", "Stash Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o600); err != nil {
		t.Fatalf("write repository file: %v", err)
	}
	runWorkspaceTestGit(t, repo, "add", "README.md")
	runWorkspaceTestGit(t, repo, "commit", "-qm", "initial")

	linked := filepath.Join(root, "linked")
	runWorkspaceTestGit(t, repo, "worktree", "add", "-qb", "feature", linked)
	before, err := collectWorkspaceFacts(context.Background(), linked, "codex", "/projects/example", true)
	if err != nil {
		t.Fatalf("collect linked worktree facts: %v", err)
	}
	beforeSlot, err := brain.WorkspaceWorktreeSlot(before.GitCommonDir, before.GitDir)
	if err != nil {
		t.Fatalf("linked worktree slot: %v", err)
	}
	beforeKey, err := brain.WorkspaceWorktreeKey(before.RepositoryInstanceID, beforeSlot)
	if err != nil {
		t.Fatalf("linked worktree key: %v", err)
	}

	moved := filepath.Join(root, "moved")
	runWorkspaceTestGit(t, repo, "worktree", "move", linked, moved)
	after, err := collectWorkspaceFacts(context.Background(), moved, "codex", "", true)
	if err != nil {
		t.Fatalf("collect moved worktree facts: %v", err)
	}
	afterSlot, err := brain.WorkspaceWorktreeSlot(after.GitCommonDir, after.GitDir)
	if err != nil {
		t.Fatalf("moved worktree slot: %v", err)
	}
	afterKey, err := brain.WorkspaceWorktreeKey(after.RepositoryInstanceID, afterSlot)
	if err != nil {
		t.Fatalf("moved worktree key: %v", err)
	}
	if beforeKey != afterKey {
		t.Fatalf("linked worktree key changed after git worktree move: %q != %q", beforeKey, afterKey)
	}
	if after.ProjectNamespace != "/projects/example" {
		t.Fatalf("moved worktree lost project namespace: %q", after.ProjectNamespace)
	}
}

func TestWorkspaceFactsSkipsDatabaseBootstrap(t *testing.T) {
	if commandNeedsBootstrap([]string{"stash", "workspace", "facts"}) {
		t.Fatal("workspace facts unexpectedly requires database bootstrap")
	}
	if !commandNeedsBootstrap([]string{"stash", "worktree", "sync"}) {
		t.Fatal("worktree sync unexpectedly skipped database bootstrap")
	}
}
