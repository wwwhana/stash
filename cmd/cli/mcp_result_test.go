package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/config"
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
