package brain

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alash3al/stash/internal/db"
)

type queueTestEmbedder struct {
	calls atomic.Int64
}

func (e *queueTestEmbedder) Embed(context.Context, string) ([]float32, error) {
	e.calls.Add(1)
	return []float32{1, 0, 0}, nil
}

func (e *queueTestEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	e.calls.Add(1)
	return []float32{1, 0, 0}, nil
}

func (*queueTestEmbedder) Model() string { return "test/queued" }
func (*queueTestEmbedder) Dims() int     { return 3 }

func TestQueueRememberPersistsBeforeEmbedding(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("STASH_TEST_POSTGRES_DSN"))
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("STASH_TEST_DATABASE_URL"))
	}
	if dsn == "" {
		t.Skip("set STASH_TEST_POSTGRES_DSN to a disposable pgvector PostgreSQL database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, dsn, "prompt-history-queue-test", 3)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer pool.Close()

	embedder := &queueTestEmbedder{}
	b := &Brain{
		pool:               pool,
		embedder:           embedder,
		config:             DefaultConfig(),
		embeddingRetryWake: make(chan struct{}, 1),
	}
	slug := fmt.Sprintf("/prompt-history-%d", time.Now().UnixNano())
	namespaceID, err := b.CreateNamespace(ctx, slug, "prompt history", "")
	if err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM namespaces WHERE id = $1`, namespaceID)
	}()

	queued, err := b.QueueRemember(ctx, slug, "save this prompt without waiting for the provider", nil)
	if err != nil {
		t.Fatalf("QueueRemember: %v", err)
	}
	if queued.ID == 0 || queued.Indexed || queued.IndexingStatus != "pending" || queued.RetryAt == nil {
		t.Fatalf("queued result = %#v", queued)
	}
	if got := embedder.calls.Load(); got != 0 {
		t.Fatalf("queue path called the embedding provider %d times", got)
	}

	var content, model string
	var attempts int
	var embeddingIsNull bool
	var retryAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT content, embedding_model, embedding_attempts,
		       embedding IS NULL, embedding_retry_at
		FROM episodes
		WHERE id = $1
	`, queued.ID).Scan(&content, &model, &attempts, &embeddingIsNull, &retryAt); err != nil {
		t.Fatalf("read queued episode: %v", err)
	}
	if content != "save this prompt without waiting for the provider" || model != "test/queued" {
		t.Fatalf("queued episode content=%q model=%q", content, model)
	}
	if attempts != 0 || !embeddingIsNull || retryAt.IsZero() {
		t.Fatalf("queued episode attempts=%d embedding_missing=%v retry_at=%v", attempts, embeddingIsNull, retryAt)
	}

	select {
	case <-b.EmbeddingRetryWake():
	default:
		t.Fatal("queue path did not wake the background embedding worker")
	}

	retried, err := b.RetryPendingEmbeddings(ctx, 1)
	if err != nil {
		t.Fatalf("RetryPendingEmbeddings: %v", err)
	}
	if retried.Attempted != 1 || retried.Indexed != 1 || retried.Failed != 0 {
		t.Fatalf("embedding worker result = %#v", retried)
	}
	if got := embedder.calls.Load(); got != 1 {
		t.Fatalf("embedding worker called the provider %d times, want 1", got)
	}
	var embeddingIndexed bool
	if err := pool.QueryRow(ctx, `SELECT embedding IS NOT NULL FROM episodes WHERE id = $1`, queued.ID).Scan(&embeddingIndexed); err != nil {
		t.Fatalf("read indexed episode: %v", err)
	}
	if !embeddingIndexed {
		t.Fatal("embedding worker did not index the queued episode")
	}
}
