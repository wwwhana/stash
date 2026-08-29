package models

import (
	"encoding/json"
	"time"
)

// WorkspaceRepository binds one movable local clone to a project namespace.
// RemoteURL is normalized and never contains user information or a query.
type WorkspaceRepository struct {
	ID                   int64           `db:"id" json:"id"`
	NamespaceID          int64           `db:"namespace_id" json:"namespace_id"`
	RepositoryInstanceID string          `db:"repository_instance_id" json:"repository_instance_id"`
	Provider             string          `db:"provider" json:"provider,omitempty"`
	ProviderRepositoryID string          `db:"provider_repository_id" json:"provider_repository_id,omitempty"`
	RemoteURL            string          `db:"remote_url" json:"remote_url,omitempty"`
	RemoteFingerprint    string          `db:"remote_fingerprint" json:"remote_fingerprint,omitempty"`
	GitCommonDir         string          `db:"git_common_dir" json:"git_common_dir"`
	LastSeenAt           time.Time       `db:"last_seen_at" json:"last_seen_at"`
	Metadata             json.RawMessage `db:"metadata" json:"metadata"`
	CreatedAt            time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt            time.Time       `db:"updated_at" json:"updated_at"`
	DeletedAt            *time.Time      `db:"deleted_at" json:"deleted_at,omitempty"`
}

// WorkspaceResolution is the small, deterministic session-start answer. It
// deliberately carries no private lease token.
type WorkspaceResolution struct {
	Namespace        Namespace           `json:"namespace"`
	Repository       WorkspaceRepository `json:"repository"`
	Worktree         Worktree            `json:"worktree"`
	ActiveWorkItem   *WorkItem           `json:"active_work_item,omitempty"`
	ActiveAttempt    *WorkAttempt        `json:"active_attempt,omitempty"`
	LatestCheckpoint *WorkCheckpoint     `json:"latest_checkpoint,omitempty"`
	NextAction       string              `json:"next_action,omitempty"`
}

type WorkspaceResumeTotals struct {
	Goals      int `json:"goals"`
	Doing      int `json:"doing"`
	Blocked    int `json:"blocked"`
	GraphNodes int `json:"graph_nodes"`
	GraphEdges int `json:"graph_edges"`
	Decisions  int `json:"decisions"`
	Failures   int `json:"failures"`
}

type WorkspaceResumeTruncated struct {
	Goals     bool `json:"goals"`
	Doing     bool `json:"doing"`
	Blocked   bool `json:"blocked"`
	Graph     bool `json:"graph"`
	Decisions bool `json:"decisions"`
	Failures  bool `json:"failures"`
	Core      bool `json:"core"`
}

// WorkspaceResumeBundle is a bounded project-wide continuation record. The
// active item keeps its existing item-level resume bundle.
type WorkspaceResumeBundle struct {
	Namespace        Namespace                `json:"namespace"`
	GoalTree         GoalTree                 `json:"goal_tree"`
	WorkPlan         *WorkPlan                `json:"work_plan"`
	Doing            []WorkItem               `json:"doing"`
	Blocked          []WorkItem               `json:"blocked"`
	Graph            WorkGraph                `json:"graph"`
	Worktree         *Worktree                `json:"worktree,omitempty"`
	CurrentWork      *WorkResumeBundle        `json:"current_work,omitempty"`
	LatestCheckpoint *WorkCheckpoint          `json:"latest_checkpoint,omitempty"`
	RecentDecisions  []WorkPlanDecision       `json:"recent_decisions"`
	RecentFailures   []Failure                `json:"recent_failures"`
	ProjectContext   *Context                 `json:"project_context,omitempty"`
	NextAction       string                   `json:"next_action,omitempty"`
	Totals           WorkspaceResumeTotals    `json:"totals"`
	Truncated        WorkspaceResumeTruncated `json:"truncated"`
}

// WorkspaceClaim combines workspace upsert and the existing exclusive work
// lease. LeaseToken is returned only from the claim response.
type WorkspaceClaim struct {
	Resolution WorkspaceResolution `json:"workspace"`
	Lease      WorkAttemptLease    `json:"lease"`
}

type WorkspaceMaintenanceResult struct {
	Stale   int64 `json:"stale"`
	Removed int64 `json:"removed"`
}
