package brain

import (
	"strings"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
)

func TestRootNamespaceConsolidationIsRejectedPostgres(t *testing.T) {
	b, ctx, _ := newWorkExecutionTestBrain(t)
	var rootID int64
	if err := b.pool.QueryRow(ctx, `SELECT id FROM namespaces WHERE slug = '/' AND deleted_at IS NULL`).Scan(&rootID); err != nil {
		t.Fatalf("read root namespace: %v", err)
	}
	_, err := b.ConsolidateByID(ctx, rootID)
	if err == nil || !strings.Contains(err.Error(), "root namespace cannot be consolidated") {
		t.Fatalf("ConsolidateByID root error = %v", err)
	}
}

func TestStructuredCorrectionIsNotDeduplicatedPostgres(t *testing.T) {
	b, ctx, namespaceID := newWorkExecutionTestBrain(t)
	b.config.DedupThreshold = 0.95
	vector := []float32{1, 0, 0}

	var originalID int64
	if err := b.pool.QueryRow(ctx,
		`INSERT INTO facts (namespace_id, content, embedding, embedding_model, confidence, entity, property, value, valid_from)
		 VALUES ($1, 'API listens on port 8080', $2, 'test', 0.9, 'API', 'port', '8080', $3)
		 RETURNING id`,
		namespaceID, pgvector.NewVector(vector), time.Now().UTC(),
	).Scan(&originalID); err != nil {
		t.Fatalf("insert original fact: %v", err)
	}

	duplicateID, err := b.findDuplicateFact(ctx, namespaceID, vector, "API", "port", "8080")
	if err != nil {
		t.Fatalf("find same-value duplicate: %v", err)
	}
	if duplicateID != originalID {
		t.Fatalf("same-value duplicate = %d, want %d", duplicateID, originalID)
	}

	correctionID, err := b.findDuplicateFact(ctx, namespaceID, vector, "API", "port", "9090")
	if err != nil {
		t.Fatalf("find corrected-value duplicate: %v", err)
	}
	if correctionID != 0 {
		t.Fatalf("corrected value was deduplicated into fact %d", correctionID)
	}
}

func TestPendingConsolidationStageInputsPostgres(t *testing.T) {
	b, ctx, namespaceID := newWorkExecutionTestBrain(t)
	cp, err := b.GetOrCreateConsolidationProgress(ctx, namespaceID)
	if err != nil {
		t.Fatalf("GetOrCreateConsolidationProgress: %v", err)
	}

	var episodeID int64
	if err := b.pool.QueryRow(ctx,
		`INSERT INTO episodes (namespace_id, content, occurred_at) VALUES ($1, 'pending observation', $2) RETURNING id`,
		namespaceID, time.Now().UTC(),
	).Scan(&episodeID); err != nil {
		t.Fatalf("insert pending episode: %v", err)
	}
	if pending, err := b.countPendingConsolidationStageInputs(ctx, namespaceID); err != nil || pending != 1 {
		t.Fatalf("pending inputs = %d, %v; want 1, nil", pending, err)
	}

	cp.LastEpisodeID = episodeID
	if err := b.SaveConsolidationProgress(ctx, *cp); err != nil {
		t.Fatalf("SaveConsolidationProgress: %v", err)
	}
	if pending, err := b.countPendingConsolidationStageInputs(ctx, namespaceID); err != nil || pending != 0 {
		t.Fatalf("pending inputs after checkpoint = %d, %v; want 0, nil", pending, err)
	}
}
