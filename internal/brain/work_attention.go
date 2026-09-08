package brain

import (
	"sort"
	"time"

	"github.com/alash3al/stash/internal/models"
)

// Derive reminders from current records; agents never maintain a second list.
func goalMapAttention(value *models.GoalMap, now time.Time) []models.GoalMapAttention {
	items := []models.GoalMapAttention{}
	for _, goal := range value.GoalTree.Goals {
		reason := ""
		if goal.CompletionMismatch {
			reason = "goal_mismatch"
		} else if goal.ReadyToComplete && goal.Status == "active" {
			reason = "goal_ready"
		}
		if reason != "" {
			items = append(items, models.GoalMapAttention{Key: goalMapNodeKey("goal", goal.ID), Reason: reason})
		}
	}
	for _, work := range append(append([]models.GoalMapWork{}, value.WorkItems...), value.UnassignedWork...) {
		// Components summarize child tasks; remind once at the actionable task.
		if work.ExecutionProgress != nil && work.ExecutionProgress.Total > 0 {
			continue
		}
		status := work.Status
		if work.ExecutionProgress != nil {
			status = work.ExecutionProgress.Status
		}
		if status == "done" || status == "canceled" {
			continue
		}
		reason := ""
		switch {
		case work.ExecutionProgress == nil && (work.AttemptStatus == "expired" || work.AttemptStatus == "active" && work.LeaseExpires != nil && !work.LeaseExpires.After(now)):
			reason = "work_expired"
		case status == "blocked":
			reason = "work_blocked"
		case status == "review":
			reason = "work_review"
		}
		if reason != "" {
			items = append(items, models.GoalMapAttention{Key: goalMapNodeKey("work", work.ID), Reason: reason})
		}
	}
	priority := map[string]int{"goal_mismatch": 0, "work_expired": 1, "work_blocked": 2, "work_review": 3, "goal_ready": 4}
	sort.SliceStable(items, func(i, j int) bool { return priority[items[i].Reason] < priority[items[j].Reason] })
	return items
}
