package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alash3al/stash/internal/models"
	"github.com/jackc/pgx/v5"
)

var (
	ErrWikiPageNotFound     = errors.New("brain: wiki page not found")
	ErrWikiSlugInvalid      = errors.New("brain: wiki slug must be lowercase segments of letters, numbers, hyphens, or underscores separated by /")
	ErrWikiTitleRequired    = errors.New("brain: wiki title is required")
	ErrWikiContentTooLong   = errors.New("brain: wiki content exceeds the maximum length")
	ErrWikiRevisionConflict = errors.New("brain: wiki page changed since it was read; re-read it and retry with the current revision")
	ErrWikiInvalidKind      = errors.New("brain: wiki kind must be article, index, entity, decision, or log")
	ErrWikiInvalidSource    = errors.New("brain: wiki source must look like fact:12, episode:3, work:W-000001, page:slug, or url:https://example.com/doc")

	wikiSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}(/[a-z0-9][a-z0-9_-]{0,63}){0,7}$`)
	// [[slug]] or [[slug|label]]
	wikiLinkRe = regexp.MustCompile(`\[\[([^\[\]|]+)(?:\|[^\[\]]*)?\]\]`)
	// [@fact:12], [@work:W-000001], [@page:api/ports], [@url:https://...]
	wikiCiteRe = regexp.MustCompile(`\[@(episode|fact|hypothesis|failure|goal|work|page|url):([^\]\s]+)\]`)
)

const (
	maxWikiContentLen = 200_000
	maxWikiTags       = 20
	maxWikiTagLen     = 40
	wikiExcerptRunes  = 240

	WikiSourceOK         = "ok"
	WikiSourceMissing    = "missing"
	WikiSourceChanged    = "changed"
	WikiSourceSuperseded = "superseded"
	WikiSourceDeleted    = "deleted"
)

var wikiKinds = map[string]bool{"article": true, "index": true, "entity": true, "decision": true, "log": true}
var wikiAuthorKinds = map[string]bool{"agent": true, "server": true, "human": true}

// WikiSourceInput names evidence attached to a page in addition to the
// citations parsed from its content.
type WikiSourceInput struct {
	Type string `json:"type"`
	Ref  string `json:"ref"`
	Note string `json:"note,omitempty"`
}

// WikiPageInput is a create-or-update request for one slug.
type WikiPageInput struct {
	Slug       string
	Title      string
	Kind       string
	Summary    string
	Content    string
	Tags       []string
	Author     string
	AuthorKind string
	Sources    []WikiSourceInput
	ChangeNote string
	// ExpectedRevision guards concurrent edits: when it is set and the stored
	// revision differs, the write is rejected instead of silently overwriting
	// another author's text.
	ExpectedRevision int
}

// WikiListFilter narrows ListWikiPages.
type WikiListFilter struct {
	Query string
	Kind  string
	Tag   string
	Stale *bool
}

// ParseWikiSourceRef splits "fact:12" into a type and reference.
func ParseWikiSourceRef(raw string) (WikiSourceInput, error) {
	raw = strings.TrimSpace(raw)
	kind, ref, ok := strings.Cut(raw, ":")
	kind = strings.ToLower(strings.TrimSpace(kind))
	ref = strings.TrimSpace(ref)
	switch kind {
	case "episode", "fact", "hypothesis", "failure", "goal", "work", "page", "url":
	default:
		ok = false
	}
	if !ok || ref == "" {
		return WikiSourceInput{}, fmt.Errorf("%w: %q", ErrWikiInvalidSource, raw)
	}
	return WikiSourceInput{Type: kind, Ref: ref}, nil
}

func normalizeWikiSlug(slug string) (string, error) {
	slug = strings.Trim(strings.ToLower(strings.TrimSpace(slug)), "/")
	if !wikiSlugRe.MatchString(slug) {
		return "", ErrWikiSlugInvalid
	}
	return slug, nil
}

func normalizeWikiTags(tags []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" || seen[tag] || len(out) >= maxWikiTags {
			continue
		}
		if runes := []rune(tag); len(runes) > maxWikiTagLen {
			tag = string(runes[:maxWikiTagLen])
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out
}

// wikiEmbeddingText is the text indexed for a page; the retry worker uses the
// same shape (see embeddingTextExpr) so a queued page embeds identically.
func wikiEmbeddingText(title, content string) string {
	return title + "\n\n" + content
}

func wikiExcerpt(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= wikiExcerptRunes {
		return string(runes)
	}
	return string(runes[:wikiExcerptRunes]) + "…"
}

func wikiDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:16])
}

// parseWikiLinks returns the distinct valid [[slug]] targets in order.
func parseWikiLinks(content string) []string {
	seen := map[string]bool{}
	var out []string
	for _, match := range wikiLinkRe.FindAllStringSubmatch(content, -1) {
		slug, err := normalizeWikiSlug(match[1])
		if err != nil || seen[slug] {
			continue
		}
		seen[slug] = true
		out = append(out, slug)
	}
	return out
}

// parseWikiCitations returns the distinct [@type:ref] citations in order.
func parseWikiCitations(content string) []WikiSourceInput {
	seen := map[string]bool{}
	var out []WikiSourceInput
	for _, match := range wikiCiteRe.FindAllStringSubmatch(content, -1) {
		key := match[1] + ":" + match[2]
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, WikiSourceInput{Type: match[1], Ref: strings.TrimSuffix(match[2], ".")})
	}
	return out
}

const wikiPageColumns = `id, namespace_id, slug, title, kind, summary, content, tags, revision, author, author_kind, embedding IS NOT NULL, stale_at, created_at, updated_at, deleted_at`

func scanWikiPage(row pgx.Row) (models.WikiPage, error) {
	var p models.WikiPage
	err := row.Scan(&p.ID, &p.NamespaceID, &p.Slug, &p.Title, &p.Kind, &p.Summary, &p.Content, &p.Tags, &p.Revision, &p.Author, &p.AuthorKind, &p.Indexed, &p.StaleAt, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrWikiPageNotFound
	}
	if p.Tags == nil {
		p.Tags = []string{}
	}
	return p, err
}

// WriteWikiPage creates or updates the page at in.Slug and returns the
// stored page with its links and sources. created reports which happened.
func (b *Brain) WriteWikiPage(ctx context.Context, namespaceID int64, in WikiPageInput) (*models.WikiPageDetail, bool, error) {
	slug, err := normalizeWikiSlug(in.Slug)
	if err != nil {
		return nil, false, err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, false, ErrWikiTitleRequired
	}
	content := strings.TrimSpace(in.Content)
	if content == "" {
		return nil, false, ErrEmptyContent
	}
	if len(content) > maxWikiContentLen {
		return nil, false, ErrWikiContentTooLong
	}
	kind := strings.TrimSpace(in.Kind)
	if kind != "" && !wikiKinds[kind] {
		return nil, false, ErrWikiInvalidKind
	}
	authorKind := strings.TrimSpace(in.AuthorKind)
	if authorKind == "" {
		authorKind = "agent"
	}
	if !wikiAuthorKinds[authorKind] {
		authorKind = "agent"
	}
	tags := normalizeWikiTags(in.Tags)
	summary := strings.TrimSpace(in.Summary)
	sources := append(parseWikiCitations(content), in.Sources...)
	for i := range sources {
		parsed, err := ParseWikiSourceRef(sources[i].Type + ":" + sources[i].Ref)
		if err != nil {
			return nil, false, err
		}
		sources[i].Type, sources[i].Ref = parsed.Type, parsed.Ref
	}

	// Embed before the transaction so a slow provider does not hold row locks.
	vec, embedErr := b.embedder.Embed(ctx, wikiEmbeddingText(title, content))
	write := b.embeddingWrite(vec, embedErr)

	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin wiki write: %w", err)
	}
	defer tx.Rollback(ctx)

	existing, err := scanWikiPage(tx.QueryRow(ctx,
		`SELECT `+wikiPageColumns+` FROM wiki_pages WHERE namespace_id = $1 AND slug = $2 AND deleted_at IS NULL FOR UPDATE`,
		namespaceID, slug))
	created := errors.Is(err, ErrWikiPageNotFound)
	if err != nil && !created {
		return nil, false, fmt.Errorf("lock wiki page: %w", err)
	}
	// On an update, fields the caller left unspecified keep their stored
	// value; an agent rewriting the body should not have to resend metadata.
	if created {
		if kind == "" {
			kind = "article"
		}
	} else {
		if kind == "" {
			kind = existing.Kind
		}
		if in.Tags == nil {
			tags = existing.Tags
		}
		if summary == "" {
			summary = existing.Summary
		}
	}

	var page models.WikiPage
	if created {
		page, err = scanWikiPage(tx.QueryRow(ctx,
			`INSERT INTO wiki_pages (namespace_id, slug, title, kind, summary, content, tags, revision, author, author_kind,
			                         embedding, embedding_model, embedding_attempts, embedding_last_error, embedding_retry_at, embedding_updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, 1, $8, $9, $10, $11, $12, $13, $14, now())
			 RETURNING `+wikiPageColumns,
			namespaceID, slug, title, kind, summary, content, tags, in.Author, authorKind,
			write.vector, write.model, write.attempts, write.lastError, write.retryAt,
		))
		if err != nil {
			return nil, false, fmt.Errorf("insert wiki page: %w", err)
		}
	} else {
		if in.ExpectedRevision > 0 && existing.Revision != in.ExpectedRevision {
			return nil, false, fmt.Errorf("%w (stored revision %d, expected %d)", ErrWikiRevisionConflict, existing.Revision, in.ExpectedRevision)
		}
		page, err = scanWikiPage(tx.QueryRow(ctx,
			`UPDATE wiki_pages
			 SET title = $2, kind = $3, summary = $4, content = $5, tags = $6, revision = revision + 1, author = $7, author_kind = $8,
			     embedding = $9, embedding_model = $10, embedding_attempts = $11, embedding_last_error = $12, embedding_retry_at = $13,
			     embedding_lease_until = NULL, embedding_updated_at = now(), stale_at = NULL, updated_at = now()
			 WHERE id = $1 RETURNING `+wikiPageColumns,
			existing.ID, title, kind, summary, content, tags, in.Author, authorKind,
			write.vector, write.model, write.attempts, write.lastError, write.retryAt,
		))
		if err != nil {
			return nil, false, fmt.Errorf("update wiki page: %w", err)
		}
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO wiki_revisions (page_id, revision, title, summary, content, tags, author, author_kind, change_note)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		page.ID, page.Revision, title, summary, content, tags, in.Author, authorKind, strings.TrimSpace(in.ChangeNote),
	); err != nil {
		return nil, false, fmt.Errorf("record wiki revision: %w", err)
	}

	if err := b.replaceWikiLinks(ctx, tx, page, parseWikiLinks(content)); err != nil {
		return nil, false, err
	}
	if err := b.replaceWikiSources(ctx, tx, page, sources); err != nil {
		return nil, false, err
	}

	action, summaryText := "update", fmt.Sprintf("Updated %q (revision %d)", title, page.Revision)
	if created {
		action, summaryText = "create", fmt.Sprintf("Created %q", title)
	}
	if note := strings.TrimSpace(in.ChangeNote); note != "" {
		summaryText += ": " + note
	}
	if err := insertWikiLog(ctx, tx, namespaceID, &page.ID, slug, action, in.Author, authorKind, summaryText, map[string]any{"revision": page.Revision}); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit wiki write: %w", err)
	}
	if !page.Indexed {
		b.WakeEmbeddingRetries()
	}
	detail, err := b.GetWikiPage(ctx, namespaceID, slug)
	return detail, created, err
}

func (b *Brain) replaceWikiLinks(ctx context.Context, tx pgx.Tx, page models.WikiPage, targets []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM wiki_links WHERE page_id = $1`, page.ID); err != nil {
		return fmt.Errorf("clear wiki links: %w", err)
	}
	for _, target := range targets {
		if _, err := tx.Exec(ctx,
			`INSERT INTO wiki_links (page_id, target_slug, target_page_id)
			 SELECT $1, $2, (SELECT id FROM wiki_pages WHERE namespace_id = $3 AND slug = $2 AND deleted_at IS NULL)`,
			page.ID, target, page.NamespaceID,
		); err != nil {
			return fmt.Errorf("store wiki link %q: %w", target, err)
		}
	}
	// Links written before this page existed now have a target.
	if _, err := tx.Exec(ctx,
		`UPDATE wiki_links SET target_page_id = $1
		 WHERE target_slug = $2 AND target_page_id IS NULL
		   AND page_id IN (SELECT id FROM wiki_pages WHERE namespace_id = $3)`,
		page.ID, page.Slug, page.NamespaceID,
	); err != nil {
		return fmt.Errorf("resolve inbound wiki links: %w", err)
	}
	return nil
}

func (b *Brain) replaceWikiSources(ctx context.Context, tx pgx.Tx, page models.WikiPage, sources []WikiSourceInput) error {
	if _, err := tx.Exec(ctx, `DELETE FROM wiki_sources WHERE page_id = $1`, page.ID); err != nil {
		return fmt.Errorf("clear wiki sources: %w", err)
	}
	for _, source := range sources {
		resolved := b.resolveWikiSource(ctx, tx, page.NamespaceID, source.Type, source.Ref)
		if _, err := tx.Exec(ctx,
			`INSERT INTO wiki_sources (page_id, source_type, source_ref, source_id, note, source_digest)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (page_id, source_type, source_ref) DO UPDATE SET note = EXCLUDED.note`,
			page.ID, source.Type, source.Ref, resolved.SourceID, strings.TrimSpace(source.Note), resolved.SourceDigest,
		); err != nil {
			return fmt.Errorf("store wiki source %s:%s: %w", source.Type, source.Ref, err)
		}
	}
	return nil
}

type wikiQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// resolveWikiSource looks the cited record up inside the page's namespace
// tree and returns its current digest and excerpt. Anything it cannot see
// is reported as missing rather than guessed at.
func (b *Brain) resolveWikiSource(ctx context.Context, q wikiQuerier, namespaceID int64, sourceType, ref string) models.WikiSource {
	out := models.WikiSource{SourceType: sourceType, SourceRef: ref, Status: WikiSourceMissing}
	var (
		text      string
		deletedAt *time.Time
		validTo   *time.Time
		id        int64
		err       error
	)
	numeric := func() (int64, bool) {
		n, convErr := strconv.ParseInt(ref, 10, 64)
		return n, convErr == nil && n > 0
	}
	switch sourceType {
	case "episode", "fact", "hypothesis", "failure", "goal":
		n, ok := numeric()
		if !ok {
			return out
		}
		column := "content"
		extra := "NULL::timestamptz"
		if sourceType == "fact" {
			extra = "valid_until"
		}
		table := sourceType + "s"
		if sourceType == "hypothesis" {
			table = "hypotheses"
		}
		err = q.QueryRow(ctx, fmt.Sprintf(
			`SELECT id, %s, deleted_at, %s FROM %s
			 WHERE id = $1 AND namespace_id IN (SELECT id FROM namespaces WHERE id = $2 OR slug LIKE (SELECT slug FROM namespaces WHERE id = $2) || '/%%')`,
			column, extra, table), n, namespaceID).Scan(&id, &text, &deletedAt, &validTo)
	case "work":
		if n, ok := numeric(); ok {
			err = q.QueryRow(ctx,
				`SELECT id, title || E'\n' || description, deleted_at, NULL::timestamptz FROM work_items WHERE id = $1 AND namespace_id = $2`,
				n, namespaceID).Scan(&id, &text, &deletedAt, &validTo)
		} else {
			err = q.QueryRow(ctx,
				`SELECT id, title || E'\n' || description, deleted_at, NULL::timestamptz FROM work_items WHERE issue_key = $1 AND namespace_id = $2`,
				strings.ToUpper(ref), namespaceID).Scan(&id, &text, &deletedAt, &validTo)
		}
	case "page":
		slug, slugErr := normalizeWikiSlug(ref)
		if slugErr != nil {
			return out
		}
		err = q.QueryRow(ctx,
			`SELECT id, title || E'\n' || content, deleted_at, NULL::timestamptz FROM wiki_pages WHERE namespace_id = $1 AND slug = $2 ORDER BY deleted_at NULLS FIRST LIMIT 1`,
			namespaceID, slug).Scan(&id, &text, &deletedAt, &validTo)
	case "url":
		// External links are recorded, never fetched.
		out.Status = WikiSourceOK
		out.Excerpt = ref
		return out
	default:
		return out
	}
	if err != nil {
		return out
	}
	out.SourceID = &id
	out.SourceDigest = wikiDigest(text)
	out.Excerpt = wikiExcerpt(text)
	switch {
	case deletedAt != nil:
		out.Status = WikiSourceDeleted
	case validTo != nil:
		out.Status = WikiSourceSuperseded
	default:
		out.Status = WikiSourceOK
	}
	return out
}

func insertWikiLog(ctx context.Context, tx pgx.Tx, namespaceID int64, pageID *int64, slug, action, actor, actorKind, summary string, details map[string]any) error {
	if details == nil {
		details = map[string]any{}
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode wiki log details: %w", err)
	}
	if actorKind == "" {
		actorKind = "agent"
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO wiki_log (namespace_id, page_id, page_slug, action, actor, actor_kind, summary, details)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)`,
		namespaceID, pageID, slug, action, strings.TrimSpace(actor), actorKind, summary, encoded,
	); err != nil {
		return fmt.Errorf("record wiki log: %w", err)
	}
	return nil
}

// GetWikiPage reads a live page with its links, backlinks, and resolved sources.
func (b *Brain) GetWikiPage(ctx context.Context, namespaceID int64, slug string) (*models.WikiPageDetail, error) {
	slug, err := normalizeWikiSlug(slug)
	if err != nil {
		return nil, err
	}
	page, err := scanWikiPage(b.pool.QueryRow(ctx,
		`SELECT `+wikiPageColumns+` FROM wiki_pages WHERE namespace_id = $1 AND slug = $2 AND deleted_at IS NULL`,
		namespaceID, slug))
	if err != nil {
		return nil, err
	}
	return b.wikiPageDetail(ctx, page)
}

// GetWikiPageByID reads a live page regardless of slug.
func (b *Brain) GetWikiPageByID(ctx context.Context, id int64) (*models.WikiPage, error) {
	page, err := scanWikiPage(b.pool.QueryRow(ctx, `SELECT `+wikiPageColumns+` FROM wiki_pages WHERE id = $1 AND deleted_at IS NULL`, id))
	if err != nil {
		return nil, err
	}
	return &page, nil
}

func (b *Brain) wikiPageDetail(ctx context.Context, page models.WikiPage) (*models.WikiPageDetail, error) {
	detail := &models.WikiPageDetail{Page: page, Links: []models.WikiLink{}, Backlinks: []models.WikiLink{}, Sources: []models.WikiSource{}}

	rows, err := b.pool.Query(ctx,
		`SELECT l.page_id, l.target_slug, l.target_page_id, COALESCE(t.title, '')
		 FROM wiki_links l LEFT JOIN wiki_pages t ON t.id = l.target_page_id AND t.deleted_at IS NULL
		 WHERE l.page_id = $1 ORDER BY l.target_slug`, page.ID)
	if err != nil {
		return nil, fmt.Errorf("read wiki links: %w", err)
	}
	for rows.Next() {
		var link models.WikiLink
		if err := rows.Scan(&link.PageID, &link.TargetSlug, &link.TargetPageID, &link.TargetTitle); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan wiki link: %w", err)
		}
		detail.Links = append(detail.Links, link)
	}
	rows.Close()

	rows, err = b.pool.Query(ctx,
		`SELECT l.page_id, s.slug, l.target_page_id, s.title
		 FROM wiki_links l JOIN wiki_pages s ON s.id = l.page_id AND s.deleted_at IS NULL
		 WHERE l.target_page_id = $1 ORDER BY s.slug`, page.ID)
	if err != nil {
		return nil, fmt.Errorf("read wiki backlinks: %w", err)
	}
	for rows.Next() {
		var link models.WikiLink
		if err := rows.Scan(&link.PageID, &link.TargetSlug, &link.TargetPageID, &link.TargetTitle); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan wiki backlink: %w", err)
		}
		detail.Backlinks = append(detail.Backlinks, link)
	}
	rows.Close()

	rows, err = b.pool.Query(ctx,
		`SELECT page_id, source_type, source_ref, source_id, note, source_digest, created_at
		 FROM wiki_sources WHERE page_id = $1 ORDER BY source_type, source_ref`, page.ID)
	if err != nil {
		return nil, fmt.Errorf("read wiki sources: %w", err)
	}
	var stored []models.WikiSource
	for rows.Next() {
		var source models.WikiSource
		if err := rows.Scan(&source.PageID, &source.SourceType, &source.SourceRef, &source.SourceID, &source.Note, &source.SourceDigest, &source.CreatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan wiki source: %w", err)
		}
		stored = append(stored, source)
	}
	rows.Close()
	for _, source := range stored {
		current := b.resolveWikiSource(ctx, b.pool, page.NamespaceID, source.SourceType, source.SourceRef)
		source.Status, source.Excerpt = current.Status, current.Excerpt
		if source.Status == WikiSourceOK && source.SourceDigest != "" && current.SourceDigest != source.SourceDigest {
			source.Status = WikiSourceChanged
		}
		detail.Sources = append(detail.Sources, source)
	}
	return detail, nil
}

// ListWikiPages returns page metadata (no content) for a namespace.
func (b *Brain) ListWikiPages(ctx context.Context, namespaceID int64, filter WikiListFilter, page Pagination) ([]models.WikiPage, error) {
	page = b.sanitizePage(page)
	if filter.Kind != "" && !wikiKinds[filter.Kind] {
		return nil, ErrWikiInvalidKind
	}
	args := []any{namespaceID, page.Limit, page.Offset}
	where := []string{"namespace_id = $1", "deleted_at IS NULL"}
	order := "updated_at DESC, id DESC"
	if filter.Kind != "" {
		args = append(args, filter.Kind)
		where = append(where, fmt.Sprintf("kind = $%d", len(args)))
	}
	if tag := strings.ToLower(strings.TrimSpace(filter.Tag)); tag != "" {
		args = append(args, tag)
		where = append(where, fmt.Sprintf("$%d = ANY(tags)", len(args)))
	}
	if filter.Stale != nil {
		if *filter.Stale {
			where = append(where, "stale_at IS NOT NULL")
		} else {
			where = append(where, "stale_at IS NULL")
		}
	}
	for _, token := range searchTextTokens(filter.Query) {
		args = append(args, "%"+escapeLikePattern(token)+"%")
		where = append(where, fmt.Sprintf(`(search_text ILIKE $%d ESCAPE '\' OR slug ILIKE $%d ESCAPE '\')`, len(args), len(args)))
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		args = append(args, query)
		order = fmt.Sprintf("word_similarity($%d, search_text) DESC, updated_at DESC", len(args))
	}
	rows, err := b.pool.Query(ctx,
		`SELECT `+wikiPageColumns+` FROM wiki_pages WHERE `+strings.Join(where, " AND ")+` ORDER BY `+order+` LIMIT $2 OFFSET $3`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("list wiki pages: %w", err)
	}
	defer rows.Close()
	pages := []models.WikiPage{}
	for rows.Next() {
		p, err := scanWikiPage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan wiki page: %w", err)
		}
		p.Content = ""
		pages = append(pages, p)
	}
	return pages, rows.Err()
}

// DeleteWikiPage hides a page; its revisions and log stay for restore.
func (b *Brain) DeleteWikiPage(ctx context.Context, namespaceID int64, slug, actor, actorKind string) error {
	return b.setWikiPageDeleted(ctx, namespaceID, slug, actor, actorKind, true)
}

// RestoreWikiPage undoes DeleteWikiPage unless the slug was reused.
func (b *Brain) RestoreWikiPage(ctx context.Context, namespaceID int64, slug, actor, actorKind string) error {
	return b.setWikiPageDeleted(ctx, namespaceID, slug, actor, actorKind, false)
}

func (b *Brain) setWikiPageDeleted(ctx context.Context, namespaceID int64, slug, actor, actorKind string, deleted bool) error {
	slug, err := normalizeWikiSlug(slug)
	if err != nil {
		return err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin wiki delete: %w", err)
	}
	defer tx.Rollback(ctx)
	var id int64
	var title string
	if deleted {
		err = tx.QueryRow(ctx,
			`UPDATE wiki_pages SET deleted_at = now(), updated_at = now() WHERE namespace_id = $1 AND slug = $2 AND deleted_at IS NULL RETURNING id, title`,
			namespaceID, slug).Scan(&id, &title)
	} else {
		err = tx.QueryRow(ctx,
			`UPDATE wiki_pages SET deleted_at = NULL, updated_at = now()
			 WHERE id = (SELECT id FROM wiki_pages WHERE namespace_id = $1 AND slug = $2 AND deleted_at IS NOT NULL ORDER BY deleted_at DESC LIMIT 1)
			   AND NOT EXISTS (SELECT 1 FROM wiki_pages WHERE namespace_id = $1 AND slug = $2 AND deleted_at IS NULL)
			 RETURNING id, title`,
			namespaceID, slug).Scan(&id, &title)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrWikiPageNotFound
	}
	if err != nil {
		return fmt.Errorf("update wiki page state: %w", err)
	}
	action, summary := "delete", fmt.Sprintf("Deleted %q", title)
	if !deleted {
		action, summary = "restore", fmt.Sprintf("Restored %q", title)
		// Inbound links pointed at this page before; outbound ones never left.
		if _, err := tx.Exec(ctx,
			`UPDATE wiki_links SET target_page_id = $1 WHERE target_slug = $2 AND target_page_id IS NULL AND page_id IN (SELECT id FROM wiki_pages WHERE namespace_id = $3)`,
			id, slug, namespaceID); err != nil {
			return fmt.Errorf("resolve inbound wiki links: %w", err)
		}
	} else if _, err := tx.Exec(ctx, `UPDATE wiki_links SET target_page_id = NULL WHERE target_page_id = $1`, id); err != nil {
		return fmt.Errorf("detach inbound wiki links: %w", err)
	}
	if err := insertWikiLog(ctx, tx, namespaceID, &id, slug, action, actor, actorKind, summary, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// WikiPageRevisions lists a page's history newest first, without content.
func (b *Brain) WikiPageRevisions(ctx context.Context, namespaceID int64, slug string, page Pagination) ([]models.WikiRevision, error) {
	slug, err := normalizeWikiSlug(slug)
	if err != nil {
		return nil, err
	}
	page = b.sanitizePage(page)
	rows, err := b.pool.Query(ctx,
		`SELECT r.id, r.page_id, r.revision, r.title, r.summary, r.tags, r.author, r.author_kind, r.change_note, r.created_at
		 FROM wiki_revisions r JOIN wiki_pages p ON p.id = r.page_id
		 WHERE p.namespace_id = $1 AND p.slug = $2 AND p.deleted_at IS NULL
		 ORDER BY r.revision DESC LIMIT $3 OFFSET $4`,
		namespaceID, slug, page.Limit, page.Offset)
	if err != nil {
		return nil, fmt.Errorf("list wiki revisions: %w", err)
	}
	defer rows.Close()
	revisions := []models.WikiRevision{}
	for rows.Next() {
		var r models.WikiRevision
		if err := rows.Scan(&r.ID, &r.PageID, &r.Revision, &r.Title, &r.Summary, &r.Tags, &r.Author, &r.AuthorKind, &r.ChangeNote, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan wiki revision: %w", err)
		}
		if r.Tags == nil {
			r.Tags = []string{}
		}
		revisions = append(revisions, r)
	}
	return revisions, rows.Err()
}

// GetWikiRevision returns one historical revision with its content.
func (b *Brain) GetWikiRevision(ctx context.Context, namespaceID int64, slug string, revision int) (*models.WikiRevision, error) {
	slug, err := normalizeWikiSlug(slug)
	if err != nil {
		return nil, err
	}
	var r models.WikiRevision
	err = b.pool.QueryRow(ctx,
		`SELECT r.id, r.page_id, r.revision, r.title, r.summary, r.content, r.tags, r.author, r.author_kind, r.change_note, r.created_at
		 FROM wiki_revisions r JOIN wiki_pages p ON p.id = r.page_id
		 WHERE p.namespace_id = $1 AND p.slug = $2 AND p.deleted_at IS NULL AND r.revision = $3`,
		namespaceID, slug, revision).Scan(&r.ID, &r.PageID, &r.Revision, &r.Title, &r.Summary, &r.Content, &r.Tags, &r.Author, &r.AuthorKind, &r.ChangeNote, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWikiPageNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read wiki revision: %w", err)
	}
	if r.Tags == nil {
		r.Tags = []string{}
	}
	return &r, nil
}

// WikiLog lists wiki activity for a namespace, newest first.
func (b *Brain) WikiLog(ctx context.Context, namespaceID int64, page Pagination) ([]models.WikiLogEntry, error) {
	page = b.sanitizePage(page)
	rows, err := b.pool.Query(ctx,
		`SELECT id, namespace_id, page_id, page_slug, action, actor, actor_kind, summary, details, created_at
		 FROM wiki_log WHERE namespace_id = $1 ORDER BY id DESC LIMIT $2 OFFSET $3`,
		namespaceID, page.Limit, page.Offset)
	if err != nil {
		return nil, fmt.Errorf("list wiki log: %w", err)
	}
	defer rows.Close()
	entries := []models.WikiLogEntry{}
	for rows.Next() {
		var e models.WikiLogEntry
		if err := rows.Scan(&e.ID, &e.NamespaceID, &e.PageID, &e.PageSlug, &e.Action, &e.Actor, &e.ActorKind, &e.Summary, &e.Details, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan wiki log: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// WikiPageRefs lists slug/title pairs so an author can link existing pages.
func (b *Brain) WikiPageRefs(ctx context.Context, namespaceID int64, limit int) ([]models.WikiPage, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := b.pool.Query(ctx,
		`SELECT id, slug, title, kind, summary FROM wiki_pages WHERE namespace_id = $1 AND deleted_at IS NULL ORDER BY slug LIMIT $2`,
		namespaceID, limit)
	if err != nil {
		return nil, fmt.Errorf("list wiki page refs: %w", err)
	}
	defer rows.Close()
	refs := []models.WikiPage{}
	for rows.Next() {
		var p models.WikiPage
		if err := rows.Scan(&p.ID, &p.Slug, &p.Title, &p.Kind, &p.Summary); err != nil {
			return nil, fmt.Errorf("scan wiki page ref: %w", err)
		}
		refs = append(refs, p)
	}
	return refs, rows.Err()
}

// LintWiki checks a namespace's wiki for broken links, orphans, stale or
// missing evidence, and a missing index page. It updates stale_at on pages
// whose evidence changed so readers and agents can see which pages to revisit.
func (b *Brain) LintWiki(ctx context.Context, namespaceID int64, actor, actorKind string) (*models.WikiLintReport, error) {
	report := &models.WikiLintReport{NamespaceID: namespaceID, Findings: []models.WikiLintFinding{}, CheckedAt: time.Now().UTC()}

	rows, err := b.pool.Query(ctx,
		`SELECT `+wikiPageColumns+` FROM wiki_pages WHERE namespace_id = $1 AND deleted_at IS NULL ORDER BY slug`, namespaceID)
	if err != nil {
		return nil, fmt.Errorf("list wiki pages for lint: %w", err)
	}
	var pages []models.WikiPage
	for rows.Next() {
		p, err := scanWikiPage(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan wiki page for lint: %w", err)
		}
		pages = append(pages, p)
	}
	rows.Close()
	report.Pages = len(pages)
	if len(pages) == 0 {
		return report, nil
	}

	// Resolve links that were broken when written but have a target now.
	if _, err := b.pool.Exec(ctx,
		`UPDATE wiki_links l SET target_page_id = t.id
		 FROM wiki_pages t
		 WHERE l.target_page_id IS NULL AND t.namespace_id = $1 AND t.slug = l.target_slug AND t.deleted_at IS NULL
		   AND l.page_id IN (SELECT id FROM wiki_pages WHERE namespace_id = $1)`, namespaceID); err != nil {
		return nil, fmt.Errorf("resolve wiki links: %w", err)
	}

	hasIndex := false
	linked := map[int64]bool{}
	for _, page := range pages {
		if page.Kind == "index" {
			hasIndex = true
		}
		// An index is usually a bare list of links, so it is exempt.
		if page.Kind != "index" && len(strings.TrimSpace(page.Content)) < 40 {
			report.Findings = append(report.Findings, models.WikiLintFinding{Code: "thin_page", Severity: "info", PageID: page.ID, PageSlug: page.Slug, Message: "page has almost no content"})
		}
		detail, err := b.wikiPageDetail(ctx, page)
		if err != nil {
			return nil, err
		}
		for _, link := range detail.Links {
			if link.TargetPageID == nil {
				report.Findings = append(report.Findings, models.WikiLintFinding{Code: "broken_link", Severity: "warning", PageID: page.ID, PageSlug: page.Slug, Target: link.TargetSlug, Message: "links to a page that does not exist"})
			} else {
				linked[*link.TargetPageID] = true
			}
		}
		stale := false
		for _, source := range detail.Sources {
			switch source.Status {
			case WikiSourceOK:
				continue
			case WikiSourceMissing:
				report.Findings = append(report.Findings, models.WikiLintFinding{Code: "missing_source", Severity: "warning", PageID: page.ID, PageSlug: page.Slug, Target: source.SourceType + ":" + source.SourceRef, Message: "cites a record that cannot be found in this namespace"})
			default:
				stale = true
				report.Findings = append(report.Findings, models.WikiLintFinding{Code: "stale_source", Severity: "warning", PageID: page.ID, PageSlug: page.Slug, Target: source.SourceType + ":" + source.SourceRef, Message: "cited evidence was " + source.Status})
			}
		}
		if stale && page.StaleAt == nil {
			if _, err := b.pool.Exec(ctx, `UPDATE wiki_pages SET stale_at = now() WHERE id = $1`, page.ID); err != nil {
				return nil, fmt.Errorf("mark wiki page stale: %w", err)
			}
			report.StaleMarked++
		} else if !stale && page.StaleAt != nil {
			if _, err := b.pool.Exec(ctx, `UPDATE wiki_pages SET stale_at = NULL WHERE id = $1`, page.ID); err != nil {
				return nil, fmt.Errorf("clear wiki page stale: %w", err)
			}
			report.StaleClear++
		}
	}
	for _, page := range pages {
		if page.Kind != "index" && !linked[page.ID] {
			report.Findings = append(report.Findings, models.WikiLintFinding{Code: "orphan_page", Severity: "info", PageID: page.ID, PageSlug: page.Slug, Message: "no other page links here; add it to an index or a related page"})
		}
	}
	if !hasIndex {
		report.Findings = append(report.Findings, models.WikiLintFinding{Code: "missing_index", Severity: "info", Message: "no index page; create one with kind=index that links the main pages"})
	}
	sort.SliceStable(report.Findings, func(i, j int) bool {
		if report.Findings[i].Severity != report.Findings[j].Severity {
			return report.Findings[i].Severity == "warning"
		}
		return report.Findings[i].PageSlug < report.Findings[j].PageSlug
	})

	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin wiki lint log: %w", err)
	}
	defer tx.Rollback(ctx)
	summary := fmt.Sprintf("Lint checked %d pages: %d findings", report.Pages, len(report.Findings))
	if err := insertWikiLog(ctx, tx, namespaceID, nil, "", "lint", actor, actorKind, summary, map[string]any{"findings": len(report.Findings), "stale_marked": report.StaleMarked}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit wiki lint log: %w", err)
	}
	return report, nil
}

// SearchWiki finds pages by meaning and keyword across namespace trees.
func (b *Brain) SearchWiki(ctx context.Context, namespaces []string, query string, limit int, opts RecallOptions) ([]RecallResult, error) {
	opts.IncludePages = true
	opts.PagesOnly = true
	return b.RecallWithOptions(ctx, namespaces, query, limit, opts)
}
