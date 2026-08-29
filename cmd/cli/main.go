package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/urfave/cli/v3"
)

func main() {
	cmd := &cli.Command{
		Name:  "stash",
		Usage: "Stash - Persistent Memory for AI",
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			if !commandNeedsBootstrap(os.Args) {
				return ctx, nil
			}
			if _, ok := cmd.Root().Metadata["bootstrapCtx"]; ok {
				return ctx, nil
			}
			bc, err := bootstrap.New(ctx)
			if err != nil {
				return ctx, err
			}
			cmd.Metadata["bootstrapCtx"] = bc
			return ctx, nil
		},
		After: func(ctx context.Context, cmd *cli.Command) error {
			if bc, ok := cmd.Metadata["bootstrapCtx"].(*bootstrap.Context); ok {
				return bc.Close()
			}
			return nil
		},
		Commands: []*cli.Command{
			{
				Name:  "serve",
				Usage: "Start MCP, web console, metrics, health checks, and consolidation",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "host", Value: "0.0.0.0", Usage: "Server host"},
					&cli.StringFlag{Name: "port", Value: "8080", Usage: "Server port"},
					&cli.DurationFlag{Name: "consolidate-interval", Value: 5 * time.Minute, Usage: "Consolidation interval"},
					&cli.StringSliceFlag{Name: "consolidate-namespaces", Usage: "Namespaces to consolidate (default: all)"},
				},
				Action: serveAllCmd,
			},
			{
				Name:   "env",
				Usage:  "Show environment variables and configuration",
				Action: EnvCmd,
			},
			{
				Name:  "namespace",
				Usage: "Manage namespaces",
				Commands: []*cli.Command{
					{
						Name:   "create",
						Usage:  "Create a namespace",
						Action: namespaceCreateCmd,
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "name", Usage: "Display name"},
							&cli.StringFlag{Name: "description", Usage: "Description"},
						},
					},
					{
						Name:   "list",
						Usage:  "List namespaces",
						Action: namespaceListCmd,
						Flags: []cli.Flag{
							&cli.StringSliceFlag{Name: "namespaces", Aliases: []string{"n"}, Usage: "Namespace paths to list (each includes descendants)"},
							&cli.IntFlag{Name: "limit", Value: 100, Usage: "Max results"},
							&cli.IntFlag{Name: "offset", Value: 0, Usage: "Result offset"},
						},
					},
				},
			},
			{
				Name:    "remember",
				Aliases: []string{"add"},
				Usage:   "Store a memory",
				Action:  rememberCmd,
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "namespace", Aliases: []string{"n"}, Value: "/", Usage: "Namespace path"},
					&cli.StringFlag{Name: "occurred-at", Usage: "When it occurred (RFC3339)"},
				},
			},
			{
				Name:    "recall",
				Aliases: []string{"search"},
				Usage:   "Search for relevant memories",
				Action:  recallCmd,
				Flags: []cli.Flag{
					&cli.StringSliceFlag{Name: "namespaces", Aliases: []string{"n"}, Usage: "Namespace paths to search (each includes descendants)"},
					&cli.IntFlag{Name: "limit", Value: 10, Usage: "Max results"},
					&cli.FloatFlag{Name: "min-score", Usage: "Drop vector candidates below this cosine similarity (0..1). 0 disables"},
				},
			},
			{
				Name:   "forget",
				Usage:  "Soft-delete the episode nearest to a query",
				Action: forgetCmd,
				Flags: []cli.Flag{
					&cli.StringSliceFlag{Name: "namespaces", Aliases: []string{"n"}, Usage: "Namespace paths to search (each includes descendants)"},
					&cli.FloatFlag{Name: "min-score", Usage: "Refuse to delete unless similarity reaches this (0..1)"},
					&cli.BoolFlag{Name: "dry-run", Aliases: []string{"d"}, Usage: "Show what would be deleted without deleting"},
				},
			},
			{
				Name:  "purge",
				Usage: "Delete by ID (soft by default; --hard is irreversible)",
				Commands: []*cli.Command{
					{
						Name:   "episode",
						Usage:  "Delete an episode by ID (soft by default)",
						Action: purgeEpisodeCmd,
						Flags: []cli.Flag{
							&cli.BoolFlag{Name: "hard", Usage: "Irreversibly DELETE the row instead of setting deleted_at"},
						},
					},
				},
			},
			{
				Name:   "facts",
				Usage:  "Query facts",
				Action: factsListCmd,
				Flags: []cli.Flag{
					&cli.StringSliceFlag{Name: "namespaces", Aliases: []string{"n"}, Usage: "Namespace paths to query (each includes descendants)"},
					&cli.StringFlag{Name: "since", Usage: "Since timestamp (RFC3339)"},
					&cli.StringFlag{Name: "until", Usage: "Until timestamp (RFC3339)"},
					&cli.IntFlag{Name: "limit", Value: 100, Usage: "Max results"},
					&cli.IntFlag{Name: "offset", Value: 0, Usage: "Result offset"},
				},
			},
			{
				Name:  "consolidate",
				Usage: "Consolidate memories",
				Commands: []*cli.Command{
					{
						Name:   "run",
						Usage:  "Run consolidation once",
						Action: consolidateRunCmd,
						Flags: []cli.Flag{
							&cli.StringSliceFlag{Name: "namespaces", Aliases: []string{"n"}, Usage: "Namespace paths to consolidate (each includes descendants)"},
							&cli.BoolFlag{Name: "dry-run", Aliases: []string{"d"}, Usage: "Show what would be consolidated"},
						},
					},
					{
						Name:   "serve",
						Usage:  "Run consolidation as a background service",
						Action: consolidateServeCmd,
						Flags: []cli.Flag{
							&cli.DurationFlag{Name: "interval", Aliases: []string{"i"}, Value: 5 * time.Minute, Usage: "Interval between runs"},
							&cli.StringSliceFlag{Name: "namespaces", Aliases: []string{"n"}, Usage: "Namespaces to consolidate"},
						},
					},
				},
			},
			{
				Name:  "context",
				Usage: "Manage working context",
				Commands: []*cli.Command{
					{
						Name:   "set",
						Usage:  "Set working context focus",
						Action: contextSetCmd,
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "namespace", Aliases: []string{"n"}, Usage: "Project namespace for the first binding; later syncs use local Git config"},
							&cli.DurationFlag{Name: "expires", Value: 1 * time.Hour, Usage: "Context TTL"},
						},
					},
					{
						Name:   "show",
						Usage:  "Show current context",
						Action: contextShowCmd,
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "namespace", Aliases: []string{"n"}, Value: "/", Usage: "Namespace path"},
						},
					},
				},
			},
			{
				Name:  "contradictions",
				Usage: "Manage contradictions",
				Commands: []*cli.Command{
					{
						Name:   "list",
						Usage:  "List unresolved contradictions",
						Action: contradictionsListCmd,
						Flags: []cli.Flag{
							&cli.StringSliceFlag{Name: "namespaces", Aliases: []string{"n"}, Usage: "Namespace paths to query (each includes descendants)"},
							&cli.IntFlag{Name: "limit", Value: 100, Usage: "Max results"},
							&cli.IntFlag{Name: "offset", Value: 0, Usage: "Result offset"},
						},
					},
					{
						Name:   "resolve",
						Usage:  "Resolve a contradiction by ID",
						Action: contradictionResolveCmd,
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "resolution", Value: "resolved", Usage: "Resolution note"},
						},
					},
				},
			},
			{
				Name:  "causal",
				Usage: "Manage causal links between facts",
				Commands: []*cli.Command{
					{
						Name:   "list",
						Usage:  "List causal links",
						Action: causalListCmd,
						Flags: []cli.Flag{
							&cli.StringSliceFlag{Name: "namespaces", Aliases: []string{"n"}, Usage: "Namespace paths to query (each includes descendants)"},
							&cli.IntFlag{Name: "limit", Value: 100, Usage: "Max results"},
							&cli.IntFlag{Name: "offset", Value: 0, Usage: "Result offset"},
						},
					},
					{
						Name:   "create",
						Usage:  "Create a causal link between two facts",
						Action: causalCreateCmd,
						Flags: []cli.Flag{
							&cli.IntFlag{Name: "cause-id", Usage: "ID of the cause fact"},
							&cli.IntFlag{Name: "effect-id", Usage: "ID of the effect fact"},
							&cli.FloatFlag{Name: "confidence", Value: 0.8, Usage: "Confidence score (0-1)"},
							&cli.StringFlag{Name: "namespace", Aliases: []string{"n"}, Value: "/", Usage: "Namespace path"},
						},
					},
					{
						Name:   "trace",
						Usage:  "Trace causal chain from a fact",
						Action: causalTraceCmd,
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "direction", Value: "forward", Usage: "Trace direction: forward or backward"},
							&cli.IntFlag{Name: "depth", Value: 10, Usage: "Max traversal depth"},
						},
					},
					{
						Name:   "delete",
						Usage:  "Delete a causal link by ID",
						Action: causalDeleteCmd,
					},
				},
			},
			{
				Name:  "hypothesis",
				Usage: "Manage hypotheses",
				Commands: []*cli.Command{
					{
						Name:   "create",
						Usage:  "Create a hypothesis",
						Action: hypothesisCreateCmd,
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "namespace", Aliases: []string{"n"}, Value: "/", Usage: "Namespace path"},
							&cli.StringFlag{Name: "verification-plan", Usage: "How to verify this hypothesis"},
							&cli.FloatFlag{Name: "confidence", Value: 0.5, Usage: "Confidence score (0-1)"},
							&cli.StringFlag{Name: "source-fact-ids", Usage: "Comma-separated fact IDs supporting this hypothesis"},
						},
					},
					{
						Name:   "list",
						Usage:  "List hypotheses",
						Action: hypothesisListCmd,
						Flags: []cli.Flag{
							&cli.StringSliceFlag{Name: "namespaces", Aliases: []string{"n"}, Usage: "Namespace paths to query (each includes descendants)"},
							&cli.StringFlag{Name: "status", Usage: "Filter by status: proposed, testing, confirmed, rejected"},
							&cli.IntFlag{Name: "limit", Value: 100, Usage: "Max results"},
							&cli.IntFlag{Name: "offset", Value: 0, Usage: "Result offset"},
						},
					},
					{
						Name:   "show",
						Usage:  "Show a hypothesis by ID",
						Action: hypothesisShowCmd,
					},
					{
						Name:   "test",
						Usage:  "Mark a hypothesis as testing",
						Action: hypothesisTestCmd,
					},
					{
						Name:   "confirm",
						Usage:  "Confirm a hypothesis and auto-create a fact",
						Action: hypothesisConfirmCmd,
					},
					{
						Name:   "reject",
						Usage:  "Reject a hypothesis",
						Action: hypothesisRejectCmd,
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "reason", Usage: "Rejection reason"},
						},
					},
					{
						Name:   "refine",
						Usage:  "Refine a hypothesis (resets to proposed)",
						Action: hypothesisRefineCmd,
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "content", Usage: "Updated hypothesis content"},
							&cli.StringFlag{Name: "verification-plan", Usage: "Updated verification plan"},
							&cli.FloatFlag{Name: "confidence", Usage: "Updated confidence score"},
						},
					},
					{
						Name:   "delete",
						Usage:  "Delete a hypothesis",
						Action: hypothesisDeleteCmd,
					},
				},
			},
			{
				Name:  "goal",
				Usage: "Manage goals",
				Commands: []*cli.Command{
					{
						Name:   "create",
						Usage:  "Create a goal",
						Action: goalCreateCmd,
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "namespace", Aliases: []string{"n"}, Value: "/", Usage: "Namespace path"},
							&cli.IntFlag{Name: "parent-id", Usage: "Parent goal ID for sub-goals"},
							&cli.IntFlag{Name: "priority", Value: 0, Usage: "Priority (higher = more important)"},
						},
					},
					{
						Name:   "list",
						Usage:  "List goals",
						Action: goalListCmd,
						Flags: []cli.Flag{
							&cli.StringSliceFlag{Name: "namespaces", Aliases: []string{"n"}, Usage: "Namespace paths to query (each includes descendants)"},
							&cli.StringFlag{Name: "status", Usage: "Filter by status: active, completed, abandoned"},
							&cli.IntFlag{Name: "parent-id", Usage: "Filter by parent goal ID"},
							&cli.IntFlag{Name: "limit", Value: 100, Usage: "Max results"},
							&cli.IntFlag{Name: "offset", Value: 0, Usage: "Result offset"},
						},
					},
					{
						Name:   "show",
						Usage:  "Show a goal with sub-goal progress",
						Action: goalShowCmd,
					},
					{
						Name:   "complete",
						Usage:  "Complete a goal",
						Action: goalCompleteCmd,
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "notes", Usage: "Completion notes"},
						},
					},
					{
						Name:   "abandon",
						Usage:  "Abandon a goal",
						Action: goalAbandonCmd,
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "notes", Usage: "Abandonment reason"},
						},
					},
					{
						Name:   "update",
						Usage:  "Update an active goal",
						Action: goalUpdateCmd,
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "content", Usage: "Updated goal content"},
							&cli.IntFlag{Name: "priority", Usage: "Updated priority"},
							&cli.StringFlag{Name: "notes", Usage: "Updated notes"},
						},
					},
					{
						Name:   "delete",
						Usage:  "Delete a goal and its sub-goals",
						Action: goalDeleteCmd,
					},
				},
			},
			{
				Name:  "failure",
				Usage: "Manage failure records",
				Commands: []*cli.Command{
					{
						Name:   "create",
						Usage:  "Record a failure",
						Action: failureCreateCmd,
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "namespace", Aliases: []string{"n"}, Value: "/", Usage: "Namespace path"},
							&cli.StringFlag{Name: "reason", Usage: "Why it failed (required)"},
							&cli.StringFlag{Name: "lesson", Usage: "What to do instead (required)"},
							&cli.IntFlag{Name: "goal-id", Usage: "Related goal ID"},
						},
					},
					{
						Name:   "list",
						Usage:  "List failures",
						Action: failureListCmd,
						Flags: []cli.Flag{
							&cli.StringSliceFlag{Name: "namespaces", Aliases: []string{"n"}, Usage: "Namespace paths to query (each includes descendants)"},
							&cli.IntFlag{Name: "goal-id", Usage: "Filter by goal ID"},
							&cli.IntFlag{Name: "limit", Value: 100, Usage: "Max results"},
							&cli.IntFlag{Name: "offset", Value: 0, Usage: "Result offset"},
						},
					},
					{
						Name:   "show",
						Usage:  "Show a failure by ID",
						Action: failureShowCmd,
					},
					{
						Name:   "delete",
						Usage:  "Delete a failure",
						Action: failureDeleteCmd,
					},
				},
			},
			{
				Name:  "issue",
				Usage: "Create and track local issues",
				Commands: []*cli.Command{
					{
						Name:   "create",
						Usage:  "Create an issue",
						Action: issueCreateCmd,
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "namespace", Aliases: []string{"n"}, Value: "/", Usage: "Namespace path"},
							&cli.StringFlag{Name: "type", Value: "task", Usage: "Issue type: task, bug, feature, chore, question"},
							&cli.StringFlag{Name: "labels", Usage: "Comma-separated labels"},
							&cli.StringFlag{Name: "description", Usage: "Issue description"},
							&cli.StringFlag{Name: "status", Value: "backlog", Usage: "backlog, ready, doing, blocked, review, done, canceled"},
							&cli.IntFlag{Name: "priority", Usage: "Priority"},
							&cli.FloatFlag{Name: "position", Usage: "Board position"},
							&cli.StringFlag{Name: "owner", Usage: "Assignee"},
							&cli.StringFlag{Name: "reporter", Usage: "Reporter"},
							&cli.StringFlag{Name: "due-at", Usage: "Deadline (RFC3339)"},
							&cli.IntFlag{Name: "goal-id", Usage: "Related goal ID"},
							&cli.IntFlag{Name: "parent-id", Usage: "Parent issue ID"},
						},
					},
					{
						Name:   "list",
						Usage:  "List issues",
						Action: issueListCmd,
						Flags: []cli.Flag{
							&cli.StringSliceFlag{Name: "namespaces", Aliases: []string{"n"}, Usage: "Namespace paths"},
							&cli.StringFlag{Name: "type", Usage: "Filter by issue type"},
							&cli.StringFlag{Name: "status", Usage: "Filter by status"},
							&cli.StringFlag{Name: "label", Usage: "Filter by label"},
							&cli.StringFlag{Name: "q", Usage: "Search key, title, or description"},
							&cli.IntFlag{Name: "worktree-id", Usage: "Filter by registered worktree"},
							&cli.IntFlag{Name: "limit", Value: 100, Usage: "Max results"},
							&cli.IntFlag{Name: "offset", Usage: "Result offset"},
						},
					},
					{
						Name:   "show",
						Usage:  "Show an issue and its comments",
						Action: issueShowCmd,
					},
					{
						Name:   "update",
						Usage:  "Update an issue",
						Action: issueUpdateCmd,
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "title"}, &cli.StringFlag{Name: "description"},
							&cli.StringFlag{Name: "type"}, &cli.StringFlag{Name: "labels"}, &cli.StringFlag{Name: "reporter"},
							&cli.StringFlag{Name: "status"}, &cli.IntFlag{Name: "priority"}, &cli.FloatFlag{Name: "position"},
							&cli.StringFlag{Name: "owner"}, &cli.StringFlag{Name: "due-at"},
						},
					},
					{
						Name:   "delete",
						Usage:  "Delete an issue and its child issues",
						Action: issueDeleteCmd,
					},
					{
						Name:  "comment",
						Usage: "Manage issue comments",
						Commands: []*cli.Command{
							{
								Name:   "add",
								Usage:  "Add a comment",
								Action: issueCommentAddCmd,
								Flags: []cli.Flag{
									&cli.StringFlag{Name: "body", Usage: "Comment body"},
									&cli.StringFlag{Name: "author", Usage: "Author (defaults to STASH_AGENT_ID)"},
								},
							},
							{
								Name:   "list",
								Usage:  "List comments",
								Action: issueCommentListCmd,
								Flags: []cli.Flag{
									&cli.IntFlag{Name: "limit", Value: 100, Usage: "Max results"},
									&cli.IntFlag{Name: "offset", Usage: "Result offset"},
								},
							},
						},
					},
				},
			},
			{
				Name:  "workspace",
				Usage: "Inspect the local Git identity used by workspace-aware agents",
				Commands: []*cli.Command{
					{
						Name:   "facts",
						Usage:  "Print optional Git connector facts without opening the database",
						Action: workspaceFactsCmd,
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "cwd", Value: ".", Usage: "Current directory inside a Git worktree"},
							&cli.StringFlag{Name: "agent-id", Usage: "Agent or session identifier"},
							&cli.StringFlag{Name: "project-namespace", Usage: "Project namespace to bind on first use"},
							&cli.BoolFlag{Name: "no-init", Usage: "Do not create missing local Stash Git config"},
						},
					},
				},
			},
			{
				Name:  "worktree",
				Usage: "Register and inspect Git worktrees",
				Commands: []*cli.Command{
					{
						Name:   "sync",
						Usage:  "Sync local Git worktrees into Stash",
						Action: worktreeSyncCmd,
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "repo", Value: ".", Usage: "Repository or worktree path"},
							&cli.StringFlag{Name: "namespace", Aliases: []string{"n"}, Value: "/", Usage: "Namespace path"},
							&cli.StringFlag{Name: "agent-id", Usage: "Agent or session identifier (defaults to STASH_AGENT_ID)"},
						},
					},
					{
						Name:   "list",
						Usage:  "List registered Git worktrees",
						Action: worktreeListCmd,
						Flags: []cli.Flag{
							&cli.StringSliceFlag{Name: "namespaces", Aliases: []string{"n"}, Usage: "Namespace paths to query"},
							&cli.IntFlag{Name: "limit", Value: 100, Usage: "Max results"},
							&cli.IntFlag{Name: "offset", Value: 0, Usage: "Result offset"},
						},
					},
				},
			},
			{
				Name:   "reindex",
				Usage:  "Re-embed all episodes and facts (content untouched; failures stay queued for retry)",
				Action: reindexCmd,
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "dry-run", Aliases: []string{"d"}, Usage: "Show how many rows would be re-embedded"},
				},
			},
			{
				Name:  "mcp",
				Usage: "MCP server for agent integration",
				Commands: []*cli.Command{
					{
						Name:   "serve",
						Usage:  "Start the HTTP server (MCP on /mcp, SSE on /sse, web console, metrics, and health checks)",
						Action: mcpServeCmd,
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "host", Value: "0.0.0.0"},
							&cli.StringFlag{Name: "port", Value: "8080"},
							&cli.BoolFlag{Name: "with-consolidation", Usage: "Run consolidation in background alongside MCP server"},
							&cli.DurationFlag{Name: "consolidate-interval", Value: 5 * time.Minute, Usage: "Consolidation interval"},
							&cli.StringSliceFlag{Name: "consolidate-namespaces", Usage: "Namespaces to consolidate (default: all)"},
						},
					},
					{
						Name:   "execute",
						Usage:  "Start MCP server over stdio",
						Action: mcpExecuteCmd,
						Flags: []cli.Flag{
							&cli.BoolFlag{Name: "with-consolidation", Usage: "Run consolidation in background alongside MCP server"},
							&cli.DurationFlag{Name: "consolidate-interval", Value: 5 * time.Minute, Usage: "Consolidation interval"},
							&cli.StringSliceFlag{Name: "consolidate-namespaces", Usage: "Namespaces to consolidate (default: all)"},
						},
					},
				},
			},
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}
