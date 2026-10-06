package models

import (
	"encoding/json"
	"time"
)

// WikiPage is one Markdown page in a namespace's wiki.
type WikiPage struct {
	ID          int64      `db:"id" json:"id"`
	NamespaceID int64      `db:"namespace_id" json:"namespace_id"`
	Slug        string     `db:"slug" json:"slug"`
	Title       string     `db:"title" json:"title"`
	Kind        string     `db:"kind" json:"kind"`
	Summary     string     `db:"summary" json:"summary"`
	Content     string     `db:"content" json:"content,omitempty"`
	Tags        []string   `db:"tags" json:"tags"`
	Revision    int        `db:"revision" json:"revision"`
	Author      string     `db:"author" json:"author"`
	AuthorKind  string     `db:"author_kind" json:"author_kind"`
	Indexed     bool       `db:"-" json:"indexed"`
	StaleAt     *time.Time `db:"stale_at" json:"stale_at,omitempty"`
	CreatedAt   time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time  `db:"updated_at" json:"updated_at"`
	DeletedAt   *time.Time `db:"deleted_at" json:"deleted_at,omitempty"`
}

// WikiRevision is a historical copy of a page.
type WikiRevision struct {
	ID         int64     `db:"id" json:"id"`
	PageID     int64     `db:"page_id" json:"page_id"`
	Revision   int       `db:"revision" json:"revision"`
	Title      string    `db:"title" json:"title"`
	Summary    string    `db:"summary" json:"summary"`
	Content    string    `db:"content" json:"content,omitempty"`
	Tags       []string  `db:"tags" json:"tags"`
	Author     string    `db:"author" json:"author"`
	AuthorKind string    `db:"author_kind" json:"author_kind"`
	ChangeNote string    `db:"change_note" json:"change_note"`
	CreatedAt  time.Time `db:"created_at" json:"created_at"`
}

// WikiLink is an outgoing [[slug]] link. TargetPageID is nil for a link to a
// page that does not exist yet.
type WikiLink struct {
	PageID       int64  `db:"page_id" json:"page_id"`
	TargetSlug   string `db:"target_slug" json:"target_slug"`
	TargetPageID *int64 `db:"target_page_id" json:"target_page_id,omitempty"`
	TargetTitle  string `db:"-" json:"target_title,omitempty"`
}

// WikiSource is a citation from a page to memory, work, another page, or an
// external URL.
type WikiSource struct {
	PageID       int64     `db:"page_id" json:"page_id"`
	SourceType   string    `db:"source_type" json:"source_type"`
	SourceRef    string    `db:"source_ref" json:"source_ref"`
	SourceID     *int64    `db:"source_id" json:"source_id,omitempty"`
	Note         string    `db:"note" json:"note,omitempty"`
	SourceDigest string    `db:"source_digest" json:"-"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`

	// Resolved state, filled when a page is read.
	Status  string `db:"-" json:"status,omitempty"`
	Excerpt string `db:"-" json:"excerpt,omitempty"`
}

// WikiLogEntry is one row of the wiki activity log.
type WikiLogEntry struct {
	ID          int64           `db:"id" json:"id"`
	NamespaceID int64           `db:"namespace_id" json:"namespace_id"`
	PageID      *int64          `db:"page_id" json:"page_id,omitempty"`
	PageSlug    string          `db:"page_slug" json:"page_slug,omitempty"`
	Action      string          `db:"action" json:"action"`
	Actor       string          `db:"actor" json:"actor,omitempty"`
	ActorKind   string          `db:"actor_kind" json:"actor_kind"`
	Summary     string          `db:"summary" json:"summary"`
	Details     json.RawMessage `db:"details" json:"details,omitempty"`
	CreatedAt   time.Time       `db:"created_at" json:"created_at"`
}

// WikiPageDetail is a page with everything a reader needs around it.
type WikiPageDetail struct {
	Page      WikiPage     `json:"page"`
	Links     []WikiLink   `json:"links"`
	Backlinks []WikiLink   `json:"backlinks"`
	Sources   []WikiSource `json:"sources"`
}

// WikiLintFinding is one problem lint found in a namespace's wiki.
type WikiLintFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	PageID   int64  `json:"page_id,omitempty"`
	PageSlug string `json:"page_slug,omitempty"`
	Target   string `json:"target,omitempty"`
	Message  string `json:"message"`
}

// WikiLintReport summarizes a lint pass.
type WikiLintReport struct {
	NamespaceID int64             `json:"namespace_id"`
	Pages       int               `json:"pages"`
	Findings    []WikiLintFinding `json:"findings"`
	StaleMarked int               `json:"stale_marked"`
	StaleClear  int               `json:"stale_cleared"`
	CheckedAt   time.Time         `json:"checked_at"`
}
