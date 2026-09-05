package main

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/models"
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
	NextQuery        string `json:"next_query,omitempty"`
}

type mcpWorkGraphPage struct {
	Nodes          []models.WorkItem     `json:"nodes"`
	Edges          []models.WorkItemEdge `json:"edges"`
	Worktrees      []models.Worktree     `json:"worktrees"`
	HasMore        bool                  `json:"has_more"`
	NextNodeOffset int                   `json:"next_node_offset"`
	NextEdgeOffset int                   `json:"next_edge_offset"`
	ReturnedNodes  int                   `json:"returned_nodes"`
	ReturnedEdges  int                   `json:"returned_edges"`
	TotalNodes     int                   `json:"total_nodes"`
	TotalEdges     int                   `json:"total_edges"`
	NextQuery      string                `json:"next_query,omitempty"`
	Message        string                `json:"message"`
}

type mcpWorkGraphPageOptions struct {
	NodeOffset int
	NodeLimit  int
	EdgeOffset int
	EdgeLimit  int
	NextQuery  func(nodeOffset, edgeOffset int) string
	NodeMore   bool
	EdgeMore   bool
}

// jsonToolResult keeps every tool response below a process-wide safety limit.
// Paginated slices are shortened into a valid JSON page with an exact next
// offset. Non-page results are replaced with a small notice instead of sending
// a payload large enough to exhaust the model context window.
func jsonToolResult(bc *bootstrap.Context, value any, pageOffset ...int) (*mcp.CallToolResult, error) {
	maxBytes := mcpMaxResponseBytes(bc)

	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal MCP result: %w", err)
	}
	if len(payload) <= maxBytes {
		return textToolResult(payload), nil
	}

	mcpResponseLimited.Inc()
	if goalMap, ok := value.(*models.GoalMap); ok {
		return goalMapPageResult(bc, goalMap, 0, 100, "")
	}
	if chunk, ok, err := fitMCPWorkGraphChunk(value, maxBytes, mcpWorkGraphPageOptions{}); err != nil {
		return nil, err
	} else if ok {
		return textToolResult(chunk), nil
	}
	if len(pageOffset) > 0 {
		if chunk, ok, err := fitMCPPageChunk(value, pageOffset[0], maxBytes); err != nil {
			return nil, err
		} else if ok {
			return textToolResult(chunk), nil
		}
	}

	notice, err := marshalMCPResponseLimitNotice(len(payload), maxBytes, "")
	if err != nil {
		return nil, err
	}
	return textToolResult(notice), nil
}

func workGraphToolResult(bc *bootstrap.Context, graph *models.WorkGraph, options mcpWorkGraphPageOptions, explicitPage bool) (*mcp.CallToolResult, error) {
	maxBytes := mcpMaxResponseBytes(bc)
	payload, err := json.Marshal(graph)
	if err != nil {
		return nil, fmt.Errorf("marshal MCP result: %w", err)
	}
	if !explicitPage && len(payload) <= maxBytes {
		return textToolResult(payload), nil
	}
	if len(payload) > maxBytes {
		mcpResponseLimited.Inc()
	}
	chunk, ok, err := fitMCPWorkGraphChunk(graph, maxBytes, options)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("MCP response limit %d is too small for one get_work_graph page; raise STASH_MCP_MAX_RESPONSE_BYTES", maxBytes)
	}
	return textToolResult(chunk), nil
}

func fitMCPWorkGraphChunk(value any, maxBytes int, options mcpWorkGraphPageOptions) ([]byte, bool, error) {
	var graph *models.WorkGraph
	switch typed := value.(type) {
	case *models.WorkGraph:
		graph = typed
	case models.WorkGraph:
		graph = &typed
	default:
		return nil, false, nil
	}
	if graph == nil {
		return nil, false, nil
	}
	return fitMCPWorkGraphChunkWithMarshal(graph, maxBytes, options, marshalMCPWorkGraphPage)
}

func fitMCPWorkGraphChunkWithMarshal(
	graph *models.WorkGraph,
	maxBytes int,
	options mcpWorkGraphPageOptions,
	marshalPage func(*models.WorkGraph, mcpWorkGraphPageOptions, int, int, int, int) ([]byte, error),
) ([]byte, bool, error) {
	nodeOffset := boundedOffset(options.NodeOffset, len(graph.Nodes))
	edgeOffset := boundedOffset(options.EdgeOffset, len(graph.Edges))
	nodeLimit := boundedCount(options.NodeLimit, len(graph.Nodes)-nodeOffset)
	edgeLimit := boundedCount(options.EdgeLimit, len(graph.Edges)-edgeOffset)
	remaining := nodeLimit > 0 || edgeLimit > 0
	low, high := 0, max(nodeLimit, edgeLimit)
	var best []byte
	for low <= high {
		step := low + (high-low)/2
		nodeCount := min(nodeLimit, step)
		edgeCount := min(edgeLimit, step)
		candidate, err := marshalPage(graph, options, nodeOffset, nodeCount, edgeOffset, edgeCount)
		if err != nil {
			return nil, false, err
		}
		if len(candidate) <= maxBytes {
			if nodeCount > 0 || edgeCount > 0 || !remaining {
				best = candidate
			}
			low = step + 1
			continue
		}
		high = step - 1
	}
	return best, len(best) > 0, nil
}

func marshalMCPWorkGraphPage(graph *models.WorkGraph, options mcpWorkGraphPageOptions, nodeOffset, nodeCount, edgeOffset, edgeCount int) ([]byte, error) {
	nodes := graph.Nodes[nodeOffset : nodeOffset+nodeCount]
	edges := graph.Edges[edgeOffset : edgeOffset+edgeCount]
	worktreeIDs := make(map[int64]struct{})
	for _, node := range nodes {
		for _, worktreeID := range node.WorktreeIDs {
			worktreeIDs[worktreeID] = struct{}{}
		}
	}
	worktrees := make([]models.Worktree, 0)
	for _, worktree := range graph.Worktrees {
		if _, ok := worktreeIDs[worktree.ID]; ok {
			worktrees = append(worktrees, worktree)
		}
	}

	nextNodeOffset := nodeOffset + nodeCount
	nextEdgeOffset := edgeOffset + edgeCount
	hasMore := nextNodeOffset < len(graph.Nodes) || nextEdgeOffset < len(graph.Edges) || options.NodeMore || options.EdgeMore
	nextQuery := ""
	if hasMore && options.NextQuery != nil {
		nextQuery = options.NextQuery(nextNodeOffset, nextEdgeOffset)
	} else if hasMore {
		nextQuery = fmt.Sprintf("Call get_work_graph with the same subject scope and include_done, node_offset=%d and edge_offset=%d.", nextNodeOffset, nextEdgeOffset)
	}
	payload, err := json.Marshal(mcpWorkGraphPage{
		Nodes: nodes, Edges: edges, Worktrees: worktrees,
		HasMore: hasMore, NextNodeOffset: nextNodeOffset, NextEdgeOffset: nextEdgeOffset,
		ReturnedNodes: nodeCount, ReturnedEdges: edgeCount,
		TotalNodes: len(graph.Nodes), TotalEdges: len(graph.Edges), NextQuery: nextQuery,
		Message: "Graph page returned. Nodes and edges use independent offsets; continue with next_query while has_more is true.",
	})
	if err != nil {
		return nil, fmt.Errorf("marshal MCP work graph page: %w", err)
	}
	return payload, nil
}

func boundedOffset(offset, total int) int {
	if offset < 0 {
		return 0
	}
	if offset > total {
		return total
	}
	return offset
}

func boundedCount(limit, remaining int) int {
	if limit <= 0 || limit > remaining {
		return remaining
	}
	return limit
}

func mcpMaxResponseBytes(bc *bootstrap.Context) int {
	if bc != nil && bc.Config != nil && bc.Config.MCPMaxResponseBytes > 0 {
		return bc.Config.MCPMaxResponseBytes
	}
	return defaultMCPMaxResponseBytes
}

// jsonResourcePayload applies the same byte limit as tool results while
// preserving a complete JSON document. Oversized resources become an explicit
// omission notice that points callers to a bounded tool query.
func jsonResourcePayload(bc *bootstrap.Context, value any, nextQuery string) ([]byte, error) {
	maxBytes := mcpMaxResponseBytes(bc)
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal MCP resource: %w", err)
	}
	if len(payload) <= maxBytes {
		return payload, nil
	}

	mcpResponseLimited.Inc()
	return marshalMCPResponseLimitNotice(len(payload), maxBytes, nextQuery)
}

func marshalMCPResponseLimitNotice(responseBytes, maxBytes int, nextQuery string) ([]byte, error) {
	notice, err := json.Marshal(mcpResponseLimitNotice{
		ResultOmitted:    true,
		ResponseBytes:    responseBytes,
		MaxResponseBytes: maxBytes,
		Message:          "Result exceeded the MCP response limit. Narrow the query or raise STASH_MCP_MAX_RESPONSE_BYTES.",
		NextQuery:        nextQuery,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal MCP response limit notice: %w", err)
	}
	if len(notice) > maxBytes {
		return nil, fmt.Errorf("MCP response limit %d is too small for the omission notice", maxBytes)
	}
	return notice, nil
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
