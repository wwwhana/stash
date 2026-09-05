package brain

import (
	"context"
	"github.com/alash3al/stash/internal/models"
)

// Calculate from leaf work each time so old records and every mutation path
// agree without requiring agents to synchronize a second stored status.
func (b *Brain) componentProgress(ctx context.Context, namespaceID int64) (map[int64]models.WorkProgress, error) {
	rows, err := b.pool.Query(ctx, `WITH RECURSIVE descendants AS (
		SELECT item.id AS component_id, child.id, child.status
		FROM work_items item JOIN work_plan_items plan ON plan.work_item_id = item.id AND plan.kind = 'component'
		JOIN work_items child ON child.parent_id = item.id AND child.deleted_at IS NULL
		WHERE item.namespace_id = $1 AND item.deleted_at IS NULL AND child.namespace_id = $1
		UNION
		SELECT parent.component_id, child.id, child.status FROM descendants parent
		JOIN work_items child ON child.parent_id = parent.id AND child.deleted_at IS NULL AND child.namespace_id = $1
	) SELECT component_id, status, count(*) FROM descendants item
	WHERE NOT EXISTS (SELECT 1 FROM work_items child WHERE child.parent_id = item.id AND child.deleted_at IS NULL)
	AND NOT EXISTS (SELECT 1 FROM work_plan_items plan WHERE plan.work_item_id = item.id AND plan.kind = 'component')
	GROUP BY component_id, status`, namespaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int64]models.WorkProgress)
	for rows.Next() {
		var id int64
		var status string
		var count int
		if err := rows.Scan(&id, &status, &count); err != nil {
			return nil, err
		}
		value := result[id]
		value.Total += count
		switch status {
		case "done":
			value.Done += count
		case "canceled":
			value.Canceled += count
		case "blocked":
			value.Blocked += count
		case "doing", "review":
			value.Active += count
		}
		result[id] = value
	}
	for id, value := range result {
		value.Status = "ready"
		if total := value.Total - value.Canceled; total > 0 {
			value.Progress = float64(value.Done) / float64(total)
		}
		switch {
		case value.Canceled == value.Total:
			value.Status = "canceled"
		case value.Done+value.Canceled == value.Total:
			value.Status = "done"
		case value.Blocked > 0:
			value.Status = "blocked"
		case value.Active > 0 || value.Done > 0:
			value.Status = "doing"
		}
		result[id] = value
	}
	return result, rows.Err()
}
