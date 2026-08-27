package reasoner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alash3al/stash/internal/models"
)

func TestNewOpenAIAllowsEmptyAPIKey(t *testing.T) {
	got, err := NewOpenAI("http://localhost:1234/v1", "", "local-reasoner")
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	if got.model != "local-reasoner" {
		t.Fatalf("model = %q, want local-reasoner", got.model)
	}
}

func TestOpenAIRequestTimeoutStopsUnavailableProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		// Do not write a response before the client's request deadline. The
		// handler returns shortly afterwards so httptest.Server.Close can finish.
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	client, err := NewOpenAIWithTimeout(server.URL+"/v1", "", "plan-model", 20*time.Millisecond)
	if err != nil {
		t.Fatalf("NewOpenAIWithTimeout: %v", err)
	}
	started := time.Now()
	_, err = client.ValidateWorkPlan(context.Background(), testWorkPlan())
	if err == nil {
		t.Fatal("ValidateWorkPlan succeeded against a provider that never responds")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ValidateWorkPlan error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("ValidateWorkPlan took %s, want it to stop promptly", elapsed)
	}
}

func testWorkPlan() models.WorkPlan {
	component := models.WorkPlanComponent{
		WorkItem:         models.WorkItem{ID: 10, IssueKey: "W-000010", Title: "Show failed alerts", Description: "The owner can see every failed alert."},
		TechnicalDetails: "Render persisted failures.",
		OwnedPaths:       []string{"internal/alerts/**"},
		Tasks: []models.WorkPlanTask{{
			WorkItem:         models.WorkItem{ID: 11, ParentID: func() *int64 { id := int64(10); return &id }(), Title: "Render failure list"},
			TechnicalDetails: "Add the list view.",
		}},
		Needs: []models.WorkPlanReference{},
		Links: []models.WorkPlanReference{},
	}
	return models.WorkPlan{Components: []models.WorkPlanComponent{component}, Decisions: []models.WorkPlanDecision{}}
}

func writeChatCompletion(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "plan-model",
		"choices": []map[string]any{{
			"index": 0, "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop",
		}},
	}); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func TestValidateWorkPlanUsesReasonerAndReturnsGroundedFinding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var request struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Model != "plan-model" {
			t.Fatalf("model = %q", request.Model)
		}
		joined := ""
		for _, message := range request.Messages {
			joined += message.Content
		}
		if !strings.Contains(joined, `"stable_id":"W-000010"`) || !strings.Contains(joined, `"id":11`) {
			t.Fatalf("prompt does not contain grounded plan IDs: %s", joined)
		}
		writeChatCompletion(t, w, `{"summary":"One task needs a clearer completion condition.","findings":[{"code":"missing_done_condition","severity":"warning","component_id":10,"related_component_id":0,"task_id":11,"message":"The task does not state what proves it is done.","suggestion":"Name the visible result and acceptance check.","confidence":0.86}]}`)
	}))
	defer server.Close()

	client, err := NewOpenAI(server.URL+"/v1", "", "plan-model")
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	result, err := client.ValidateWorkPlan(context.Background(), testWorkPlan())
	if err != nil {
		t.Fatalf("ValidateWorkPlan: %v", err)
	}
	if client.ModelName() != "plan-model" {
		t.Fatalf("ModelName = %q", client.ModelName())
	}
	if len(result.Findings) != 1 || result.Findings[0].TaskID != 11 || result.Findings[0].Code != "missing_done_condition" {
		t.Fatalf("findings = %#v", result.Findings)
	}
}

func TestValidateWorkPlanRetriesUnknownIDs(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			writeChatCompletion(t, w, `{"summary":"Needs work.","findings":[{"code":"task_not_outcome","severity":"warning","component_id":10,"related_component_id":0,"task_id":999,"message":"Unknown task.","suggestion":"Rewrite it.","confidence":0.8}]}`)
			return
		}
		writeChatCompletion(t, w, `{"summary":"The plan describes concrete outcomes.","findings":[]}`)
	}))
	defer server.Close()

	client, err := NewOpenAI(server.URL+"/v1", "", "plan-model")
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	result, err := client.ValidateWorkPlan(context.Background(), testWorkPlan())
	if err != nil {
		t.Fatalf("ValidateWorkPlan: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
	if len(result.Findings) != 0 {
		t.Fatalf("findings = %#v", result.Findings)
	}
}

func TestValidateWorkPlanUsesConfiguredBudgetInsteadOfFixedPayloadCap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		joined := ""
		for _, message := range request.Messages {
			joined += message.Content
		}
		if len(joined) <= 120_000 {
			t.Fatalf("request prompt is %d bytes, want it to exceed the removed fixed cap", len(joined))
		}
		writeChatCompletion(t, w, `{"summary":"The plan was reviewed.","findings":[]}`)
	}))
	defer server.Close()

	plan := testWorkPlan()
	plan.Components[0].Description = strings.Repeat("A", 130_000)
	client, err := NewOpenAIWithTimeout(server.URL+"/v1", "", "plan-model", time.Second)
	if err != nil {
		t.Fatalf("NewOpenAIWithTimeout: %v", err)
	}
	if _, err := client.ValidateWorkPlan(context.Background(), plan); err != nil {
		t.Fatalf("ValidateWorkPlan: %v", err)
	}
}
