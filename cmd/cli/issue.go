package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alash3al/stash/internal/brain"
	"github.com/alash3al/stash/internal/models"
	"github.com/urfave/cli/v3"
)

func issueLabels(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '\n' })
	labels := make([]string, 0, len(parts))
	for _, part := range parts {
		if label := strings.TrimSpace(part); label != "" {
			labels = append(labels, label)
		}
	}
	return labels
}

func issueDueAt(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("due-at must be RFC3339: %w", err)
	}
	return &parsed, nil
}

func issueIDOrKey(ctx context.Context, bc brainBootstrap, raw string) (*models.WorkItem, error) {
	if id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && id > 0 {
		return bc.GetWorkItem(ctx, id)
	}
	return bc.GetWorkItemByKey(ctx, raw)
}

// brainBootstrap is the small subset of Brain used by issue command helpers.
// Keeping this interface makes the ID/key parsing independent from CLI wiring.
type brainBootstrap interface {
	GetWorkItem(context.Context, int64) (*models.WorkItem, error)
	GetWorkItemByKey(context.Context, string) (*models.WorkItem, error)
}

func issueCreateCmd(ctx context.Context, cmd *cli.Command) error {
	if cmd.Args().Len() == 0 {
		return fmt.Errorf("issue title is required")
	}
	bc := getBootstrap(cmd)
	ns, err := bc.Brain.GetNamespace(ctx, cmd.String("namespace"))
	if err != nil {
		return err
	}
	goalID := optionalCLIID(cmd.Int("goal-id"))
	parentID := optionalCLIID(cmd.Int("parent-id"))
	dueAt, err := issueDueAt(cmd.String("due-at"))
	if err != nil {
		return err
	}
	item, err := bc.Brain.CreateWorkItemWithDetails(ctx, ns.ID, brain.WorkItemInput{
		GoalID: goalID, ParentID: parentID, IssueType: cmd.String("type"), Labels: issueLabels(cmd.String("labels")),
		Reporter: cmd.String("reporter"), Title: cmd.Args().First(), Description: cmd.String("description"),
		Status: cmd.String("status"), Priority: cmd.Int("priority"), Position: cmd.Float("position"),
		Owner: cmd.String("owner"), DueAt: dueAt,
	})
	if err != nil {
		return err
	}
	return printJSON(item)
}

func issueListCmd(ctx context.Context, cmd *cli.Command) error {
	namespaces := cmd.StringSlice("namespaces")
	if len(namespaces) == 0 {
		namespaces = []string{"/"}
	}
	var worktreeID *int64
	if value := cmd.Int("worktree-id"); value > 0 {
		worktreeID = optionalCLIID(value)
	}
	bc := getBootstrap(cmd)
	items, err := bc.Brain.ListWorkItemsFiltered(ctx, namespaces, cmd.String("status"), cmd.String("type"), cmd.String("label"), cmd.String("q"), worktreeID, brain.Pagination{
		Limit: cmd.Int("limit"), Offset: cmd.Int("offset"),
	})
	if err != nil {
		return err
	}
	return printJSON(items)
}

func issueShowCmd(ctx context.Context, cmd *cli.Command) error {
	if cmd.Args().Len() == 0 {
		return fmt.Errorf("issue ID or key is required")
	}
	bc := getBootstrap(cmd)
	item, err := issueIDOrKey(ctx, bc.Brain, cmd.Args().First())
	if err != nil {
		return err
	}
	comments, err := bc.Brain.ListWorkItemComments(ctx, item.ID, brain.Pagination{Limit: 100})
	if err != nil {
		return err
	}
	links, err := bc.Brain.ListWorkItemMemoryLinks(ctx, item.ID)
	if err != nil {
		return err
	}
	return printJSON(map[string]any{"issue": item, "comments": comments, "memory_links": links})
}

func issueUpdateCmd(ctx context.Context, cmd *cli.Command) error {
	if cmd.Args().Len() == 0 {
		return fmt.Errorf("issue ID or key is required")
	}
	bc := getBootstrap(cmd)
	current, err := issueIDOrKey(ctx, bc.Brain, cmd.Args().First())
	if err != nil {
		return err
	}
	input := brain.WorkItemInput{
		IssueType: current.IssueType, Labels: current.Labels, Reporter: current.Reporter,
		Title: current.Title, Description: current.Description, Status: current.Status,
		Priority: current.Priority, Position: current.Position, Owner: current.Owner, DueAt: current.DueAt,
	}
	if cmd.IsSet("title") {
		input.Title = cmd.String("title")
	}
	if cmd.IsSet("description") {
		input.Description = cmd.String("description")
	}
	if cmd.IsSet("type") {
		input.IssueType = cmd.String("type")
	}
	if cmd.IsSet("labels") {
		input.Labels = issueLabels(cmd.String("labels"))
	}
	if cmd.IsSet("reporter") {
		input.Reporter = cmd.String("reporter")
	}
	if cmd.IsSet("status") {
		input.Status = cmd.String("status")
	}
	if cmd.IsSet("priority") {
		input.Priority = cmd.Int("priority")
	}
	if cmd.IsSet("position") {
		input.Position = cmd.Float("position")
	}
	if cmd.IsSet("owner") {
		input.Owner = cmd.String("owner")
	}
	if cmd.IsSet("due-at") {
		input.DueAt, err = issueDueAt(cmd.String("due-at"))
		if err != nil {
			return err
		}
	}
	item, err := bc.Brain.UpdateWorkItemWithDetails(ctx, current.ID, input)
	if err != nil {
		return err
	}
	return printJSON(item)
}

func issueDeleteCmd(ctx context.Context, cmd *cli.Command) error {
	if cmd.Args().Len() == 0 {
		return fmt.Errorf("issue ID or key is required")
	}
	bc := getBootstrap(cmd)
	item, err := issueIDOrKey(ctx, bc.Brain, cmd.Args().First())
	if err != nil {
		return err
	}
	if err := bc.Brain.DeleteWorkItem(ctx, item.ID); err != nil {
		return err
	}
	return printJSON(map[string]any{"ok": true, "issue_key": item.IssueKey, "id": item.ID})
}

func issueCommentAddCmd(ctx context.Context, cmd *cli.Command) error {
	if cmd.Args().Len() == 0 {
		return fmt.Errorf("issue ID or key is required")
	}
	body := strings.TrimSpace(cmd.String("body"))
	if body == "" && cmd.Args().Len() > 1 {
		body = strings.TrimSpace(strings.Join(cmd.Args().Slice()[1:], " "))
	}
	if body == "" {
		return fmt.Errorf("comment body is required")
	}
	bc := getBootstrap(cmd)
	item, err := issueIDOrKey(ctx, bc.Brain, cmd.Args().First())
	if err != nil {
		return err
	}
	author := strings.TrimSpace(cmd.String("author"))
	if author == "" {
		author = strings.TrimSpace(os.Getenv("STASH_AGENT_ID"))
	}
	comment, err := bc.Brain.CreateWorkItemComment(ctx, item.ID, author, body)
	if err != nil {
		return err
	}
	return printJSON(comment)
}

func issueCommentListCmd(ctx context.Context, cmd *cli.Command) error {
	if cmd.Args().Len() == 0 {
		return fmt.Errorf("issue ID or key is required")
	}
	bc := getBootstrap(cmd)
	item, err := issueIDOrKey(ctx, bc.Brain, cmd.Args().First())
	if err != nil {
		return err
	}
	comments, err := bc.Brain.ListWorkItemComments(ctx, item.ID, brain.Pagination{Limit: cmd.Int("limit"), Offset: cmd.Int("offset")})
	if err != nil {
		return err
	}
	return printJSON(comments)
}

func optionalCLIID(value int) *int64 {
	if value <= 0 {
		return nil
	}
	result := int64(value)
	return &result
}
