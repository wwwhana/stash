package models

import "time"

// GoalProgress is one node in the shared project goal tree. Progress is
// derived from executable leaf work across the goal subtree.
type GoalProgress struct {
	Goal
	Depth              int     `json:"depth"`
	Path               []int64 `json:"path"`
	DirectWorkTotal    int     `json:"direct_work_total"`
	DirectWorkDone     int     `json:"direct_work_done"`
	SubtreeWorkTotal   int     `json:"subtree_work_total"`
	SubtreeWorkDone    int     `json:"subtree_work_done"`
	ChildGoalTotal     int     `json:"child_goal_total"`
	ChildGoalCompleted int     `json:"child_goal_completed"`
	Progress           float64 `json:"progress"`
	ReadyToComplete    bool    `json:"ready_to_complete"`
	CompletionMismatch bool    `json:"completion_mismatch,omitempty"`
}

// GoalTree is the common outcome hierarchy returned to every project agent.
type GoalTree struct {
	RootGoalID *int64         `json:"root_goal_id,omitempty"`
	Goals      []GoalProgress `json:"goals"`
}

// GoalMemoryLink connects durable memory directly to a goal.
type GoalMemoryLink struct {
	GoalID     int64     `db:"goal_id" json:"goal_id"`
	MemoryType string    `db:"memory_type" json:"memory_type"`
	MemoryID   int64     `db:"memory_id" json:"memory_id"`
	Relation   string    `db:"relation" json:"relation"`
	CreatedAt  time.Time `db:"created_at" json:"created_at"`
}

// GoalMapMemory is the bounded memory content rendered in the goal map.
type GoalMapMemory struct {
	Key              string `json:"key"`
	MemoryType       string `json:"memory_type"`
	MemoryID         int64  `json:"memory_id"`
	Content          string `json:"content"`
	Status           string `json:"status"`
	ContentTruncated bool   `json:"content_truncated,omitempty"`
}

// GoalMapWork adds the small live execution summary needed by the owner-facing
// map. Full attempt history remains available from resume_work.
type GoalMapWork struct {
	ID       int64   `json:"id"`
	GoalID   *int64  `json:"goal_id,omitempty"`
	ParentID *int64  `json:"parent_id,omitempty"`
	IssueKey string  `json:"issue_key"`
	Title    string  `json:"title"`
	Status   string  `json:"status"`
	Priority int     `json:"priority"`
	Position float64 `json:"position"`
	Owner    string  `json:"owner,omitempty"`

	AgentID       string     `json:"agent_id,omitempty"`
	AttemptStatus string     `json:"attempt_status,omitempty"`
	LeaseExpires  *time.Time `json:"lease_expires_at,omitempty"`
	LatestResult  string     `json:"latest_result,omitempty"`
	NextAction    string     `json:"next_action,omitempty"`
}

// GoalMapGoal omits notes, timestamps, and the repeated namespace ID. The map
// only needs hierarchy, status, and rolled-up progress.
type GoalMapGoal struct {
	ID                 int64   `json:"id"`
	ParentID           *int64  `json:"parent_id,omitempty"`
	Content            string  `json:"content"`
	Status             string  `json:"status"`
	Depth              int     `json:"depth"`
	DirectWorkTotal    int     `json:"direct_work_total"`
	DirectWorkDone     int     `json:"direct_work_done"`
	SubtreeWorkTotal   int     `json:"subtree_work_total"`
	SubtreeWorkDone    int     `json:"subtree_work_done"`
	ChildGoalTotal     int     `json:"child_goal_total"`
	ChildGoalCompleted int     `json:"child_goal_completed"`
	Progress           float64 `json:"progress"`
	ReadyToComplete    bool    `json:"ready_to_complete"`
	CompletionMismatch bool    `json:"completion_mismatch,omitempty"`
}

type GoalMapTree struct {
	RootGoalID *int64        `json:"root_goal_id,omitempty"`
	Goals      []GoalMapGoal `json:"goals"`
}

// GoalMapEdge is a typed relation between memory, work, and goal nodes.
type GoalMapEdge struct {
	Key      string `json:"key"`
	From     string `json:"from"`
	To       string `json:"to"`
	Relation string `json:"relation"`
}

// GoalMap projects durable memory through executable work into the shared
// hierarchical outcome. Work items without a goal stay visible as warnings.
type GoalMap struct {
	GoalTree                GoalMapTree     `json:"goal_tree"`
	RootCandidates          []GoalBrief     `json:"root_candidates"`
	RootCandidatesTruncated bool            `json:"root_candidates_truncated,omitempty"`
	WorkItems               []GoalMapWork   `json:"work_items"`
	Memories                []GoalMapMemory `json:"memories"`
	Edges                   []GoalMapEdge   `json:"edges"`
	UnassignedWork          []GoalMapWork   `json:"unassigned_work"`
}

// GoalBrief deliberately omits notes, timestamps, and unrelated goal fields.
// It is the bounded goal representation sent to quota-constrained agents.
type GoalBrief struct {
	ID                 int64   `json:"id"`
	ParentID           *int64  `json:"parent_id,omitempty"`
	Content            string  `json:"content"`
	Status             string  `json:"status"`
	Progress           float64 `json:"progress"`
	SubtreeWorkDone    int     `json:"work_done"`
	SubtreeWorkTotal   int     `json:"work_total"`
	ChildGoalCompleted int     `json:"child_goals_done"`
	ChildGoalTotal     int     `json:"child_goals_total"`
}

// WorkGoalContext keeps a focused agent aligned with the project root while
// avoiding a second full project snapshot in resume_work. ContextDigest lets a
// caller avoid receiving the same text again.
type WorkGoalContext struct {
	ContextDigest     string      `json:"context_digest,omitempty"`
	RootGoalID        *int64      `json:"root_goal_id,omitempty"`
	CurrentGoalID     *int64      `json:"current_goal_id,omitempty"`
	Path              []GoalBrief `json:"path"`
	PathTotal         int         `json:"path_total"`
	PathTruncated     bool        `json:"path_truncated,omitempty"`
	Siblings          []GoalBrief `json:"siblings"`
	SiblingTotal      int         `json:"sibling_total"`
	SiblingsTruncated bool        `json:"siblings_truncated,omitempty"`
}
