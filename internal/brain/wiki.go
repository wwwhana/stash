package brain

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/alash3al/stash/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	MaxWikiSlugBytes     = 128
	MaxWikiTitleBytes    = 256
	MaxWikiMarkdownBytes = 256 * 1024
	MaxWikiSources       = 100
	MaxWikiRelatedPages  = 32
)

var (
	ErrWikiPageNotFound = errors.New("brain: wiki page revision not found")
	ErrWikiConflict     = errors.New("brain: wiki revision conflict; read the latest revision before editing")
	ErrWikiDeleted      = errors.New("brain: wiki page is already deleted")
	ErrWikiSource       = errors.New("brain: wiki sources must be active episodes in the same namespace")
	ErrWikiRelatedPage  = errors.New("brain: related wiki pages must have active current revisions in the same namespace")
)

type WikiRevisionConflict struct {
	ExpectedRevision int64 `json:"expected_revision"`
	CurrentRevision  int64 `json:"current_revision"`
}

func (e *WikiRevisionConflict) Error() string {
	return fmt.Sprintf("%s (expected %d, current %d)", ErrWikiConflict, e.ExpectedRevision, e.CurrentRevision)
}

func (e *WikiRevisionConflict) Unwrap() error { return ErrWikiConflict }

// Pointer/slice presence distinguishes an omitted tombstone field from an
// explicitly supplied empty value. A save always supplies a complete revision.
type WikiSaveInput struct {
	Slug             string
	ExpectedRevision int64
	Title            *string
	Markdown         *string
	SourceEpisodeIDs []int64
	RelatedSlugs     []string
	Deleted          bool
}

func validateWikiSlug(slug string) error {
	if slug == "" || len(slug) > MaxWikiSlugBytes || strings.HasPrefix(slug, "/") || validatePath("/"+slug) != nil {
		return fmt.Errorf("wiki slug must be a relative path of lowercase alphanumeric, hyphen or underscore segments, at most %d bytes", MaxWikiSlugBytes)
	}
	return nil
}

func validateWikiInput(input WikiSaveInput) (WikiSaveInput, error) {
	if err := validateWikiSlug(input.Slug); err != nil {
		return input, err
	}
	if input.ExpectedRevision < 0 || input.ExpectedRevision == math.MaxInt64 {
		return input, fmt.Errorf("expected_revision must be a nonnegative revision with room for the next version")
	}
	if input.Deleted {
		if input.Title != nil || input.Markdown != nil || input.SourceEpisodeIDs != nil || input.RelatedSlugs != nil {
			return input, fmt.Errorf("deletion accepts only slug, expected_revision and deleted; omit title, markdown, source_episode_ids and related_slugs")
		}
		return input, nil
	}
	if input.Title == nil || strings.TrimSpace(*input.Title) == "" || !utf8.ValidString(*input.Title) || len(*input.Title) > MaxWikiTitleBytes {
		return input, fmt.Errorf("wiki title must contain text and be at most %d UTF-8 bytes", MaxWikiTitleBytes)
	}
	if input.Markdown == nil || strings.TrimSpace(*input.Markdown) == "" || !utf8.ValidString(*input.Markdown) || len(*input.Markdown) > MaxWikiMarkdownBytes {
		return input, fmt.Errorf("wiki markdown must contain text and be at most %d UTF-8 bytes", MaxWikiMarkdownBytes)
	}
	if len(input.SourceEpisodeIDs) == 0 || len(input.SourceEpisodeIDs) > MaxWikiSources {
		return input, fmt.Errorf("wiki revisions require between 1 and %d source episodes", MaxWikiSources)
	}
	seenSources := make(map[int64]bool)
	sources := make([]int64, 0, len(input.SourceEpisodeIDs))
	for _, id := range input.SourceEpisodeIDs {
		if id <= 0 {
			return input, ErrWikiSource
		}
		if !seenSources[id] {
			sources = append(sources, id)
			seenSources[id] = true
		}
	}
	if len(input.RelatedSlugs) > MaxWikiRelatedPages {
		return input, fmt.Errorf("wiki revisions support at most %d related pages", MaxWikiRelatedPages)
	}
	seenRelated := make(map[string]bool)
	related := make([]string, 0, len(input.RelatedSlugs))
	for _, slug := range input.RelatedSlugs {
		if err := validateWikiSlug(slug); err != nil {
			return input, err
		}
		if !seenRelated[slug] {
			related = append(related, slug)
			seenRelated[slug] = true
		}
	}
	input.SourceEpisodeIDs, input.RelatedSlugs = sources, related
	return input, nil
}

// SaveWikiPage only inserts. The namespace lock serializes wiki edits and link
// validation, including links to a page being deleted by a concurrent save.
func (b *Brain) SaveWikiPage(ctx context.Context, namespaceID int64, input WikiSaveInput) (*models.WikiRevision, error) {
	input, err := validateWikiInput(input)
	if err != nil {
		return nil, err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, namespaceID); err != nil {
		return nil, err
	}
	var id int64
	if err := tx.QueryRow(ctx, `SELECT id FROM namespaces WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, namespaceID).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNamespaceNotFound
		}
		return nil, err
	}
	var current int64
	var deleted bool
	err = tx.QueryRow(ctx, `SELECT revision, deleted FROM wiki_revisions WHERE namespace_id=$1 AND slug=$2 ORDER BY revision DESC LIMIT 1`, namespaceID, input.Slug).Scan(&current, &deleted)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if current != input.ExpectedRevision {
		return nil, &WikiRevisionConflict{ExpectedRevision: input.ExpectedRevision, CurrentRevision: current}
	}
	if input.Deleted {
		if current == 0 {
			return nil, ErrWikiPageNotFound
		}
		if deleted {
			return nil, ErrWikiDeleted
		}
	} else {
		// Share locks keep source soft-deletes from racing the validated insert.
		rows, err := tx.Query(ctx, `SELECT id FROM episodes WHERE namespace_id=$1 AND id=ANY($2::bigint[]) AND deleted_at IS NULL ORDER BY id FOR SHARE`, namespaceID, input.SourceEpisodeIDs)
		if err != nil {
			return nil, err
		}
		sources, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return nil, err
		}
		if len(sources) != len(input.SourceEpisodeIDs) {
			return nil, ErrWikiSource
		}
		if len(input.RelatedSlugs) > 0 {
			var count int
			err := tx.QueryRow(ctx, `SELECT count(*) FROM (
				SELECT DISTINCT ON (slug) slug, deleted FROM wiki_revisions
				WHERE namespace_id=$1 AND slug=ANY($2::text[]) ORDER BY slug, revision DESC
			) latest WHERE NOT deleted`, namespaceID, input.RelatedSlugs).Scan(&count)
			if err != nil {
				return nil, err
			}
			if count != len(input.RelatedSlugs) {
				return nil, ErrWikiRelatedPage
			}
		}
	}
	result := &models.WikiRevision{
		NamespaceID: namespaceID, Slug: input.Slug, Revision: current + 1, Deleted: input.Deleted,
		SourceEpisodeIDs: []int64{}, RelatedSlugs: []string{},
	}
	if !input.Deleted {
		result.Title, result.Markdown = *input.Title, *input.Markdown
		result.SourceEpisodeIDs, result.RelatedSlugs = input.SourceEpisodeIDs, input.RelatedSlugs
	}
	err = tx.QueryRow(ctx, `INSERT INTO wiki_revisions
		(namespace_id, slug, revision, title, markdown, source_episode_ids, related_slugs, deleted)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING created_at`,
		namespaceID, result.Slug, result.Revision, result.Title, result.Markdown, result.SourceEpisodeIDs, result.RelatedSlugs, result.Deleted).Scan(&result.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
				return nil, rollbackErr
			}
			if readErr := b.pool.QueryRow(ctx, `SELECT COALESCE(MAX(revision),0) FROM wiki_revisions WHERE namespace_id=$1 AND slug=$2`, namespaceID, input.Slug).Scan(&current); readErr != nil {
				return nil, readErr
			}
			return nil, &WikiRevisionConflict{ExpectedRevision: input.ExpectedRevision, CurrentRevision: current}
		}
		return nil, fmt.Errorf("insert wiki revision: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

// ListWikiPages filters only after choosing each document's latest revision.
func (b *Brain) ListWikiPages(ctx context.Context, namespaceID int64, query string, page Pagination) (*models.WikiPageList, error) {
	page = b.sanitizePage(page)
	if len(query) > 1000 || !utf8.ValidString(query) {
		return nil, fmt.Errorf("wiki query exceeds 1000 UTF-8 bytes or is invalid text")
	}
	rows, err := b.pool.Query(ctx, `SELECT latest.slug, latest.revision, latest.title, left(latest.markdown,120), latest.created_at
		FROM (
			SELECT DISTINCT ON (slug) slug, revision, title, markdown, deleted, created_at
			FROM wiki_revisions WHERE namespace_id=$1 ORDER BY slug, revision DESC
		) latest JOIN namespaces ns ON ns.id=$1 AND ns.deleted_at IS NULL
		WHERE NOT latest.deleted AND (latest.title ILIKE $2 ESCAPE '\' OR latest.markdown ILIKE $2 ESCAPE '\')
		ORDER BY latest.slug LIMIT $3 OFFSET $4`, namespaceID, "%"+escapeLikePattern(strings.TrimSpace(query))+"%", page.Limit+1, page.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := &models.WikiPageList{Pages: []models.WikiPageSummary{}, NextOffset: page.Offset}
	for rows.Next() {
		var item models.WikiPageSummary
		if err := rows.Scan(&item.Slug, &item.Revision, &item.Title, &item.Summary, &item.CreatedAt); err != nil {
			return nil, err
		}
		if len(result.Pages) == page.Limit {
			result.HasMore = true
			break
		}
		result.Pages = append(result.Pages, item)
	}
	result.NextOffset += len(result.Pages)
	return result, rows.Err()
}

// A zero revision selects the current version; a positive value selects an
// immutable historical version, even when the document has since been deleted.
func (b *Brain) GetWikiPage(ctx context.Context, namespaceID int64, slug string, revision int64) (*models.WikiPage, error) {
	if err := validateWikiSlug(slug); err != nil {
		return nil, err
	}
	if revision < 0 {
		return nil, fmt.Errorf("wiki revision cannot be negative")
	}
	page := &models.WikiPage{Sources: []models.WikiSource{}, RelatedPages: []models.WikiRelatedPage{}}
	err := b.pool.QueryRow(ctx, `WITH latest AS (
		SELECT MAX(revision) AS revision FROM wiki_revisions WHERE namespace_id=$1 AND slug=$2
	) SELECT w.namespace_id,w.slug,w.revision,w.title,w.markdown,w.source_episode_ids,w.related_slugs,w.deleted,w.created_at,latest.revision
		FROM wiki_revisions w CROSS JOIN latest JOIN namespaces ns ON ns.id=w.namespace_id AND ns.deleted_at IS NULL
		WHERE w.namespace_id=$1 AND w.slug=$2 AND w.revision=CASE WHEN $3::bigint=0 THEN latest.revision ELSE $3 END`,
		namespaceID, slug, revision).Scan(&page.NamespaceID, &page.Slug, &page.Revision, &page.Title, &page.Markdown, &page.SourceEpisodeIDs, &page.RelatedSlugs, &page.Deleted, &page.CreatedAt, &page.CurrentRevision)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrWikiPageNotFound
		}
		return nil, fmt.Errorf("get wiki revision: %w", err)
	}
	if len(page.SourceEpisodeIDs) > 0 {
		rows, err := b.pool.Query(ctx, `SELECT id FROM episodes WHERE namespace_id=$1 AND id=ANY($2::bigint[]) AND deleted_at IS NULL`, namespaceID, page.SourceEpisodeIDs)
		if err != nil {
			return nil, err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return nil, err
		}
		available := make(map[int64]bool, len(ids))
		for _, id := range ids {
			available[id] = true
		}
		for _, id := range page.SourceEpisodeIDs {
			page.Sources = append(page.Sources, models.WikiSource{EpisodeID: id, Available: available[id]})
		}
	}
	if len(page.RelatedSlugs) > 0 {
		rows, err := b.pool.Query(ctx, `SELECT DISTINCT ON (slug) slug, revision, NOT deleted FROM wiki_revisions WHERE namespace_id=$1 AND slug=ANY($2::text[]) ORDER BY slug, revision DESC`, namespaceID, page.RelatedSlugs)
		if err != nil {
			return nil, err
		}
		links, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (models.WikiRelatedPage, error) {
			var link models.WikiRelatedPage
			err := row.Scan(&link.Slug, &link.CurrentRevision, &link.Available)
			return link, err
		})
		if err != nil {
			return nil, err
		}
		bySlug := make(map[string]models.WikiRelatedPage, len(links))
		for _, link := range links {
			bySlug[link.Slug] = link
		}
		for _, slug := range page.RelatedSlugs {
			link, ok := bySlug[slug]
			if !ok {
				link.Slug = slug
			}
			page.RelatedPages = append(page.RelatedPages, link)
		}
	}
	return page, nil
}
