package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"time"

	"github.com/alash3al/stash/internal/auth"
	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/brain"
	"github.com/alash3al/stash/internal/models"
	"github.com/alash3al/stash/internal/observability"
	"github.com/alash3al/stash/internal/web"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/urfave/cli/v3"
)

func stdioContextFunc(ctx context.Context) context.Context {
	return context.WithValue(ctx, keyMode, "local")
}

func stdioContextFuncFor(provider *auth.Provider) server.StdioContextFunc {
	return func(ctx context.Context) context.Context {
		if provider == nil || provider.Mode() == "none" {
			return context.WithValue(ctx, keyMode, "local")
		}
		if provider.Mode() != "stdio" {
			// HTTP OAuth does not apply to a trusted local STDIO process.
			return context.WithValue(ctx, keyMode, "local")
		}

		rawToken := provider.StdioCredential()
		if rawToken == "" {
			return context.WithValue(ctx, keyMode, "local")
		}
		user, err := provider.VerifyBearerToken(ctx, rawToken)
		if err != nil || user == "" {
			// Keep the request in the remote mode without an identity. Namespace
			// resolution will reject it instead of silently falling back to /.
			return context.WithValue(ctx, keyMode, "remote")
		}
		ctx = context.WithValue(ctx, keyMode, "remote")
		return context.WithValue(ctx, keySSOUser, user)
	}
}

//go:embed mcp_prompts.tmpl
var mcpPromptsFS string

var mcpTmpl *template.Template

func init() {
	var err error
	mcpTmpl, err = template.New("mcp_prompts").Parse(mcpPromptsFS)
	if err != nil {
		panic(fmt.Sprintf("parse mcp_prompts.tmpl: %v", err))
	}
}

func render(name string) string {
	var buf strings.Builder
	if err := mcpTmpl.ExecuteTemplate(&buf, name, nil); err != nil {
		return ""
	}
	return buf.String()
}

// observeMCPTool gives every tool call a finite lifetime. A provider or a
// database connection pool can otherwise leave one call open until the MCP
// client gives up, making an unrelated read or write look broken as well.
func observeMCPTool(logger *slog.Logger, timeout time.Duration) server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			started := time.Now()
			toolCtx := ctx
			var cancel context.CancelFunc
			if timeout > 0 {
				toolCtx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			result, err := next(toolCtx, request)
			outcome := "ok"
			if errors.Is(toolCtx.Err(), context.DeadlineExceeded) {
				outcome = "timeout"
				if err == nil {
					result = nil
					err = context.DeadlineExceeded
				}
				if logger != nil {
					logger.Warn("mcp tool timed out",
						"tool", request.Params.Name,
						"timeout", timeout,
						"duration_ms", float64(time.Since(started))/float64(time.Millisecond),
					)
				}
			} else if err != nil || (result != nil && result.IsError) {
				outcome = "error"
			}
			elapsed := time.Since(started)
			observability.RecordMCPToolCall(request.Params.Name, outcome, elapsed)
			if logger != nil {
				args := []any{
					"tool", request.Params.Name,
					"outcome", outcome,
					"duration_ms", float64(elapsed) / float64(time.Millisecond),
				}
				if requestID := observability.RequestID(toolCtx); requestID != "" {
					args = append(args, "request_id", requestID)
				}
				if err != nil {
					errorText := err.Error()
					if len(errorText) > 2000 {
						errorText = errorText[:2000]
					}
					args = append(args, "error", errorText)
				} else if outcome == "error" {
					args = append(args, "error", "tool returned an error result")
				}
				if outcome == "ok" {
					logger.Info("mcp tool call", args...)
				} else {
					logger.Warn("mcp tool call failed", args...)
				}
			}
			return result, err
		}
	}
}

func newMCPServer(bc *bootstrap.Context) *server.MCPServer {
	toolTimeout := 2 * time.Minute
	var logger *slog.Logger
	if bc != nil {
		logger = bc.Logger
		if bc.Config != nil && bc.Config.MCPToolTimeout > 0 {
			toolTimeout = bc.Config.MCPToolTimeout
		}
	}
	mcpServer := server.NewMCPServer("stash", "0.2.8",
		server.WithToolCapabilities(true),
		server.WithDescription(render("server_description")),
		server.WithInstructions(render("server_instructions")),
		server.WithToolHandlerMiddleware(observeMCPTool(logger, toolTimeout)),
	)

	mcpServer.AddTool(mcp.NewTool("init",
		mcp.WithDescription(render("init_description")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		scaffold := []struct {
			slug        string
			name        string
			description string
		}{
			{"/", "Workspace", "Default workspace for this Stash user."},
			{"/self", "Self", "Agent's self-knowledge: capabilities, limits, preferences, and behavioral patterns."},
			{"/self/capabilities", "Capabilities", "What the agent can do well. Strengths and proven competencies."},
			{"/self/limits", "Limits", "What the agent struggles with or cannot do. Known failure modes."},
			{"/self/preferences", "Preferences", "How the agent works best. Behavioral patterns and standing instructions."},
		}

		var created []string
		for _, ns := range scaffold {
			resolved, err := resolveNamespaces(ctx, ns.slug)
			if err != nil {
				return nil, err
			}
			_, err = bc.Brain.CreateNamespace(ctx, resolved[0], ns.name, ns.description)
			if err != nil {
				return nil, fmt.Errorf("init: create namespace %s: %w", ns.slug, err)
			}
			created = append(created, ns.slug)
		}

		result := map[string]any{"ok": true, "namespaces_created": created}
		return jsonToolResult(bc, result)
	})

	mcpServer.AddTool(mcp.NewTool("remember",
		mcp.WithDescription(render("remember_description")),
		mcp.WithString("content", mcp.Description(render("remember_content")), mcp.Required()),
		mcp.WithString("namespace", mcp.Description(render("remember_namespace"))),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content := request.GetString("content", "")
		nss, err := resolveNamespaces(ctx, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}
		namespace := nss[0]

		remembered, err := bc.Brain.RememberWithStatus(ctx, namespace, content, nil)
		if err != nil {
			return nil, err
		}
		message := "Memory remembered successfully"
		if !remembered.Indexed {
			message = "Memory saved; indexing is pending and will retry automatically"
			recordEmbeddingQueued()
			if bc.Logger != nil {
				bc.Logger.Warn("memory saved with pending embedding",
					"episode_id", remembered.ID,
					"retry_at", remembered.RetryAt,
				)
			}
		}
		result := map[string]any{
			"id":              remembered.ID,
			"indexed":         remembered.Indexed,
			"indexing_status": remembered.IndexingStatus,
			"message":         message,
		}
		if remembered.RetryAt != nil {
			result["retry_at"] = remembered.RetryAt
		}
		return jsonToolResult(bc, result)
	})

	mcpServer.AddTool(mcp.NewTool("recall",
		mcp.WithDescription(render("recall_description")),
		mcp.WithString("query", mcp.Description(render("recall_query")), mcp.Required()),
		mcp.WithString("namespaces", mcp.Description(render("recall_namespaces"))),
		mcp.WithNumber("limit", mcp.Description(render("limit_param")), mcp.DefaultNumber(10)),
		mcp.WithNumber("offset", mcp.Description(render("pagination_offset")), mcp.DefaultNumber(0)),
		mcp.WithNumber("min_score", mcp.Description(render("recall_min_score")), mcp.DefaultNumber(0)),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query := request.GetString("query", "")
		limit := request.GetInt("limit", 10)
		offset := request.GetInt("offset", 0)

		namespaces, err := resolveNamespaces(ctx, request.GetString("namespaces", "/"))
		if err != nil {
			return nil, err
		}

		results, err := bc.Brain.RecallWithOptions(ctx, namespaces, query, limit, brain.RecallOptions{
			MinScore: float32(request.GetFloat("min_score", 0)),
			Offset:   offset,
		})
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, results, offset)
	})

	mcpServer.AddTool(mcp.NewTool("forget",
		mcp.WithDescription(render("forget_description")),
		mcp.WithString("about", mcp.Description(render("forget_about")), mcp.Required()),
		mcp.WithString("namespaces", mcp.Description(render("namespaces_param"))),
		mcp.WithNumber("min_score", mcp.Description(render("forget_min_score")), mcp.DefaultNumber(0)),
		mcp.WithBoolean("dry_run", mcp.Description(render("forget_dry_run")), mcp.DefaultBool(false)),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		about := request.GetString("about", "")
		namespaces, err := resolveNamespaces(ctx, request.GetString("namespaces", "/"))
		if err != nil {
			return nil, err
		}

		opts := brain.ForgetOptions{
			MinScore: float32(request.GetFloat("min_score", 0)),
			DryRun:   request.GetBool("dry_run", false),
		}

		res, err := bc.Brain.ForgetEpisodeMatch(ctx, namespaces, about, opts)
		if err != nil {
			return nil, err
		}

		// 무엇이 지워졌는지(또는 왜 안 지워졌는지) 항상 돌려준다.
		// 예전에는 {"ok":true} 만 반환해, 반복 호출이 의도한 대상을 지나쳐도 알 수 없었다.
		preview := res.Content
		if len(preview) > 200 {
			preview = preview[:200] + "…"
		}
		out := map[string]any{
			"ok":      true,
			"id":      res.ID,
			"score":   res.Score,
			"deleted": res.Deleted,
			"preview": preview,
		}
		if res.Skipped {
			out["skipped"] = true
			out["reason"] = fmt.Sprintf("nearest match scored %.3f, below min_score %.3f — nothing deleted", res.Score, opts.MinScore)
		}
		if opts.DryRun {
			out["dry_run"] = true
		}
		return jsonToolResult(bc, out)
	})

	mcpServer.AddTool(mcp.NewTool("consolidate",
		mcp.WithDescription(render("consolidate_description")),
		mcp.WithString("namespaces", mcp.Description(render("namespaces_param"))),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespaces, err := resolveNamespaces(ctx, request.GetString("namespaces", "/"))
		if err != nil {
			return nil, err
		}

		ids, err := bc.Brain.ResolveNamespaceIDs(ctx, namespaces)
		if err != nil {
			return nil, err
		}

		var summaries []map[string]any
		for _, id := range ids {
			result, err := bc.Brain.ConsolidateByID(ctx, id)
			if err != nil {
				summaries = append(summaries, map[string]any{"namespace_id": id, "error": err.Error()})
				continue
			}
			summaries = append(summaries, map[string]any{
				"namespace":                    result.Namespace,
				"episodes_read":                result.EpisodesRead,
				"facts_created":                result.FactsCreated,
				"facts_deduplicated":           result.FactsDeduplicated,
				"relationships_found":          result.RelationshipsFound,
				"causal_links_found":           result.CausalLinksFound,
				"patterns_found":               result.PatternsFound,
				"contradictions_found":         result.ContradictionsFound,
				"contradictions_auto_resolved": result.ContradictionsAutoResolved,
				"goals_annotated":              result.GoalsAnnotated,
				"goals_suggested_complete":     result.GoalsSuggestedComplete,
				"failure_repeats_detected":     result.FailureRepeatsDetected,
				"failure_patterns_found":       result.FailurePatternsFound,
				"hypotheses_auto_confirmed":    result.HypothesesAutoConfirmed,
				"hypotheses_auto_rejected":     result.HypothesesAutoRejected,
				"hypotheses_updated":           result.HypothesesUpdated,
				"facts_decayed":                result.FactsDecayed,
				"facts_expired":                result.FactsExpired,
				"llm_calls":                    result.LLMCalls,
				"duration":                     result.Duration.String(),
				"errors":                       result.Errors,
			})
		}
		return jsonToolResult(bc, summaries)
	})

	mcpServer.AddTool(mcp.NewTool("set_context",
		mcp.WithDescription(render("set_context_description")),
		mcp.WithString("focus", mcp.Description(render("set_context_focus")), mcp.Required()),
		mcp.WithString("namespace", mcp.Description(render("context_namespace_param")), mcp.Required()),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		focus := request.GetString("focus", "")
		nss, err := resolveNamespaces(ctx, request.GetString("namespace", ""))
		if err != nil {
			return nil, err
		}
		namespace := nss[0]
		if namespace == "" {
			return nil, fmt.Errorf("namespace is required for context tools")
		}
		ttl := time.Hour
		if bc.Config != nil && bc.Config.ContextTTL > 0 {
			ttl = bc.Config.ContextTTL
		}
		if err := bc.Brain.SetContext(ctx, namespace, focus, time.Now().UTC().Add(ttl)); err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent{Type: "text", Text: `{"ok": true}`}}}, nil
	})

	mcpServer.AddTool(mcp.NewTool("get_context",
		mcp.WithDescription(render("get_context_description")),
		mcp.WithString("namespace", mcp.Description(render("context_namespace_param")), mcp.Required()),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		nss, err := resolveNamespaces(ctx, request.GetString("namespace", ""))
		if err != nil {
			return nil, err
		}
		namespace := nss[0]
		if namespace == "" {
			return nil, fmt.Errorf("namespace is required for context tools")
		}
		c, err := bc.Brain.GetContext(ctx, namespace)
		if err != nil {
			return nil, err
		}
		if c == nil {
			return &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent{Type: "text", Text: `{"focus": ""}`}}}, nil
		}
		return jsonToolResult(bc, c)
	})

	mcpServer.AddTool(mcp.NewTool("clear_context",
		mcp.WithDescription(render("clear_context_description")),
		mcp.WithString("namespace", mcp.Description(render("context_namespace_param")), mcp.Required()),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		nss, err := resolveNamespaces(ctx, request.GetString("namespace", ""))
		if err != nil {
			return nil, err
		}
		namespace := nss[0]
		if namespace == "" {
			return nil, fmt.Errorf("namespace is required for context tools")
		}
		if err := bc.Brain.ClearContext(ctx, namespace); err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent{Type: "text", Text: `{"ok": true}`}}}, nil
	})

	mcpServer.AddTool(mcp.NewTool("list_namespaces",
		mcp.WithDescription(render("list_namespaces_description")),
		mcp.WithString("namespaces", mcp.Description(render("list_namespaces_namespaces"))),
		mcp.WithNumber("limit", mcp.Description(render("pagination_limit")), mcp.DefaultNumber(100)),
		mcp.WithNumber("offset", mcp.Description(render("pagination_offset")), mcp.DefaultNumber(0)),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		nsRaw := request.GetString("namespaces", "")
		var patterns []string
		if strings.TrimSpace(nsRaw) != "" || ctx.Value(keyMode) == "remote" {
			var err error
			patterns, err = resolveNamespaces(ctx, nsRaw)
			if err != nil {
				return nil, err
			}
		}

		page := brain.Pagination{
			Limit:  request.GetInt("limit", 100),
			Offset: request.GetInt("offset", 0),
		}

		namespaces, err := bc.Brain.ListNamespaces(ctx, patterns, page)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, logicalNamespaces(ctx, namespaces), page.Offset)
	})

	mcpServer.AddTool(mcp.NewTool("create_namespace",
		mcp.WithDescription(render("create_namespace_description")),
		mcp.WithString("slug", mcp.Description(render("create_namespace_slug")), mcp.Required()),
		mcp.WithString("name", mcp.Description(render("create_namespace_name")), mcp.Required()),
		mcp.WithString("description", mcp.Description(render("create_namespace_description_param"))),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		slug := request.GetString("slug", "")
		name := request.GetString("name", slug)
		description := request.GetString("description", "")
		slug, err := resolveSingleNamespace(ctx, slug)
		if err != nil {
			return nil, err
		}

		id, err := bc.Brain.CreateNamespace(ctx, slug, name, description)
		if err != nil {
			return nil, err
		}
		displaySlug, ok := logicalNamespaceSlug(ctx, slug)
		if !ok {
			return nil, fmt.Errorf("unauthorized: verified identity is required")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent{
			Type: "text",
			Text: fmt.Sprintf(`{"id": %d, "slug": "%s"}`, id, displaySlug),
		}}}, nil
	})

	mcpServer.AddTool(mcp.NewTool("delete_namespace",
		mcp.WithDescription(render("delete_namespace_description")),
		mcp.WithString("slug", mcp.Description(render("delete_namespace_slug")), mcp.Required()),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		raw := request.GetString("slug", "")
		if strings.Trim(strings.TrimSpace(raw), "/") == "" {
			return nil, brain.ErrCannotDeleteRootNamespace
		}
		slug, err := resolveSingleNamespace(ctx, raw)
		if err != nil {
			return nil, err
		}
		if err := bc.Brain.DeleteNamespace(ctx, slug); err != nil {
			return nil, err
		}
		displaySlug, ok := logicalNamespaceSlug(ctx, slug)
		if !ok {
			return nil, fmt.Errorf("unauthorized: verified identity is required")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent{
			Type: "text",
			Text: fmt.Sprintf("{\"ok\": true, \"slug\": \"%s\"}", displaySlug),
		}}}, nil
	})

	mcpServer.AddTool(mcp.NewTool("query_facts",
		mcp.WithDescription(render("query_facts_description")),
		mcp.WithString("namespaces", mcp.Description(render("namespaces_param"))),
		mcp.WithString("q", mcp.Description("내용, 개체, 속성, 값으로 사실을 찾습니다.")),
		mcp.WithNumber("limit", mcp.Description(render("pagination_limit")), mcp.DefaultNumber(100)),
		mcp.WithNumber("offset", mcp.Description(render("pagination_offset")), mcp.DefaultNumber(0)),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespaces, err := resolveNamespaces(ctx, request.GetString("namespaces", "/"))
		if err != nil {
			return nil, err
		}

		page := brain.Pagination{
			Limit:  request.GetInt("limit", 100),
			Offset: request.GetInt("offset", 0),
		}

		facts, err := bc.Brain.QueryFactsFiltered(ctx, namespaces, nil, nil, request.GetString("q", ""), page)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, facts, page.Offset)
	})

	mcpServer.AddTool(mcp.NewTool("query_relationships",
		mcp.WithDescription(render("query_relationships_description")),
		mcp.WithString("namespaces", mcp.Description(render("namespaces_param"))),
		mcp.WithNumber("limit", mcp.Description(render("pagination_limit")), mcp.DefaultNumber(100)),
		mcp.WithNumber("offset", mcp.Description(render("pagination_offset")), mcp.DefaultNumber(0)),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespaces, err := resolveNamespaces(ctx, request.GetString("namespaces", "/"))
		if err != nil {
			return nil, err
		}

		page := brain.Pagination{
			Limit:  request.GetInt("limit", 100),
			Offset: request.GetInt("offset", 0),
		}

		rels, err := bc.Brain.QueryRelationships(ctx, namespaces, page)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, rels, page.Offset)
	})

	// patterns 는 consolidation 6단계가 채우는데 조회 도구가 없어 에이전트가 꺼내 볼 수 없었다.
	// query_relationships 와 대칭으로 노출한다.
	mcpServer.AddTool(mcp.NewTool("query_patterns",
		mcp.WithDescription(render("query_patterns_description")),
		mcp.WithString("namespaces", mcp.Description(render("namespaces_param"))),
		mcp.WithNumber("limit", mcp.Description(render("pagination_limit")), mcp.DefaultNumber(100)),
		mcp.WithNumber("offset", mcp.Description(render("pagination_offset")), mcp.DefaultNumber(0)),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespaces, err := resolveNamespaces(ctx, request.GetString("namespaces", "/"))
		if err != nil {
			return nil, err
		}

		page := brain.Pagination{
			Limit:  request.GetInt("limit", 100),
			Offset: request.GetInt("offset", 0),
		}

		pats, err := bc.Brain.QueryPatterns(ctx, namespaces, page)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, pats, page.Offset)
	})

	mcpServer.AddTool(mcp.NewTool("list_contradictions",
		mcp.WithDescription(render("list_contradictions_description")),
		mcp.WithString("namespaces", mcp.Description(render("namespaces_param"))),
		mcp.WithNumber("limit", mcp.Description(render("pagination_limit")), mcp.DefaultNumber(100)),
		mcp.WithNumber("offset", mcp.Description(render("pagination_offset")), mcp.DefaultNumber(0)),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespaces, err := resolveNamespaces(ctx, request.GetString("namespaces", "/"))
		if err != nil {
			return nil, err
		}

		page := brain.Pagination{
			Limit:  request.GetInt("limit", 100),
			Offset: request.GetInt("offset", 0),
		}

		contradictions, err := bc.Brain.ListContradictions(ctx, namespaces, page)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, contradictions, page.Offset)
	})

	mcpServer.AddTool(mcp.NewTool("resolve_contradiction",
		mcp.WithDescription(render("resolve_contradiction_description")),
		mcp.WithNumber("id", mcp.Description(render("resolve_contradiction_id")), mcp.Required()),
		mcp.WithString("resolution", mcp.Description(render("resolve_contradiction_resolution"))),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := request.GetInt("id", 0)
		resolution := request.GetString("resolution", "resolved")
		contradiction, err := bc.Brain.GetContradiction(ctx, int64(id))
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, contradiction.NamespaceID); err != nil {
			return nil, err
		}

		if err := bc.Brain.ResolveContradiction(ctx, int64(id), resolution); err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent{Type: "text", Text: `{"ok": true}`}}}, nil
	})

	mcpServer.AddTool(mcp.NewTool("list_causal_links",
		mcp.WithDescription(render("list_causal_links_description")),
		mcp.WithString("namespaces", mcp.Description(render("namespaces_param"))),
		mcp.WithNumber("limit", mcp.Description(render("pagination_limit")), mcp.DefaultNumber(100)),
		mcp.WithNumber("offset", mcp.Description(render("pagination_offset")), mcp.DefaultNumber(0)),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespaces, err := resolveNamespaces(ctx, request.GetString("namespaces", "/"))
		if err != nil {
			return nil, err
		}

		page := brain.Pagination{
			Limit:  request.GetInt("limit", 100),
			Offset: request.GetInt("offset", 0),
		}

		links, err := bc.Brain.ListCausalLinks(ctx, namespaces, page)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, links, page.Offset)
	})

	mcpServer.AddTool(mcp.NewTool("create_causal_link",
		mcp.WithDescription(render("create_causal_link_description")),
		mcp.WithNumber("cause_id", mcp.Description(render("create_causal_link_cause_id")), mcp.Required()),
		mcp.WithNumber("effect_id", mcp.Description(render("create_causal_link_effect_id")), mcp.Required()),
		mcp.WithNumber("confidence", mcp.Description(render("create_causal_link_confidence")), mcp.DefaultNumber(0.8)),
		mcp.WithString("namespace", mcp.Description(render("namespace_param"))),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		causeID := request.GetInt("cause_id", 0)
		effectID := request.GetInt("effect_id", 0)
		confidence := float32(request.GetFloat("confidence", 0.8))
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}
		cause, err := bc.Brain.GetFact(ctx, int64(causeID))
		if err != nil {
			return nil, err
		}
		effect, err := bc.Brain.GetFact(ctx, int64(effectID))
		if err != nil {
			return nil, err
		}
		if err := authorizeRelatedNamespace(ctx, bc, namespaceID, cause.NamespaceID); err != nil {
			return nil, err
		}
		if err := authorizeRelatedNamespace(ctx, bc, namespaceID, effect.NamespaceID); err != nil {
			return nil, err
		}
		if cause.NamespaceID != effect.NamespaceID {
			return nil, fmt.Errorf("cause and effect facts must share a namespace")
		}

		link, err := bc.Brain.CreateCausalLink(ctx, namespaceID, int64(causeID), int64(effectID), confidence)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, link)
	})

	mcpServer.AddTool(mcp.NewTool("trace_causal_chain",
		mcp.WithDescription(render("trace_causal_chain_description")),
		mcp.WithNumber("fact_id", mcp.Description(render("trace_causal_chain_fact_id")), mcp.Required()),
		mcp.WithString("direction", mcp.Description(render("trace_causal_chain_direction")), mcp.DefaultString("forward")),
		mcp.WithNumber("max_depth", mcp.Description(render("trace_causal_chain_max_depth")), mcp.DefaultNumber(10)),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		factID := request.GetInt("fact_id", 0)
		direction := request.GetString("direction", "forward")
		maxDepth := request.GetInt("max_depth", 10)
		fact, err := bc.Brain.GetFact(ctx, int64(factID))
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, fact.NamespaceID); err != nil {
			return nil, err
		}

		var chain []models.CausalLink
		if mode, _ := ctx.Value(keyMode).(string); mode == "remote" {
			var namespaceIDs []int64
			namespaceIDs, err = authenticatedNamespaceIDs(ctx, bc)
			if err != nil {
				return nil, err
			}
			chain, err = bc.Brain.TraceCausalChainInNamespaces(ctx, int64(factID), direction, maxDepth, namespaceIDs)
		} else {
			chain, err = bc.Brain.TraceCausalChain(ctx, int64(factID), direction, maxDepth)
		}
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, chain)
	})

	mcpServer.AddTool(mcp.NewTool("list_hypotheses",
		mcp.WithDescription(render("list_hypotheses_description")),
		mcp.WithString("namespaces", mcp.Description(render("namespaces_param"))),
		mcp.WithString("status", mcp.Description(render("list_hypotheses_status"))),
		mcp.WithString("q", mcp.Description("내용, 검증 방법, 상태, 기각 사유로 가설을 찾습니다.")),
		mcp.WithNumber("limit", mcp.Description(render("pagination_limit")), mcp.DefaultNumber(100)),
		mcp.WithNumber("offset", mcp.Description(render("pagination_offset")), mcp.DefaultNumber(0)),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespaces, err := resolveNamespaces(ctx, request.GetString("namespaces", "/"))
		if err != nil {
			return nil, err
		}

		page := brain.Pagination{
			Limit:  request.GetInt("limit", 100),
			Offset: request.GetInt("offset", 0),
		}
		status := request.GetString("status", "")

		hypotheses, err := bc.Brain.ListHypothesesFiltered(ctx, namespaces, status, request.GetString("q", ""), page)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, hypotheses, page.Offset)
	})

	mcpServer.AddTool(mcp.NewTool("create_hypothesis",
		mcp.WithDescription(render("create_hypothesis_description")),
		mcp.WithString("content", mcp.Description(render("create_hypothesis_content")), mcp.Required()),
		mcp.WithString("verification_plan", mcp.Description(render("create_hypothesis_verification_plan")), mcp.Required()),
		mcp.WithNumber("confidence", mcp.Description(render("create_hypothesis_confidence")), mcp.DefaultNumber(0.5)),
		mcp.WithString("namespace", mcp.Description(render("namespace_param"))),
		mcp.WithString("source_fact_ids", mcp.Description(render("create_hypothesis_source_fact_ids"))),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content := request.GetString("content", "")
		verificationPlan := request.GetString("verification_plan", "")
		confidence := float32(request.GetFloat("confidence", 0.5))
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}

		var sourceFactIDs []int64
		if raw := request.GetString("source_fact_ids", ""); raw != "" {
			for _, s := range strings.Split(raw, ",") {
				id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
				if err != nil {
					return nil, fmt.Errorf("invalid source fact ID %q: %w", s, err)
				}
				fact, err := bc.Brain.GetFact(ctx, id)
				if err != nil {
					return nil, err
				}
				if err := authorizeRelatedNamespace(ctx, bc, namespaceID, fact.NamespaceID); err != nil {
					return nil, err
				}
				if fact.NamespaceID != namespaceID {
					return nil, fmt.Errorf("source fact %d must share the target namespace", id)
				}
				sourceFactIDs = append(sourceFactIDs, id)
			}
		}

		h, err := bc.Brain.CreateHypothesis(ctx, namespaceID, content, verificationPlan, confidence, sourceFactIDs)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, h)
	})

	mcpServer.AddTool(mcp.NewTool("confirm_hypothesis",
		mcp.WithDescription(render("confirm_hypothesis_description")),
		mcp.WithNumber("id", mcp.Description(render("confirm_hypothesis_id")), mcp.Required()),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := request.GetInt("id", 0)
		hypothesis, err := bc.Brain.GetHypothesis(ctx, int64(id))
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, hypothesis.NamespaceID); err != nil {
			return nil, err
		}

		h, f, err := bc.Brain.ConfirmHypothesis(ctx, int64(id))
		if err != nil {
			return nil, err
		}
		result := map[string]any{
			"hypothesis": h,
			"fact":       f,
		}
		return jsonToolResult(bc, result)
	})

	mcpServer.AddTool(mcp.NewTool("reject_hypothesis",
		mcp.WithDescription(render("reject_hypothesis_description")),
		mcp.WithNumber("id", mcp.Description(render("reject_hypothesis_id")), mcp.Required()),
		mcp.WithString("reason", mcp.Description(render("reject_hypothesis_reason"))),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := request.GetInt("id", 0)
		reason := request.GetString("reason", "")
		hypothesis, err := bc.Brain.GetHypothesis(ctx, int64(id))
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, hypothesis.NamespaceID); err != nil {
			return nil, err
		}

		h, err := bc.Brain.RejectHypothesis(ctx, int64(id), reason)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, h)
	})

	mcpServer.AddTool(mcp.NewTool("list_goals",
		mcp.WithDescription(render("list_goals_description")),
		mcp.WithString("namespaces", mcp.Description(render("namespaces_param"))),
		mcp.WithString("status", mcp.Description(render("list_goals_status"))),
		mcp.WithString("q", mcp.Description("내용, 메모, 상태로 목표를 찾습니다.")),
		mcp.WithNumber("parent_id", mcp.Description(render("list_goals_parent_id"))),
		mcp.WithNumber("limit", mcp.Description(render("pagination_limit")), mcp.DefaultNumber(100)),
		mcp.WithNumber("offset", mcp.Description(render("pagination_offset")), mcp.DefaultNumber(0)),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespaces, err := resolveNamespaces(ctx, request.GetString("namespaces", "/"))
		if err != nil {
			return nil, err
		}

		page := brain.Pagination{
			Limit:  request.GetInt("limit", 100),
			Offset: request.GetInt("offset", 0),
		}
		status := request.GetString("status", "")

		var parentID *int64
		if pid := request.GetInt("parent_id", 0); pid != 0 {
			pid64 := int64(pid)
			parent, err := bc.Brain.GetGoal(ctx, pid64)
			if err != nil {
				return nil, err
			}
			if err := authorizeNamespaceID(ctx, bc, parent.NamespaceID); err != nil {
				return nil, err
			}
			parentID = &pid64
		}

		goals, err := bc.Brain.ListGoalsFiltered(ctx, namespaces, status, parentID, request.GetString("q", ""), page)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, goals, page.Offset)
	})

	mcpServer.AddTool(mcp.NewTool("create_goal",
		mcp.WithDescription(render("create_goal_description")),
		mcp.WithString("content", mcp.Description(render("create_goal_content")), mcp.Required()),
		mcp.WithNumber("parent_id", mcp.Description(render("create_goal_parent_id"))),
		mcp.WithNumber("priority", mcp.Description(render("create_goal_priority")), mcp.DefaultNumber(0)),
		mcp.WithString("namespace", mcp.Description(render("namespace_param"))),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content := request.GetString("content", "")
		priority := request.GetInt("priority", 0)
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}

		var parentID *int64
		if pid := request.GetInt("parent_id", 0); pid != 0 {
			pid64 := int64(pid)
			parent, err := bc.Brain.GetGoal(ctx, pid64)
			if err != nil {
				return nil, err
			}
			if err := authorizeRelatedNamespace(ctx, bc, namespaceID, parent.NamespaceID); err != nil {
				return nil, err
			}
			if parent.NamespaceID != namespaceID {
				return nil, fmt.Errorf("parent goal must share the target namespace")
			}
			parentID = &pid64
		}

		g, err := bc.Brain.CreateGoal(ctx, namespaceID, content, parentID, priority)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, g)
	})

	mcpServer.AddTool(mcp.NewTool("complete_goal",
		mcp.WithDescription(render("complete_goal_description")),
		mcp.WithNumber("id", mcp.Description(render("complete_goal_id")), mcp.Required()),
		mcp.WithString("notes", mcp.Description(render("complete_goal_notes"))),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := request.GetInt("id", 0)
		notes := request.GetString("notes", "")
		goal, err := bc.Brain.GetGoal(ctx, int64(id))
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, goal.NamespaceID); err != nil {
			return nil, err
		}

		g, err := bc.Brain.CompleteGoal(ctx, int64(id), notes)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, g)
	})

	mcpServer.AddTool(mcp.NewTool("abandon_goal",
		mcp.WithDescription(render("abandon_goal_description")),
		mcp.WithNumber("id", mcp.Description(render("abandon_goal_id")), mcp.Required()),
		mcp.WithString("notes", mcp.Description(render("abandon_goal_notes"))),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := request.GetInt("id", 0)
		notes := request.GetString("notes", "")
		goal, err := bc.Brain.GetGoal(ctx, int64(id))
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, goal.NamespaceID); err != nil {
			return nil, err
		}

		g, err := bc.Brain.AbandonGoal(ctx, int64(id), notes)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, g)
	})

	mcpServer.AddTool(mcp.NewTool("list_failures",
		mcp.WithDescription(render("list_failures_description")),
		mcp.WithString("namespaces", mcp.Description(render("namespaces_param"))),
		mcp.WithNumber("goal_id", mcp.Description(render("list_failures_goal_id"))),
		mcp.WithNumber("limit", mcp.Description(render("pagination_limit")), mcp.DefaultNumber(100)),
		mcp.WithNumber("offset", mcp.Description(render("pagination_offset")), mcp.DefaultNumber(0)),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespaces, err := resolveNamespaces(ctx, request.GetString("namespaces", "/"))
		if err != nil {
			return nil, err
		}

		page := brain.Pagination{
			Limit:  request.GetInt("limit", 100),
			Offset: request.GetInt("offset", 0),
		}

		var goalID *int64
		if gid := request.GetInt("goal_id", 0); gid != 0 {
			gid64 := int64(gid)
			goal, err := bc.Brain.GetGoal(ctx, gid64)
			if err != nil {
				return nil, err
			}
			if err := authorizeNamespaceID(ctx, bc, goal.NamespaceID); err != nil {
				return nil, err
			}
			goalID = &gid64
		}

		failures, err := bc.Brain.ListFailures(ctx, namespaces, goalID, page)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, failures, page.Offset)
	})

	mcpServer.AddTool(mcp.NewTool("create_failure",
		mcp.WithDescription(render("create_failure_description")),
		mcp.WithString("content", mcp.Description(render("create_failure_content")), mcp.Required()),
		mcp.WithString("reason", mcp.Description(render("create_failure_reason")), mcp.Required()),
		mcp.WithString("lesson", mcp.Description(render("create_failure_lesson")), mcp.Required()),
		mcp.WithNumber("goal_id", mcp.Description(render("create_failure_goal_id"))),
		mcp.WithString("namespace", mcp.Description(render("namespace_param"))),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content := request.GetString("content", "")
		reason := request.GetString("reason", "")
		lesson := request.GetString("lesson", "")
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}

		var goalID *int64
		if gid := request.GetInt("goal_id", 0); gid != 0 {
			gid64 := int64(gid)
			goal, err := bc.Brain.GetGoal(ctx, gid64)
			if err != nil {
				return nil, err
			}
			if err := authorizeRelatedNamespace(ctx, bc, namespaceID, goal.NamespaceID); err != nil {
				return nil, err
			}
			if goal.NamespaceID != namespaceID {
				return nil, fmt.Errorf("goal must share the target namespace")
			}
			goalID = &gid64
		}

		f, err := bc.Brain.CreateFailure(ctx, namespaceID, content, reason, lesson, goalID)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, f)
	})

	mcpServer.AddTool(mcp.NewTool("delete_failure",
		mcp.WithDescription(render("delete_failure_description")),
		mcp.WithNumber("id", mcp.Description(render("delete_failure_id")), mcp.Required()),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := request.GetInt("id", 0)
		failure, err := bc.Brain.GetFailure(ctx, int64(id))
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, failure.NamespaceID); err != nil {
			return nil, err
		}

		if err := bc.Brain.DeleteFailure(ctx, int64(id)); err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent{Type: "text", Text: `{"ok": true}`}}}, nil
	})

	registerWorkGraphTools(mcpServer, bc)
	registerGoalMapTools(mcpServer, bc)
	registerWorkPlanTools(mcpServer, bc)
	registerWorkExecutionTools(mcpServer, bc)
	registerProjectCoordinationTools(mcpServer, bc)
	registerWorkspaceTools(mcpServer, bc)
	registerStashSkills(mcpServer)
	return mcpServer
}

func mcpServeCmd(ctx context.Context, cmd *cli.Command) error {
	bc := getBootstrap(cmd)
	options := mcpHTTPOptions{Addr: configuredListenAddress(bc, cmd)}
	if cmd.Bool("with-consolidation") {
		consolidation := consolidationOptionsFromCommand(cmd)
		options.Consolidation = &consolidation
	}
	return serveMCPHTTP(ctx, bc, options)
}

type mcpHTTPOptions struct {
	Addr          string
	Consolidation *consolidationOptions
}

type consolidationOptions struct {
	Interval   time.Duration
	Namespaces []string
}

func consolidationOptionsFromCommand(cmd *cli.Command) consolidationOptions {
	return consolidationOptions{
		Interval:   cmd.Duration("consolidate-interval"),
		Namespaces: cmd.StringSlice("consolidate-namespaces"),
	}
}

func configuredListenAddress(bc *bootstrap.Context, cmd *cli.Command) string {
	host := cmd.String("host")
	port := cmd.String("port")
	if bc != nil && bc.Config != nil {
		configuredHost, configuredPort := configuredHTTPAddress(bc.Config.HTTPAddr, host, port)
		if !cmd.IsSet("host") {
			host = configuredHost
		}
		if !cmd.IsSet("port") {
			port = configuredPort
		}
	}
	return net.JoinHostPort(host, port)
}

func serveMCPHTTP(ctx context.Context, bc *bootstrap.Context, options mcpHTTPOptions) error {
	if bc == nil {
		return fmt.Errorf("server is not initialized")
	}
	if bc.Auth != nil && bc.Auth.Mode() == "stdio" {
		return fmt.Errorf("STASH_AUTH_MODE=stdio can only be used with `mcp execute`")
	}

	httpServer := &http.Server{
		Addr:    options.Addr,
		Handler: newStashHTTPHandler(bc),
		// SSE 는 응답이 끝나지 않는 스트림이므로 쓰기 타임아웃을 걸지 않는다.
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		runEmbeddingRetryTicker(ctx, bc)
	}()
	go func() {
		defer wg.Done()
		runWorkspaceLifecycleTicker(ctx, bc)
	}()

	if options.Consolidation != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runConsolidationTicker(ctx, bc, *options.Consolidation)
		}()
	}

	fmt.Printf("Starting Stash server on %s (MCP: /mcp, SSE: /sse, docs: /docs, metrics: /metrics)\n", options.Addr)

	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		fmt.Println("\nStash server shutting down")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = httpServer.Shutdown(shutdownCtx)
		wg.Wait()
		return nil
	case err := <-errCh:
		cancel()
		wg.Wait()
		return err
	}
}

// newStashHTTPHandler keeps every HTTP surface on one listener. A nil Auth
// provider is the intentional representation of STASH_AUTH_MODE=none.
func newStashHTTPHandler(bc *bootstrap.Context) http.Handler {
	mcpServer := newMCPServer(bc)

	// 전송 방식 두 가지를 같은 포트에서 각자의 경로로 제공한다.
	//
	// 이전 구현은 Streamable HTTP 서버를 "/sse" 경로에 걸어 두었다. 경로 이름만 SSE 였고
	// 실제 전송은 Streamable HTTP 라서, SSE 로 접속한 클라이언트는 200 응답만 받고
	// 초기 `event: endpoint` 를 영원히 기다리다 타임아웃됐다(Claude Code 30s).
	// 이름과 동작을 1:1 로 맞춘다.
	//
	//   POST/GET/DELETE /mcp       → Streamable HTTP (권장 기본값)
	//   GET             /sse       → SSE 스트림
	//   POST            /message   → SSE 세션의 클라이언트 → 서버 메시지 채널
	sessionResolver := server.NewDefaultSessionIdManagerResolver(&server.StatelessGeneratingSessionIdManager{})
	streamableServer := server.NewStreamableHTTPServer(mcpServer,
		server.WithHTTPContextFunc(httpContextFunc),
		server.WithSessionIdManagerResolver(sessionResolver),
	)
	sseServer := server.NewSSEServer(mcpServer, server.WithSSEContextFunc(httpContextFunc))

	mux := http.NewServeMux()
	mux.Handle("/mcp", authenticatedHTTP(bc.Auth, newStashSkillsHTTPTransport(streamableServer, sessionResolver)))
	mux.Handle("/sse", authenticatedHTTP(bc.Auth, sseServer.SSEHandler()))
	mux.Handle("/message", authenticatedHTTP(bc.Auth, sseServer.MessageHandler()))
	mux.HandleFunc("/.well-known/oauth-protected-resource", bc.Auth.HandleProtectedResourceMetadata)
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", bc.Auth.HandleProtectedResourceMetadata)
	mux.HandleFunc("/.well-known/oauth-protected-resource/sse", bc.Auth.HandleProtectedResourceMetadata)
	mux.HandleFunc("/.well-known/oauth-protected-resource/message", bc.Auth.HandleProtectedResourceMetadata)
	mux.HandleFunc("/.well-known/oauth-authorization-server", bc.Auth.HandleAuthorizationServerMetadata)
	mux.HandleFunc("/.well-known/openid-configuration", bc.Auth.HandleAuthorizationServerMetadata)
	mux.HandleFunc("/authorize", bc.Auth.HandleAuthorize)
	mux.HandleFunc("/oauth/token", bc.Auth.HandleOAuthToken)
	mux.HandleFunc("/oauth/register", bc.Auth.HandleOAuthRegister)
	mux.HandleFunc("/auth/login", bc.Auth.HandleLogin)
	mux.HandleFunc("/auth/callback", bc.Auth.HandleCallback)
	mux.HandleFunc("/oauth/callback", bc.Auth.HandleCallback)
	mux.HandleFunc("/auth/logout", bc.Auth.HandleLogout)
	mux.HandleFunc("/auth/status", bc.Auth.HandleStatus)
	mux.HandleFunc("/auth/token", bc.Auth.HandleGenerateToken)
	registerDocumentationRoutes(mux)
	registerOperationalRoutes(mux, bc)
	registerAdminRoutes(mux, bc)
	mux.Handle("/", web.GetUIHandler())

	return observability.InstrumentHTTP(bc.Logger, mux)
}

func mcpExecuteCmd(ctx context.Context, cmd *cli.Command) error {
	bc := getBootstrap(cmd)
	mcpServer := newMCPServer(bc)

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		runEmbeddingRetryTicker(workerCtx, bc)
	}()
	go func() {
		defer wg.Done()
		runWorkspaceLifecycleTicker(workerCtx, bc)
	}()

	if cmd.Bool("with-consolidation") {
		consolidation := consolidationOptionsFromCommand(cmd)
		wg.Add(1)
		go func() {
			defer wg.Done()
			runConsolidationTicker(workerCtx, bc, consolidation)
		}()
	}

	err := server.ServeStdio(mcpServer, server.WithStdioContextFunc(stdioContextFuncFor(bc.Auth)))
	cancel()
	wg.Wait()
	return err
}

func mcpTokenCmd(_ context.Context, cmd *cli.Command) error {
	secret := strings.TrimSpace(os.Getenv("STASH_AUTH_API_SECRET"))
	if secret == "" {
		secret = strings.TrimSpace(os.Getenv("STASH_AUTH_OAUTH_API_SECRET"))
	}
	if secret == "" {
		return fmt.Errorf("STASH_AUTH_API_SECRET must be set")
	}
	subject := strings.TrimSpace(cmd.String("subject"))
	if subject == "" {
		subject = strings.TrimSpace(os.Getenv("STASH_AGENT_ID"))
	}
	if subject == "" {
		return fmt.Errorf("--subject or STASH_AGENT_ID must be set")
	}
	ttl := cmd.Duration("ttl")
	if !cmd.IsSet("ttl") {
		rawTTL := strings.TrimSpace(os.Getenv("STASH_AUTH_TOKEN_TTL"))
		if rawTTL == "" {
			rawTTL = strings.TrimSpace(os.Getenv("STASH_AUTH_OAUTH_TOKEN_TTL"))
		}
		if rawTTL != "" {
			parsed, err := time.ParseDuration(rawTTL)
			if err != nil {
				return fmt.Errorf("STASH_AUTH_TOKEN_TTL is invalid: %w", err)
			}
			ttl = parsed
		}
	}
	token, err := auth.GenerateAPIToken(subject, secret, ttl)
	if err != nil {
		return fmt.Errorf("generate MCP token: %w", err)
	}
	// This is an explicit credential-generation command; do not log it from
	// the server or include it in any durable work record.
	fmt.Println(token)
	return nil
}

func runEmbeddingRetryTicker(ctx context.Context, bc *bootstrap.Context) {
	if bc == nil || bc.Config == nil || bc.Brain == nil {
		return
	}
	interval := bc.Config.EmbeddingRetryInterval
	batchSize := bc.Config.EmbeddingRetryBatchSize

	run := func() {
		result, err := bc.Brain.RetryPendingEmbeddings(ctx, batchSize)
		if err != nil {
			if ctx.Err() == nil && bc.Logger != nil {
				bc.Logger.Error("embedding retry pass failed", "error", err)
			}
			return
		}
		recordEmbeddingRetryMetrics(result)
		if result.Attempted == 0 || bc.Logger == nil {
			return
		}
		if result.Failed > 0 {
			bc.Logger.Warn("embedding retry pass completed",
				"attempted", result.Attempted,
				"indexed", result.Indexed,
				"failed", result.Failed,
				"pending", result.Pending,
				"error", result.Error,
			)
			return
		}
		bc.Logger.Info("embedding retry pass completed",
			"attempted", result.Attempted,
			"indexed", result.Indexed,
			"failed", result.Failed,
			"pending", result.Pending,
		)
	}

	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	wake := bc.Brain.EmbeddingRetryWake()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		case <-wake:
			run()
		}
	}
}

func runWorkspaceLifecycleTicker(ctx context.Context, bc *bootstrap.Context) {
	if bc == nil || bc.Brain == nil {
		return
	}
	const (
		interval    = 5 * time.Minute
		staleAfter  = 24 * time.Hour
		removeAfter = 7 * 24 * time.Hour
	)
	run := func() {
		result, err := bc.Brain.MaintainWorkspaceLifecycle(ctx, staleAfter, removeAfter)
		if err != nil {
			if ctx.Err() == nil && bc.Logger != nil {
				bc.Logger.Error("workspace lifecycle maintenance failed", "error", err)
			}
			return
		}
		if bc.Logger != nil && (result.Stale > 0 || result.Removed > 0) {
			bc.Logger.Info("workspace lifecycle maintenance completed", "stale", result.Stale, "removed", result.Removed)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func runConsolidationTicker(ctx context.Context, bc *bootstrap.Context, options consolidationOptions) {
	interval := options.Interval
	namespaces := options.Namespaces

	if len(namespaces) == 0 {
		namespaces = []string{"/"}
	}

	log.Printf("Starting background consolidation with interval %s", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			seen := make(map[int64]struct{})
			for _, namespace := range namespaces {
				ids, err := bc.Brain.ResolveNamespaceIDs(ctx, []string{namespace})
				if err != nil {
					log.Printf("Consolidation: skipping namespace %s: %v", namespace, err)
					continue
				}
				for _, id := range ids {
					if _, ok := seen[id]; ok {
						continue
					}
					seen[id] = struct{}{}
					result, err := bc.Brain.ConsolidateByID(ctx, id)
					if err != nil {
						log.Printf("Consolidation failed for namespace ID %d: %v", id, err)
						continue
					}
					log.Printf("Consolidation completed for %s: facts=%d relationships=%d goals_annotated=%d failure_repeats=%d hypotheses_updated=%d",
						result.Namespace, result.FactsCreated, result.RelationshipsFound, result.GoalsAnnotated, result.FailureRepeatsDetected, result.HypothesesUpdated)
				}
			}
		case <-ctx.Done():
			log.Printf("Background consolidation shutting down")
			return
		}
	}
}
