package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/brain"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func stringListArgument(request mcp.CallToolRequest, key string) ([]string, error) {
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
		result := make([]string, 0, len(value))
		for _, entry := range value {
			text, ok := entry.(string)
			if !ok {
				return nil, fmt.Errorf("argument %q must contain only strings", key)
			}
			result = append(result, text)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("argument %q must be a comma-separated string or string array", key)
	}
}

func jsonObjectArgument(request mcp.CallToolRequest, key string) (json.RawMessage, error) {
	data, err := structuredArgumentBytes(request, key, false)
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
		return nil, fmt.Errorf("argument %q must be a JSON object: %w", key, err)
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("argument %q must be a JSON object: %w", key, err)
	}
	return canonical, nil
}

func projectCapabilityOption() mcp.ToolOption {
	return mcp.WithArray("capabilities",
		mcp.Description("Routing hints such as code, browser, research, document, data, device, or human. They do not grant permission."),
		mcp.MaxItems(16), mcp.UniqueItems(true),
		mcp.Items(map[string]any{"type": "string", "pattern": `^[a-z][a-z0-9_-]{0,63}$`}),
	)
}

func registerProjectCoordinationTools(mcpServer *server.MCPServer, bc *bootstrap.Context) {
	mcpServer.AddTool(mcp.NewTool("resume_project",
		mcp.WithDescription("Universal session entry point for Web MCP agents. Returns the shared goal, this agent's active work, and at most three runnable candidates without requiring Git, local paths, or MCP Roots."),
		mcp.WithString("namespace", mcp.Description("Exact project namespace path"), mcp.Required()),
		mcp.WithString("agent_id", mcp.Description("Optional stable display and routing name for this agent")),
		projectCapabilityOption(),
		mcp.WithString("known_context_digest", mcp.Description("Digest from the previous response; matching state returns a tiny unchanged receipt"), mcp.Pattern(`sha256:[0-9a-f]{64}`)),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
	), recordWorkExecutionHandler("resume_project", func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		logicalNamespace := request.GetString("namespace", "")
		physicalNamespace, namespaceID, err := exactNamespaceID(ctx, bc, logicalNamespace)
		if err != nil {
			return nil, err
		}
		capabilities, err := stringListArgument(request, "capabilities")
		if err != nil {
			return nil, err
		}
		principalID, err := workExecutionPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		_, filterCapabilities := request.GetArguments()["capabilities"]
		brief, err := bc.Brain.ResumeProject(
			ctx, physicalNamespace, namespaceID, principalID,
			request.GetString("agent_id", ""), capabilities, filterCapabilities,
		)
		if err != nil {
			return nil, err
		}
		brief.Namespace = strings.TrimSpace(logicalNamespace)
		brief.ContextDigest = ""
		digest, err := agentContextDigest(brief)
		if err != nil {
			return nil, err
		}
		brief.ContextDigest = digest
		if receipt := matchingAgentContextReceipt(
			request.GetString("known_context_digest", ""), "project", namespaceID, brief.NextAction, digest,
		); receipt != nil {
			return jsonToolResult(bc, receipt)
		}
		return jsonToolResult(bc, brief)
	}))

	mcpServer.AddTool(mcp.NewTool("claim_work", claimWorkToolOptions(
		"Claim one prepared work item immediately before acting. The exclusive lease works for code, research, documents, browser actions, APIs, data, devices, and human approvals; Git metadata is optional.",
	)...), recordWorkExecutionHandler("claim", func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleClaimWork(ctx, request, bc)
	}))

	mcpServer.AddTool(mcp.NewTool("set_work_capabilities",
		mcp.WithDescription("Set the small list of capabilities required to route a work item. Capability names guide selection but never grant authorization."),
		mcp.WithNumber("work_item_id", mcp.Required()),
		projectCapabilityOption(),
		mcp.WithIdempotentHintAnnotation(true),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		workItemID, err := requiredPositiveID(request, "work_item_id")
		if err != nil {
			return nil, err
		}
		if _, err := authorizeWorkItem(ctx, bc, workItemID); err != nil {
			return nil, err
		}
		capabilities, err := stringListArgument(request, "capabilities")
		if err != nil {
			return nil, err
		}
		capabilities, err = bc.Brain.SetWorkItemCapabilities(ctx, workItemID, capabilities)
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, map[string]any{"work_item_id": workItemID, "required_capabilities": capabilities})
	})

	mcpServer.AddTool(mcp.NewTool("spawn_work", attemptMutationOptions(
		mcp.WithDescription("Create and prepare a child, prerequisite, or related work item discovered during an active attempt. Child and prerequisite work block the current item until completed."),
		mcp.WithString("title", mcp.Required()),
		mcp.WithString("description"),
		mcp.WithString("issue_type", mcp.Description("task, bug, feature, chore, or question"), mcp.DefaultString("task")),
		mcp.WithNumber("priority", mcp.DefaultNumber(0)),
		mcp.WithNumber("position", mcp.DefaultNumber(0)),
		mcp.WithString("reporter"),
		mcp.WithString("relationship", mcp.Description("child, prerequisite, or related"), mcp.DefaultString("child"), mcp.Enum("child", "prerequisite", "related")),
		mcp.WithString("next_action", mcp.Description("One concrete first action for the new work item"), mcp.Required()),
		mcp.WithArray("conditions", mcp.Description("Observable completion conditions for the new work item"), mcp.Required(), mcp.MinItems(1), mcp.MaxItems(100), mcp.Items(conditionArraySchema())),
		projectCapabilityOption(),
	)...), recordWorkExecutionHandler("spawn", func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		attemptID, leaseToken, actionKey, err := attemptMutationArguments(request)
		if err != nil {
			return nil, err
		}
		if _, _, err := authorizeWorkAttempt(ctx, bc, attemptID); err != nil {
			return nil, redactWorkLeaseError(err, leaseToken)
		}
		conditions, err := parseCompletionConditionInputs(request)
		if err != nil {
			return nil, err
		}
		capabilities, err := stringListArgument(request, "capabilities")
		if err != nil {
			return nil, err
		}
		input := brain.SpawnWorkInput{
			Title: request.GetString("title", ""), Description: request.GetString("description", ""),
			IssueType: request.GetString("issue_type", "task"), Priority: request.GetInt("priority", 0),
			Position: request.GetFloat("position", 0), Reporter: request.GetString("reporter", ""),
			Relationship: request.GetString("relationship", "child"), NextAction: request.GetString("next_action", ""),
			Conditions: conditions, Capabilities: capabilities,
		}
		if err := rejectLeaseTokenContent(leaseToken, input.Title, input.Description, input.NextAction, input.Reporter); err != nil {
			return nil, err
		}
		principalID, err := workExecutionPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		spawned, err := bc.Brain.SpawnWorkForPrincipal(ctx, attemptID, leaseToken, principalID, input, actionKey)
		if err != nil {
			return nil, redactWorkLeaseError(err, leaseToken)
		}
		return jsonToolResult(bc, spawned)
	}))

	mcpServer.AddTool(mcp.NewTool("attach_work_resource",
		mcp.WithDescription("Attach a bounded reference to a work item. Store only a short summary and URI; keep document bodies and connector credentials in their original systems."),
		mcp.WithNumber("work_item_id", mcp.Required()),
		mcp.WithString("resource_key", mcp.Description("Stable retry-safe key such as jira:PROJ-12, confluence:12345, or artifact:<uuid>"), mcp.Required()),
		mcp.WithString("kind", mcp.Description("git, document, url, browser, api, dataset, device, ticket, file, or other"), mcp.Required()),
		mcp.WithString("source", mcp.Description("Short source name such as stash, jira, confluence, git, or web"), mcp.DefaultString("stash")),
		mcp.WithString("authority", mcp.Description("stash when Stash owns this record; external when another system remains authoritative"), mcp.DefaultString("stash"), mcp.Enum("stash", "external")),
		mcp.WithString("title", mcp.Required()),
		mcp.WithString("uri", mcp.Description("Optional absolute URI without credentials or secret query parameters")),
		mcp.WithString("summary", mcp.Description("Short context needed by an agent; do not copy the full document")),
		mcp.WithString("external_id"),
		mcp.WithString("revision"),
		mcp.WithString("content_digest", mcp.Pattern(`^sha256:[0-9a-f]{64}$`)),
		mcp.WithString("role", mcp.Description("input, target, output, evidence, or reference"), mcp.DefaultString("reference"), mcp.Enum("input", "target", "output", "evidence", "reference")),
		mcp.WithObject("metadata", mcp.Description("Optional small non-secret metadata object"), mcp.AdditionalProperties(true)),
		mcp.WithString("actor"),
		mcp.WithIdempotentHintAnnotation(true),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		workItemID, err := requiredPositiveID(request, "work_item_id")
		if err != nil {
			return nil, err
		}
		if _, err := authorizeWorkItem(ctx, bc, workItemID); err != nil {
			return nil, err
		}
		metadata, err := jsonObjectArgument(request, "metadata")
		if err != nil {
			return nil, err
		}
		actor := strings.TrimSpace(request.GetString("actor", ""))
		if user, ok := ctx.Value(keySSOUser).(string); ok && strings.TrimSpace(user) != "" {
			actor = user
		}
		attachment, err := bc.Brain.AttachWorkResource(ctx, workItemID, brain.WorkResourceInput{
			ResourceKey: request.GetString("resource_key", ""), Kind: request.GetString("kind", ""),
			Source: request.GetString("source", "stash"), Authority: request.GetString("authority", "stash"),
			Title: request.GetString("title", ""), URI: request.GetString("uri", ""),
			Summary: request.GetString("summary", ""), ExternalID: request.GetString("external_id", ""),
			Revision: request.GetString("revision", ""), ContentDigest: request.GetString("content_digest", ""),
			Metadata: metadata, CreatedBy: actor, LinkedBy: actor, Role: request.GetString("role", "reference"),
		})
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, attachment)
	})

	mcpServer.AddTool(mcp.NewTool("list_work_resources",
		mcp.WithDescription("List bounded resource references attached to one work item. Fetch an external body only when its URI is needed for the next action."),
		mcp.WithNumber("work_item_id", mcp.Required()),
		mcp.WithNumber("limit", mcp.DefaultNumber(20)), mcp.WithNumber("offset", mcp.DefaultNumber(0)),
		mcp.WithReadOnlyHintAnnotation(true),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		workItemID, err := requiredPositiveID(request, "work_item_id")
		if err != nil {
			return nil, err
		}
		if _, err := authorizeWorkItem(ctx, bc, workItemID); err != nil {
			return nil, err
		}
		resources, err := bc.Brain.ListWorkResourceRefs(ctx, workItemID, brain.Pagination{
			Limit: request.GetInt("limit", 20), Offset: request.GetInt("offset", 0),
		})
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, resources, request.GetInt("offset", 0))
	})

	mcpServer.AddTool(mcp.NewTool("get_work_resource",
		mcp.WithDescription("Get one bounded work resource reference by ID."),
		mcp.WithNumber("resource_id", mcp.Required()),
		mcp.WithReadOnlyHintAnnotation(true),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		resourceID, err := requiredPositiveID(request, "resource_id")
		if err != nil {
			return nil, err
		}
		resource, err := bc.Brain.GetWorkResource(ctx, resourceID)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, resource.NamespaceID); err != nil {
			return nil, err
		}
		return jsonToolResult(bc, resource)
	})

	registerProjectResourceTemplates(mcpServer, bc)
}

func parseTemplateID(uri, prefix, suffix string) (int64, error) {
	if !strings.HasPrefix(uri, prefix) || !strings.HasSuffix(uri, suffix) {
		return 0, fmt.Errorf("resource URI %q does not match %s{id}%s", uri, prefix, suffix)
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(uri, prefix), suffix)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("resource URI %q contains an invalid ID", uri)
	}
	return id, nil
}

func registerProjectResourceTemplates(mcpServer *server.MCPServer, bc *bootstrap.Context) {
	mcpServer.AddResourceTemplate(mcp.NewResourceTemplate("stash://work/{id}/brief", "Work brief",
		mcp.WithTemplateDescription("Compact goal, action, conditions, resources, dependency results, memory, and blockers for one work item."),
		mcp.WithTemplateMIMEType("application/json"),
	), func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		workItemID, err := parseTemplateID(request.Params.URI, "stash://work/", "/brief")
		if err != nil {
			return nil, err
		}
		if _, err := authorizeWorkItem(ctx, bc, workItemID); err != nil {
			return nil, err
		}
		bundle, err := bc.Brain.GetWorkResumeBundle(ctx, workItemID, 8)
		if err != nil {
			return nil, err
		}
		brief, err := buildWorkResumeBrief(bundle)
		if err != nil {
			return nil, err
		}
		payload, err := jsonResourcePayload(bc, brief, fmt.Sprintf("Call resume_work with work_item_id=%d for bounded context and continuation metadata.", workItemID))
		if err != nil {
			return nil, fmt.Errorf("prepare work brief resource: %w", err)
		}
		return []mcp.ResourceContents{mcp.TextResourceContents{
			URI: request.Params.URI, MIMEType: "application/json", Text: string(payload),
		}}, nil
	})

	mcpServer.AddResourceTemplate(mcp.NewResourceTemplate("stash://work-resource/{id}", "Work resource reference",
		mcp.WithTemplateDescription("Bounded metadata for one work resource. External content remains at its URI."),
		mcp.WithTemplateMIMEType("application/json"),
	), func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		resourceID, err := parseTemplateID(request.Params.URI, "stash://work-resource/", "")
		if err != nil {
			return nil, err
		}
		resource, err := bc.Brain.GetWorkResource(ctx, resourceID)
		if err != nil {
			return nil, err
		}
		if err := authorizeNamespaceID(ctx, bc, resource.NamespaceID); err != nil {
			return nil, err
		}
		payload, err := jsonResourcePayload(bc, resource, fmt.Sprintf("Call get_work_resource with resource_id=%d for the bounded reference.", resourceID))
		if err != nil {
			return nil, fmt.Errorf("prepare work resource: %w", err)
		}
		return []mcp.ResourceContents{mcp.TextResourceContents{
			URI: request.Params.URI, MIMEType: "application/json", Text: string(payload),
		}}, nil
	})
}
