// Package embedder converts text into fixed-dimension vectors.
// Implementations: OpenAI (production), Fake (tests).
package embedder

import (
	"context"
)

// Embedder converts text into a fixed-dimension vector.
// Implementations: OpenAI (production), Fake (tests).
type Embedder interface {
	// Embed generates a vector embedding for text being STORED (a passage).
	Embed(ctx context.Context, text string) ([]float32, error)

	// EmbedQuery generates a vector embedding for a SEARCH QUERY.
	//
	// Asymmetric models (the e5 family in particular) are trained with distinct
	// "query: " and "passage: " prefixes and lose noticeable accuracy when a short
	// query is embedded the same way as a long document. Implementations that wrap a
	// symmetric model may simply delegate to Embed.
	EmbedQuery(ctx context.Context, text string) ([]float32, error)

	// Model returns the full model string as passed at construction.
	// Examples: "openai/text-embedding-3-small", "nomic-embed-text".
	// Used as the vector key in store.Record.Vectors.
	Model() string

	// Dims returns the vector dimensions, e.g. 1536, 768.
	Dims() int
}
