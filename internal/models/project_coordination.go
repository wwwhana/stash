package models

import (
	"encoding/json"
	"time"
)

// WorkResource is a small pointer to material used by a work item. Large
// documents and connector credentials remain in their original systems.
type WorkResource struct {
	ID            int64           `db:"id" json:"id"`
	NamespaceID   int64           `db:"namespace_id" json:"namespace_id"`
	ResourceKey   string          `db:"resource_key" json:"resource_key"`
	Kind          string          `db:"kind" json:"kind"`
	Source        string          `db:"source" json:"source"`
	Authority     string          `db:"authority" json:"authority"`
	Title         string          `db:"title" json:"title"`
	URI           string          `db:"uri" json:"uri,omitempty"`
	Summary       string          `db:"summary" json:"summary,omitempty"`
	ExternalID    string          `db:"external_id" json:"external_id,omitempty"`
	Revision      string          `db:"revision" json:"revision,omitempty"`
	ContentDigest string          `db:"content_digest" json:"content_digest,omitempty"`
	Metadata      json.RawMessage `db:"metadata" json:"metadata"`
	CreatedBy     string          `db:"created_by" json:"created_by,omitempty"`
	CreatedAt     time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt     time.Time       `db:"updated_at" json:"updated_at"`
	DeletedAt     *time.Time      `db:"deleted_at" json:"deleted_at,omitempty"`
}

type WorkResourceLink struct {
	ID          int64     `db:"id" json:"id"`
	NamespaceID int64     `db:"namespace_id" json:"namespace_id"`
	WorkItemID  int64     `db:"work_item_id" json:"work_item_id"`
	ResourceID  int64     `db:"resource_id" json:"resource_id"`
	Role        string    `db:"role" json:"role"`
	LinkedBy    string    `db:"linked_by" json:"linked_by,omitempty"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
}

// WorkResourceRef is the bounded form returned to an agent. It contains a
// summary and URI, never an external document body or connector secret.
type WorkResourceRef struct {
	ID            int64  `json:"id"`
	ResourceKey   string `json:"resource_key"`
	Kind          string `json:"kind"`
	Source        string `json:"source"`
	Authority     string `json:"authority"`
	Title         string `json:"title"`
	URI           string `json:"uri,omitempty"`
	Summary       string `json:"summary,omitempty"`
	ExternalID    string `json:"external_id,omitempty"`
	Revision      string `json:"revision,omitempty"`
	ContentDigest string `json:"content_digest,omitempty"`
	Role          string `json:"role"`
}

type WorkResourceAttachment struct {
	Resource WorkResource     `json:"resource"`
	Link     WorkResourceLink `json:"link"`
}

// WorkDependencyResult gives a downstream agent only the final observation of
// a completed prerequisite instead of replaying that prerequisite's history.
type WorkDependencyResult struct {
	WorkItem AgentWorkItem `json:"work_item"`
	Summary  string        `json:"summary,omitempty"`
	Result   string        `json:"result,omitempty"`
}

type ProjectResumeCounts struct {
	Goals   int `json:"goals"`
	Ready   int `json:"ready"`
	Doing   int `json:"doing"`
	Blocked int `json:"blocked"`
	Done    int `json:"done"`
}

// ProjectResumeBrief is the universal Web MCP entry point. It intentionally
// contains only active work and a few runnable candidates; the owner-facing
// goal map remains a separate view.
type ProjectResumeBrief struct {
	ContextDigest string              `json:"context_digest"`
	NamespaceID   int64               `json:"namespace_id"`
	Namespace     string              `json:"namespace"`
	SharedGoal    *GoalBrief          `json:"shared_goal,omitempty"`
	ActiveWork    []AgentWorkItem     `json:"active_work"`
	ReadyWork     []AgentWorkItem     `json:"ready_work"`
	NextAction    string              `json:"next_action,omitempty"`
	Counts        ProjectResumeCounts `json:"counts"`
	MoreActive    bool                `json:"more_active,omitempty"`
	MoreReady     bool                `json:"more_ready,omitempty"`
}

type SpawnedWork struct {
	WorkItem             WorkItem        `json:"work_item"`
	Relationship         string          `json:"relationship"`
	Edge                 *WorkItemEdge   `json:"edge,omitempty"`
	RequiredCapabilities []string        `json:"required_capabilities"`
	Preparation          WorkPreparation `json:"preparation"`
}
