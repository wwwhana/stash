package models

import "time"

// WikiRevision is an append-only document version. Episodes remain its sources.
type WikiRevision struct {
	NamespaceID      int64     `db:"namespace_id" json:"namespace_id"`
	Slug             string    `db:"slug" json:"slug"`
	Revision         int64     `db:"revision" json:"revision"`
	Title            string    `db:"title" json:"title"`
	Markdown         string    `db:"markdown" json:"markdown"`
	SourceEpisodeIDs []int64   `db:"source_episode_ids" json:"source_episode_ids"`
	RelatedSlugs     []string  `db:"related_slugs" json:"related_slugs"`
	Deleted          bool      `db:"deleted" json:"deleted"`
	CreatedAt        time.Time `db:"created_at" json:"created_at"`
}

type WikiSource struct {
	EpisodeID int64 `json:"episode_id"`
	Available bool  `json:"available"`
}

type WikiRelatedPage struct {
	Slug            string `json:"slug"`
	Available       bool   `json:"available"`
	CurrentRevision int64  `json:"current_revision,omitempty"`
}

// WikiPage projects live link availability without rewriting a saved revision.
type WikiPage struct {
	WikiRevision
	CurrentRevision int64             `json:"current_revision"`
	Sources         []WikiSource      `json:"sources"`
	RelatedPages    []WikiRelatedPage `json:"related_pages"`
}

type WikiPageSummary struct {
	Slug      string    `json:"slug"`
	Revision  int64     `json:"revision"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary"`
	CreatedAt time.Time `json:"created_at"`
}

type WikiPageList struct {
	Pages      []WikiPageSummary `json:"pages"`
	HasMore    bool              `json:"has_more"`
	NextOffset int               `json:"next_offset"`
}
