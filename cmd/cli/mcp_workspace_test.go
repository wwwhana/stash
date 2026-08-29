package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/alash3al/stash/internal/models"
	"github.com/mark3labs/mcp-go/server"
)

func TestNewMCPServerRegistersWorkspaceTools(t *testing.T) {
	mcpServer := newMCPServer(nil)
	for _, name := range []string{"resolve_workspace", "resume_workspace", "claim_workspace"} {
		if mcpServer.GetTool(name) == nil {
			t.Fatalf("newMCPServer did not register %q", name)
		}
	}
}

func TestWorkspaceToolSchemas(t *testing.T) {
	mcpServer := server.NewMCPServer("test", "test")
	registerWorkspaceTools(mcpServer, nil)
	required := map[string][]string{
		"resolve_workspace": {"cwd", "repository_instance_id", "git_common_dir", "git_dir", "worktree_path"},
		"resume_workspace":  {"namespace"},
		"claim_workspace":   {"work_item_id", "cwd", "repository_instance_id", "git_common_dir", "git_dir", "worktree_path", "agent_id", "action_key"},
	}
	for name, fields := range required {
		registered := mcpServer.GetTool(name)
		if registered == nil {
			t.Fatalf("tool %q is missing", name)
		}
		seen := make(map[string]bool)
		for _, field := range registered.Tool.InputSchema.Required {
			seen[field] = true
		}
		for _, field := range fields {
			if !seen[field] {
				t.Errorf("tool %q does not require %q; required=%v", name, field, registered.Tool.InputSchema.Required)
			}
		}
	}
	if tool := mcpServer.GetTool("resume_workspace").Tool; tool.Annotations.ReadOnlyHint == nil || *tool.Annotations.ReadOnlyHint {
		t.Fatal("resume_workspace must disclose that it may expire stale work")
	}
}

func TestWorkspaceResumeCompactionKeepsCore(t *testing.T) {
	bundle := &models.WorkspaceResumeBundle{
		Namespace:  models.Namespace{Slug: "/projects/example", Name: "Example", Description: strings.Repeat("x", 4096)},
		NextAction: "Run the focused tests",
		Worktree: &models.Worktree{
			ID: 1, Repository: strings.Repeat("repository", 1024),
			WorktreePath: strings.Repeat("path", 1024), Metadata: json.RawMessage(`{"source":"test"}`),
		},
		CurrentWork: &models.WorkResumeBundle{
			WorkItem:   models.WorkItem{ID: 1, Title: strings.Repeat("current", 1024), Description: strings.Repeat("description", 1024)},
			NextAction: strings.Repeat("continue", 1024),
		},
		LatestCheckpoint: &models.WorkCheckpoint{ID: 1, Summary: strings.Repeat("summary", 1024)},
		Graph: models.WorkGraph{Nodes: []models.WorkItem{
			{ID: 1, Title: strings.Repeat("node", 1024)},
		}},
		RecentFailures: []models.Failure{{ID: 1, Content: strings.Repeat("failure", 1024)}},
	}
	compact := compactWorkspaceResumeBundle(bundle, 1024)
	payload, err := json.Marshal(compact)
	if err != nil {
		t.Fatalf("marshal compact workspace resume: %v", err)
	}
	if len(payload) > 1024 {
		t.Fatalf("compact workspace resume has %d bytes, want at most 1024", len(payload))
	}
	if compact.Namespace.Slug != "/projects/example" || compact.NextAction != "Run the focused tests" {
		t.Fatalf("compaction lost continuation core: %#v", compact)
	}
	if !compact.Truncated.Core {
		t.Fatal("core truncation was not reported")
	}
}
