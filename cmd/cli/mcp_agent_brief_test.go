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
		PlanContext: &models.WorkPlanExecutionContext{
			Component: models.WorkPlanReference{ID: 4, IssueKey: "W-000004", Title: strings.Repeat("A-2 운영 화면 ", 80)},
			Outcome:   strings.Repeat("운영자가 결과를 확인한다. ", 80), Guidance: strings.Repeat("공통 계획을 따른다. ", 80),
			TaskDetails: strings.Repeat("필터와 상태를 연결한다. ", 80),
			OwnedScopes: []string{"web://agent-atlas/**", "jira://agent-atlas/**", "confluence://agent-atlas/**", "stash://memory/**", "api://agent-atlas/**"},
		},
		NextAction: strings.Repeat("다음 행동 ", 300),
		LatestAttempt: &models.WorkAttempt{
			ID: 42, AgentID: strings.Repeat("에이전트", 3000), Status: "active",
		},
		Totals: models.WorkResumeTotals{
			CompletionConditions: 30, MemoryLinks: 10, Resources: 10, DependencyResults: 10, Blockers: 10,
		},
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
		bundle.Resources = append(bundle.Resources, models.WorkResourceRef{
			ID: int64(200 + index), ResourceKey: fmt.Sprintf("jira:APP-%d", index), Kind: "ticket",
			Source: "jira", Authority: "external", Title: strings.Repeat("연결 자료 ", 80),
			URI: fmt.Sprintf("https://jira.example.test/browse/APP-%d", index), Summary: strings.Repeat("요약 ", 300), Role: "input",
		})
		bundle.DependencyResults = append(bundle.DependencyResults, models.WorkDependencyResult{
			WorkItem: models.AgentWorkItem{ID: int64(300 + index), IssueKey: fmt.Sprintf("W-%06d", 300+index), Title: strings.Repeat("선행 작업 ", 80), Status: "done"},
			Summary:  strings.Repeat("완료 내용 ", 120), Result: strings.Repeat("확인 결과 ", 160),
		})
		bundle.Blockers = append(bundle.Blockers, models.WorkItem{ID: int64(100 + index), IssueKey: fmt.Sprintf("W-%06d", 100+index), Title: strings.Repeat("막힌 작업 ", 80), Status: "blocked"})
	}

	brief, err := buildWorkResumeBrief(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if len(brief.CompletionConditions) < 1 || len(brief.CompletionConditions) > agentBriefConditionLimit || !brief.MoreConditions ||
		len(brief.RelevantMemory) != 0 || brief.MoreMemory ||
		len(brief.ChangedFacts) < 1 || len(brief.ChangedFacts) > agentBriefFactChangeLimit || !brief.MoreChangedFacts ||
		len(brief.Resources) < 1 || len(brief.Resources) > agentBriefResourceLimit || !brief.MoreResources ||
		len(brief.DependencyResults) < 1 || len(brief.DependencyResults) > agentBriefDependencyLimit || !brief.MoreDependencyResults ||
		len(brief.Blockers) < 1 || len(brief.Blockers) > agentBriefBlockerLimit || !brief.MoreBlockers {
		t.Fatalf("brief limits = conditions:%d memory:%d facts:%d resources:%d dependencies:%d blockers:%d", len(brief.CompletionConditions), len(brief.RelevantMemory), len(brief.ChangedFacts), len(brief.Resources), len(brief.DependencyResults), len(brief.Blockers))
	}
	payload, err := json.Marshal(brief)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > agentBriefMaxBytes {
		t.Fatalf("brief payload = %d bytes", len(payload))
	}
	if len(brief.LatestAttempt.AgentID) > 128 {
		t.Fatalf("agent ID = %d bytes", len(brief.LatestAttempt.AgentID))
	}
	if brief.PlanContext == nil || brief.PlanContext.Component.ID != 4 || len(brief.PlanContext.OwnedScopes) != 4 || !brief.PlanContext.MoreOwnedScopes {
		t.Fatalf("plan context = %#v", brief.PlanContext)
	}
	if len(brief.PlanContext.Outcome) > 384 || len(brief.PlanContext.Guidance) > 384 || len(brief.PlanContext.TaskDetails) > 384 {
		t.Fatalf("plan context text was not bounded: %#v", brief.PlanContext)
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
	bundle.NextAction = strings.Repeat("다음 행동 ", 300)
	bundle.PlanContext.Outcome = "다른 구성 결과"
	planChanged, err := buildWorkResumeBrief(bundle)
	if err != nil || planChanged.ContextDigest == brief.ContextDigest {
		t.Fatalf("plan digest = %q, previous %q, err=%v", planChanged.ContextDigest, brief.ContextDigest, err)
	}
}

func TestApplyWorkContextFactDiffPaginatesWithoutSkipping(t *testing.T) {
	knownDigest := "sha256:" + strings.Repeat("a", 64)
	currentDigest := "sha256:" + strings.Repeat("b", 64)
	changes := make([]models.WorkContextFactChange, 0, 10)
	for index := 0; index < 10; index++ {
		changes = append(changes, models.WorkContextFactChange{
			FactID: int64(index + 1), Relation: "constraint", Change: "updated",
			Content: fmt.Sprintf("바뀐 사실 %d", index+1), Status: "active",
			StateDigest: "sha256:" + strings.Repeat(fmt.Sprintf("%x", index), 64),
		})
	}
	diff := &models.WorkContextFactDiff{BaselineFound: true, Changes: changes}

	first := &models.WorkResumeBrief{ContextDigest: currentDigest}
	complete, err := applyWorkContextFactDiff(first, diff, knownDigest, "", 0, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if complete || len(first.ChangedFacts) != agentBriefFactChangeLimit || !first.MoreChangedFacts {
		t.Fatalf("first page = complete:%v facts:%d more:%v", complete, len(first.ChangedFacts), first.MoreChangedFacts)
	}
	if first.ContextWindow.NextQuery.KnownContextDigest != knownDigest ||
		first.ContextWindow.NextQuery.ExpectedContextDigest != currentDigest ||
		first.ContextWindow.NextQuery.FactOffset != agentBriefFactChangeLimit {
		t.Fatalf("first next query = %#v", first.ContextWindow.NextQuery)
	}

	second := &models.WorkResumeBrief{ContextDigest: currentDigest}
	complete, err = applyWorkContextFactDiff(second, diff, knownDigest, currentDigest, agentBriefFactChangeLimit, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if !complete || len(second.ChangedFacts) != 2 || second.MoreChangedFacts {
		t.Fatalf("second page = complete:%v facts:%d more:%v", complete, len(second.ChangedFacts), second.MoreChangedFacts)
	}
	if second.ContextWindow.NextQuery.KnownContextDigest != currentDigest || second.ContextWindow.NextQuery.FactOffset != 0 {
		t.Fatalf("second next query = %#v", second.ContextWindow.NextQuery)
	}

	reset := &models.WorkResumeBrief{ContextDigest: currentDigest}
	complete, err = applyWorkContextFactDiff(reset, diff, knownDigest, knownDigest, agentBriefFactChangeLimit, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if complete || !reset.ContextWindow.CursorReset || len(reset.ChangedFacts) != agentBriefFactChangeLimit ||
		reset.ContextWindow.NextQuery.FactOffset != agentBriefFactChangeLimit {
		t.Fatalf("reset page = complete:%v window:%#v facts:%d", complete, reset.ContextWindow, len(reset.ChangedFacts))
	}
}

func TestFinalizeWorkResumeBriefHonorsSmallInputLimit(t *testing.T) {
	brief := &models.WorkResumeBrief{
		ContextDigest: "sha256:" + strings.Repeat("c", 64),
		SharedGoal:    &models.GoalBrief{ID: 1, Content: strings.Repeat("공통 목표 ", 80)},
		WorkItem:      models.AgentWorkItem{ID: 2, Title: strings.Repeat("현재 작업 ", 80), Status: "doing"},
		NextAction:    strings.Repeat("다음 행동 ", 80),
		PlanContext: &models.WorkPlanExecutionContext{
			Outcome: strings.Repeat("결과 ", 80), Guidance: strings.Repeat("지침 ", 80),
		},
		CompletionConditions: []models.AgentCondition{{ID: 3, Description: strings.Repeat("완료 조건 ", 80)}},
		ChangedFacts:         []models.WorkContextFactChange{{FactID: 4, Change: "added", Content: strings.Repeat("사실 ", 80), Status: "active"}},
		ContextWindow:        models.AgentContextWindow{NextQuery: models.AgentContextNextQuery{Detail: "brief"}},
	}
	finalizeWorkResumeBrief(brief, 1024)
	payload, err := json.Marshal(brief)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > 1024 || brief.ContextWindow.InputBytes != len(payload) {
		t.Fatalf("small context = payload:%d reported:%d", len(payload), brief.ContextWindow.InputBytes)
	}
	if len(brief.ChangedFacts) != 1 {
		t.Fatalf("small context dropped its only changed fact: %s", payload)
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
