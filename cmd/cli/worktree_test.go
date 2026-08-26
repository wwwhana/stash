package main

import "testing"

func TestParseGitWorktreeList(t *testing.T) {
	output := `worktree /tmp/project
HEAD abc123
branch refs/heads/main

worktree /tmp/project-feature
HEAD def456
detached
`
	got, err := parseGitWorktreeList(output)
	if err != nil {
		t.Fatalf("parseGitWorktreeList returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d worktrees, want 2", len(got))
	}
	if got[0].Path != "/tmp/project" || got[0].Branch != "main" || got[0].Detached {
		t.Fatalf("main worktree = %#v", got[0])
	}
	if got[1].Path != "/tmp/project-feature" || got[1].HeadSHA != "def456" || !got[1].Detached {
		t.Fatalf("detached worktree = %#v", got[1])
	}
}

func TestParseGitWorktreeListRejectsEmpty(t *testing.T) {
	if _, err := parseGitWorktreeList("not a porcelain worktree listing"); err == nil {
		t.Fatal("expected an error for an empty worktree listing")
	}
}
