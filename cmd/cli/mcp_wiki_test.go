package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/config"
	"github.com/alash3al/stash/internal/models"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestWikiToolsAreRegisteredAndScoped(t *testing.T) {
	s := newMCPServer(nil)
	for _, name := range []string{"list_wiki_pages", "get_wiki_page", "save_wiki_page"} {
		tool := s.GetTool(name)
		if tool == nil || !strings.Contains(tool.Tool.Description, "namespace") {
			t.Fatalf("wiki tool %s absent or undocumented", name)
		}
		if tool.Tool.InputSchema.AdditionalProperties != false {
			t.Fatalf("%s allows unspecified arguments", name)
		}
		required := map[string]bool{}
		for _, key := range tool.Tool.InputSchema.Required {
			required[key] = true
		}
		if !required["namespace"] || (name != "list_wiki_pages" && !required["slug"]) || (name == "save_wiki_page" && !required["expected_revision"]) {
			t.Fatalf("%s required fields = %v", name, required)
		}
		if name != "save_wiki_page" && (tool.Tool.Annotations.ReadOnlyHint == nil || !*tool.Tool.Annotations.ReadOnlyHint) {
			t.Fatalf("%s lacks read-only annotation", name)
		}
	}
}

func wikiSaveArguments(namespace, slug string, revision, source int64, markdown string) map[string]any {
	return map[string]any{"namespace": namespace, "slug": slug, "expected_revision": revision, "title": "Title", "markdown": markdown, "source_episode_ids": []int64{source}}
}

func TestWikiSaveParsingRejectsLossyOrAmbiguousInputs(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"missing expected revision", func(a map[string]any) { delete(a, "expected_revision") }},
		{"fractional revision", func(a map[string]any) { a["expected_revision"] = 1.5 }},
		{"string revision", func(a map[string]any) { a["expected_revision"] = "0" }},
		{"null revision", func(a map[string]any) { a["expected_revision"] = nil }},
		{"negative revision", func(a map[string]any) { a["expected_revision"] = -1 }},
		{"fractional source", func(a map[string]any) { a["source_episode_ids"] = []float64{1.5} }},
		{"stringified sources", func(a map[string]any) { a["source_episode_ids"] = "[1]" }},
		{"null sources", func(a map[string]any) { a["source_episode_ids"] = nil }},
		{"empty sources", func(a map[string]any) { a["source_episode_ids"] = []int64{} }},
		{"missing body", func(a map[string]any) { delete(a, "markdown") }},
		{"wrong related type", func(a map[string]any) { a["related_slugs"] = []int{1} }},
		{"null related array", func(a map[string]any) { a["related_slugs"] = nil }},
		{"string deleted flag", func(a map[string]any) { a["deleted"] = "true" }},
		{"misspelled argument", func(a map[string]any) { a["delated"] = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := wikiSaveArguments("/", "topic", 0, 1, "body")
			tc.edit(args)
			request := mcp.CallToolRequest{}
			request.Params.Arguments = args
			if _, err := parseWikiSaveInput(request); err == nil {
				t.Fatal("ambiguous save accepted")
			}
		})
	}
	for _, key := range []string{"title", "markdown", "source_episode_ids", "related_slugs"} {
		for _, value := range []any{nil, "", []any{}} {
			request := mcp.CallToolRequest{}
			request.Params.Arguments = map[string]any{"slug": "topic", "expected_revision": 1, "deleted": true, key: value}
			if _, err := parseWikiSaveInput(request); err == nil {
				t.Fatalf("deletion accepted %s=%#v", key, value)
			}
		}
	}
	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{"slug": "topic", "expected_revision": float64(3), "deleted": true}
	input, err := parseWikiSaveInput(request)
	if err != nil || !input.Deleted || input.ExpectedRevision != 3 || input.Title != nil || input.SourceEpisodeIDs != nil {
		t.Fatalf("valid deletion = %#v, %v", input, err)
	}
}

func TestWikiBodyPagesRespectBytesAndPreserveUnicode(t *testing.T) {
	bc := &bootstrap.Context{Config: &config.Config{MCPMaxResponseBytes: 1024}}
	body := strings.Repeat("가🙂\\\"\n", 500)
	page := &models.WikiPage{WikiRevision: models.WikiRevision{Slug: "topic", Revision: 1, Title: "Title", Markdown: body, SourceEpisodeIDs: []int64{1}, RelatedSlugs: []string{}}, CurrentRevision: 2, Sources: []models.WikiSource{{EpisodeID: 1, Available: true}}, RelatedPages: []models.WikiRelatedPage{}}
	var joined strings.Builder
	offset := 0
	for {
		result, err := wikiPageResult(bc, page, offset, 1000, true)
		if err != nil || result.IsError {
			t.Fatalf("body page failed = %#v, %v", result, err)
		}
		text := toolResultText(t, result)
		if len(text) > 1024 {
			t.Fatalf("body page exceeds byte cap: %d", len(text))
		}
		var chunk wikiBodyPage
		if err := json.Unmarshal([]byte(text), &chunk); err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(chunk.Markdown) || chunk.Revision != 1 || chunk.CurrentRevision != 2 || chunk.Offset != offset {
			t.Fatalf("body cursor/version = %#v", chunk)
		}
		joined.WriteString(chunk.Markdown)
		if !chunk.HasMore {
			break
		}
		if chunk.NextOffset <= offset {
			t.Fatal("body cursor did not advance")
		}
		offset = chunk.NextOffset
	}
	if joined.String() != body || page.Markdown != body {
		t.Fatal("paging changed or omitted original markdown")
	}
	page.Title = strings.Repeat("metadata", 1000)
	result, err := wikiPageResult(bc, page, 0, 1000, true)
	if err != nil || !result.IsError {
		t.Fatalf("unrepresentable metadata did not fail explicitly: %#v, %v", result, err)
	}
}

func TestWikiListResponseContinuesAtActualReturnedOffset(t *testing.T) {
	bc := &bootstrap.Context{Config: &config.Config{MCPMaxResponseBytes: 1024}}
	list := &models.WikiPageList{Pages: []models.WikiPageSummary{}}
	for i := 0; i < 30; i++ {
		list.Pages = append(list.Pages, models.WikiPageSummary{Slug: fmt.Sprintf("topic-%d", i), Revision: 1, Title: "Title", Summary: strings.Repeat("요약", 50)})
	}
	offset := 40
	seen := 0
	for len(list.Pages) > 0 {
		result, err := wikiListResult(bc, list, offset)
		if err != nil || result.IsError {
			t.Fatalf("list page failed = %#v, %v", result, err)
		}
		text := toolResultText(t, result)
		if len(text) > 1024 {
			t.Fatal("list response exceeded cap")
		}
		var page models.WikiPageList
		if err := json.Unmarshal([]byte(text), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Pages) == 0 || page.NextOffset != offset+len(page.Pages) || page.HasMore != (len(page.Pages) < len(list.Pages)) {
			t.Fatalf("list cursor = %#v", page)
		}
		seen += len(page.Pages)
		offset = page.NextOffset
		list.Pages = list.Pages[len(page.Pages):]
	}
	if seen != 30 {
		t.Fatalf("list silently omitted items: %d", seen)
	}
}

func newWikiMCPTestBootstrap(t *testing.T) (*bootstrap.Context, context.Context) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("STASH_TEST_DATABASE_URL"))
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("STASH_TEST_POSTGRES_DSN"))
	}
	if dsn == "" {
		t.Skip("set STASH_TEST_DATABASE_URL to a disposable pgvector PostgreSQL database")
	}
	for key, value := range map[string]string{
		"STASH_POSTGRES_DSN": dsn, "STASH_VECTOR_DIM": "3", "STASH_MAX_RESULT_SIZE": "1000",
		"STASH_OPENAI_BASE_URL": "http://127.0.0.1:9/v1", "STASH_OPENAI_API_KEY": "",
		"STASH_EMBEDDING_MODEL": "work-execution-test", "STASH_REASONER_MODEL": "wiki-test",
		"STASH_CONTEXT_TTL": "1h", "STASH_HTTP_ADDR": "127.0.0.1:8080",
		"STASH_LOG_LEVEL": "error", "STASH_LOG_FORMAT": "text", "STASH_AUTH_MODE": "none",
	} {
		t.Setenv(key, value)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)
	bc, err := bootstrap.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bc.Close() })
	bc.Config.MCPMaxResponseBytes = 1024
	return bc, ctx
}

func wikiMCPNamespace(t *testing.T, bc *bootstrap.Context, ctx context.Context, logical, content string) int64 {
	t.Helper()
	path, err := resolveSingleNamespace(ctx, logical)
	if err != nil {
		t.Fatal(err)
	}
	nsID, err := bc.Brain.CreateNamespace(ctx, path, "Wiki test", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = bc.Pool.Exec(context.Background(), `DELETE FROM namespaces WHERE id=$1`, nsID) })
	var source int64
	if err := bc.Pool.QueryRow(ctx, `INSERT INTO episodes(namespace_id,content,occurred_at) VALUES($1,$2,now()) RETURNING id`, nsID, content).Scan(&source); err != nil {
		t.Fatal(err)
	}
	return source
}

func wikiMCPCall(s *server.MCPServer, ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	// Match JSON-RPC's numeric decoding, including existing get_memory tools.
	data, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	var wireArguments map[string]any
	if err := json.Unmarshal(data, &wireArguments); err != nil {
		return nil, err
	}
	request := mcp.CallToolRequest{}
	request.Params.Name, request.Params.Arguments = name, wireArguments
	return s.GetTool(name).Handler(ctx, request)
}

func wikiMCPResult[T any](t *testing.T, s *server.MCPServer, ctx context.Context, name string, args map[string]any) T {
	t.Helper()
	result, err := wikiMCPCall(s, ctx, name, args)
	if err != nil || result.IsError {
		t.Fatalf("%s failed: %#v, %v", name, result, err)
	}
	var value T
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestWikiMCPPinsBodyRevisionAndReadsFullSourcesPostgres(t *testing.T) {
	bc, ctx := newWikiMCPTestBootstrap(t)
	namespace := fmt.Sprintf("/wiki-mcp-%d", time.Now().UnixNano())
	original := strings.Repeat("원문 문장\n", 300)
	source := wikiMCPNamespace(t, bc, ctx, namespace, original)
	s := newMCPServer(bc)
	body := strings.Repeat("위키🙂\\\"\n", 300)
	wikiMCPResult[map[string]any](t, s, ctx, "save_wiki_page", wikiSaveArguments(namespace, "topic", 0, source, body))
	first := wikiMCPResult[wikiBodyPage](t, s, ctx, "get_wiki_page", map[string]any{"namespace": namespace, "slug": "topic"})
	if first.Revision != 1 || !first.HasMore {
		t.Fatalf("initial page = %#v", first)
	}
	wikiMCPResult[map[string]any](t, s, ctx, "save_wiki_page", wikiSaveArguments(namespace, "topic", 1, source, "updated body"))
	joined := first.Markdown
	next := first.NextOffset
	for first.HasMore {
		first = wikiMCPResult[wikiBodyPage](t, s, ctx, "get_wiki_page", map[string]any{"namespace": namespace, "slug": "topic", "revision": 1, "offset": next})
		if first.Revision != 1 || first.CurrentRevision != 2 || (first.HasMore && first.NextOffset <= next) {
			t.Fatalf("revision changed or cursor stalled: %#v", first)
		}
		joined += first.Markdown
		next = first.NextOffset
	}
	if joined != body {
		t.Fatal("concurrent update mixed body pages")
	}
	if _, err := wikiMCPCall(s, ctx, "get_wiki_page", map[string]any{"namespace": namespace, "slug": "topic", "offset": 1}); err == nil {
		t.Fatal("continuation accepted without a fixed revision")
	}
	wikiMCPResult[map[string]any](t, s, ctx, "save_wiki_page", map[string]any{"namespace": namespace, "slug": "topic", "expected_revision": 2, "deleted": true})
	tombstone := wikiMCPResult[map[string]any](t, s, ctx, "get_wiki_page", map[string]any{"namespace": namespace, "slug": "topic"})
	if len(tombstone) != 4 || tombstone["deleted"] != true || tombstone["revision"] != float64(3) {
		t.Fatalf("default deleted read leaked body: %#v", tombstone)
	}
	history := wikiMCPResult[wikiBodyPage](t, s, ctx, "get_wiki_page", map[string]any{"namespace": namespace, "slug": "topic", "revision": 1})
	if history.Markdown == "" || history.Deleted || history.Revision != 1 || history.CurrentRevision != 3 {
		t.Fatalf("historical read after deletion = %#v", history)
	}
	result, err := wikiMCPCall(s, ctx, "save_wiki_page", wikiSaveArguments(namespace, "topic", 99, source, "must conflict"))
	if err != nil || !result.IsError {
		t.Fatalf("future revision not an MCP conflict: %#v, %v", result, err)
	}
	var conflict map[string]any
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &conflict); err != nil || conflict["error"] != "wiki_revision_conflict" || conflict["current_revision"] != float64(3) {
		t.Fatalf("conflict receipt = %#v, %v", conflict, err)
	}
	var reconstructed strings.Builder
	offset, snapshot := 0, ""
	for {
		memory := wikiMCPResult[map[string]any](t, s, ctx, "get_memory", map[string]any{"namespace": namespace, "memory_type": "episode", "memory_id": source, "offset": offset, "snapshot": snapshot})
		reconstructed.WriteString(memory["content"].(string))
		if snapshot != "" && snapshot != memory["snapshot"] {
			t.Fatal("original source snapshot changed")
		}
		snapshot = memory["snapshot"].(string)
		if !memory["has_more"].(bool) {
			break
		}
		next := int(memory["next_offset"].(float64))
		if next <= offset {
			t.Fatal("source cursor stalled")
		}
		offset = next
	}
	if reconstructed.String() != original {
		t.Fatal("original source was changed or incompletely read")
	}
}

func TestWikiMCPRejectsCrossUserReadsAndSourcesPostgres(t *testing.T) {
	bc, ctx := newWikiMCPTestBootstrap(t)
	namespace := fmt.Sprintf("/wiki-users-%d", time.Now().UnixNano())
	alice := context.WithValue(context.WithValue(ctx, keyMode, "remote"), keySSOUser, "wiki-alice")
	bob := context.WithValue(context.WithValue(ctx, keyMode, "remote"), keySSOUser, "wiki-bob")
	aliceSource := wikiMCPNamespace(t, bc, alice, namespace, "Alice original")
	bobSource := wikiMCPNamespace(t, bc, bob, namespace, "Bob private original")
	s := newMCPServer(bc)
	wikiMCPResult[map[string]any](t, s, bob, "save_wiki_page", wikiSaveArguments(namespace, "bob-only", 0, bobSource, "Bob private wiki"))
	if _, err := wikiMCPCall(s, alice, "get_wiki_page", map[string]any{"namespace": namespace, "slug": "bob-only", "revision": 1}); err == nil {
		t.Fatal("Alice read Bob's wiki through a logical namespace")
	}
	if _, err := wikiMCPCall(s, alice, "save_wiki_page", wikiSaveArguments(namespace, "topic", 0, bobSource, "foreign source")); err == nil {
		t.Fatal("Alice linked Bob's private source")
	}
	args := wikiSaveArguments(namespace, "topic", 0, aliceSource, "foreign link")
	args["related_slugs"] = []string{"bob-only"}
	if _, err := wikiMCPCall(s, alice, "save_wiki_page", args); err == nil {
		t.Fatal("Alice linked Bob's private page")
	}
	list := wikiMCPResult[models.WikiPageList](t, s, alice, "list_wiki_pages", map[string]any{"namespace": namespace})
	if len(list.Pages) != 0 {
		t.Fatalf("foreign wiki leaked into list: %#v", list)
	}
	internalPath, err := resolveSingleNamespace(bob, namespace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wikiMCPCall(s, alice, "get_wiki_page", map[string]any{"namespace": internalPath, "slug": "bob-only"}); err == nil {
		t.Fatal("internal namespace path bypassed authenticated scoping")
	}
	for _, name := range []string{"list_wiki_pages", "get_wiki_page", "save_wiki_page"} {
		unauthenticated := context.WithValue(ctx, keyMode, "remote")
		args := map[string]any{"namespace": namespace}
		if name == "get_wiki_page" {
			args["slug"] = "topic"
		} else if name == "save_wiki_page" {
			args = wikiSaveArguments(namespace, "topic", 0, aliceSource, "body")
		}
		if _, err := wikiMCPCall(s, unauthenticated, name, args); err == nil || !strings.Contains(err.Error(), "unauthorized") {
			t.Fatalf("%s accepted an unverified remote identity: %v", name, err)
		}
	}
}
