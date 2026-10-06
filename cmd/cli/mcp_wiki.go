package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/brain"
	"github.com/alash3al/stash/internal/models"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// wikiActor records who wrote a page. A verified login identity wins; an
// unauthenticated local agent may name itself. author_kind=human marks
// console edits so a reader can tell reviewed text from agent output.
func wikiActor(ctx context.Context, request mcp.CallToolRequest) (string, string) {
	kind := request.GetString("author_kind", "agent")
	if kind != "human" {
		kind = "agent"
	}
	if user, ok := ctx.Value(keySSOUser).(string); ok && user != "" {
		return user, kind
	}
	return request.GetString("author", ""), kind
}

func wikiSourceInputs(request mcp.CallToolRequest) ([]brain.WikiSourceInput, error) {
	refs, err := stringListArgument(request, "sources")
	if err != nil {
		return nil, err
	}
	inputs := make([]brain.WikiSourceInput, 0, len(refs))
	for _, ref := range refs {
		if strings.TrimSpace(ref) == "" {
			continue
		}
		parsed, err := brain.ParseWikiSourceRef(ref)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, parsed)
	}
	return inputs, nil
}

// wikiPageResponse is the bounded shape returned after a write or in a list:
// metadata, relations, and warnings, never the full body.
type wikiPageResponse struct {
	Page      models.WikiPage     `json:"page"`
	Links     []models.WikiLink   `json:"links"`
	Backlinks []models.WikiLink   `json:"backlinks"`
	Sources   []models.WikiSource `json:"sources"`
	Warnings  []string            `json:"warnings,omitempty"`
	Created   bool                `json:"created,omitempty"`
	Message   string              `json:"message,omitempty"`
}

func wikiResponse(detail *models.WikiPageDetail, created bool) wikiPageResponse {
	out := wikiPageResponse{Page: detail.Page, Links: detail.Links, Backlinks: detail.Backlinks, Sources: detail.Sources, Created: created}
	out.Page.Content = ""
	for _, link := range detail.Links {
		if link.TargetPageID == nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("[[%s]] does not exist yet", link.TargetSlug))
		}
	}
	for _, source := range detail.Sources {
		if source.Status != brain.WikiSourceOK {
			out.Warnings = append(out.Warnings, fmt.Sprintf("source %s:%s is %s", source.SourceType, source.SourceRef, source.Status))
		}
	}
	if !detail.Page.Indexed {
		out.Message = "Page saved; vector indexing is pending, keyword search already works."
	}
	return out
}

func registerWikiTools(mcpServer *server.MCPServer, bc *bootstrap.Context) {
	mcpServer.AddTool(mcp.NewTool("wiki_write",
		mcp.WithDescription(render("wiki_write_description")),
		mcp.WithString("namespace", mcp.Required(), mcp.Description(render("wiki_namespace"))),
		mcp.WithString("slug", mcp.Required(), mcp.Description(render("wiki_slug"))),
		mcp.WithString("title", mcp.Required(), mcp.Description("Human-readable page title")),
		mcp.WithString("content", mcp.Required(), mcp.Description(render("wiki_content"))),
		mcp.WithString("summary", mcp.Description("One or two sentences shown in lists and search results; unchanged when omitted on an update")),
		mcp.WithString("kind", mcp.Enum("article", "index", "entity", "decision", "log"), mcp.Description("article (default), index (links the main pages), entity (one thing), decision (why something was decided), log (running notes)")),
		mcp.WithString("tags", mcp.Description("Comma-separated tags; omit on an update to keep the stored tags")),
		mcp.WithString("sources", mcp.Description(render("wiki_sources"))),
		mcp.WithString("change_note", mcp.Description("What changed and why; shown in the page history")),
		mcp.WithNumber("expected_revision", mcp.Description(render("wiki_expected_revision")), mcp.DefaultNumber(0)),
		mcp.WithString("author", mcp.Description("Agent identity recorded as the author when no login identity exists")),
		mcp.WithString("author_kind", mcp.Enum("agent", "human"), mcp.DefaultString("agent")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", ""))
		if err != nil {
			return nil, err
		}
		sources, err := wikiSourceInputs(request)
		if err != nil {
			return nil, err
		}
		author, authorKind := wikiActor(ctx, request)
		input := brain.WikiPageInput{
			Slug:             request.GetString("slug", ""),
			Title:            request.GetString("title", ""),
			Kind:             request.GetString("kind", ""),
			Summary:          request.GetString("summary", ""),
			Content:          request.GetString("content", ""),
			Sources:          sources,
			ChangeNote:       request.GetString("change_note", ""),
			ExpectedRevision: request.GetInt("expected_revision", 0),
			Author:           author,
			AuthorKind:       authorKind,
		}
		if _, present := request.GetArguments()["tags"]; present {
			tags, err := stringListArgument(request, "tags")
			if err != nil {
				return nil, err
			}
			input.Tags = tags
		}
		detail, created, err := bc.Brain.WriteWikiPage(ctx, namespaceID, input)
		if err != nil {
			if errors.Is(err, brain.ErrWikiRevisionConflict) {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return nil, err
		}
		if !detail.Page.Indexed {
			recordEmbeddingQueued()
		}
		return jsonToolResult(bc, wikiResponse(detail, created))
	})

	mcpServer.AddTool(mcp.NewTool("wiki_read",
		mcp.WithDescription(render("wiki_read_description")),
		mcp.WithString("namespace", mcp.Required(), mcp.Description(render("wiki_namespace"))),
		mcp.WithString("slug", mcp.Required()),
		mcp.WithNumber("revision", mcp.Description("Read a historical revision instead of the current page"), mcp.DefaultNumber(0)),
		mcp.WithNumber("offset", mcp.Description("Character offset into the content"), mcp.DefaultNumber(0)),
		mcp.WithNumber("limit", mcp.Description("Characters per page, at most 20000"), mcp.DefaultNumber(8000)),
		mcp.WithString("snapshot", mcp.Description("Snapshot from the previous page of the same read; rejects a page that changed in between")),
		mcp.WithReadOnlyHintAnnotation(true),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", ""))
		if err != nil {
			return nil, err
		}
		slug := request.GetString("slug", "")
		detail, err := bc.Brain.GetWikiPage(ctx, namespaceID, slug)
		if err != nil {
			return nil, err
		}
		content, revision := detail.Page.Content, detail.Page.Revision
		if wanted := request.GetInt("revision", 0); wanted > 0 && wanted != detail.Page.Revision {
			historical, err := bc.Brain.GetWikiRevision(ctx, namespaceID, slug, wanted)
			if err != nil {
				return nil, err
			}
			content, revision = historical.Content, historical.Revision
		}
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))[:32]
		if snapshot := request.GetString("snapshot", ""); snapshot != "" && snapshot != digest {
			return mcp.NewToolResultError("the page changed since the previous read; start again from offset 0"), nil
		}
		runes := []rune(content)
		offset, limit := request.GetInt("offset", 0), request.GetInt("limit", 8000)
		if offset < 0 || offset > len(runes) || limit < 1 || limit > 20000 {
			return nil, fmt.Errorf("invalid content window")
		}
		page := detail.Page
		page.Content = ""
		end := min(offset+limit, len(runes))
		for {
			value := map[string]any{
				"page": page, "revision": revision, "content": string(runes[offset:end]), "has_more": end < len(runes), "next_offset": end,
				"total_characters": len(runes), "snapshot": digest, "links": detail.Links, "backlinks": detail.Backlinks, "sources": detail.Sources,
			}
			payload, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			if len(payload) <= mcpMaxResponseBytes(bc) {
				return textToolResult(payload), nil
			}
			if end-offset <= 1 {
				return mcp.NewToolResultError("the MCP response limit is too small to return this page; raise STASH_MCP_MAX_RESPONSE_BYTES"), nil
			}
			end = offset + (end-offset)/2
		}
	})

	mcpServer.AddTool(mcp.NewTool("wiki_list",
		mcp.WithDescription(render("wiki_list_description")),
		mcp.WithString("namespace", mcp.Required(), mcp.Description(render("wiki_namespace"))),
		mcp.WithString("q", mcp.Description("Keyword filter over title, summary, content, and slug")),
		mcp.WithString("kind", mcp.Enum("article", "index", "entity", "decision", "log")),
		mcp.WithString("tag"),
		mcp.WithBoolean("stale", mcp.Description("true lists only pages whose cited evidence changed; false hides them")),
		mcp.WithNumber("limit", mcp.Description(render("pagination_limit")), mcp.DefaultNumber(100)),
		mcp.WithNumber("offset", mcp.Description(render("pagination_offset")), mcp.DefaultNumber(0)),
		mcp.WithReadOnlyHintAnnotation(true),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", ""))
		if err != nil {
			return nil, err
		}
		filter := brain.WikiListFilter{Query: request.GetString("q", ""), Kind: request.GetString("kind", ""), Tag: request.GetString("tag", "")}
		if _, present := request.GetArguments()["stale"]; present {
			stale := request.GetBool("stale", false)
			filter.Stale = &stale
		}
		page := brain.Pagination{Limit: request.GetInt("limit", 100), Offset: request.GetInt("offset", 0)}
		pages, err := bc.Brain.ListWikiPages(ctx, namespaceID, filter, page)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, pages, page.Offset)
	})

	mcpServer.AddTool(mcp.NewTool("wiki_search",
		mcp.WithDescription(render("wiki_search_description")),
		mcp.WithString("query", mcp.Required(), mcp.Description(render("recall_query"))),
		mcp.WithString("namespaces", mcp.Description(render("recall_namespaces"))),
		mcp.WithNumber("limit", mcp.Description(render("limit_param")), mcp.DefaultNumber(10)),
		mcp.WithNumber("offset", mcp.Description(render("pagination_offset")), mcp.DefaultNumber(0)),
		mcp.WithNumber("min_score", mcp.Description(render("recall_min_score")), mcp.DefaultNumber(0)),
		mcp.WithReadOnlyHintAnnotation(true),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespaces, err := resolveNamespaces(ctx, request.GetString("namespaces", "/"))
		if err != nil {
			return nil, err
		}
		offset := request.GetInt("offset", 0)
		results, err := bc.Brain.SearchWiki(ctx, namespaces, request.GetString("query", ""), request.GetInt("limit", 10), brain.RecallOptions{
			MinScore: float32(request.GetFloat("min_score", 0)),
			Offset:   offset,
		})
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, results, offset)
	})

	mcpServer.AddTool(mcp.NewTool("wiki_delete",
		mcp.WithDescription(render("wiki_delete_description")),
		mcp.WithString("namespace", mcp.Required(), mcp.Description(render("wiki_namespace"))),
		mcp.WithString("slug", mcp.Required()),
		mcp.WithBoolean("restore", mcp.Description("true restores a deleted page instead"), mcp.DefaultBool(false)),
		mcp.WithString("author"), mcp.WithString("author_kind", mcp.Enum("agent", "human"), mcp.DefaultString("agent")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", ""))
		if err != nil {
			return nil, err
		}
		author, authorKind := wikiActor(ctx, request)
		slug := request.GetString("slug", "")
		if request.GetBool("restore", false) {
			if err := bc.Brain.RestoreWikiPage(ctx, namespaceID, slug, author, authorKind); err != nil {
				return nil, err
			}
			return jsonToolResult(bc, map[string]any{"ok": true, "slug": slug, "restored": true})
		}
		if err := bc.Brain.DeleteWikiPage(ctx, namespaceID, slug, author, authorKind); err != nil {
			return nil, err
		}
		return jsonToolResult(bc, map[string]any{"ok": true, "slug": slug, "deleted": true, "message": "Page hidden; wiki_delete with restore=true brings it back"})
	})

	mcpServer.AddTool(mcp.NewTool("wiki_history",
		mcp.WithDescription("List a page's revisions, newest first. Read one with wiki_read revision=N."),
		mcp.WithString("namespace", mcp.Required(), mcp.Description(render("wiki_namespace"))),
		mcp.WithString("slug", mcp.Required()),
		mcp.WithNumber("limit", mcp.Description(render("pagination_limit")), mcp.DefaultNumber(50)),
		mcp.WithNumber("offset", mcp.Description(render("pagination_offset")), mcp.DefaultNumber(0)),
		mcp.WithReadOnlyHintAnnotation(true),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", ""))
		if err != nil {
			return nil, err
		}
		page := brain.Pagination{Limit: request.GetInt("limit", 50), Offset: request.GetInt("offset", 0)}
		revisions, err := bc.Brain.WikiPageRevisions(ctx, namespaceID, request.GetString("slug", ""), page)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, revisions, page.Offset)
	})

	mcpServer.AddTool(mcp.NewTool("wiki_log",
		mcp.WithDescription("Read the wiki activity log for a namespace, newest first: creates, updates, deletes, compiles, and lint runs."),
		mcp.WithString("namespace", mcp.Required(), mcp.Description(render("wiki_namespace"))),
		mcp.WithNumber("limit", mcp.Description(render("pagination_limit")), mcp.DefaultNumber(50)),
		mcp.WithNumber("offset", mcp.Description(render("pagination_offset")), mcp.DefaultNumber(0)),
		mcp.WithReadOnlyHintAnnotation(true),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", ""))
		if err != nil {
			return nil, err
		}
		page := brain.Pagination{Limit: request.GetInt("limit", 50), Offset: request.GetInt("offset", 0)}
		entries, err := bc.Brain.WikiLog(ctx, namespaceID, page)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, entries, page.Offset)
	})

	mcpServer.AddTool(mcp.NewTool("wiki_lint",
		mcp.WithDescription(render("wiki_lint_description")),
		mcp.WithString("namespace", mcp.Required(), mcp.Description(render("wiki_namespace"))),
		mcp.WithString("author"),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", ""))
		if err != nil {
			return nil, err
		}
		author, authorKind := wikiActor(ctx, request)
		report, err := bc.Brain.LintWiki(ctx, namespaceID, author, authorKind)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, report)
	})

	mcpServer.AddTool(mcp.NewTool("wiki_compile",
		mcp.WithDescription(render("wiki_compile_description")),
		mcp.WithString("namespace", mcp.Required(), mcp.Description(render("wiki_namespace"))),
		mcp.WithString("slug", mcp.Required(), mcp.Description(render("wiki_slug"))),
		mcp.WithString("topic", mcp.Description("What the page should explain; also the recall query when no sources are pinned")),
		mcp.WithString("title"),
		mcp.WithString("sources", mcp.Description("Comma-separated refs such as fact:12,episode:3 to pin the evidence; omitted means recall by topic")),
		mcp.WithNumber("max_sources", mcp.DefaultNumber(20)),
		mcp.WithBoolean("save", mcp.Description("true stores the draft as a server-authored page; false only returns it"), mcp.DefaultBool(false)),
		mcp.WithString("change_note"),
		mcp.WithString("author", mcp.Description("Recorded as the requesting agent when the draft is saved")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", ""))
		if err != nil {
			return nil, err
		}
		sources, err := stringListArgument(request, "sources")
		if err != nil {
			return nil, err
		}
		author, _ := wikiActor(ctx, request)
		result, err := bc.Brain.CompileWikiPage(ctx, namespaceID, brain.WikiCompileRequest{
			Slug: request.GetString("slug", ""), Title: request.GetString("title", ""), Topic: request.GetString("topic", ""),
			SourceRefs: sources, MaxSources: request.GetInt("max_sources", 20), Save: request.GetBool("save", false),
			Author: author, ChangeNote: request.GetString("change_note", ""),
		})
		if err != nil {
			if errors.Is(err, brain.ErrWikiCompileUnavailable) {
				return mcp.NewToolResultError("no reasoning provider is assigned to the wiki feature; assign one in model settings or write the page with wiki_write"), nil
			}
			return nil, err
		}
		if result.Page != nil {
			result.Page.Page.Content = ""
		}
		return jsonToolResult(bc, result)
	})
}
