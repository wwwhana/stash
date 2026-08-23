package embedder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

// Cached wraps an Embedder with pgx-backed caching and request deduplication.
type Cached struct {
	embedder Embedder
	pool     *pgxpool.Pool
	inflight sync.Map
}

type call struct {
	wg  sync.WaitGroup
	vec []float32
	err error
}

// NewCached creates a cached embedder that stores embeddings in the embedding_cache table.
func NewCached(e Embedder, pool *pgxpool.Pool) *Cached {
	return &Cached{
		embedder: e,
		pool:     pool,
	}
}

// Embed returns a cached embedding for a stored passage.
func (c *Cached) Embed(ctx context.Context, text string) ([]float32, error) {
	return c.embedCached(ctx, "passage", text, c.embedder.Embed)
}

// EmbedQuery returns a cached embedding for a search query.
func (c *Cached) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	return c.embedCached(ctx, "query", text, c.embedder.EmbedQuery)
}

// embedCached is the shared cache/dedup path.
//
// `role` ("passage" or "query") is part of the cache key on purpose: with an
// asymmetric model the same text embeds to a different vector depending on which
// prefix the underlying embedder applies, so the two must not share a cache entry.
func (c *Cached) embedCached(
	ctx context.Context,
	role string,
	text string,
	embed func(context.Context, string) ([]float32, error),
) ([]float32, error) {
	hash := cacheKey(role + "\x00" + text)

	// Try cache
	cached, err := c.getCached(ctx, hash, c.embedder.Model())
	if err == nil && cached != nil {
		return cached, nil
	}

	// Check inflight dedup
	callVal, loaded := c.inflight.LoadOrStore(hash, &call{})
	callInfo := callVal.(*call)

	if loaded {
		callInfo.wg.Wait()
		return callInfo.vec, callInfo.err
	}

	callInfo.wg.Add(1)
	defer func() {
		callInfo.wg.Done()
		c.inflight.Delete(hash)
	}()

	vec, err := embed(ctx, text)
	if err != nil {
		callInfo.err = err
		return nil, err
	}

	callInfo.vec = vec

	// Write cache in background with timeout
	cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.putCached(cacheCtx, hash, text, vec, c.embedder.Model()); err != nil {
		log.Printf("embedder: cache write failed for hash %s: %v", hash[:8], err)
	}

	return vec, nil
}

// Model returns the underlying embedder's model.
func (c *Cached) Model() string {
	return c.embedder.Model()
}

// Dims returns the underlying embedder's dimensions.
func (c *Cached) Dims() int {
	return c.embedder.Dims()
}

func (c *Cached) getCached(ctx context.Context, hash, model string) ([]float32, error) {
	var vec pgvector.Vector
	err := c.pool.QueryRow(ctx,
		"SELECT embedding FROM embedding_cache WHERE text_hash = $1 AND model = $2",
		hash, model,
	).Scan(&vec)
	if err != nil {
		return nil, err
	}
	return vec.Slice(), nil
}

func (c *Cached) putCached(ctx context.Context, hash, text string, vec []float32, model string) error {
	_, err := c.pool.Exec(ctx,
		`INSERT INTO embedding_cache (text_hash, model, text, embedding)
		 VALUES ($1, $2, $3, $4) ON CONFLICT (text_hash, model) DO NOTHING`,
		hash, model, text, pgvector.NewVector(vec),
	)
	return err
}

func cacheKey(text string) string {
	hash := sha256.Sum256([]byte(text))
	return hex.EncodeToString(hash[:])
}
