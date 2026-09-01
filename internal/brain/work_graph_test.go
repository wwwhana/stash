package brain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWorkGraphInputValidation(t *testing.T) {
	for _, status := range []string{"backlog", "ready", "doing", "blocked", "review", "done", "canceled"} {
		if err := validateWorkItemStatus(status); err != nil {
			t.Errorf("status %q rejected: %v", status, err)
		}
	}
	if err := validateWorkItemStatus("unknown"); err == nil {
		t.Fatal("unknown work item status was accepted")
	}
	if err := validateWorktreeStatus("dirty"); err != nil {
		t.Fatalf("dirty worktree status rejected: %v", err)
	}
	if err := validateWorktreeStatus("active"); err == nil {
		t.Fatal("unknown worktree status was accepted")
	}
	if err := validatePosition(0.5); err != nil {
		t.Fatalf("finite position rejected: %v", err)
	}
	for _, issueType := range []string{"task", "bug", "feature", "chore", "question", "component"} {
		if err := validateWorkItemType(issueType); err != nil {
			t.Errorf("issue type %q rejected: %v", issueType, err)
		}
	}
	if err := validateWorkItemType("incident"); err == nil {
		t.Fatal("unknown issue type was accepted")
	}
	labels, err := normalizeWorkItemLabels([]string{" bug ", "ui", "bug", ""})
	if err != nil {
		t.Fatalf("labels rejected: %v", err)
	}
	if len(labels) != 2 || labels[0] != "bug" || labels[1] != "ui" {
		t.Fatalf("labels = %#v, want deduplicated labels", labels)
	}
}

func TestNormalizeWorkGoalDBError(t *testing.T) {
	err := normalizeWorkGoalDBError(&pgconn.PgError{
		Code:    "23514",
		Message: "work goal must be active and share the work namespace",
	})
	if !errors.Is(err, ErrWorkGoalInvalid) {
		t.Fatalf("goal trigger error = %v, want %v", err, ErrWorkGoalInvalid)
	}
	if got := err.Error(); !strings.Contains(got, "work goal must be active") {
		t.Fatalf("normalized error lost the database reason: %v", got)
	}

	other := normalizeWorkGoalDBError(&pgconn.PgError{Code: "23514", Message: "unrelated constraint"})
	if errors.Is(other, ErrWorkGoalInvalid) {
		t.Fatalf("unrelated constraint was classified as a work-goal error: %v", other)
	}
}

func TestWorkGraphKeysetSnapshotHandlesBoundaryInsertAndStaleChanges(t *testing.T) {
	dsn := os.Getenv("STASH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("STASH_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	b := &Brain{pool: pool, config: DefaultConfig()}
	slug := fmt.Sprintf("/work-graph-page-%d", time.Now().UnixNano())
	var nsID int64
	if err := pool.QueryRow(ctx, `INSERT INTO namespaces (slug,name,description) VALUES ($1,$1,'') RETURNING id`, slug).Scan(&nsID); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `DELETE FROM namespaces WHERE id=$1`, nsID)
	ids := make([]int64, 4)
	for i := range ids {
		if err := pool.QueryRow(ctx, `INSERT INTO work_items(namespace_id,title,description,status,priority,position) VALUES($1,$2,'','ready',$3,$4) RETURNING id`, nsID, fmt.Sprintf("node-%d", i), 2-i/2, float64(i%2)).Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			if _, err := pool.Exec(ctx, `INSERT INTO work_item_edges(namespace_id,from_item_id,to_item_id,edge_type) VALUES($1,$2,$3,'blocks')`, nsID, ids[i-1], ids[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	first, cursor, nodeMore, edgeMore, err := b.GetWorkGraphPage(ctx, []string{slug}, false, nil, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Nodes) != 2 || len(first.Edges) != 1 || !nodeMore || !edgeMore {
		t.Fatalf("first page nodes=%d edges=%d more=%v/%v", len(first.Nodes), len(first.Edges), nodeMore, edgeMore)
	}
	var inserted int64
	if err := pool.QueryRow(ctx, `INSERT INTO work_items(namespace_id,title,description,status,priority,position) VALUES($1,'later','','ready',99,0) RETURNING id`, nsID).Scan(&inserted); err != nil {
		t.Fatal(err)
	}
	second, _, _, _, err := b.GetWorkGraphPage(ctx, []string{slug}, false, &cursor, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range second.Nodes {
		if node.ID == inserted {
			t.Fatal("post-snapshot insertion leaked into continuation")
		}
	}
	if len(second.Nodes) != 2 || second.Nodes[0].ID == first.Nodes[0].ID || second.Nodes[0].ID == first.Nodes[1].ID {
		t.Fatalf("keyset boundary duplicated or skipped nodes: first=%v second=%v", []int64{first.Nodes[0].ID, first.Nodes[1].ID}, []int64{second.Nodes[0].ID, second.Nodes[1].ID})
	}
	if _, err := pool.Exec(ctx, `UPDATE work_items SET title='changed',updated_at=clock_timestamp() WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := b.GetWorkGraphPage(ctx, []string{slug}, false, &cursor, 2, 2); !errors.Is(err, ErrWorkGraphCursorStale) {
		t.Fatalf("updated snapshot error=%v", err)
	}
	fresh, freshCursor, _, _, err := b.GetWorkGraphPage(ctx, []string{slug}, false, nil, 2, 2)
	if err != nil || len(fresh.Nodes) == 0 {
		t.Fatalf("fresh page: nodes=%d err=%v", len(fresh.Nodes), err)
	}
	if _, err := pool.Exec(ctx, `UPDATE work_item_edges SET deleted_at=clock_timestamp() WHERE namespace_id=$1 AND deleted_at IS NULL`, nsID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := b.GetWorkGraphPage(ctx, []string{slug}, false, &freshCursor, 2, 2); !errors.Is(err, ErrWorkGraphCursorStale) {
		t.Fatalf("deleted edge snapshot error=%v", err)
	}
}
