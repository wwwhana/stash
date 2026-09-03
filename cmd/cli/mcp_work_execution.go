package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/brain"
	"github.com/alash3al/stash/internal/models"
	"github.com/alash3al/stash/internal/observability"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	maxWorkToolStructuredBytes = 64 * 1024
	minStartActionKeyChars     = 32
	startActionUUIDPatternText = `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}`
)

var recordWorkExecutionTransition = observability.RecordWorkExecution
var startActionUUIDPattern = regexp.MustCompile(startActionUUIDPatternText)

type completionConditionArgument struct {
	Kind         string          `json:"kind"`
	Description  string          `json:"description"`
	Verification json.RawMessage `json:"verification"`
	Required     *bool           `json:"required,omitempty"`
}

func structuredArgumentBytes(request mcp.CallToolRequest, key string, required bool) ([]byte, error) {
	raw, ok := request.GetArguments()[key]
	if !ok || raw == nil {
		if required {
			return nil, fmt.Errorf("argument %q is required", key)
		}
		return nil, nil
	}
	var data []byte
	if value, ok := raw.(string); ok {
		data = []byte(strings.TrimSpace(value))
	} else {
		var err error
		data, err = json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("argument %q must be valid JSON: %w", key, err)
		}
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("argument %q must not be empty", key)
	}
	if len(data) > maxWorkToolStructuredBytes {
		return nil, fmt.Errorf("argument %q exceeds %d bytes", key, maxWorkToolStructuredBytes)
	}
	return data, nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return fmt.Errorf("multiple JSON values are not allowed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return fmt.Errorf("multiple JSON values are not allowed")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func parseCompletionConditionInputs(request mcp.CallToolRequest) ([]brain.CompletionConditionInput, error) {
	data, err := structuredArgumentBytes(request, "conditions", true)
	if err != nil {
		return nil, err
	}
	var arguments []completionConditionArgument
	if err := decodeStrictJSON(data, &arguments); err != nil {
		return nil, fmt.Errorf("argument %q must be an array of completion conditions: %w", "conditions", err)
	}
	if len(arguments) == 0 || len(arguments) > 100 {
		return nil, fmt.Errorf("argument %q must contain between 1 and 100 conditions", "conditions")
	}
	conditions := make([]brain.CompletionConditionInput, 0, len(arguments))
	for i, argument := range arguments {
		kind := strings.TrimSpace(argument.Kind)
		if kind == "" {
			return nil, fmt.Errorf("argument %q condition %d requires a kind", "conditions", i+1)
		}
		description := strings.TrimSpace(argument.Description)
		if description == "" {
			return nil, fmt.Errorf("argument %q condition %d requires a description", "conditions", i+1)
		}
		var verification map[string]json.RawMessage
		if len(argument.Verification) == 0 {
			return nil, fmt.Errorf("argument %q condition %d requires a verification object", "conditions", i+1)
		}
		if err := decodeStrictJSON(argument.Verification, &verification); err != nil || len(verification) == 0 {
			if err == nil {
				err = fmt.Errorf("object must not be empty")
			}
			return nil, fmt.Errorf("argument %q condition %d verification must be a non-empty JSON object: %w", "conditions", i+1, err)
		}
		canonicalVerification, err := json.Marshal(verification)
		if err != nil {
			return nil, fmt.Errorf("argument %q condition %d verification must be a JSON object: %w", "conditions", i+1, err)
		}
		required := true
		if argument.Required != nil {
			required = *argument.Required
		}
		conditions = append(conditions, brain.CompletionConditionInput{
			Kind:         kind,
			Description:  description,
			Verification: canonicalVerification,
			Required:     required,
		})
	}
	return conditions, nil
}

func parseEvidencePayload(request mcp.CallToolRequest) (json.RawMessage, error) {
	data, err := structuredArgumentBytes(request, "payload", false)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]json.RawMessage
	if err := decodeStrictJSON(data, &object); err != nil || object == nil {
		if err == nil {
			err = fmt.Errorf("null is not an object")
		}
		return nil, fmt.Errorf("argument %q must be a JSON object: %w", "payload", err)
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("argument %q must be a JSON object: %w", "payload", err)
	}
	return canonical, nil
}

func requiredStartActionKey(request mcp.CallToolRequest) (string, error) {
	actionKey, err := requiredWorkString(request, "action_key")
	if err != nil {
		return "", err
	}
	if len(actionKey) < minStartActionKeyChars {
		return "", fmt.Errorf("argument %q must contain at least %d characters and a fresh random UUIDv4", "action_key", minStartActionKeyChars)
	}
	if !startActionUUIDPattern.MatchString(actionKey) {
		return "", fmt.Errorf("argument %q must contain a fresh random UUIDv4", "action_key")
	}
	return actionKey, nil
}

func workExecutionPrincipal(ctx context.Context) (string, error) {
	mode, _ := ctx.Value(keyMode).(string)
	if mode != "remote" {
		return "", nil
	}
	principalID, _ := ctx.Value(keySSOUser).(string)
	if strings.TrimSpace(principalID) == "" {
		return "", fmt.Errorf("unauthorized: verified identity is required")
	}
	return namespaceOwnerKey(principalID), nil
}

func recordWorkExecutionHandler(action string, next server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (result *mcp.CallToolResult, err error) {
		defer func() {
			outcome := "success"
			if err != nil || (result != nil && result.IsError) {
				outcome = "rejected"
			}
			recordWorkExecutionTransition(action, outcome)
		}()
		return next(ctx, request)
	}
}

func truncateWorkResumeString(value string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value, false
	}
	end := maxBytes - len("…")
	if end < 1 {
		end = maxBytes
	}
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	if end == 0 {
		return "", true
	}
	if end+len("…") <= maxBytes {
		return value[:end] + "…", true
	}
	return value[:end], true
}

func compactWorkResumeRawJSON(value json.RawMessage, maxBytes int) (json.RawMessage, bool) {
	if len(value) <= maxBytes {
		return value, false
	}
	return json.RawMessage(`{"truncated":true}`), true
}

func compactWorkResumeBundle(bundle *models.WorkResumeBundle, maxBytes int) *models.WorkResumeBundle {
	if bundle == nil {
		return nil
	}
	compact := *bundle
	compact.WorkItem.Labels = append([]string(nil), bundle.WorkItem.Labels...)
	compact.WorkItem.WorktreeIDs = append([]int64(nil), bundle.WorkItem.WorktreeIDs...)
	compact.CompletionConditions = append([]models.WorkCompletionCondition(nil), bundle.CompletionConditions...)
	compact.Evidence = append([]models.WorkEvidence(nil), bundle.Evidence...)
	compact.WorktreeLinks = append([]models.WorktreeLink(nil), bundle.WorktreeLinks...)
	compact.MemoryLinks = append([]models.WorkMemorySnapshot(nil), bundle.MemoryLinks...)
	compact.Blockers = append([]models.WorkItem(nil), bundle.Blockers...)
	compact.RecentEvents = append([]models.WorkEvent(nil), bundle.RecentEvents...)
	if bundle.GoalContext != nil {
		goalContext := *bundle.GoalContext
		goalContext.Path = append([]models.GoalBrief(nil), bundle.GoalContext.Path...)
		goalContext.Siblings = append([]models.GoalBrief(nil), bundle.GoalContext.Siblings...)
		compact.GoalContext = &goalContext
	}
	if bundle.PlanContext != nil {
		planContext := *bundle.PlanContext
		planContext.OwnedScopes = append([]string(nil), bundle.PlanContext.OwnedScopes...)
		compact.PlanContext = &planContext
	}
	if bundle.LatestAttempt != nil {
		attempt := *bundle.LatestAttempt
		compact.LatestAttempt = &attempt
	}
	if bundle.LatestCheckpoint != nil {
		checkpoint := *bundle.LatestCheckpoint
		compact.LatestCheckpoint = &checkpoint
	}

	var shortened bool
	compact.WorkItem.Title, shortened = truncateWorkResumeString(compact.WorkItem.Title, 2048)
	compact.Truncated.Core = compact.Truncated.Core || shortened
	compact.WorkItem.Description, shortened = truncateWorkResumeString(compact.WorkItem.Description, 4096)
	compact.Truncated.Core = compact.Truncated.Core || shortened
	compact.WorkItem.Reporter, shortened = truncateWorkResumeString(compact.WorkItem.Reporter, 512)
	compact.Truncated.Core = compact.Truncated.Core || shortened
	compact.WorkItem.Owner, shortened = truncateWorkResumeString(compact.WorkItem.Owner, 512)
	compact.Truncated.Core = compact.Truncated.Core || shortened
	if len(compact.WorkItem.Labels) > 20 {
		compact.WorkItem.Labels = compact.WorkItem.Labels[:20]
		compact.Truncated.Core = true
	}
	for i := range compact.WorkItem.Labels {
		compact.WorkItem.Labels[i], shortened = truncateWorkResumeString(compact.WorkItem.Labels[i], 128)
		compact.Truncated.Core = compact.Truncated.Core || shortened
	}
	compact.NextAction, shortened = truncateWorkResumeString(compact.NextAction, 2048)
	compact.Truncated.Core = compact.Truncated.Core || shortened
	if compact.GoalContext != nil {
		for index := range compact.GoalContext.Path {
			compact.GoalContext.Path[index].Content, shortened = truncateWorkResumeString(compact.GoalContext.Path[index].Content, 256)
			compact.Truncated.Core = compact.Truncated.Core || shortened
		}
		for index := range compact.GoalContext.Siblings {
			compact.GoalContext.Siblings[index].Content, shortened = truncateWorkResumeString(compact.GoalContext.Siblings[index].Content, 192)
			compact.Truncated.Core = compact.Truncated.Core || shortened
		}
	}
	if compact.PlanContext != nil {
		compact.PlanContext.Component.IssueKey, shortened = truncateWorkResumeString(compact.PlanContext.Component.IssueKey, 128)
		compact.Truncated.Core = compact.Truncated.Core || shortened
		compact.PlanContext.Component.Title, shortened = truncateWorkResumeString(compact.PlanContext.Component.Title, 512)
		compact.Truncated.Core = compact.Truncated.Core || shortened
		compact.PlanContext.Outcome, shortened = truncateWorkResumeString(compact.PlanContext.Outcome, 1024)
		compact.Truncated.Core = compact.Truncated.Core || shortened
		compact.PlanContext.Guidance, shortened = truncateWorkResumeString(compact.PlanContext.Guidance, 1024)
		compact.Truncated.Core = compact.Truncated.Core || shortened
		compact.PlanContext.TaskDetails, shortened = truncateWorkResumeString(compact.PlanContext.TaskDetails, 1024)
		compact.Truncated.Core = compact.Truncated.Core || shortened
		if len(compact.PlanContext.OwnedScopes) > 16 {
			compact.PlanContext.OwnedScopes = compact.PlanContext.OwnedScopes[:16]
			compact.PlanContext.MoreOwnedScopes = true
			compact.Truncated.Core = true
		}
		for index := range compact.PlanContext.OwnedScopes {
			compact.PlanContext.OwnedScopes[index], shortened = truncateWorkResumeString(compact.PlanContext.OwnedScopes[index], 512)
			compact.Truncated.Core = compact.Truncated.Core || shortened
		}
	}
	if compact.LatestAttempt != nil {
		compact.LatestAttempt.AgentID, shortened = truncateWorkResumeString(compact.LatestAttempt.AgentID, 512)
		compact.Truncated.Core = compact.Truncated.Core || shortened
		compact.LatestAttempt.PrincipalID, shortened = truncateWorkResumeString(compact.LatestAttempt.PrincipalID, 512)
		compact.Truncated.Core = compact.Truncated.Core || shortened
	}
	if compact.LatestCheckpoint != nil {
		compact.LatestCheckpoint.Summary, shortened = truncateWorkResumeString(compact.LatestCheckpoint.Summary, 2048)
		compact.Truncated.Core = compact.Truncated.Core || shortened
		compact.LatestCheckpoint.Result, shortened = truncateWorkResumeString(compact.LatestCheckpoint.Result, 2048)
		compact.Truncated.Core = compact.Truncated.Core || shortened
		compact.LatestCheckpoint.NextAction, shortened = truncateWorkResumeString(compact.LatestCheckpoint.NextAction, 2048)
		compact.Truncated.Core = compact.Truncated.Core || shortened
	}
	for i := range compact.CompletionConditions {
		condition := &compact.CompletionConditions[i]
		condition.Description, shortened = truncateWorkResumeString(condition.Description, 1024)
		compact.Truncated.CompletionConditions = compact.Truncated.CompletionConditions || shortened
		condition.WaiverReason, shortened = truncateWorkResumeString(condition.WaiverReason, 512)
		compact.Truncated.CompletionConditions = compact.Truncated.CompletionConditions || shortened
		condition.Verification, shortened = compactWorkResumeRawJSON(condition.Verification, 2048)
		compact.Truncated.CompletionConditions = compact.Truncated.CompletionConditions || shortened
	}
	for i := range compact.Evidence {
		evidence := &compact.Evidence[i]
		evidence.EvidenceType, shortened = truncateWorkResumeString(evidence.EvidenceType, 128)
		compact.Truncated.Evidence = compact.Truncated.Evidence || shortened
		evidence.Summary, shortened = truncateWorkResumeString(evidence.Summary, 1024)
		compact.Truncated.Evidence = compact.Truncated.Evidence || shortened
		evidence.Reference, shortened = truncateWorkResumeString(evidence.Reference, 512)
		compact.Truncated.Evidence = compact.Truncated.Evidence || shortened
		evidence.Payload, shortened = compactWorkResumeRawJSON(evidence.Payload, 2048)
		compact.Truncated.Evidence = compact.Truncated.Evidence || shortened
		evidence.PrincipalID, shortened = truncateWorkResumeString(evidence.PrincipalID, 512)
		compact.Truncated.Evidence = compact.Truncated.Evidence || shortened
	}
	for i := range compact.WorktreeLinks {
		link := &compact.WorktreeLinks[i]
		link.Worktree.Repository, shortened = truncateWorkResumeString(link.Worktree.Repository, 512)
		compact.Truncated.WorktreeLinks = compact.Truncated.WorktreeLinks || shortened
		link.Worktree.WorktreePath, shortened = truncateWorkResumeString(link.Worktree.WorktreePath, 1024)
		compact.Truncated.WorktreeLinks = compact.Truncated.WorktreeLinks || shortened
		link.Worktree.Branch, shortened = truncateWorkResumeString(link.Worktree.Branch, 512)
		compact.Truncated.WorktreeLinks = compact.Truncated.WorktreeLinks || shortened
		link.Worktree.AgentID, shortened = truncateWorkResumeString(link.Worktree.AgentID, 512)
		compact.Truncated.WorktreeLinks = compact.Truncated.WorktreeLinks || shortened
		link.Worktree.Metadata, shortened = compactWorkResumeRawJSON(link.Worktree.Metadata, 1024)
		compact.Truncated.WorktreeLinks = compact.Truncated.WorktreeLinks || shortened
	}
	for i := range compact.MemoryLinks {
		memory := &compact.MemoryLinks[i]
		memory.Content, shortened = truncateWorkResumeString(memory.Content, 2048)
		memory.ContentTruncated = memory.ContentTruncated || shortened
		compact.Truncated.MemoryLinks = compact.Truncated.MemoryLinks || memory.ContentTruncated
		memory.Status, shortened = truncateWorkResumeString(memory.Status, 128)
		compact.Truncated.MemoryLinks = compact.Truncated.MemoryLinks || shortened
		memory.Relation, shortened = truncateWorkResumeString(memory.Relation, 128)
		compact.Truncated.MemoryLinks = compact.Truncated.MemoryLinks || shortened
	}
	for i := range compact.Blockers {
		blocker := &compact.Blockers[i]
		blocker.Title, shortened = truncateWorkResumeString(blocker.Title, 512)
		compact.Truncated.Blockers = compact.Truncated.Blockers || shortened
		blocker.Description, shortened = truncateWorkResumeString(blocker.Description, 1024)
		compact.Truncated.Blockers = compact.Truncated.Blockers || shortened
		blocker.Reporter, shortened = truncateWorkResumeString(blocker.Reporter, 256)
		compact.Truncated.Blockers = compact.Truncated.Blockers || shortened
		blocker.Owner, shortened = truncateWorkResumeString(blocker.Owner, 256)
		compact.Truncated.Blockers = compact.Truncated.Blockers || shortened
	}
	for i := range compact.RecentEvents {
		event := &compact.RecentEvents[i]
		event.EventType, shortened = truncateWorkResumeString(event.EventType, 256)
		compact.Truncated.RecentEvents = compact.Truncated.RecentEvents || shortened
		event.Payload, shortened = compactWorkResumeRawJSON(event.Payload, 1024)
		compact.Truncated.RecentEvents = compact.Truncated.RecentEvents || shortened
		if event.EventKey != nil {
			key, keyShortened := truncateWorkResumeString(*event.EventKey, 512)
			event.EventKey = &key
			compact.Truncated.RecentEvents = compact.Truncated.RecentEvents || keyShortened
		}
	}

	encodedSize := func() int {
		payload, err := json.Marshal(&compact)
		if err != nil {
			return maxBytes + 1
		}
		return len(payload)
	}
	for encodedSize() > maxBytes {
		switch {
		case len(compact.RecentEvents) > 0:
			compact.RecentEvents = compact.RecentEvents[:len(compact.RecentEvents)-1]
			compact.Truncated.RecentEvents = true
		case len(compact.MemoryLinks) > 0:
			compact.MemoryLinks = compact.MemoryLinks[:len(compact.MemoryLinks)-1]
			compact.Truncated.MemoryLinks = true
		case len(compact.WorktreeLinks) > 0:
			compact.WorktreeLinks = compact.WorktreeLinks[:len(compact.WorktreeLinks)-1]
			compact.Truncated.WorktreeLinks = true
		case len(compact.Blockers) > 0:
			compact.Blockers = compact.Blockers[:len(compact.Blockers)-1]
			compact.Truncated.Blockers = true
		case len(compact.Evidence) > 0:
			compact.Evidence = compact.Evidence[:len(compact.Evidence)-1]
			compact.Truncated.Evidence = true
		case len(compact.CompletionConditions) > 0:
			compact.CompletionConditions = compact.CompletionConditions[:len(compact.CompletionConditions)-1]
			compact.Truncated.CompletionConditions = true
		case compact.GoalContext != nil && len(compact.GoalContext.Siblings) > 0:
			compact.GoalContext.Siblings = compact.GoalContext.Siblings[:len(compact.GoalContext.Siblings)-1]
			compact.GoalContext.SiblingsTruncated = true
			compact.Truncated.Core = true
		case compact.GoalContext != nil && len(compact.GoalContext.Path) > 2:
			compact.GoalContext.Path = append(compact.GoalContext.Path[:1], compact.GoalContext.Path[2:]...)
			compact.GoalContext.PathTruncated = true
			compact.Truncated.Core = true
		default:
			compact.Truncated.Core = true
			compact.WorkItem.Labels = nil
			compact.WorkItem.WorktreeIDs = nil
			compact.WorkItem.Title, _ = truncateWorkResumeString(compact.WorkItem.Title, 256)
			compact.WorkItem.Description, _ = truncateWorkResumeString(compact.WorkItem.Description, 512)
			compact.WorkItem.IssueKey, _ = truncateWorkResumeString(compact.WorkItem.IssueKey, 128)
			compact.WorkItem.Reporter, _ = truncateWorkResumeString(compact.WorkItem.Reporter, 128)
			compact.WorkItem.Owner, _ = truncateWorkResumeString(compact.WorkItem.Owner, 128)
			compact.NextAction, _ = truncateWorkResumeString(compact.NextAction, 512)
			if compact.LatestCheckpoint != nil {
				compact.LatestCheckpoint.Summary, _ = truncateWorkResumeString(compact.LatestCheckpoint.Summary, 256)
				compact.LatestCheckpoint.Result, _ = truncateWorkResumeString(compact.LatestCheckpoint.Result, 256)
				compact.LatestCheckpoint.NextAction, _ = truncateWorkResumeString(compact.LatestCheckpoint.NextAction, 256)
			}
			return &compact
		}
	}
	return &compact
}

func workResumeToolResult(bc *bootstrap.Context, bundle *models.WorkResumeBundle) (*mcp.CallToolResult, error) {
	maxBytes := defaultMCPMaxResponseBytes
	if bc != nil && bc.Config != nil && bc.Config.MCPMaxResponseBytes > 0 {
		maxBytes = bc.Config.MCPMaxResponseBytes
	}
	return jsonToolResult(bc, compactWorkResumeBundle(bundle, maxBytes))
}

type workResumeCollectionPage struct {
	WorkItemID int64             `json:"work_item_id"`
	Collection string            `json:"collection"`
	Detail     string            `json:"detail"`
	Offset     int               `json:"offset"`
	Items      []json.RawMessage `json:"items"`
	HasMore    bool              `json:"has_more"`
	NextOffset int               `json:"next_offset,omitempty"`
	NextQuery  string            `json:"next_query,omitempty"`
}

func workResumeCollectionItems(bundle *models.WorkResumeBundle, collection string) ([]json.RawMessage, error) {
	var value any
	switch collection {
	case "completion_conditions":
		value = bundle.CompletionConditions
	case "evidence":
		value = bundle.Evidence
	case "memories":
		value = bundle.MemoryLinks
	case "resources":
		value = bundle.Resources
	case "worktree_links":
		value = bundle.WorktreeLinks
	case "dependency_results":
		value = bundle.DependencyResults
	case "blockers":
		value = bundle.Blockers
	default:
		return nil, fmt.Errorf("argument %q has unsupported value %q", "collection", collection)
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(payload, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func compactWorkResumeCollectionItem(collection string, raw json.RawMessage, maxBytes int) (json.RawMessage, error) {
	if len(raw) <= maxBytes {
		return raw, nil
	}
	shorten := func(value string) string {
		compact, _ := truncateWorkResumeString(strings.TrimSpace(value), 128)
		return compact
	}
	var value any
	switch collection {
	case "completion_conditions":
		var item models.WorkCompletionCondition
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		item.Description, item.WaiverReason = shorten(item.Description), shorten(item.WaiverReason)
		item.Verification = json.RawMessage(`{"truncated":true}`)
		value = item
	case "evidence":
		var item models.WorkEvidence
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		item.EvidenceType, item.Summary, item.Reference, item.PrincipalID = shorten(item.EvidenceType), shorten(item.Summary), shorten(item.Reference), shorten(item.PrincipalID)
		item.Payload = json.RawMessage(`{"truncated":true}`)
		value = item
	case "memories":
		var item models.WorkMemorySnapshot
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		item.Content, item.Relation, item.Status = shorten(item.Content), shorten(item.Relation), shorten(item.Status)
		item.ContentTruncated = true
		value = item
	case "resources":
		var item models.WorkResourceRef
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		item.ResourceKey, item.Title, item.URI, item.Summary = shorten(item.ResourceKey), shorten(item.Title), shorten(item.URI), shorten(item.Summary)
		item.ExternalID, item.Revision = shorten(item.ExternalID), shorten(item.Revision)
		value = item
	case "worktree_links":
		var item models.WorktreeLink
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		item.Worktree.Repository, item.Worktree.WorktreePath, item.Worktree.Branch, item.Worktree.AgentID = shorten(item.Worktree.Repository), shorten(item.Worktree.WorktreePath), shorten(item.Worktree.Branch), shorten(item.Worktree.AgentID)
		item.Worktree.Metadata = json.RawMessage(`{"truncated":true}`)
		value = item
	case "dependency_results":
		var item models.WorkDependencyResult
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		item.WorkItem.Title, item.WorkItem.Owner, item.Summary, item.Result = shorten(item.WorkItem.Title), shorten(item.WorkItem.Owner), shorten(item.Summary), shorten(item.Result)
		value = item
	case "blockers":
		var item models.WorkItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		item.Title, item.Description, item.Reporter, item.Owner = shorten(item.Title), shorten(item.Description), shorten(item.Reporter), shorten(item.Owner)
		item.Labels = nil
		value = item
	default:
		return nil, fmt.Errorf("unsupported collection %q", collection)
	}
	return json.Marshal(value)
}

func workResumeCollectionResult(bc *bootstrap.Context, bundle *models.WorkResumeBundle, collection, detail string, offset, limit int) (*mcp.CallToolResult, error) {
	if offset < 0 {
		return nil, fmt.Errorf("argument %q must be zero or greater", "collection_offset")
	}
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("argument %q must be between 1 and 100", "collection_limit")
	}
	items, err := workResumeCollectionItems(bundle, collection)
	if err != nil {
		return nil, err
	}
	if offset > len(items) {
		return nil, fmt.Errorf("argument %q exceeds the %d available items", "collection_offset", len(items))
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	maxBytes := defaultMCPMaxResponseBytes
	if bc != nil && bc.Config != nil && bc.Config.MCPMaxResponseBytes > 0 {
		maxBytes = bc.Config.MCPMaxResponseBytes
	}
	itemBudget := maxBytes - 512
	if itemBudget < 256 {
		itemBudget = 256
	}
	for index := range items {
		items[index], err = compactWorkResumeCollectionItem(collection, items[index], itemBudget)
		if err != nil {
			return nil, err
		}
	}
	for end > offset {
		page := workResumeCollectionPage{WorkItemID: bundle.WorkItem.ID, Collection: collection, Detail: detail, Offset: offset, Items: items[offset:end], HasMore: end < len(items)}
		if page.HasMore {
			page.NextOffset = end
			page.NextQuery = fmt.Sprintf("Call resume_work with work_item_id=%d, detail=%s, collection=%s, collection_offset=%d, collection_limit=%d.", bundle.WorkItem.ID, detail, collection, end, limit)
		}
		payload, marshalErr := json.Marshal(page)
		if marshalErr != nil {
			return nil, marshalErr
		}
		if len(payload) <= maxBytes {
			return jsonToolResult(bc, page)
		}
		end--
	}
	return nil, fmt.Errorf("STASH_MCP_MAX_RESPONSE_BYTES=%d is too small for one complete %s item", maxBytes, collection)
}

func parsePositiveIDList(request mcp.CallToolRequest, key string) ([]int64, error) {
	data, err := structuredArgumentBytes(request, key, true)
	if err != nil {
		return nil, err
	}
	var ids []int64
	if err := decodeStrictJSON(data, &ids); err != nil {
		return nil, fmt.Errorf("argument %q must be an array of positive integer IDs: %w", key, err)
	}
	if len(ids) == 0 || len(ids) > 100 {
		return nil, fmt.Errorf("argument %q must contain between 1 and 100 IDs", key)
	}
	seen := make(map[int64]struct{}, len(ids))
	normalized := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("argument %q must contain only positive integer IDs", key)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	return normalized, nil
}

func parseOptionalPositiveIDList(request mcp.CallToolRequest, key string) ([]int64, error) {
	if _, ok := request.GetArguments()[key]; !ok {
		return nil, nil
	}
	return parsePositiveIDList(request, key)
}

func requiredWorkString(request mcp.CallToolRequest, key string) (string, error) {
	value, err := request.RequireString(key)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("argument %q must not be empty", key)
	}
	return value, nil
}

func workLeaseDuration(request mcp.CallToolRequest) (time.Duration, error) {
	seconds := request.GetInt("lease_seconds", int(brain.DefaultWorkLeaseDuration/time.Second))
	if seconds <= 0 {
		return 0, fmt.Errorf("argument %q must be a positive integer", "lease_seconds")
	}
	maxSeconds := int(brain.MaxWorkLeaseDuration / time.Second)
	if seconds > maxSeconds {
		return 0, fmt.Errorf("argument %q must not exceed %d seconds", "lease_seconds", int(brain.MaxWorkLeaseDuration/time.Second))
	}
	return time.Duration(seconds) * time.Second, nil
}

func redactWorkLeaseError(err error, leaseToken string) error {
	if err == nil || leaseToken == "" {
		return err
	}
	if !strings.Contains(err.Error(), leaseToken) {
		return err
	}
	message := strings.ReplaceAll(err.Error(), leaseToken, "[redacted]")
	return errors.New(message)
}

func rejectLeaseTokenContent(leaseToken string, values ...string) error {
	if leaseToken == "" {
		return nil
	}
	for _, value := range values {
		if strings.Contains(value, leaseToken) {
			return fmt.Errorf("lease_token must not appear in action keys, checkpoints, evidence, verification text, or handoff text")
		}
	}
	return nil
}

func hideForeignWorkObject(ctx context.Context, err, notFound error) error {
	if err == nil {
		return nil
	}
	mode, _ := ctx.Value(keyMode).(string)
	if mode == "remote" && strings.HasPrefix(err.Error(), "forbidden:") {
		return notFound
	}
	return err
}

func authorizeWorkItem(ctx context.Context, bc *bootstrap.Context, workItemID int64) (*models.WorkItem, error) {
	item, err := bc.Brain.GetWorkItem(ctx, workItemID)
	if err != nil {
		return nil, err
	}
	if err := authorizeNamespaceID(ctx, bc, item.NamespaceID); err != nil {
		return nil, hideForeignWorkObject(ctx, err, brain.ErrWorkItemNotFound)
	}
	return item, nil
}

func authorizeWorkAttempt(ctx context.Context, bc *bootstrap.Context, attemptID int64) (*models.WorkAttempt, *models.WorkItem, error) {
	attempt, err := bc.Brain.GetWorkAttempt(ctx, attemptID)
	if err != nil {
		return nil, nil, err
	}
	item, err := authorizeWorkItem(ctx, bc, attempt.WorkItemID)
	if err != nil {
		if errors.Is(err, brain.ErrWorkItemNotFound) {
			return nil, nil, brain.ErrWorkAttemptNotFound
		}
		return nil, nil, err
	}
	return attempt, item, nil
}

func authorizeWorktree(ctx context.Context, bc *bootstrap.Context, worktreeID int64) (*models.Worktree, error) {
	worktree, err := bc.Brain.GetWorktree(ctx, worktreeID)
	if err != nil {
		return nil, err
	}
	if err := authorizeNamespaceID(ctx, bc, worktree.NamespaceID); err != nil {
		return nil, hideForeignWorkObject(ctx, err, brain.ErrWorktreeNotFound)
	}
	return worktree, nil
}

func attemptMutationArguments(request mcp.CallToolRequest) (int64, string, string, error) {
	attemptID, err := requiredPositiveID(request, "attempt_id")
	if err != nil {
		return 0, "", "", err
	}
	leaseToken, err := requiredWorkString(request, "lease_token")
	if err != nil {
		return 0, "", "", err
	}
	actionKey, err := requiredWorkString(request, "action_key")
	if err != nil {
		return 0, "", "", err
	}
	if err := rejectLeaseTokenContent(leaseToken, actionKey); err != nil {
		return 0, "", "", err
	}
	return attemptID, leaseToken, actionKey, nil
}

func attemptMutationOptions(options ...mcp.ToolOption) []mcp.ToolOption {
	base := []mcp.ToolOption{
		mcp.WithNumber("attempt_id", mcp.Description("Active attempt ID returned by claim_work"), mcp.Required()),
		mcp.WithString("lease_token", mcp.Description("Secret returned once by claim_work. Never log it or put it in evidence or handoff text."), mcp.Required()),
		mcp.WithString("action_key", mcp.Description("Stable unique key for this logical mutation. Reuse it only when retrying the same call."), mcp.Required()),
		mcp.WithIdempotentHintAnnotation(true),
	}
	return append(base, options...)
}

func checkpointInput(request mcp.CallToolRequest, requireNextAction bool) (brain.WorkCheckpointInput, error) {
	summary, err := requiredWorkString(request, "summary")
	if err != nil {
		return brain.WorkCheckpointInput{}, err
	}
	result, err := requiredWorkString(request, "result")
	if err != nil {
		return brain.WorkCheckpointInput{}, err
	}
	nextAction := strings.TrimSpace(request.GetString("next_action", ""))
	if requireNextAction && nextAction == "" {
		return brain.WorkCheckpointInput{}, fmt.Errorf("argument %q must not be empty", "next_action")
	}
	return brain.WorkCheckpointInput{Summary: summary, Result: result, NextAction: nextAction}, nil
}

func finishInput(request mcp.CallToolRequest) (brain.WorkFinishInput, error) {
	summary, err := requiredWorkString(request, "summary")
	if err != nil {
		return brain.WorkFinishInput{}, err
	}
	result, err := requiredWorkString(request, "result")
	if err != nil {
		return brain.WorkFinishInput{}, err
	}
	passedConditionIDs, err := parseOptionalPositiveIDList(request, "passed_condition_ids")
	if err != nil {
		return brain.WorkFinishInput{}, err
	}
	return brain.WorkFinishInput{
		Summary:            summary,
		Result:             result,
		PassedConditionIDs: passedConditionIDs,
	}, nil
}

func conditionArraySchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"kind":         map[string]any{"type": "string", "description": "How to verify the condition", "enum": []string{"command", "test", "http", "file", "build", "ui", "user", "custom"}},
			"description":  map[string]any{"type": "string", "description": "Observable result that proves this condition is satisfied"},
			"verification": map[string]any{"type": "object", "description": "Structured verification details such as a command, path, URL, selector, or expected result", "minProperties": 1, "additionalProperties": true},
			"required":     map[string]any{"type": "boolean", "description": "Whether finish_work must satisfy this condition", "default": true},
		},
		"required": []string{"kind", "description", "verification"},
	}
}

func claimWorkToolOptions(description string) []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithDescription(description),
		mcp.WithNumber("work_item_id", mcp.Description("Prepared work item to claim"), mcp.Required()),
		mcp.WithString("agent_id", mcp.Description("Stable identifier for the agent doing the work"), mcp.Required()),
		mcp.WithNumber("worktree_id", mcp.Description("Optional registered Git worktree used by a connector")),
		mcp.WithNumber("lease_seconds", mcp.Description("Lease lifetime from now, from 1 to 86400 seconds"), mcp.DefaultNumber(900)),
		mcp.WithString("action_key", mcp.Description("Unpredictable unique key containing a fresh random UUIDv4 for this claim; keep it private and reuse it only when retrying the same call"), mcp.MinLength(minStartActionKeyChars), mcp.Pattern(startActionUUIDPatternText), mcp.Required()),
		mcp.WithIdempotentHintAnnotation(true),
	}
}

func handleClaimWork(ctx context.Context, request mcp.CallToolRequest, bc *bootstrap.Context) (*mcp.CallToolResult, error) {
	workItemID, err := requiredPositiveID(request, "work_item_id")
	if err != nil {
		return nil, err
	}
	item, err := authorizeWorkItem(ctx, bc, workItemID)
	if err != nil {
		return nil, err
	}
	agentID, err := requiredWorkString(request, "agent_id")
	if err != nil {
		return nil, err
	}
	actionKey, err := requiredStartActionKey(request)
	if err != nil {
		return nil, err
	}
	principalID, err := workExecutionPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	worktreeID, err := optionalID(request, "worktree_id")
	if err != nil {
		return nil, err
	}
	if worktreeID != nil {
		worktree, err := authorizeWorktree(ctx, bc, *worktreeID)
		if err != nil {
			return nil, err
		}
		if worktree.NamespaceID != item.NamespaceID {
			return nil, fmt.Errorf("work item and worktree must share a namespace")
		}
	}
	leaseDuration, err := workLeaseDuration(request)
	if err != nil {
		return nil, err
	}
	lease, err := bc.Brain.StartWorkAttemptForPrincipal(ctx, workItemID, agentID, principalID, worktreeID, leaseDuration, actionKey)
	if err != nil {
		return nil, err
	}
	return jsonToolResult(bc, lease)
}

func registerWorkExecutionTools(mcpServer *server.MCPServer, bc *bootstrap.Context) {
	mcpServer.AddTool(mcp.NewTool("prepare_work",
		mcp.WithDescription("Call before starting tracked work to set one concrete next action and replace the work item's observable completion conditions. At least one condition must be required."),
		mcp.WithNumber("work_item_id", mcp.Description("Work item to prepare"), mcp.Required()),
		mcp.WithString("next_action", mcp.Description("One concrete action the first agent should take"), mcp.Required()),
		mcp.WithArray("conditions", mcp.Description("Ordered completion conditions"), mcp.Required(), mcp.MinItems(1), mcp.MaxItems(100), mcp.Items(conditionArraySchema())),
		mcp.WithString("action_key", mcp.Description("Stable unique key for this preparation; reuse it only when retrying the same call"), mcp.Required()),
		mcp.WithIdempotentHintAnnotation(true),
	), recordWorkExecutionHandler("prepare", func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		workItemID, err := requiredPositiveID(request, "work_item_id")
		if err != nil {
			return nil, err
		}
		if _, err := authorizeWorkItem(ctx, bc, workItemID); err != nil {
			return nil, err
		}
		nextAction, err := requiredWorkString(request, "next_action")
		if err != nil {
			return nil, err
		}
		conditions, err := parseCompletionConditionInputs(request)
		if err != nil {
			return nil, err
		}
		actionKey, err := requiredWorkString(request, "action_key")
		if err != nil {
			return nil, err
		}
		prepared, err := bc.Brain.PrepareWork(ctx, workItemID, nextAction, conditions, actionKey)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, prepared)
	}))

	mcpServer.AddTool(mcp.NewTool("start_work", claimWorkToolOptions(
		"Compatibility name for claim_work. Call immediately before acting on an available prepared item; Git worktree metadata is optional.",
	)...), recordWorkExecutionHandler("start", func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleClaimWork(ctx, request, bc)
	}))

	mcpServer.AddTool(mcp.NewTool("resume_work",
		mcp.WithDescription("Call before acting on tracked work and after a handoff. The default bounded context returns the shared goal, current work, prerequisites, completion conditions, latest checkpoint, evidence references, and only facts changed since the previous digest; Git is optional."),
		mcp.WithNumber("work_item_id", mcp.Description("Work item to resume"), mcp.Required()),
		mcp.WithString("detail", mcp.Description("brief minimizes model input; full also includes evidence payloads, events, and optional Git worktree metadata"), mcp.DefaultString("brief"), mcp.Enum("brief", "full")),
		mcp.WithString("known_context_digest", mcp.Description("Digest from the previous brief; matching state returns a small receipt and changed state returns only fact changes since that cursor"), mcp.Pattern(`sha256:[0-9a-f]{64}`)),
		mcp.WithString("expected_context_digest", mcp.Description("Target digest from next_query when continuing a truncated changed_facts page"), mcp.Pattern(`sha256:[0-9a-f]{64}`)),
		mcp.WithNumber("fact_offset", mcp.Description("Continuation offset from next_query; use zero for a fresh context read"), mcp.DefaultNumber(0), mcp.Min(0)),
		mcp.WithString("collection", mcp.Description("Optional collection page to retrieve without repeating an omission notice"), mcp.Enum("completion_conditions", "evidence", "memories", "resources", "worktree_links", "dependency_results", "blockers")),
		mcp.WithNumber("collection_offset", mcp.Description("Continuation offset from the collection next_query"), mcp.DefaultNumber(0), mcp.Min(0)),
		mcp.WithNumber("collection_limit", mcp.Description("Maximum collection items to return"), mcp.DefaultNumber(20), mcp.Min(1), mcp.Max(100)),
		mcp.WithNumber("recent_event_limit", mcp.Description("Maximum recent events when detail is full"), mcp.DefaultNumber(8), mcp.Min(1), mcp.Max(100)),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
	), recordWorkExecutionHandler("resume", func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		workItemID, err := requiredPositiveID(request, "work_item_id")
		if err != nil {
			return nil, err
		}
		if _, err := authorizeWorkItem(ctx, bc, workItemID); err != nil {
			return nil, err
		}
		bundle, err := bc.Brain.GetWorkResumeBundle(ctx, workItemID, request.GetInt("recent_event_limit", 8))
		if err != nil {
			return nil, err
		}
		if collection := request.GetString("collection", ""); collection != "" {
			return workResumeCollectionResult(bc, bundle, collection, request.GetString("detail", "brief"), request.GetInt("collection_offset", 0), request.GetInt("collection_limit", 20))
		}
		if request.GetString("detail", "brief") == "full" {
			return workResumeToolResult(bc, bundle)
		}
		brief, err := buildWorkResumeBrief(bundle)
		if err != nil {
			return nil, err
		}
		principalID, err := workExecutionPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		knownDigest := request.GetString("known_context_digest", "")
		factDiff, err := bc.Brain.DiffWorkContextFacts(ctx, workItemID, principalID, knownDigest)
		if err != nil {
			return nil, err
		}
		brief.ContextDigest, err = workContextDigest(bundle, factDiff.CurrentStates)
		if err != nil {
			return nil, err
		}
		configuredMaxBytes := 0
		if bc != nil && bc.Config != nil {
			configuredMaxBytes = bc.Config.MCPMaxResponseBytes
		}
		inputLimit := workContextInputLimit(configuredMaxBytes)
		if receipt := matchingAgentContextReceipt(
			knownDigest, "work", workItemID, brief.NextAction, brief.ContextDigest,
		); receipt != nil {
			finalizeAgentContextReceipt(receipt, inputLimit)
			return jsonToolResult(bc, receipt)
		}
		complete, err := applyWorkContextFactDiff(
			brief, factDiff, knownDigest, request.GetString("expected_context_digest", ""),
			request.GetInt("fact_offset", 0), inputLimit,
		)
		if err != nil {
			return nil, err
		}
		if complete {
			if err := bc.Brain.SaveWorkContextCursor(ctx, workItemID, principalID, brief.ContextDigest, factDiff.CurrentStates); err != nil {
				return nil, err
			}
		}
		return jsonToolResult(bc, brief)
	}))

	mcpServer.AddTool(mcp.NewTool("checkpoint_work", attemptMutationOptions(
		mcp.WithDescription("Save a recoverable partial result before interruption, lease risk, or handoff. Do not call after routine shell commands or file edits."),
		mcp.WithString("summary", mcp.Description("Concise description of the action taken"), mcp.Required()),
		mcp.WithString("result", mcp.Description("Observed result, including failures or unresolved facts"), mcp.Required()),
		mcp.WithString("next_action", mcp.Description("One concrete action to take next"), mcp.Required()),
		mcp.WithNumber("lease_seconds", mcp.Description("Lease lifetime from now, from 1 to 86400 seconds"), mcp.DefaultNumber(900)),
	)...), recordWorkExecutionHandler("checkpoint", func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		attemptID, leaseToken, actionKey, err := attemptMutationArguments(request)
		if err != nil {
			return nil, err
		}
		if _, _, err := authorizeWorkAttempt(ctx, bc, attemptID); err != nil {
			return nil, redactWorkLeaseError(err, leaseToken)
		}
		principalID, err := workExecutionPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		input, err := checkpointInput(request, true)
		if err != nil {
			return nil, err
		}
		if err := rejectLeaseTokenContent(leaseToken, input.Summary, input.Result, input.NextAction); err != nil {
			return nil, err
		}
		leaseDuration, err := workLeaseDuration(request)
		if err != nil {
			return nil, err
		}
		receipt, err := bc.Brain.CheckpointWorkAttemptForPrincipal(ctx, attemptID, leaseToken, principalID, input, leaseDuration, actionKey)
		if err != nil {
			return nil, redactWorkLeaseError(err, leaseToken)
		}
		return jsonToolResult(bc, receipt)
	}))

	mcpServer.AddTool(mcp.NewTool("submit_work_evidence", attemptMutationOptions(
		mcp.WithDescription("Record an observed test, build, review, artifact, or other result. One call can support every condition proved by the same observation."),
		mcp.WithString("evidence_type", mcp.Description("Short kind such as test, build, review, artifact, or observation"), mcp.Required()),
		mcp.WithString("summary", mcp.Description("What was observed and why it matters"), mcp.Required()),
		mcp.WithString("reference", mcp.Description("Optional file, command, run, URL, or artifact reference")),
		mcp.WithObject("payload", mcp.Description("Optional structured details; secrets and lease tokens are forbidden"), mcp.AdditionalProperties(true)),
		mcp.WithArray("condition_ids", mcp.Description("Completion conditions this evidence supports"), mcp.Required(), mcp.MinItems(1), mcp.MaxItems(100), mcp.UniqueItems(true), mcp.Items(map[string]any{"type": "integer", "minimum": 1})),
	)...), recordWorkExecutionHandler("evidence", func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		attemptID, leaseToken, actionKey, err := attemptMutationArguments(request)
		if err != nil {
			return nil, err
		}
		if _, _, err := authorizeWorkAttempt(ctx, bc, attemptID); err != nil {
			return nil, redactWorkLeaseError(err, leaseToken)
		}
		principalID, err := workExecutionPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		evidenceType, err := requiredWorkString(request, "evidence_type")
		if err != nil {
			return nil, err
		}
		summary, err := requiredWorkString(request, "summary")
		if err != nil {
			return nil, err
		}
		payload, err := parseEvidencePayload(request)
		if err != nil {
			return nil, err
		}
		reference := strings.TrimSpace(request.GetString("reference", ""))
		if err := rejectLeaseTokenContent(leaseToken, evidenceType, summary, reference, string(payload)); err != nil {
			return nil, err
		}
		conditionIDs, err := parsePositiveIDList(request, "condition_ids")
		if err != nil {
			return nil, err
		}
		evidence, err := bc.Brain.SubmitWorkEvidenceForPrincipal(ctx, attemptID, leaseToken, principalID, brain.WorkEvidenceInput{
			EvidenceType: evidenceType,
			Summary:      summary,
			Reference:    reference,
			Payload:      payload,
		}, conditionIDs, actionKey)
		if err != nil {
			return nil, redactWorkLeaseError(err, leaseToken)
		}
		return jsonToolResult(bc, evidence)
	}))

	mcpServer.AddTool(mcp.NewTool("verify_work_condition", attemptMutationOptions(
		mcp.WithDescription("Explicitly accept or waive one evidence-backed condition before finish. Usually omit this for passed conditions because finish_work accepts named pending conditions in the same call; waivers still require this call."),
		mcp.WithNumber("condition_id", mcp.Description("Condition from resume_work"), mcp.Required()),
		mcp.WithString("status", mcp.Description("Verification result"), mcp.Enum("passed", "waived"), mcp.Required()),
		mcp.WithArray("evidence_ids", mcp.Description("Evidence from this work item that proves or supports the result"), mcp.Required(), mcp.MinItems(1), mcp.MaxItems(100), mcp.UniqueItems(true), mcp.Items(map[string]any{"type": "integer", "minimum": 1})),
		mcp.WithString("waiver_reason", mcp.Description("Required when status is waived; ignored when passed")),
	)...), recordWorkExecutionHandler("verify", func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		attemptID, leaseToken, actionKey, err := attemptMutationArguments(request)
		if err != nil {
			return nil, err
		}
		if _, _, err := authorizeWorkAttempt(ctx, bc, attemptID); err != nil {
			return nil, redactWorkLeaseError(err, leaseToken)
		}
		principalID, err := workExecutionPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		conditionID, err := requiredPositiveID(request, "condition_id")
		if err != nil {
			return nil, err
		}
		status, err := requiredWorkString(request, "status")
		if err != nil {
			return nil, err
		}
		evidenceIDs, err := parsePositiveIDList(request, "evidence_ids")
		if err != nil {
			return nil, err
		}
		waiverReason := strings.TrimSpace(request.GetString("waiver_reason", ""))
		if err := rejectLeaseTokenContent(leaseToken, waiverReason); err != nil {
			return nil, err
		}
		condition, err := bc.Brain.VerifyWorkConditionForPrincipal(ctx, attemptID, leaseToken, principalID, conditionID, status, evidenceIDs, waiverReason, actionKey)
		if err != nil {
			return nil, redactWorkLeaseError(err, leaseToken)
		}
		return jsonToolResult(bc, condition)
	}))

	mcpServer.AddTool(mcp.NewTool("renew_work_lease", attemptMutationOptions(
		mcp.WithDescription("Call while work is still active and before the lease expires when the next meaningful checkpoint will take longer."),
		mcp.WithNumber("lease_seconds", mcp.Description("Lease lifetime from now, from 1 to 86400 seconds"), mcp.DefaultNumber(900)),
	)...), recordWorkExecutionHandler("renew", func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		attemptID, leaseToken, actionKey, err := attemptMutationArguments(request)
		if err != nil {
			return nil, err
		}
		if _, _, err := authorizeWorkAttempt(ctx, bc, attemptID); err != nil {
			return nil, redactWorkLeaseError(err, leaseToken)
		}
		principalID, err := workExecutionPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		leaseDuration, err := workLeaseDuration(request)
		if err != nil {
			return nil, err
		}
		attempt, err := bc.Brain.RenewWorkAttemptLeaseForPrincipal(ctx, attemptID, leaseToken, principalID, leaseDuration, actionKey)
		if err != nil {
			return nil, redactWorkLeaseError(err, leaseToken)
		}
		return jsonToolResult(bc, attempt)
	}))

	mcpServer.AddTool(mcp.NewTool("finish_work", attemptMutationOptions(
		mcp.WithDescription("Complete work after every required condition has linked evidence and all blockers are finished. Include pending conditions proved by evidence from this attempt in passed_condition_ids; explicit waivers must already be recorded. The server accepts those conditions and verifies a result-memory link in the same call."),
		mcp.WithString("summary", mcp.Description("Concise description of the completed work"), mcp.Required()),
		mcp.WithString("result", mcp.Description("Observed final result"), mcp.Required()),
		mcp.WithArray("passed_condition_ids", mcp.Description("Pending condition IDs explicitly accepted by this finish call; each must have evidence from this attempt"), mcp.MinItems(1), mcp.MaxItems(100), mcp.UniqueItems(true), mcp.Items(map[string]any{"type": "integer", "minimum": 1})),
	)...), recordWorkExecutionHandler("finish", func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		attemptID, leaseToken, actionKey, err := attemptMutationArguments(request)
		if err != nil {
			return nil, err
		}
		if _, _, err := authorizeWorkAttempt(ctx, bc, attemptID); err != nil {
			return nil, redactWorkLeaseError(err, leaseToken)
		}
		principalID, err := workExecutionPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		input, err := finishInput(request)
		if err != nil {
			return nil, err
		}
		if err := rejectLeaseTokenContent(leaseToken, input.Summary, input.Result); err != nil {
			return nil, err
		}
		attempt, err := bc.Brain.FinishWorkAttemptForPrincipal(ctx, attemptID, leaseToken, principalID, input, actionKey)
		if err != nil {
			return nil, redactWorkLeaseError(err, leaseToken)
		}
		return jsonToolResult(bc, attempt)
	}))

	mcpServer.AddTool(mcp.NewTool("handoff_work", attemptMutationOptions(
		mcp.WithDescription("Call before stopping unfinished work to save the observed result and one concrete next action, verify a result-memory link, end the lease, and make the item available."),
		mcp.WithString("summary", mcp.Description("Concise description of work completed so far"), mcp.Required()),
		mcp.WithString("result", mcp.Description("Observed current result, including blockers or failures"), mcp.Required()),
		mcp.WithString("next_action", mcp.Description("One concrete action for the next agent"), mcp.Required()),
	)...), recordWorkExecutionHandler("handoff", func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		attemptID, leaseToken, actionKey, err := attemptMutationArguments(request)
		if err != nil {
			return nil, err
		}
		if _, _, err := authorizeWorkAttempt(ctx, bc, attemptID); err != nil {
			return nil, redactWorkLeaseError(err, leaseToken)
		}
		principalID, err := workExecutionPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		input, err := checkpointInput(request, true)
		if err != nil {
			return nil, err
		}
		if err := rejectLeaseTokenContent(leaseToken, input.Summary, input.Result, input.NextAction); err != nil {
			return nil, err
		}
		attempt, err := bc.Brain.HandoffWorkAttemptForPrincipal(ctx, attemptID, leaseToken, principalID, input, actionKey)
		if err != nil {
			return nil, redactWorkLeaseError(err, leaseToken)
		}
		return jsonToolResult(bc, attempt)
	}))

	mcpServer.AddTool(mcp.NewTool("remember_work",
		mcp.WithDescription("Call when a durable decision, correction, failure, or lesson comes from tracked work; this call is required for those outcomes. Omit routine narration. Stash separately verifies one result link when finish_work or handoff_work succeeds."),
		mcp.WithNumber("work_item_id", mcp.Description("Work item that produced this memory"), mcp.Required()),
		mcp.WithString("content", mcp.Description("Self-contained durable information; omit routine narration and all lease tokens"), mcp.Required()),
		mcp.WithString("relation", mcp.Description("How the memory relates to the work item"), mcp.DefaultString("result"), mcp.Enum("context", "constraint", "decision", "evidence", "failure", "result", "supersedes")),
		mcp.WithString("action_key", mcp.Description("Stable unique key for this memory write; reuse it only when retrying the same call"), mcp.Required()),
		mcp.WithIdempotentHintAnnotation(true),
	), recordWorkExecutionHandler("remember", func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		workItemID, err := requiredPositiveID(request, "work_item_id")
		if err != nil {
			return nil, err
		}
		if _, err := authorizeWorkItem(ctx, bc, workItemID); err != nil {
			return nil, err
		}
		content, err := requiredWorkString(request, "content")
		if err != nil {
			return nil, err
		}
		actionKey, err := requiredWorkString(request, "action_key")
		if err != nil {
			return nil, err
		}
		remembered, err := bc.Brain.RememberForWork(ctx, workItemID, content, request.GetString("relation", "result"), actionKey)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, remembered)
	}))
}
