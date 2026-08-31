package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/brain"
	"github.com/alash3al/stash/internal/models"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func workspaceIdentityToolOptions(requireAgent bool) []mcp.ToolOption {
	options := []mcp.ToolOption{
		mcp.WithString("cwd", mcp.Description("Current working directory"), mcp.Required()),
		mcp.WithString("repository_instance_id", mcp.Description("Stable random ID stored in local Git config for this clone"), mcp.Required()),
		mcp.WithString("git_common_dir", mcp.Description("Absolute path from git rev-parse --git-common-dir"), mcp.Required()),
		mcp.WithString("git_dir", mcp.Description("Absolute path from git rev-parse --git-dir"), mcp.Required()),
		mcp.WithString("worktree_path", mcp.Description("Absolute worktree root path"), mcp.Required()),
		mcp.WithString("remote_url", mcp.Description("Git remote URL; credentials are stripped before storage")),
		mcp.WithString("repository_provider", mcp.Description("Repository provider such as github, gitlab, or gitea")),
		mcp.WithString("repository_provider_id", mcp.Description("Provider-owned repository ID")),
		mcp.WithString("branch"),
		mcp.WithString("head_sha"),
		mcp.WithString("worktree_status", mcp.Description("unknown, clean, dirty, stale, missing, merged, or removed"), mcp.DefaultString("unknown")),
		mcp.WithObject("metadata", mcp.Description("Optional non-secret bridge metadata"), mcp.AdditionalProperties(true)),
	}
	if requireAgent {
		options = append(options, mcp.WithString("agent_id", mcp.Description("Agent or session identifier"), mcp.Required()))
	} else {
		options = append(options, mcp.WithString("agent_id", mcp.Description("Agent or session identifier")))
	}
	return options
}

func workspaceMetadataArgument(request mcp.CallToolRequest) (json.RawMessage, error) {
	data, err := structuredArgumentBytes(request, "metadata", false)
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
		return nil, fmt.Errorf("argument %q must be a JSON object: %w", "metadata", err)
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("argument %q must be a JSON object: %w", "metadata", err)
	}
	return canonical, nil
}

func workspaceIdentityArgument(request mcp.CallToolRequest) (brain.WorkspaceIdentityInput, error) {
	metadata, err := workspaceMetadataArgument(request)
	if err != nil {
		return brain.WorkspaceIdentityInput{}, err
	}
	input := brain.WorkspaceIdentityInput{
		CWD:                  request.GetString("cwd", ""),
		RepositoryInstanceID: request.GetString("repository_instance_id", ""),
		Provider:             request.GetString("repository_provider", ""),
		ProviderRepositoryID: request.GetString("repository_provider_id", ""),
		RemoteURL:            request.GetString("remote_url", ""),
		GitCommonDir:         request.GetString("git_common_dir", ""),
		GitDir:               request.GetString("git_dir", ""),
		WorktreePath:         request.GetString("worktree_path", ""),
		Branch:               request.GetString("branch", ""),
		HeadSHA:              request.GetString("head_sha", ""),
		Status:               request.GetString("worktree_status", "unknown"),
		AgentID:              request.GetString("agent_id", ""),
		Metadata:             metadata,
	}
	for name, value := range map[string]string{
		"cwd":                    input.CWD,
		"repository_instance_id": input.RepositoryInstanceID,
		"git_common_dir":         input.GitCommonDir,
		"git_dir":                input.GitDir,
		"worktree_path":          input.WorktreePath,
	} {
		if strings.TrimSpace(value) == "" {
			return brain.WorkspaceIdentityInput{}, fmt.Errorf("argument %q is required", name)
		}
	}
	return input, nil
}

func logicalWorkspaceResolution(ctx context.Context, resolution *models.WorkspaceResolution) error {
	if resolution == nil {
		return nil
	}
	logical, ok := logicalNamespaceSlug(ctx, resolution.Namespace.Slug)
	if !ok {
		return fmt.Errorf("forbidden: workspace is outside the authenticated namespace")
	}
	if resolution.Namespace.Name == resolution.Namespace.Slug {
		resolution.Namespace.Name = logical
	}
	resolution.Namespace.Slug = logical
	return nil
}

func removeLastNonRootGoal(tree *models.GoalTree) bool {
	if tree == nil || len(tree.Goals) <= 1 {
		return false
	}
	for index := len(tree.Goals) - 1; index >= 0; index-- {
		if tree.RootGoalID != nil && tree.Goals[index].ID == *tree.RootGoalID {
			continue
		}
		tree.Goals = append(tree.Goals[:index], tree.Goals[index+1:]...)
		return true
	}
	return false
}

func keepWorkspaceRootGoal(tree *models.GoalTree) {
	if tree == nil || tree.RootGoalID == nil {
		return
	}
	for _, goal := range tree.Goals {
		if goal.ID == *tree.RootGoalID {
			tree.Goals = []models.GoalProgress{goal}
			return
		}
	}
	tree.Goals = nil
}

func compactWorkspaceResumeBundle(bundle *models.WorkspaceResumeBundle, maxBytes int) *models.WorkspaceResumeBundle {
	if bundle == nil {
		return nil
	}
	data, err := json.Marshal(bundle)
	if err != nil || len(data) <= maxBytes {
		return bundle
	}
	var compact models.WorkspaceResumeBundle
	if err := json.Unmarshal(data, &compact); err != nil {
		return bundle
	}
	if compact.CurrentWork != nil {
		compact.CurrentWork = compactWorkResumeBundle(compact.CurrentWork, maxBytes/2)
	}
	var shortened bool
	compact.Namespace.Name, shortened = truncateWorkResumeString(compact.Namespace.Name, 512)
	compact.Truncated.Core = compact.Truncated.Core || shortened
	compact.Namespace.Description, shortened = truncateWorkResumeString(compact.Namespace.Description, 1024)
	compact.Truncated.Core = compact.Truncated.Core || shortened
	compact.NextAction, shortened = truncateWorkResumeString(compact.NextAction, 2048)
	compact.Truncated.Core = compact.Truncated.Core || shortened
	for index := range compact.GoalTree.Goals {
		goal := &compact.GoalTree.Goals[index]
		goal.Content, shortened = truncateWorkResumeString(goal.Content, 512)
		compact.Truncated.Goals = compact.Truncated.Goals || shortened
		goal.Notes, shortened = truncateWorkResumeString(goal.Notes, 256)
		compact.Truncated.Goals = compact.Truncated.Goals || shortened
	}
	if compact.Worktree != nil {
		compact.Worktree.Repository, shortened = truncateWorkResumeString(compact.Worktree.Repository, 512)
		compact.Truncated.Core = compact.Truncated.Core || shortened
		compact.Worktree.WorktreePath, shortened = truncateWorkResumeString(compact.Worktree.WorktreePath, 1024)
		compact.Truncated.Core = compact.Truncated.Core || shortened
		compact.Worktree.GitDir, shortened = truncateWorkResumeString(compact.Worktree.GitDir, 1024)
		compact.Truncated.Core = compact.Truncated.Core || shortened
		compact.Worktree.WorktreeSlot, shortened = truncateWorkResumeString(compact.Worktree.WorktreeSlot, 256)
		compact.Truncated.Core = compact.Truncated.Core || shortened
		compact.Worktree.Branch, shortened = truncateWorkResumeString(compact.Worktree.Branch, 512)
		compact.Truncated.Core = compact.Truncated.Core || shortened
		compact.Worktree.AgentID, shortened = truncateWorkResumeString(compact.Worktree.AgentID, 512)
		compact.Truncated.Core = compact.Truncated.Core || shortened
		compact.Worktree.Metadata, shortened = compactWorkResumeRawJSON(compact.Worktree.Metadata, 1024)
		compact.Truncated.Core = compact.Truncated.Core || shortened
	}
	if compact.LatestCheckpoint != nil {
		compact.LatestCheckpoint.Summary, shortened = truncateWorkResumeString(compact.LatestCheckpoint.Summary, 2048)
		compact.Truncated.Core = compact.Truncated.Core || shortened
		compact.LatestCheckpoint.Result, shortened = truncateWorkResumeString(compact.LatestCheckpoint.Result, 2048)
		compact.Truncated.Core = compact.Truncated.Core || shortened
		compact.LatestCheckpoint.NextAction, shortened = truncateWorkResumeString(compact.LatestCheckpoint.NextAction, 2048)
		compact.Truncated.Core = compact.Truncated.Core || shortened
	}
	fits := func() bool {
		payload, err := json.Marshal(&compact)
		return err == nil && len(payload) <= maxBytes
	}
	for !fits() {
		switch {
		case len(compact.RecentFailures) > 0:
			compact.RecentFailures = compact.RecentFailures[:len(compact.RecentFailures)-1]
			compact.Truncated.Failures = true
		case len(compact.RecentDecisions) > 0:
			compact.RecentDecisions = compact.RecentDecisions[:len(compact.RecentDecisions)-1]
			compact.Truncated.Decisions = true
		case len(compact.Graph.Worktrees) > 0:
			compact.Graph.Worktrees = compact.Graph.Worktrees[:len(compact.Graph.Worktrees)-1]
			compact.Truncated.Graph = true
		case len(compact.Graph.Edges) > 0:
			compact.Graph.Edges = compact.Graph.Edges[:len(compact.Graph.Edges)-1]
			compact.Truncated.Graph = true
		case len(compact.Graph.Nodes) > 0:
			compact.Graph.Nodes = compact.Graph.Nodes[:len(compact.Graph.Nodes)-1]
			compact.Truncated.Graph = true
		case len(compact.Blocked) > 0:
			compact.Blocked = compact.Blocked[:len(compact.Blocked)-1]
			compact.Truncated.Blocked = true
		case len(compact.Doing) > 0:
			compact.Doing = compact.Doing[:len(compact.Doing)-1]
			compact.Truncated.Doing = true
		case removeLastNonRootGoal(&compact.GoalTree):
			compact.Truncated.Goals = true
		case compact.WorkPlan != nil && len(compact.WorkPlan.Decisions) > 0:
			compact.WorkPlan.Decisions = nil
			compact.Truncated.Core = true
		case compact.WorkPlan != nil && compact.WorkPlan.Validation != nil && len(compact.WorkPlan.Validation.Findings) > 0:
			compact.WorkPlan.Validation.Findings = nil
			compact.Truncated.Core = true
		default:
			removedTask := false
			if compact.WorkPlan != nil {
				for i := len(compact.WorkPlan.Components) - 1; i >= 0; i-- {
					if len(compact.WorkPlan.Components[i].Tasks) == 0 {
						continue
					}
					compact.WorkPlan.Components[i].Tasks = compact.WorkPlan.Components[i].Tasks[:len(compact.WorkPlan.Components[i].Tasks)-1]
					removedTask = true
					break
				}
			}
			if removedTask {
				compact.Truncated.Core = true
				continue
			}
			compact.Truncated.Core = true
			compact.ProjectContext = nil
			compact.WorkPlan = nil
			compact.CurrentWork = nil
			compact.LatestCheckpoint = nil
			compact.Worktree = nil
			compact.Graph = models.WorkGraph{}
			compact.Doing = nil
			compact.Blocked = nil
			compact.RecentDecisions = nil
			compact.RecentFailures = nil
			keepWorkspaceRootGoal(&compact.GoalTree)
			compact.Truncated.Goals = compact.Truncated.Goals || compact.Totals.Goals > len(compact.GoalTree.Goals)
			if len(compact.GoalTree.Goals) == 1 {
				compact.GoalTree.Goals[0].Content, _ = truncateWorkResumeString(compact.GoalTree.Goals[0].Content, 256)
				compact.GoalTree.Goals[0].Notes = ""
			}
			compact.Namespace.Name, _ = truncateWorkResumeString(compact.Namespace.Name, 128)
			compact.Namespace.Description, _ = truncateWorkResumeString(compact.Namespace.Description, 128)
			compact.NextAction, _ = truncateWorkResumeString(compact.NextAction, 256)
			return &compact
		}
	}
	return &compact
}

func workspaceResumeToolResult(bc *bootstrap.Context, bundle *models.WorkspaceResumeBundle) (*mcp.CallToolResult, error) {
	maxBytes := defaultMCPMaxResponseBytes
	if bc != nil && bc.Config != nil && bc.Config.MCPMaxResponseBytes > 0 {
		maxBytes = bc.Config.MCPMaxResponseBytes
	}
	return jsonToolResult(bc, compactWorkspaceResumeBundle(bundle, maxBytes))
}

func registerWorkspaceTools(mcpServer *server.MCPServer, bc *bootstrap.Context) {
	resolveOptions := append([]mcp.ToolOption{
		mcp.WithDescription("Optional Git connector helper. Resolve a checkout already observed by a local bridge; ordinary Web MCP work should start with resume_project instead."),
		mcp.WithString("project_namespace", mcp.Description("Exact project namespace; required only for the first binding")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
	}, workspaceIdentityToolOptions(false)...)
	mcpServer.AddTool(mcp.NewTool("resolve_workspace", resolveOptions...), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		input, err := workspaceIdentityArgument(request)
		if err != nil {
			return nil, err
		}
		allowedIDs, err := authenticatedNamespaceIDs(ctx, bc)
		if err != nil {
			return nil, err
		}
		var targetID *int64
		if raw := strings.TrimSpace(request.GetString("project_namespace", "")); raw != "" {
			_, id, err := exactNamespaceID(ctx, bc, raw)
			if err != nil {
				return nil, err
			}
			targetID = &id
		}
		resolution, err := bc.Brain.ResolveWorkspace(ctx, allowedIDs, targetID, input)
		if err != nil {
			return nil, err
		}
		if err := logicalWorkspaceResolution(ctx, resolution); err != nil {
			return nil, err
		}
		return jsonToolResult(bc, resolution)
	})

	mcpServer.AddTool(mcp.NewTool("resume_workspace",
		mcp.WithDescription("Optional Git-specific project view after resolve_workspace. Ordinary agents should use resume_project and resume_work."),
		mcp.WithString("namespace", mcp.Description("Exact project namespace returned by resolve_workspace"), mcp.Required()),
		mcp.WithNumber("worktree_id", mcp.Description("Optional resolved worktree ID")),
		mcp.WithString("detail", mcp.Description("brief minimizes model input; full includes the complete bounded project snapshot"), mcp.DefaultString("brief"), mcp.Enum("brief", "full")),
		mcp.WithString("known_context_digest", mcp.Description("Digest from the previous brief response; matching state returns only an unchanged receipt"), mcp.Pattern(`sha256:[0-9a-f]{64}`)),
		mcp.WithNumber("recent_limit", mcp.Description("Maximum recent records per collection when detail is full"), mcp.DefaultNumber(8), mcp.Min(1), mcp.Max(100)),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		logicalNamespace := request.GetString("namespace", "")
		physicalNamespace, namespaceID, err := exactNamespaceID(ctx, bc, logicalNamespace)
		if err != nil {
			return nil, err
		}
		worktreeID, err := optionalID(request, "worktree_id")
		if err != nil {
			return nil, err
		}
		if worktreeID != nil {
			worktree, err := authorizeWorktree(ctx, bc, *worktreeID)
			if err != nil {
				return nil, err
			}
			if worktree.NamespaceID != namespaceID {
				return nil, fmt.Errorf("worktree and project namespace must match")
			}
		}
		principalID, err := workExecutionPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		bundle, err := bc.Brain.ResumeWorkspace(
			ctx, physicalNamespace, namespaceID, worktreeID, principalID, request.GetInt("recent_limit", 8),
		)
		if err != nil {
			return nil, err
		}
		bundle.Namespace.Slug = strings.TrimSpace(logicalNamespace)
		if bundle.Namespace.Name == physicalNamespace {
			bundle.Namespace.Name = bundle.Namespace.Slug
		}
		if request.GetString("detail", "brief") == "full" {
			return workspaceResumeToolResult(bc, bundle)
		}
		brief, err := buildWorkspaceResumeBrief(bundle)
		if err != nil {
			return nil, err
		}
		if receipt := matchingAgentContextReceipt(
			request.GetString("known_context_digest", ""), "workspace", namespaceID, brief.NextAction, brief.ContextDigest,
		); receipt != nil {
			return jsonToolResult(bc, receipt)
		}
		return jsonToolResult(bc, brief)
	})

	claimOptions := append([]mcp.ToolOption{
		mcp.WithDescription("Optional Git convenience helper that attaches a checkout while claiming work. Use claim_work when no Git checkout is involved."),
		mcp.WithNumber("work_item_id", mcp.Description("Prepared work item to claim"), mcp.Required()),
		mcp.WithString("project_namespace", mcp.Description("Optional exact project namespace; the work item namespace is authoritative")),
		mcp.WithNumber("lease_seconds", mcp.Description("Lease lifetime from 1 to 86400 seconds"), mcp.DefaultNumber(900)),
		mcp.WithString("action_key", mcp.Description("Unpredictable key containing a fresh random UUIDv4; keep it private and reuse only for this exact retry"), mcp.MinLength(minStartActionKeyChars), mcp.Pattern(startActionUUIDPatternText), mcp.Required()),
		mcp.WithIdempotentHintAnnotation(true),
	}, workspaceIdentityToolOptions(true)...)
	mcpServer.AddTool(mcp.NewTool("claim_workspace", claimOptions...), recordWorkExecutionHandler("claim_workspace", func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		workItemID, err := requiredPositiveID(request, "work_item_id")
		if err != nil {
			return nil, err
		}
		item, err := authorizeWorkItem(ctx, bc, workItemID)
		if err != nil {
			return nil, err
		}
		if raw := strings.TrimSpace(request.GetString("project_namespace", "")); raw != "" {
			_, explicitID, err := exactNamespaceID(ctx, bc, raw)
			if err != nil {
				return nil, err
			}
			if explicitID != item.NamespaceID {
				return nil, fmt.Errorf("work item and project namespace must match")
			}
		}
		input, err := workspaceIdentityArgument(request)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(input.AgentID) == "" {
			return nil, fmt.Errorf("argument %q is required", "agent_id")
		}
		actionKey, err := requiredStartActionKey(request)
		if err != nil {
			return nil, err
		}
		principalID, err := workExecutionPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		leaseDuration, err := workLeaseDuration(request)
		if err != nil {
			return nil, err
		}
		allowedIDs, err := authenticatedNamespaceIDs(ctx, bc)
		if err != nil {
			return nil, err
		}
		targetID := item.NamespaceID
		claim, err := bc.Brain.ClaimWorkspace(ctx, allowedIDs, &targetID, input, workItemID, input.AgentID, principalID, leaseDuration, actionKey)
		if err != nil {
			return nil, err
		}
		if err := logicalWorkspaceResolution(ctx, &claim.Resolution); err != nil {
			return nil, err
		}
		return jsonToolResult(bc, claim)
	}))
}
