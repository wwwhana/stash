package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/brain"
	"github.com/alash3al/stash/internal/config"
	"github.com/alash3al/stash/internal/models"
	"github.com/alash3al/stash/internal/observability"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/prometheus/client_golang/prometheus"
)

func TestNewMCPServerRegistersWorkExecutionTools(t *testing.T) {
	mcpServer := newMCPServer(nil)
	for _, name := range []string{
		"prepare_work", "start_work", "resume_work", "checkpoint_work",
		"submit_work_evidence", "verify_work_condition", "renew_work_lease",
		"finish_work", "handoff_work", "remember_work",
	} {
		if mcpServer.GetTool(name) == nil {
			t.Fatalf("newMCPServer did not register %q", name)
		}
	}
}

func TestWorkExecutionToolSchemas(t *testing.T) {
	mcpServer := server.NewMCPServer("test", "test")
	registerWorkExecutionTools(mcpServer, nil)
	tools := mcpServer.ListTools()

	requiredByTool := map[string][]string{
		"prepare_work":          {"work_item_id", "next_action", "conditions", "action_key"},
		"start_work":            {"work_item_id", "agent_id", "action_key"},
		"resume_work":           {"work_item_id"},
		"checkpoint_work":       {"attempt_id", "lease_token", "action_key", "summary", "result", "next_action"},
		"submit_work_evidence":  {"attempt_id", "lease_token", "action_key", "evidence_type", "summary", "condition_ids"},
		"verify_work_condition": {"attempt_id", "lease_token", "action_key", "condition_id", "status", "evidence_ids"},
		"renew_work_lease":      {"attempt_id", "lease_token", "action_key"},
		"finish_work":           {"attempt_id", "lease_token", "action_key", "summary", "result"},
		"handoff_work":          {"attempt_id", "lease_token", "action_key", "summary", "result", "next_action"},
		"remember_work":         {"work_item_id", "content", "action_key"},
	}

	for name, wantRequired := range requiredByTool {
		tool, ok := tools[name]
		if !ok {
			t.Fatalf("tool %q is not registered", name)
		}
		if strings.TrimSpace(tool.Tool.Description) == "" {
			t.Fatalf("tool %q has no description", name)
		}
		required := make(map[string]bool, len(tool.Tool.InputSchema.Required))
		for _, property := range tool.Tool.InputSchema.Required {
			required[property] = true
		}
		for _, property := range wantRequired {
			if !required[property] {
				t.Errorf("tool %q does not require %q; required=%v", name, property, tool.Tool.InputSchema.Required)
			}
		}
	}
	finish := tools["finish_work"].Tool.InputSchema
	if _, ok := finish.Properties["passed_condition_ids"]; !ok {
		t.Fatal("finish_work does not accept passed_condition_ids")
	}
	for _, required := range finish.Required {
		if required == "passed_condition_ids" {
			t.Fatal("finish_work unexpectedly requires passed_condition_ids for already verified conditions")
		}
	}
}

func TestWorkExecutionDescriptionsSayWhenToCall(t *testing.T) {
	mcpServer := server.NewMCPServer("test", "test")
	registerWorkExecutionTools(mcpServer, nil)

	wantCue := map[string]string{
		"prepare_work":          "before starting",
		"start_work":            "immediately before",
		"resume_work":           "before acting",
		"checkpoint_work":       "before interruption",
		"submit_work_evidence":  "one call can support",
		"verify_work_condition": "usually omit",
		"renew_work_lease":      "before the lease expires",
		"finish_work":           "same call",
		"handoff_work":          "before stopping unfinished",
		"remember_work":         "when a durable",
	}
	for name, cue := range wantCue {
		description := strings.ToLower(mcpServer.GetTool(name).Tool.Description)
		if !strings.Contains(description, cue) {
			t.Errorf("tool %q description %q does not tell agents when to call it; want cue %q", name, description, cue)
		}
	}
}

func TestResumeWorkHasNoLeaseCredentialAndIsIdempotent(t *testing.T) {
	mcpServer := server.NewMCPServer("test", "test")
	registerWorkExecutionTools(mcpServer, nil)
	tool := mcpServer.GetTool("resume_work").Tool
	if tool.Annotations.ReadOnlyHint == nil || *tool.Annotations.ReadOnlyHint {
		t.Fatal("resume_work must advertise readOnlyHint=false because it may expire stale leases")
	}
	if tool.Annotations.IdempotentHint == nil || !*tool.Annotations.IdempotentHint {
		t.Fatal("resume_work must advertise idempotentHint=true")
	}
	for _, forbidden := range []string{"lease_token", "action_key"} {
		if _, ok := tool.InputSchema.Properties[forbidden]; ok {
			t.Fatalf("resume_work unexpectedly accepts %q", forbidden)
		}
	}
}

func TestParseCompletionConditionInputs(t *testing.T) {
	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{
		"conditions": []any{
			map[string]any{"kind": "test", "description": "Tests pass", "verification": map[string]any{"command": "go test ./..."}},
			map[string]any{"kind": "user", "description": "Optional review", "verification": map[string]any{"approval": "owner"}, "required": false},
		},
	}
	conditions, err := parseCompletionConditionInputs(request)
	if err != nil {
		t.Fatalf("parse conditions: %v", err)
	}
	if len(conditions) != 2 || conditions[0].Kind != "test" || conditions[0].Description != "Tests pass" || !conditions[0].Required {
		t.Fatalf("conditions = %#v", conditions)
	}
	if got := string(conditions[0].Verification); !strings.Contains(got, `"command":"go test ./..."`) {
		t.Fatalf("verification = %s", got)
	}
	if conditions[1].Required {
		t.Fatalf("explicit optional condition became required: %#v", conditions[1])
	}
}

func TestParseCompletionConditionInputsRejectsUnknownFields(t *testing.T) {
	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{
		"conditions": `[ {"kind":"test","description":"Tests pass","verification":{"command":"go test ./..."},"requiredd":true} ]`,
	}
	if _, err := parseCompletionConditionInputs(request); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v, want unknown field", err)
	}
}

func TestParseEvidencePayloadAcceptsObjectAndRejectsOtherJSON(t *testing.T) {
	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{
		"payload": map[string]any{"command": "go test ./...", "exit_code": 0},
	}
	payload, err := parseEvidencePayload(request)
	if err != nil {
		t.Fatalf("parse evidence payload: %v", err)
	}
	if got := string(payload); !strings.Contains(got, `"command":"go test ./..."`) {
		t.Fatalf("payload = %s", got)
	}

	request.Params.Arguments = map[string]any{"payload": `["not","an","object"]`}
	if _, err := parseEvidencePayload(request); err == nil {
		t.Fatal("array evidence payload was accepted")
	}
}

func TestParsePositiveIDListRejectsFractionalIDs(t *testing.T) {
	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{"evidence_ids": []any{float64(1), float64(2)}}
	ids, err := parsePositiveIDList(request, "evidence_ids")
	if err != nil {
		t.Fatalf("parse evidence IDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("evidence IDs = %#v", ids)
	}

	request.Params.Arguments = map[string]any{"evidence_ids": []any{1.5}}
	if _, err := parsePositiveIDList(request, "evidence_ids"); err == nil {
		t.Fatal("fractional evidence ID was accepted")
	}
}

func TestFinishInputParsesOptionalPassedConditionIDs(t *testing.T) {
	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{
		"summary":              "done",
		"result":               "tests passed",
		"passed_condition_ids": []any{float64(3), float64(3), float64(7)},
	}
	input, err := finishInput(request)
	if err != nil {
		t.Fatalf("parse finish input: %v", err)
	}
	if len(input.PassedConditionIDs) != 2 || input.PassedConditionIDs[0] != 3 || input.PassedConditionIDs[1] != 7 {
		t.Fatalf("passed condition IDs = %#v", input.PassedConditionIDs)
	}

	request.Params.Arguments = map[string]any{"summary": "done", "result": "already verified"}
	input, err = finishInput(request)
	if err != nil || input.PassedConditionIDs != nil {
		t.Fatalf("optional passed condition IDs = %#v, %v", input.PassedConditionIDs, err)
	}
}

func TestRedactWorkLeaseError(t *testing.T) {
	const token = "private-lease-token"
	err := redactWorkLeaseError(errors.New("invalid token: "+token), token)
	if strings.Contains(err.Error(), token) {
		t.Fatalf("lease token leaked in error: %v", err)
	}
}

func TestRejectLeaseTokenContentDoesNotEchoToken(t *testing.T) {
	const token = "private-lease-token"
	err := rejectLeaseTokenContent(token, "checkpoint accidentally contains "+token)
	if err == nil {
		t.Fatal("lease token in checkpoint content was accepted")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("lease token leaked in rejection: %v", err)
	}
}

func TestHideForeignWorkObjectDoesNotExposeRemoteIDs(t *testing.T) {
	foreign := errors.New("forbidden: object is outside the authenticated namespace")
	remote := context.WithValue(context.Background(), keyMode, "remote")
	if err := hideForeignWorkObject(remote, foreign, brain.ErrWorkItemNotFound); !errors.Is(err, brain.ErrWorkItemNotFound) {
		t.Fatalf("remote foreign object error = %v, want %v", err, brain.ErrWorkItemNotFound)
	}

	local := context.WithValue(context.Background(), keyMode, "local")
	if err := hideForeignWorkObject(local, foreign, brain.ErrWorkItemNotFound); !errors.Is(err, foreign) {
		t.Fatalf("local authorization error = %v, want original error", err)
	}

	infrastructure := errors.New("resolve namespace IDs: database unavailable")
	if err := hideForeignWorkObject(remote, infrastructure, brain.ErrWorkItemNotFound); !errors.Is(err, infrastructure) {
		t.Fatalf("remote infrastructure error = %v, want original error", err)
	}
}

func TestStartActionKeyRequiresUnpredictableLength(t *testing.T) {
	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{"action_key": "predictable"}
	if _, err := requiredStartActionKey(request); err == nil || !strings.Contains(err.Error(), "at least 32") {
		t.Fatalf("short start action key error = %v", err)
	}
	request.Params.Arguments = map[string]any{"action_key": strings.Repeat("a", minStartActionKeyChars)}
	if _, err := requiredStartActionKey(request); err == nil || !strings.Contains(err.Error(), "UUIDv4") {
		t.Fatalf("predictable long start action key error = %v", err)
	}

	request.Params.Arguments = map[string]any{"action_key": "73996bc5-5458-4ab2-b3e8-bf1d4f0ca537"}
	if key, err := requiredStartActionKey(request); err != nil || key == "" {
		t.Fatalf("UUID start action key = %q, %v", key, err)
	}
}

func TestWorkExecutionPrincipalUsesOnlyVerifiedRemoteIdentity(t *testing.T) {
	local := context.WithValue(context.Background(), keyMode, "local")
	if principal, err := workExecutionPrincipal(local); err != nil || principal != "" {
		t.Fatalf("local principal = %q, %v; want empty", principal, err)
	}

	remote := context.WithValue(context.Background(), keyMode, "remote")
	remote = context.WithValue(remote, keySSOUser, "subject-1")
	principal, err := workExecutionPrincipal(remote)
	if err != nil || principal != namespaceOwnerKey("subject-1") || strings.Contains(principal, "subject-1") {
		t.Fatalf("remote principal = %q, %v; want stable one-way owner key", principal, err)
	}
	other := context.WithValue(context.Background(), keyMode, "remote")
	other = context.WithValue(other, keySSOUser, "subject-2")
	otherPrincipal, err := workExecutionPrincipal(other)
	if err != nil || otherPrincipal == principal || strings.Contains(otherPrincipal, "subject-2") {
		t.Fatalf("other remote principal = %q, %v; first = %q", otherPrincipal, err, principal)
	}

	missing := context.WithValue(context.Background(), keyMode, "remote")
	if principal, err := workExecutionPrincipal(missing); err == nil || principal != "" {
		t.Fatalf("missing remote principal = %q, %v; want rejection", principal, err)
	}
}

func TestRecordWorkExecutionHandlerRecordsSuccessAndRejection(t *testing.T) {
	original := recordWorkExecutionTransition
	t.Cleanup(func() { recordWorkExecutionTransition = original })
	var observations []string
	recordWorkExecutionTransition = func(action, outcome string) {
		observations = append(observations, action+":"+outcome)
	}

	success := recordWorkExecutionHandler("checkpoint", func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	if _, err := success(context.Background(), mcp.CallToolRequest{}); err != nil {
		t.Fatalf("success handler: %v", err)
	}
	rejected := recordWorkExecutionHandler("finish", func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, errors.New("denied")
	})
	_, _ = rejected(context.Background(), mcp.CallToolRequest{})

	if len(observations) != 2 || observations[0] != "checkpoint:success" || observations[1] != "finish:rejected" {
		t.Fatalf("execution observations = %#v", observations)
	}
}

func TestRecordWorkExecutionHandlerExportsTransitionMetric(t *testing.T) {
	original := recordWorkExecutionTransition
	recordWorkExecutionTransition = observability.RecordWorkExecution
	t.Cleanup(func() { recordWorkExecutionTransition = original })
	handler := recordWorkExecutionHandler("start", func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	if _, err := handler(context.Background(), mcp.CallToolRequest{}); err != nil {
		t.Fatalf("recorded handler: %v", err)
	}

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != "stash_work_execution_transitions_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["action"] == "start" && labels["result"] == "success" && metric.GetCounter().GetValue() > 0 {
				return
			}
		}
	}
	t.Fatal("work execution transition metric was not exported")
}

func TestWorkResumeResultKeepsCoreAndReportsTruncationWithinLimit(t *testing.T) {
	const maxBytes = 32 * 1024
	bundle := &models.WorkResumeBundle{
		WorkItem: models.WorkItem{ID: 71, Title: "bounded resume", Description: strings.Repeat("d", 10000)},
		PlanContext: &models.WorkPlanExecutionContext{
			Component: models.WorkPlanReference{ID: 70, IssueKey: "W-000070", Title: "상위 구성 작업"},
			Outcome:   strings.Repeat("o", 10000), Guidance: strings.Repeat("g", 10000), TaskDetails: strings.Repeat("t", 10000),
			OwnedScopes: []string{"jira://team/**", "confluence://team/**"},
		},
		NextAction:       strings.Repeat("n", 10000),
		LatestAttempt:    &models.WorkAttempt{ID: 9, WorkItemID: 71, AgentID: "agent-a", PrincipalID: namespaceOwnerKey("subject-a"), Status: "active"},
		LatestCheckpoint: &models.WorkCheckpoint{ID: 8, AttemptID: 9, Summary: strings.Repeat("s", 10000), Result: strings.Repeat("r", 10000)},
		MemoryLinks: []models.WorkMemorySnapshot{{
			WorkItemID: 71, MemoryType: "episode", MemoryID: 3, Relation: "context",
			Content: strings.Repeat("m", 10000), Status: "recorded",
		}},
		Totals: models.WorkResumeTotals{Evidence: 60, MemoryLinks: 1, RecentEvents: 60},
	}
	for i := 0; i < 60; i++ {
		bundle.Evidence = append(bundle.Evidence, models.WorkEvidence{
			ID: int64(i + 1), WorkItemID: 71, AttemptID: 9, EvidenceType: "test",
			Summary: strings.Repeat("e", 4000), Payload: json.RawMessage(`{"passed":true}`),
		})
		bundle.RecentEvents = append(bundle.RecentEvents, models.WorkEvent{
			ID: int64(i + 1), EventType: "work.test", Payload: json.RawMessage(`{"observed":true}`),
		})
	}

	bc := &bootstrap.Context{Config: &config.Config{MCPMaxResponseBytes: maxBytes}}
	result, err := workResumeToolResult(bc, bundle)
	if err != nil {
		t.Fatalf("workResumeToolResult: %v", err)
	}
	text := toolResultText(t, result)
	if len(text) > maxBytes {
		t.Fatalf("resume response bytes = %d, limit = %d", len(text), maxBytes)
	}
	if strings.Contains(text, `"result_omitted":true`) {
		t.Fatalf("resume response omitted its core state: %s", text)
	}
	var compact models.WorkResumeBundle
	if err := json.Unmarshal([]byte(text), &compact); err != nil {
		t.Fatalf("decode compact resume: %v", err)
	}
	if compact.WorkItem.ID != 71 || compact.LatestAttempt == nil || compact.LatestAttempt.ID != 9 {
		t.Fatalf("compact resume lost core state: %#v", compact)
	}
	if compact.PlanContext == nil || compact.PlanContext.Component.ID != 70 || len(compact.PlanContext.OwnedScopes) != 2 {
		t.Fatalf("compact resume lost plan context: %#v", compact.PlanContext)
	}
	if compact.Totals.Evidence != 60 || compact.Totals.MemoryLinks != 1 || compact.Totals.RecentEvents != 60 || !compact.Truncated.Evidence || !compact.Truncated.MemoryLinks || !compact.Truncated.RecentEvents || !compact.Truncated.Core {
		t.Fatalf("compact resume bounds = totals:%#v truncated:%#v", compact.Totals, compact.Truncated)
	}
}

func TestWorkResumeCollectionPagesRecoverEveryItemWithinSmallLimit(t *testing.T) {
	const maxBytes = 1400
	bundle := &models.WorkResumeBundle{WorkItem: models.WorkItem{ID: 91, Title: "paged resume"}}
	for index := 0; index < 17; index++ {
		bundle.Evidence = append(bundle.Evidence, models.WorkEvidence{
			ID: int64(index + 1), WorkItemID: 91, EvidenceType: "test",
			Summary: strings.Repeat("observed ", 18), Payload: json.RawMessage(`{"passed":true}`),
		})
	}
	bc := &bootstrap.Context{Config: &config.Config{MCPMaxResponseBytes: maxBytes}}
	for _, detail := range []string{"brief", "full"} {
		offset := 0
		seen := make([]int64, 0, len(bundle.Evidence))
		for pageNumber := 0; ; pageNumber++ {
			result, err := workResumeCollectionResult(bc, bundle, "evidence", detail, offset, 9)
			if err != nil {
				t.Fatalf("detail %s page %d: %v", detail, pageNumber, err)
			}
			text := toolResultText(t, result)
			if len(text) > maxBytes {
				t.Fatalf("detail %s page %d bytes = %d", detail, pageNumber, len(text))
			}
			var page workResumeCollectionPage
			if err := json.Unmarshal([]byte(text), &page); err != nil {
				t.Fatalf("decode detail %s page %d: %v", detail, pageNumber, err)
			}
			if page.Offset != offset || len(page.Items) == 0 {
				t.Fatalf("detail %s page %d = %#v", detail, pageNumber, page)
			}
			for _, raw := range page.Items {
				var item struct {
					ID int64 `json:"id"`
				}
				if err := json.Unmarshal(raw, &item); err != nil {
					t.Fatal(err)
				}
				seen = append(seen, item.ID)
			}
			if !page.HasMore {
				if page.NextQuery != "" || page.NextOffset != 0 {
					t.Fatalf("terminal page continuation = %#v", page)
				}
				break
			}
			if page.NextOffset != offset+len(page.Items) || !strings.Contains(page.NextQuery, "resume_work") ||
				!strings.Contains(page.NextQuery, "detail="+detail) || !strings.Contains(page.NextQuery, "collection=evidence") ||
				!strings.Contains(page.NextQuery, "collection_offset=") || !strings.Contains(page.NextQuery, "collection_limit=9") {
				t.Fatalf("detail %s next query = %#v", detail, page)
			}
			offset = page.NextOffset
		}
		if len(seen) != len(bundle.Evidence) {
			t.Fatalf("detail %s recovered %d items", detail, len(seen))
		}
		for index, id := range seen {
			if id != int64(index+1) {
				t.Fatalf("detail %s item %d = %d", detail, index, id)
			}
		}
	}
}
