package main

import (
	"strings"
	"testing"
)

func TestPromptHistoryQueueToolIsHookOnly(t *testing.T) {
	mcpServer := newMCPServer(nil)
	tool := mcpServer.GetTool("queue_prompt_history")
	if tool == nil {
		t.Fatal("queue_prompt_history tool is not registered")
	}
	if len(tool.Tool.InputSchema.Required) != 1 || tool.Tool.InputSchema.Required[0] != "prompt" {
		t.Fatalf("queue_prompt_history required fields = %v, want prompt", tool.Tool.InputSchema.Required)
	}
	description := strings.ToLower(tool.Tool.Description)
	for _, phrase := range []string{"hook-only", "return before embedding", "must not call"} {
		if !strings.Contains(description, phrase) {
			t.Errorf("queue_prompt_history description does not contain %q: %q", phrase, tool.Tool.Description)
		}
	}

	initTool := mcpServer.GetTool("init")
	if initTool == nil || !strings.Contains(initTool.Tool.Description, "/self/history") {
		t.Fatalf("init description does not include /self/history: %#v", initTool)
	}
}
