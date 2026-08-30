package models

// WorkContextFactState is the compact server-side state saved behind a work
// context cursor. Content is populated only for the current read and is never
// written to the cursor tables.
type WorkContextFactState struct {
	FactID           int64  `json:"fact_id"`
	Relation         string `json:"relation"`
	Content          string `json:"content,omitempty"`
	ContentTruncated bool   `json:"content_truncated,omitempty"`
	Status           string `json:"status"`
	StateDigest      string `json:"-"`
}

// WorkContextFactChange describes one linked fact that was added, updated, or
// removed since a previously returned context digest.
type WorkContextFactChange struct {
	FactID           int64  `json:"fact_id"`
	Relation         string `json:"relation"`
	Change           string `json:"change"`
	Content          string `json:"content,omitempty"`
	ContentTruncated bool   `json:"content_truncated,omitempty"`
	Status           string `json:"status"`
	StateDigest      string `json:"state_digest"`
}

// WorkContextFactDiff includes current states so the caller can persist a new
// cursor only after every bounded change page has been returned.
type WorkContextFactDiff struct {
	BaselineFound bool                    `json:"baseline_found"`
	Changes       []WorkContextFactChange `json:"changes"`
	CurrentStates []WorkContextFactState  `json:"-"`
}
