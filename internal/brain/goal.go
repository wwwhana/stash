package brain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alash3al/stash/internal/models"
	"github.com/jackc/pgx/v5"
)

var (
	ErrGoalNotFound               = fmt.Errorf("brain: goal not found")
	ErrGoalNotActive              = fmt.Errorf("brain: goal is not active")
	ErrGoalContributorsIncomplete = fmt.Errorf("brain: goal still has unfinished child goals or work")
)

const goalColumns = `id, namespace_id, parent_id, content, status, priority, notes,
completed_at, abandoned_at, created_at, updated_at, deleted_at`

func scanGoal(h *models.Goal, row pgx.Row) error {
	return row.Scan(
		&h.ID, &h.NamespaceID, &h.ParentID, &h.Content, &h.Status, &h.Priority, &h.Notes,
		&h.CompletedAt, &h.AbandonedAt, &h.CreatedAt, &h.UpdatedAt, &h.DeletedAt,
	)
}

func scanGoalRows(rows pgx.Rows) ([]models.Goal, error) {
	var result []models.Goal
	for rows.Next() {
		var g models.Goal
		if err := rows.Scan(
			&g.ID, &g.NamespaceID, &g.ParentID, &g.Content, &g.Status, &g.Priority, &g.Notes,
			&g.CompletedAt, &g.AbandonedAt, &g.CreatedAt, &g.UpdatedAt, &g.DeletedAt,
		); err != nil {
			return nil, fmt.Errorf("scan goal: %w", err)
		}
		result = append(result, g)
	}
	return result, rows.Err()
}

// CreateGoal creates a new goal in active status.
func (b *Brain) CreateGoal(ctx context.Context, nsID int64, content string, parentID *int64, priority int) (*models.Goal, error) {
	if err := validateContent(content); err != nil {
		return nil, err
	}

	if parentID != nil {
		parent, err := b.GetGoal(ctx, *parentID)
		if err != nil {
			return nil, fmt.Errorf("check parent goal: %w", err)
		}
		if parent.Status != "active" {
			return nil, fmt.Errorf("%w: parent goal %d is %s, must be active", ErrGoalNotActive, *parentID, parent.Status)
		}
		if parent.NamespaceID != nsID {
			return nil, fmt.Errorf("brain: parent goal must share the target namespace")
		}
	}

	var g models.Goal
	err := b.pool.QueryRow(ctx,
		`INSERT INTO goals (namespace_id, parent_id, content, priority)
		 VALUES ($1, $2, $3, $4)
		 RETURNING `+goalColumns,
		nsID, parentID, content, priority,
	).Scan(
		&g.ID, &g.NamespaceID, &g.ParentID, &g.Content, &g.Status, &g.Priority, &g.Notes,
		&g.CompletedAt, &g.AbandonedAt, &g.CreatedAt, &g.UpdatedAt, &g.DeletedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create goal: %w", err)
	}
	return &g, nil
}

// ListGoals returns goals across namespaces, optionally filtered by status and parent.
func (b *Brain) ListGoals(ctx context.Context, namespaceSlugs []string, status string, parentID *int64, page Pagination) ([]models.Goal, error) {
	nsIDs, err := b.resolveNamespaceIDs(ctx, namespaceSlugs)
	if err != nil {
		return nil, err
	}

	page = b.sanitizePage(page)

	query := `SELECT ` + goalColumns + ` FROM goals WHERE namespace_id = ANY($1) AND deleted_at IS NULL`
	args := []any{nsIDs}
	argN := 1

	if status != "" {
		argN++
		query += fmt.Sprintf(" AND status = $%d", argN)
		args = append(args, status)
	}

	if parentID != nil {
		argN++
		query += fmt.Sprintf(" AND parent_id = $%d", argN)
		args = append(args, *parentID)
	} else if status == "" {
		query += " AND parent_id IS NULL"
	}

	argN++
	query += fmt.Sprintf(" ORDER BY priority DESC, created_at ASC LIMIT $%d", argN)
	args = append(args, page.Limit)

	argN++
	query += fmt.Sprintf(" OFFSET $%d", argN)
	args = append(args, page.Offset)

	rows, err := b.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list goals: %w", err)
	}
	defer rows.Close()
	return scanGoalRows(rows)
}

// GetGoal returns a single goal by ID.
func (b *Brain) GetGoal(ctx context.Context, id int64) (*models.Goal, error) {
	var g models.Goal
	err := b.pool.QueryRow(ctx,
		`SELECT `+goalColumns+` FROM goals WHERE id = $1 AND deleted_at IS NULL`, id,
	).Scan(
		&g.ID, &g.NamespaceID, &g.ParentID, &g.Content, &g.Status, &g.Priority, &g.Notes,
		&g.CompletedAt, &g.AbandonedAt, &g.CreatedAt, &g.UpdatedAt, &g.DeletedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrGoalNotFound
		}
		return nil, fmt.Errorf("get goal: %w", err)
	}
	return &g, nil
}

// GetGoalProgress returns sub-goal counts for a parent goal.
func (b *Brain) GetGoalProgress(ctx context.Context, id int64) (total, completed int, err error) {
	err = b.pool.QueryRow(ctx,
		`SELECT COUNT(*) FILTER (WHERE status IN ('active', 'completed')),
		        COUNT(*) FILTER (WHERE status = 'completed')
		 FROM goals WHERE parent_id = $1 AND deleted_at IS NULL`,
		id,
	).Scan(&total, &completed)
	if err != nil {
		return 0, 0, fmt.Errorf("get goal progress: %w", err)
	}
	return total, completed, nil
}

// CompleteGoal marks a goal as completed. A composed goal cannot bypass
// unfinished child goals or executable work. Satisfied parents roll up in the
// same transaction.
func (b *Brain) CompleteGoal(ctx context.Context, id int64, notes string) (*models.Goal, error) {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin complete goal: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	if err := tx.QueryRow(ctx,
		`SELECT status FROM goals WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, id,
	).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrGoalNotFound
	} else if err != nil {
		return nil, fmt.Errorf("lock goal for completion: %w", err)
	}
	if status != "active" {
		return nil, fmt.Errorf("%w: goal %d is %s, must be active", ErrGoalNotActive, id, status)
	}
	hasContributors, pending, err := goalContributorState(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if hasContributors && pending {
		return nil, ErrGoalContributorsIncomplete
	}

	var g models.Goal
	err = tx.QueryRow(ctx,
		`UPDATE goals SET status = 'completed', completed_at = clock_timestamp(),
		        notes = CASE WHEN $2 = '' THEN notes ELSE $2 END, updated_at = now()
		 WHERE id = $1 AND status = 'active' AND deleted_at IS NULL
		 RETURNING `+goalColumns,
		id, notes,
	).Scan(
		&g.ID, &g.NamespaceID, &g.ParentID, &g.Content, &g.Status, &g.Priority, &g.Notes,
		&g.CompletedAt, &g.AbandonedAt, &g.CreatedAt, &g.UpdatedAt, &g.DeletedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("complete goal: %w", err)
	}

	if g.ParentID != nil {
		if err := autoCompleteGoalChain(ctx, tx, *g.ParentID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit complete goal: %w", err)
	}
	return &g, nil
}

func goalContributorState(ctx context.Context, tx pgx.Tx, goalID int64) (hasContributors, pending bool, err error) {
	err = tx.QueryRow(ctx,
		`WITH RECURSIVE goal_tree AS (
		    SELECT id, namespace_id, status, ARRAY[id]::bigint[] AS path
		    FROM goals WHERE id = $1 AND deleted_at IS NULL
		    UNION ALL
		    SELECT child.id, child.namespace_id, child.status, parent.path || child.id
		    FROM goals child JOIN goal_tree parent ON child.parent_id = parent.id
		    WHERE child.namespace_id = parent.namespace_id
		      AND child.deleted_at IS NULL
		      AND NOT child.id = ANY(parent.path)
		), executable_work AS (
		    SELECT item.id, item.status
		    FROM work_items item
		    WHERE item.goal_id IN (SELECT id FROM goal_tree) AND item.deleted_at IS NULL
		      AND NOT EXISTS (
		          SELECT 1 FROM work_items child
		          WHERE child.parent_id = item.id AND child.deleted_at IS NULL
		      )
		      AND NOT EXISTS (
		          SELECT 1 FROM work_plan_items plan
		          WHERE plan.work_item_id = item.id AND plan.kind = 'component'
		      )
		)
		SELECT
		    EXISTS (SELECT 1 FROM goal_tree WHERE id <> $1) OR EXISTS (SELECT 1 FROM executable_work),
		    EXISTS (SELECT 1 FROM goal_tree WHERE id <> $1 AND status <> 'completed')
		      OR EXISTS (SELECT 1 FROM executable_work WHERE status NOT IN ('done', 'canceled'))`,
		goalID,
	).Scan(&hasContributors, &pending)
	if err != nil {
		return false, false, fmt.Errorf("check goal contributors: %w", err)
	}
	return hasContributors, pending, nil
}

func autoCompleteGoalChain(ctx context.Context, tx pgx.Tx, goalID int64) error {
	for goalID > 0 {
		var parentID *int64
		var status string
		if err := tx.QueryRow(ctx,
			`SELECT parent_id, status FROM goals WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, goalID,
		).Scan(&parentID, &status); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return fmt.Errorf("lock goal rollup: %w", err)
		}
		if status == "abandoned" {
			return nil
		}
		if status == "active" {
			hasContributors, pending, err := goalContributorState(ctx, tx, goalID)
			if err != nil {
				return err
			}
			if !hasContributors || pending {
				return nil
			}
			if _, err := tx.Exec(ctx,
				`UPDATE goals SET status = 'completed', completed_at = clock_timestamp(), updated_at = now()
				 WHERE id = $1 AND status = 'active' AND deleted_at IS NULL`, goalID,
			); err != nil {
				return fmt.Errorf("roll up completed goal: %w", err)
			}
		}
		if parentID == nil {
			return nil
		}
		goalID = *parentID
	}
	return nil
}

// AbandonGoal marks a goal as abandoned.
func (b *Brain) AbandonGoal(ctx context.Context, id int64, notes string) (*models.Goal, error) {
	current, err := b.GetGoal(ctx, id)
	if err != nil {
		return nil, err
	}

	if current.Status != "active" {
		return nil, fmt.Errorf("%w: goal %d is %s, must be active", ErrGoalNotActive, id, current.Status)
	}

	now := time.Now().UTC()

	var g models.Goal
	err = b.pool.QueryRow(ctx,
		`UPDATE goals SET status = 'abandoned', abandoned_at = $2, notes = CASE WHEN $3 = '' THEN notes ELSE $3 END, updated_at = $2
		 WHERE id = $1 AND status = 'active' AND deleted_at IS NULL
		 RETURNING `+goalColumns,
		id, now, notes,
	).Scan(
		&g.ID, &g.NamespaceID, &g.ParentID, &g.Content, &g.Status, &g.Priority, &g.Notes,
		&g.CompletedAt, &g.AbandonedAt, &g.CreatedAt, &g.UpdatedAt, &g.DeletedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("abandon goal: %w", err)
	}
	return &g, nil
}

// UpdateGoal updates content, priority, and notes of an active goal.
func (b *Brain) UpdateGoal(ctx context.Context, id int64, content string, priority int, notes string) (*models.Goal, error) {
	current, err := b.GetGoal(ctx, id)
	if err != nil {
		return nil, err
	}

	if current.Status != "active" {
		return nil, fmt.Errorf("%w: goal %d is %s, must be active", ErrGoalNotActive, id, current.Status)
	}

	if content == "" {
		content = current.Content
	}
	if priority == 0 {
		priority = current.Priority
	}
	if notes == "" {
		notes = current.Notes
	}
	if err := validateContent(content); err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	var g models.Goal
	err = b.pool.QueryRow(ctx,
		`UPDATE goals SET content = $2, priority = $3, notes = $4, updated_at = $5
		 WHERE id = $1 AND status = 'active' AND deleted_at IS NULL
		 RETURNING `+goalColumns,
		id, content, priority, notes, now,
	).Scan(
		&g.ID, &g.NamespaceID, &g.ParentID, &g.Content, &g.Status, &g.Priority, &g.Notes,
		&g.CompletedAt, &g.AbandonedAt, &g.CreatedAt, &g.UpdatedAt, &g.DeletedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("update goal: %w", err)
	}
	return &g, nil
}

// DeleteGoal soft-deletes a goal by ID. Children cascade via FK.
func (b *Brain) DeleteGoal(ctx context.Context, id int64) error {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete goal: %w", err)
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx,
		`WITH RECURSIVE goal_tree AS (
			SELECT id FROM goals WHERE id = $1 AND deleted_at IS NULL
			UNION ALL
			SELECT child.id FROM goals child JOIN goal_tree parent ON child.parent_id = parent.id
		)
		UPDATE goals SET deleted_at = now(), updated_at = now()
		WHERE id IN (SELECT id FROM goal_tree) AND deleted_at IS NULL`,
		id,
	)
	if err != nil {
		return fmt.Errorf("delete goal: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrGoalNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete goal: %w", err)
	}
	return nil
}
