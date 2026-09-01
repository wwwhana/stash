package brain

import (
	"context"
	"errors"
	"testing"
)

func TestDeleteNamespaceRejectsRoot(t *testing.T) {
	b := &Brain{}
	for _, slug := range []string{"", "/"} {
		if err := b.DeleteNamespace(context.Background(), slug); !errors.Is(err, ErrCannotDeleteRootNamespace) {
			t.Errorf("DeleteNamespace(%q) error = %v, want root namespace error", slug, err)
		}
	}
}

func TestNamespaceSoftDeleteAndRestore(t *testing.T) {
	b, ctx, namespaceID := newWorkExecutionTestBrain(t)
	var slug string
	if err := b.pool.QueryRow(ctx, "SELECT slug FROM namespaces WHERE id = $1", namespaceID).Scan(&slug); err != nil {
		t.Fatalf("read namespace slug: %v", err)
	}
	childSlug := slug + "/child"
	t.Cleanup(func() {
		_, _ = b.pool.Exec(context.Background(),
			"DELETE FROM namespaces WHERE slug = $1 OR slug LIKE $2",
			slug, likePatternForDescendants(slug),
		)
	})
	if _, err := b.CreateNamespace(ctx, childSlug, "child", ""); err != nil {
		t.Fatalf("create child namespace: %v", err)
	}

	if err := b.DeleteNamespace(ctx, slug); err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}
	if _, err := b.GetNamespace(ctx, slug); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("GetNamespace after delete error = %v, want namespace not found", err)
	}
	if _, err := b.ResolveNamespaceIDs(ctx, []string{slug}); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("ResolveNamespaceIDs after delete error = %v, want namespace not found", err)
	}
	namespaces, err := b.ListNamespaces(ctx, nil, Pagination{})
	if err != nil {
		t.Fatalf("ListNamespaces after delete: %v", err)
	}
	for _, namespace := range namespaces {
		if namespace.Slug == slug || namespace.Slug == childSlug {
			t.Fatalf("deleted namespace %q remained in list", namespace.Slug)
		}
	}
	var deleted int
	if err := b.pool.QueryRow(ctx,
		"SELECT count(*) FROM namespaces WHERE (slug = $1 OR slug = $2) AND deleted_at IS NOT NULL",
		slug, childSlug,
	).Scan(&deleted); err != nil {
		t.Fatalf("check deleted namespaces: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted namespace rows = %d, want 2", deleted)
	}

	newNamespaceID, err := b.CreateNamespace(ctx, slug, "new project", "")
	if err != nil {
		t.Fatalf("create new namespace at deleted path: %v", err)
	}
	if newNamespaceID == namespaceID {
		t.Fatalf("new namespace reused deleted namespace id %d", namespaceID)
	}
	created, err := b.GetNamespace(ctx, slug)
	if err != nil {
		t.Fatalf("GetNamespace after new create: %v", err)
	}
	if created.Name != "new project" {
		t.Fatalf("new namespace name = %q, want new project", created.Name)
	}
	if _, err := b.GetNamespace(ctx, childSlug); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("deleted child became visible after new parent creation: %v", err)
	}
}
