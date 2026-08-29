package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/alash3al/stash/internal/models"
)

const (
	agentBriefConditionLimit = 16
	agentBriefMemoryLimit    = 6
	agentBriefBlockerLimit   = 8
	agentBriefSelectionLimit = 12
)

func agentWorkItem(item models.WorkItem) models.AgentWorkItem {
	title, _ := truncateWorkResumeString(strings.TrimSpace(item.Title), 256)
	owner, _ := truncateWorkResumeString(strings.TrimSpace(item.Owner), 128)
	key, _ := truncateWorkResumeString(strings.TrimSpace(item.IssueKey), 96)
	return models.AgentWorkItem{
		ID: item.ID, GoalID: item.GoalID, IssueKey: key, Title: title, Status: item.Status, Owner: owner,
	}
}

func agentGoalBrief(goal models.GoalProgress) models.GoalBrief {
	content, _ := truncateWorkResumeString(strings.TrimSpace(goal.Content), 512)
	return models.GoalBrief{
		ID: goal.ID, ParentID: goal.ParentID, Content: content, Status: goal.Status,
		Progress: goal.Progress, SubtreeWorkDone: goal.SubtreeWorkDone, SubtreeWorkTotal: goal.SubtreeWorkTotal,
		ChildGoalCompleted: goal.ChildGoalCompleted, ChildGoalTotal: goal.ChildGoalTotal,
	}
}

func agentContextDigest(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("digest agent context: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return "", fmt.Errorf("normalize agent context: %w", err)
	}
	stripAgentDigestHeartbeats(normalized)
	payload, err = json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode normalized agent context: %w", err)
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("sha256:%x", digest), nil
}

// Heartbeats keep workspace identity current but do not change what an agent
// needs to read. Status, lease deadlines, Git state, content, and IDs remain in
// the digest, so operational changes still invalidate the brief.
func stripAgentDigestHeartbeats(value any) {
	switch typed := value.(type) {
	case map[string]any:
		_, hasLastSeen := typed["last_seen_at"]
		_, isWorktree := typed["worktree_path"]
		_, isRepository := typed["repository_instance_id"]
		if hasLastSeen && (isWorktree || isRepository) {
			delete(typed, "last_seen_at")
			delete(typed, "updated_at")
		}
		for key, child := range typed {
			if key == "metadata" || key == "payload" || key == "verification" {
				continue
			}
			stripAgentDigestHeartbeats(child)
		}
	case []any:
		for _, child := range typed {
			stripAgentDigestHeartbeats(child)
		}
	}
}

func buildWorkResumeBrief(bundle *models.WorkResumeBundle) (*models.WorkResumeBrief, error) {
	if bundle == nil {
		return nil, nil
	}
	brief := &models.WorkResumeBrief{
		WorkItem:             agentWorkItem(bundle.WorkItem),
		GoalContext:          bundle.GoalContext,
		CompletionConditions: make([]models.AgentCondition, 0),
		RelevantMemory:       make([]models.AgentMemory, 0),
		Blockers:             make([]models.AgentWorkItem, 0),
		Totals:               bundle.Totals,
	}
	brief.NextAction, _ = truncateWorkResumeString(strings.TrimSpace(bundle.NextAction), 512)
	if bundle.LatestAttempt != nil {
		brief.LatestAttempt = &models.AgentAttempt{
			ID: bundle.LatestAttempt.ID, AgentID: bundle.LatestAttempt.AgentID,
			Status: bundle.LatestAttempt.Status, WorktreeID: bundle.LatestAttempt.WorktreeID,
			LeaseExpiresAt: bundle.LatestAttempt.LeaseExpiresAt, EndedAt: bundle.LatestAttempt.EndedAt,
		}
	}
	if bundle.LatestCheckpoint != nil {
		summary, _ := truncateWorkResumeString(strings.TrimSpace(bundle.LatestCheckpoint.Summary), 512)
		result, _ := truncateWorkResumeString(strings.TrimSpace(bundle.LatestCheckpoint.Result), 512)
		nextAction, _ := truncateWorkResumeString(strings.TrimSpace(bundle.LatestCheckpoint.NextAction), 512)
		brief.LatestCheckpoint = &models.AgentCheckpoint{
			Summary: summary, Result: result, NextAction: nextAction, CreatedAt: bundle.LatestCheckpoint.CreatedAt,
		}
	}
	conditions := append([]models.WorkCompletionCondition(nil), bundle.CompletionConditions...)
	sort.SliceStable(conditions, func(left, right int) bool {
		leftPending := conditions[left].Required && conditions[left].Status != "passed" && conditions[left].Status != "waived"
		rightPending := conditions[right].Required && conditions[right].Status != "passed" && conditions[right].Status != "waived"
		if leftPending != rightPending {
			return leftPending
		}
		return conditions[left].Position < conditions[right].Position
	})
	for _, condition := range conditions {
		if len(brief.CompletionConditions) >= agentBriefConditionLimit {
			break
		}
		description, _ := truncateWorkResumeString(strings.TrimSpace(condition.Description), 256)
		brief.CompletionConditions = append(brief.CompletionConditions, models.AgentCondition{
			ID: condition.ID, Description: description, Required: condition.Required,
			Status: condition.Status, EvidenceCount: len(condition.EvidenceIDs),
		})
	}
	brief.MoreConditions = len(conditions) > len(brief.CompletionConditions)

	memories := append([]models.WorkMemorySnapshot(nil), bundle.MemoryLinks...)
	relationRank := func(relation string) int {
		switch relation {
		case "constraint":
			return 0
		case "decision":
			return 1
		case "failure":
			return 2
		case "context":
			return 3
		default:
			return 4
		}
	}
	sort.SliceStable(memories, func(left, right int) bool {
		leftRank, rightRank := relationRank(memories[left].Relation), relationRank(memories[right].Relation)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return memories[left].LinkedAt.After(memories[right].LinkedAt)
	})
	for _, memory := range memories {
		if len(brief.RelevantMemory) >= agentBriefMemoryLimit {
			break
		}
		content, _ := truncateWorkResumeString(strings.TrimSpace(memory.Content), 384)
		brief.RelevantMemory = append(brief.RelevantMemory, models.AgentMemory{
			MemoryType: memory.MemoryType, MemoryID: memory.MemoryID, Relation: memory.Relation,
			Content: content, Status: memory.Status,
		})
	}
	brief.MoreMemory = len(memories) > len(brief.RelevantMemory)
	for _, blocker := range bundle.Blockers {
		if len(brief.Blockers) >= agentBriefBlockerLimit {
			break
		}
		brief.Blockers = append(brief.Blockers, agentWorkItem(blocker))
	}
	brief.MoreBlockers = len(bundle.Blockers) > len(brief.Blockers)
	// Hash the complete authorized state, including records omitted from the
	// brief, so a matching receipt never hides a change beyond a brief limit.
	digest, err := agentContextDigest(bundle)
	if err != nil {
		return nil, err
	}
	brief.ContextDigest = digest
	return brief, nil
}

func workspaceRootContext(tree models.GoalTree) (*models.WorkGoalContext, *models.GoalBrief, error) {
	if tree.RootGoalID == nil || len(tree.Goals) == 0 {
		return nil, nil, nil
	}
	var root *models.GoalBrief
	for _, goal := range tree.Goals {
		if goal.ID != *tree.RootGoalID {
			continue
		}
		value := agentGoalBrief(goal)
		root = &value
		break
	}
	if root == nil {
		return nil, nil, nil
	}
	digest, err := agentContextDigest(tree)
	if err != nil {
		return nil, nil, err
	}
	rootID := root.ID
	context := &models.WorkGoalContext{
		ContextDigest: digest, RootGoalID: &rootID, Path: []models.GoalBrief{*root}, PathTotal: 1,
		Siblings: []models.GoalBrief{},
	}
	return context, root, nil
}

func buildWorkspaceResumeBrief(bundle *models.WorkspaceResumeBundle) (*models.WorkspaceResumeBrief, error) {
	if bundle == nil {
		return nil, nil
	}
	rootContext, sharedGoal, err := workspaceRootContext(bundle.GoalTree)
	if err != nil {
		return nil, err
	}
	brief := &models.WorkspaceResumeBrief{
		NamespaceID: bundle.Namespace.ID, Namespace: bundle.Namespace.Slug,
		GoalContext: rootContext, SharedGoal: sharedGoal,
		WorkSelection: make([]models.AgentWorkItem, 0),
	}
	brief.NextAction, _ = truncateWorkResumeString(strings.TrimSpace(bundle.NextAction), 512)
	if bundle.CurrentWork != nil {
		current := agentWorkItem(bundle.CurrentWork.WorkItem)
		brief.CurrentWork = &current
		if bundle.CurrentWork.GoalContext != nil {
			brief.GoalContext = bundle.CurrentWork.GoalContext
		}
		if brief.NextAction == "" {
			brief.NextAction, _ = truncateWorkResumeString(strings.TrimSpace(bundle.CurrentWork.NextAction), 512)
		}
	}

	items := make(map[int64]models.WorkItem)
	if bundle.WorkPlan != nil {
		for _, component := range bundle.WorkPlan.Components {
			for _, task := range component.Tasks {
				items[task.ID] = task.WorkItem
			}
		}
		brief.PlanDigest, err = agentContextDigest(bundle.WorkPlan)
		if err != nil {
			return nil, err
		}
	}
	for _, item := range bundle.Graph.Nodes {
		if item.IssueType != "component" {
			items[item.ID] = item
		}
	}
	for _, item := range bundle.Doing {
		items[item.ID] = item
	}
	for _, item := range bundle.Blocked {
		items[item.ID] = item
	}
	ordered := make([]models.WorkItem, 0, len(items))
	for _, item := range items {
		ordered = append(ordered, item)
		switch item.Status {
		case "ready", "backlog":
			brief.Counts.Ready++
		case "doing", "review":
			brief.Counts.Doing++
		case "blocked":
			brief.Counts.Blocked++
		case "done", "canceled":
			brief.Counts.Done++
		}
	}
	brief.Counts.Goals = len(bundle.GoalTree.Goals)
	statusRank := func(status string) int {
		switch status {
		case "doing", "review":
			return 0
		case "ready":
			return 1
		case "blocked":
			return 2
		case "backlog":
			return 3
		default:
			return 4
		}
	}
	sort.SliceStable(ordered, func(left, right int) bool {
		leftRank, rightRank := statusRank(ordered[left].Status), statusRank(ordered[right].Status)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if ordered[left].Priority != ordered[right].Priority {
			return ordered[left].Priority > ordered[right].Priority
		}
		return ordered[left].ID < ordered[right].ID
	})
	for _, item := range ordered {
		if len(brief.WorkSelection) >= agentBriefSelectionLimit {
			break
		}
		if brief.CurrentWork != nil && item.ID == brief.CurrentWork.ID {
			continue
		}
		if item.Status == "done" || item.Status == "canceled" {
			continue
		}
		brief.WorkSelection = append(brief.WorkSelection, agentWorkItem(item))
	}
	// The response stays short, but its digest covers the full bounded snapshot.
	digest, err := agentContextDigest(bundle)
	if err != nil {
		return nil, err
	}
	brief.ContextDigest = digest
	return brief, nil
}

func matchingAgentContextReceipt(knownDigest, scope string, id int64, nextAction string, actualDigest string) *models.AgentContextReceipt {
	if strings.TrimSpace(knownDigest) == "" || strings.TrimSpace(knownDigest) != actualDigest {
		return nil
	}
	return &models.AgentContextReceipt{
		Unchanged: true, ContextDigest: actualDigest, Scope: scope, ID: id, NextAction: nextAction,
	}
}
