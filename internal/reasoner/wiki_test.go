package reasoner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// wikiTestServer answers chat completions with the given contents in order.
func wikiTestServer(t *testing.T, replies ...string) (*httptest.Server, *[]string) {
	t.Helper()
	var prompts []string
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var joined []string
		for _, message := range body.Messages {
			joined = append(joined, message.Role+": "+message.Content)
		}
		prompts = append(prompts, strings.Join(joined, "\n"))
		reply := replies[len(replies)-1]
		if calls < len(replies) {
			reply = replies[calls]
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": reply}}},
		})
	}))
	t.Cleanup(server.Close)
	return server, &prompts
}

func TestDraftWikiPageRejectsUnknownCitationsThenAccepts(t *testing.T) {
	bad := `{"title":"Ports","summary":"s","content":"The API listens on 8080 [@fact:99].","tags":["api"]}`
	good := `{"title":"Ports","summary":"s","content":"The API listens on 8080 [@fact:12]. See [[index]].","tags":["api"]}`
	server, prompts := wikiTestServer(t, bad, good)
	client, err := NewOpenAI(server.URL+"/v1", "", "wiki-model")
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	draft, err := client.DraftWikiPage(context.Background(), WikiDraftRequest{
		Slug: "api/ports", Topic: "API ports",
		Sources: []WikiSourceInput{{Ref: "fact:12", Content: "API listens on port 8080"}},
		Pages:   []WikiPageRef{{Slug: "index", Title: "Index"}},
	})
	if err != nil {
		t.Fatalf("DraftWikiPage: %v", err)
	}
	if draft.Title != "Ports" || !strings.Contains(draft.Content, "[@fact:12]") {
		t.Fatalf("draft = %+v", draft)
	}
	if len(*prompts) != 2 || !strings.Contains((*prompts)[1], "unknown refs: fact:99") {
		t.Fatalf("expected a retry naming the unknown citation, got %d prompts", len(*prompts))
	}
	if !strings.Contains((*prompts)[0], "[[index]] Index") || !strings.Contains((*prompts)[0], "[@fact:12]\nAPI listens on port 8080") {
		t.Fatalf("prompt did not offer pages and sources: %s", (*prompts)[0])
	}
}

func TestDraftWikiPageFailsWithoutCitations(t *testing.T) {
	server, _ := wikiTestServer(t, `{"title":"x","summary":"","content":"No evidence here.","tags":[]}`)
	client, _ := NewOpenAI(server.URL+"/v1", "", "wiki-model")
	_, err := client.DraftWikiPage(context.Background(), WikiDraftRequest{Slug: "x", Topic: "x", Sources: []WikiSourceInput{{Ref: "fact:1", Content: "a"}}})
	if err == nil || !strings.Contains(err.Error(), "no citations") {
		t.Fatalf("error = %v, want missing citations", err)
	}
}

func TestLimitedDraftTrimsSourcesToBudget(t *testing.T) {
	server, prompts := wikiTestServer(t, `{"title":"t","summary":"","content":"a [@fact:1]","tags":[]}`)
	client, _ := NewOpenAI(server.URL+"/v1", "", "wiki-model")
	limited := NewLimited(client, 64, 0)
	draft, err := limited.DraftWikiPage(context.Background(), WikiDraftRequest{
		Slug: "t", Topic: "t",
		Sources: []WikiSourceInput{{Ref: "fact:1", Content: strings.Repeat("a", 40)}, {Ref: "fact:2", Content: strings.Repeat("b", 400)}},
	})
	if err != nil || draft == nil {
		t.Fatalf("DraftWikiPage = %+v, %v", draft, err)
	}
	if strings.Contains((*prompts)[0], "fact:2") {
		t.Fatal("the lowest-ranked source should have been trimmed to fit the budget")
	}
}
