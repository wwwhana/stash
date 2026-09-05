package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/config"
	"github.com/alash3al/stash/internal/models"
)

func TestGoalMapPagesRecoverAllKindsAndRejectMixedRevisions(t *testing.T) {
	value := &models.GoalMap{}
	for i := 1; i <= 80; i++ {
		id := int64(i)
		value.GoalTree.Goals = append(value.GoalTree.Goals, models.GoalMapGoal{ID: id, Content: strings.Repeat("목표 ", 25)})
		value.WorkItems = append(value.WorkItems, models.GoalMapWork{ID: id, Title: "작업"})
		value.Memories = append(value.Memories, models.GoalMapMemory{MemoryID: id, Content: "기억"})
		value.Resources = append(value.Resources, models.GoalMapResource{ID: id, Title: "자료"})
		value.Edges = append(value.Edges, models.GoalMapEdge{Key: "edge"})
	}
	value.UnassignedWork = []models.GoalMapWork{{ID: 81}}
	value.RootCandidates = []models.GoalBrief{{ID: 82}}
	bc := &bootstrap.Context{Config: &config.Config{MCPMaxResponseBytes: 1200}}
	offset, pages := 0, 0
	snapshot := ""
	counts := map[string]int{}
	for {
		result, err := goalMapPageResult(bc, value, offset, 100, snapshot)
		if err != nil || result.IsError {
			t.Fatalf("page failed: %v %+v", err, result)
		}
		payload := toolResultText(t, result)
		if len(payload) > 1200 {
			t.Fatalf("page exceeds bound: %d", len(payload))
		}
		var page goalMapPage
		if err := json.Unmarshal([]byte(payload), &page); err != nil {
			t.Fatal(err)
		}
		for _, entry := range page.Items {
			counts[entry.Kind]++
		}
		snapshot = page.Snapshot
		pages++
		if !page.HasMore {
			break
		}
		if page.NextOffset <= offset || pages > 450 {
			t.Fatal("pagination failed to advance")
		}
		offset = page.NextOffset
	}
	for _, kind := range []string{"goal", "work", "memory", "resource", "edge"} {
		if counts[kind] != 80 {
			t.Fatalf("lost %s: %d", kind, counts[kind])
		}
	}
	if counts["unassigned_work"] != 1 || counts["root_candidate"] != 1 {
		t.Fatal("lost unassigned data")
	}
	value.WorkItems[0].Title = "변경됨"
	changed, err := goalMapPageResult(bc, value, 0, 100, snapshot)
	if err != nil || !changed.IsError || !strings.Contains(toolResultText(t, changed), "goal_map_changed") {
		t.Fatalf("mixed revisions accepted: %+v %v", changed, err)
	}
	if _, err := goalMapPageResult(bc, value, -1, 100, ""); err == nil {
		t.Fatal("negative offset accepted")
	}
	value.WorkItems[0].Title = strings.Repeat("x", 6000)
	oversized, err := goalMapPageResult(bc, value, 81, 1, "")
	if err != nil || !oversized.IsError {
		t.Fatal("oversized single entry silently omitted")
	}
}
