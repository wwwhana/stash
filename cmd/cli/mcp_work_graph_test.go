package main

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestWorkGraphNamespaceQueryUsesProjectScope(t *testing.T) {
	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{
		"namespaces": "/projects",
		"project":    "  /projects/myapp  ",
	}
	if got := workGraphNamespaceQuery(request); got != "/projects/myapp" {
		t.Fatalf("project scope = %q, want %q", got, "/projects/myapp")
	}
}

func TestWorkGraphNamespaceQueryFallsBackToNamespaces(t *testing.T) {
	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{"namespaces": "/projects"}
	if got := workGraphNamespaceQuery(request); got != "/projects" {
		t.Fatalf("namespace scope = %q, want %q", got, "/projects")
	}
}

func TestWorkGraphNamespaceQueryDefaultsToRoot(t *testing.T) {
	if got := workGraphNamespaceQuery(mcp.CallToolRequest{}); got != "/" {
		t.Fatalf("default scope = %q, want %q", got, "/")
	}
}
