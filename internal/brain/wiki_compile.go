package brain

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/alash3al/stash/internal/models"
	"github.com/alash3al/stash/internal/reasoner"
)

var ErrWikiCompileUnavailable = errors.New("brain: no reasoning provider is assigned to the wiki feature")

// WikiCompileRequest asks the server's reasoner to draft or refresh a page
// from memory. It is the optional server-side author; agents can always
// write pages themselves with WriteWikiPage.
type WikiCompileRequest struct {
	Slug  string
	Title string
	// Topic steers the draft: what the page should explain. When no explicit
	// sources are given, it is also the recall query that gathers evidence.
	Topic string
	// SourceRefs such as "fact:12" pin the evidence; empty means recall.
	SourceRefs []string
	MaxSources int
	Save       bool
	Author     string
	ChangeNote string
}

// WikiCompileResult is the draft plus what it was built from.
type WikiCompileResult struct {
	Draft   reasoner.WikiDraft     `json:"draft"`
	Model   string                 `json:"model"`
	Sources []models.WikiSource    `json:"sources"`
	Page    *models.WikiPageDetail `json:"page,omitempty"`
}

const defaultWikiCompileSources = 20

// CompileWikiPage gathers evidence, asks the wiki reasoner for a cited draft,
// and optionally stores it as a server-authored page.
func (b *Brain) CompileWikiPage(ctx context.Context, namespaceID int64, req WikiCompileRequest) (*WikiCompileResult, error) {
	author, ok := b.reasoner.(reasoner.WikiAuthor)
	if !ok {
		return nil, ErrWikiCompileUnavailable
	}
	slug, err := normalizeWikiSlug(req.Slug)
	if err != nil {
		return nil, err
	}
	topic := strings.TrimSpace(req.Topic)
	if topic == "" {
		topic = strings.TrimSpace(req.Title)
	}
	if topic == "" {
		topic = strings.ReplaceAll(slug, "/", " ")
	}
	var namespaceSlug string
	if err := b.pool.QueryRow(ctx, `SELECT slug FROM namespaces WHERE id = $1 AND deleted_at IS NULL`, namespaceID).Scan(&namespaceSlug); err != nil {
		return nil, ErrNamespaceNotFound
	}
	maxSources := req.MaxSources
	if maxSources <= 0 || maxSources > 100 {
		maxSources = defaultWikiCompileSources
	}

	// Evidence: pinned refs, or whatever recall finds for the topic.
	var sources []models.WikiSource
	if len(req.SourceRefs) > 0 {
		for _, raw := range req.SourceRefs {
			parsed, err := ParseWikiSourceRef(raw)
			if err != nil {
				return nil, err
			}
			resolved := b.resolveWikiSource(ctx, b.pool, namespaceID, parsed.Type, parsed.Ref)
			if resolved.Status == WikiSourceMissing {
				return nil, fmt.Errorf("%w: %s", ErrWikiInvalidSource, raw)
			}
			sources = append(sources, resolved)
		}
	} else {
		hits, err := b.RecallWithOptions(ctx, []string{namespaceSlug}, topic, maxSources, RecallOptions{})
		if err != nil {
			return nil, fmt.Errorf("gather wiki sources: %w", err)
		}
		for _, hit := range hits {
			if hit.Type != "episode" && hit.Type != "fact" {
				continue
			}
			sources = append(sources, models.WikiSource{SourceType: hit.Type, SourceRef: fmt.Sprint(hit.ID), SourceID: &hit.ID, Status: WikiSourceOK, Excerpt: hit.Content})
		}
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("brain: no memory found for %q; remember something first or pin sources", topic)
	}

	draftReq := reasoner.WikiDraftRequest{Slug: slug, Title: strings.TrimSpace(req.Title), Topic: topic}
	for _, source := range sources {
		draftReq.Sources = append(draftReq.Sources, reasoner.WikiSourceInput{Ref: source.SourceType + ":" + source.SourceRef, Content: b.wikiSourceFullText(ctx, namespaceID, source)})
	}
	if existing, err := b.GetWikiPage(ctx, namespaceID, slug); err == nil {
		draftReq.Existing = existing.Page.Content
		if draftReq.Title == "" {
			draftReq.Title = existing.Page.Title
		}
	}
	if refs, err := b.WikiPageRefs(ctx, namespaceID, 200); err == nil {
		for _, ref := range refs {
			if ref.Slug != slug {
				draftReq.Pages = append(draftReq.Pages, reasoner.WikiPageRef{Slug: ref.Slug, Title: ref.Title})
			}
		}
	}

	draft, err := author.DraftWikiPage(ctx, draftReq)
	if err != nil {
		return nil, fmt.Errorf("draft wiki page: %w", err)
	}
	if draft == nil || strings.TrimSpace(draft.Content) == "" {
		return nil, errors.New("brain: wiki reasoner returned an empty draft")
	}
	if strings.TrimSpace(draft.Title) == "" {
		draft.Title = draftReq.Title
	}
	if strings.TrimSpace(draft.Title) == "" {
		draft.Title = topic
	}
	result := &WikiCompileResult{Draft: *draft, Model: author.ModelName(), Sources: sources}
	if !req.Save {
		return result, nil
	}

	inputs := make([]WikiSourceInput, 0, len(sources))
	for _, source := range sources {
		inputs = append(inputs, WikiSourceInput{Type: source.SourceType, Ref: source.SourceRef})
	}
	authorName := strings.TrimSpace(req.Author)
	if authorName == "" {
		authorName = result.Model
	}
	note := strings.TrimSpace(req.ChangeNote)
	if note == "" {
		note = "compiled by " + result.Model
	}
	detail, _, err := b.WriteWikiPage(ctx, namespaceID, WikiPageInput{
		Slug: slug, Title: draft.Title, Summary: draft.Summary, Content: draft.Content, Tags: draft.Tags,
		Author: authorName, AuthorKind: "server", Sources: inputs, ChangeNote: note,
	})
	if err != nil {
		return nil, err
	}
	result.Page = detail
	tx, err := b.pool.Begin(ctx)
	if err == nil {
		_ = insertWikiLog(ctx, tx, namespaceID, &detail.Page.ID, slug, "compile", authorName, "server", fmt.Sprintf("Compiled %q from %d sources", draft.Title, len(sources)), map[string]any{"model": result.Model, "sources": len(sources)})
		_ = tx.Commit(ctx)
	}
	return result, nil
}

// wikiSourceFullText returns the full cited text for drafting; the resolver's
// excerpt is bounded for display and too short to write from.
func (b *Brain) wikiSourceFullText(ctx context.Context, namespaceID int64, source models.WikiSource) string {
	if source.SourceID == nil {
		return source.Excerpt
	}
	var text string
	var err error
	switch source.SourceType {
	case "episode":
		err = b.pool.QueryRow(ctx, `SELECT content FROM episodes WHERE id = $1`, *source.SourceID).Scan(&text)
	case "fact":
		err = b.pool.QueryRow(ctx, `SELECT content FROM facts WHERE id = $1`, *source.SourceID).Scan(&text)
	case "hypothesis":
		err = b.pool.QueryRow(ctx, `SELECT content FROM hypotheses WHERE id = $1`, *source.SourceID).Scan(&text)
	case "failure":
		err = b.pool.QueryRow(ctx, `SELECT content || E'\nReason: ' || reason || E'\nLesson: ' || lesson FROM failures WHERE id = $1`, *source.SourceID).Scan(&text)
	case "goal":
		err = b.pool.QueryRow(ctx, `SELECT content || E'\n' || notes FROM goals WHERE id = $1`, *source.SourceID).Scan(&text)
	case "work":
		err = b.pool.QueryRow(ctx, `SELECT title || E'\n' || description FROM work_items WHERE id = $1`, *source.SourceID).Scan(&text)
	case "page":
		err = b.pool.QueryRow(ctx, `SELECT title || E'\n' || content FROM wiki_pages WHERE id = $1 AND namespace_id = $2`, *source.SourceID, namespaceID).Scan(&text)
	default:
		return source.Excerpt
	}
	if err != nil || strings.TrimSpace(text) == "" {
		return source.Excerpt
	}
	return text
}
