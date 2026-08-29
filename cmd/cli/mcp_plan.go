package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/brain"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func workPlanPaths(request mcp.CallToolRequest, key string) ([]string, error) {
	raw, ok := request.GetArguments()[key]
	if !ok || raw == nil {
		return nil, nil
	}
	switch value := raw.(type) {
	case string:
		return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '\n' }), nil
	case []string:
		return value, nil
	case []any:
		paths := make([]string, 0, len(value))
		for _, entry := range value {
			path, ok := entry.(string)
			if !ok {
				return nil, fmt.Errorf("argument %q must contain only strings", key)
			}
			paths = append(paths, path)
		}
		return paths, nil
	default:
		return nil, fmt.Errorf("argument %q must be a comma-separated string or string array", key)
	}
}

func workPlanActor(ctx context.Context, request mcp.CallToolRequest) string {
	if user, ok := ctx.Value(keySSOUser).(string); ok && user != "" {
		return user
	}
	return request.GetString("agent", "")
}

func workPlanOptionalString(request mcp.CallToolRequest, key string) (*string, error) {
	raw, ok := request.GetArguments()[key]
	if !ok || raw == nil {
		return nil, nil
	}
	value, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("argument %q must be a string", key)
	}
	return &value, nil
}

func workPlanOptionalPaths(request mcp.CallToolRequest, key string) (*[]string, error) {
	if raw, ok := request.GetArguments()[key]; !ok || raw == nil {
		return nil, nil
	}
	paths, err := workPlanPaths(request, key)
	if err != nil {
		return nil, err
	}
	return &paths, nil
}

func registerWorkPlanTools(mcpServer *server.MCPServer, bc *bootstrap.Context) {
	mcpServer.AddTool(mcp.NewTool("get_work_plan",
		mcp.WithDescription("Return the owner-facing component map, nested executable tasks, component dependencies, owned scopes, decisions, and convention warnings for one namespace."),
		mcp.WithString("namespace", mcp.Description("Exact project namespace path")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}
		plan, err := bc.Brain.GetWorkPlan(ctx, namespaceID)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, plan)
	})

	mcpServer.AddTool(mcp.NewTool("validate_work_plan",
		mcp.WithDescription("Run an explicit semantic review of the living work plan with the configured reasoning model, persist the result, and return concrete findings. This does not use the embedding model."),
		mcp.WithString("namespace", mcp.Description("Exact project namespace path")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}
		validation, err := bc.Brain.ValidateWorkPlan(ctx, namespaceID)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, validation)
	})

	mcpServer.AddTool(mcp.NewTool("create_plan_component",
		mcp.WithDescription("Create a stable, owner-facing system component in the living work plan. Put executable work in child tasks."),
		mcp.WithString("title", mcp.Required(), mcp.Description("Verb-led, plain-language component outcome")),
		mcp.WithString("description", mcp.Description("Owner-facing scope and done condition")),
		mcp.WithString("technical_details", mcp.Description("Optional implementation detail for the technical line")),
		mcp.WithString("owned_paths", mcp.Description("Comma-separated file paths, connector resource patterns, or other work scopes owned by this component")),
		mcp.WithNumber("goal_id", mcp.Description("Goal this component contributes to; defaults to the shared project goal")),
		mcp.WithString("labels", mcp.Description("Comma-separated labels")),
		mcp.WithString("reporter"), mcp.WithString("owner"), mcp.WithString("status", mcp.DefaultString("ready")),
		mcp.WithNumber("priority", mcp.DefaultNumber(0)), mcp.WithNumber("position", mcp.DefaultNumber(0)),
		mcp.WithString("namespace", mcp.Description("Exact project namespace path")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}
		paths, err := workPlanPaths(request, "owned_paths")
		if err != nil {
			return nil, err
		}
		labels, err := workItemLabels(request)
		if err != nil {
			return nil, err
		}
		goalID, err := optionalID(request, "goal_id")
		if err != nil {
			return nil, err
		}
		component, err := bc.Brain.CreateWorkPlanComponent(ctx, namespaceID, brain.WorkPlanComponentInput{
			GoalID: goalID, Title: request.GetString("title", ""), Description: request.GetString("description", ""),
			TechnicalDetails: request.GetString("technical_details", ""), OwnedPaths: paths, Labels: labels,
			Reporter: request.GetString("reporter", ""), Owner: request.GetString("owner", ""),
			Status: request.GetString("status", "ready"), Priority: request.GetInt("priority", 0), Position: request.GetFloat("position", 0),
		})
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, component)
	})

	mcpServer.AddTool(mcp.NewTool("update_plan_component",
		mcp.WithDescription("Update selected wording or technical metadata on a plan component while preserving its stable ID, status, tasks, worktrees, and lifecycle history."),
		mcp.WithNumber("component_id", mcp.Required()),
		mcp.WithNumber("goal_id", mcp.Description("Replacement goal in the shared project goal tree")),
		mcp.WithString("title", mcp.Description("Replacement owner-facing component outcome")),
		mcp.WithString("description", mcp.Description("Replacement owner-facing scope and done condition")),
		mcp.WithString("technical_details", mcp.Description("Replacement implementation detail; send an empty string to clear")),
		mcp.WithString("owned_paths", mcp.Description("Replacement comma-separated file patterns, connector resources, or other work scopes; send an empty string to clear")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		componentID, err := requiredPositiveID(request, "component_id")
		if err != nil {
			return nil, err
		}
		current, err := bc.Brain.GetWorkItem(ctx, componentID)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, current.NamespaceID); err != nil {
			return nil, err
		}
		title, err := workPlanOptionalString(request, "title")
		if err != nil {
			return nil, err
		}
		description, err := workPlanOptionalString(request, "description")
		if err != nil {
			return nil, err
		}
		technicalDetails, err := workPlanOptionalString(request, "technical_details")
		if err != nil {
			return nil, err
		}
		paths, err := workPlanOptionalPaths(request, "owned_paths")
		if err != nil {
			return nil, err
		}
		goalID, err := optionalID(request, "goal_id")
		if err != nil {
			return nil, err
		}
		if title == nil && description == nil && technicalDetails == nil && paths == nil && goalID == nil {
			return nil, fmt.Errorf("provide at least one component field to update")
		}
		component, err := bc.Brain.UpdateWorkPlanComponent(ctx, componentID, brain.WorkPlanComponentUpdate{
			GoalID: goalID, Title: title, Description: description, TechnicalDetails: technicalDetails, OwnedPaths: paths,
		})
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, component)
	})

	mcpServer.AddTool(mcp.NewTool("create_plan_task",
		mcp.WithDescription("Create one executable child task under a plan component. Set provenance to agent for the caller's imminent intent or roadmap for durable planning intent."),
		mcp.WithNumber("component_id", mcp.Required()),
		mcp.WithNumber("goal_id", mcp.Description("Specific child goal this task completes; defaults to the component goal")),
		mcp.WithString("title", mcp.Required(), mcp.Description("Plain-language outcome that can be recognized as done")),
		mcp.WithString("description", mcp.Description("Owner-facing scope and done condition")),
		mcp.WithString("technical_details", mcp.Description("Optional implementation detail for the technical line")),
		mcp.WithString("labels"), mcp.WithString("reporter"),
		mcp.WithString("provenance", mcp.Description("agent, roadmap, or empty")),
		mcp.WithNumber("priority", mcp.DefaultNumber(0)), mcp.WithNumber("position", mcp.DefaultNumber(0)),
		mcp.WithString("namespace", mcp.Description("Exact project namespace path")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}
		componentID, err := requiredPositiveID(request, "component_id")
		if err != nil {
			return nil, err
		}
		labels, err := workItemLabels(request)
		if err != nil {
			return nil, err
		}
		goalID, err := optionalID(request, "goal_id")
		if err != nil {
			return nil, err
		}
		task, err := bc.Brain.CreateWorkPlanTask(ctx, namespaceID, brain.WorkPlanTaskInput{
			ComponentID: componentID, GoalID: goalID, Title: request.GetString("title", ""), Description: request.GetString("description", ""),
			TechnicalDetails: request.GetString("technical_details", ""), Labels: labels, Reporter: request.GetString("reporter", ""),
			Provenance: request.GetString("provenance", ""), Priority: request.GetInt("priority", 0), Position: request.GetFloat("position", 0),
		})
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, task)
	})

	mcpServer.AddTool(mcp.NewTool("update_plan_task",
		mcp.WithDescription("Update selected wording, completion criteria, technical detail, or provenance on a plan task while preserving its component, stable ID, state, agents, and worktree links."),
		mcp.WithNumber("task_id", mcp.Required()),
		mcp.WithNumber("goal_id", mcp.Description("Replacement goal in the shared project goal tree")),
		mcp.WithString("title", mcp.Description("Replacement plain-language outcome")),
		mcp.WithString("description", mcp.Description("Replacement owner-facing scope and done condition")),
		mcp.WithString("technical_details", mcp.Description("Replacement implementation detail; send an empty string to clear")),
		mcp.WithString("provenance", mcp.Description("agent, roadmap, or empty")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		taskID, err := requiredPositiveID(request, "task_id")
		if err != nil {
			return nil, err
		}
		current, err := bc.Brain.GetWorkItem(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, current.NamespaceID); err != nil {
			return nil, err
		}
		title, err := workPlanOptionalString(request, "title")
		if err != nil {
			return nil, err
		}
		description, err := workPlanOptionalString(request, "description")
		if err != nil {
			return nil, err
		}
		technicalDetails, err := workPlanOptionalString(request, "technical_details")
		if err != nil {
			return nil, err
		}
		provenance, err := workPlanOptionalString(request, "provenance")
		if err != nil {
			return nil, err
		}
		goalID, err := optionalID(request, "goal_id")
		if err != nil {
			return nil, err
		}
		if title == nil && description == nil && technicalDetails == nil && provenance == nil && goalID == nil {
			return nil, fmt.Errorf("provide at least one task field to update")
		}
		task, err := bc.Brain.UpdateWorkPlanTask(ctx, taskID, brain.WorkPlanTaskUpdate{
			GoalID: goalID, Title: title, Description: description, TechnicalDetails: technicalDetails, Provenance: provenance,
		})
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, task)
	})

	mcpServer.AddTool(mcp.NewTool("start_plan_task",
		mcp.WithDescription("Mark a plan task doing before implementation and permanently record the agent that started it."),
		mcp.WithNumber("task_id", mcp.Required()), mcp.WithString("agent", mcp.Description("Required for local clients; ignored when an authenticated identity is available")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		taskID, err := requiredPositiveID(request, "task_id")
		if err != nil {
			return nil, err
		}
		current, err := bc.Brain.GetWorkItem(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, current.NamespaceID); err != nil {
			return nil, err
		}
		task, err := bc.Brain.StartWorkPlanTask(ctx, taskID, workPlanActor(ctx, request))
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, task)
	})

	mcpServer.AddTool(mcp.NewTool("complete_plan_task",
		mcp.WithDescription("Mark a started plan task done immediately after it completes and retain its completion agent."),
		mcp.WithNumber("task_id", mcp.Required()), mcp.WithString("agent", mcp.Description("Required for local clients; ignored when an authenticated identity is available")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		taskID, err := requiredPositiveID(request, "task_id")
		if err != nil {
			return nil, err
		}
		current, err := bc.Brain.GetWorkItem(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, current.NamespaceID); err != nil {
			return nil, err
		}
		task, err := bc.Brain.CompleteWorkPlanTask(ctx, taskID, workPlanActor(ctx, request))
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, task)
	})

	mcpServer.AddTool(mcp.NewTool("block_plan_task",
		mcp.WithDescription("Mark a plan task blocked as soon as it is stuck and optionally record the blocker."),
		mcp.WithNumber("task_id", mcp.Required()), mcp.WithString("agent"), mcp.WithString("reason"),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		taskID, err := requiredPositiveID(request, "task_id")
		if err != nil {
			return nil, err
		}
		current, err := bc.Brain.GetWorkItem(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, current.NamespaceID); err != nil {
			return nil, err
		}
		task, err := bc.Brain.BlockWorkPlanTask(ctx, taskID, workPlanActor(ctx, request), request.GetString("reason", ""))
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, task)
	})

	mcpServer.AddTool(mcp.NewTool("unblock_plan_task",
		mcp.WithDescription("Clear a plan task's blocked state after its blocker is resolved."),
		mcp.WithNumber("task_id", mcp.Required()),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		taskID, err := requiredPositiveID(request, "task_id")
		if err != nil {
			return nil, err
		}
		current, err := bc.Brain.GetWorkItem(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, current.NamespaceID); err != nil {
			return nil, err
		}
		task, err := bc.Brain.UnblockWorkPlanTask(ctx, taskID)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, task)
	})

	mcpServer.AddTool(mcp.NewTool("delete_plan_component",
		mcp.WithDescription("Remove a plan component and its child tasks. Create a new component instead of repurposing an old one."),
		mcp.WithNumber("component_id", mcp.Required()),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		componentID, err := requiredPositiveID(request, "component_id")
		if err != nil {
			return nil, err
		}
		current, err := bc.Brain.GetWorkItem(ctx, componentID)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, current.NamespaceID); err != nil {
			return nil, err
		}
		if err := bc.Brain.DeleteWorkPlanComponent(ctx, componentID); err != nil {
			return nil, err
		}
		return jsonToolResult(bc, map[string]any{"ok": true, "component_id": componentID})
	})

	mcpServer.AddTool(mcp.NewTool("delete_plan_task",
		mcp.WithDescription("Remove one plan task that is no longer needed."),
		mcp.WithNumber("task_id", mcp.Required()),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		taskID, err := requiredPositiveID(request, "task_id")
		if err != nil {
			return nil, err
		}
		current, err := bc.Brain.GetWorkItem(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, current.NamespaceID); err != nil {
			return nil, err
		}
		if err := bc.Brain.DeleteWorkPlanTask(ctx, taskID); err != nil {
			return nil, err
		}
		return jsonToolResult(bc, map[string]any{"ok": true, "task_id": taskID})
	})

	mcpServer.AddTool(mcp.NewTool("set_plan_component_paths",
		mcp.WithDescription("Replace the file patterns, connector resources, or other work scopes owned by a plan component."),
		mcp.WithNumber("component_id", mcp.Required()), mcp.WithString("owned_paths", mcp.Required(), mcp.Description("Comma-separated work scopes")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		componentID, err := requiredPositiveID(request, "component_id")
		if err != nil {
			return nil, err
		}
		current, err := bc.Brain.GetWorkItem(ctx, componentID)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, current.NamespaceID); err != nil {
			return nil, err
		}
		paths, err := workPlanPaths(request, "owned_paths")
		if err != nil {
			return nil, err
		}
		component, err := bc.Brain.SetWorkPlanComponentPaths(ctx, componentID, paths)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, component)
	})

	mcpServer.AddTool(mcp.NewTool("link_plan_components",
		mcp.WithDescription("Connect plan components. relationship needs means the first component must come after the related component; links means they are connected."),
		mcp.WithNumber("component_id", mcp.Required()), mcp.WithNumber("related_component_id", mcp.Required()),
		mcp.WithString("relationship", mcp.Description("needs or links"), mcp.DefaultString("needs")),
		mcp.WithString("namespace", mcp.Description("Exact project namespace path")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}
		componentID, err := requiredPositiveID(request, "component_id")
		if err != nil {
			return nil, err
		}
		relatedID, err := requiredPositiveID(request, "related_component_id")
		if err != nil {
			return nil, err
		}
		edge, err := bc.Brain.LinkWorkPlanComponents(ctx, namespaceID, componentID, relatedID, request.GetString("relationship", "needs"))
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, edge)
	})

	mcpServer.AddTool(mcp.NewTool("record_plan_decision",
		mcp.WithDescription("Record a decision that changes the plan before implementing it."),
		mcp.WithString("title", mcp.Required()), mcp.WithString("rationale"),
		mcp.WithNumber("component_id"), mcp.WithNumber("work_item_id"), mcp.WithString("author"),
		mcp.WithString("namespace", mcp.Description("Exact project namespace path")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}
		componentID, err := optionalID(request, "component_id")
		if err != nil {
			return nil, err
		}
		workItemID, err := optionalID(request, "work_item_id")
		if err != nil {
			return nil, err
		}
		author := workPlanActor(ctx, request)
		if author == "" {
			author = request.GetString("author", "")
		}
		decision, err := bc.Brain.RecordWorkPlanDecision(ctx, namespaceID, brain.WorkPlanDecisionInput{
			ComponentID: componentID, WorkItemID: workItemID, Title: request.GetString("title", ""),
			Rationale: request.GetString("rationale", ""), Author: author,
		})
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, decision)
	})

	mcpServer.AddTool(mcp.NewTool("list_plan_decisions",
		mcp.WithDescription("List plan-affecting decisions in newest-first order."),
		mcp.WithString("namespace", mcp.Description("Exact project namespace path")),
		mcp.WithNumber("limit", mcp.DefaultNumber(100)), mcp.WithNumber("offset", mcp.DefaultNumber(0)),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}
		decisions, err := bc.Brain.ListWorkPlanDecisions(ctx, namespaceID, brain.Pagination{
			Limit: request.GetInt("limit", 100), Offset: request.GetInt("offset", 0),
		})
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, decisions, request.GetInt("offset", 0))
	})
}
