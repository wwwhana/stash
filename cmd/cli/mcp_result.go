package main

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/mark3labs/mcp-go/mcp"
)

const defaultMCPMaxResponseBytes = 32 * 1024

type mcpPageChunk struct {
	Items       any    `json:"items"`
	HasMore     bool   `json:"has_more"`
	NextOffset  int    `json:"next_offset"`
	Returned    int    `json:"returned"`
	TotalInPage int    `json:"total_in_page"`
	Message     string `json:"message"`
}

type mcpResponseLimitNotice struct {
	ResultOmitted    bool   `json:"result_omitted"`
	ResponseBytes    int    `json:"response_bytes"`
	MaxResponseBytes int    `json:"max_response_bytes"`
	Message          string `json:"message"`
}

// jsonToolResult keeps every tool response below a process-wide safety limit.
// Paginated slices are shortened into a valid JSON page with an exact next
// offset. Non-page results are replaced with a small notice instead of sending
// a payload large enough to exhaust the model context window.
func jsonToolResult(bc *bootstrap.Context, value any, pageOffset ...int) (*mcp.CallToolResult, error) {
	maxBytes := defaultMCPMaxResponseBytes
	if bc != nil && bc.Config != nil && bc.Config.MCPMaxResponseBytes > 0 {
		maxBytes = bc.Config.MCPMaxResponseBytes
	}

	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal MCP result: %w", err)
	}
	if len(payload) <= maxBytes {
		return textToolResult(payload), nil
	}

	mcpResponseLimited.Inc()
	if len(pageOffset) > 0 {
		if chunk, ok, err := fitMCPPageChunk(value, pageOffset[0], maxBytes); err != nil {
			return nil, err
		} else if ok {
			return textToolResult(chunk), nil
		}
	}

	notice, err := json.Marshal(mcpResponseLimitNotice{
		ResultOmitted:    true,
		ResponseBytes:    len(payload),
		MaxResponseBytes: maxBytes,
		Message:          "Result exceeded the MCP response limit. Narrow the query or raise STASH_MCP_MAX_RESPONSE_BYTES.",
	})
	if err != nil {
		return nil, fmt.Errorf("marshal MCP response limit notice: %w", err)
	}
	return textToolResult(notice), nil
}

func fitMCPPageChunk(value any, offset, maxBytes int) ([]byte, bool, error) {
	rv := reflect.ValueOf(value)
	if !rv.IsValid() || rv.Kind() != reflect.Slice || rv.Len() == 0 {
		return nil, false, nil
	}
	if offset < 0 {
		offset = 0
	}

	low, high := 1, rv.Len()
	var best []byte
	for low <= high {
		count := low + (high-low)/2
		candidate, err := json.Marshal(mcpPageChunk{
			Items:       rv.Slice(0, count).Interface(),
			HasMore:     count < rv.Len(),
			NextOffset:  offset + count,
			Returned:    count,
			TotalInPage: rv.Len(),
			Message:     "Response split to protect the model context. Call the same tool with offset=next_offset.",
		})
		if err != nil {
			return nil, false, fmt.Errorf("marshal MCP page chunk: %w", err)
		}
		if len(candidate) <= maxBytes {
			best = candidate
			low = count + 1
			continue
		}
		high = count - 1
	}
	return best, len(best) > 0, nil
}

func textToolResult(payload []byte) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent{Type: "text", Text: string(payload)}}}
}
