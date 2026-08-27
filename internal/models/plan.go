package models

import "time"

// WorkPlanTask is an executable child task in a component-oriented work plan.
// It embeds the regular work item so existing issue keys, states, and worktree
// links remain the canonical operational records.
type WorkPlanTask struct {
	WorkItem
	TechnicalDetails string `db:"technical_details" json:"technical_details"`
	Provenance       string `db:"provenance" json:"provenance,omitempty"`
	StartedBy        string `db:"started_by" json:"started_by,omitempty"`
	CompletedBy      string `db:"completed_by" json:"completed_by,omitempty"`
}

// WorkPlanReference is a stable component reference rendered by the plan view.
type WorkPlanReference struct {
	ID       int64  `json:"id"`
	IssueKey string `json:"issue_key"`
	Title    string `json:"title"`
}

// WorkPlanComponent groups executable tasks that one agent can own in a
// session. The WorkItem issue key is the stable component identifier.
type WorkPlanComponent struct {
	WorkItem
	TechnicalDetails string              `db:"technical_details" json:"technical_details"`
	OwnedPaths       []string            `db:"owned_paths" json:"owned_paths"`
	Tasks            []WorkPlanTask      `json:"tasks"`
	Needs            []WorkPlanReference `json:"needs"`
	Links            []WorkPlanReference `json:"links"`
}

// WorkPlanDecision records a decision that changes the plan before work starts.
type WorkPlanDecision struct {
	ID          int64      `db:"id" json:"id"`
	NamespaceID int64      `db:"namespace_id" json:"namespace_id"`
	ComponentID *int64     `db:"component_id" json:"component_id,omitempty"`
	WorkItemID  *int64     `db:"work_item_id" json:"work_item_id,omitempty"`
	Title       string     `db:"title" json:"title"`
	Rationale   string     `db:"rationale" json:"rationale"`
	Author      string     `db:"author" json:"author"`
	CreatedAt   time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time  `db:"updated_at" json:"updated_at"`
	DeletedAt   *time.Time `db:"deleted_at" json:"deleted_at,omitempty"`
}

// WorkPlanWarning is a machine-readable planning convention warning. Clients
// render it in their own language without parsing prose from the API.
type WorkPlanWarning struct {
	Code        string `json:"code"`
	Count       int    `json:"count,omitempty"`
	ComponentID int64  `json:"component_id,omitempty"`
	TaskID      int64  `json:"task_id,omitempty"`
}

// WorkPlanValidationFinding is one semantic issue found by the configured
// reasoning model. Codes and references stay machine-readable while the
// message and suggestion are suitable for the owner-facing plan view.
type WorkPlanValidationFinding struct {
	Code               string  `json:"code"`
	Severity           string  `json:"severity"`
	ComponentID        int64   `json:"component_id,omitempty"`
	RelatedComponentID int64   `json:"related_component_id,omitempty"`
	TaskID             int64   `json:"task_id,omitempty"`
	Message            string  `json:"message"`
	Suggestion         string  `json:"suggestion,omitempty"`
	Confidence         float32 `json:"confidence"`
}

// WorkPlanValidation is the latest persisted semantic review for a namespace.
// Stale is computed when the plan is read and becomes true as soon as any
// semantically relevant plan content changes.
type WorkPlanValidation struct {
	NamespaceID int64                       `json:"namespace_id"`
	Model       string                      `json:"model"`
	PlanDigest  string                      `json:"plan_digest"`
	Passed      bool                        `json:"passed"`
	Stale       bool                        `json:"stale"`
	Summary     string                      `json:"summary"`
	Findings    []WorkPlanValidationFinding `json:"findings"`
	CheckedAt   time.Time                   `json:"checked_at"`
}

// WorkPlan is the owner-facing, component-oriented projection of a namespace.
type WorkPlan struct {
	Components []WorkPlanComponent `json:"components"`
	Decisions  []WorkPlanDecision  `json:"decisions"`
	Warnings   []WorkPlanWarning   `json:"warnings"`
	Validation *WorkPlanValidation `json:"validation,omitempty"`
}
