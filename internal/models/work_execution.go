package models

import (
	"encoding/json"
	"time"
)

// WorkAttempt is one leased execution of a durable work item. The lease token
// is deliberately absent because the database stores only its hash.
type WorkAttempt struct {
	ID                 int64      `db:"id" json:"id"`
	WorkItemID         int64      `db:"work_item_id" json:"work_item_id"`
	WorktreeID         *int64     `db:"worktree_id" json:"worktree_id,omitempty"`
	AttemptNumber      int        `db:"attempt_number" json:"attempt_number"`
	AgentID            string     `db:"agent_id" json:"agent_id"`
	PrincipalID        string     `db:"principal_id" json:"principal_id"`
	Status             string     `db:"status" json:"status"`
	LeaseExpiresAt     time.Time  `db:"lease_expires_at" json:"lease_expires_at"`
	StartedAt          time.Time  `db:"started_at" json:"started_at"`
	EndedAt            *time.Time `db:"ended_at" json:"ended_at,omitempty"`
	CreatedAt          time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt          time.Time  `db:"updated_at" json:"updated_at"`
	ResultMemoryLinked bool       `db:"-" json:"result_memory_linked,omitempty"`
	ResultMemorySource string     `db:"-" json:"result_memory_source,omitempty"`
}

// WorkAttemptLease is returned only when an attempt starts. Callers must keep
// LeaseToken private and present it for every later mutation of that attempt.
type WorkAttemptLease struct {
	Attempt     WorkAttempt      `json:"attempt"`
	GoalContext *WorkGoalContext `json:"goal_context,omitempty"`
	LeaseToken  string           `json:"lease_token"`
}

// WorkCheckpoint is an append-only summary of an attempt's current state.
type WorkCheckpoint struct {
	ID         int64     `db:"id" json:"id"`
	AttemptID  int64     `db:"attempt_id" json:"attempt_id"`
	Summary    string    `db:"summary" json:"summary"`
	Result     string    `db:"result" json:"result"`
	NextAction string    `db:"next_action" json:"next_action"`
	CreatedAt  time.Time `db:"created_at" json:"created_at"`
}

// WorkCheckpointReceipt includes the server-side lease deadline established
// by the same transaction that stored the checkpoint.
type WorkCheckpointReceipt struct {
	Checkpoint     WorkCheckpoint `json:"checkpoint"`
	LeaseExpiresAt time.Time      `json:"lease_expires_at"`
}

// WorkCompletionCondition is one observable completion requirement. Evidence
// IDs are populated by resume reads and are not stored on the condition row.
type WorkCompletionCondition struct {
	ID                  int64           `db:"id" json:"id"`
	WorkItemID          int64           `db:"work_item_id" json:"work_item_id"`
	Kind                string          `db:"kind" json:"kind"`
	Description         string          `db:"description" json:"description"`
	Verification        json.RawMessage `db:"verification" json:"verification"`
	Required            bool            `db:"required" json:"required"`
	Position            int             `db:"position" json:"position"`
	Status              string          `db:"status" json:"status"`
	WaiverReason        string          `db:"waiver_reason" json:"waiver_reason,omitempty"`
	VerifiedByAttemptID *int64          `db:"verified_by_attempt_id" json:"verified_by_attempt_id,omitempty"`
	VerifiedAt          *time.Time      `db:"verified_at" json:"verified_at,omitempty"`
	SupersededAt        *time.Time      `db:"superseded_at" json:"superseded_at,omitempty"`
	CreatedAt           time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt           time.Time       `db:"updated_at" json:"updated_at"`
	EvidenceIDs         []int64         `db:"-" json:"evidence_ids"`
}

// WorkEvidence is immutable evidence submitted by a leased attempt. Evidence
// may be linked to more than one completion condition for the same work item.
type WorkEvidence struct {
	ID              int64           `db:"id" json:"id"`
	WorkItemID      int64           `db:"work_item_id" json:"work_item_id"`
	AttemptID       int64           `db:"attempt_id" json:"attempt_id"`
	EvidenceType    string          `db:"evidence_type" json:"evidence_type"`
	Summary         string          `db:"summary" json:"summary"`
	Reference       string          `db:"reference" json:"reference,omitempty"`
	Payload         json.RawMessage `db:"payload" json:"payload"`
	ContentDigest   string          `db:"content_digest" json:"content_digest"`
	PrincipalID     string          `db:"principal_id" json:"principal_id"`
	WorktreeHeadSHA string          `db:"worktree_head_sha" json:"worktree_head_sha,omitempty"`
	SubmittedAt     time.Time       `db:"submitted_at" json:"submitted_at"`
	CreatedAt       time.Time       `db:"created_at" json:"created_at"`
	ConditionIDs    []int64         `db:"-" json:"condition_ids"`
}

// WorkPreparation is the persisted starting action and replacement set of
// active completion conditions.
type WorkPreparation struct {
	WorkItemID           int64                     `json:"work_item_id"`
	NextAction           string                    `json:"next_action"`
	CompletionConditions []WorkCompletionCondition `json:"completion_conditions"`
}

// WorktreeLink retains the relationship metadata while returning the current
// registered worktree record in a resume bundle.
type WorktreeLink struct {
	WorkItemID int64     `json:"work_item_id"`
	Relation   string    `json:"relation"`
	LinkedAt   time.Time `json:"linked_at"`
	Worktree   Worktree  `json:"worktree"`
}

// WorkMemorySnapshot carries the bounded content needed to resume linked
// memory without requiring a second, potentially unauthorized ID lookup.
type WorkMemorySnapshot struct {
	Derived          bool      `json:"derived,omitempty"`
	WorkItemID       int64     `json:"work_item_id"`
	MemoryType       string    `json:"memory_type"`
	MemoryID         int64     `json:"memory_id"`
	Relation         string    `json:"relation"`
	Content          string    `json:"content"`
	Status           string    `json:"status"`
	ContentTruncated bool      `json:"content_truncated,omitempty"`
	LinkedAt         time.Time `json:"linked_at"`
}

// WorkResumeTotals reports how many authorized rows exist for each bounded
// collection, including rows omitted from the compact response.
type WorkResumeTotals struct {
	CompletionConditions int `json:"completion_conditions"`
	Evidence             int `json:"evidence"`
	WorktreeLinks        int `json:"worktree_links"`
	MemoryLinks          int `json:"memory_links"`
	Resources            int `json:"resources"`
	DependencyResults    int `json:"dependency_results"`
	Blockers             int `json:"blockers"`
	RecentEvents         int `json:"recent_events"`
}

// WorkResumeTruncated identifies collections or core text shortened to keep a
// resume response finite. Totals remain the authoritative collection counts.
type WorkResumeTruncated struct {
	CompletionConditions bool `json:"completion_conditions"`
	Evidence             bool `json:"evidence"`
	WorktreeLinks        bool `json:"worktree_links"`
	MemoryLinks          bool `json:"memory_links"`
	Resources            bool `json:"resources"`
	DependencyResults    bool `json:"dependency_results"`
	Blockers             bool `json:"blockers"`
	RecentEvents         bool `json:"recent_events"`
	Core                 bool `json:"core"`
}

// WorkResumeBundle contains everything a new agent needs to continue a work
// item without reconstructing state from a previous chat session.
type WorkResumeBundle struct {
	WorkItem             WorkItem                  `json:"work_item"`
	GoalContext          *WorkGoalContext          `json:"goal_context,omitempty"`
	PlanContext          *WorkPlanExecutionContext `json:"plan_context,omitempty"`
	NextAction           string                    `json:"next_action"`
	LatestAttempt        *WorkAttempt              `json:"latest_attempt,omitempty"`
	LatestCheckpoint     *WorkCheckpoint           `json:"latest_checkpoint,omitempty"`
	CompletionConditions []WorkCompletionCondition `json:"completion_conditions"`
	Evidence             []WorkEvidence            `json:"evidence"`
	WorktreeLinks        []WorktreeLink            `json:"worktree_links"`
	MemoryLinks          []WorkMemorySnapshot      `json:"memory_links"`
	Resources            []WorkResourceRef         `json:"resources"`
	DependencyResults    []WorkDependencyResult    `json:"dependency_results"`
	Blockers             []WorkItem                `json:"blockers"`
	RecentEvents         []WorkEvent               `json:"recent_events"`
	Totals               WorkResumeTotals          `json:"totals"`
	Truncated            WorkResumeTruncated       `json:"truncated"`
}
