package brain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/alash3al/stash/internal/queries"
	"github.com/jackc/pgx/v5/pgxpool"
)

type recallFailingEmbedder struct{}

func (recallFailingEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, errors.New("test embedding provider unavailable")
}

func (recallFailingEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	return nil, errors.New("test embedding provider unavailable")
}

func (recallFailingEmbedder) Model() string { return "test/recall-fallback" }
func (recallFailingEmbedder) Dims() int     { return 1 }

func TestRecallUsesTrigramWhenQueryEmbeddingFails(t *testing.T) {
	dsn := os.Getenv("STASH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("STASH_TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer p.Close()
	if err := p.Ping(ctx); err != nil {
		t.Fatalf("database ping: %v", err)
	}

	q, err := queries.New()
	if err != nil {
		t.Fatalf("queries.New: %v", err)
	}
	b := &Brain{pool: p, embedder: recallFailingEmbedder{}, queries: q, config: DefaultConfig()}

	slug := fmt.Sprintf("/recall-trigram-%d", time.Now().UnixNano())
	var namespaceID int64
	if err := p.QueryRow(ctx,
		`INSERT INTO namespaces (slug, name, description) VALUES ($1, 'recall trigram fallback', '') RETURNING id`,
		slug,
	).Scan(&namespaceID); err != nil {
		t.Fatalf("create test namespace: %v", err)
	}
	defer func() {
		if _, err := p.Exec(context.Background(), `DELETE FROM namespaces WHERE id = $1`, namespaceID); err != nil {
			t.Errorf("delete test namespace: %v", err)
		}
	}()

	const marker = "TRIGRAM-FALLBACK-7F3A"
	remembered, err := b.RememberWithStatus(ctx, slug, "pending embedding contains "+marker, nil)
	if err != nil {
		t.Fatalf("RememberWithStatus: %v", err)
	}
	if remembered.Indexed || remembered.IndexingStatus != "pending" {
		t.Fatalf("failed embedding was not queued: %#v", remembered)
	}
	episodeID := remembered.ID
	var factID int64
	if err := p.QueryRow(ctx,
		`INSERT INTO facts (namespace_id, content, confidence) VALUES ($1, $2, 1) RETURNING id`,
		namespaceID, "consolidated fact contains "+marker,
	).Scan(&factID); err != nil {
		t.Fatalf("insert fact: %v", err)
	}

	results, err := b.RecallWithOptions(ctx, []string{slug}, marker, 10, RecallOptions{})
	if err != nil {
		t.Fatalf("RecallWithOptions should use trigram fallback: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("trigram fallback returned %d results, want episode and fact: %#v", len(results), results)
	}
	seen := map[string]bool{}
	for _, result := range results {
		if result.VectorScore != 0 {
			t.Fatalf("fallback result has vector score %v: %#v", result.VectorScore, result)
		}
		if result.KeywordScore <= 0 {
			t.Fatalf("fallback result has no trigram score: %#v", result)
		}
		if result.Type == "episode" && result.ID == episodeID {
			seen["episode"] = true
		}
		if result.Type == "fact" && result.ID == factID {
			seen["fact"] = true
		}
	}
	if !seen["episode"] || !seen["fact"] {
		t.Fatalf("trigram fallback missed stored rows: seen=%v results=%#v", seen, results)
	}
}
