package brain

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alash3al/stash/internal/embedder"
	"github.com/alash3al/stash/internal/queries"
	"github.com/alash3al/stash/internal/reasoner"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNamespaceNotFound = fmt.Errorf("brain: namespace not found — call create_namespace first")
	ErrEpisodeNotFound   = fmt.Errorf("brain: episode not found")
	ErrFactNotFound      = fmt.Errorf("brain: fact not found")
	ErrEmptyContent      = fmt.Errorf("brain: content cannot be empty")
	ErrContentTooLong    = fmt.Errorf("brain: content exceeds maximum length")
	ErrInvalidConfidence = fmt.Errorf("brain: confidence must be between 0 and 1")
	ErrInvalidScore      = fmt.Errorf("brain: score must be between 0 and 1")
	ErrInvalidPath       = fmt.Errorf("brain: namespace path must start with / and contain valid segments (lowercase alphanumeric, hyphens, underscores)")

	pathSegmentRe         = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	ErrNamespacesRequired = fmt.Errorf("brain: at least one namespace is required")
	maxContentLen         = 10000
)

const (
	DefaultLimit = 100
	MaxLimit     = 1000
)

// Pagination controls offset-based pagination for list queries.
type Pagination struct {
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}

// Sanitize applies defaults and enforces bounds.
func (p Pagination) Sanitize() Pagination {
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	return p
}

// SanitizeWithMax applies the hard safety bound and the configured result
// limit. A smaller configured value is useful for deployments that must keep
// MCP responses small; the hard bound still protects callers that omit config.
func (p Pagination) SanitizeWithMax(max int) Pagination {
	p = p.Sanitize()
	if max > 0 && max < p.Limit {
		p.Limit = max
	}
	return p
}

type Config struct {
	MaxResultSize                  int
	BatchSize                      int
	SimilarityThreshold            float64
	DedupThreshold                 float64
	Window                         time.Duration
	DecayFactor                    float64
	ExpiryThreshold                float32
	HypothesisAutoConfirmThreshold float32
	HypothesisAutoRejectThreshold  float32
	EmbeddingRetryInterval         time.Duration
	EmbeddingRetryMaxInterval      time.Duration
}

func DefaultConfig() Config {
	return Config{
		MaxResultSize:                  10000,
		BatchSize:                      100,
		SimilarityThreshold:            0.85,
		DedupThreshold:                 0.85,
		Window:                         7 * 24 * time.Hour,
		DecayFactor:                    0.95,
		ExpiryThreshold:                0.1,
		HypothesisAutoConfirmThreshold: 0.9,
		HypothesisAutoRejectThreshold:  0.9,
		EmbeddingRetryInterval:         time.Minute,
		EmbeddingRetryMaxInterval:      time.Hour,
	}
}

type Brain struct {
	pool     *pgxpool.Pool
	embedder embedder.Embedder
	reasoner reasoner.Reasoner
	queries  *queries.Queries
	config   Config
	// Consolidation is also exposed as an MCP tool while the background worker
	// is running. Keep the checkpoint and derived rows consistent when both
	// paths target the same process.
	consolidationMu sync.Mutex
	// Alternate which durable embedding queue receives an odd batch slot. This
	// keeps a batch size of one from permanently starving facts or episodes.
	embeddingRetryPass atomic.Uint64
}

func (b *Brain) sanitizePage(page Pagination) Pagination {
	return page.SanitizeWithMax(b.config.MaxResultSize)
}

func New(pool *pgxpool.Pool, e embedder.Embedder, r reasoner.Reasoner, q *queries.Queries, cfg Config) (*Brain, error) {
	if pool == nil {
		return nil, fmt.Errorf("brain: pool is required")
	}
	if e == nil {
		return nil, fmt.Errorf("brain: embedder is required")
	}
	if r == nil {
		return nil, fmt.Errorf("brain: reasoner is required")
	}
	if q == nil {
		return nil, fmt.Errorf("brain: queries is required")
	}
	defaults := DefaultConfig()
	if cfg.EmbeddingRetryInterval <= 0 {
		cfg.EmbeddingRetryInterval = defaults.EmbeddingRetryInterval
	}
	if cfg.EmbeddingRetryMaxInterval < cfg.EmbeddingRetryInterval {
		cfg.EmbeddingRetryMaxInterval = defaults.EmbeddingRetryMaxInterval
		if cfg.EmbeddingRetryMaxInterval < cfg.EmbeddingRetryInterval {
			cfg.EmbeddingRetryMaxInterval = cfg.EmbeddingRetryInterval
		}
	}
	return &Brain{
		pool:     pool,
		embedder: e,
		reasoner: r,
		queries:  q,
		config:   cfg,
	}, nil
}

func (b *Brain) Close() {
	b.pool.Close()
}

func validateContent(content string) error {
	if strings.TrimSpace(content) == "" {
		return ErrEmptyContent
	}
	if len(content) > maxContentLen {
		return ErrContentTooLong
	}
	return nil
}

func validateConfidence(confidence float32) error {
	if math.IsNaN(float64(confidence)) || math.IsInf(float64(confidence), 0) || confidence < 0 || confidence > 1 {
		return ErrInvalidConfidence
	}
	return nil
}

func validateScore(score float32) error {
	if math.IsNaN(float64(score)) || math.IsInf(float64(score), 0) || score < 0 || score > 1 {
		return ErrInvalidScore
	}
	return nil
}

func validatePath(path string) error {
	if path == "" || path == "/" {
		return nil
	}
	if !strings.HasPrefix(path, "/") {
		return ErrInvalidPath
	}
	if strings.HasSuffix(path, "/") {
		return ErrInvalidPath
	}
	segments := splitPath(path)
	for _, seg := range segments {
		if !pathSegmentRe.MatchString(seg) {
			return ErrInvalidPath
		}
	}
	return nil
}

// splitPath splits a path into its segments.
// "/" → [], "/foo/bar" → ["foo", "bar"]
func splitPath(path string) []string {
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

// escapeLikePattern escapes PostgreSQL LIKE meta-characters so the input
// is matched as a literal. Use with `ESCAPE '\'` in the query.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func escapeLikePattern(s string) string {
	return likeEscaper.Replace(s)
}

// likePatternForDescendants builds a LIKE prefix for slug descendant lookup.
func likePatternForDescendants(path string) string {
	return escapeLikePattern(path) + "/%"
}

// resolveNamespaceID returns the namespace ID for an exact path.
// Returns ErrNamespaceNotFound if no matching namespace exists.
func (b *Brain) resolveNamespaceID(ctx context.Context, path string) (int64, error) {
	var id int64
	err := b.pool.QueryRow(ctx,
		"SELECT id FROM namespaces WHERE slug = $1", path,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNamespaceNotFound
		}
		return 0, fmt.Errorf("resolve namespace %q: %w", path, err)
	}
	return id, nil
}

// ResolveNamespaceIDs resolves a list of paths to namespace IDs.
// Each path matches itself and all descendants. For "/", returns all IDs.
// Returns ErrNamespacesRequired if paths is empty.
func (b *Brain) ResolveNamespaceIDs(ctx context.Context, paths []string) ([]int64, error) {
	return b.resolveNamespaceIDs(ctx, paths)
}

// resolveNamespaceIDs resolves a list of paths to namespace IDs.
// Each path matches itself and all descendants.
func (b *Brain) resolveNamespaceIDs(ctx context.Context, paths []string) ([]int64, error) {
	if len(paths) == 0 {
		return nil, ErrNamespacesRequired
	}

	var allIDs []int64
	seen := make(map[int64]bool)

	for _, p := range paths {
		if err := validatePath(p); err != nil {
			return nil, fmt.Errorf("namespace %q: %w", p, err)
		}
		expanded, err := b.resolveNamespaceIDWithDescendants(ctx, p)
		if err != nil {
			return nil, fmt.Errorf("namespace %q: %w", p, err)
		}
		for _, id := range expanded {
			if !seen[id] {
				seen[id] = true
				allIDs = append(allIDs, id)
			}
		}
	}

	return allIDs, nil
}

// resolveNamespaceIDWithDescendants returns IDs for the exact path plus all descendants.
// For "/", returns all namespace IDs.
func (b *Brain) resolveNamespaceIDWithDescendants(ctx context.Context, path string) ([]int64, error) {
	if path == "/" {
		rows, err := b.pool.Query(ctx, "SELECT id FROM namespaces")
		if err != nil {
			return nil, fmt.Errorf("resolve all namespaces: %w", err)
		}
		defer rows.Close()

		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return nil, fmt.Errorf("scan namespace: %w", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("read all namespaces: %w", err)
		}
		return ids, nil
	}

	// Slug segments allow `_` (see pathSegmentRe), which is a LIKE wildcard
	// in PostgreSQL. Escape it so `/foo_bar` only matches its own descendants,
	// not `/fooXbar/...` for any X.
	rows, err := b.pool.Query(ctx,
		`SELECT id FROM namespaces WHERE slug = $1 OR slug LIKE $2 ESCAPE '\'`,
		path, likePatternForDescendants(path),
	)
	if err != nil {
		return nil, fmt.Errorf("resolve namespace descendants: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan namespace: %w", err)
		}
		ids = append(ids, id)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read namespace descendants: %w", err)
	}
	if len(ids) == 0 {
		return nil, ErrNamespaceNotFound
	}

	return ids, nil
}

func (b *Brain) Health(ctx context.Context) error {
	return b.pool.Ping(ctx)
}

func (b *Brain) Ready(ctx context.Context) error {
	_, err := b.pool.Exec(ctx, "SELECT 1 FROM consolidation_progress LIMIT 0")
	if err != nil {
		return fmt.Errorf("ready: %w", err)
	}
	return b.pool.Ping(ctx)
}
