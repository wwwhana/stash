// Package models defines domain structs for pgx scanning.
// Every field tag matches the PostgreSQL column name exactly.
package models

import (
	"encoding/json"
	"time"

	"github.com/pgvector/pgvector-go"
)

// Namespace owns memory.
type Namespace struct {
	ID          int64     `db:"id" json:"id"`
	Slug        string    `db:"slug" json:"slug"`
	Name        string    `db:"name" json:"name"`
	Description string    `db:"description" json:"description"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time `db:"updated_at" json:"updated_at"`
}

// Episode is an immutable, append-only raw event.
type Episode struct {
	ID             int64           `db:"id" json:"id"`
	NamespaceID    int64           `db:"namespace_id" json:"namespace_id"`
	Content        string          `db:"content" json:"content"`
	Embedding      pgvector.Vector `db:"embedding" json:"-"`
	EmbeddingModel string          `db:"embedding_model" json:"embedding_model"`
	OccurredAt     time.Time       `db:"occurred_at" json:"occurred_at"`
	CreatedAt      time.Time       `db:"created_at" json:"created_at"`
	DeletedAt      *time.Time      `db:"deleted_at" json:"deleted_at,omitempty"`
}

// Fact is a belief derived from episodes.
type Fact struct {
	ID             int64           `db:"id" json:"id"`
	NamespaceID    int64           `db:"namespace_id" json:"namespace_id"`
	Content        string          `db:"content" json:"content"`
	Embedding      pgvector.Vector `db:"embedding" json:"-"`
	EmbeddingModel string          `db:"embedding_model" json:"embedding_model"`
	Confidence     float32         `db:"confidence" json:"confidence"`
	Entity         *string         `db:"entity" json:"entity,omitempty"`
	Property       *string         `db:"property" json:"property,omitempty"`
	Value          *string         `db:"value" json:"value,omitempty"`
	ValidFrom      *time.Time      `db:"valid_from" json:"valid_from,omitempty"`
	ValidUntil     *time.Time      `db:"valid_until" json:"valid_until,omitempty"`
	CreatedAt      time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time       `db:"updated_at" json:"updated_at"`
	DeletedAt      *time.Time      `db:"deleted_at" json:"deleted_at,omitempty"`
}

// Contradiction records a conflict between two facts about the same entity and property.
type Contradiction struct {
	ID          int64      `db:"id" json:"id"`
	NamespaceID int64      `db:"namespace_id" json:"namespace_id"`
	OldFactID   int64      `db:"old_fact_id" json:"old_fact_id"`
	NewFactID   int64      `db:"new_fact_id" json:"new_fact_id"`
	Entity      string     `db:"entity" json:"entity"`
	Property    string     `db:"property" json:"property"`
	OldValue    string     `db:"old_value" json:"old_value"`
	NewValue    string     `db:"new_value" json:"new_value"`
	Confidence  float32    `db:"confidence" json:"confidence"`
	Method      string     `db:"method" json:"method"`
	Resolved    bool       `db:"resolved" json:"resolved"`
	Resolution  *string    `db:"resolution" json:"resolution,omitempty"`
	ResolvedAt  *time.Time `db:"resolved_at" json:"resolved_at,omitempty"`
	CreatedAt   time.Time  `db:"created_at" json:"created_at"`
}

// FactSource links a fact to the episodes that support it.
type FactSource struct {
	FactID    int64 `db:"fact_id" json:"fact_id"`
	EpisodeID int64 `db:"episode_id" json:"episode_id"`
}

// Relationship is an extracted entity edge.
type Relationship struct {
	ID           int64      `db:"id" json:"id"`
	NamespaceID  int64      `db:"namespace_id" json:"namespace_id"`
	FromEntity   string     `db:"from_entity" json:"from_entity"`
	RelationType string     `db:"relation_type" json:"relation_type"`
	ToEntity     string     `db:"to_entity" json:"to_entity"`
	Confidence   float32    `db:"confidence" json:"confidence"`
	SourceFactID *int64     `db:"source_fact_id" json:"source_fact_id,omitempty"`
	CreatedAt    time.Time  `db:"created_at" json:"created_at"`
	DeletedAt    *time.Time `db:"deleted_at" json:"deleted_at,omitempty"`
}

// Pattern is an abstraction over facts and relationships.
type Pattern struct {
	ID             int64      `db:"id" json:"id"`
	NamespaceID    int64      `db:"namespace_id" json:"namespace_id"`
	Content        string     `db:"content" json:"content"`
	Confidence     float32    `db:"confidence" json:"confidence"`
	SourceFactIDs  []int64    `db:"source_fact_ids" json:"source_fact_ids"`
	SourceRelIDs   []int64    `db:"source_rel_ids" json:"source_rel_ids"`
	CoherenceScore float32    `db:"coherence_score" json:"coherence_score"`
	CreatedAt      time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time  `db:"updated_at" json:"updated_at"`
	DeletedAt      *time.Time `db:"deleted_at" json:"deleted_at,omitempty"`
}

// Context is the active working state for a namespace.
type Context struct {
	NamespaceID int64     `db:"namespace_id" json:"namespace_id"`
	Focus       string    `db:"focus" json:"focus"`
	ExpiresAt   time.Time `db:"expires_at" json:"expires_at"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time `db:"updated_at" json:"updated_at"`
}

// CausalLink records a cause-effect relationship between two facts.
type CausalLink struct {
	ID           int64      `db:"id" json:"id"`
	NamespaceID  int64      `db:"namespace_id" json:"namespace_id"`
	CauseFactID  int64      `db:"cause_fact_id" json:"cause_fact_id"`
	EffectFactID int64      `db:"effect_fact_id" json:"effect_fact_id"`
	Confidence   float32    `db:"confidence" json:"confidence"`
	Method       string     `db:"method" json:"method"`
	CreatedAt    time.Time  `db:"created_at" json:"created_at"`
	DeletedAt    *time.Time `db:"deleted_at" json:"deleted_at,omitempty"`
}

// Hypothesis is a belief held with uncertainty plus a plan to verify it.
type Hypothesis struct {
	ID               int64      `db:"id" json:"id"`
	NamespaceID      int64      `db:"namespace_id" json:"namespace_id"`
	Content          string     `db:"content" json:"content"`
	Confidence       float32    `db:"confidence" json:"confidence"`
	Status           string     `db:"status" json:"status"`
	VerificationPlan string     `db:"verification_plan" json:"verification_plan"`
	Method           string     `db:"method" json:"method"`
	ConfirmedFactID  *int64     `db:"confirmed_fact_id" json:"confirmed_fact_id,omitempty"`
	RejectionReason  *string    `db:"rejection_reason" json:"rejection_reason,omitempty"`
	SourceFactIDs    []int64    `db:"source_fact_ids" json:"source_fact_ids"`
	TestedAt         *time.Time `db:"tested_at" json:"tested_at,omitempty"`
	ConfirmedAt      *time.Time `db:"confirmed_at" json:"confirmed_at,omitempty"`
	RejectedAt       *time.Time `db:"rejected_at" json:"rejected_at,omitempty"`
	CreatedAt        time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt        time.Time  `db:"updated_at" json:"updated_at"`
	DeletedAt        *time.Time `db:"deleted_at" json:"deleted_at,omitempty"`
}

// Goal is an intended outcome that persists across sessions.
type Goal struct {
	ID          int64      `db:"id" json:"id"`
	NamespaceID int64      `db:"namespace_id" json:"namespace_id"`
	ParentID    *int64     `db:"parent_id" json:"parent_id,omitempty"`
	Content     string     `db:"content" json:"content"`
	Status      string     `db:"status" json:"status"`
	Priority    int        `db:"priority" json:"priority"`
	Notes       string     `db:"notes" json:"notes"`
	CompletedAt *time.Time `db:"completed_at" json:"completed_at,omitempty"`
	AbandonedAt *time.Time `db:"abandoned_at" json:"abandoned_at,omitempty"`
	CreatedAt   time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time  `db:"updated_at" json:"updated_at"`
	DeletedAt   *time.Time `db:"deleted_at" json:"deleted_at,omitempty"`
}

// WorkItem is a mutable piece of work. It is separate from Goal so operational
// status changes do not overwrite the durable intent recorded in memory.
type WorkItem struct {
	ID                   int64      `db:"id" json:"id"`
	NamespaceID          int64      `db:"namespace_id" json:"namespace_id"`
	GoalID               *int64     `db:"goal_id" json:"goal_id,omitempty"`
	ParentID             *int64     `db:"parent_id" json:"parent_id,omitempty"`
	IssueKey             string     `db:"issue_key" json:"issue_key"`
	IssueType            string     `db:"issue_type" json:"issue_type"`
	Labels               []string   `db:"labels" json:"labels,omitempty"`
	Reporter             string     `db:"reporter" json:"reporter,omitempty"`
	Title                string     `db:"title" json:"title"`
	Description          string     `db:"description" json:"description"`
	Status               string     `db:"status" json:"status"`
	Priority             int        `db:"priority" json:"priority"`
	Position             float64    `db:"position" json:"position"`
	Owner                string     `db:"owner" json:"owner"`
	DueAt                *time.Time `db:"due_at" json:"due_at,omitempty"`
	StartedAt            *time.Time `db:"started_at" json:"started_at,omitempty"`
	CompletedAt          *time.Time `db:"completed_at" json:"completed_at,omitempty"`
	CreatedAt            time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt            time.Time  `db:"updated_at" json:"updated_at"`
	DeletedAt            *time.Time `db:"deleted_at" json:"deleted_at,omitempty"`
	WorktreeIDs          []int64    `db:"-" json:"worktree_ids,omitempty"`
	RequiredCapabilities []string   `db:"-" json:"required_capabilities,omitempty"`
}

// WorkItemComment is a human or agent note attached to an issue.
type WorkItemComment struct {
	ID         int64      `db:"id" json:"id"`
	WorkItemID int64      `db:"work_item_id" json:"work_item_id"`
	Author     string     `db:"author" json:"author"`
	Body       string     `db:"body" json:"body"`
	CreatedAt  time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt  time.Time  `db:"updated_at" json:"updated_at"`
	DeletedAt  *time.Time `db:"deleted_at" json:"deleted_at,omitempty"`
}

// WorkItemEdge is a directed relationship in the execution graph. For a
// blocks edge, from_item_id blocks to_item_id.
type WorkItemEdge struct {
	ID          int64      `db:"id" json:"id"`
	NamespaceID int64      `db:"namespace_id" json:"namespace_id"`
	FromItemID  int64      `db:"from_item_id" json:"from_item_id"`
	ToItemID    int64      `db:"to_item_id" json:"to_item_id"`
	EdgeType    string     `db:"edge_type" json:"edge_type"`
	CreatedAt   time.Time  `db:"created_at" json:"created_at"`
	DeletedAt   *time.Time `db:"deleted_at" json:"deleted_at,omitempty"`
}

// Worktree is a local Git worktree registered by an agent-side bridge.
// Paths and repository names are metadata; Git remains the source of code.
type Worktree struct {
	ID                    int64           `db:"id" json:"id"`
	NamespaceID           int64           `db:"namespace_id" json:"namespace_id"`
	WorkspaceRepositoryID *int64          `db:"workspace_repository_id" json:"workspace_repository_id,omitempty"`
	WorktreeKey           *string         `db:"worktree_key" json:"worktree_key,omitempty"`
	Repository            string          `db:"repository" json:"repository"`
	WorktreePath          string          `db:"worktree_path" json:"worktree_path"`
	GitDir                string          `db:"git_dir" json:"git_dir,omitempty"`
	WorktreeSlot          string          `db:"worktree_slot" json:"worktree_slot,omitempty"`
	Branch                string          `db:"branch" json:"branch"`
	HeadSHA               string          `db:"head_sha" json:"head_sha"`
	Status                string          `db:"status" json:"status"`
	AgentID               string          `db:"agent_id" json:"agent_id"`
	LastSeenAt            *time.Time      `db:"last_seen_at" json:"last_seen_at,omitempty"`
	StaleAt               *time.Time      `db:"stale_at" json:"stale_at,omitempty"`
	MissingSince          *time.Time      `db:"missing_since" json:"missing_since,omitempty"`
	RemovedAt             *time.Time      `db:"removed_at" json:"removed_at,omitempty"`
	Metadata              json.RawMessage `db:"metadata" json:"metadata"`
	CreatedAt             time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt             time.Time       `db:"updated_at" json:"updated_at"`
	DeletedAt             *time.Time      `db:"deleted_at" json:"deleted_at,omitempty"`
}

// WorkEvent is an append-only structured observation from a local agent or
// worktree bridge.
type WorkEvent struct {
	ID          int64           `db:"id" json:"id"`
	NamespaceID int64           `db:"namespace_id" json:"namespace_id"`
	WorktreeID  *int64          `db:"worktree_id" json:"worktree_id,omitempty"`
	WorkItemID  *int64          `db:"work_item_id" json:"work_item_id,omitempty"`
	AttemptID   *int64          `db:"attempt_id" json:"attempt_id,omitempty"`
	EventType   string          `db:"event_type" json:"event_type"`
	EventKey    *string         `db:"event_key" json:"event_key,omitempty"`
	Payload     json.RawMessage `db:"payload" json:"payload"`
	OccurredAt  time.Time       `db:"occurred_at" json:"occurred_at"`
	CreatedAt   time.Time       `db:"created_at" json:"created_at"`
}

// WorkItemMemoryLink connects operational work to durable memory entities.
type WorkItemMemoryLink struct {
	Derived    bool      `json:"derived,omitempty"`
	WorkItemID int64     `db:"work_item_id" json:"work_item_id"`
	MemoryType string    `db:"memory_type" json:"memory_type"`
	MemoryID   int64     `db:"memory_id" json:"memory_id"`
	Relation   string    `db:"relation" json:"relation"`
	CreatedAt  time.Time `db:"created_at" json:"created_at"`
}

// WorkGraph is the graph view consumed by the board and graph UI.
type WorkGraph struct {
	Nodes     []WorkItem     `json:"nodes"`
	Edges     []WorkItemEdge `json:"edges"`
	Worktrees []Worktree     `json:"worktrees"`
}

// Failure records what didn't work, why, and what to do instead.
type Failure struct {
	ID          int64      `db:"id" json:"id"`
	NamespaceID int64      `db:"namespace_id" json:"namespace_id"`
	GoalID      *int64     `db:"goal_id" json:"goal_id,omitempty"`
	Content     string     `db:"content" json:"content"`
	Reason      string     `db:"reason" json:"reason"`
	Lesson      string     `db:"lesson" json:"lesson"`
	CreatedAt   time.Time  `db:"created_at" json:"created_at"`
	DeletedAt   *time.Time `db:"deleted_at" json:"deleted_at,omitempty"`
}

// ConsolidationProgress tracks per-stage checkpoint per namespace.
type ConsolidationProgress struct {
	NamespaceID            int64      `db:"namespace_id" json:"namespace_id"`
	LastEpisodeID          int64      `db:"last_episode_id" json:"last_episode_id"`
	LastFactID             int64      `db:"last_fact_id" json:"last_fact_id"`
	LastRelationshipID     int64      `db:"last_relationship_id" json:"last_relationship_id"`
	LastPatternFactID      int64      `db:"last_pattern_fact_id" json:"last_pattern_fact_id"`
	LastPatternRelID       int64      `db:"last_pattern_rel_id" json:"last_pattern_rel_id"`
	LastGoalProgressFactID int64      `db:"last_goal_progress_fact_id" json:"last_goal_progress_fact_id"`
	LastFailureID          int64      `db:"last_failure_id" json:"last_failure_id"`
	LastFailureEpisodeID   int64      `db:"last_failure_episode_id" json:"last_failure_episode_id"`
	LastHypothesisFactID   int64      `db:"last_hypothesis_fact_id" json:"last_hypothesis_fact_id"`
	LastCausalFactID       int64      `db:"last_causal_fact_id" json:"last_causal_fact_id"`
	LastDecayRun           *time.Time `db:"last_decay_run" json:"last_decay_run,omitempty"`
	LastRun                *time.Time `db:"last_run" json:"last_run,omitempty"`
	UpdatedAt              time.Time  `db:"updated_at" json:"updated_at"`
}

// Setting is a key-value store for operational state.
type Setting struct {
	Key       string    `db:"key" json:"key"`
	Value     string    `db:"value" json:"value"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

// EmbeddingCache stores computed embeddings by text hash and model.
type EmbeddingCache struct {
	TextHash  string          `db:"text_hash" json:"text_hash"`
	Model     string          `db:"model" json:"model"`
	Text      string          `db:"text" json:"text"`
	Embedding pgvector.Vector `db:"embedding" json:"-"`
	CreatedAt time.Time       `db:"created_at" json:"created_at"`
}

// RecallResult carries a row from recall queries.
type RecallResult struct {
	ID             int64           `db:"id" json:"id"`
	NamespaceID    int64           `db:"namespace_id" json:"namespace_id"`
	Content        string          `db:"content" json:"content"`
	Embedding      pgvector.Vector `db:"embedding" json:"-"`
	EmbeddingModel string          `db:"embedding_model" json:"embedding_model"`
	OccurredAt     time.Time       `db:"occurred_at" json:"occurred_at"`
	CreatedAt      time.Time       `db:"created_at" json:"created_at"`
	Score          float32         `db:"score" json:"score"`
}
