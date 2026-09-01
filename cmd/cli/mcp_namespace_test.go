package main

import (
	"strings"
	"testing"
)

func TestDeleteNamespaceToolIsRegistered(t *testing.T) {
	tool := newMCPServer(nil).GetTool("delete_namespace")
	if tool == nil {
		t.Fatal("delete_namespace tool is not registered")
	}
	if len(tool.Tool.InputSchema.Required) != 1 || tool.Tool.InputSchema.Required[0] != "slug" {
		t.Fatalf("delete_namespace required fields = %v, want slug", tool.Tool.InputSchema.Required)
	}
	if !strings.Contains(strings.ToLower(tool.Tool.Description), "soft-delete") {
		t.Fatalf("delete_namespace description does not explain soft deletion: %q", tool.Tool.Description)
	}
}
