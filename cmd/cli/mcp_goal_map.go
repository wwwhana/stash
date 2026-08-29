package main

import (
	"context"
	"strings"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerGoalMapTools(mcpServer *server.MCPServer, bc *bootstrap.Context) {
	mcpServer.AddTool(mcp.NewTool("set_project_goal",
		mcp.WithDescription("Select one active top-level goal as the shared project outcome. Its child goals form the outcome tree seen by every agent."),
		mcp.WithNumber("goal_id", mcp.Required()),
		mcp.WithString("namespace", mcp.Description("Exact project namespace path")),
		mcp.WithString("set_by", mcp.Description("Person or agent selecting the shared goal")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}
		goalID, err := requiredPositiveID(request, "goal_id")
		if err != nil {
			return nil, err
		}
		goal, err := bc.Brain.GetGoal(ctx, goalID)
		if err != nil {
			return nil, err
		}
		if err := authorizeRelatedNamespace(ctx, bc, namespaceID, goal.NamespaceID); err != nil {
			return nil, err
		}
		setBy := strings.TrimSpace(request.GetString("set_by", ""))
		if user, ok := ctx.Value(keySSOUser).(string); ok && user != "" {
			setBy = user
		}
		tree, err := bc.Brain.SetProjectGoalRoot(ctx, namespaceID, goalID, setBy)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, tree)
	})

	mcpServer.AddTool(mcp.NewTool("get_goal_map",
		mcp.WithDescription("Owner-facing overview of the shared goal hierarchy, contributing work, linked memory, typed graph edges, and work that still lacks a goal. Worker agents should use resume_work or resume_workspace for a compact goal path."),
		mcp.WithString("namespace", mcp.Description("Exact project namespace path")),
		mcp.WithBoolean("include_done", mcp.Description("Include completed and canceled work cards"), mcp.DefaultBool(true)),
		mcp.WithReadOnlyHintAnnotation(true),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}
		goalMap, err := bc.Brain.GetGoalMap(ctx, namespaceID, request.GetBool("include_done", true))
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, goalMap)
	})

	mcpServer.AddTool(mcp.NewTool("link_goal_memory",
		mcp.WithDescription("Link durable memory to a goal so its context, constraint, decision, evidence, failure, or result appears in the shared goal map."),
		mcp.WithNumber("goal_id", mcp.Required()),
		mcp.WithString("memory_type", mcp.Description("episode, fact, hypothesis, or failure"), mcp.Required()),
		mcp.WithNumber("memory_id", mcp.Required()),
		mcp.WithString("relation", mcp.Description("context, constraint, decision, evidence, failure, result, or supersedes"), mcp.DefaultString("context")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		goalID, err := requiredPositiveID(request, "goal_id")
		if err != nil {
			return nil, err
		}
		memoryID, err := requiredPositiveID(request, "memory_id")
		if err != nil {
			return nil, err
		}
		goal, err := bc.Brain.GetGoal(ctx, goalID)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, goal.NamespaceID); err != nil {
			return nil, err
		}
		link, err := bc.Brain.LinkGoalMemory(ctx, goalID, request.GetString("memory_type", ""), memoryID, request.GetString("relation", "context"))
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, link)
	})

	mcpServer.AddTool(mcp.NewTool("list_goal_memory_links",
		mcp.WithDescription("List durable memory references attached directly to one goal."),
		mcp.WithNumber("goal_id", mcp.Required()),
		mcp.WithReadOnlyHintAnnotation(true),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		goalID, err := requiredPositiveID(request, "goal_id")
		if err != nil {
			return nil, err
		}
		goal, err := bc.Brain.GetGoal(ctx, goalID)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, goal.NamespaceID); err != nil {
			return nil, err
		}
		links, err := bc.Brain.ListGoalMemoryLinks(ctx, goalID)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, links)
	})
}
