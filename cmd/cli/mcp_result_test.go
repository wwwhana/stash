package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/config"
	"github.com/alash3al/stash/internal/models"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestJSONToolResultSplitsPaginatedSliceWithinLimit(t *testing.T) {
	bc := &bootstrap.Context{Config: &config.Config{MCPMaxResponseBytes: 1024}}
	values := make([]string, 20)
	for i := range values {
		values[i] = strings.Repeat(string(rune('a'+i)), 300)
	}

	result, err := jsonToolResult(bc, values, 40)
	if err != nil {
		t.Fatal(err)
	}
	text := toolResultText(t, result)
	if len(text) > bc.Config.MCPMaxResponseBytes {
		t.Fatalf("response size = %d, limit = %d", len(text), bc.Config.MCPMaxResponseBytes)
	}

	var chunk struct {
		Items      []string `json:"items"`
		HasMore    bool     `json:"has_more"`
		NextOffset int      `json:"next_offset"`
	}
	if err := json.Unmarshal([]byte(text), &chunk); err != nil {
		t.Fatalf("decode chunk: %v", err)
	}
	if len(chunk.Items) == 0 || len(chunk.Items) >= len(values) {
		t.Fatalf("returned items = %d, want a non-empty partial page", len(chunk.Items))
	}
	if !chunk.HasMore || chunk.NextOffset != 40+len(chunk.Items) {
		t.Fatalf("chunk continuation = has_more:%v next_offset:%d", chunk.HasMore, chunk.NextOffset)
	}
}

func TestJSONToolResultOmitsOversizedNonPageResult(t *testing.T) {
	bc := &bootstrap.Context{Config: &config.Config{MCPMaxResponseBytes: 1024}}
	result, err := jsonToolResult(bc, map[string]any{"content": strings.Repeat("x", 4000)})
	if err != nil {
		t.Fatal(err)
	}
	text := toolResultText(t, result)
	if len(text) > bc.Config.MCPMaxResponseBytes {
		t.Fatalf("response size = %d, limit = %d", len(text), bc.Config.MCPMaxResponseBytes)
	}
	var notice mcpResponseLimitNotice
	if err := json.Unmarshal([]byte(text), &notice); err != nil {
		t.Fatalf("decode notice: %v", err)
	}
	if !notice.ResultOmitted || notice.ResponseBytes <= notice.MaxResponseBytes {
		t.Fatalf("unexpected response limit notice: %#v", notice)
	}
}

func TestJSONToolResultKeepsSmallResultShape(t *testing.T) {
	result, err := jsonToolResult(nil, []string{"one", "two"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResultText(t, result); got != `["one","two"]` {
		t.Fatalf("small result = %s", got)
	}
}

func TestJSONToolResultPagesOversizedWorkGraph(t *testing.T) {
	bc := &bootstrap.Context{Config: &config.Config{MCPMaxResponseBytes: 2048}}
	nodes := make([]models.WorkItem, 20)
	edges := make([]models.WorkItemEdge, 0, len(nodes)-1)
	for i := range nodes {
		nodes[i] = models.WorkItem{ID: int64(i + 1), NamespaceID: 7, Title: strings.Repeat("node", 30)}
		if i > 0 {
			edges = append(edges, models.WorkItemEdge{ID: int64(i), NamespaceID: 7, FromItemID: int64(i), ToItemID: int64(i + 1), EdgeType: "blocks"})
		}
	}

	result, err := jsonToolResult(bc, &models.WorkGraph{Nodes: nodes, Edges: edges})
	if err != nil {
		t.Fatal(err)
	}
	text := toolResultText(t, result)
	if len(text) > bc.Config.MCPMaxResponseBytes {
		t.Fatalf("response size = %d, limit = %d", len(text), bc.Config.MCPMaxResponseBytes)
	}
	var page mcpWorkGraphPage
	if err := json.Unmarshal([]byte(text), &page); err != nil {
		t.Fatalf("decode graph page: %v", err)
	}
	if len(page.Nodes) == 0 || len(page.Nodes) >= len(nodes) {
		t.Fatalf("returned nodes = %d, want a non-empty partial graph", len(page.Nodes))
	}
	if len(page.Edges) == 0 {
		t.Fatal("partial graph omitted every connection")
	}
	if !page.HasMore || page.NextNodeOffset != len(page.Nodes) || page.NextQuery == "" {
		t.Fatalf("graph continuation = has_more:%v next_node_offset:%d next_query:%q", page.HasMore, page.NextNodeOffset, page.NextQuery)
	}
	if page.NextEdgeOffset != len(page.Edges) || page.ReturnedEdges != len(page.Edges) {
		t.Fatalf("edge continuation = next:%d returned:%d edges:%d", page.NextEdgeOffset, page.ReturnedEdges, len(page.Edges))
	}
	if !strings.Contains(page.NextQuery, "get_work_graph") || strings.Contains(page.NextQuery, "list_work_items") {
		t.Fatalf("next query does not continue the graph: %q", page.NextQuery)
	}
}

func TestWorkGraphPagesRecoverEveryNodeAndIndependentEdge(t *testing.T) {
	bc := &bootstrap.Context{Config: &config.Config{MCPMaxResponseBytes: 1600}}
	nodes := make([]models.WorkItem, 12)
	edges := make([]models.WorkItemEdge, 0, 11)
	for i := range nodes {
		nodes[i] = models.WorkItem{ID: int64(i + 1), NamespaceID: 7, Title: strings.Repeat("node", 20)}
		if i > 0 {
			edges = append(edges, models.WorkItemEdge{ID: int64(i), NamespaceID: 7, FromItemID: int64(i), ToItemID: int64(i + 1), EdgeType: "blocks"})
		}
	}
	graph := &models.WorkGraph{Nodes: nodes, Edges: edges}
	nodeOffset, edgeOffset := 0, 0
	seenNodes := make(map[int64]bool)
	seenEdges := make(map[int64]bool)
	for pageNumber := 0; pageNumber < 30; pageNumber++ {
		options := mcpWorkGraphPageOptions{NodeOffset: nodeOffset, NodeLimit: 3, EdgeOffset: edgeOffset, EdgeLimit: 2}
		options.NextQuery = func(nextNodeOffset, nextEdgeOffset int) string {
			return fmt.Sprintf("Call get_work_graph project=/projects/demo include_done=true node_offset=%d node_limit=3 edge_offset=%d edge_limit=2.", nextNodeOffset, nextEdgeOffset)
		}
		result, err := workGraphToolResult(bc, graph, options, true)
		if err != nil {
			t.Fatal(err)
		}
		var page mcpWorkGraphPage
		if err := json.Unmarshal([]byte(toolResultText(t, result)), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Nodes) == 0 && nodeOffset < len(nodes) || len(page.Edges) == 0 && edgeOffset < len(edges) {
			t.Fatalf("page %d made no progress: %#v", pageNumber, page)
		}
		for _, node := range page.Nodes {
			seenNodes[node.ID] = true
		}
		for _, edge := range page.Edges {
			seenEdges[edge.ID] = true
		}
		if page.HasMore && (!strings.Contains(page.NextQuery, "get_work_graph") || !strings.Contains(page.NextQuery, "include_done=true")) {
			t.Fatalf("page %d next_query = %q", pageNumber, page.NextQuery)
		}
		nodeOffset, edgeOffset = page.NextNodeOffset, page.NextEdgeOffset
		if !page.HasMore {
			break
		}
	}
	if len(seenNodes) != len(nodes) || len(seenEdges) != len(edges) {
		t.Fatalf("recovered nodes=%d/%d edges=%d/%d", len(seenNodes), len(nodes), len(seenEdges), len(edges))
	}
	if !seenEdges[3] {
		t.Fatal("edge spanning node page boundaries was lost")
	}
}

func TestWorkGraphPageFailsExplicitlyWhenLimitCannotFitEnvelope(t *testing.T) {
	bc := &bootstrap.Context{Config: &config.Config{MCPMaxResponseBytes: 32}}
	_, err := workGraphToolResult(bc, &models.WorkGraph{Nodes: []models.WorkItem{{ID: 1}}}, mcpWorkGraphPageOptions{}, true)
	if err == nil || !strings.Contains(err.Error(), "too small") {
		t.Fatalf("error = %v, want explicit response-limit guidance", err)
	}
}

func TestWorkGraphPageSizeSearchIsLogarithmic(t *testing.T) {
	graph := &models.WorkGraph{
		Nodes: make([]models.WorkItem, 10_000),
		Edges: make([]models.WorkItemEdge, 10_000),
	}
	calls := 0
	marshalPage := func(_ *models.WorkGraph, _ mcpWorkGraphPageOptions, _ int, nodeCount, _ int, edgeCount int) ([]byte, error) {
		calls++
		return make([]byte, nodeCount+edgeCount+200), nil
	}
	payload, ok, err := fitMCPWorkGraphChunkWithMarshal(graph, 1200, mcpWorkGraphPageOptions{}, marshalPage)
	if err != nil || !ok || len(payload) > 1200 {
		t.Fatalf("fit graph page = bytes:%d ok:%v err:%v", len(payload), ok, err)
	}
	if calls > 16 {
		t.Fatalf("page size search marshaled %d candidates, want logarithmic search", calls)
	}
}

func TestJSONToolResultKeepsSmallWorkGraphShape(t *testing.T) {
	graph := &models.WorkGraph{Nodes: []models.WorkItem{{ID: 1, NamespaceID: 7, Title: "small"}}}
	result, err := jsonToolResult(nil, graph)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResultText(t, result); got != `{"nodes":[{"id":1,"namespace_id":7,"issue_key":"","issue_type":"","title":"small","description":"","status":"","priority":0,"position":0,"owner":"","created_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z"}],"edges":null,"worktrees":null}` {
		t.Fatalf("small graph shape changed: %s", got)
	}
}

func TestJSONResourcePayloadStaysValidWithinSmallLimits(t *testing.T) {
	for _, maxBytes := range []int{1024, 2048, 4096} {
		t.Run(fmt.Sprintf("%d_bytes", maxBytes), func(t *testing.T) {
			bc := &bootstrap.Context{Config: &config.Config{MCPMaxResponseBytes: maxBytes}}
			payload, err := jsonResourcePayload(bc, map[string]any{"content": strings.Repeat("x", 8192)}, "Call resume_work with work_item_id=42.")
			if err != nil {
				t.Fatal(err)
			}
			if len(payload) > maxBytes {
				t.Fatalf("resource size = %d, limit = %d", len(payload), maxBytes)
			}

			var notice mcpResponseLimitNotice
			if err := json.Unmarshal(payload, &notice); err != nil {
				t.Fatalf("resource is not valid JSON: %v; payload=%q", err, payload)
			}
			if !notice.ResultOmitted || notice.NextQuery == "" {
				t.Fatalf("resource omission notice = %#v", notice)
			}
		})
	}
}

func TestJSONResourcePayloadKeepsSmallResultShape(t *testing.T) {
	payload, err := jsonResourcePayload(nil, map[string]any{"id": 42}, "unused")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(payload); got != `{"id":42}` {
		t.Fatalf("small resource = %s", got)
	}
}

func toolResultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if result == nil || len(result.Content) != 1 {
		t.Fatalf("unexpected tool result: %#v", result)
	}
	content, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("tool content type = %T", result.Content[0])
	}
	return content.Text
}
