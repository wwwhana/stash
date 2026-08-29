package models

import "time"

// AgentWorkItem is the minimum work identity needed to choose or continue an
// item. The full tracker record remains available from the explicit full view.
type AgentWorkItem struct {
	ID                   int64    `json:"id"`
	GoalID               *int64   `json:"goal_id,omitempty"`
	IssueKey             string   `json:"issue_key"`
	Title                string   `json:"title"`
	Status               string   `json:"status"`
	Owner                string   `json:"owner,omitempty"`
	NextAction           string   `json:"next_action,omitempty"`
	RequiredCapabilities []string `json:"required_capabilities,omitempty"`
}

type AgentAttempt struct {
	ID             int64      `json:"id"`
	AgentID        string     `json:"agent_id"`
	Status         string     `json:"status"`
	WorktreeID     *int64     `json:"worktree_id,omitempty"`
	LeaseExpiresAt time.Time  `json:"lease_expires_at"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
}

type AgentCheckpoint struct {
	Summary    string    `json:"summary"`
	Result     string    `json:"result"`
	NextAction string    `json:"next_action"`
	CreatedAt  time.Time `json:"created_at"`
}

type AgentCondition struct {
	ID            int64  `json:"id"`
	Description   string `json:"description"`
	Required      bool   `json:"required"`
	Status        string `json:"status"`
	EvidenceCount int    `json:"evidence_count"`
}

type AgentMemory struct {
	MemoryType string `json:"memory_type"`
	MemoryID   int64  `json:"memory_id"`
	Relation   string `json:"relation"`
	Content    string `json:"content"`
	Status     string `json:"status"`
}

// WorkResumeBrief is the default, quota-conscious work context. Lists with
// full evidence, events, and worktree metadata are opt-in through detail=full.
type WorkResumeBrief struct {
	ContextDigest         string                    `json:"context_digest"`
	WorkItem              AgentWorkItem             `json:"work_item"`
	GoalContext           *WorkGoalContext          `json:"goal_context,omitempty"`
	PlanContext           *WorkPlanExecutionContext `json:"plan_context,omitempty"`
	NextAction            string                    `json:"next_action"`
	LatestAttempt         *AgentAttempt             `json:"latest_attempt,omitempty"`
	LatestCheckpoint      *AgentCheckpoint          `json:"latest_checkpoint,omitempty"`
	CompletionConditions  []AgentCondition          `json:"completion_conditions"`
	RelevantMemory        []AgentMemory             `json:"relevant_memory"`
	Resources             []WorkResourceRef         `json:"resources"`
	DependencyResults     []WorkDependencyResult    `json:"dependency_results"`
	Blockers              []AgentWorkItem           `json:"blockers"`
	Totals                WorkResumeTotals          `json:"totals"`
	MoreConditions        bool                      `json:"more_conditions,omitempty"`
	MoreMemory            bool                      `json:"more_memory,omitempty"`
	MoreResources         bool                      `json:"more_resources,omitempty"`
	MoreDependencyResults bool                      `json:"more_dependency_results,omitempty"`
	MoreBlockers          bool                      `json:"more_blockers,omitempty"`
}

type AgentWorkspaceCounts struct {
	Goals   int `json:"goals"`
	Ready   int `json:"ready"`
	Doing   int `json:"doing"`
	Blocked int `json:"blocked"`
	Done    int `json:"done"`
}

// WorkspaceResumeBrief gives a new agent only the shared outcome, its current
// continuation, and a short work selection. The map and full plan stay in the
// owner-facing or explicit full response.
type WorkspaceResumeBrief struct {
	ContextDigest string               `json:"context_digest"`
	NamespaceID   int64                `json:"namespace_id"`
	Namespace     string               `json:"namespace"`
	GoalContext   *WorkGoalContext     `json:"goal_context,omitempty"`
	SharedGoal    *GoalBrief           `json:"shared_goal,omitempty"`
	CurrentWork   *AgentWorkItem       `json:"current_work,omitempty"`
	NextAction    string               `json:"next_action,omitempty"`
	WorkSelection []AgentWorkItem      `json:"work_selection"`
	PlanDigest    string               `json:"plan_digest"`
	Counts        AgentWorkspaceCounts `json:"counts"`
}

// AgentContextReceipt is returned when the caller already has the same digest.
type AgentContextReceipt struct {
	Unchanged     bool   `json:"unchanged"`
	ContextDigest string `json:"context_digest"`
	Scope         string `json:"scope"`
	ID            int64  `json:"id,omitempty"`
	NextAction    string `json:"next_action,omitempty"`
}
