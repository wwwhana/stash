package brain

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/alash3al/stash/internal/models"
	"github.com/jackc/pgx/v5"
)

var (
	ErrProjectGoalNotSet   = fmt.Errorf("brain: project goal is not set")
	ErrProjectGoalInactive = fmt.Errorf("brain: project goal is not active")
	ErrWorkGoalInvalid     = fmt.Errorf("brain: work item goal is invalid")
)

const goalMapMemoryContentLimit = 256

const (
	goalBriefContentLimit = 512
	goalContextPathLimit  = 16
	goalContextPeerLimit  = 12
)

type goalWorkCount struct {
	Total int
	Done  int
}

// SetProjectGoalRoot selects the shared top-level outcome returned to every
// agent that resumes this project namespace.
func (b *Brain) SetProjectGoalRoot(ctx context.Context, namespaceID, goalID int64, setBy string) (*models.GoalTree, error) {
	goal, err := b.GetGoal(ctx, goalID)
	if err != nil {
		return nil, err
	}
	if goal.NamespaceID != namespaceID {
		return nil, fmt.Errorf("brain: project goal must share the target namespace")
	}
	if goal.ParentID != nil {
		return nil, fmt.Errorf("brain: project goal must be a top-level goal")
	}
	if goal.Status != "active" {
		return nil, fmt.Errorf("%w: project goal %d is %s", ErrGoalNotActive, goalID, goal.Status)
	}
	setBy = strings.TrimSpace(setBy)
	if len(setBy) > maxContentLen {
		return nil, ErrContentTooLong
	}
	if _, err := b.pool.Exec(ctx,
		`INSERT INTO project_goal_roots (namespace_id, goal_id, set_by)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (namespace_id) DO UPDATE
		 SET goal_id = EXCLUDED.goal_id, set_by = EXCLUDED.set_by, updated_at = now()`,
		namespaceID, goalID, setBy,
	); err != nil {
		return nil, fmt.Errorf("set project goal: %w", err)
	}
	return b.GetProjectGoalTree(ctx, namespaceID)
}

type projectGoalRootRecord struct {
	ID        int64
	Status    string
	DeletedAt *time.Time
}

// readProjectGoalRoot reads the configured root even when its goal was later
// completed or soft-deleted. Callers that start work must reject that stale
// configuration explicitly instead of letting the database trigger fail later.
func readProjectGoalRoot(ctx context.Context, queryer workItemRowWriter, namespaceID int64) (*projectGoalRootRecord, error) {
	var root projectGoalRootRecord
	err := queryer.QueryRow(ctx,
		`SELECT root.goal_id, goal.status, goal.deleted_at
		 FROM project_goal_roots root
		 JOIN goals goal ON goal.id = root.goal_id AND goal.namespace_id = root.namespace_id
		 WHERE root.namespace_id = $1`,
		namespaceID,
	).Scan(&root.ID, &root.Status, &root.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read project goal: %w", err)
	}
	return &root, nil
}

// ProjectGoalRootID returns nil when a project has not selected a configured
// root. A completed root remains visible to history and the goal map, so its
// ID is still returned; execution paths use readProjectGoalRoot to check its
// status before binding or starting work.
func (b *Brain) ProjectGoalRootID(ctx context.Context, namespaceID int64) (*int64, error) {
	root, err := readProjectGoalRoot(ctx, b.pool, namespaceID)
	if err != nil {
		return nil, err
	}
	if root == nil || root.DeletedAt != nil {
		return nil, nil
	}
	return &root.ID, nil
}

func (b *Brain) resolveProjectGoalForWork(ctx context.Context, namespaceID int64, requested, fallback *int64) (*int64, error) {
	return b.resolveProjectGoalForWorkWith(ctx, b.pool, namespaceID, requested, fallback)
}

func (b *Brain) resolveProjectGoalForWorkTx(ctx context.Context, tx pgx.Tx, namespaceID int64, requested, fallback *int64) (*int64, error) {
	return b.resolveProjectGoalForWorkWith(ctx, tx, namespaceID, requested, fallback)
}

func (b *Brain) resolveProjectGoalForWorkWith(ctx context.Context, queryer workItemRowWriter, namespaceID int64, requested, fallback *int64) (*int64, error) {
	goalID := requested
	if goalID == nil {
		goalID = fallback
	}
	root, err := readProjectGoalRoot(ctx, queryer, namespaceID)
	if err != nil {
		return nil, err
	}
	var rootID *int64
	if root != nil {
		rootID = &root.ID
	}
	if goalID == nil {
		goalID = rootID
	}
	if goalID == nil {
		return nil, nil
	}
	var goal models.Goal
	if err := scanGoal(&goal, queryer.QueryRow(ctx, `SELECT `+goalColumns+` FROM goals WHERE id = $1 AND deleted_at IS NULL`, *goalID)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrGoalNotFound
		}
		return nil, fmt.Errorf("read work goal: %w", err)
	}
	if goal.NamespaceID != namespaceID {
		return nil, fmt.Errorf("%w: work goal must share the project namespace", ErrWorkGoalInvalid)
	}
	if goal.Status != "active" {
		return nil, fmt.Errorf("%w: work goal %d is %s", ErrGoalNotActive, goal.ID, goal.Status)
	}
	if rootID != nil {
		var belongs bool
		if err := queryer.QueryRow(ctx,
			`WITH RECURSIVE ancestors AS (
			    SELECT id, parent_id FROM goals WHERE id = $1 AND namespace_id = $2 AND deleted_at IS NULL
			    UNION ALL
			    SELECT parent.id, parent.parent_id
			    FROM goals parent JOIN ancestors child ON child.parent_id = parent.id
			    WHERE parent.namespace_id = $2 AND parent.deleted_at IS NULL
			)
			SELECT EXISTS (SELECT 1 FROM ancestors WHERE id = $3)`,
			*goalID, namespaceID, *rootID,
		).Scan(&belongs); err != nil {
			return nil, fmt.Errorf("check project goal lineage: %w", err)
		}
		if !belongs {
			return nil, fmt.Errorf("%w: work goal must belong to the shared project goal tree", ErrWorkGoalInvalid)
		}
	}
	resolved := *goalID
	return &resolved, nil
}

// GetProjectGoalTree returns the root goal and all of its descendants with
// progress rolled up from executable leaf work.
func (b *Brain) GetProjectGoalTree(ctx context.Context, namespaceID int64) (*models.GoalTree, error) {
	rootID, err := b.ProjectGoalRootID(ctx, namespaceID)
	if err != nil {
		return nil, err
	}
	return b.getGoalTree(ctx, namespaceID, rootID, false)
}

// The owner can browse independent outcomes without selecting a shared root.
func (b *Brain) getGoalTree(ctx context.Context, namespaceID int64, rootID *int64, includeIndependent bool) (*models.GoalTree, error) {
	tree := &models.GoalTree{RootGoalID: rootID, Goals: make([]models.GoalProgress, 0)}
	if rootID == nil && !includeIndependent {
		return tree, nil
	}

	rows, err := b.pool.Query(ctx,
		`WITH RECURSIVE goal_tree AS (
		    SELECT goal.id, goal.namespace_id, goal.parent_id, goal.content, goal.status,
		           goal.priority, goal.notes, goal.completed_at, goal.abandoned_at,
		           goal.created_at, goal.updated_at, goal.deleted_at,
		           0 AS depth, ARRAY[goal.id]::bigint[] AS path
		    FROM goals goal
		    WHERE (goal.id = $1 OR ($3 AND goal.parent_id IS NULL)) AND goal.namespace_id = $2 AND goal.deleted_at IS NULL
		    UNION ALL
		    SELECT child.id, child.namespace_id, child.parent_id, child.content, child.status,
		           child.priority, child.notes, child.completed_at, child.abandoned_at,
		           child.created_at, child.updated_at, child.deleted_at,
		           parent.depth + 1, parent.path || child.id
		    FROM goals child
		    JOIN goal_tree parent ON child.parent_id = parent.id
		    WHERE child.namespace_id = $2 AND child.deleted_at IS NULL
		      AND NOT child.id = ANY(parent.path)
		)
		SELECT id, namespace_id, parent_id, content, status, priority, notes,
		       completed_at, abandoned_at, created_at, updated_at, deleted_at, depth, path
		FROM goal_tree
		ORDER BY depth, priority DESC, id`,
		rootID, namespaceID, includeIndependent,
	)
	if err != nil {
		return nil, fmt.Errorf("read project goal tree: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var goal models.GoalProgress
		if err := rows.Scan(
			&goal.ID, &goal.NamespaceID, &goal.ParentID, &goal.Content, &goal.Status,
			&goal.Priority, &goal.Notes, &goal.CompletedAt, &goal.AbandonedAt,
			&goal.CreatedAt, &goal.UpdatedAt, &goal.DeletedAt, &goal.Depth, &goal.Path,
		); err != nil {
			return nil, fmt.Errorf("scan project goal tree: %w", err)
		}
		tree.Goals = append(tree.Goals, goal)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read project goal tree rows: %w", err)
	}
	if len(tree.Goals) == 0 && rootID != nil {
		return nil, ErrProjectGoalNotSet
	}

	goalIDs := make([]int64, 0, len(tree.Goals))
	for _, goal := range tree.Goals {
		goalIDs = append(goalIDs, goal.ID)
	}
	workRows, err := b.pool.Query(ctx,
		`SELECT item.goal_id,
		        count(*) FILTER (WHERE item.status <> 'canceled'),
		        count(*) FILTER (WHERE item.status = 'done')
		 FROM work_items item
		 WHERE item.goal_id = ANY($1) AND item.namespace_id = $2 AND item.deleted_at IS NULL
		   AND NOT EXISTS (
		       SELECT 1 FROM work_items child
		       WHERE child.parent_id = item.id AND child.deleted_at IS NULL
		   )
		   AND NOT EXISTS (
		       SELECT 1 FROM work_plan_items plan
		       WHERE plan.work_item_id = item.id AND plan.kind = 'component'
		   )
		 GROUP BY item.goal_id`,
		goalIDs, namespaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("count project goal work: %w", err)
	}
	workCounts := make(map[int64]goalWorkCount, len(goalIDs))
	for workRows.Next() {
		var goalID int64
		var count goalWorkCount
		if err := workRows.Scan(&goalID, &count.Total, &count.Done); err != nil {
			workRows.Close()
			return nil, fmt.Errorf("scan project goal work count: %w", err)
		}
		workCounts[goalID] = count
	}
	if err := workRows.Err(); err != nil {
		workRows.Close()
		return nil, fmt.Errorf("read project goal work counts: %w", err)
	}
	workRows.Close()

	tree.Goals = buildGoalProgress(tree.Goals, workCounts)
	return tree, nil
}

func buildGoalProgress(goals []models.GoalProgress, workCounts map[int64]goalWorkCount) []models.GoalProgress {
	result := append([]models.GoalProgress(nil), goals...)
	children := make(map[int64][]int, len(result))
	for index := range result {
		if result[index].ParentID != nil {
			children[*result[index].ParentID] = append(children[*result[index].ParentID], index)
		}
		count := workCounts[result[index].ID]
		result[index].DirectWorkTotal = count.Total
		result[index].DirectWorkDone = count.Done
	}

	order := make([]int, len(result))
	for index := range result {
		order[index] = index
	}
	sort.SliceStable(order, func(left, right int) bool {
		return result[order[left]].Depth > result[order[right]].Depth
	})
	for _, index := range order {
		goal := &result[index]
		goal.SubtreeWorkTotal = goal.DirectWorkTotal
		goal.SubtreeWorkDone = goal.DirectWorkDone
		for _, childIndex := range children[goal.ID] {
			child := result[childIndex]
			goal.ChildGoalTotal++
			if child.Status == "completed" {
				goal.ChildGoalCompleted++
			}
			goal.SubtreeWorkTotal += child.SubtreeWorkTotal
			goal.SubtreeWorkDone += child.SubtreeWorkDone
		}
		switch {
		case goal.SubtreeWorkTotal > 0:
			goal.Progress = float64(goal.SubtreeWorkDone) / float64(goal.SubtreeWorkTotal)
		case goal.ChildGoalTotal > 0:
			goal.Progress = float64(goal.ChildGoalCompleted) / float64(goal.ChildGoalTotal)
		case goal.Status == "completed":
			goal.Progress = 1
		default:
			goal.Progress = 0
		}
		goal.ReadyToComplete = goal.Status == "active" &&
			(goal.DirectWorkTotal > 0 || goal.ChildGoalTotal > 0) &&
			goal.DirectWorkDone == goal.DirectWorkTotal &&
			goal.ChildGoalCompleted == goal.ChildGoalTotal
		goal.CompletionMismatch = goal.Status == "completed" && goal.Progress < 1
	}
	return result
}

// LinkGoalMemory records why a goal exists or what constrains its outcome.
func (b *Brain) LinkGoalMemory(ctx context.Context, goalID int64, memoryType string, memoryID int64, relation string) (*models.GoalMemoryLink, error) {
	if memoryType == "goal" {
		return nil, fmt.Errorf("brain: goal hierarchy must use parent_id")
	}
	if _, ok := workMemoryTypes[memoryType]; !ok {
		return nil, fmt.Errorf("brain: invalid goal memory type %q", memoryType)
	}
	relation = strings.TrimSpace(relation)
	if _, ok := workMemoryRelations[relation]; !ok {
		return nil, fmt.Errorf("brain: invalid goal memory relation %q", relation)
	}
	goal, err := b.GetGoal(ctx, goalID)
	if err != nil {
		return nil, err
	}
	memoryNamespace, err := b.memoryNamespaceID(ctx, memoryType, memoryID)
	if err != nil {
		return nil, err
	}
	if goal.NamespaceID != memoryNamespace {
		return nil, fmt.Errorf("brain: goal and memory must share a namespace")
	}
	var link models.GoalMemoryLink
	if err := b.pool.QueryRow(ctx,
		`INSERT INTO goal_memory_links (goal_id, memory_type, memory_id, relation)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (goal_id, memory_type, memory_id) DO UPDATE SET relation = EXCLUDED.relation
		 RETURNING goal_id, memory_type, memory_id, relation, created_at`,
		goalID, memoryType, memoryID, relation,
	).Scan(&link.GoalID, &link.MemoryType, &link.MemoryID, &link.Relation, &link.CreatedAt); err != nil {
		return nil, fmt.Errorf("link goal memory: %w", err)
	}
	return &link, nil
}

func (b *Brain) ListGoalMemoryLinks(ctx context.Context, goalID int64) ([]models.GoalMemoryLink, error) {
	if _, err := b.GetGoal(ctx, goalID); err != nil {
		return nil, err
	}
	rows, err := b.pool.Query(ctx,
		`SELECT goal_id, memory_type, memory_id, relation, created_at
		 FROM goal_memory_links WHERE goal_id = $1
		 ORDER BY created_at, memory_type, memory_id`, goalID,
	)
	if err != nil {
		return nil, fmt.Errorf("list goal memory links: %w", err)
	}
	defer rows.Close()
	links := make([]models.GoalMemoryLink, 0)
	for rows.Next() {
		var link models.GoalMemoryLink
		if err := rows.Scan(&link.GoalID, &link.MemoryType, &link.MemoryID, &link.Relation, &link.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan goal memory link: %w", err)
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

// GetWorkGoalContext returns the shared root, the current goal path, and the
// sibling outcomes an agent can affect indirectly while doing one work item.
func (b *Brain) GetWorkGoalContext(ctx context.Context, workItemID int64) (*models.WorkGoalContext, error) {
	item, err := b.GetWorkItem(ctx, workItemID)
	if err != nil {
		return nil, err
	}
	tree, err := b.GetProjectGoalTree(ctx, item.NamespaceID)
	if err != nil {
		if errors.Is(err, ErrProjectGoalNotSet) {
			return &models.WorkGoalContext{Path: []models.GoalBrief{}, Siblings: []models.GoalBrief{}}, nil
		}
		return nil, err
	}
	goalContext := &models.WorkGoalContext{
		RootGoalID: tree.RootGoalID,
		Path:       []models.GoalBrief{},
		Siblings:   []models.GoalBrief{},
	}
	goalContext.ContextDigest, err = goalContextDigest(tree, item.GoalID)
	if err != nil {
		return nil, err
	}
	if len(tree.Goals) == 0 {
		return goalContext, nil
	}
	byID := make(map[int64]models.GoalProgress, len(tree.Goals))
	for _, goal := range tree.Goals {
		byID[goal.ID] = goal
	}
	if item.GoalID == nil {
		if tree.RootGoalID != nil {
			if root, ok := byID[*tree.RootGoalID]; ok {
				goalContext.Path = append(goalContext.Path, compactGoalBrief(root))
				goalContext.PathTotal = 1
			}
		}
		return goalContext, nil
	}
	current, ok := byID[*item.GoalID]
	if !ok {
		return goalContext, nil
	}
	currentID := current.ID
	goalContext.CurrentGoalID = &currentID
	path := make([]models.GoalBrief, 0, len(current.Path))
	for _, id := range current.Path {
		if goal, exists := byID[id]; exists {
			path = append(path, compactGoalBrief(goal))
		}
	}
	goalContext.PathTotal = len(path)
	if len(path) > goalContextPathLimit {
		path = append([]models.GoalBrief{path[0]}, path[len(path)-(goalContextPathLimit-1):]...)
		goalContext.PathTruncated = true
	}
	goalContext.Path = path
	seenSiblings := make(map[int64]struct{})
	for _, pathGoal := range current.Path {
		pathGoalValue, exists := byID[pathGoal]
		if !exists {
			continue
		}
		if pathGoalValue.ParentID == nil {
			continue
		}
		for _, candidate := range tree.Goals {
			if candidate.ID == pathGoalValue.ID || candidate.ParentID == nil || *candidate.ParentID != *pathGoalValue.ParentID {
				continue
			}
			if _, exists := seenSiblings[candidate.ID]; exists {
				continue
			}
			seenSiblings[candidate.ID] = struct{}{}
			goalContext.Siblings = append(goalContext.Siblings, compactGoalBrief(candidate))
		}
	}
	goalContext.SiblingTotal = len(goalContext.Siblings)
	if len(goalContext.Siblings) > goalContextPeerLimit {
		goalContext.Siblings = goalContext.Siblings[:goalContextPeerLimit]
		goalContext.SiblingsTruncated = true
	}
	return goalContext, nil
}

func compactGoalBrief(goal models.GoalProgress) models.GoalBrief {
	return models.GoalBrief{
		ID: goal.ID, ParentID: goal.ParentID, Content: compactGoalMapText(goal.Content, goalBriefContentLimit), Status: goal.Status,
		Progress: goal.Progress, SubtreeWorkDone: goal.SubtreeWorkDone, SubtreeWorkTotal: goal.SubtreeWorkTotal,
		ChildGoalCompleted: goal.ChildGoalCompleted, ChildGoalTotal: goal.ChildGoalTotal,
	}
}

func compactGoalMapText(value string, limit int) string {
	content := []rune(strings.TrimSpace(value))
	if limit <= 0 || len(content) <= limit {
		return string(content)
	}
	return string(append(content[:limit-1], '…'))
}

func compactGoalMapGoal(goal models.GoalProgress) models.GoalMapGoal {
	return models.GoalMapGoal{
		ID: goal.ID, ParentID: goal.ParentID, Content: compactGoalMapText(goal.Content, goalBriefContentLimit),
		Status: goal.Status, Depth: goal.Depth,
		DirectWorkTotal: goal.DirectWorkTotal, DirectWorkDone: goal.DirectWorkDone,
		SubtreeWorkTotal: goal.SubtreeWorkTotal, SubtreeWorkDone: goal.SubtreeWorkDone,
		ChildGoalTotal: goal.ChildGoalTotal, ChildGoalCompleted: goal.ChildGoalCompleted,
		Progress: goal.Progress, ReadyToComplete: goal.ReadyToComplete, CompletionMismatch: goal.CompletionMismatch,
	}
}

func compactGoalMapWork(item models.WorkItem) models.GoalMapWork {
	return models.GoalMapWork{
		ID: item.ID, GoalID: item.GoalID, ParentID: item.ParentID,
		IssueKey: compactGoalMapText(item.IssueKey, 96), Title: compactGoalMapText(item.Title, 256),
		Status: item.Status, Priority: item.Priority, Position: item.Position,
		Owner: compactGoalMapText(item.Owner, 128),
	}
}

func goalContextDigest(tree *models.GoalTree, currentGoalID *int64) (string, error) {
	type digestGoal struct {
		ID       int64   `json:"id"`
		ParentID *int64  `json:"parent_id,omitempty"`
		Content  string  `json:"content"`
		Status   string  `json:"status"`
		Progress float64 `json:"progress"`
	}
	input := struct {
		RootID    *int64       `json:"root_id,omitempty"`
		CurrentID *int64       `json:"current_id,omitempty"`
		Goals     []digestGoal `json:"goals"`
	}{RootID: tree.RootGoalID, CurrentID: currentGoalID, Goals: make([]digestGoal, 0, len(tree.Goals))}
	for _, goal := range tree.Goals {
		input.Goals = append(input.Goals, digestGoal{
			ID: goal.ID, ParentID: goal.ParentID, Content: goal.Content, Status: goal.Status, Progress: goal.Progress,
		})
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("digest work goal context: %w", err)
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("sha256:%x", digest), nil
}

func goalMapNodeKey(kind string, id int64) string {
	return fmt.Sprintf("%s:%d", kind, id)
}

func goalMapMemoryKey(memoryType string, memoryID int64) string {
	return fmt.Sprintf("memory:%s:%d", memoryType, memoryID)
}

func goalMapResourceKey(resourceID int64) string {
	return fmt.Sprintf("resource:%d", resourceID)
}

func goalMapEdgeDirection(ownerKey, memoryKey, relation string) (string, string) {
	switch relation {
	case "evidence", "result", "supersedes":
		return ownerKey, memoryKey
	default:
		return memoryKey, ownerKey
	}
}

// GetGoalMap returns the complete memory -> work -> nested goal projection for
// one project namespace. Progress always includes completed work even when the
// caller hides completed work cards.
func (b *Brain) GetGoalMap(ctx context.Context, namespaceID int64, includeDone bool) (*models.GoalMap, error) {
	rootID, err := b.ProjectGoalRootID(ctx, namespaceID)
	if err != nil {
		return nil, err
	}
	tree, err := b.getGoalTree(ctx, namespaceID, rootID, true)
	if err != nil {
		return nil, err
	}
	result := &models.GoalMap{
		GoalTree:       models.GoalMapTree{RootGoalID: tree.RootGoalID, Goals: make([]models.GoalMapGoal, 0, len(tree.Goals))},
		RootCandidates: make([]models.GoalBrief, 0),
		WorkItems:      make([]models.GoalMapWork, 0),
		Resources:      make([]models.GoalMapResource, 0),
		Memories:       make([]models.GoalMapMemory, 0),
		Edges:          make([]models.GoalMapEdge, 0),
		UnassignedWork: make([]models.GoalMapWork, 0),
	}
	if tree.RootGoalID == nil {
		rows, err := b.pool.Query(ctx,
			`SELECT `+goalColumns+`
			 FROM goals
			 WHERE namespace_id = $1 AND parent_id IS NULL AND status = 'active' AND deleted_at IS NULL
			 ORDER BY priority DESC, created_at, id`, namespaceID,
		)
		if err != nil {
			return nil, fmt.Errorf("list project goal candidates: %w", err)
		}
		candidates, err := scanGoalRows(rows)
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("scan project goal candidates: %w", err)
		}

		for _, goal := range candidates {
			result.RootCandidates = append(result.RootCandidates, compactGoalBrief(models.GoalProgress{Goal: goal}))
		}
	}
	goalIDs := make([]int64, 0, len(tree.Goals))
	goalSet := make(map[int64]struct{}, len(tree.Goals))
	for _, goal := range tree.Goals {
		result.GoalTree.Goals = append(result.GoalTree.Goals, compactGoalMapGoal(goal))
		goalIDs = append(goalIDs, goal.ID)
		goalSet[goal.ID] = struct{}{}
		if goal.ParentID != nil {
			result.Edges = append(result.Edges, models.GoalMapEdge{
				Key: goalMapNodeKey("goal-tree", goal.ID), From: goalMapNodeKey("goal", goal.ID),
				To: goalMapNodeKey("goal", *goal.ParentID), Relation: "contributes_to",
			})
		}
	}

	statusClause := ""
	if !includeDone {
		statusClause = " AND item.status NOT IN ('done', 'canceled')"
	}
	rows, err := b.pool.Query(ctx,
		`SELECT `+workItemColumns+`
		 FROM work_items item
		 WHERE item.namespace_id = $1 AND item.deleted_at IS NULL`+statusClause+`
		 ORDER BY item.priority DESC, item.position, item.id`, namespaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("list goal map work: %w", err)
	}
	items, err := scanWorkItemRows(rows)
	if err != nil {
		return nil, err
	}
	items, err = b.attachWorktreeIDs(ctx, items)
	if err != nil {
		return nil, err
	}
	progressByID, err := b.componentProgress(ctx, namespaceID)
	if err != nil {
		return nil, err
	}
	monitorByID := make(map[int64]models.GoalMapWork, len(items))
	itemIDs := make([]int64, 0, len(items))
	for _, item := range items {
		entry := compactGoalMapWork(item)
		if progress, ok := progressByID[item.ID]; ok {
			entry.ExecutionProgress = &progress
		}
		monitorByID[item.ID] = entry
		itemIDs = append(itemIDs, item.ID)
	}
	if len(itemIDs) > 0 {
		monitorRows, err := b.pool.Query(ctx,
			`SELECT item.id,
			        coalesce(attempt.agent_id, ''), coalesce(attempt.status, ''), attempt.lease_expires_at,
			        coalesce(checkpoint.result, ''),
			        coalesce(nullif(state.current_next_action, ''), checkpoint.next_action, ''),
			        coalesce(ARRAY(SELECT capability FROM work_item_capabilities required
			                       WHERE required.work_item_id = item.id ORDER BY capability), '{}'::text[])
			 FROM work_items item
			 LEFT JOIN LATERAL (
			     SELECT candidate.id, candidate.agent_id, candidate.status, candidate.lease_expires_at
			     FROM work_attempts candidate
			     WHERE candidate.work_item_id = item.id
			     ORDER BY candidate.started_at DESC, candidate.id DESC LIMIT 1
			 ) attempt ON true
			 LEFT JOIN LATERAL (
			     SELECT candidate.result, candidate.next_action
			     FROM work_checkpoints candidate
			     JOIN work_attempts checkpoint_attempt ON checkpoint_attempt.id = candidate.attempt_id
			     WHERE checkpoint_attempt.work_item_id = item.id
			     ORDER BY candidate.created_at DESC, candidate.id DESC LIMIT 1
			 ) checkpoint ON true
			 LEFT JOIN work_execution_states state ON state.work_item_id = item.id
			 WHERE item.id = ANY($1)`, itemIDs)
		if err != nil {
			return nil, fmt.Errorf("read goal map execution summaries: %w", err)
		}
		for monitorRows.Next() {
			var id int64
			var agentID, attemptStatus, latestResult, nextAction string
			var leaseExpires *time.Time
			var requiredCapabilities []string
			if err := monitorRows.Scan(&id, &agentID, &attemptStatus, &leaseExpires, &latestResult, &nextAction, &requiredCapabilities); err != nil {
				monitorRows.Close()
				return nil, fmt.Errorf("scan goal map execution summary: %w", err)
			}
			entry := monitorByID[id]
			entry.AgentID = compactGoalMapText(agentID, 128)
			entry.AttemptStatus = attemptStatus
			entry.LeaseExpires = leaseExpires
			entry.LatestResult = compactGoalMapText(latestResult, 384)
			entry.NextAction = compactGoalMapText(nextAction, 384)
			entry.RequiredCapabilities = requiredCapabilities
			monitorByID[id] = entry
		}
		if err := monitorRows.Err(); err != nil {
			monitorRows.Close()
			return nil, fmt.Errorf("read goal map execution summary rows: %w", err)
		}
		monitorRows.Close()
	}
	workSet := make(map[int64]struct{}, len(items))
	for _, item := range items {
		monitored := monitorByID[item.ID]
		workSet[item.ID] = struct{}{}
		if item.GoalID == nil {
			result.UnassignedWork = append(result.UnassignedWork, monitored)
			continue
		}
		if _, ok := goalSet[*item.GoalID]; !ok {
			result.UnassignedWork = append(result.UnassignedWork, monitored)
			continue
		}
		result.WorkItems = append(result.WorkItems, monitored)
		result.Edges = append(result.Edges, models.GoalMapEdge{
			Key: goalMapNodeKey("work-goal", item.ID), From: goalMapNodeKey("work", item.ID),
			To: goalMapNodeKey("goal", *item.GoalID), Relation: "contributes_to",
		})
	}
	for _, item := range append(append([]models.GoalMapWork{}, result.WorkItems...), result.UnassignedWork...) {
		if item.ParentID == nil {
			continue
		}
		if _, ok := workSet[*item.ParentID]; ok {
			result.Edges = append(result.Edges, models.GoalMapEdge{
				Key: goalMapNodeKey("work-parent", item.ID), From: goalMapNodeKey("work", item.ID),
				To: goalMapNodeKey("work", *item.ParentID), Relation: "part_of",
			})
		}
	}

	if len(workSet) > 0 {
		workIDs := make([]int64, 0, len(workSet))
		for id := range workSet {
			workIDs = append(workIDs, id)
		}
		edgeRows, err := b.pool.Query(ctx,
			`SELECT edge.id, edge.from_item_id, edge.to_item_id, edge.edge_type
			 FROM work_item_edges edge
			 WHERE edge.from_item_id = ANY($1) AND edge.to_item_id = ANY($1) AND edge.deleted_at IS NULL
			 ORDER BY edge.id`, workIDs,
		)
		if err != nil {
			return nil, fmt.Errorf("list goal map work edges: %w", err)
		}
		for edgeRows.Next() {
			var id, fromID, toID int64
			var relation string
			if err := edgeRows.Scan(&id, &fromID, &toID, &relation); err != nil {
				edgeRows.Close()
				return nil, fmt.Errorf("scan goal map work edge: %w", err)
			}
			result.Edges = append(result.Edges, models.GoalMapEdge{
				Key: goalMapNodeKey("work-edge", id), From: goalMapNodeKey("work", fromID),
				To: goalMapNodeKey("work", toID), Relation: relation,
			})
		}
		if err := edgeRows.Err(); err != nil {
			edgeRows.Close()
			return nil, fmt.Errorf("read goal map work edges: %w", err)
		}
		edgeRows.Close()

		if err := b.pool.QueryRow(ctx,
			`SELECT count(DISTINCT resource.id) FROM work_resource_links linked
			 JOIN work_resources resource ON resource.id = linked.resource_id
			 WHERE linked.work_item_id = ANY($1) AND linked.namespace_id = $2
			   AND resource.namespace_id = $2 AND resource.deleted_at IS NULL`,
			workIDs, namespaceID,
		).Scan(&result.ResourceTotal); err != nil {
			return nil, fmt.Errorf("count goal map resources: %w", err)
		}
		resourceRows, err := b.pool.Query(ctx,
			`SELECT resource.id, resource.kind, resource.source, resource.authority, resource.title,
			        resource.uri, resource.summary, resource.external_id, resource.revision,
			        linked.work_item_id, linked.role
			 FROM work_resource_links linked
			 JOIN work_resources resource ON resource.id = linked.resource_id
			 WHERE linked.work_item_id = ANY($1) AND linked.namespace_id = $2
			   AND resource.namespace_id = $2 AND resource.deleted_at IS NULL
			 ORDER BY linked.created_at, linked.id`,
			workIDs, namespaceID,
		)
		if err != nil {
			return nil, fmt.Errorf("list goal map resources: %w", err)
		}
		resourceSeen := make(map[int64]struct{})
		for resourceRows.Next() {
			var resource models.GoalMapResource
			var workItemID int64
			var role string
			if err := resourceRows.Scan(
				&resource.ID, &resource.Kind, &resource.Source, &resource.Authority, &resource.Title,
				&resource.URI, &resource.Summary, &resource.ExternalID, &resource.Revision,
				&workItemID, &role,
			); err != nil {
				resourceRows.Close()
				return nil, fmt.Errorf("scan goal map resource: %w", err)
			}
			resource.Key = goalMapResourceKey(resource.ID)
			resource.Title = compactGoalMapText(resource.Title, 256)
			resource.URI = compactGoalMapText(resource.URI, 512)
			resource.Summary = compactGoalMapText(resource.Summary, 256)
			resource.ExternalID = compactGoalMapText(resource.ExternalID, 128)
			resource.Revision = compactGoalMapText(resource.Revision, 128)
			if _, exists := resourceSeen[resource.ID]; !exists {
				resourceSeen[resource.ID] = struct{}{}
				result.Resources = append(result.Resources, resource)
			}
			from, to := resource.Key, goalMapNodeKey("work", workItemID)
			if role == "output" || role == "evidence" {
				from, to = to, from
			}
			result.Edges = append(result.Edges, models.GoalMapEdge{
				Key:  fmt.Sprintf("resource-edge:%d:%d:%s", resource.ID, workItemID, role),
				From: from, To: to, Relation: role,
			})
		}
		if err := resourceRows.Err(); err != nil {
			resourceRows.Close()
			return nil, fmt.Errorf("read goal map resource rows: %w", err)
		}
		resourceRows.Close()
		result.ResourcesTruncated = result.ResourceTotal > len(result.Resources)
	}

	if len(goalIDs) == 0 && len(itemIDs) == 0 {
		return result, nil
	}
	ownerRows, err := b.pool.Query(ctx,
		`WITH links AS (
		    SELECT 'goal'::text AS owner_type, linked.goal_id AS owner_id,
		           linked.memory_type, linked.memory_id, linked.relation, linked.created_at
		    FROM goal_memory_links linked WHERE linked.goal_id = ANY($1)
		    UNION ALL
		    SELECT 'goal'::text, failure.goal_id, 'failure'::text, failure.id, 'failure'::text, failure.created_at
		    FROM failures failure
		    WHERE failure.goal_id = ANY($1) AND failure.deleted_at IS NULL
		    UNION ALL
		    SELECT 'work'::text, linked.work_item_id, linked.memory_type, linked.memory_id, linked.relation, linked.created_at
		    FROM work_item_memory_links linked WHERE linked.work_item_id = ANY($2) AND linked.memory_type <> 'goal'
		), snapshots AS (
		    SELECT links.owner_type, links.owner_id, links.memory_type, links.memory_id, links.relation,
		           left(memory.content, $3) AS content, 'recorded'::text AS status,
		           char_length(memory.content) > $3 AS content_truncated, links.created_at
		    FROM links JOIN episodes memory ON links.memory_type = 'episode' AND memory.id = links.memory_id
		    WHERE memory.namespace_id = $4 AND memory.deleted_at IS NULL
		    UNION ALL
		    SELECT links.owner_type, links.owner_id, links.memory_type, links.memory_id, links.relation,
		           left(memory.content, $3), 'active'::text,
		           char_length(memory.content) > $3, links.created_at
		    FROM links JOIN facts memory ON links.memory_type = 'fact' AND memory.id = links.memory_id
		    WHERE memory.namespace_id = $4 AND memory.deleted_at IS NULL AND memory.valid_until IS NULL
		    UNION ALL
		    SELECT links.owner_type, links.owner_id, links.memory_type, links.memory_id, links.relation,
		           left(memory.content, $3), memory.status,
		           char_length(memory.content) > $3, links.created_at
		    FROM links JOIN hypotheses memory ON links.memory_type = 'hypothesis' AND memory.id = links.memory_id
		    WHERE memory.namespace_id = $4 AND memory.deleted_at IS NULL
		    UNION ALL
		    SELECT links.owner_type, links.owner_id, links.memory_type, links.memory_id, links.relation,
		           left(memory.content, $3), 'recorded'::text,
		           char_length(memory.content) > $3, links.created_at
		    FROM links JOIN failures memory ON links.memory_type = 'failure' AND memory.id = links.memory_id
		    WHERE memory.namespace_id = $4 AND memory.deleted_at IS NULL
		)
		SELECT owner_type, owner_id, memory_type, memory_id, relation, content, status, content_truncated, created_at
		FROM snapshots ORDER BY created_at, owner_type, owner_id, memory_type, memory_id`,
		goalIDs, itemIDs, goalMapMemoryContentLimit, namespaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("read goal map memory: %w", err)
	}
	memorySeen := make(map[string]struct{})
	edgeSeen := make(map[string]struct{})
	for ownerRows.Next() {
		var ownerType, memoryType, relation, content, status string
		var ownerID, memoryID int64
		var contentTruncated bool
		var ignoredCreatedAt time.Time
		if err := ownerRows.Scan(&ownerType, &ownerID, &memoryType, &memoryID, &relation, &content, &status, &contentTruncated, &ignoredCreatedAt); err != nil {
			ownerRows.Close()
			return nil, fmt.Errorf("scan goal map memory: %w", err)
		}
		memoryKey := goalMapMemoryKey(memoryType, memoryID)
		if _, ok := memorySeen[memoryKey]; !ok {
			memorySeen[memoryKey] = struct{}{}
			result.Memories = append(result.Memories, models.GoalMapMemory{
				Key: memoryKey, MemoryType: memoryType, MemoryID: memoryID,
				Content: content, Status: status, ContentTruncated: contentTruncated,
			})
		}
		ownerKey := goalMapNodeKey(ownerType, ownerID)
		from, to := goalMapEdgeDirection(ownerKey, memoryKey, relation)
		edgeKey := fmt.Sprintf("memory-edge:%s:%s:%s", from, to, relation)
		if _, ok := edgeSeen[edgeKey]; ok {
			continue
		}
		edgeSeen[edgeKey] = struct{}{}
		result.Edges = append(result.Edges, models.GoalMapEdge{Key: edgeKey, From: from, To: to, Relation: relation})
	}
	if err := ownerRows.Err(); err != nil {
		ownerRows.Close()
		return nil, fmt.Errorf("read goal map memory rows: %w", err)
	}
	ownerRows.Close()
	if err := b.appendGoalMapFactSources(ctx, namespaceID, result); err != nil {
		return nil, err
	}
	return result, nil
}

func mapKeys(values map[int64]struct{}) []int64 {
	keys := make([]int64, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
