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
	agentBriefConditionLimit  = 16
	agentBriefEvidenceLimit   = 6
	agentBriefMemoryLimit     = 6
	agentBriefFactChangeLimit = 8
	agentBriefResourceLimit   = 6
	agentBriefDependencyLimit = 6
	agentBriefBlockerLimit    = 8
	agentBriefSelectionLimit  = 12
	agentBriefMaxBytes        = 16 * 1024
)

func agentWorkItem(item models.WorkItem) models.AgentWorkItem {
	title, _ := truncateWorkResumeString(strings.TrimSpace(item.Title), 256)
	owner, _ := truncateWorkResumeString(strings.TrimSpace(item.Owner), 128)
	key, _ := truncateWorkResumeString(strings.TrimSpace(item.IssueKey), 96)
	return models.AgentWorkItem{
		ID: item.ID, GoalID: item.GoalID, IssueKey: key, Title: title, Status: item.Status, Owner: owner,
		RequiredCapabilities: append([]string(nil), item.RequiredCapabilities...),
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
		CompletionConditions: make([]models.AgentCondition, 0),
		EvidenceReferences:   make([]models.AgentEvidenceReference, 0),
		RelevantMemory:       make([]models.AgentMemory, 0),
		ChangedFacts:         make([]models.WorkContextFactChange, 0),
		Resources:            make([]models.WorkResourceRef, 0),
		DependencyResults:    make([]models.WorkDependencyResult, 0),
		Blockers:             make([]models.AgentWorkItem, 0),
		Totals:               bundle.Totals,
	}
	if bundle.GoalContext != nil {
		goalContext := *bundle.GoalContext
		goalContext.Path = append([]models.GoalBrief(nil), bundle.GoalContext.Path...)
		goalContext.Siblings = append([]models.GoalBrief(nil), bundle.GoalContext.Siblings...)
		for index := range goalContext.Path {
			goalContext.Path[index].Content, _ = truncateWorkResumeString(strings.TrimSpace(goalContext.Path[index].Content), 192)
		}
		if len(goalContext.Siblings) > 4 {
			goalContext.Siblings = goalContext.Siblings[:4]
			goalContext.SiblingsTruncated = true
		}
		for index := range goalContext.Siblings {
			goalContext.Siblings[index].Content, _ = truncateWorkResumeString(strings.TrimSpace(goalContext.Siblings[index].Content), 128)
		}
		brief.GoalContext = &goalContext
		if len(goalContext.Path) > 0 {
			sharedGoal := goalContext.Path[0]
			brief.SharedGoal = &sharedGoal
		}
	}
	if bundle.PlanContext != nil {
		planContext := *bundle.PlanContext
		planContext.Component.IssueKey, _ = truncateWorkResumeString(strings.TrimSpace(planContext.Component.IssueKey), 96)
		planContext.Component.Title, _ = truncateWorkResumeString(strings.TrimSpace(planContext.Component.Title), 256)
		planContext.Outcome, _ = truncateWorkResumeString(strings.TrimSpace(planContext.Outcome), 384)
		planContext.Guidance, _ = truncateWorkResumeString(strings.TrimSpace(planContext.Guidance), 384)
		planContext.TaskDetails, _ = truncateWorkResumeString(strings.TrimSpace(planContext.TaskDetails), 384)
		planContext.OwnedScopes = append([]string(nil), bundle.PlanContext.OwnedScopes...)
		if len(planContext.OwnedScopes) > 4 {
			planContext.OwnedScopes = planContext.OwnedScopes[:4]
			planContext.MoreOwnedScopes = true
		}
		for index := range planContext.OwnedScopes {
			planContext.OwnedScopes[index], _ = truncateWorkResumeString(strings.TrimSpace(planContext.OwnedScopes[index]), 256)
		}
		brief.PlanContext = &planContext
	}
	brief.NextAction, _ = truncateWorkResumeString(strings.TrimSpace(bundle.NextAction), 512)
	if bundle.LatestAttempt != nil {
		agentID, _ := truncateWorkResumeString(strings.TrimSpace(bundle.LatestAttempt.AgentID), 128)
		brief.LatestAttempt = &models.AgentAttempt{
			ID: bundle.LatestAttempt.ID, AgentID: agentID,
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

	for _, evidence := range bundle.Evidence {
		if len(brief.EvidenceReferences) >= agentBriefEvidenceLimit {
			break
		}
		evidenceType, _ := truncateWorkResumeString(strings.TrimSpace(evidence.EvidenceType), 128)
		summary, _ := truncateWorkResumeString(strings.TrimSpace(evidence.Summary), 384)
		reference, _ := truncateWorkResumeString(strings.TrimSpace(evidence.Reference), 512)
		conditionIDs := append([]int64(nil), evidence.ConditionIDs...)
		if len(conditionIDs) > 16 {
			conditionIDs = conditionIDs[:16]
			brief.MoreEvidence = true
		}
		brief.EvidenceReferences = append(brief.EvidenceReferences, models.AgentEvidenceReference{
			ID: evidence.ID, EvidenceType: evidenceType, Summary: summary, Reference: reference,
			ContentDigest: evidence.ContentDigest, ConditionIDs: conditionIDs, SubmittedAt: evidence.SubmittedAt,
		})
	}
	brief.MoreEvidence = brief.MoreEvidence || len(bundle.Evidence) > len(brief.EvidenceReferences)

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
	nonFactTotal := 0
	factTotal := 0
	for _, memory := range memories {
		content, _ := truncateWorkResumeString(strings.TrimSpace(memory.Content), 384)
		if memory.MemoryType == "fact" {
			factTotal++
			if memory.Status != "active" || len(brief.ChangedFacts) >= agentBriefFactChangeLimit {
				continue
			}
			stateDigest, _ := agentContextDigest(struct {
				FactID   int64  `json:"fact_id"`
				Relation string `json:"relation"`
				Content  string `json:"content"`
				Status   string `json:"status"`
			}{memory.MemoryID, memory.Relation, memory.Content, memory.Status})
			brief.ChangedFacts = append(brief.ChangedFacts, models.WorkContextFactChange{
				FactID: memory.MemoryID, Relation: memory.Relation, Change: "added", Content: content,
				ContentTruncated: memory.ContentTruncated || len(content) < len(strings.TrimSpace(memory.Content)),
				Status:           memory.Status, StateDigest: stateDigest,
			})
			continue
		}
		nonFactTotal++
		if len(brief.RelevantMemory) >= agentBriefMemoryLimit {
			continue
		}
		brief.RelevantMemory = append(brief.RelevantMemory, models.AgentMemory{
			MemoryType: memory.MemoryType, MemoryID: memory.MemoryID, Relation: memory.Relation,
			Content: content, Status: memory.Status,
		})
	}
	brief.MoreMemory = nonFactTotal > len(brief.RelevantMemory)
	brief.MoreChangedFacts = factTotal > len(brief.ChangedFacts)
	for _, resource := range bundle.Resources {
		if len(brief.Resources) >= agentBriefResourceLimit {
			break
		}
		resource.ResourceKey, _ = truncateWorkResumeString(strings.TrimSpace(resource.ResourceKey), 256)
		resource.Title, _ = truncateWorkResumeString(strings.TrimSpace(resource.Title), 256)
		resource.URI, _ = truncateWorkResumeString(strings.TrimSpace(resource.URI), 512)
		resource.Summary, _ = truncateWorkResumeString(strings.TrimSpace(resource.Summary), 384)
		resource.ExternalID, _ = truncateWorkResumeString(strings.TrimSpace(resource.ExternalID), 128)
		resource.Revision, _ = truncateWorkResumeString(strings.TrimSpace(resource.Revision), 128)
		brief.Resources = append(brief.Resources, resource)
	}
	brief.MoreResources = len(bundle.Resources) > len(brief.Resources)
	for _, dependency := range bundle.DependencyResults {
		if len(brief.DependencyResults) >= agentBriefDependencyLimit {
			break
		}
		dependency.WorkItem.IssueKey, _ = truncateWorkResumeString(strings.TrimSpace(dependency.WorkItem.IssueKey), 96)
		dependency.WorkItem.Title, _ = truncateWorkResumeString(strings.TrimSpace(dependency.WorkItem.Title), 256)
		dependency.WorkItem.Owner, _ = truncateWorkResumeString(strings.TrimSpace(dependency.WorkItem.Owner), 128)
		dependency.Summary, _ = truncateWorkResumeString(strings.TrimSpace(dependency.Summary), 384)
		dependency.Result, _ = truncateWorkResumeString(strings.TrimSpace(dependency.Result), 512)
		brief.DependencyResults = append(brief.DependencyResults, dependency)
	}
	brief.MoreDependencyResults = len(bundle.DependencyResults) > len(brief.DependencyResults)
	for _, blocker := range bundle.Blockers {
		if len(brief.Blockers) >= agentBriefBlockerLimit {
			break
		}
		brief.Blockers = append(brief.Blockers, agentWorkItem(blocker))
	}
	brief.MoreBlockers = len(bundle.Blockers) > len(brief.Blockers)
	brief.WorkItem.NextAction = brief.NextAction
	// Hash the complete authorized state, including records omitted from the
	// brief, so a matching receipt never hides a change beyond a brief limit.
	digest, err := agentContextDigest(bundle)
	if err != nil {
		return nil, err
	}
	brief.ContextDigest = digest
	brief.ContextWindow = models.AgentContextWindow{
		InputLimitBytes: agentBriefMaxBytes,
		NextQuery: models.AgentContextNextQuery{
			KnownContextDigest: brief.ContextDigest, FactOffset: 0, Detail: "brief",
		},
	}
	finalizeWorkResumeBrief(brief, agentBriefMaxBytes)
	return brief, nil
}

// fitWorkResumeBrief enforces a small worker-input envelope even when every
// bounded collection contains unusually long text. Items are already ordered
// by usefulness, so removing from the tail preserves the most relevant entry.
func fitWorkResumeBrief(brief *models.WorkResumeBrief, maxBytes int) {
	if brief == nil {
		return
	}
	if maxBytes <= 0 {
		maxBytes = agentBriefMaxBytes
	}
	type removable struct {
		name string
		size int
	}
	allowEmpty := false
	for {
		payload, err := json.Marshal(brief)
		if err != nil || len(payload) <= maxBytes {
			return
		}
		candidates := make([]removable, 0, 7)
		appendCandidate := func(name string, value any, count, minimum int) {
			if count <= minimum {
				return
			}
			encoded, _ := json.Marshal(value)
			candidates = append(candidates, removable{name: name, size: len(encoded)})
		}
		minimum := 1
		if allowEmpty {
			minimum = 0
		}
		if len(brief.CompletionConditions) > 0 {
			appendCandidate("conditions", brief.CompletionConditions[len(brief.CompletionConditions)-1], len(brief.CompletionConditions), minimum)
		}
		if len(brief.EvidenceReferences) > 0 {
			appendCandidate("evidence", brief.EvidenceReferences[len(brief.EvidenceReferences)-1], len(brief.EvidenceReferences), minimum)
		}
		if len(brief.RelevantMemory) > 0 {
			appendCandidate("memory", brief.RelevantMemory[len(brief.RelevantMemory)-1], len(brief.RelevantMemory), minimum)
		}
		if len(brief.ChangedFacts) > 0 {
			// A page must always advance. Dropping the final fact would return the
			// same offset forever at the smallest supported input limit.
			appendCandidate("facts", brief.ChangedFacts[len(brief.ChangedFacts)-1], len(brief.ChangedFacts), 1)
		}
		if len(brief.Resources) > 0 {
			appendCandidate("resources", brief.Resources[len(brief.Resources)-1], len(brief.Resources), minimum)
		}
		if len(brief.DependencyResults) > 0 {
			appendCandidate("dependencies", brief.DependencyResults[len(brief.DependencyResults)-1], len(brief.DependencyResults), minimum)
		}
		if len(brief.Blockers) > 0 {
			appendCandidate("blockers", brief.Blockers[len(brief.Blockers)-1], len(brief.Blockers), minimum)
		}
		if len(candidates) == 0 {
			if compactWorkResumeBrief(brief) {
				continue
			}
			if !allowEmpty {
				allowEmpty = true
				continue
			}
			return
		}
		sort.SliceStable(candidates, func(left, right int) bool { return candidates[left].size > candidates[right].size })
		switch candidates[0].name {
		case "conditions":
			brief.CompletionConditions = brief.CompletionConditions[:len(brief.CompletionConditions)-1]
			brief.MoreConditions = true
		case "evidence":
			brief.EvidenceReferences = brief.EvidenceReferences[:len(brief.EvidenceReferences)-1]
			brief.MoreEvidence = true
		case "memory":
			brief.RelevantMemory = brief.RelevantMemory[:len(brief.RelevantMemory)-1]
			brief.MoreMemory = true
		case "facts":
			brief.ChangedFacts = brief.ChangedFacts[:len(brief.ChangedFacts)-1]
			brief.MoreChangedFacts = true
		case "resources":
			brief.Resources = brief.Resources[:len(brief.Resources)-1]
			brief.MoreResources = true
		case "dependencies":
			brief.DependencyResults = brief.DependencyResults[:len(brief.DependencyResults)-1]
			brief.MoreDependencyResults = true
		case "blockers":
			brief.Blockers = brief.Blockers[:len(brief.Blockers)-1]
			brief.MoreBlockers = true
		}
	}
}

func compactWorkResumeBrief(brief *models.WorkResumeBrief) bool {
	if brief == nil {
		return false
	}
	changed := false
	compact := func(value *string, limit int) {
		if value == nil {
			return
		}
		shortened, _ := truncateWorkResumeString(strings.TrimSpace(*value), limit)
		if shortened != *value {
			*value = shortened
			changed = true
		}
	}
	compact(&brief.WorkItem.IssueKey, 48)
	compact(&brief.WorkItem.Title, 96)
	compact(&brief.WorkItem.Owner, 48)
	compact(&brief.WorkItem.NextAction, 96)
	compact(&brief.NextAction, 128)
	if len(brief.WorkItem.RequiredCapabilities) > 2 {
		brief.WorkItem.RequiredCapabilities = brief.WorkItem.RequiredCapabilities[:2]
		changed = true
	}
	for index := range brief.WorkItem.RequiredCapabilities {
		compact(&brief.WorkItem.RequiredCapabilities[index], 48)
	}
	if brief.SharedGoal != nil {
		compact(&brief.SharedGoal.Content, 96)
	}
	if brief.GoalContext != nil {
		if len(brief.GoalContext.Path) > 2 {
			brief.GoalContext.Path = []models.GoalBrief{brief.GoalContext.Path[0], brief.GoalContext.Path[len(brief.GoalContext.Path)-1]}
			brief.GoalContext.PathTruncated = true
			changed = true
		}
		if len(brief.GoalContext.Siblings) > 1 {
			brief.GoalContext.Siblings = brief.GoalContext.Siblings[:1]
			brief.GoalContext.SiblingsTruncated = true
			changed = true
		}
		for index := range brief.GoalContext.Path {
			compact(&brief.GoalContext.Path[index].Content, 80)
		}
		for index := range brief.GoalContext.Siblings {
			compact(&brief.GoalContext.Siblings[index].Content, 64)
		}
	}
	if brief.PlanContext != nil {
		compact(&brief.PlanContext.Component.IssueKey, 48)
		compact(&brief.PlanContext.Component.Title, 80)
		compact(&brief.PlanContext.Outcome, 80)
		compact(&brief.PlanContext.Guidance, 80)
		compact(&brief.PlanContext.TaskDetails, 80)
		if len(brief.PlanContext.OwnedScopes) > 1 {
			brief.PlanContext.OwnedScopes = brief.PlanContext.OwnedScopes[:1]
			brief.PlanContext.MoreOwnedScopes = true
			changed = true
		}
		for index := range brief.PlanContext.OwnedScopes {
			compact(&brief.PlanContext.OwnedScopes[index], 96)
		}
	}
	if brief.LatestAttempt != nil {
		compact(&brief.LatestAttempt.AgentID, 48)
	}
	if brief.LatestCheckpoint != nil {
		compact(&brief.LatestCheckpoint.Summary, 80)
		compact(&brief.LatestCheckpoint.Result, 80)
		compact(&brief.LatestCheckpoint.NextAction, 80)
	}
	for index := range brief.CompletionConditions {
		compact(&brief.CompletionConditions[index].Description, 80)
	}
	for index := range brief.EvidenceReferences {
		compact(&brief.EvidenceReferences[index].EvidenceType, 48)
		compact(&brief.EvidenceReferences[index].Summary, 80)
		compact(&brief.EvidenceReferences[index].Reference, 96)
	}
	for index := range brief.RelevantMemory {
		compact(&brief.RelevantMemory[index].Content, 80)
	}
	for index := range brief.ChangedFacts {
		compact(&brief.ChangedFacts[index].Content, 80)
	}
	for index := range brief.Resources {
		compact(&brief.Resources[index].ResourceKey, 64)
		compact(&brief.Resources[index].Title, 80)
		compact(&brief.Resources[index].URI, 96)
		compact(&brief.Resources[index].Summary, 80)
	}
	for index := range brief.DependencyResults {
		compact(&brief.DependencyResults[index].WorkItem.Title, 80)
		compact(&brief.DependencyResults[index].Summary, 80)
		compact(&brief.DependencyResults[index].Result, 80)
	}
	for index := range brief.Blockers {
		compact(&brief.Blockers[index].Title, 80)
	}
	if changed {
		brief.ContextWindow.Truncated = true
		return true
	}
	if brief.PlanContext != nil {
		brief.PlanContext = nil
		brief.ContextWindow.Truncated = true
		return true
	}
	if brief.GoalContext != nil {
		brief.GoalContext = nil
		brief.ContextWindow.Truncated = true
		return true
	}
	if brief.LatestAttempt != nil {
		brief.LatestAttempt = nil
		brief.ContextWindow.Truncated = true
		return true
	}
	if brief.LatestCheckpoint != nil {
		brief.LatestCheckpoint = nil
		brief.ContextWindow.Truncated = true
		return true
	}
	compact(&brief.WorkItem.IssueKey, 32)
	compact(&brief.WorkItem.Title, 16)
	compact(&brief.WorkItem.Owner, 32)
	if brief.WorkItem.NextAction != "" {
		// The same value remains in the top-level next_action field.
		brief.WorkItem.NextAction = ""
		changed = true
	}
	compact(&brief.NextAction, 16)
	if brief.SharedGoal != nil {
		compact(&brief.SharedGoal.Content, 16)
	}
	if len(brief.WorkItem.RequiredCapabilities) > 1 {
		brief.WorkItem.RequiredCapabilities = brief.WorkItem.RequiredCapabilities[:1]
		changed = true
	}
	for index := range brief.WorkItem.RequiredCapabilities {
		compact(&brief.WorkItem.RequiredCapabilities[index], 32)
	}
	if changed {
		brief.ContextWindow.Truncated = true
		return true
	}
	return false
}

func workResumeBriefTruncated(brief *models.WorkResumeBrief) bool {
	return brief.MoreConditions || brief.MoreEvidence || brief.MoreMemory || brief.MoreChangedFacts ||
		brief.MoreResources || brief.MoreDependencyResults || brief.MoreBlockers
}

func finalizeWorkResumeBrief(brief *models.WorkResumeBrief, maxBytes int) {
	if brief == nil {
		return
	}
	if maxBytes <= 0 || maxBytes > agentBriefMaxBytes {
		maxBytes = agentBriefMaxBytes
	}
	brief.ContextWindow.InputLimitBytes = maxBytes
	for iteration := 0; iteration < 8; iteration++ {
		truncated := brief.ContextWindow.Truncated
		fitWorkResumeBrief(brief, maxBytes)
		brief.ContextWindow.Truncated = truncated || brief.ContextWindow.Truncated || workResumeBriefTruncated(brief)
		if brief.ContextWindow.Truncated && brief.ContextWindow.NextQuery.Detail == "brief" && !brief.MoreChangedFacts {
			brief.ContextWindow.NextQuery.Detail = "full"
		}
		payload, err := json.Marshal(brief)
		if err != nil {
			return
		}
		if brief.ContextWindow.InputBytes == len(payload) && len(payload) <= maxBytes {
			return
		}
		brief.ContextWindow.InputBytes = len(payload)
	}
}

func workContextDigest(bundle *models.WorkResumeBundle, states []models.WorkContextFactState) (string, error) {
	type factMarker struct {
		FactID      int64  `json:"fact_id"`
		Relation    string `json:"relation"`
		Status      string `json:"status"`
		StateDigest string `json:"state_digest"`
	}
	markers := make([]factMarker, 0, len(states))
	for _, state := range states {
		markers = append(markers, factMarker{
			FactID: state.FactID, Relation: state.Relation, Status: state.Status, StateDigest: state.StateDigest,
		})
	}
	return agentContextDigest(struct {
		Bundle *models.WorkResumeBundle `json:"bundle"`
		Facts  []factMarker             `json:"facts"`
	}{Bundle: bundle, Facts: markers})
}

func workContextInputLimit(bcMaxBytes int) int {
	if bcMaxBytes > 0 && bcMaxBytes < agentBriefMaxBytes {
		return bcMaxBytes
	}
	return agentBriefMaxBytes
}

func applyWorkContextFactDiff(brief *models.WorkResumeBrief, diff *models.WorkContextFactDiff, knownDigest, expectedDigest string, factOffset, maxBytes int) (bool, error) {
	if brief == nil || diff == nil {
		return false, fmt.Errorf("work context is unavailable")
	}
	if factOffset < 0 {
		return false, fmt.Errorf("argument %q must be zero or greater", "fact_offset")
	}
	cursorReset := strings.TrimSpace(knownDigest) != "" && !diff.BaselineFound
	if factOffset > 0 && strings.TrimSpace(expectedDigest) != brief.ContextDigest {
		factOffset = 0
		cursorReset = true
	}
	if factOffset > len(diff.Changes) {
		return false, fmt.Errorf("argument %q exceeds the %d available fact changes", "fact_offset", len(diff.Changes))
	}

	end := factOffset + agentBriefFactChangeLimit
	if end > len(diff.Changes) {
		end = len(diff.Changes)
	}
	brief.ChangedFacts = append([]models.WorkContextFactChange(nil), diff.Changes[factOffset:end]...)
	for index := range brief.ChangedFacts {
		content, shortened := truncateWorkResumeString(strings.TrimSpace(brief.ChangedFacts[index].Content), 384)
		brief.ChangedFacts[index].Content = content
		brief.ChangedFacts[index].ContentTruncated = brief.ChangedFacts[index].ContentTruncated || shortened
	}
	brief.MoreChangedFacts = end < len(diff.Changes)
	brief.ContextWindow = models.AgentContextWindow{
		InputLimitBytes: maxBytes,
		CursorReset:     cursorReset,
		NextQuery: models.AgentContextNextQuery{
			KnownContextDigest: brief.ContextDigest, FactOffset: 0, Detail: "brief",
		},
	}
	if brief.MoreChangedFacts {
		brief.ContextWindow.NextQuery = models.AgentContextNextQuery{
			KnownContextDigest: knownDigest, ExpectedContextDigest: brief.ContextDigest,
			FactOffset: end, Detail: "brief",
		}
	}
	finalizeWorkResumeBrief(brief, maxBytes)

	// The byte fitter may have removed additional fact changes. Advance only by
	// the entries actually returned so a continuation cannot skip a fact.
	returnedEnd := factOffset + len(brief.ChangedFacts)
	complete := returnedEnd >= len(diff.Changes)
	brief.MoreChangedFacts = !complete
	if !complete {
		brief.ContextWindow.NextQuery = models.AgentContextNextQuery{
			KnownContextDigest: knownDigest, ExpectedContextDigest: brief.ContextDigest,
			FactOffset: returnedEnd, Detail: "brief",
		}
	} else {
		brief.ContextWindow.NextQuery = models.AgentContextNextQuery{
			KnownContextDigest: brief.ContextDigest, FactOffset: 0, Detail: "brief",
		}
	}
	finalizeWorkResumeBrief(brief, maxBytes)
	return complete, nil
}

func finalizeAgentContextReceipt(receipt *models.AgentContextReceipt, maxBytes int) {
	if receipt == nil {
		return
	}
	window := &models.AgentContextWindow{
		InputLimitBytes: maxBytes,
		NextQuery: models.AgentContextNextQuery{
			KnownContextDigest: receipt.ContextDigest, FactOffset: 0, Detail: "brief",
		},
	}
	receipt.ContextWindow = window
	for iteration := 0; iteration < 4; iteration++ {
		payload, err := json.Marshal(receipt)
		if err != nil || window.InputBytes == len(payload) {
			return
		}
		window.InputBytes = len(payload)
	}
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
