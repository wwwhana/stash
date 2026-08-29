package brain

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/alash3al/stash/internal/models"
	"github.com/alash3al/stash/internal/reasoner"
	"github.com/jackc/pgx/v5"
)

const (
	workPlanComponentKind = "component"
	workPlanTaskKind      = "task"
)

var (
	ErrWorkPlanItemNotFound          = fmt.Errorf("brain: work plan item not found")
	ErrWorkPlanComponentNotFound     = fmt.Errorf("brain: work plan component not found")
	ErrWorkPlanTaskNotFound          = fmt.Errorf("brain: work plan task not found")
	ErrWorkPlanTaskAlreadyDone       = fmt.Errorf("brain: completed work plan task must be reopened before it can start")
	ErrWorkPlanTaskAlreadyActive     = fmt.Errorf("brain: work plan task is already active for another agent")
	ErrWorkPlanTaskNotStarted        = fmt.Errorf("brain: start the work plan task before completing it")
	ErrWorkPlanTaskNotBlocked        = fmt.Errorf("brain: work plan task is not blocked")
	ErrWorkPlanInvalidProvenance     = fmt.Errorf("brain: invalid work plan provenance")
	ErrWorkPlanInvalidRelationship   = fmt.Errorf("brain: invalid work plan component relationship")
	ErrWorkPlanValidationUnavailable = fmt.Errorf("brain: configured reasoner does not support work plan validation")
	ErrWorkPlanValidationEmpty       = fmt.Errorf("brain: create a work plan component before validation")
)

// WorkPlanComponentInput creates one owner-facing system component. It is a
// parent work item; executable work lives in child WorkPlanTask records.
type WorkPlanComponentInput struct {
	GoalID           *int64
	Title            string
	Description      string
	TechnicalDetails string
	OwnedPaths       []string
	Labels           []string
	Reporter         string
	Owner            string
	Status           string
	Priority         int
	Position         float64
}

// WorkPlanTaskInput creates an executable task under a plan component.
type WorkPlanTaskInput struct {
	ComponentID      int64
	GoalID           *int64
	Title            string
	Description      string
	TechnicalDetails string
	Labels           []string
	Reporter         string
	Provenance       string
	Priority         int
	Position         float64
}

// WorkPlanComponentUpdate changes owner-facing wording and technical metadata
// without changing the component's stable work item identity.
type WorkPlanComponentUpdate struct {
	GoalID           *int64
	Title            *string
	Description      *string
	TechnicalDetails *string
	OwnedPaths       *[]string
}

// WorkPlanTaskUpdate changes an executable task without moving it to a
// different component or rewriting its lifecycle history.
type WorkPlanTaskUpdate struct {
	GoalID           *int64
	Title            *string
	Description      *string
	TechnicalDetails *string
	Provenance       *string
}

// WorkPlanDecisionInput is written before implementing a decision that
// changes the plan. It may target the whole plan, a component, or a task.
type WorkPlanDecisionInput struct {
	ComponentID *int64
	WorkItemID  *int64
	Title       string
	Rationale   string
	Author      string
}

type workPlanItemMetadata struct {
	Kind             string
	TechnicalDetails string
	OwnedPaths       []string
	Provenance       string
	StartedBy        string
	CompletedBy      string
}

type workPlanItemRecord struct {
	Item     models.WorkItem
	Metadata workPlanItemMetadata
}

type workPlanDigestTask struct {
	ID               int64  `json:"id"`
	GoalID           int64  `json:"goal_id,omitempty"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	TechnicalDetails string `json:"technical_details"`
}

type workPlanDigestComponent struct {
	ID               int64                `json:"id"`
	GoalID           int64                `json:"goal_id,omitempty"`
	IssueKey         string               `json:"issue_key"`
	Title            string               `json:"title"`
	Description      string               `json:"description"`
	TechnicalDetails string               `json:"technical_details"`
	OwnedPaths       []string             `json:"owned_paths"`
	Needs            []int64              `json:"needs"`
	Links            []int64              `json:"links"`
	Tasks            []workPlanDigestTask `json:"tasks"`
}

type workPlanDigestDecision struct {
	ID          int64  `json:"id"`
	ComponentID int64  `json:"component_id,omitempty"`
	WorkItemID  int64  `json:"work_item_id,omitempty"`
	Title       string `json:"title"`
	Rationale   string `json:"rationale"`
}

type workPlanDigestInput struct {
	RootGoalID int64                     `json:"root_goal_id,omitempty"`
	Components []workPlanDigestComponent `json:"components"`
	Decisions  []workPlanDigestDecision  `json:"decisions"`
}

const workPlanItemColumns = `wi.id, wi.namespace_id, wi.goal_id, wi.parent_id, wi.issue_key, wi.issue_type, wi.labels, wi.reporter,
 wi.title, wi.description, wi.status, wi.priority, wi.position, wi.owner, wi.due_at, wi.started_at,
 wi.completed_at, wi.created_at, wi.updated_at, wi.deleted_at,
 pi.kind, pi.technical_details, pi.owned_paths, pi.provenance, pi.started_by, pi.completed_by`

func normalizeWorkPlanText(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) > maxContentLen {
		return "", ErrContentTooLong
	}
	return value, nil
}

func normalizeWorkPlanPaths(paths []string) ([]string, error) {
	result := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if len(path) > 512 || strings.ContainsAny(path, "\r\n\x00") {
			return nil, fmt.Errorf("brain: invalid work plan owned path")
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		result = append(result, path)
		if len(result) > 64 {
			return nil, fmt.Errorf("brain: work plan component has too many owned paths (max 64)")
		}
	}
	return result, nil
}

func normalizeWorkPlanProvenance(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "agent" || value == "roadmap" {
		return value, nil
	}
	return "", ErrWorkPlanInvalidProvenance
}

func scanWorkPlanItem(row pgx.Row) (workPlanItemRecord, error) {
	var record workPlanItemRecord
	err := row.Scan(
		&record.Item.ID, &record.Item.NamespaceID, &record.Item.GoalID, &record.Item.ParentID,
		&record.Item.IssueKey, &record.Item.IssueType, &record.Item.Labels, &record.Item.Reporter,
		&record.Item.Title, &record.Item.Description, &record.Item.Status, &record.Item.Priority,
		&record.Item.Position, &record.Item.Owner, &record.Item.DueAt, &record.Item.StartedAt,
		&record.Item.CompletedAt, &record.Item.CreatedAt, &record.Item.UpdatedAt, &record.Item.DeletedAt,
		&record.Metadata.Kind, &record.Metadata.TechnicalDetails, &record.Metadata.OwnedPaths,
		&record.Metadata.Provenance, &record.Metadata.StartedBy, &record.Metadata.CompletedBy,
	)
	return record, err
}

func scanWorkPlanItemRows(rows pgx.Rows) ([]workPlanItemRecord, error) {
	defer rows.Close()
	result := make([]workPlanItemRecord, 0)
	for rows.Next() {
		record, err := scanWorkPlanItem(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (b *Brain) getWorkPlanItem(ctx context.Context, writer workItemRowWriter, id int64, lock bool) (workPlanItemRecord, error) {
	query := `SELECT ` + workPlanItemColumns + `
		FROM work_items wi
		JOIN work_plan_items pi ON pi.work_item_id = wi.id
		WHERE wi.id = $1 AND wi.deleted_at IS NULL`
	if lock {
		query += ` FOR UPDATE OF wi, pi`
	}
	record, err := scanWorkPlanItem(writer.QueryRow(ctx, query, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return workPlanItemRecord{}, ErrWorkPlanItemNotFound
	}
	if err != nil {
		return workPlanItemRecord{}, fmt.Errorf("get work plan item: %w", err)
	}
	return record, nil
}

func (b *Brain) getWorkPlanComponent(ctx context.Context, id int64) (*models.WorkPlanComponent, error) {
	record, err := b.getWorkPlanItem(ctx, b.pool, id, false)
	if err != nil {
		return nil, err
	}
	if record.Metadata.Kind != workPlanComponentKind {
		return nil, ErrWorkPlanComponentNotFound
	}
	items, err := b.attachWorktreeIDs(ctx, []models.WorkItem{record.Item})
	if err != nil {
		return nil, err
	}
	component := workPlanComponentFromRecord(record)
	component.WorkItem = items[0]
	return &component, nil
}

func (b *Brain) getWorkPlanTask(ctx context.Context, id int64) (*models.WorkPlanTask, error) {
	record, err := b.getWorkPlanItem(ctx, b.pool, id, false)
	if err != nil {
		return nil, err
	}
	if record.Metadata.Kind != workPlanTaskKind {
		return nil, ErrWorkPlanTaskNotFound
	}
	items, err := b.attachWorktreeIDs(ctx, []models.WorkItem{record.Item})
	if err != nil {
		return nil, err
	}
	task := workPlanTaskFromRecord(record)
	task.WorkItem = items[0]
	return &task, nil
}

func workPlanTaskFromRecord(record workPlanItemRecord) models.WorkPlanTask {
	return models.WorkPlanTask{
		WorkItem:         record.Item,
		TechnicalDetails: record.Metadata.TechnicalDetails,
		Provenance:       record.Metadata.Provenance,
		StartedBy:        record.Metadata.StartedBy,
		CompletedBy:      record.Metadata.CompletedBy,
	}
}

func workPlanComponentFromRecord(record workPlanItemRecord) models.WorkPlanComponent {
	return models.WorkPlanComponent{
		WorkItem:         record.Item,
		TechnicalDetails: record.Metadata.TechnicalDetails,
		OwnedPaths:       record.Metadata.OwnedPaths,
		Tasks:            []models.WorkPlanTask{},
		Needs:            []models.WorkPlanReference{},
		Links:            []models.WorkPlanReference{},
	}
}

// CreateWorkPlanComponent adds a stable parent node to the owner-facing plan.
func (b *Brain) CreateWorkPlanComponent(ctx context.Context, namespaceID int64, input WorkPlanComponentInput) (*models.WorkPlanComponent, error) {
	technicalDetails, err := normalizeWorkPlanText(input.TechnicalDetails)
	if err != nil {
		return nil, fmt.Errorf("brain: component technical details: %w", err)
	}
	paths, err := normalizeWorkPlanPaths(input.OwnedPaths)
	if err != nil {
		return nil, err
	}
	if input.Status == "" {
		input.Status = "ready"
	}
	goalID, err := b.resolveProjectGoalForWork(ctx, namespaceID, input.GoalID, nil)
	if err != nil {
		return nil, err
	}

	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create work plan component: %w", err)
	}
	defer tx.Rollback(ctx)

	item, err := b.insertWorkItem(ctx, tx, namespaceID, WorkItemInput{
		GoalID: goalID, IssueType: "component", Labels: input.Labels, Reporter: input.Reporter,
		Title: input.Title, Description: input.Description, Status: input.Status,
		Priority: input.Priority, Position: input.Position, Owner: input.Owner,
	})
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO work_plan_items (work_item_id, kind, technical_details, owned_paths)
		 VALUES ($1, $2, $3, $4)`,
		item.ID, workPlanComponentKind, technicalDetails, paths,
	)
	if err != nil {
		return nil, fmt.Errorf("create work plan component metadata: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create work plan component: %w", err)
	}
	return b.getWorkPlanComponent(ctx, item.ID)
}

// UpdateWorkPlanComponent edits a component while preserving its issue key,
// status, worktree links, tasks, and lifecycle history.
func (b *Brain) UpdateWorkPlanComponent(ctx context.Context, componentID int64, input WorkPlanComponentUpdate) (*models.WorkPlanComponent, error) {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update work plan component: %w", err)
	}
	defer tx.Rollback(ctx)

	current, err := b.getWorkPlanItem(ctx, tx, componentID, true)
	if err != nil {
		return nil, err
	}
	if current.Metadata.Kind != workPlanComponentKind {
		return nil, ErrWorkPlanComponentNotFound
	}
	goalID := current.Item.GoalID
	if input.GoalID != nil {
		goalID, err = b.resolveProjectGoalForWork(ctx, current.Item.NamespaceID, input.GoalID, nil)
		if err != nil {
			return nil, err
		}
	}
	title := current.Item.Title
	if input.Title != nil {
		title = *input.Title
	}
	description := current.Item.Description
	if input.Description != nil {
		description = *input.Description
	}
	technicalDetails := current.Metadata.TechnicalDetails
	if input.TechnicalDetails != nil {
		technicalDetails, err = normalizeWorkPlanText(*input.TechnicalDetails)
		if err != nil {
			return nil, fmt.Errorf("brain: component technical details: %w", err)
		}
	}
	paths := current.Metadata.OwnedPaths
	if input.OwnedPaths != nil {
		paths, err = normalizeWorkPlanPaths(*input.OwnedPaths)
		if err != nil {
			return nil, err
		}
	}
	_, err = b.updateWorkItem(ctx, tx, componentID, WorkItemInput{
		IssueType: current.Item.IssueType, Labels: current.Item.Labels, Reporter: current.Item.Reporter,
		Title: title, Description: description, Status: current.Item.Status,
		Priority: current.Item.Priority, Position: current.Item.Position, Owner: current.Item.Owner, DueAt: current.Item.DueAt,
	})
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE work_items SET goal_id = $2, updated_at = now() WHERE id = $1`, componentID, goalID); err != nil {
		return nil, fmt.Errorf("update work plan component goal: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE work_plan_items SET technical_details = $2, owned_paths = $3, updated_at = now() WHERE work_item_id = $1`,
		componentID, technicalDetails, paths,
	); err != nil {
		return nil, fmt.Errorf("update work plan component metadata: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update work plan component: %w", err)
	}
	return b.getWorkPlanComponent(ctx, componentID)
}

// CreateWorkPlanTask adds an executable task beneath a plan component.
func (b *Brain) CreateWorkPlanTask(ctx context.Context, namespaceID int64, input WorkPlanTaskInput) (*models.WorkPlanTask, error) {
	if input.ComponentID <= 0 {
		return nil, ErrWorkPlanComponentNotFound
	}
	technicalDetails, err := normalizeWorkPlanText(input.TechnicalDetails)
	if err != nil {
		return nil, fmt.Errorf("brain: task technical details: %w", err)
	}
	provenance, err := normalizeWorkPlanProvenance(input.Provenance)
	if err != nil {
		return nil, err
	}

	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create work plan task: %w", err)
	}
	defer tx.Rollback(ctx)

	component, err := b.getWorkPlanItem(ctx, tx, input.ComponentID, true)
	if err != nil {
		return nil, err
	}
	if component.Metadata.Kind != workPlanComponentKind || component.Item.NamespaceID != namespaceID {
		return nil, ErrWorkPlanComponentNotFound
	}
	goalID, err := b.resolveProjectGoalForWork(ctx, namespaceID, input.GoalID, component.Item.GoalID)
	if err != nil {
		return nil, err
	}
	item, err := b.insertWorkItem(ctx, tx, namespaceID, WorkItemInput{
		GoalID: goalID, ParentID: &input.ComponentID, IssueType: "task", Labels: input.Labels, Reporter: input.Reporter,
		Title: input.Title, Description: input.Description, Status: "ready",
		Priority: input.Priority, Position: input.Position,
	})
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO work_plan_items (work_item_id, kind, technical_details, provenance)
		 VALUES ($1, $2, $3, $4)`,
		item.ID, workPlanTaskKind, technicalDetails, provenance,
	)
	if err != nil {
		return nil, fmt.Errorf("create work plan task metadata: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create work plan task: %w", err)
	}
	return b.getWorkPlanTask(ctx, item.ID)
}

// UpdateWorkPlanTask edits task wording and provenance while preserving its
// parent component, issue key, status, agents, and lifecycle timestamps.
func (b *Brain) UpdateWorkPlanTask(ctx context.Context, taskID int64, input WorkPlanTaskUpdate) (*models.WorkPlanTask, error) {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update work plan task: %w", err)
	}
	defer tx.Rollback(ctx)

	current, err := b.getWorkPlanItem(ctx, tx, taskID, true)
	if err != nil {
		return nil, err
	}
	if current.Metadata.Kind != workPlanTaskKind {
		return nil, ErrWorkPlanTaskNotFound
	}
	goalID := current.Item.GoalID
	if input.GoalID != nil {
		goalID, err = b.resolveProjectGoalForWork(ctx, current.Item.NamespaceID, input.GoalID, nil)
		if err != nil {
			return nil, err
		}
	}
	title := current.Item.Title
	if input.Title != nil {
		title = *input.Title
	}
	description := current.Item.Description
	if input.Description != nil {
		description = *input.Description
	}
	technicalDetails := current.Metadata.TechnicalDetails
	if input.TechnicalDetails != nil {
		technicalDetails, err = normalizeWorkPlanText(*input.TechnicalDetails)
		if err != nil {
			return nil, fmt.Errorf("brain: task technical details: %w", err)
		}
	}
	provenance := current.Metadata.Provenance
	if input.Provenance != nil {
		provenance, err = normalizeWorkPlanProvenance(*input.Provenance)
		if err != nil {
			return nil, err
		}
	}
	_, err = b.updateWorkItem(ctx, tx, taskID, WorkItemInput{
		ParentID: current.Item.ParentID, IssueType: current.Item.IssueType, Labels: current.Item.Labels, Reporter: current.Item.Reporter,
		Title: title, Description: description, Status: current.Item.Status,
		Priority: current.Item.Priority, Position: current.Item.Position, Owner: current.Item.Owner, DueAt: current.Item.DueAt,
	})
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE work_items SET goal_id = $2, updated_at = now() WHERE id = $1`, taskID, goalID); err != nil {
		return nil, fmt.Errorf("update work plan task goal: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE work_plan_items SET technical_details = $2, provenance = $3, updated_at = now() WHERE work_item_id = $1`,
		taskID, technicalDetails, provenance,
	); err != nil {
		return nil, fmt.Errorf("update work plan task metadata: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update work plan task: %w", err)
	}
	return b.getWorkPlanTask(ctx, taskID)
}

// SetWorkPlanComponentPaths changes the file patterns owned by a component.
func (b *Brain) SetWorkPlanComponentPaths(ctx context.Context, componentID int64, paths []string) (*models.WorkPlanComponent, error) {
	paths, err := normalizeWorkPlanPaths(paths)
	if err != nil {
		return nil, err
	}
	component, err := b.getWorkPlanItem(ctx, b.pool, componentID, false)
	if err != nil {
		return nil, err
	}
	if component.Metadata.Kind != workPlanComponentKind {
		return nil, ErrWorkPlanComponentNotFound
	}
	_, err = b.pool.Exec(ctx,
		`UPDATE work_plan_items SET owned_paths = $2, updated_at = now() WHERE work_item_id = $1`,
		componentID, paths,
	)
	if err != nil {
		return nil, fmt.Errorf("update work plan component paths: %w", err)
	}
	return b.getWorkPlanComponent(ctx, componentID)
}

// LinkWorkPlanComponents records either a directed prerequisite (needs) or a
// symmetric conceptual link. Existing work graph edges remain the source of
// dependency truth.
func (b *Brain) LinkWorkPlanComponents(ctx context.Context, namespaceID, componentID, relatedComponentID int64, relationship string) (*models.WorkItemEdge, error) {
	if relationship != "needs" && relationship != "links" {
		return nil, ErrWorkPlanInvalidRelationship
	}
	component, err := b.getWorkPlanItem(ctx, b.pool, componentID, false)
	if err != nil {
		return nil, err
	}
	related, err := b.getWorkPlanItem(ctx, b.pool, relatedComponentID, false)
	if err != nil {
		return nil, err
	}
	if component.Metadata.Kind != workPlanComponentKind || related.Metadata.Kind != workPlanComponentKind ||
		component.Item.NamespaceID != namespaceID || related.Item.NamespaceID != namespaceID {
		return nil, ErrWorkPlanComponentNotFound
	}
	if relationship == "needs" {
		return b.AddWorkItemEdge(ctx, namespaceID, relatedComponentID, componentID, "blocks")
	}
	return b.AddWorkItemEdge(ctx, namespaceID, componentID, relatedComponentID, "relates_to")
}

func workItemInputFromExisting(item models.WorkItem) WorkItemInput {
	return WorkItemInput{
		IssueType: item.IssueType, Labels: item.Labels, Reporter: item.Reporter,
		Title: item.Title, Description: item.Description, Status: item.Status,
		Priority: item.Priority, Position: item.Position, Owner: item.Owner, DueAt: item.DueAt,
	}
}

func (b *Brain) transitionWorkPlanTask(ctx context.Context, taskID int64, status, agent, reason string) (*models.WorkPlanTask, error) {
	agent = strings.TrimSpace(agent)
	reason, err := normalizeWorkPlanText(reason)
	if err != nil {
		return nil, fmt.Errorf("brain: task blocker reason: %w", err)
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition work plan task: %w", err)
	}
	defer tx.Rollback(ctx)

	record, err := b.getWorkPlanItem(ctx, tx, taskID, true)
	if err != nil {
		return nil, err
	}
	if record.Metadata.Kind != workPlanTaskKind {
		return nil, ErrWorkPlanTaskNotFound
	}
	input := workItemInputFromExisting(record.Item)
	switch status {
	case "doing":
		if agent == "" {
			return nil, fmt.Errorf("brain: agent is required to start a work plan task")
		}
		if record.Item.Status == "done" || record.Item.Status == "canceled" {
			return nil, ErrWorkPlanTaskAlreadyDone
		}
		if record.Item.Status == "doing" && record.Item.Owner != "" && record.Item.Owner != agent {
			return nil, ErrWorkPlanTaskAlreadyActive
		}
		input.Status = "doing"
		input.Owner = agent
	case "done":
		if agent == "" {
			return nil, fmt.Errorf("brain: agent is required to complete a work plan task")
		}
		if record.Metadata.StartedBy == "" {
			return nil, ErrWorkPlanTaskNotStarted
		}
		input.Status = "done"
		if input.Owner == "" {
			input.Owner = agent
		}
	case "blocked":
		if record.Item.Status == "done" || record.Item.Status == "canceled" {
			return nil, ErrWorkPlanTaskAlreadyDone
		}
		input.Status = "blocked"
		if input.Owner == "" && agent != "" {
			input.Owner = agent
		}
	case "ready":
		if record.Item.Status != "blocked" {
			return nil, ErrWorkPlanTaskNotBlocked
		}
		input.Status = "ready"
	default:
		return nil, fmt.Errorf("brain: unsupported work plan task status %q", status)
	}

	if _, err := b.updateWorkItem(ctx, tx, taskID, input); err != nil {
		return nil, err
	}
	startedBy, completedBy := "", ""
	if status == "doing" {
		startedBy = agent
	}
	if status == "done" {
		completedBy = agent
	}
	_, err = tx.Exec(ctx,
		`UPDATE work_plan_items
		 SET started_by = CASE WHEN $2 <> '' AND started_by = '' THEN $2 ELSE started_by END,
		     completed_by = CASE WHEN $3 <> '' THEN $3 ELSE completed_by END,
		     updated_at = now()
		 WHERE work_item_id = $1`,
		taskID, startedBy, completedBy,
	)
	if err != nil {
		return nil, fmt.Errorf("update work plan task metadata: %w", err)
	}
	if status == "blocked" && reason != "" {
		_, err = tx.Exec(ctx,
			`INSERT INTO work_item_comments (work_item_id, author, body) VALUES ($1, $2, $3)`,
			taskID, agent, "Blocked: "+reason,
		)
		if err != nil {
			return nil, fmt.Errorf("record work plan task blocker: %w", err)
		}
	}
	if status == "done" && record.Item.GoalID != nil {
		if err := autoCompleteGoalChain(ctx, tx, *record.Item.GoalID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition work plan task: %w", err)
	}
	return b.getWorkPlanTask(ctx, taskID)
}

// StartWorkPlanTask marks a task active and persists who started it before work begins.
func (b *Brain) StartWorkPlanTask(ctx context.Context, taskID int64, agent string) (*models.WorkPlanTask, error) {
	return b.transitionWorkPlanTask(ctx, taskID, "doing", agent, "")
}

// CompleteWorkPlanTask marks a task done and retains the starter and finisher.
func (b *Brain) CompleteWorkPlanTask(ctx context.Context, taskID int64, agent string) (*models.WorkPlanTask, error) {
	return b.transitionWorkPlanTask(ctx, taskID, "done", agent, "")
}

// BlockWorkPlanTask marks a task blocked and optionally records why.
func (b *Brain) BlockWorkPlanTask(ctx context.Context, taskID int64, agent, reason string) (*models.WorkPlanTask, error) {
	return b.transitionWorkPlanTask(ctx, taskID, "blocked", agent, reason)
}

// UnblockWorkPlanTask clears a blocked marker by returning the task to ready.
func (b *Brain) UnblockWorkPlanTask(ctx context.Context, taskID int64) (*models.WorkPlanTask, error) {
	return b.transitionWorkPlanTask(ctx, taskID, "ready", "", "")
}

// DeleteWorkPlanComponent removes a component and its child tasks. Its issue
// key is retired with the records; callers must create a new component rather
// than reusing an old key for a different purpose.
func (b *Brain) DeleteWorkPlanComponent(ctx context.Context, componentID int64) error {
	record, err := b.getWorkPlanItem(ctx, b.pool, componentID, false)
	if err != nil {
		return err
	}
	if record.Metadata.Kind != workPlanComponentKind {
		return ErrWorkPlanComponentNotFound
	}
	return b.deleteWorkItem(ctx, componentID)
}

// DeleteWorkPlanTask removes one executable child task from a component.
func (b *Brain) DeleteWorkPlanTask(ctx context.Context, taskID int64) error {
	record, err := b.getWorkPlanItem(ctx, b.pool, taskID, false)
	if err != nil {
		return err
	}
	if record.Metadata.Kind != workPlanTaskKind {
		return ErrWorkPlanTaskNotFound
	}
	return b.deleteWorkItem(ctx, taskID)
}

func scanWorkPlanDecision(row pgx.Row) (*models.WorkPlanDecision, error) {
	var decision models.WorkPlanDecision
	err := row.Scan(
		&decision.ID, &decision.NamespaceID, &decision.ComponentID, &decision.WorkItemID,
		&decision.Title, &decision.Rationale, &decision.Author, &decision.CreatedAt,
		&decision.UpdatedAt, &decision.DeletedAt,
	)
	return &decision, err
}

// RecordWorkPlanDecision saves a decision before implementation changes the plan.
func (b *Brain) RecordWorkPlanDecision(ctx context.Context, namespaceID int64, input WorkPlanDecisionInput) (*models.WorkPlanDecision, error) {
	if err := validateContent(input.Title); err != nil {
		return nil, fmt.Errorf("brain: work plan decision title: %w", err)
	}
	rationale, err := normalizeWorkPlanText(input.Rationale)
	if err != nil {
		return nil, fmt.Errorf("brain: work plan decision rationale: %w", err)
	}
	if input.ComponentID != nil {
		record, err := b.getWorkPlanItem(ctx, b.pool, *input.ComponentID, false)
		if err != nil {
			return nil, err
		}
		if record.Item.NamespaceID != namespaceID || record.Metadata.Kind != workPlanComponentKind {
			return nil, ErrWorkPlanItemNotFound
		}
	}
	if input.WorkItemID != nil {
		record, err := b.getWorkPlanItem(ctx, b.pool, *input.WorkItemID, false)
		if err != nil {
			return nil, err
		}
		if record.Item.NamespaceID != namespaceID {
			return nil, ErrWorkPlanItemNotFound
		}
	}
	decision, err := scanWorkPlanDecision(b.pool.QueryRow(ctx,
		`INSERT INTO work_plan_decisions (namespace_id, component_id, work_item_id, title, rationale, author)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, namespace_id, component_id, work_item_id, title, rationale, author, created_at, updated_at, deleted_at`,
		namespaceID, input.ComponentID, input.WorkItemID, input.Title, rationale, strings.TrimSpace(input.Author),
	))
	if err != nil {
		return nil, fmt.Errorf("record work plan decision: %w", err)
	}
	return decision, nil
}

// ListWorkPlanDecisions returns owner-facing decisions in newest-first order.
func (b *Brain) ListWorkPlanDecisions(ctx context.Context, namespaceID int64, page Pagination) ([]models.WorkPlanDecision, error) {
	page = b.sanitizePage(page)
	rows, err := b.pool.Query(ctx,
		`SELECT id, namespace_id, component_id, work_item_id, title, rationale, author, created_at, updated_at, deleted_at
		 FROM work_plan_decisions
		 WHERE namespace_id = $1 AND deleted_at IS NULL
		 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`,
		namespaceID, page.Limit, page.Offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list work plan decisions: %w", err)
	}
	defer rows.Close()
	decisions := make([]models.WorkPlanDecision, 0)
	for rows.Next() {
		decision, err := scanWorkPlanDecision(rows)
		if err != nil {
			return nil, fmt.Errorf("scan work plan decision: %w", err)
		}
		decisions = append(decisions, *decision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read work plan decisions: %w", err)
	}
	return decisions, nil
}

func attachWorktreesToWorkPlanRecords(ctx context.Context, b *Brain, records []workPlanItemRecord) ([]workPlanItemRecord, error) {
	if len(records) == 0 {
		return records, nil
	}
	items := make([]models.WorkItem, len(records))
	for i := range records {
		items[i] = records[i].Item
	}
	items, err := b.attachWorktreeIDs(ctx, items)
	if err != nil {
		return nil, err
	}
	for i := range records {
		records[i].Item = items[i]
	}
	return records, nil
}

func appendWorkPlanReference(references []models.WorkPlanReference, reference models.WorkPlanReference) []models.WorkPlanReference {
	for _, existing := range references {
		if existing.ID == reference.ID {
			return references
		}
	}
	return append(references, reference)
}

func workPlanDigest(plan *models.WorkPlan) (string, error) {
	input := workPlanDigestInput{
		Components: make([]workPlanDigestComponent, 0, len(plan.Components)),
		Decisions:  make([]workPlanDigestDecision, 0, len(plan.Decisions)),
	}
	if plan.GoalTree.RootGoalID != nil {
		input.RootGoalID = *plan.GoalTree.RootGoalID
	}
	for _, component := range plan.Components {
		item := workPlanDigestComponent{
			ID:               component.ID,
			IssueKey:         component.IssueKey,
			Title:            component.Title,
			Description:      component.Description,
			TechnicalDetails: component.TechnicalDetails,
			OwnedPaths:       append([]string(nil), component.OwnedPaths...),
			Needs:            make([]int64, 0, len(component.Needs)),
			Links:            make([]int64, 0, len(component.Links)),
			Tasks:            make([]workPlanDigestTask, 0, len(component.Tasks)),
		}
		if component.GoalID != nil {
			item.GoalID = *component.GoalID
		}
		for _, needed := range component.Needs {
			item.Needs = append(item.Needs, needed.ID)
		}
		for _, linked := range component.Links {
			item.Links = append(item.Links, linked.ID)
		}
		for _, task := range component.Tasks {
			digestTask := workPlanDigestTask{
				ID:               task.ID,
				Title:            task.Title,
				Description:      task.Description,
				TechnicalDetails: task.TechnicalDetails,
			}
			if task.GoalID != nil {
				digestTask.GoalID = *task.GoalID
			}
			item.Tasks = append(item.Tasks, digestTask)
		}
		input.Components = append(input.Components, item)
	}
	for _, decision := range plan.Decisions {
		item := workPlanDigestDecision{ID: decision.ID, Title: decision.Title, Rationale: decision.Rationale}
		if decision.ComponentID != nil {
			item.ComponentID = *decision.ComponentID
		}
		if decision.WorkItemID != nil {
			item.WorkItemID = *decision.WorkItemID
		}
		input.Decisions = append(input.Decisions, item)
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("digest work plan: %w", err)
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("%x", digest), nil
}

func (b *Brain) latestWorkPlanValidation(ctx context.Context, namespaceID int64) (*models.WorkPlanValidation, error) {
	var validation models.WorkPlanValidation
	var findingsJSON []byte
	err := b.pool.QueryRow(ctx,
		`SELECT namespace_id, model, plan_digest, passed, summary, findings, checked_at
		 FROM work_plan_validations WHERE namespace_id = $1`, namespaceID,
	).Scan(
		&validation.NamespaceID, &validation.Model, &validation.PlanDigest, &validation.Passed,
		&validation.Summary, &findingsJSON, &validation.CheckedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get latest work plan validation: %w", err)
	}
	if err := json.Unmarshal(findingsJSON, &validation.Findings); err != nil {
		return nil, fmt.Errorf("decode work plan validation findings: %w", err)
	}
	if validation.Findings == nil {
		validation.Findings = []models.WorkPlanValidationFinding{}
	}
	return &validation, nil
}

// ValidateWorkPlan runs an explicit semantic review with the configured
// reasoning model and stores the latest result. Mechanical convention warnings
// remain computed by GetWorkPlan and do not consume a model call.
func (b *Brain) ValidateWorkPlan(ctx context.Context, namespaceID int64) (*models.WorkPlanValidation, error) {
	validator, ok := b.reasoner.(reasoner.WorkPlanValidator)
	if !ok {
		return nil, ErrWorkPlanValidationUnavailable
	}
	plan, err := b.GetWorkPlan(ctx, namespaceID)
	if err != nil {
		return nil, err
	}
	if len(plan.Components) == 0 {
		return nil, ErrWorkPlanValidationEmpty
	}
	digest, err := workPlanDigest(plan)
	if err != nil {
		return nil, err
	}
	result, err := validator.ValidateWorkPlan(ctx, *plan)
	if err != nil {
		return nil, fmt.Errorf("validate work plan with reasoner: %w", err)
	}
	if result == nil {
		return nil, errors.New("brain: reasoner returned an empty work plan validation")
	}

	findings := make([]models.WorkPlanValidationFinding, 0, len(result.Findings))
	for _, finding := range result.Findings {
		findings = append(findings, models.WorkPlanValidationFinding{
			Code:               finding.Code,
			Severity:           finding.Severity,
			ComponentID:        finding.ComponentID,
			RelatedComponentID: finding.RelatedComponentID,
			TaskID:             finding.TaskID,
			Message:            finding.Message,
			Suggestion:         finding.Suggestion,
			Confidence:         finding.Confidence,
		})
	}
	findingsJSON, err := json.Marshal(findings)
	if err != nil {
		return nil, fmt.Errorf("encode work plan validation findings: %w", err)
	}
	modelName := strings.TrimSpace(validator.ModelName())
	if modelName == "" {
		return nil, errors.New("brain: work plan validator model name is empty")
	}
	validation := &models.WorkPlanValidation{
		NamespaceID: namespaceID,
		Model:       modelName,
		PlanDigest:  digest,
		Passed:      len(findings) == 0,
		Stale:       false,
		Summary:     strings.TrimSpace(result.Summary),
		Findings:    findings,
	}
	err = b.pool.QueryRow(ctx,
		`INSERT INTO work_plan_validations (namespace_id, model, plan_digest, passed, summary, findings, checked_at)
		 VALUES ($1, $2, $3, $4, $5, $6::jsonb, now())
		 ON CONFLICT (namespace_id) DO UPDATE SET
		   model = EXCLUDED.model,
		   plan_digest = EXCLUDED.plan_digest,
		   passed = EXCLUDED.passed,
		   summary = EXCLUDED.summary,
		   findings = EXCLUDED.findings,
		   checked_at = EXCLUDED.checked_at
		 RETURNING checked_at`,
		namespaceID, validation.Model, validation.PlanDigest, validation.Passed,
		validation.Summary, string(findingsJSON),
	).Scan(&validation.CheckedAt)
	if err != nil {
		return nil, fmt.Errorf("save work plan validation: %w", err)
	}
	return validation, nil
}

// GetWorkPlan builds the stable component map, nested tasks, component edges,
// and recent decisions for one namespace. Completed nodes stay in this view so
// an owner can read the plan as a living record.
func (b *Brain) GetWorkPlan(ctx context.Context, namespaceID int64) (*models.WorkPlan, error) {
	goalTree, err := b.GetProjectGoalTree(ctx, namespaceID)
	if err != nil {
		return nil, err
	}
	componentRows, err := b.pool.Query(ctx,
		`SELECT `+workPlanItemColumns+`
		 FROM work_items wi JOIN work_plan_items pi ON pi.work_item_id = wi.id
		 WHERE wi.namespace_id = $1 AND wi.deleted_at IS NULL AND pi.kind = $2
		 ORDER BY wi.position, wi.id`,
		namespaceID, workPlanComponentKind,
	)
	if err != nil {
		return nil, fmt.Errorf("list work plan components: %w", err)
	}
	components, err := scanWorkPlanItemRows(componentRows)
	if err != nil {
		return nil, fmt.Errorf("scan work plan components: %w", err)
	}
	components, err = attachWorktreesToWorkPlanRecords(ctx, b, components)
	if err != nil {
		return nil, err
	}

	plan := &models.WorkPlan{
		GoalTree:   *goalTree,
		Components: make([]models.WorkPlanComponent, len(components)),
		Warnings:   make([]models.WorkPlanWarning, 0),
	}
	componentIndex := make(map[int64]int, len(components))
	componentReferences := make(map[int64]models.WorkPlanReference, len(components))
	componentIDs := make([]int64, 0, len(components))
	for i, record := range components {
		plan.Components[i] = workPlanComponentFromRecord(record)
		componentIndex[record.Item.ID] = i
		componentReferences[record.Item.ID] = models.WorkPlanReference{ID: record.Item.ID, IssueKey: record.Item.IssueKey, Title: record.Item.Title}
		componentIDs = append(componentIDs, record.Item.ID)
	}

	if len(componentIDs) > 0 {
		taskRows, err := b.pool.Query(ctx,
			`SELECT `+workPlanItemColumns+`
			 FROM work_items wi JOIN work_plan_items pi ON pi.work_item_id = wi.id
			 WHERE wi.namespace_id = $1 AND wi.deleted_at IS NULL AND pi.kind = $2 AND wi.parent_id = ANY($3)
			 ORDER BY wi.position, wi.id`,
			namespaceID, workPlanTaskKind, componentIDs,
		)
		if err != nil {
			return nil, fmt.Errorf("list work plan tasks: %w", err)
		}
		tasks, err := scanWorkPlanItemRows(taskRows)
		if err != nil {
			return nil, fmt.Errorf("scan work plan tasks: %w", err)
		}
		tasks, err = attachWorktreesToWorkPlanRecords(ctx, b, tasks)
		if err != nil {
			return nil, err
		}
		for _, task := range tasks {
			if task.Item.ParentID == nil {
				continue
			}
			index, ok := componentIndex[*task.Item.ParentID]
			if !ok {
				continue
			}
			plan.Components[index].Tasks = append(plan.Components[index].Tasks, workPlanTaskFromRecord(task))
		}

		edgeRows, err := b.pool.Query(ctx,
			`SELECT edge.id, edge.namespace_id, edge.from_item_id, edge.to_item_id, edge.edge_type, edge.created_at, edge.deleted_at
			 FROM work_item_edges edge
			 JOIN work_plan_items source_plan ON source_plan.work_item_id = edge.from_item_id AND source_plan.kind = $2
			 JOIN work_plan_items target_plan ON target_plan.work_item_id = edge.to_item_id AND target_plan.kind = $2
			 WHERE edge.namespace_id = $1 AND edge.deleted_at IS NULL
			 ORDER BY edge.id`,
			namespaceID, workPlanComponentKind,
		)
		if err != nil {
			return nil, fmt.Errorf("list work plan component edges: %w", err)
		}
		edges, err := scanWorkItemEdges(edgeRows)
		if err != nil {
			return nil, fmt.Errorf("scan work plan component edges: %w", err)
		}
		for _, edge := range edges {
			fromReference, fromOK := componentReferences[edge.FromItemID]
			toReference, toOK := componentReferences[edge.ToItemID]
			fromIndex, fromIndexed := componentIndex[edge.FromItemID]
			toIndex, toIndexed := componentIndex[edge.ToItemID]
			if !fromOK || !toOK || !fromIndexed || !toIndexed {
				continue
			}
			switch edge.EdgeType {
			case "blocks":
				plan.Components[toIndex].Needs = appendWorkPlanReference(plan.Components[toIndex].Needs, fromReference)
			case "relates_to":
				plan.Components[fromIndex].Links = appendWorkPlanReference(plan.Components[fromIndex].Links, toReference)
				plan.Components[toIndex].Links = appendWorkPlanReference(plan.Components[toIndex].Links, fromReference)
			}
		}
	}

	decisions, err := b.ListWorkPlanDecisions(ctx, namespaceID, Pagination{Limit: 100})
	if err != nil {
		return nil, err
	}
	plan.Decisions = decisions
	if len(plan.Components) == 0 {
		plan.Warnings = append(plan.Warnings, models.WorkPlanWarning{Code: "no_components"})
	}
	goalSet := make(map[int64]struct{}, len(plan.GoalTree.Goals))
	for _, goal := range plan.GoalTree.Goals {
		goalSet[goal.ID] = struct{}{}
	}
	if plan.GoalTree.RootGoalID == nil {
		plan.Warnings = append(plan.Warnings, models.WorkPlanWarning{Code: "no_project_goal"})
	}
	for _, component := range plan.Components {
		if component.GoalID == nil {
			plan.Warnings = append(plan.Warnings, models.WorkPlanWarning{Code: "component_without_goal", ComponentID: component.ID})
		} else if _, ok := goalSet[*component.GoalID]; !ok {
			plan.Warnings = append(plan.Warnings, models.WorkPlanWarning{Code: "component_goal_outside_tree", ComponentID: component.ID})
		}
		if len(component.OwnedPaths) == 0 {
			plan.Warnings = append(plan.Warnings, models.WorkPlanWarning{Code: "component_without_paths", ComponentID: component.ID})
		}
		for _, task := range component.Tasks {
			if task.GoalID == nil {
				plan.Warnings = append(plan.Warnings, models.WorkPlanWarning{Code: "task_without_goal", TaskID: task.ID})
			} else if _, ok := goalSet[*task.GoalID]; !ok {
				plan.Warnings = append(plan.Warnings, models.WorkPlanWarning{Code: "task_goal_outside_tree", TaskID: task.ID})
			}
			if task.Status != "done" && task.Status != "canceled" && task.Provenance == "" {
				plan.Warnings = append(plan.Warnings, models.WorkPlanWarning{Code: "open_task_without_provenance", TaskID: task.ID})
			}
			if task.Status == "doing" && task.StartedBy == "" {
				plan.Warnings = append(plan.Warnings, models.WorkPlanWarning{Code: "active_task_without_starter", TaskID: task.ID})
			}
		}
	}
	validation, err := b.latestWorkPlanValidation(ctx, namespaceID)
	if err != nil {
		return nil, err
	}
	if validation != nil {
		digest, err := workPlanDigest(plan)
		if err != nil {
			return nil, err
		}
		validation.Stale = validation.PlanDigest != digest
		plan.Validation = validation
	}
	return plan, nil
}
