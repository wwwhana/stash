package brain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/alash3al/stash/internal/queries"
	"github.com/alash3al/stash/internal/reasoner"
)

// wikiTestAuthor returns a canned, cited draft so compile can be exercised
// without a model.
type wikiTestAuthor struct {
	contradictionTestReasoner
	requests []reasoner.WikiDraftRequest
}

func (a *wikiTestAuthor) DraftWikiPage(_ context.Context, request reasoner.WikiDraftRequest) (*reasoner.WikiDraft, error) {
	a.requests = append(a.requests, request)
	var cites []string
	for _, source := range request.Sources {
		cites = append(cites, "[@"+source.Ref+"]")
	}
	return &reasoner.WikiDraft{
		Title:   "Staging deploys",
		Summary: "What breaks staging deploys and why.",
		Content: "# Staging deploys\n\nMigrations time out on staging " + strings.Join(cites, " ") + ".\n\nSee [[index]].",
		Tags:    []string{"Deploy", "staging"},
	}, nil
}

func (a *wikiTestAuthor) ModelName() string { return "wiki-test-model" }

func TestWikiPagesLinkCiteAndLintPostgres(t *testing.T) {
	b, ctx, namespaceID := newWorkExecutionTestBrain(t)
	q, err := queries.New()
	if err != nil {
		t.Fatalf("queries: %v", err)
	}
	b.queries = q
	b.embedder = unavailableEmbedder{}
	b.reasoner = &wikiTestAuthor{}
	var slug string
	if err := b.pool.QueryRow(ctx, `SELECT slug FROM namespaces WHERE id = $1`, namespaceID).Scan(&slug); err != nil {
		t.Fatalf("read namespace slug: %v", err)
	}

	episode, err := b.RememberWithStatus(ctx, slug, "FROMM-4414 staging deploy failed because the migration timed out after 30s", nil)
	if err != nil {
		t.Fatalf("remember: %v", err)
	}

	// Create a page that links to a page that does not exist yet and cites
	// the episode inline.
	content := fmt.Sprintf("# Deploys\n\nStaging migrations time out [@episode:%d].\n\nSee [[runbooks/deploy]] and [[Runbooks/Deploy|the runbook]].", episode.ID)
	detail, created, err := b.WriteWikiPage(ctx, namespaceID, WikiPageInput{Slug: "ops/deploys", Title: "Deploys", Content: content, Tags: []string{"Ops", "ops", " deploy "}, Author: "codex"})
	if err != nil || !created {
		t.Fatalf("WriteWikiPage = %+v, created=%v, err=%v", detail, created, err)
	}
	if detail.Page.Revision != 1 || detail.Page.Indexed || strings.Join(detail.Page.Tags, ",") != "ops,deploy" {
		t.Fatalf("page = %+v", detail.Page)
	}
	if len(detail.Links) != 1 || detail.Links[0].TargetSlug != "runbooks/deploy" || detail.Links[0].TargetPageID != nil {
		t.Fatalf("links = %+v, want one unresolved link", detail.Links)
	}
	if len(detail.Sources) != 1 || detail.Sources[0].SourceType != "episode" || detail.Sources[0].Status != WikiSourceOK || !strings.Contains(detail.Sources[0].Excerpt, "FROMM-4414") {
		t.Fatalf("sources = %+v", detail.Sources)
	}

	report, err := b.LintWiki(ctx, namespaceID, "codex", "agent")
	if err != nil {
		t.Fatalf("LintWiki: %v", err)
	}
	codes := map[string]int{}
	for _, finding := range report.Findings {
		codes[finding.Code]++
	}
	if codes["broken_link"] != 1 || codes["orphan_page"] != 1 || codes["missing_index"] != 1 {
		t.Fatalf("lint findings = %+v", report.Findings)
	}

	// Writing the target resolves the earlier link; an index page links it.
	if _, _, err := b.WriteWikiPage(ctx, namespaceID, WikiPageInput{Slug: "runbooks/deploy", Title: "Deploy runbook", Content: "Steps to deploy. Related: [[ops/deploys]].", Author: "codex"}); err != nil {
		t.Fatalf("write runbook: %v", err)
	}
	if _, _, err := b.WriteWikiPage(ctx, namespaceID, WikiPageInput{Slug: "index", Title: "Index", Kind: "index", Content: "- [[ops/deploys]]\n- [[runbooks/deploy]]", Author: "codex"}); err != nil {
		t.Fatalf("write index: %v", err)
	}
	detail, err = b.GetWikiPage(ctx, namespaceID, "ops/deploys")
	if err != nil {
		t.Fatalf("GetWikiPage: %v", err)
	}
	if detail.Links[0].TargetPageID == nil || detail.Links[0].TargetTitle != "Deploy runbook" {
		t.Fatalf("link not resolved after target was written: %+v", detail.Links)
	}
	if len(detail.Backlinks) != 2 {
		t.Fatalf("backlinks = %+v, want runbook and index", detail.Backlinks)
	}
	report, err = b.LintWiki(ctx, namespaceID, "codex", "agent")
	if err != nil {
		t.Fatalf("second LintWiki: %v", err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("lint after fixes = %+v, want clean", report.Findings)
	}

	// Concurrent edits: a stale expected revision is rejected, the current
	// one is accepted and recorded as revision 2.
	if _, _, err := b.WriteWikiPage(ctx, namespaceID, WikiPageInput{Slug: "ops/deploys", Title: "Deploys", Content: content + "\n\nMore.", ExpectedRevision: 5}); !errors.Is(err, ErrWikiRevisionConflict) {
		t.Fatalf("stale revision error = %v", err)
	}
	detail, created, err = b.WriteWikiPage(ctx, namespaceID, WikiPageInput{Slug: "ops/deploys", Title: "Deploys", Content: content + "\n\nMore.", ExpectedRevision: 1, ChangeNote: "add note", Author: "claude"})
	if err != nil || created || detail.Page.Revision != 2 {
		t.Fatalf("update = %+v, created=%v, err=%v", detail.Page, created, err)
	}
	revisions, err := b.WikiPageRevisions(ctx, namespaceID, "ops/deploys", Pagination{})
	if err != nil || len(revisions) != 2 || revisions[0].Revision != 2 || revisions[0].ChangeNote != "add note" {
		t.Fatalf("revisions = %+v, %v", revisions, err)
	}
	first, err := b.GetWikiRevision(ctx, namespaceID, "ops/deploys", 1)
	if err != nil || first.Content != content {
		t.Fatalf("revision 1 = %+v, %v", first, err)
	}

	// Keyword search works without any embedding provider, through recall
	// and through the page-only search.
	hits, err := b.SearchWiki(ctx, []string{slug}, "migration timed out", 5, RecallOptions{})
	if err != nil || len(hits) == 0 || hits[0].Type != "page" || hits[0].Slug != "ops/deploys" {
		t.Fatalf("SearchWiki = %+v, %v", hits, err)
	}
	mixed, err := b.RecallWithOptions(ctx, []string{slug}, "migration timed out", 10, RecallOptions{IncludePages: true})
	if err != nil {
		t.Fatalf("recall with pages: %v", err)
	}
	types := map[string]bool{}
	for _, hit := range mixed {
		types[hit.Type] = true
	}
	if !types["page"] || !types["episode"] {
		t.Fatalf("recall types = %v, want page and episode", types)
	}
	listed, err := b.ListWikiPages(ctx, namespaceID, WikiListFilter{Query: "deploy", Tag: "ops"}, Pagination{})
	if err != nil || len(listed) != 1 || listed[0].Slug != "ops/deploys" || listed[0].Content != "" {
		t.Fatalf("ListWikiPages = %+v, %v", listed, err)
	}

	// Deleting the cited episode makes the page stale on the next lint.
	if err := b.PurgeEpisode(ctx, episode.ID, false); err != nil {
		t.Fatalf("purge episode: %v", err)
	}
	report, err = b.LintWiki(ctx, namespaceID, "codex", "agent")
	if err != nil || report.StaleMarked != 1 {
		t.Fatalf("lint after source deletion = %+v, %v", report, err)
	}
	stale := true
	listed, err = b.ListWikiPages(ctx, namespaceID, WikiListFilter{Stale: &stale}, Pagination{})
	if err != nil || len(listed) != 1 || listed[0].StaleAt == nil {
		t.Fatalf("stale pages = %+v, %v", listed, err)
	}

	// Server-side compile pins the evidence and stores a server-authored page.
	if err := b.RestoreEpisode(ctx, episode.ID); err != nil {
		t.Fatalf("restore episode: %v", err)
	}
	compiled, err := b.CompileWikiPage(ctx, namespaceID, WikiCompileRequest{Slug: "ops/staging-deploys", Topic: "staging deploy failures", SourceRefs: []string{fmt.Sprintf("episode:%d", episode.ID)}, Save: true})
	if err != nil {
		t.Fatalf("CompileWikiPage: %v", err)
	}
	if compiled.Page == nil || compiled.Page.Page.AuthorKind != "server" || compiled.Page.Page.Author != "wiki-test-model" || len(compiled.Page.Sources) != 1 {
		t.Fatalf("compiled page = %+v", compiled.Page)
	}
	if strings.Join(compiled.Page.Page.Tags, ",") != "deploy,staging" {
		t.Fatalf("compiled tags = %v", compiled.Page.Page.Tags)
	}
	author := b.reasoner.(*wikiTestAuthor)
	if len(author.requests) != 1 || len(author.requests[0].Pages) != 3 {
		t.Fatalf("draft request = %+v, want existing pages offered for linking", author.requests)
	}

	// Delete and restore keep the history and the activity log.
	if err := b.DeleteWikiPage(ctx, namespaceID, "ops/deploys", "codex", "agent"); err != nil {
		t.Fatalf("DeleteWikiPage: %v", err)
	}
	if _, err := b.GetWikiPage(ctx, namespaceID, "ops/deploys"); !errors.Is(err, ErrWikiPageNotFound) {
		t.Fatalf("deleted page read error = %v", err)
	}
	if err := b.RestoreWikiPage(ctx, namespaceID, "ops/deploys", "codex", "agent"); err != nil {
		t.Fatalf("RestoreWikiPage: %v", err)
	}
	detail, err = b.GetWikiPage(ctx, namespaceID, "ops/deploys")
	if err != nil || detail.Page.Revision != 2 || len(detail.Backlinks) != 2 {
		t.Fatalf("restored page = %+v, %v", detail, err)
	}
	log, err := b.WikiLog(ctx, namespaceID, Pagination{})
	if err != nil {
		t.Fatalf("WikiLog: %v", err)
	}
	actions := map[string]int{}
	for _, entry := range log {
		actions[entry.Action]++
	}
	if actions["create"] < 4 || actions["update"] != 1 || actions["lint"] != 3 || actions["delete"] != 1 || actions["restore"] != 1 || actions["compile"] != 1 {
		t.Fatalf("log actions = %v", actions)
	}

	// Queued page embeddings are counted with the other tables.
	status, err := b.EmbeddingMaintenanceStatus(ctx)
	if err != nil || status.PagesPending < 4 || status.PagesTotal < 4 {
		t.Fatalf("maintenance status = %+v, %v", status, err)
	}
}

func TestWikiSlugAndSourceValidation(t *testing.T) {
	for _, bad := range []string{"", "/", "Spaces here", "a//b", "-lead", strings.Repeat("a", 65)} {
		if _, err := normalizeWikiSlug(bad); err == nil {
			t.Fatalf("slug %q should be rejected", bad)
		}
	}
	if slug, err := normalizeWikiSlug("/Ops/Deploys/"); err != nil || slug != "ops/deploys" {
		t.Fatalf("normalize = %q, %v", slug, err)
	}
	if links := parseWikiLinks("see [[a/b|label]], [[A/B]], [[bad slug]] and [[c]]"); strings.Join(links, " ") != "a/b c" {
		t.Fatalf("links = %v", links)
	}
	cites := parseWikiCitations("x [@fact:1] y [@work:W-000001]. [@fact:1] [@url:https://example.com/a].")
	if len(cites) != 3 || cites[1].Ref != "W-000001" || cites[2].Ref != "https://example.com/a" {
		t.Fatalf("citations = %+v", cites)
	}
	if _, err := ParseWikiSourceRef("nope:1"); !errors.Is(err, ErrWikiInvalidSource) {
		t.Fatalf("invalid source ref error = %v", err)
	}
}
