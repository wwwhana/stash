package brain

import (
	"context"
	"fmt"
	"strings"

	"github.com/alash3al/stash/internal/models"
)

// ListMemory includes memories with no work item or goal. Tracking is optional.
func (b *Brain) ListMemory(ctx context.Context, namespaceID int64, kind, query, status string, page Pagination) ([]models.GoalMapMemory, error) {
	if kind != "" && kind != "fact" && kind != "episode" && kind != "hypothesis" && kind != "failure" {
		return nil, fmt.Errorf("invalid memory type")
	}
	page = b.sanitizePage(page)
	rows, err := b.pool.Query(ctx, `WITH memories AS (
		SELECT 'fact'::text AS memory_type, id, content, 'active'::text AS status, created_at FROM facts WHERE namespace_id=$1 AND deleted_at IS NULL AND valid_until IS NULL
		UNION ALL SELECT 'episode', id, content, 'recorded', created_at FROM episodes WHERE namespace_id=$1 AND deleted_at IS NULL
		UNION ALL SELECT 'hypothesis', id, content, status, created_at FROM hypotheses WHERE namespace_id=$1 AND deleted_at IS NULL
		UNION ALL SELECT 'failure', id, content, 'recorded', created_at FROM failures WHERE namespace_id=$1 AND deleted_at IS NULL
	) SELECT memory_type, id, left(content,256), status, char_length(content)>256 FROM memories
	WHERE ($2='' OR memory_type=$2) AND ($3='' OR content ILIKE $3 ESCAPE '\') AND ($4='' OR status=$4)
	ORDER BY created_at DESC, memory_type, id DESC LIMIT $5 OFFSET $6`, namespaceID, kind, memorySearchPattern(query), status, page.Limit, page.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []models.GoalMapMemory{}
	for rows.Next() {
		var item models.GoalMapMemory
		if err := rows.Scan(&item.MemoryType, &item.MemoryID, &item.Content, &item.Status, &item.ContentTruncated); err != nil {
			return nil, err
		}
		item.Key = goalMapMemoryKey(item.MemoryType, item.MemoryID)
		items = append(items, item)
	}
	return items, rows.Err()
}

func memorySearchPattern(value string) string {
	if value = strings.TrimSpace(value); value == "" {
		return ""
	}
	return "%" + escapeLikePattern(value) + "%"
}

// MemoryContext exposes source records even when neither memory has a ticket.
func (b *Brain) MemoryContext(ctx context.Context, namespaceID int64, kind string, id int64) (*models.GoalMap, error) {
	var recordNamespace int64
	var content string
	switch kind {
	case "fact":
		record, err := b.GetFact(ctx, id)
		if err != nil {
			return nil, err
		}
		recordNamespace, content = record.NamespaceID, record.Content
	case "episode":
		record, err := b.GetEpisode(ctx, id)
		if err != nil {
			return nil, err
		}
		recordNamespace, content = record.NamespaceID, record.Content
	case "hypothesis":
		record, err := b.GetHypothesis(ctx, id)
		if err != nil {
			return nil, err
		}
		recordNamespace, content = record.NamespaceID, record.Content
	case "failure":
		record, err := b.GetFailure(ctx, id)
		if err != nil {
			return nil, err
		}
		recordNamespace, content = record.NamespaceID, record.Content
	default:
		return nil, fmt.Errorf("invalid memory type")
	}
	if recordNamespace != namespaceID {
		return nil, fmt.Errorf("memory not found in this workspace")
	}
	result := &models.GoalMap{Memories: []models.GoalMapMemory{{Key: goalMapMemoryKey(kind, id), MemoryType: kind, MemoryID: id, Content: compactGoalMapText(content, goalMapMemoryContentLimit), ContentTruncated: len([]rune(content)) > goalMapMemoryContentLimit}}}
	if err := b.appendGoalMapFactSources(ctx, namespaceID, result); err != nil {
		return nil, err
	}
	return result, nil
}
