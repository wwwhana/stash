package brain

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/alash3al/stash/internal/models"
	"github.com/jackc/pgx/v5"
)

const (
	workContextFactContentLimit = 2048
	workContextCursorKeep       = 8
)

var workContextDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func workContextFactStateDigest(factID int64, relation, status, fingerprint string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s\x00%s", factID, relation, status, fingerprint)))
	return fmt.Sprintf("sha256:%x", digest)
}

func removedWorkContextFactDigest(previous string) string {
	digest := sha256.Sum256([]byte("stash-work-context-removed\x00" + previous))
	return fmt.Sprintf("sha256:%x", digest)
}

func (b *Brain) listCurrentWorkContextFactStates(ctx context.Context, workItemID int64) ([]models.WorkContextFactState, error) {
	rows, err := b.pool.Query(ctx,
		`SELECT linked.memory_id, linked.relation,
		        left(coalesce(fact.content, ''), $2),
		        coalesce(char_length(fact.content) > $2, false),
		        CASE
		            WHEN fact.id IS NULL THEN 'missing'
		            WHEN fact.deleted_at IS NOT NULL THEN 'deleted'
		            WHEN fact.valid_until IS NOT NULL THEN 'superseded'
		            ELSE 'active'
		        END,
		        coalesce(md5(concat_ws(E'\\x1f', fact.content, fact.confidence::text,
		                            fact.entity, fact.property, fact.value,
		                            fact.valid_from::text, fact.valid_until::text,
		                            fact.deleted_at::text, fact.updated_at::text)), '')
		 FROM work_item_memory_links linked
		 JOIN work_items item ON item.id = linked.work_item_id AND item.deleted_at IS NULL
		 LEFT JOIN facts fact
		   ON linked.memory_type = 'fact' AND fact.id = linked.memory_id
		  AND fact.namespace_id = item.namespace_id
		 WHERE linked.work_item_id = $1 AND linked.memory_type = 'fact'
		 ORDER BY linked.memory_id`,
		workItemID, workContextFactContentLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("list current work context facts: %w", err)
	}
	defer rows.Close()

	states := make([]models.WorkContextFactState, 0)
	for rows.Next() {
		var state models.WorkContextFactState
		var fingerprint string
		if err := rows.Scan(
			&state.FactID, &state.Relation, &state.Content, &state.ContentTruncated,
			&state.Status, &fingerprint,
		); err != nil {
			return nil, fmt.Errorf("scan current work context fact: %w", err)
		}
		state.Relation = strings.TrimSpace(state.Relation)
		state.StateDigest = workContextFactStateDigest(state.FactID, state.Relation, state.Status, fingerprint)
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list current work context fact rows: %w", err)
	}
	return states, nil
}

func (b *Brain) loadWorkContextCursorFactStates(ctx context.Context, workItemID int64, principalID, contextDigest string) ([]models.WorkContextFactState, bool, error) {
	if strings.TrimSpace(contextDigest) == "" {
		return nil, false, nil
	}
	var cursorID int64
	err := b.pool.QueryRow(ctx,
		`SELECT id FROM work_context_cursors
		 WHERE work_item_id = $1 AND principal_id = $2 AND context_digest = $3`,
		workItemID, principalID, contextDigest,
	).Scan(&cursorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("load work context cursor: %w", err)
	}

	rows, err := b.pool.Query(ctx,
		`SELECT fact_id, relation, status, state_digest
		 FROM work_context_cursor_facts WHERE cursor_id = $1 ORDER BY fact_id`, cursorID,
	)
	if err != nil {
		return nil, false, fmt.Errorf("list work context cursor facts: %w", err)
	}
	defer rows.Close()
	states := make([]models.WorkContextFactState, 0)
	for rows.Next() {
		var state models.WorkContextFactState
		if err := rows.Scan(&state.FactID, &state.Relation, &state.Status, &state.StateDigest); err != nil {
			return nil, false, fmt.Errorf("scan work context cursor fact: %w", err)
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list work context cursor fact rows: %w", err)
	}
	return states, true, nil
}

// DiffWorkContextFacts compares the currently linked facts with the cursor
// saved for a previous context digest. A missing cursor is safe: the caller
// receives every active fact and marks the cursor as reset.
func (b *Brain) DiffWorkContextFacts(ctx context.Context, workItemID int64, principalID, contextDigest string) (*models.WorkContextFactDiff, error) {
	principalID, err := validateWorkPrincipalID(principalID)
	if err != nil {
		return nil, err
	}
	if contextDigest != "" && !workContextDigestPattern.MatchString(contextDigest) {
		return nil, fmt.Errorf("brain: invalid work context digest")
	}
	current, err := b.listCurrentWorkContextFactStates(ctx, workItemID)
	if err != nil {
		return nil, err
	}
	previous, found, err := b.loadWorkContextCursorFactStates(ctx, workItemID, principalID, contextDigest)
	if err != nil {
		return nil, err
	}

	previousByID := make(map[int64]models.WorkContextFactState, len(previous))
	for _, state := range previous {
		previousByID[state.FactID] = state
	}
	currentByID := make(map[int64]models.WorkContextFactState, len(current))
	changes := make([]models.WorkContextFactChange, 0)
	for _, state := range current {
		currentByID[state.FactID] = state
		before, existed := previousByID[state.FactID]
		if !found {
			if state.Status == "active" {
				changes = append(changes, workContextFactChange(state, "added"))
			}
			continue
		}
		if existed && before.StateDigest == state.StateDigest {
			continue
		}
		switch state.Status {
		case "active":
			change := "updated"
			if !existed || before.Status != "active" {
				change = "added"
			}
			changes = append(changes, workContextFactChange(state, change))
		default:
			if existed && before.Status == "active" {
				changes = append(changes, workContextFactChange(state, "removed"))
			}
		}
	}
	if found {
		for _, before := range previous {
			if _, exists := currentByID[before.FactID]; exists || before.Status != "active" {
				continue
			}
			changes = append(changes, models.WorkContextFactChange{
				FactID: before.FactID, Relation: before.Relation, Change: "removed",
				Status: "missing", StateDigest: removedWorkContextFactDigest(before.StateDigest),
			})
		}
	}
	sort.SliceStable(changes, func(left, right int) bool {
		if changes[left].FactID != changes[right].FactID {
			return changes[left].FactID < changes[right].FactID
		}
		return changes[left].Relation < changes[right].Relation
	})
	return &models.WorkContextFactDiff{
		BaselineFound: found, Changes: changes, CurrentStates: current,
	}, nil
}

func workContextFactChange(state models.WorkContextFactState, change string) models.WorkContextFactChange {
	result := models.WorkContextFactChange{
		FactID: state.FactID, Relation: state.Relation, Change: change,
		Status: state.Status, StateDigest: state.StateDigest,
	}
	if state.Status == "active" {
		result.Content = state.Content
		result.ContentTruncated = state.ContentTruncated
	}
	return result
}

// SaveWorkContextCursor persists a fact baseline only after the final response
// page has been returned. Keeping a few recent cursors lets concurrent agents
// continue from their own last digest without storing raw fact content.
func (b *Brain) SaveWorkContextCursor(ctx context.Context, workItemID int64, principalID, contextDigest string, states []models.WorkContextFactState) error {
	principalID, err := validateWorkPrincipalID(principalID)
	if err != nil {
		return err
	}
	if !workContextDigestPattern.MatchString(contextDigest) {
		return fmt.Errorf("brain: invalid work context digest")
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin save work context cursor: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var cursorID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO work_context_cursors (work_item_id, principal_id, context_digest)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (work_item_id, principal_id, context_digest)
		 DO UPDATE SET created_at = clock_timestamp()
		 RETURNING id`,
		workItemID, principalID, contextDigest,
	).Scan(&cursorID); err != nil {
		return fmt.Errorf("save work context cursor: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM work_context_cursor_facts WHERE cursor_id = $1`, cursorID); err != nil {
		return fmt.Errorf("replace work context cursor facts: %w", err)
	}
	for _, state := range states {
		if _, err := tx.Exec(ctx,
			`INSERT INTO work_context_cursor_facts (cursor_id, fact_id, relation, status, state_digest)
			 VALUES ($1, $2, $3, $4, $5)`,
			cursorID, state.FactID, state.Relation, state.Status, state.StateDigest,
		); err != nil {
			return fmt.Errorf("save work context cursor fact: %w", err)
		}
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM work_context_cursors cursor
		 WHERE cursor.id IN (
		     SELECT id FROM (
		         SELECT id, created_at,
		                row_number() OVER (ORDER BY created_at DESC, id DESC) AS position
		         FROM work_context_cursors
		         WHERE work_item_id = $1 AND principal_id = $2
		     ) ranked
		     WHERE ranked.position > $3
		        OR ranked.created_at < clock_timestamp() - interval '7 days'
		 )`,
		workItemID, principalID, workContextCursorKeep,
	); err != nil {
		return fmt.Errorf("prune work context cursors: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit work context cursor: %w", err)
	}
	return nil
}
