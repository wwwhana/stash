package brain

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func wikiTestInput(slug string, expected, source int64, markdown string) WikiSaveInput {
	title := "Wiki " + slug
	return WikiSaveInput{Slug: slug, ExpectedRevision: expected, Title: &title, Markdown: &markdown, SourceEpisodeIDs: []int64{source}}
}

func wikiTestSource(t *testing.T, b *Brain, ctx context.Context, namespaceID int64, content string) int64 {
	t.Helper()
	var id int64
	if err := b.pool.QueryRow(ctx, `INSERT INTO episodes(namespace_id,content,occurred_at) VALUES($1,$2,now()) RETURNING id`, namespaceID, content).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestWikiRevisionLifecyclePostgres(t *testing.T) {
	b, ctx, namespaceID := newWorkExecutionTestBrain(t)
	const original = "원문 내용은 문서를 갱신해도 유지됩니다."
	source := wikiTestSource(t, b, ctx, namespaceID, original)
	first, err := b.SaveWikiPage(ctx, namespaceID, wikiTestInput("topic", 0, source, "past-only-needle"))
	if err != nil || first.Revision != 1 {
		t.Fatalf("first save = %#v, %v", first, err)
	}
	second, err := b.SaveWikiPage(ctx, namespaceID, wikiTestInput("topic", 1, source, "current-only-needle 100% _literal_"))
	if err != nil || second.Revision != 2 || second.Slug != first.Slug {
		t.Fatalf("second save = %#v, %v", second, err)
	}
	if _, err := b.SaveWikiPage(ctx, namespaceID, wikiTestInput("other", 0, source, "unrelated body")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		count int
	}{
		{"past-only-needle", 0}, {"current-only-needle", 1}, {"%", 1}, {"_literal_", 1}, {"WIKI TOPIC", 1},
	} {
		list, err := b.ListWikiPages(ctx, namespaceID, tc.query, Pagination{})
		if err != nil || len(list.Pages) != tc.count {
			t.Fatalf("list query %q = %#v, %v", tc.query, list, err)
		}
	}
	list, err := b.ListWikiPages(ctx, namespaceID, "", Pagination{Limit: 1})
	if err != nil || len(list.Pages) != 1 || !list.HasMore || list.NextOffset != 1 {
		t.Fatalf("first list page = %#v, %v", list, err)
	}
	last, err := b.ListWikiPages(ctx, namespaceID, "", Pagination{Limit: 1, Offset: list.NextOffset})
	if err != nil || len(last.Pages) != 1 || last.HasMore || last.Pages[0].Slug == list.Pages[0].Slug {
		t.Fatalf("last list page = %#v, %v", last, err)
	}

	for _, expected := range []int64{0, 1, 99} {
		_, err := b.SaveWikiPage(ctx, namespaceID, wikiTestInput("topic", expected, source, "must not be saved"))
		var conflict *WikiRevisionConflict
		if !errors.As(err, &conflict) || conflict.CurrentRevision != 2 || conflict.ExpectedRevision != expected {
			t.Fatalf("expected %d conflict = %v", expected, err)
		}
	}
	deleted, err := b.SaveWikiPage(ctx, namespaceID, WikiSaveInput{Slug: "topic", ExpectedRevision: 2, Deleted: true})
	if err != nil || !deleted.Deleted || deleted.Revision != 3 || deleted.Title != "" || deleted.Markdown != "" || len(deleted.SourceEpisodeIDs)+len(deleted.RelatedSlugs) != 0 {
		t.Fatalf("tombstone = %#v, %v", deleted, err)
	}
	current, err := b.GetWikiPage(ctx, namespaceID, "topic", 0)
	if err != nil || !current.Deleted || current.Revision != 3 || current.Markdown != "" {
		t.Fatalf("current deleted version = %#v, %v", current, err)
	}
	for _, query := range []string{"", "current-only-needle", "past-only-needle"} {
		list, err := b.ListWikiPages(ctx, namespaceID, query, Pagination{})
		if err != nil {
			t.Fatal(err)
		}
		for _, page := range list.Pages {
			if page.Slug == "topic" {
				t.Fatalf("deleted page reappeared in query %q", query)
			}
		}
	}
	old, err := b.GetWikiPage(ctx, namespaceID, "topic", 1)
	if err != nil || old.CurrentRevision != 3 || !reflect.DeepEqual(old.WikiRevision, *first) {
		t.Fatalf("historical revision = %#v, %v", old, err)
	}
	if _, err := b.SaveWikiPage(ctx, namespaceID, WikiSaveInput{Slug: "topic", ExpectedRevision: 3, Deleted: true}); !errors.Is(err, ErrWikiDeleted) {
		t.Fatalf("repeat deletion = %v", err)
	}
	if _, err := b.SaveWikiPage(ctx, namespaceID, WikiSaveInput{Slug: "missing", Deleted: true}); !errors.Is(err, ErrWikiPageNotFound) {
		t.Fatalf("delete nonexistent document = %v", err)
	}
	restored, err := b.SaveWikiPage(ctx, namespaceID, wikiTestInput("topic", 3, source, "restored content"))
	if err != nil || restored.Deleted || restored.Revision != 4 {
		t.Fatalf("restore = %#v, %v", restored, err)
	}
	list, err = b.ListWikiPages(ctx, namespaceID, "restored", Pagination{})
	if err != nil || len(list.Pages) != 1 || list.Pages[0].Revision != 4 {
		t.Fatalf("restored search = %#v, %v", list, err)
	}
	old, err = b.GetWikiPage(ctx, namespaceID, "topic", 1)
	if err != nil || !reflect.DeepEqual(old.WikiRevision, *first) || old.CurrentRevision != 4 {
		t.Fatalf("restore changed history = %#v, %v", old, err)
	}
	var content string
	if err := b.pool.QueryRow(ctx, `SELECT content FROM episodes WHERE id=$1`, source).Scan(&content); err != nil || content != original {
		t.Fatalf("original changed = %q, %v", content, err)
	}
	var count int
	if err := b.pool.QueryRow(ctx, `SELECT count(*) FROM wiki_revisions WHERE namespace_id=$1 AND slug='topic'`, namespaceID).Scan(&count); err != nil || count != 4 {
		t.Fatalf("history count = %d, %v", count, err)
	}
}

func TestWikiSourcesAndRelatedPagesPostgres(t *testing.T) {
	b, ctx, namespaceID := newWorkExecutionTestBrain(t)
	source := wikiTestSource(t, b, ctx, namespaceID, "source preserved")
	otherSlug := fmt.Sprintf("/wiki-other-%d", time.Now().UnixNano())
	otherID, err := b.CreateNamespace(ctx, otherSlug, "Other", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = b.pool.Exec(context.Background(), `DELETE FROM namespaces WHERE id=$1`, otherID) })
	foreignSource := wikiTestSource(t, b, ctx, otherID, "foreign original")
	deletedSource := wikiTestSource(t, b, ctx, namespaceID, "deleted original")
	if _, err := b.pool.Exec(ctx, `UPDATE episodes SET deleted_at=now() WHERE id=$1`, deletedSource); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{foreignSource, deletedSource, 9223372036854775000} {
		if _, err := b.SaveWikiPage(ctx, namespaceID, wikiTestInput("invalid", 0, id, "body")); !errors.Is(err, ErrWikiSource) {
			t.Fatalf("invalid source %d = %v", id, err)
		}
	}
	if _, err := b.SaveWikiPage(ctx, otherID, wikiTestInput("foreign-only", 0, foreignSource, "foreign wiki")); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"foreign-only", "missing"} {
		input := wikiTestInput("invalid", 0, source, "body")
		input.RelatedSlugs = []string{slug}
		if _, err := b.SaveWikiPage(ctx, namespaceID, input); !errors.Is(err, ErrWikiRelatedPage) {
			t.Fatalf("invalid related page %q = %v", slug, err)
		}
	}
	if _, err := b.GetWikiPage(ctx, namespaceID, "foreign-only", 1); !errors.Is(err, ErrWikiPageNotFound) {
		t.Fatalf("foreign document read = %v", err)
	}
	if _, err := b.SaveWikiPage(ctx, namespaceID, wikiTestInput("related", 0, source, "linked document")); err != nil {
		t.Fatal(err)
	}
	input := wikiTestInput("topic", 0, source, "source-backed document")
	input.SourceEpisodeIDs = []int64{source, source}
	input.RelatedSlugs = []string{"related", "related"}
	saved, err := b.SaveWikiPage(ctx, namespaceID, input)
	if err != nil || len(saved.SourceEpisodeIDs) != 1 || len(saved.RelatedSlugs) != 1 {
		t.Fatalf("save with links = %#v, %v", saved, err)
	}
	page, err := b.GetWikiPage(ctx, namespaceID, "topic", 0)
	if err != nil || !page.Sources[0].Available || !page.RelatedPages[0].Available {
		t.Fatalf("active links = %#v, %v", page, err)
	}
	if _, err := b.SaveWikiPage(ctx, namespaceID, WikiSaveInput{Slug: "related", ExpectedRevision: 1, Deleted: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.pool.Exec(ctx, `UPDATE episodes SET deleted_at=now() WHERE id=$1`, source); err != nil {
		t.Fatal(err)
	}
	page, err = b.GetWikiPage(ctx, namespaceID, "topic", 0)
	if err != nil || page.Sources[0].Available || page.RelatedPages[0].Available || !reflect.DeepEqual(page.WikiRevision, *saved) {
		t.Fatalf("unavailable links changed history = %#v, %v", page, err)
	}
	if _, err := b.SaveWikiPage(ctx, namespaceID, wikiTestInput("topic", 1, source, "new body")); !errors.Is(err, ErrWikiSource) {
		t.Fatalf("reuse deleted source = %v", err)
	}
	activeSource := wikiTestSource(t, b, ctx, namespaceID, "active original")
	input = wikiTestInput("topic", 1, activeSource, "new body")
	input.RelatedSlugs = []string{"related"}
	if _, err := b.SaveWikiPage(ctx, namespaceID, input); !errors.Is(err, ErrWikiRelatedPage) {
		t.Fatalf("reuse deleted link = %v", err)
	}
	// Simulate out-of-band removal; the service itself offers no revision delete.
	if _, err := b.pool.Exec(ctx, `DELETE FROM wiki_revisions WHERE namespace_id=$1 AND slug='related'`, namespaceID); err != nil {
		t.Fatal(err)
	}
	page, err = b.GetWikiPage(ctx, namespaceID, "topic", 1)
	if err != nil || page.RelatedPages[0].Available || page.RelatedPages[0].Slug != "related" {
		t.Fatalf("missing link = %#v, %v", page, err)
	}
	list, err := b.ListWikiPages(ctx, namespaceID, "source-backed", Pagination{})
	if err != nil || len(list.Pages) != 1 {
		t.Fatalf("source deletion expired wiki = %#v, %v", list, err)
	}
	if _, err := b.pool.Exec(ctx, `UPDATE namespaces SET deleted_at=now() WHERE id=$1`, namespaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.GetWikiPage(ctx, namespaceID, "topic", 1); !errors.Is(err, ErrWikiPageNotFound) {
		t.Fatalf("deleted namespace read = %v", err)
	}
	if _, err := b.SaveWikiPage(ctx, namespaceID, wikiTestInput("topic", 1, activeSource, "body")); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("deleted namespace write = %v", err)
	}
}

func TestWikiConcurrentSavesPostgres(t *testing.T) {
	b, ctx, namespaceID := newWorkExecutionTestBrain(t)
	source := wikiTestSource(t, b, ctx, namespaceID, "immutable original")
	for _, expected := range []int64{0, 1} {
		start := make(chan struct{})
		results := make(chan error, 8)
		for i := 0; i < 8; i++ {
			go func(i int) {
				<-start
				_, err := b.SaveWikiPage(ctx, namespaceID, wikiTestInput("topic", expected, source, fmt.Sprintf("writer %d", i)))
				results <- err
			}(i)
		}
		close(start)
		successes := 0
		for i := 0; i < 8; i++ {
			err := <-results
			if err == nil {
				successes++
				continue
			}
			var conflict *WikiRevisionConflict
			if !errors.As(err, &conflict) || conflict.ExpectedRevision != expected || conflict.CurrentRevision != expected+1 {
				t.Fatalf("concurrent result = %v", err)
			}
		}
		if successes != 1 {
			t.Fatalf("expected revision %d produced %d successful saves", expected, successes)
		}
	}
	var count int
	if err := b.pool.QueryRow(ctx, `SELECT count(*) FROM wiki_revisions WHERE namespace_id=$1`, namespaceID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("concurrent history count = %d, %v", count, err)
	}
}

func TestWikiInputRejectsInvalidDocuments(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*WikiSaveInput)
	}{
		{"absolute slug", func(i *WikiSaveInput) { i.Slug = "/topic" }},
		{"traversal slug", func(i *WikiSaveInput) { i.Slug = "../topic" }},
		{"no sources", func(i *WikiSaveInput) { i.SourceEpisodeIDs = nil }},
		{"invalid source", func(i *WikiSaveInput) { i.SourceEpisodeIDs = []int64{0} }},
		{"too many sources", func(i *WikiSaveInput) { i.SourceEpisodeIDs = make([]int64, MaxWikiSources+1) }},
		{"missing title", func(i *WikiSaveInput) { i.Title = nil }},
		{"empty body", func(i *WikiSaveInput) { value := " \n"; i.Markdown = &value }},
		{"UTF-8 byte limit", func(i *WikiSaveInput) { value := strings.Repeat("글", MaxWikiMarkdownBytes/3+1); i.Markdown = &value }},
		{"invalid UTF-8", func(i *WikiSaveInput) { value := string([]byte{0xff}); i.Markdown = &value }},
		{"negative revision", func(i *WikiSaveInput) { i.ExpectedRevision = -1 }},
		{"delete with title", func(i *WikiSaveInput) { i.Deleted = true; i.Markdown = nil; i.SourceEpisodeIDs = nil }},
		{"delete with empty sources", func(i *WikiSaveInput) {
			i.Deleted = true
			i.Title = nil
			i.Markdown = nil
			i.SourceEpisodeIDs = []int64{}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := wikiTestInput("topic", 0, 1, "valid markdown")
			tc.edit(&input)
			if _, err := validateWikiInput(input); err == nil {
				t.Fatal("invalid document accepted")
			}
		})
	}
}
