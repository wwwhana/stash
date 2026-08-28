package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/brain"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func parseOptionalTime(request mcp.CallToolRequest, key string) (*time.Time, error) {
	args := request.GetArguments()
	raw, ok := args[key]
	if !ok {
		return nil, nil
	}
	value, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("argument %q must be an RFC3339 string", key)
	}
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("argument %q must be RFC3339: %w", key, err)
	}
	return &parsed, nil
}

func workGraphNamespaceQuery(request mcp.CallToolRequest) string {
	if project := strings.TrimSpace(request.GetString("project", "")); project != "" {
		return project
	}
	return request.GetString("namespaces", "/")
}

func optionalID(request mcp.CallToolRequest, key string) (*int64, error) {
	args := request.GetArguments()
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}
	id := request.GetInt(key, 0)
	if id <= 0 {
		return nil, fmt.Errorf("argument %q must be a positive integer", key)
	}
	id64 := int64(id)
	return &id64, nil
}

func requiredPositiveID(request mcp.CallToolRequest, key string) (int64, error) {
	id := request.GetInt(key, 0)
	if id <= 0 {
		return 0, fmt.Errorf("argument %q must be a positive integer", key)
	}
	return int64(id), nil
}

func worktreeMetadata(request mcp.CallToolRequest) (json.RawMessage, error) {
	args := request.GetArguments()
	raw, ok := args["metadata"]
	if !ok || raw == nil {
		return json.RawMessage(`{}`), nil
	}
	if value, ok := raw.(string); ok {
		metadata := json.RawMessage(value)
		if !json.Valid(metadata) {
			return nil, fmt.Errorf("metadata must be valid JSON")
		}
		return metadata, nil
	}
	metadata, err := json.Marshal(raw)
	if err != nil || !json.Valid(metadata) {
		return nil, fmt.Errorf("metadata must be valid JSON")
	}
	return metadata, nil
}

func workEventPayload(request mcp.CallToolRequest) (json.RawMessage, error) {
	args := request.GetArguments()
	raw, ok := args["payload"]
	if !ok || raw == nil {
		return json.RawMessage(`{}`), nil
	}
	if value, ok := raw.(string); ok {
		payload := json.RawMessage(value)
		if !json.Valid(payload) {
			return nil, fmt.Errorf("payload must be valid JSON")
		}
		return payload, nil
	}
	payload, err := json.Marshal(raw)
	if err != nil || !json.Valid(payload) {
		return nil, fmt.Errorf("payload must be valid JSON")
	}
	return payload, nil
}

func workItemLabels(request mcp.CallToolRequest) ([]string, error) {
	raw, ok := request.GetArguments()["labels"]
	if !ok || raw == nil {
		return nil, nil
	}
	switch value := raw.(type) {
	case string:
		parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '\n' })
		return parts, nil
	case []string:
		return value, nil
	case []any:
		labels := make([]string, 0, len(value))
		for _, entry := range value {
			label, ok := entry.(string)
			if !ok {
				return nil, fmt.Errorf("argument %q must contain only strings", "labels")
			}
			labels = append(labels, label)
		}
		return labels, nil
	default:
		return nil, fmt.Errorf("argument %q must be a comma-separated string or string array", "labels")
	}
}

func registerWorkGraphTools(mcpServer *server.MCPServer, bc *bootstrap.Context) {
	mcpServer.AddTool(mcp.NewTool("list_work_items",
		mcp.WithDescription("List active work items for a namespace. Use status for Kanban columns."),
		mcp.WithString("namespaces", mcp.Description("Comma-separated namespace paths")),
		mcp.WithString("status", mcp.Description("backlog, ready, doing, blocked, review, done, or canceled")),
		mcp.WithString("issue_type", mcp.Description("task, bug, feature, chore, or question")),
		mcp.WithString("label", mcp.Description("Filter by one label")),
		mcp.WithString("q", mcp.Description("Search issue key, title, or description")),
		mcp.WithNumber("worktree_id", mcp.Description("Optional registered worktree ID")),
		mcp.WithNumber("limit", mcp.Description("Maximum results"), mcp.DefaultNumber(100)),
		mcp.WithNumber("offset", mcp.Description("Result offset"), mcp.DefaultNumber(0)),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespaces, err := resolveNamespaces(ctx, request.GetString("namespaces", "/"))
		if err != nil {
			return nil, err
		}
		worktreeID, err := optionalID(request, "worktree_id")
		if err != nil {
			return nil, err
		}
		items, err := bc.Brain.ListWorkItemsFiltered(ctx, namespaces, request.GetString("status", ""), request.GetString("issue_type", ""), request.GetString("label", ""), request.GetString("q", ""), worktreeID, brain.Pagination{
			Limit: request.GetInt("limit", 100), Offset: request.GetInt("offset", 0),
		})
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, items, request.GetInt("offset", 0))
	})

	mcpServer.AddTool(mcp.NewTool("get_work_item",
		mcp.WithDescription("Get one work item and its linked worktrees."),
		mcp.WithNumber("id", mcp.Required()),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := requiredPositiveID(request, "id")
		if err != nil {
			return nil, err
		}
		item, err := bc.Brain.GetWorkItem(ctx, id)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, item.NamespaceID); err != nil {
			return nil, err
		}
		return jsonToolResult(bc, item)
	})

	mcpServer.AddTool(mcp.NewTool("get_work_item_by_key",
		mcp.WithDescription("Get one local issue by key such as W-000123."),
		mcp.WithString("issue_key", mcp.Required()),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		item, err := bc.Brain.GetWorkItemByKey(ctx, request.GetString("issue_key", ""))
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, item.NamespaceID); err != nil {
			return nil, err
		}
		return jsonToolResult(bc, item)
	})

	mcpServer.AddTool(mcp.NewTool("create_work_item",
		mcp.WithDescription("Create a local issue/task card under an optional goal or parent task."),
		mcp.WithString("title", mcp.Required()),
		mcp.WithString("description"),
		mcp.WithString("issue_type", mcp.Description("task, bug, feature, chore, or question"), mcp.DefaultString("task")),
		mcp.WithString("labels", mcp.Description("Comma-separated labels")),
		mcp.WithString("reporter", mcp.Description("Person or agent that reported the issue")),
		mcp.WithString("status", mcp.Description("backlog, ready, doing, blocked, review, done, or canceled"), mcp.DefaultString("backlog")),
		mcp.WithNumber("priority", mcp.DefaultNumber(0)),
		mcp.WithNumber("position", mcp.DefaultNumber(0)),
		mcp.WithString("owner"),
		mcp.WithString("due_at", mcp.Description("Optional RFC3339 deadline")),
		mcp.WithNumber("goal_id"),
		mcp.WithNumber("parent_id"),
		mcp.WithString("namespace", mcp.Description("Exact namespace path")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}
		goalID, err := optionalID(request, "goal_id")
		if err != nil {
			return nil, err
		}
		parentID, err := optionalID(request, "parent_id")
		if err != nil {
			return nil, err
		}
		if goalID != nil {
			goal, err := bc.Brain.GetGoal(ctx, *goalID)
			if err != nil {
				return nil, err
			}
			if err := authorizeRelatedNamespace(ctx, bc, namespaceID, goal.NamespaceID); err != nil {
				return nil, err
			}
		}
		if parentID != nil {
			parent, err := bc.Brain.GetWorkItem(ctx, *parentID)
			if err != nil {
				return nil, err
			}
			if err := authorizeRelatedNamespace(ctx, bc, namespaceID, parent.NamespaceID); err != nil {
				return nil, err
			}
		}
		dueAt, err := parseOptionalTime(request, "due_at")
		if err != nil {
			return nil, err
		}
		labels, err := workItemLabels(request)
		if err != nil {
			return nil, err
		}
		item, err := bc.Brain.CreateWorkItemWithDetails(ctx, namespaceID, brain.WorkItemInput{
			GoalID: goalID, ParentID: parentID, IssueType: request.GetString("issue_type", "task"),
			Labels: labels, Reporter: request.GetString("reporter", ""), Title: request.GetString("title", ""),
			Description: request.GetString("description", ""), Status: request.GetString("status", "backlog"),
			Priority: request.GetInt("priority", 0), Position: request.GetFloat("position", 0),
			Owner: request.GetString("owner", ""), DueAt: dueAt,
		})
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, item)
	})

	mcpServer.AddTool(mcp.NewTool("update_work_item",
		mcp.WithDescription("Update a local issue/task card. Omitted fields keep their current value."),
		mcp.WithNumber("id", mcp.Required()),
		mcp.WithString("title"), mcp.WithString("description"), mcp.WithString("issue_type"), mcp.WithString("labels"), mcp.WithString("reporter"), mcp.WithString("status"),
		mcp.WithNumber("priority"), mcp.WithNumber("position"), mcp.WithString("owner"),
		mcp.WithString("due_at", mcp.Description("RFC3339 deadline; empty string clears it")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := requiredPositiveID(request, "id")
		if err != nil {
			return nil, err
		}
		current, err := bc.Brain.GetWorkItem(ctx, id)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, current.NamespaceID); err != nil {
			return nil, err
		}
		args := request.GetArguments()
		title, description, issueType, reporter, status, owner := current.Title, current.Description, current.IssueType, current.Reporter, current.Status, current.Owner
		labels := current.Labels
		priority, position := current.Priority, current.Position
		if _, ok := args["title"]; ok {
			title = request.GetString("title", title)
		}
		if _, ok := args["description"]; ok {
			description = request.GetString("description", description)
		}
		if _, ok := args["issue_type"]; ok {
			issueType = request.GetString("issue_type", issueType)
		}
		if _, ok := args["labels"]; ok {
			labels, err = workItemLabels(request)
			if err != nil {
				return nil, err
			}
		}
		if _, ok := args["reporter"]; ok {
			reporter = request.GetString("reporter", reporter)
		}
		if _, ok := args["status"]; ok {
			status = request.GetString("status", status)
		}
		if _, ok := args["owner"]; ok {
			owner = request.GetString("owner", owner)
		}
		if _, ok := args["priority"]; ok {
			priority = request.GetInt("priority", priority)
		}
		if _, ok := args["position"]; ok {
			position = request.GetFloat("position", position)
		}
		dueAt := current.DueAt
		if _, ok := args["due_at"]; ok {
			dueAt, err = parseOptionalTime(request, "due_at")
			if err != nil {
				return nil, err
			}
		}
		item, err := bc.Brain.UpdateWorkItemWithDetails(ctx, id, brain.WorkItemInput{
			IssueType: issueType, Labels: labels, Reporter: reporter, Title: title, Description: description,
			Status: status, Priority: priority, Position: position, Owner: owner, DueAt: dueAt,
		})
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, item)
	})

	mcpServer.AddTool(mcp.NewTool("delete_work_item",
		mcp.WithDescription("Soft-delete a work item and its child tasks."),
		mcp.WithNumber("id", mcp.Required()),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := requiredPositiveID(request, "id")
		if err != nil {
			return nil, err
		}
		item, err := bc.Brain.GetWorkItem(ctx, id)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, item.NamespaceID); err != nil {
			return nil, err
		}
		if err := bc.Brain.DeleteWorkItem(ctx, id); err != nil {
			return nil, err
		}
		return jsonToolResult(bc, map[string]any{"ok": true, "id": id})
	})

	mcpServer.AddTool(mcp.NewTool("add_work_item_comment",
		mcp.WithDescription("Add a human or agent note to a local issue."),
		mcp.WithNumber("work_item_id", mcp.Required()), mcp.WithString("body", mcp.Required()),
		mcp.WithString("author", mcp.Description("Author for local clients; verified SSO identity is used remotely")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		itemID, err := requiredPositiveID(request, "work_item_id")
		if err != nil {
			return nil, err
		}
		item, err := bc.Brain.GetWorkItem(ctx, itemID)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, item.NamespaceID); err != nil {
			return nil, err
		}
		author := request.GetString("author", "")
		if user, ok := ctx.Value(keySSOUser).(string); ok && user != "" {
			author = user
		}
		comment, err := bc.Brain.CreateWorkItemComment(ctx, itemID, author, request.GetString("body", ""))
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, comment)
	})

	mcpServer.AddTool(mcp.NewTool("list_work_item_comments",
		mcp.WithDescription("List notes attached to a local issue in conversation order."),
		mcp.WithNumber("work_item_id", mcp.Required()),
		mcp.WithNumber("limit", mcp.DefaultNumber(100)), mcp.WithNumber("offset", mcp.DefaultNumber(0)),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		itemID, err := requiredPositiveID(request, "work_item_id")
		if err != nil {
			return nil, err
		}
		item, err := bc.Brain.GetWorkItem(ctx, itemID)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, item.NamespaceID); err != nil {
			return nil, err
		}
		comments, err := bc.Brain.ListWorkItemComments(ctx, itemID, brain.Pagination{
			Limit: request.GetInt("limit", 100), Offset: request.GetInt("offset", 0),
		})
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, comments, request.GetInt("offset", 0))
	})

	mcpServer.AddTool(mcp.NewTool("add_work_item_dependency",
		mcp.WithDescription("Add a dependency edge. from_item_id blocks to_item_id when edge_type is blocks."),
		mcp.WithNumber("from_item_id", mcp.Required()),
		mcp.WithNumber("to_item_id", mcp.Required()),
		mcp.WithString("edge_type", mcp.Description("blocks or relates_to"), mcp.DefaultString("blocks")),
		mcp.WithString("namespace", mcp.Description("Exact namespace path")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fromID, err := requiredPositiveID(request, "from_item_id")
		if err != nil {
			return nil, err
		}
		toID, err := requiredPositiveID(request, "to_item_id")
		if err != nil {
			return nil, err
		}
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}
		edge, err := bc.Brain.AddWorkItemEdge(ctx, namespaceID, fromID, toID, request.GetString("edge_type", "blocks"))
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, edge)
	})

	mcpServer.AddTool(mcp.NewTool("delete_work_item_dependency",
		mcp.WithDescription("Soft-delete a work item dependency edge."),
		mcp.WithNumber("id", mcp.Required()),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := requiredPositiveID(request, "id")
		if err != nil {
			return nil, err
		}
		edge, err := bc.Brain.GetWorkItemEdge(ctx, id)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, edge.NamespaceID); err != nil {
			return nil, err
		}
		if err := bc.Brain.DeleteWorkItemEdge(ctx, id); err != nil {
			return nil, err
		}
		return jsonToolResult(bc, map[string]any{"ok": true, "id": id})
	})

	mcpServer.AddTool(mcp.NewTool("get_work_graph",
		mcp.WithDescription("Return task nodes, dependency edges, and registered worktrees for a graph view. Set project to an exact project namespace such as /projects/myapp to isolate one project."),
		mcp.WithString("namespaces", mcp.Description("Comma-separated namespace paths")),
		mcp.WithString("project", mcp.Description("Optional exact project namespace path; when set, it takes precedence over namespaces")),
		mcp.WithBoolean("include_done", mcp.Description("Include done and canceled nodes"), mcp.DefaultBool(false)),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespaceQuery := workGraphNamespaceQuery(request)
		namespaces, err := resolveNamespaces(ctx, namespaceQuery)
		if err != nil {
			return nil, err
		}
		graph, err := bc.Brain.GetWorkGraph(ctx, namespaces, request.GetBool("include_done", false))
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, graph)
	})

	mcpServer.AddTool(mcp.NewTool("list_worktrees",
		mcp.WithDescription("List local Git worktrees registered by an agent bridge."),
		mcp.WithString("namespaces", mcp.Description("Comma-separated namespace paths")),
		mcp.WithNumber("limit", mcp.DefaultNumber(100)), mcp.WithNumber("offset", mcp.DefaultNumber(0)),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespaces, err := resolveNamespaces(ctx, request.GetString("namespaces", "/"))
		if err != nil {
			return nil, err
		}
		worktrees, err := bc.Brain.ListWorktrees(ctx, namespaces, brain.Pagination{
			Limit: request.GetInt("limit", 100), Offset: request.GetInt("offset", 0),
		})
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, worktrees, request.GetInt("offset", 0))
	})

	mcpServer.AddTool(mcp.NewTool("register_worktree",
		mcp.WithDescription("Register or update one Git worktree reported by a local bridge."),
		mcp.WithString("repository", mcp.Required()), mcp.WithString("worktree_path", mcp.Required()),
		mcp.WithString("branch"), mcp.WithString("head_sha"),
		mcp.WithString("status", mcp.Description("unknown, clean, dirty, missing, merged, or removed"), mcp.DefaultString("unknown")),
		mcp.WithString("agent_id"), mcp.WithString("metadata"), mcp.WithString("namespace"),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}
		metadata, err := worktreeMetadata(request)
		if err != nil {
			return nil, err
		}
		worktree, err := bc.Brain.RegisterWorktree(ctx, namespaceID,
			request.GetString("repository", ""), request.GetString("worktree_path", ""),
			request.GetString("branch", ""), request.GetString("head_sha", ""),
			request.GetString("status", "unknown"), request.GetString("agent_id", ""), metadata)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, worktree)
	})

	mcpServer.AddTool(mcp.NewTool("attach_worktree_to_item",
		mcp.WithDescription("Attach a registered worktree to a task card."),
		mcp.WithNumber("work_item_id", mcp.Required()), mcp.WithNumber("worktree_id", mcp.Required()),
		mcp.WithString("relation", mcp.Description("active or related"), mcp.DefaultString("active")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		itemID, err := requiredPositiveID(request, "work_item_id")
		if err != nil {
			return nil, err
		}
		worktreeID, err := requiredPositiveID(request, "worktree_id")
		if err != nil {
			return nil, err
		}
		item, err := bc.Brain.GetWorkItem(ctx, itemID)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, item.NamespaceID); err != nil {
			return nil, err
		}
		if err := bc.Brain.AttachWorktreeToItem(ctx, itemID, worktreeID, request.GetString("relation", "active")); err != nil {
			return nil, err
		}
		return jsonToolResult(bc, map[string]any{"ok": true, "work_item_id": itemID, "worktree_id": worktreeID})
	})

	mcpServer.AddTool(mcp.NewTool("record_work_event",
		mcp.WithDescription("Append a structured worktree or agent event for live activity and later memory consolidation."),
		mcp.WithString("event_type", mcp.Required()), mcp.WithString("event_key"), mcp.WithString("payload"),
		mcp.WithNumber("worktree_id"), mcp.WithNumber("work_item_id"), mcp.WithString("occurred_at"), mcp.WithString("namespace"),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}
		worktreeID, err := optionalID(request, "worktree_id")
		if err != nil {
			return nil, err
		}
		workItemID, err := optionalID(request, "work_item_id")
		if err != nil {
			return nil, err
		}
		payload, err := workEventPayload(request)
		if err != nil {
			return nil, err
		}
		occurredAt, err := parseOptionalTime(request, "occurred_at")
		if err != nil {
			return nil, err
		}
		event, err := bc.Brain.RecordWorkEvent(ctx, namespaceID, worktreeID, workItemID,
			request.GetString("event_type", ""), request.GetString("event_key", ""), payload, occurredAt)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, event)
	})

	mcpServer.AddTool(mcp.NewTool("list_work_events",
		mcp.WithDescription("List recent structured work events."),
		mcp.WithString("namespaces"), mcp.WithNumber("worktree_id"), mcp.WithNumber("work_item_id"),
		mcp.WithNumber("limit", mcp.DefaultNumber(100)), mcp.WithNumber("offset", mcp.DefaultNumber(0)),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespaces, err := resolveNamespaces(ctx, request.GetString("namespaces", "/"))
		if err != nil {
			return nil, err
		}
		worktreeID, err := optionalID(request, "worktree_id")
		if err != nil {
			return nil, err
		}
		workItemID, err := optionalID(request, "work_item_id")
		if err != nil {
			return nil, err
		}
		events, err := bc.Brain.ListWorkEvents(ctx, namespaces, worktreeID, workItemID, brain.Pagination{
			Limit: request.GetInt("limit", 100), Offset: request.GetInt("offset", 0),
		})
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, events, request.GetInt("offset", 0))
	})

	mcpServer.AddTool(mcp.NewTool("link_work_item_memory",
		mcp.WithDescription("Link a task to an episode, fact, hypothesis, failure, or goal in the same namespace."),
		mcp.WithNumber("work_item_id", mcp.Required()), mcp.WithString("memory_type", mcp.Required()),
		mcp.WithNumber("memory_id", mcp.Required()), mcp.WithString("relation", mcp.DefaultString("context")),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		workItemID, err := requiredPositiveID(request, "work_item_id")
		if err != nil {
			return nil, err
		}
		memoryID, err := requiredPositiveID(request, "memory_id")
		if err != nil {
			return nil, err
		}
		item, err := bc.Brain.GetWorkItem(ctx, workItemID)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, item.NamespaceID); err != nil {
			return nil, err
		}
		link, err := bc.Brain.LinkWorkItemMemory(ctx, workItemID, request.GetString("memory_type", ""), memoryID, request.GetString("relation", "context"))
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, link)
	})

	mcpServer.AddTool(mcp.NewTool("list_work_item_memory_links",
		mcp.WithDescription("List memory evidence linked to a local issue."),
		mcp.WithNumber("work_item_id", mcp.Required()),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		workItemID, err := requiredPositiveID(request, "work_item_id")
		if err != nil {
			return nil, err
		}
		item, err := bc.Brain.GetWorkItem(ctx, workItemID)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, item.NamespaceID); err != nil {
			return nil, err
		}
		links, err := bc.Brain.ListWorkItemMemoryLinks(ctx, workItemID)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, links)
	})
}
