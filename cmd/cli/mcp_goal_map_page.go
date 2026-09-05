package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/models"
	"github.com/mark3labs/mcp-go/mcp"
)

type goalMapEntry struct {
	Kind  string `json:"kind"`
	Value any    `json:"value"`
}

type goalMapPage struct {
	Items         []goalMapEntry `json:"items"`
	RootGoalID    *int64         `json:"root_goal_id,omitempty"`
	ResourceTotal int            `json:"resource_total"`
	Total         int            `json:"total"`
	HasMore       bool           `json:"has_more"`
	NextOffset    int            `json:"next_offset"`
	Snapshot      string         `json:"snapshot"`
}

// ponytail: rebuilds the projection for each page; use a database snapshot cursor
// if large-project query cost warrants it. The digest rejects mixed revisions.
func goalMapPageResult(bc *bootstrap.Context, value *models.GoalMap, offset, limit int, snapshot string) (*mcp.CallToolResult, error) {
	if offset < 0 || limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("offset must be non-negative and limit must be between 1 and 1000")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	if snapshot != "" && snapshot != digest {
		return mcp.NewToolResultError("goal_map_changed: 목록이 변경되었습니다. 처음부터 다시 불러오세요."), nil
	}
	entries := []goalMapEntry{}
	appendEntries := func(kind string, values any) {
		items := reflect.ValueOf(values)
		for i := 0; i < items.Len(); i++ {
			entries = append(entries, goalMapEntry{Kind: kind, Value: items.Index(i).Interface()})
		}
	}
	appendEntries("goal", value.GoalTree.Goals)
	appendEntries("root_candidate", value.RootCandidates)
	appendEntries("work", value.WorkItems)
	appendEntries("unassigned_work", value.UnassignedWork)
	appendEntries("memory", value.Memories)
	appendEntries("resource", value.Resources)
	appendEntries("edge", value.Edges)
	if offset > len(entries) {
		return nil, fmt.Errorf("offset exceeds the map size")
	}
	end := min(offset+limit, len(entries))
	for {
		page := goalMapPage{Items: entries[offset:end], RootGoalID: value.GoalTree.RootGoalID, ResourceTotal: value.ResourceTotal, Total: len(entries), HasMore: end < len(entries), NextOffset: end, Snapshot: digest}
		encoded, err = json.Marshal(page)
		if err != nil {
			return nil, err
		}
		if len(encoded) <= mcpMaxResponseBytes(bc) {
			return textToolResult(encoded), nil
		}
		if end-offset <= 1 {
			return mcp.NewToolResultError("지도 항목 한 개가 응답 제한보다 큽니다. 서버 응답 제한을 확인하세요."), nil
		}
		end = offset + (end-offset)/2
	}
}
