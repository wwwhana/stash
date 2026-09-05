package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/alash3al/stash/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrWorkItemNotFound    = fmt.Errorf("brain: work item not found")
	ErrWorktreeNotFound    = fmt.Errorf("brain: worktree not found")
	ErrWorkEdgeNotFound    = fmt.Errorf("brain: work item edge not found")
	ErrWorkItemCycle       = fmt.Errorf("brain: work item dependency would create a cycle")
	ErrWorkPlanManagedItem = fmt.Errorf("brain: work plan items must be changed with the work plan API")
)

const workItemColumns = `id, namespace_id, goal_id, parent_id, issue_key, issue_type, labels, reporter, title, description,
 status, priority, position, owner, due_at, started_at, completed_at,
 created_at, updated_at, deleted_at`

const workItemCommentColumns = `id, work_item_id, author, body, created_at, updated_at, deleted_at`

const worktreeColumns = `id, namespace_id, workspace_repository_id, worktree_key,
 repository, worktree_path, git_dir, worktree_slot, branch, head_sha, status, agent_id,
 last_seen_at, stale_at, missing_since, removed_at, metadata, created_at, updated_at, deleted_at`

var workItemStatuses = map[string]struct{}{
	"backlog":  {},
	"ready":    {},
	"doing":    {},
	"blocked":  {},
	"review":   {},
	"done":     {},
	"canceled": {},
}

var workItemTypes = map[string]struct{}{
	"task":      {},
	"bug":       {},
	"feature":   {},
	"chore":     {},
	"question":  {},
	"component": {},
}

var worktreeStatuses = map[string]struct{}{
	"unknown": {},
	"clean":   {},
	"dirty":   {},
	"stale":   {},
	"missing": {},
	"merged":  {},
	"removed": {},
}

var workMemoryTypes = map[string]struct{}{
	"episode":    {},
	"fact":       {},
	"hypothesis": {},
	"failure":    {},
	"goal":       {},
}

var workMemoryRelations = map[string]struct{}{
	"context":    {},
	"constraint": {},
	"decision":   {},
	"evidence":   {},
	"failure":    {},
	"result":     {},
	"supersedes": {},
}

func validateWorkItemStatus(status string) error {
	if _, ok := workItemStatuses[status]; !ok {
		return fmt.Errorf("brain: invalid work item status %q", status)
	}
	return nil
}

func validateWorkItemType(issueType string) error {
	if _, ok := workItemTypes[issueType]; !ok {
		return fmt.Errorf("brain: invalid work item type %q", issueType)
	}
	return nil
}

// normalizeWorkGoalDBError turns the final database guard into a stable
// domain error. Validation happens before a write, but a goal can be
// abandoned or moved by another request between those two statements.
func normalizeWorkGoalDBError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		return err
	}
	message := strings.ToLower(pgErr.Message)
	if !strings.Contains(message, "work goal") && !strings.Contains(message, "active work must belong") {
		return err
	}
	return fmt.Errorf("%w: %s", ErrWorkGoalInvalid, pgErr.Message)
}

func normalizeWorkItemLabels(labels []string) ([]string, error) {
	result := make([]string, 0, len(labels))
	seen := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		if len(label) > 64 {
			return nil, fmt.Errorf("brain: work item label is too long")
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		result = append(result, label)
		if len(result) > 32 {
			return nil, fmt.Errorf("brain: work item has too many labels (max 32)")
		}
	}
	return result, nil
}

func validateWorktreeStatus(status string) error {
	if _, ok := worktreeStatuses[status]; !ok {
		return fmt.Errorf("brain: invalid worktree status %q", status)
	}
	return nil
}

func validatePosition(position float64) error {
	if math.IsNaN(position) || math.IsInf(position, 0) {
		return fmt.Errorf("brain: work item position must be finite")
	}
	return nil
}

func scanWorkItem(row pgx.Row) (*models.WorkItem, error) {
	var item models.WorkItem
	err := row.Scan(
		&item.ID, &item.NamespaceID, &item.GoalID, &item.ParentID,
		&item.IssueKey, &item.IssueType, &item.Labels, &item.Reporter,
		&item.Title, &item.Description, &item.Status, &item.Priority,
		&item.Position, &item.Owner, &item.DueAt, &item.StartedAt,
		&item.CompletedAt, &item.CreatedAt, &item.UpdatedAt, &item.DeletedAt,
	)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func scanWorkItemRows(rows pgx.Rows) ([]models.WorkItem, error) {
	defer rows.Close()
	items := make([]models.WorkItem, 0)
	for rows.Next() {
		var item models.WorkItem
		if err := rows.Scan(
			&item.ID, &item.NamespaceID, &item.GoalID, &item.ParentID,
			&item.IssueKey, &item.IssueType, &item.Labels, &item.Reporter,
			&item.Title, &item.Description, &item.Status, &item.Priority,
			&item.Position, &item.Owner, &item.DueAt, &item.StartedAt,
			&item.CompletedAt, &item.CreatedAt, &item.UpdatedAt, &item.DeletedAt,
		); err != nil {
			return nil, fmt.Errorf("scan work item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read work items: %w", err)
	}
	return items, nil
}

func scanWorktree(row pgx.Row) (*models.Worktree, error) {
	var worktree models.Worktree
	err := row.Scan(
		&worktree.ID, &worktree.NamespaceID, &worktree.WorkspaceRepositoryID, &worktree.WorktreeKey,
		&worktree.Repository, &worktree.WorktreePath, &worktree.GitDir, &worktree.WorktreeSlot,
		&worktree.Branch, &worktree.HeadSHA, &worktree.Status, &worktree.AgentID,
		&worktree.LastSeenAt, &worktree.StaleAt, &worktree.MissingSince, &worktree.RemovedAt,
		&worktree.Metadata, &worktree.CreatedAt, &worktree.UpdatedAt, &worktree.DeletedAt,
	)
	if err != nil {
		return nil, err
	}
	return &worktree, nil
}

func scanWorktreeRows(rows pgx.Rows) ([]models.Worktree, error) {
	defer rows.Close()
	worktrees := make([]models.Worktree, 0)
	for rows.Next() {
		var worktree models.Worktree
		if err := rows.Scan(
			&worktree.ID, &worktree.NamespaceID, &worktree.WorkspaceRepositoryID, &worktree.WorktreeKey,
			&worktree.Repository, &worktree.WorktreePath, &worktree.GitDir, &worktree.WorktreeSlot,
			&worktree.Branch, &worktree.HeadSHA, &worktree.Status, &worktree.AgentID,
			&worktree.LastSeenAt, &worktree.StaleAt, &worktree.MissingSince, &worktree.RemovedAt,
			&worktree.Metadata, &worktree.CreatedAt, &worktree.UpdatedAt, &worktree.DeletedAt,
		); err != nil {
			return nil, fmt.Errorf("scan worktree: %w", err)
		}
		worktrees = append(worktrees, worktree)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read worktrees: %w", err)
	}
	return worktrees, nil
}

func validateWorkReferences(ctx context.Context, queryer workItemRowWriter, namespaceID int64, goalID, parentID *int64) error {
	if goalID != nil {
		var goalNamespace int64
		var status string
		err := queryer.QueryRow(ctx,
			`SELECT namespace_id, status FROM goals WHERE id = $1 AND deleted_at IS NULL`, *goalID,
		).Scan(&goalNamespace, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("brain: goal %d not found", *goalID)
		}
		if err != nil {
			return fmt.Errorf("check work item goal: %w", err)
		}
		if goalNamespace != namespaceID {
			return fmt.Errorf("brain: work item goal must share the target namespace")
		}
		if status != "active" {
			return fmt.Errorf("brain: work item goal must be active")
		}
	}
	if parentID != nil {
		var parentNamespace int64
		var parentStatus string
		err := queryer.QueryRow(ctx,
			`SELECT namespace_id, status FROM work_items WHERE id = $1 AND deleted_at IS NULL`, *parentID,
		).Scan(&parentNamespace, &parentStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("brain: parent work item %d not found", *parentID)
		}
		if err != nil {
			return fmt.Errorf("check parent work item: %w", err)
		}
		if parentNamespace != namespaceID {
			return fmt.Errorf("brain: parent work item must share the target namespace")
		}
		if parentStatus == "done" || parentStatus == "canceled" {
			return fmt.Errorf("brain: parent work item must not be terminal")
		}
	}
	return nil
}

// WorkItemInput contains the mutable fields used to create or update an issue.
type WorkItemInput struct {
	GoalID      *int64
	ParentID    *int64
	IssueType   string
	Labels      []string
	Reporter    string
	Title       string
	Description string
	Status      string
	Priority    int
	Position    float64
	Owner       string
	DueAt       *time.Time
}

type workItemRowWriter interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func normalizeWorkItemInput(input WorkItemInput) (WorkItemInput, error) {
	if input.IssueType == "" {
		input.IssueType = "task"
	}
	labels, err := normalizeWorkItemLabels(input.Labels)
	if err != nil {
		return WorkItemInput{}, err
	}
	if err := validateWorkItemType(input.IssueType); err != nil {
		return WorkItemInput{}, err
	}
	if err := validateContent(input.Title); err != nil {
		return WorkItemInput{}, fmt.Errorf("brain: work item title: %w", err)
	}
	if len(input.Description) > maxContentLen {
		return WorkItemInput{}, ErrContentTooLong
	}
	if err := validateWorkItemStatus(input.Status); err != nil {
		return WorkItemInput{}, err
	}
	if err := validatePosition(input.Position); err != nil {
		return WorkItemInput{}, err
	}
	input.Labels = labels
	input.Reporter = strings.TrimSpace(input.Reporter)
	input.Owner = strings.TrimSpace(input.Owner)
	return input, nil
}

func (b *Brain) insertWorkItem(ctx context.Context, writer workItemRowWriter, namespaceID int64, input WorkItemInput) (*models.WorkItem, error) {
	input, err := normalizeWorkItemInput(input)
	if err != nil {
		return nil, err
	}
	if err := validateWorkReferences(ctx, writer, namespaceID, input.GoalID, input.ParentID); err != nil {
		return nil, err
	}

	item, err := scanWorkItem(writer.QueryRow(ctx,
		`WITH next_id AS (
			SELECT nextval(pg_get_serial_sequence('work_items', 'id')) AS id
		)
		INSERT INTO work_items (id, namespace_id, goal_id, parent_id, issue_key, issue_type, labels, reporter, title, description, status, priority, position, owner, due_at, started_at, completed_at)
		SELECT id, $1, $2, $3, 'W-' || lpad(id::text, 6, '0'), $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
		       CASE WHEN $9 = 'doing' THEN now() ELSE NULL END,
		       CASE WHEN $9 = 'done' THEN now() ELSE NULL END
		FROM next_id
		RETURNING `+workItemColumns,
		namespaceID, input.GoalID, input.ParentID, input.IssueType, input.Labels, input.Reporter,
		input.Title, input.Description, input.Status, input.Priority, input.Position, input.Owner, input.DueAt,
	))
	if err != nil {
		return nil, normalizeWorkGoalDBError(err)
	}
	return item, nil
}

// CreateWorkItem creates an operational task under an optional goal or work item.
// It remains as a small compatibility wrapper for callers that do not use issue fields.
func (b *Brain) CreateWorkItem(ctx context.Context, namespaceID int64, goalID, parentID *int64, title, description, status string, priority int, position float64, owner string, dueAt *time.Time) (*models.WorkItem, error) {
	return b.CreateWorkItemWithDetails(ctx, namespaceID, WorkItemInput{
		GoalID: goalID, ParentID: parentID, IssueType: "task", Title: title,
		Description: description, Status: status, Priority: priority, Position: position,
		Owner: owner, DueAt: dueAt,
	})
}

// CreateWorkItemWithDetails creates an issue/task with tracker metadata.
func (b *Brain) CreateWorkItemWithDetails(ctx context.Context, namespaceID int64, input WorkItemInput) (*models.WorkItem, error) {
	return b.CreateWorkItemWithCapabilities(ctx, namespaceID, input, nil)
}

func (b *Brain) createWorkItemWithCapabilities(ctx context.Context, namespaceID int64, input WorkItemInput, capabilities []string) (*models.WorkItem, error) {
	if err := b.ensureOrdinaryWorkItem(ctx, input); err != nil {
		return nil, err
	}
	var inheritedGoalID *int64
	if input.ParentID != nil {
		parent, err := b.GetWorkItem(ctx, *input.ParentID)
		if err != nil {
			return nil, err
		}
		if parent.NamespaceID != namespaceID {
			return nil, fmt.Errorf("brain: parent work item must share the target namespace")
		}
		inheritedGoalID = parent.GoalID
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin work item creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	resolvedGoalID, err := b.resolveProjectGoalForWorkTx(ctx, tx, namespaceID, input.GoalID, inheritedGoalID)
	if err != nil {
		return nil, err
	}
	input.GoalID = resolvedGoalID
	item, err := b.insertWorkItem(ctx, tx, namespaceID, input)
	if err != nil {
		return nil, err
	}
	if err := setWorkCapabilitiesTx(ctx, tx, item.ID, capabilities); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit work item creation: %w", err)
	}
	items, err := b.attachWorktreeIDs(ctx, []models.WorkItem{*item})
	if err != nil {
		return nil, err
	}
	return &items[0], nil
}

// ListWorkItems lists active work items for namespaces, optionally by status or worktree.
func (b *Brain) ListWorkItems(ctx context.Context, namespaceSlugs []string, status string, worktreeID *int64, page Pagination) ([]models.WorkItem, error) {
	return b.ListWorkItemsFiltered(ctx, namespaceSlugs, status, "", "", "", worktreeID, page)
}

// ListWorkItemsFiltered lists issues with optional status, type, label, text,
// and worktree filters. The text filter searches the key, title, body, labels,
// owner, reporter, type, and status so one board search covers the visible
// issue metadata.
func (b *Brain) ListWorkItemsFiltered(ctx context.Context, namespaceSlugs []string, status, issueType, label, textQuery string, worktreeID *int64, page Pagination) ([]models.WorkItem, error) {
	nsIDs, err := b.resolveNamespaceIDs(ctx, namespaceSlugs)
	if err != nil {
		return nil, err
	}
	if status != "" {
		if err := validateWorkItemStatus(status); err != nil {
			return nil, err
		}
	}
	if issueType != "" {
		if err := validateWorkItemType(issueType); err != nil {
			return nil, err
		}
	}
	label = strings.TrimSpace(label)
	page = b.sanitizePage(page)

	query := `SELECT ` + workItemColumns + ` FROM work_items
		WHERE namespace_id = ANY($1) AND deleted_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM work_plan_items pi WHERE pi.work_item_id = work_items.id)`
	args := []any{nsIDs}
	arg := 2
	if status != "" {
		query += fmt.Sprintf(" AND status = $%d", arg)
		args = append(args, status)
		arg++
	}
	if issueType != "" {
		query += fmt.Sprintf(" AND issue_type = $%d", arg)
		args = append(args, issueType)
		arg++
	}
	if label != "" {
		query += fmt.Sprintf(" AND $%d = ANY(labels)", arg)
		args = append(args, label)
		arg++
	}
	for _, token := range searchTextTokens(textQuery) {
		pattern := "%" + escapeLikePattern(token) + "%"
		query += fmt.Sprintf(" AND (issue_key ILIKE $%d ESCAPE '\\' OR title ILIKE $%d ESCAPE '\\' OR description ILIKE $%d ESCAPE '\\' OR COALESCE(array_to_string(labels, ' '), '') ILIKE $%d ESCAPE '\\' OR owner ILIKE $%d ESCAPE '\\' OR reporter ILIKE $%d ESCAPE '\\' OR issue_type ILIKE $%d ESCAPE '\\' OR status ILIKE $%d ESCAPE '\\')", arg, arg, arg, arg, arg, arg, arg, arg)
		args = append(args, pattern)
		arg++
	}
	if worktreeID != nil {
		query += fmt.Sprintf(" AND EXISTS (SELECT 1 FROM work_item_worktrees wiwt WHERE wiwt.work_item_id = work_items.id AND wiwt.worktree_id = $%d)", arg)
		args = append(args, *worktreeID)
		arg++
	}
	query += fmt.Sprintf(" ORDER BY priority DESC, position, id LIMIT $%d OFFSET $%d", arg, arg+1)
	args = append(args, page.Limit, page.Offset)
	rows, err := b.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list work items: %w", err)
	}
	items, err := scanWorkItemRows(rows)
	if err != nil {
		return nil, err
	}
	return b.attachWorktreeIDs(ctx, items)
}

// GetWorkItem returns one active work item.
func (b *Brain) GetWorkItem(ctx context.Context, id int64) (*models.WorkItem, error) {
	item, err := scanWorkItem(b.pool.QueryRow(ctx,
		`SELECT `+workItemColumns+` FROM work_items WHERE id = $1 AND deleted_at IS NULL`, id,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWorkItemNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get work item: %w", err)
	}
	items, err := b.attachWorktreeIDs(ctx, []models.WorkItem{*item})
	if err != nil {
		return nil, err
	}
	return &items[0], nil
}

// GetWorkItemByKey returns one active issue by its human-readable key.
func (b *Brain) GetWorkItemByKey(ctx context.Context, issueKey string) (*models.WorkItem, error) {
	issueKey = strings.TrimSpace(issueKey)
	if issueKey == "" {
		return nil, ErrWorkItemNotFound
	}
	item, err := scanWorkItem(b.pool.QueryRow(ctx,
		`SELECT `+workItemColumns+` FROM work_items WHERE issue_key = $1 AND deleted_at IS NULL`, issueKey,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWorkItemNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get work item by key: %w", err)
	}
	items, err := b.attachWorktreeIDs(ctx, []models.WorkItem{*item})
	if err != nil {
		return nil, err
	}
	return &items[0], nil
}

// UpdateWorkItem replaces mutable fields and records lifecycle timestamps.
func (b *Brain) UpdateWorkItem(ctx context.Context, id int64, title, description, status string, priority int, position float64, owner string, dueAt *time.Time) (*models.WorkItem, error) {
	current, err := b.GetWorkItem(ctx, id)
	if err != nil {
		return nil, err
	}
	return b.UpdateWorkItemWithDetails(ctx, id, WorkItemInput{
		IssueType: current.IssueType, Labels: current.Labels, Reporter: current.Reporter,
		Title: title, Description: description, Status: status, Priority: priority,
		Position: position, Owner: owner, DueAt: dueAt,
	})
}

// UpdateWorkItemWithDetails replaces issue fields and records lifecycle timestamps.
func (b *Brain) UpdateWorkItemWithDetails(ctx context.Context, id int64, input WorkItemInput) (*models.WorkItem, error) {
	managed, err := b.isWorkPlanItem(ctx, id)
	if err != nil {
		return nil, err
	}
	if managed {
		return nil, ErrWorkPlanManagedItem
	}
	item, err := b.updateWorkItem(ctx, b.pool, id, input)
	if err != nil {
		return nil, err
	}
	items, err := b.attachWorktreeIDs(ctx, []models.WorkItem{*item})
	if err != nil {
		return nil, err
	}
	return &items[0], nil
}

func (b *Brain) updateWorkItem(ctx context.Context, writer workItemRowWriter, id int64, input WorkItemInput) (*models.WorkItem, error) {
	input, err := normalizeWorkItemInput(input)
	if err != nil {
		return nil, err
	}

	item, err := scanWorkItem(writer.QueryRow(ctx,
		`UPDATE work_items
		 SET issue_type = $2, labels = $3, reporter = $4, title = $5, description = $6, status = $7, priority = $8, position = $9,
		     owner = $10, due_at = $11,
		     started_at = CASE WHEN $7 = 'doing' THEN COALESCE(started_at, now()) ELSE started_at END,
		     completed_at = CASE WHEN $7 = 'done' THEN COALESCE(completed_at, now()) ELSE NULL END,
		     updated_at = now()
		 WHERE id = $1 AND deleted_at IS NULL
		 RETURNING `+workItemColumns,
		id, input.IssueType, input.Labels, input.Reporter, input.Title, input.Description,
		input.Status, input.Priority, input.Position, input.Owner, input.DueAt,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWorkItemNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update work item: %w", normalizeWorkGoalDBError(err))
	}
	return item, nil
}

// updateWorkItemWithGoal updates a plan item and its goal in one statement.
// The goal-map trigger also runs when status changes, so changing the goal in
// a later statement cannot repair a legacy item that already points to an
// inactive or out-of-scope goal.
func (b *Brain) updateWorkItemWithGoal(ctx context.Context, writer workItemRowWriter, id int64, goalID *int64, input WorkItemInput) (*models.WorkItem, error) {
	input, err := normalizeWorkItemInput(input)
	if err != nil {
		return nil, err
	}

	item, err := scanWorkItem(writer.QueryRow(ctx,
		`UPDATE work_items
		 SET goal_id = $2, issue_type = $3, labels = $4, reporter = $5, title = $6, description = $7, status = $8, priority = $9, position = $10,
		     owner = $11, due_at = $12,
		     started_at = CASE WHEN $8 = 'doing' THEN COALESCE(started_at, now()) ELSE started_at END,
		     completed_at = CASE WHEN $8 = 'done' THEN COALESCE(completed_at, now()) ELSE NULL END,
		     updated_at = now()
		 WHERE id = $1 AND deleted_at IS NULL
		 RETURNING `+workItemColumns,
		id, goalID, input.IssueType, input.Labels, input.Reporter, input.Title, input.Description,
		input.Status, input.Priority, input.Position, input.Owner, input.DueAt,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWorkItemNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update work item with goal: %w", normalizeWorkGoalDBError(err))
	}
	return item, nil
}

// DeleteWorkItem soft-deletes a work item and its active descendants atomically.
func (b *Brain) DeleteWorkItem(ctx context.Context, id int64) error {
	managed, err := b.isWorkPlanItem(ctx, id)
	if err != nil {
		return err
	}
	if managed {
		return ErrWorkPlanManagedItem
	}
	return b.deleteWorkItem(ctx, id)
}

func (b *Brain) deleteWorkItem(ctx context.Context, id int64) error {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete work item: %w", err)
	}
	defer tx.Rollback(ctx)

	result, err := tx.Exec(ctx,
		`WITH RECURSIVE work_tree AS (
			SELECT id FROM work_items WHERE id = $1 AND deleted_at IS NULL
			UNION ALL
			SELECT child.id FROM work_items child JOIN work_tree parent ON child.parent_id = parent.id
			WHERE child.deleted_at IS NULL
		)
		UPDATE work_items SET deleted_at = now(), updated_at = now()
		WHERE id IN (SELECT id FROM work_tree) AND deleted_at IS NULL`, id,
	)
	if err != nil {
		return fmt.Errorf("delete work item: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrWorkItemNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete work item: %w", err)
	}
	return nil
}

func (b *Brain) ensureOrdinaryWorkItem(ctx context.Context, input WorkItemInput) error {
	if input.IssueType == "component" {
		return ErrWorkPlanManagedItem
	}
	if input.ParentID == nil {
		return nil
	}
	managed, err := b.isWorkPlanItem(ctx, *input.ParentID)
	if err != nil {
		return err
	}
	if managed {
		return ErrWorkPlanManagedItem
	}
	return nil
}

func (b *Brain) isWorkPlanItem(ctx context.Context, id int64) (bool, error) {
	var managed bool
	if err := b.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM work_plan_items WHERE work_item_id = $1)`, id,
	).Scan(&managed); err != nil {
		return false, fmt.Errorf("check work plan item: %w", err)
	}
	return managed, nil
}

// CreateWorkItemComment appends a note to an active issue.
func (b *Brain) CreateWorkItemComment(ctx context.Context, workItemID int64, author, body string) (*models.WorkItemComment, error) {
	if err := validateContent(body); err != nil {
		return nil, fmt.Errorf("brain: work item comment: %w", err)
	}
	if len(author) > maxContentLen {
		return nil, ErrContentTooLong
	}
	if _, err := b.GetWorkItem(ctx, workItemID); err != nil {
		return nil, err
	}
	var comment models.WorkItemComment
	err := b.pool.QueryRow(ctx,
		`INSERT INTO work_item_comments (work_item_id, author, body)
		 VALUES ($1, $2, $3)
		 RETURNING `+workItemCommentColumns,
		workItemID, strings.TrimSpace(author), body,
	).Scan(&comment.ID, &comment.WorkItemID, &comment.Author, &comment.Body,
		&comment.CreatedAt, &comment.UpdatedAt, &comment.DeletedAt)
	if err != nil {
		return nil, fmt.Errorf("create work item comment: %w", err)
	}
	return &comment, nil
}

// ListWorkItemComments returns active issue comments in conversation order.
func (b *Brain) ListWorkItemComments(ctx context.Context, workItemID int64, page Pagination) ([]models.WorkItemComment, error) {
	if _, err := b.GetWorkItem(ctx, workItemID); err != nil {
		return nil, err
	}
	page = b.sanitizePage(page)
	rows, err := b.pool.Query(ctx,
		`SELECT `+workItemCommentColumns+` FROM work_item_comments
		 WHERE work_item_id = $1 AND deleted_at IS NULL
		 ORDER BY created_at, id LIMIT $2 OFFSET $3`, workItemID, page.Limit, page.Offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list work item comments: %w", err)
	}
	defer rows.Close()
	comments := make([]models.WorkItemComment, 0)
	for rows.Next() {
		var comment models.WorkItemComment
		if err := rows.Scan(&comment.ID, &comment.WorkItemID, &comment.Author, &comment.Body,
			&comment.CreatedAt, &comment.UpdatedAt, &comment.DeletedAt); err != nil {
			return nil, fmt.Errorf("scan work item comment: %w", err)
		}
		comments = append(comments, comment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read work item comments: %w", err)
	}
	return comments, nil
}

// AddWorkItemEdge adds a graph edge. Blocks edges are cycle-checked under a
// namespace advisory lock so concurrent agents cannot create a cycle.
func (b *Brain) AddWorkItemEdge(ctx context.Context, namespaceID, fromItemID, toItemID int64, edgeType string) (*models.WorkItemEdge, error) {
	if edgeType != "blocks" && edgeType != "relates_to" {
		return nil, fmt.Errorf("brain: invalid work item edge type %q", edgeType)
	}
	if fromItemID == toItemID {
		return nil, ErrWorkItemCycle
	}

	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin add work item edge: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, namespaceID); err != nil {
		return nil, fmt.Errorf("lock work graph: %w", err)
	}

	var fromNS, toNS int64
	if err := tx.QueryRow(ctx, `SELECT namespace_id FROM work_items WHERE id = $1 AND deleted_at IS NULL`, fromItemID).Scan(&fromNS); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrWorkItemNotFound
		}
		return nil, fmt.Errorf("check source work item: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT namespace_id FROM work_items WHERE id = $1 AND deleted_at IS NULL`, toItemID).Scan(&toNS); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrWorkItemNotFound
		}
		return nil, fmt.Errorf("check target work item: %w", err)
	}
	if fromNS != namespaceID || toNS != namespaceID {
		return nil, fmt.Errorf("brain: work item edge objects must share the target namespace")
	}

	if edgeType == "blocks" {
		var cycle bool
		err := tx.QueryRow(ctx, `WITH RECURSIVE reachable(id) AS (
			SELECT to_item_id FROM work_item_edges
			 WHERE from_item_id = $1 AND edge_type = 'blocks' AND deleted_at IS NULL
			UNION
			SELECT edge.to_item_id FROM work_item_edges edge
			 JOIN reachable r ON edge.from_item_id = r.id
			 WHERE edge.edge_type = 'blocks' AND edge.deleted_at IS NULL
		)
		SELECT EXISTS (SELECT 1 FROM reachable WHERE id = $2)`, toItemID, fromItemID).Scan(&cycle)
		if err != nil {
			return nil, fmt.Errorf("check work graph cycle: %w", err)
		}
		if cycle {
			return nil, ErrWorkItemCycle
		}
	}

	var edge models.WorkItemEdge
	err = tx.QueryRow(ctx,
		`INSERT INTO work_item_edges (namespace_id, from_item_id, to_item_id, edge_type)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, namespace_id, from_item_id, to_item_id, edge_type, created_at, deleted_at`,
		namespaceID, fromItemID, toItemID, edgeType,
	).Scan(&edge.ID, &edge.NamespaceID, &edge.FromItemID, &edge.ToItemID, &edge.EdgeType, &edge.CreatedAt, &edge.DeletedAt)
	if err != nil {
		return nil, fmt.Errorf("create work item edge: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit work item edge: %w", err)
	}
	return &edge, nil
}

// GetWorkItemEdge returns one active graph edge.
func (b *Brain) GetWorkItemEdge(ctx context.Context, id int64) (*models.WorkItemEdge, error) {
	var edge models.WorkItemEdge
	err := b.pool.QueryRow(ctx,
		`SELECT id, namespace_id, from_item_id, to_item_id, edge_type, created_at, deleted_at
		 FROM work_item_edges WHERE id = $1 AND deleted_at IS NULL`, id,
	).Scan(&edge.ID, &edge.NamespaceID, &edge.FromItemID, &edge.ToItemID, &edge.EdgeType, &edge.CreatedAt, &edge.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWorkEdgeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get work item edge: %w", err)
	}
	return &edge, nil
}

// DeleteWorkItemEdge soft-deletes a graph edge.
func (b *Brain) DeleteWorkItemEdge(ctx context.Context, id int64) error {
	result, err := b.pool.Exec(ctx,
		`UPDATE work_item_edges SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL`, id,
	)
	if err != nil {
		return fmt.Errorf("delete work item edge: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrWorkEdgeNotFound
	}
	return nil
}

func scanWorkItemEdges(rows pgx.Rows) ([]models.WorkItemEdge, error) {
	defer rows.Close()
	edges := make([]models.WorkItemEdge, 0)
	for rows.Next() {
		var edge models.WorkItemEdge
		if err := rows.Scan(&edge.ID, &edge.NamespaceID, &edge.FromItemID, &edge.ToItemID, &edge.EdgeType, &edge.CreatedAt, &edge.DeletedAt); err != nil {
			return nil, fmt.Errorf("scan work item edge: %w", err)
		}
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read work item edges: %w", err)
	}
	return edges, nil
}

// GetWorkGraph returns active work nodes, dependency edges, and registered
// worktrees in the requested namespaces.
func (b *Brain) GetWorkGraph(ctx context.Context, namespaceSlugs []string, includeDone bool) (*models.WorkGraph, error) {
	nsIDs, err := b.resolveNamespaceIDs(ctx, namespaceSlugs)
	if err != nil {
		return nil, err
	}
	statusClause := ""
	if !includeDone {
		statusClause = " AND status NOT IN ('done', 'canceled')"
	}
	rows, err := b.pool.Query(ctx, `SELECT `+workItemColumns+` FROM work_items WHERE namespace_id = ANY($1) AND deleted_at IS NULL`+statusClause+` ORDER BY priority DESC, position, id`, nsIDs)
	if err != nil {
		return nil, fmt.Errorf("list work graph items: %w", err)
	}
	nodes, err := scanWorkItemRows(rows)
	if err != nil {
		return nil, err
	}
	nodes, err = b.attachWorktreeIDs(ctx, nodes)
	if err != nil {
		return nil, err
	}

	edgeRows, err := b.pool.Query(ctx,
		`SELECT edge.id, edge.namespace_id, edge.from_item_id, edge.to_item_id, edge.edge_type, edge.created_at, edge.deleted_at
		 FROM work_item_edges edge
		 JOIN work_items source_item ON source_item.id = edge.from_item_id AND source_item.namespace_id = edge.namespace_id AND source_item.deleted_at IS NULL
		 JOIN work_items target_item ON target_item.id = edge.to_item_id AND target_item.namespace_id = edge.namespace_id AND target_item.deleted_at IS NULL
		 WHERE edge.namespace_id = ANY($1) AND edge.deleted_at IS NULL
		 ORDER BY edge.id`, nsIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("list work graph edges: %w", err)
	}
	edges, err := scanWorkItemEdges(edgeRows)
	if err != nil {
		return nil, err
	}
	if !includeDone {
		nodeIDs := make(map[int64]struct{}, len(nodes))
		for _, node := range nodes {
			nodeIDs[node.ID] = struct{}{}
		}
		filtered := edges[:0]
		for _, edge := range edges {
			if _, ok := nodeIDs[edge.FromItemID]; !ok {
				continue
			}
			if _, ok := nodeIDs[edge.ToItemID]; !ok {
				continue
			}
			filtered = append(filtered, edge)
		}
		edges = filtered
	}
	worktrees, err := b.listWorktreesByNamespaceIDs(ctx, nsIDs)
	if err != nil {
		return nil, err
	}
	return &models.WorkGraph{Nodes: nodes, Edges: edges, Worktrees: worktrees}, nil
}

// WorkGraphCursor fixes the graph membership at the first page and records the
// last independently returned node and edge. Callers must treat it as opaque.
type WorkGraphCursor struct {
	SnapshotAt   time.Time `json:"snapshot_at"`
	MaxNodeID    int64     `json:"max_node_id"`
	MaxEdgeID    int64     `json:"max_edge_id"`
	NodeSet      bool      `json:"node_set,omitempty"`
	NodePriority int       `json:"node_priority,omitempty"`
	NodePosition float64   `json:"node_position,omitempty"`
	NodeID       int64     `json:"node_id,omitempty"`
	EdgeID       int64     `json:"edge_id,omitempty"`
}

var ErrWorkGraphCursorStale = fmt.Errorf("brain: work graph continuation is stale; restart get_work_graph")

// GetWorkGraphPage reads bounded node and edge pages directly from PostgreSQL.
// Inserts after the first call are excluded by ID watermarks. Updates or
// deletes inside that fixed range explicitly expire the continuation.
func (b *Brain) GetWorkGraphPage(ctx context.Context, namespaceSlugs []string, includeDone bool, cursor *WorkGraphCursor, nodeLimit, edgeLimit int) (*models.WorkGraph, WorkGraphCursor, bool, bool, error) {
	nsIDs, err := b.resolveNamespaceIDs(ctx, namespaceSlugs)
	if err != nil {
		return nil, WorkGraphCursor{}, false, false, err
	}
	nodeLimit = Pagination{Limit: nodeLimit}.Sanitize().Limit
	edgeLimit = Pagination{Limit: edgeLimit}.Sanitize().Limit
	tx, err := b.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, WorkGraphCursor{}, false, false, fmt.Errorf("begin work graph page: %w", err)
	}
	defer tx.Rollback(ctx)
	state := WorkGraphCursor{}
	if cursor == nil {
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp(), coalesce(max(id), 0) FROM work_items WHERE namespace_id = ANY($1)`, nsIDs).Scan(&state.SnapshotAt, &state.MaxNodeID); err != nil {
			return nil, state, false, false, fmt.Errorf("snapshot work graph nodes: %w", err)
		}
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM work_item_edges WHERE namespace_id = ANY($1)`, nsIDs).Scan(&state.MaxEdgeID); err != nil {
			return nil, state, false, false, fmt.Errorf("snapshot work graph edges: %w", err)
		}
	} else {
		state = *cursor
		var changed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM work_items WHERE namespace_id = ANY($1) AND id <= $2 AND updated_at > $3
			UNION ALL SELECT 1 FROM work_item_edges WHERE namespace_id = ANY($1) AND id <= $4 AND deleted_at > $3
		)`, nsIDs, state.MaxNodeID, state.SnapshotAt, state.MaxEdgeID).Scan(&changed); err != nil {
			return nil, state, false, false, fmt.Errorf("validate work graph continuation: %w", err)
		}
		if changed {
			return nil, state, false, false, ErrWorkGraphCursorStale
		}
	}
	statusClause := ""
	if !includeDone {
		statusClause = " AND status NOT IN ('done', 'canceled')"
	}
	nodeRows, err := tx.Query(ctx, `SELECT `+workItemColumns+` FROM work_items
		WHERE namespace_id = ANY($1) AND id <= $2 AND deleted_at IS NULL`+statusClause+`
		AND (NOT $3 OR priority < $4 OR (priority = $4 AND (position > $5 OR (position = $5 AND id > $6))))
		ORDER BY priority DESC, position, id LIMIT $7`, nsIDs, state.MaxNodeID, state.NodeSet, state.NodePriority, state.NodePosition, state.NodeID, nodeLimit+1)
	if err != nil {
		return nil, state, false, false, fmt.Errorf("list work graph node page: %w", err)
	}
	nodes, err := scanWorkItemRows(nodeRows)
	if err != nil {
		return nil, state, false, false, err
	}
	nodeMore := len(nodes) > nodeLimit
	if nodeMore {
		nodes = nodes[:nodeLimit]
	}
	if len(nodes) > 0 {
		last := nodes[len(nodes)-1]
		state.NodeSet, state.NodePriority, state.NodePosition, state.NodeID = true, last.Priority, last.Position, last.ID
	}
	// Worktree IDs and capabilities use the same repeatable-read snapshot as
	// the page rows, so a response cannot mix states from concurrent writes.
	nodes, err = attachWorkGraphPageDetails(ctx, tx, nodes)
	if err != nil {
		return nil, state, false, false, err
	}
	edgeRows, err := tx.Query(ctx, `SELECT edge.id, edge.namespace_id, edge.from_item_id, edge.to_item_id, edge.edge_type, edge.created_at, edge.deleted_at
		FROM work_item_edges edge
		JOIN work_items source_item ON source_item.id=edge.from_item_id AND source_item.namespace_id=edge.namespace_id AND source_item.deleted_at IS NULL
		JOIN work_items target_item ON target_item.id=edge.to_item_id AND target_item.namespace_id=edge.namespace_id AND target_item.deleted_at IS NULL
		WHERE edge.namespace_id = ANY($1) AND edge.id <= $2 AND edge.id > $3 AND edge.deleted_at IS NULL
		AND ($4 OR (source_item.status NOT IN ('done','canceled') AND target_item.status NOT IN ('done','canceled')))
		ORDER BY edge.id LIMIT $5`, nsIDs, state.MaxEdgeID, state.EdgeID, includeDone, edgeLimit+1)
	if err != nil {
		return nil, state, false, false, fmt.Errorf("list work graph edge page: %w", err)
	}
	edges, err := scanWorkItemEdges(edgeRows)
	if err != nil {
		return nil, state, false, false, err
	}
	edgeMore := len(edges) > edgeLimit
	if edgeMore {
		edges = edges[:edgeLimit]
	}
	if len(edges) > 0 {
		state.EdgeID = edges[len(edges)-1].ID
	}
	var worktreeRows pgx.Rows
	if nodeMore || edgeMore {
		worktreeIDs := make([]int64, 0)
		seen := make(map[int64]struct{})
		for _, node := range nodes {
			for _, id := range node.WorktreeIDs {
				if _, ok := seen[id]; !ok {
					seen[id] = struct{}{}
					worktreeIDs = append(worktreeIDs, id)
				}
			}
		}
		worktreeRows, err = tx.Query(ctx, `SELECT `+worktreeColumns+` FROM worktrees WHERE id = ANY($1) AND deleted_at IS NULL ORDER BY updated_at DESC, id`, worktreeIDs)
	} else {
		worktreeRows, err = tx.Query(ctx, `SELECT `+worktreeColumns+` FROM worktrees WHERE namespace_id = ANY($1) AND deleted_at IS NULL ORDER BY updated_at DESC, id`, nsIDs)
	}
	if err != nil {
		return nil, state, false, false, fmt.Errorf("list work graph page worktrees: %w", err)
	}
	worktrees, err := scanWorktreeRows(worktreeRows)
	if err != nil {
		return nil, state, false, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, state, false, false, fmt.Errorf("commit work graph page: %w", err)
	}
	return &models.WorkGraph{Nodes: nodes, Edges: edges, Worktrees: worktrees}, state, nodeMore, edgeMore, nil
}

func attachWorkGraphPageDetails(ctx context.Context, tx pgx.Tx, items []models.WorkItem) ([]models.WorkItem, error) {
	if len(items) == 0 {
		return items, nil
	}
	ids := make([]int64, len(items))
	byID := make(map[int64]int, len(items))
	for i := range items {
		ids[i], byID[items[i].ID] = items[i].ID, i
		items[i].RequiredCapabilities = []string{}
	}
	rows, err := tx.Query(ctx, `SELECT wiwt.work_item_id, array_agg(wiwt.worktree_id ORDER BY wiwt.worktree_id)
		FROM work_item_worktrees wiwt JOIN worktrees wt ON wt.id=wiwt.worktree_id AND wt.deleted_at IS NULL
		WHERE wiwt.work_item_id=ANY($1) GROUP BY wiwt.work_item_id`, ids)
	if err != nil {
		return nil, fmt.Errorf("list work graph page worktree links: %w", err)
	}
	for rows.Next() {
		var id int64
		var linked []int64
		if err := rows.Scan(&id, &linked); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan work graph page worktree links: %w", err)
		}
		items[byID[id]].WorktreeIDs = linked
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("read work graph page worktree links: %w", err)
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT work_item_id, array_agg(capability ORDER BY capability) FROM work_item_capabilities WHERE work_item_id=ANY($1) GROUP BY work_item_id`, ids)
	if err != nil {
		return nil, fmt.Errorf("list work graph page capabilities: %w", err)
	}
	for rows.Next() {
		var id int64
		var capabilities []string
		if err := rows.Scan(&id, &capabilities); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan work graph page capabilities: %w", err)
		}
		items[byID[id]].RequiredCapabilities = capabilities
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("read work graph page capabilities: %w", err)
	}
	rows.Close()
	return items, nil
}

func (b *Brain) attachWorktreeIDs(ctx context.Context, items []models.WorkItem) ([]models.WorkItem, error) {
	if len(items) == 0 {
		return items, nil
	}
	ids := make([]int64, 0, len(items))
	byID := make(map[int64]int, len(items))
	for i := range items {
		ids = append(ids, items[i].ID)
		byID[items[i].ID] = i
	}
	rows, err := b.pool.Query(ctx,
		`SELECT wiwt.work_item_id, array_agg(wiwt.worktree_id ORDER BY wiwt.worktree_id)
		 FROM work_item_worktrees wiwt
		 JOIN worktrees wt ON wt.id = wiwt.worktree_id AND wt.deleted_at IS NULL
		 WHERE wiwt.work_item_id = ANY($1) GROUP BY wiwt.work_item_id`, ids,
	)
	if err != nil {
		return nil, fmt.Errorf("list work item worktrees: %w", err)
	}
	for rows.Next() {
		var itemID int64
		var worktreeIDs []int64
		if err := rows.Scan(&itemID, &worktreeIDs); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan work item worktrees: %w", err)
		}
		if i, ok := byID[itemID]; ok {
			items[i].WorktreeIDs = worktreeIDs
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("read work item worktrees: %w", err)
	}
	rows.Close()
	return b.attachWorkCapabilities(ctx, items)
}

// AttachWorktreeToItem links a local worktree to an operational task.
func (b *Brain) AttachWorktreeToItem(ctx context.Context, workItemID, worktreeID int64, relation string) error {
	if relation != "active" && relation != "related" {
		return fmt.Errorf("brain: invalid worktree relation %q", relation)
	}
	var itemNS, treeNS int64
	if err := b.pool.QueryRow(ctx, `SELECT namespace_id FROM work_items WHERE id = $1 AND deleted_at IS NULL`, workItemID).Scan(&itemNS); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrWorkItemNotFound
		}
		return fmt.Errorf("check work item for worktree: %w", err)
	}
	if err := b.pool.QueryRow(ctx, `SELECT namespace_id FROM worktrees WHERE id = $1 AND deleted_at IS NULL`, worktreeID).Scan(&treeNS); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrWorktreeNotFound
		}
		return fmt.Errorf("check worktree for work item: %w", err)
	}
	if itemNS != treeNS {
		return fmt.Errorf("brain: work item and worktree must share a namespace")
	}
	_, err := b.pool.Exec(ctx,
		`INSERT INTO work_item_worktrees (work_item_id, worktree_id, relation) VALUES ($1, $2, $3)
		 ON CONFLICT (work_item_id, worktree_id) DO UPDATE SET relation = EXCLUDED.relation`,
		workItemID, worktreeID, relation,
	)
	if err != nil {
		return fmt.Errorf("attach worktree to work item: %w", err)
	}
	return nil
}

// RegisterWorktree upserts metadata reported by a local worktree bridge.
func (b *Brain) RegisterWorktree(ctx context.Context, namespaceID int64, repository, worktreePath, branch, headSHA, status, agentID string, metadata json.RawMessage) (*models.Worktree, error) {
	if err := validateContent(repository); err != nil {
		return nil, fmt.Errorf("brain: repository: %w", err)
	}
	if err := validateContent(worktreePath); err != nil {
		return nil, fmt.Errorf("brain: worktree path: %w", err)
	}
	if err := validateWorktreeStatus(status); err != nil {
		return nil, err
	}
	if metadata == nil {
		metadata = json.RawMessage(`{}`)
	}
	if len(metadata) > maxContentLen {
		return nil, ErrContentTooLong
	}
	if !json.Valid(metadata) {
		return nil, fmt.Errorf("brain: worktree metadata must be valid JSON")
	}

	worktree, err := scanWorktree(b.pool.QueryRow(ctx,
		`INSERT INTO worktrees (namespace_id, repository, worktree_path, branch, head_sha, status, agent_id, last_seen_at, metadata, deleted_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, now(), $8, NULL)
		 ON CONFLICT (namespace_id, worktree_path) WHERE deleted_at IS NULL DO UPDATE SET
		   repository = EXCLUDED.repository, branch = EXCLUDED.branch, head_sha = EXCLUDED.head_sha,
		   status = EXCLUDED.status, agent_id = EXCLUDED.agent_id, last_seen_at = now(),
		   metadata = EXCLUDED.metadata, updated_at = now(), deleted_at = NULL
		 RETURNING `+worktreeColumns,
		namespaceID, repository, worktreePath, branch, headSHA, status, agentID, metadata,
	))
	if err != nil {
		return nil, fmt.Errorf("register worktree: %w", err)
	}
	return worktree, nil
}

func (b *Brain) listWorktreesByNamespaceIDs(ctx context.Context, nsIDs []int64) ([]models.Worktree, error) {
	rows, err := b.pool.Query(ctx,
		`SELECT `+worktreeColumns+`
		 FROM worktrees WHERE namespace_id = ANY($1) AND deleted_at IS NULL
		 ORDER BY updated_at DESC, id`, nsIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}
	return scanWorktreeRows(rows)
}

// ListWorktrees lists registered worktrees for namespaces.
func (b *Brain) ListWorktrees(ctx context.Context, namespaceSlugs []string, page Pagination) ([]models.Worktree, error) {
	nsIDs, err := b.resolveNamespaceIDs(ctx, namespaceSlugs)
	if err != nil {
		return nil, err
	}
	page = b.sanitizePage(page)
	rows, err := b.pool.Query(ctx,
		`SELECT `+worktreeColumns+`
		 FROM worktrees WHERE namespace_id = ANY($1) AND deleted_at IS NULL
		 ORDER BY updated_at DESC, id LIMIT $2 OFFSET $3`, nsIDs, page.Limit, page.Offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}
	return scanWorktreeRows(rows)
}

// GetWorktree returns one active registered worktree.
func (b *Brain) GetWorktree(ctx context.Context, id int64) (*models.Worktree, error) {
	worktree, err := scanWorktree(b.pool.QueryRow(ctx,
		`SELECT `+worktreeColumns+`
		 FROM worktrees WHERE id = $1 AND deleted_at IS NULL`, id,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWorktreeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get worktree: %w", err)
	}
	return worktree, nil
}

// RecordWorkEvent stores an append-only structured event. eventKey makes
// retries safe for bridges that may reconnect after a timeout.
func (b *Brain) RecordWorkEvent(ctx context.Context, namespaceID int64, worktreeID, workItemID *int64, eventType, eventKey string, payload json.RawMessage, occurredAt *time.Time) (*models.WorkEvent, error) {
	if err := validateContent(eventType); err != nil {
		return nil, fmt.Errorf("brain: work event type: %w", err)
	}
	if payload == nil {
		payload = json.RawMessage(`{}`)
	}
	if len(payload) > maxContentLen {
		return nil, ErrContentTooLong
	}
	if !json.Valid(payload) {
		return nil, fmt.Errorf("brain: work event payload must be valid JSON")
	}
	if worktreeID != nil {
		var treeNS int64
		if err := b.pool.QueryRow(ctx, `SELECT namespace_id FROM worktrees WHERE id = $1 AND deleted_at IS NULL`, *worktreeID).Scan(&treeNS); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrWorktreeNotFound
			}
			return nil, fmt.Errorf("check work event worktree: %w", err)
		}
		if treeNS != namespaceID {
			return nil, fmt.Errorf("brain: work event worktree must share the target namespace")
		}
	}
	if workItemID != nil {
		var itemNS int64
		if err := b.pool.QueryRow(ctx, `SELECT namespace_id FROM work_items WHERE id = $1 AND deleted_at IS NULL`, *workItemID).Scan(&itemNS); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrWorkItemNotFound
			}
			return nil, fmt.Errorf("check work event work item: %w", err)
		}
		if itemNS != namespaceID {
			return nil, fmt.Errorf("brain: work event work item must share the target namespace")
		}
	}
	if occurredAt == nil {
		now := time.Now().UTC()
		occurredAt = &now
	}

	var event models.WorkEvent
	err := b.pool.QueryRow(ctx,
		`INSERT INTO work_events (namespace_id, worktree_id, work_item_id, attempt_id, event_type, event_key, payload, occurred_at)
		 VALUES ($1, $2, $3, NULL, $4, NULLIF($5, ''), $6, $7)
		 ON CONFLICT (worktree_id, event_key) WHERE event_key IS NOT NULL DO UPDATE SET id = work_events.id
		 RETURNING id, namespace_id, worktree_id, work_item_id, attempt_id, event_type, event_key, payload, occurred_at, created_at`,
		namespaceID, worktreeID, workItemID, eventType, strings.TrimSpace(eventKey), payload, *occurredAt,
	).Scan(&event.ID, &event.NamespaceID, &event.WorktreeID, &event.WorkItemID, &event.AttemptID, &event.EventType, &event.EventKey, &event.Payload, &event.OccurredAt, &event.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("record work event: %w", err)
	}
	return &event, nil
}

// ListWorkEvents returns recent events for a namespace.
func (b *Brain) ListWorkEvents(ctx context.Context, namespaceSlugs []string, worktreeID, workItemID *int64, page Pagination) ([]models.WorkEvent, error) {
	nsIDs, err := b.resolveNamespaceIDs(ctx, namespaceSlugs)
	if err != nil {
		return nil, err
	}
	page = b.sanitizePage(page)
	query := `SELECT id, namespace_id, worktree_id, work_item_id, attempt_id, event_type, event_key, payload, occurred_at, created_at FROM work_events WHERE namespace_id = ANY($1)`
	args := []any{nsIDs}
	arg := 2
	if worktreeID != nil {
		query += fmt.Sprintf(" AND worktree_id = $%d", arg)
		args = append(args, *worktreeID)
		arg++
	}
	if workItemID != nil {
		query += fmt.Sprintf(" AND work_item_id = $%d", arg)
		args = append(args, *workItemID)
		arg++
	}
	query += fmt.Sprintf(" ORDER BY occurred_at DESC, id DESC LIMIT $%d OFFSET $%d", arg, arg+1)
	args = append(args, page.Limit, page.Offset)
	rows, err := b.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list work events: %w", err)
	}
	defer rows.Close()
	events := make([]models.WorkEvent, 0)
	for rows.Next() {
		var event models.WorkEvent
		if err := rows.Scan(&event.ID, &event.NamespaceID, &event.WorktreeID, &event.WorkItemID, &event.AttemptID, &event.EventType, &event.EventKey, &event.Payload, &event.OccurredAt, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan work event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read work events: %w", err)
	}
	return events, nil
}

// LinkWorkItemMemory connects an operational task to an active memory object.
func (b *Brain) LinkWorkItemMemory(ctx context.Context, workItemID int64, memoryType string, memoryID int64, relation string) (*models.WorkItemMemoryLink, error) {
	if _, ok := workMemoryTypes[memoryType]; !ok {
		return nil, fmt.Errorf("brain: invalid work memory type %q", memoryType)
	}
	relation = strings.TrimSpace(relation)
	if _, ok := workMemoryRelations[relation]; !ok {
		return nil, fmt.Errorf("brain: invalid work memory relation %q", relation)
	}
	var namespaceID int64
	if err := b.pool.QueryRow(ctx, `SELECT namespace_id FROM work_items WHERE id = $1 AND deleted_at IS NULL`, workItemID).Scan(&namespaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrWorkItemNotFound
		}
		return nil, fmt.Errorf("check work item memory link: %w", err)
	}
	memoryNamespace, err := b.memoryNamespaceID(ctx, memoryType, memoryID)
	if err != nil {
		return nil, err
	}
	if memoryNamespace != namespaceID {
		return nil, fmt.Errorf("brain: memory link objects must share a namespace")
	}

	var link models.WorkItemMemoryLink
	err = b.pool.QueryRow(ctx,
		`INSERT INTO work_item_memory_links (work_item_id, memory_type, memory_id, relation)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (work_item_id, memory_type, memory_id) DO UPDATE SET relation = EXCLUDED.relation
		 RETURNING work_item_id, memory_type, memory_id, relation, created_at`,
		workItemID, memoryType, memoryID, relation,
	).Scan(&link.WorkItemID, &link.MemoryType, &link.MemoryID, &link.Relation, &link.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("link work item memory: %w", err)
	}
	return &link, nil
}

// ListWorkItemMemoryLinks returns the memory references attached to an issue.
func (b *Brain) ListWorkItemMemoryLinks(ctx context.Context, workItemID int64) ([]models.WorkItemMemoryLink, error) {
	if _, err := b.GetWorkItem(ctx, workItemID); err != nil {
		return nil, err
	}
	rows, err := b.pool.Query(ctx,
		`SELECT work_item_id, memory_type, memory_id, relation, created_at, derived
		 FROM work_item_memory_context
		 WHERE work_item_id = $1
		 ORDER BY created_at, memory_type, memory_id`, workItemID,
	)
	if err != nil {
		return nil, fmt.Errorf("list work item memory links: %w", err)
	}
	defer rows.Close()
	links := make([]models.WorkItemMemoryLink, 0)
	for rows.Next() {
		var link models.WorkItemMemoryLink
		if err := rows.Scan(&link.WorkItemID, &link.MemoryType, &link.MemoryID, &link.Relation, &link.CreatedAt, &link.Derived); err != nil {
			return nil, fmt.Errorf("scan work item memory link: %w", err)
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read work item memory links: %w", err)
	}
	return links, nil
}

func (b *Brain) memoryNamespaceID(ctx context.Context, memoryType string, memoryID int64) (int64, error) {
	table := map[string]string{
		"episode":    "episodes",
		"fact":       "facts",
		"hypothesis": "hypotheses",
		"failure":    "failures",
		"goal":       "goals",
	}[memoryType]
	var namespaceID int64
	query := fmt.Sprintf("SELECT namespace_id FROM %s WHERE id = $1 AND deleted_at IS NULL", table)
	if memoryType == "fact" {
		query = "SELECT namespace_id FROM facts WHERE id = $1 AND deleted_at IS NULL AND valid_until IS NULL"
	}
	if err := b.pool.QueryRow(ctx, query, memoryID).Scan(&namespaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("brain: %s %d not found", memoryType, memoryID)
		}
		return 0, fmt.Errorf("check %s memory: %w", memoryType, err)
	}
	return namespaceID, nil
}
