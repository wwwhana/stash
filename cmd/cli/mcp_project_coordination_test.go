package main

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestNewMCPServerRegistersProjectCoordinationTools(t *testing.T) {
	mcpServer := newMCPServer(nil)
	for _, name := range []string{
		"resume_project", "claim_work", "set_work_capabilities", "spawn_work",
		"attach_work_resource", "list_work_resources", "get_work_resource",
	} {
		if mcpServer.GetTool(name) == nil {
			t.Fatalf("newMCPServer did not register %q", name)
		}
	}
}

func TestProjectCoordinationToolSchemas(t *testing.T) {
	mcpServer := server.NewMCPServer("test", "test")
	registerProjectCoordinationTools(mcpServer, nil)

	requiredByTool := map[string][]string{
		"resume_project":        {"namespace"},
		"claim_work":            {"work_item_id", "agent_id", "action_key"},
		"set_work_capabilities": {"work_item_id"},
		"spawn_work": {
			"attempt_id", "lease_token", "action_key", "title", "next_action", "conditions",
		},
		"attach_work_resource": {"work_item_id", "resource_key", "kind", "title"},
		"list_work_resources":  {"work_item_id"},
		"get_work_resource":    {"resource_id"},
	}

	for name, wantRequired := range requiredByTool {
		tool := mcpServer.GetTool(name)
		if tool == nil {
			t.Fatalf("tool %q is not registered", name)
		}
		required := make(map[string]bool, len(tool.Tool.InputSchema.Required))
		for _, property := range tool.Tool.InputSchema.Required {
			required[property] = true
		}
		for _, property := range wantRequired {
			if !required[property] {
				t.Errorf("tool %q does not require %q; required=%v", name, property, tool.Tool.InputSchema.Required)
			}
		}
	}
}

func TestResumeProjectSchemaDoesNotRequireLocalWorkspace(t *testing.T) {
	mcpServer := server.NewMCPServer("test", "test")
	registerProjectCoordinationTools(mcpServer, nil)
	tool := mcpServer.GetTool("resume_project").Tool
	for _, forbidden := range []string{
		"cwd", "repository_instance_id", "git_common_dir", "git_dir", "worktree_path", "worktree_id", "root",
	} {
		if _, ok := tool.InputSchema.Properties[forbidden]; ok {
			t.Errorf("resume_project unexpectedly accepts local workspace field %q", forbidden)
		}
	}
	if tool.Annotations.ReadOnlyHint == nil || *tool.Annotations.ReadOnlyHint {
		t.Fatal("resume_project must advertise readOnlyHint=false because it may expire stale leases")
	}
	if tool.Annotations.IdempotentHint == nil || !*tool.Annotations.IdempotentHint {
		t.Fatal("resume_project must advertise idempotentHint=true")
	}
	if description := strings.ToLower(tool.Description); !strings.Contains(description, "without requiring git") {
		t.Fatalf("resume_project description does not make Git independence explicit: %q", tool.Description)
	}

	claim := mcpServer.GetTool("claim_work").Tool
	if _, ok := claim.InputSchema.Properties["worktree_id"]; !ok {
		t.Fatal("claim_work must retain optional worktree_id connector metadata")
	}
	for _, required := range claim.InputSchema.Required {
		if required == "worktree_id" {
			t.Fatal("claim_work unexpectedly requires worktree_id")
		}
	}
}

func TestStringListArgumentSupportsArraysAndCommaSeparatedValues(t *testing.T) {
	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{"capabilities": []any{"code", "browser"}}
	values, err := stringListArgument(request, "capabilities")
	if err != nil || len(values) != 2 || values[0] != "code" || values[1] != "browser" {
		t.Fatalf("array capabilities = %#v, err=%v", values, err)
	}

	request.Params.Arguments = map[string]any{"capabilities": "document, research\ndata"}
	values, err = stringListArgument(request, "capabilities")
	if err != nil || strings.Join(values, ",") != "document, research,data" {
		t.Fatalf("text capabilities = %#v, err=%v", values, err)
	}

	request.Params.Arguments = map[string]any{"capabilities": []any{"code", 1}}
	if _, err := stringListArgument(request, "capabilities"); err == nil {
		t.Fatal("non-string capability was accepted")
	}
}

func TestParseProjectResourceTemplateID(t *testing.T) {
	id, err := parseTemplateID("stash://work/42/brief", "stash://work/", "/brief")
	if err != nil || id != 42 {
		t.Fatalf("template ID = %d, err=%v", id, err)
	}
	for _, uri := range []string{"stash://work/0/brief", "stash://work/nope/brief", "stash://work/42/full"} {
		if _, err := parseTemplateID(uri, "stash://work/", "/brief"); err == nil {
			t.Errorf("invalid resource template URI was accepted: %q", uri)
		}
	}
}
