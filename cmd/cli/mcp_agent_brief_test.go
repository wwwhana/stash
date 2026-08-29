package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alash3al/stash/internal/models"
)

func TestBuildWorkResumeBriefBoundsAgentInputAndSupportsDigestReceipt(t *testing.T) {
	bundle := &models.WorkResumeBundle{
		WorkItem: models.WorkItem{ID: 41, GoalID: int64Pointer(7), IssueKey: "W-000041", Title: strings.Repeat("작업", 400), Status: "doing", Owner: "agent-a"},
		GoalContext: &models.WorkGoalContext{
			ContextDigest: "sha256:goal", RootGoalID: int64Pointer(1), CurrentGoalID: int64Pointer(7),
			Path: []models.GoalBrief{{ID: 1, Content: "A", Status: "active"}, {ID: 7, ParentID: int64Pointer(1), Content: "A-1", Status: "active"}}, PathTotal: 2,
			Siblings: []models.GoalBrief{},
		},
		NextAction: strings.Repeat("다음 행동 ", 300),
		Totals:     models.WorkResumeTotals{CompletionConditions: 30, MemoryLinks: 10, Blockers: 10},
	}
	for index := 0; index < 30; index++ {
		bundle.CompletionConditions = append(bundle.CompletionConditions, models.WorkCompletionCondition{
			ID: int64(index + 1), Description: strings.Repeat(fmt.Sprintf("조건 %d ", index), 100), Required: true, Status: "pending", Position: index,
		})
	}
	for index := 0; index < 10; index++ {
		bundle.MemoryLinks = append(bundle.MemoryLinks, models.WorkMemorySnapshot{
			MemoryType: "fact", MemoryID: int64(index + 1), Relation: "constraint", Content: strings.Repeat("제약 ", 300), Status: "active",
		})
		bundle.Blockers = append(bundle.Blockers, models.WorkItem{ID: int64(100 + index), IssueKey: fmt.Sprintf("W-%06d", 100+index), Title: strings.Repeat("막힌 작업 ", 80), Status: "blocked"})
	}

	brief, err := buildWorkResumeBrief(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if len(brief.CompletionConditions) != agentBriefConditionLimit || !brief.MoreConditions || len(brief.RelevantMemory) != agentBriefMemoryLimit || !brief.MoreMemory || len(brief.Blockers) != agentBriefBlockerLimit || !brief.MoreBlockers {
		t.Fatalf("brief limits = conditions:%d memory:%d blockers:%d flags:%v/%v/%v", len(brief.CompletionConditions), len(brief.RelevantMemory), len(brief.Blockers), brief.MoreConditions, brief.MoreMemory, brief.MoreBlockers)
	}
	payload, err := json.Marshal(brief)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > 16*1024 {
		t.Fatalf("brief payload = %d bytes", len(payload))
	}
	repeated, err := buildWorkResumeBrief(bundle)
	if err != nil || repeated.ContextDigest != brief.ContextDigest {
		t.Fatalf("stable digest = %q, want %q, err=%v", repeated.ContextDigest, brief.ContextDigest, err)
	}
	receipt := matchingAgentContextReceipt(brief.ContextDigest, "work", 41, brief.NextAction, brief.ContextDigest)
	if receipt == nil || !receipt.Unchanged || receipt.ID != 41 {
		t.Fatalf("matching receipt = %#v", receipt)
	}
	bundle.NextAction = "다른 행동"
	changed, err := buildWorkResumeBrief(bundle)
	if err != nil || changed.ContextDigest == brief.ContextDigest {
		t.Fatalf("changed digest = %q, previous %q, err=%v", changed.ContextDigest, brief.ContextDigest, err)
	}
}

func TestBuildWorkspaceResumeBriefKeepsSharedGoalAndShortSelection(t *testing.T) {
	rootID := int64(1)
	bundle := &models.WorkspaceResumeBundle{
		Namespace: models.Namespace{ID: 5, Slug: "/projects/a", Name: "A"},
		GoalTree:  models.GoalTree{RootGoalID: &rootID, Goals: []models.GoalProgress{{Goal: models.Goal{ID: rootID, Content: "A", Status: "active"}}}},
		WorkPlan: &models.WorkPlan{Components: []models.WorkPlanComponent{{
			WorkItem: models.WorkItem{ID: 2, IssueType: "component", Title: "구성"}, Tasks: make([]models.WorkPlanTask, 0),
		}}},
	}
	for index := 0; index < 20; index++ {
		status := "ready"
		if index < 2 {
			status = "doing"
		}
		bundle.WorkPlan.Components[0].Tasks = append(bundle.WorkPlan.Components[0].Tasks, models.WorkPlanTask{WorkItem: models.WorkItem{
			ID: int64(10 + index), GoalID: &rootID, IssueKey: fmt.Sprintf("W-%06d", 10+index), Title: fmt.Sprintf("작업 %d", index), Status: status, Priority: 20 - index,
		}})
	}
	brief, err := buildWorkspaceResumeBrief(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if brief.SharedGoal == nil || brief.SharedGoal.Content != "A" || brief.GoalContext == nil || len(brief.GoalContext.Path) != 1 {
		t.Fatalf("shared goal brief = %#v", brief)
	}
	if len(brief.WorkSelection) != agentBriefSelectionLimit || brief.Counts.Doing != 2 || brief.Counts.Ready != 18 {
		t.Fatalf("selection=%d counts=%#v", len(brief.WorkSelection), brief.Counts)
	}
	if brief.ContextDigest == "" || brief.PlanDigest == "" {
		t.Fatalf("digests = context:%q plan:%q", brief.ContextDigest, brief.PlanDigest)
	}
}

func TestWorkspaceBriefDigestIgnoresHeartbeatButDetectsGitState(t *testing.T) {
	firstSeen := time.Date(2026, 8, 29, 1, 0, 0, 0, time.UTC)
	secondSeen := firstSeen.Add(time.Minute)
	bundle := &models.WorkspaceResumeBundle{
		Namespace: models.Namespace{ID: 5, Slug: "/projects/a", UpdatedAt: firstSeen},
		Worktree: &models.Worktree{
			ID: 9, NamespaceID: 5, HeadSHA: "first", Status: "clean",
			LastSeenAt: &firstSeen, UpdatedAt: firstSeen,
		},
	}
	first, err := buildWorkspaceResumeBrief(bundle)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Worktree.LastSeenAt = &secondSeen
	bundle.Worktree.UpdatedAt = secondSeen
	second, err := buildWorkspaceResumeBrief(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if second.ContextDigest != first.ContextDigest {
		t.Fatalf("heartbeat changed digest: %q != %q", second.ContextDigest, first.ContextDigest)
	}
	bundle.Worktree.HeadSHA = "second"
	changed, err := buildWorkspaceResumeBrief(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if changed.ContextDigest == first.ContextDigest {
		t.Fatal("Git state change did not invalidate digest")
	}
}

func int64Pointer(value int64) *int64 { return &value }
