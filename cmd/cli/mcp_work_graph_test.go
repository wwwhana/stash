package main

import (
	"strings"
	"testing"
	"time"

	"github.com/alash3al/stash/internal/brain"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestWorkGraphContinuationRoundTripAndRejectsMalformed(t *testing.T) {
	want := workGraphContinuation{NamespaceQuery: "/projects/demo", IncludeDone: true, Cursor: brain.WorkGraphCursor{SnapshotAt: time.Unix(123, 0).UTC(), MaxNodeID: 41, MaxEdgeID: 19, NodeSet: true, NodePriority: 3, NodePosition: 1.5, NodeID: 9, EdgeID: 7}}
	token := encodeWorkGraphContinuation(want)
	if strings.Contains(token, "/projects/demo") || strings.Contains(token, "snapshot_at") {
		t.Fatalf("continuation exposed its JSON payload: %q", token)
	}
	got, err := decodeWorkGraphContinuation(token)
	if err != nil {
		t.Fatal(err)
	}
	if got.NamespaceQuery != want.NamespaceQuery || got.IncludeDone != want.IncludeDone || got.Cursor != want.Cursor {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
	if _, err := decodeWorkGraphContinuation("not-a-token"); err == nil {
		t.Fatal("malformed continuation was accepted")
	}
}

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

func TestWorkGraphNextQueryPreservesProjectAndIncludeDone(t *testing.T) {
	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{
		"namespaces":   "/ignored",
		"project":      "/projects/myapp",
		"include_done": true,
	}
	query := workGraphNextQuery(request, 12, 34, 5, 7)
	for _, want := range []string{`get_work_graph`, `"project":"/projects/myapp"`, `"include_done":true`, `"node_offset":12`, `"node_limit":5`, `"edge_offset":34`, `"edge_limit":7`} {
		if !strings.Contains(query, want) {
			t.Fatalf("next query %q does not contain %q", query, want)
		}
	}
	if strings.Contains(query, "/ignored") {
		t.Fatalf("project-scoped next query leaked namespaces: %q", query)
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
