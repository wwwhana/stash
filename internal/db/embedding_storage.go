package db

import (
	"context"
	"database/sql"
	"fmt"
)

type vectorColumn struct {
	table  string
	column string
}

var vectorColumns = []vectorColumn{
	{table: "episodes", column: "embedding"},
	{table: "facts", column: "embedding"},
	{table: "embedding_cache", column: "embedding"},
}

var hnswIndexes = []struct {
	table  string
	column string
	name   string
}{
	{table: "episodes", column: "embedding", name: "episodes_embedding_hnsw_idx"},
	{table: "facts", column: "embedding", name: "facts_embedding_hnsw_idx"},
}

// prepareEmbeddingStorage keeps the schema, settings lock, and stored vectors
// consistent with the configured embedder. A changed model or dimension queues
// an automatic reindex; content rows are never deleted.
func prepareEmbeddingStorage(ctx context.Context, sqlDB *sql.DB, expectedModel string, expectedDim int) (EmbeddingStorageReport, error) {
	var report EmbeddingStorageReport

	needsResize := false
	for _, vc := range vectorColumns {
		currentDim, err := vectorColumnDimension(ctx, sqlDB, vc)
		if err != nil {
			return report, err
		}
		if currentDim != expectedDim {
			needsResize = true
		}
	}

	previousModel, modelExists, err := settingValue(ctx, sqlDB, "embedding_model")
	if err != nil {
		return report, fmt.Errorf("read embedding model setting: %w", err)
	}
	report.DimensionChanged = needsResize
	report.ModelChanged = modelExists && previousModel != "" && previousModel != expectedModel

	// A row-level check also repairs mixed data left by older releases or a
	// manually changed endpoint, even if the global setting was unchanged.
	mixedRows, err := countRowsWithDifferentModel(ctx, sqlDB, expectedModel)
	if err != nil {
		return report, err
	}
	needsReindex := needsResize || report.ModelChanged || mixedRows > 0

	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return report, fmt.Errorf("begin embedding storage update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if needsResize {
		for _, idx := range hnswIndexes {
			if _, err := tx.ExecContext(ctx, "DROP INDEX IF EXISTS "+idx.name); err != nil {
				return report, fmt.Errorf("drop hnsw index %s: %w", idx.name, err)
			}
		}
		for _, vc := range vectorColumns {
			// Existing values cannot be cast between arbitrary dimensions. The
			// content remains intact and all rows are queued below for fresh vectors.
			ddl := fmt.Sprintf(
				"ALTER TABLE %s ALTER COLUMN %s TYPE vector(%d) USING NULL::vector(%d)",
				vc.table, vc.column, expectedDim, expectedDim,
			)
			if _, err := tx.ExecContext(ctx, ddl); err != nil {
				return report, fmt.Errorf("alter vector dim %s.%s: %w", vc.table, vc.column, err)
			}
		}
	}

	if needsReindex {
		reason := fmt.Sprintf("embedding configuration changed to model %q (%d dimensions); queued for reindex", expectedModel, expectedDim)
		for _, table := range []string{"episodes", "facts"} {
			result, err := tx.ExecContext(ctx, fmt.Sprintf(`
				UPDATE %s
				SET embedding = NULL,
				    embedding_model = $1,
				    embedding_attempts = 0,
				    embedding_last_error = $2,
				    embedding_retry_at = now(),
				    embedding_updated_at = now()
				WHERE deleted_at IS NULL
			`, table), expectedModel, reason)
			if err != nil {
				return report, fmt.Errorf("queue %s reindex: %w", table, err)
			}
			rows, err := result.RowsAffected()
			if err != nil {
				return report, fmt.Errorf("count queued %s reindex rows: %w", table, err)
			}
			report.ReindexQueued += rows
		}
		// Cache entries are an optimization only. Clearing it avoids returning
		// a vector produced by an old endpoint under a reused model name.
		if _, err := tx.ExecContext(ctx, "DELETE FROM embedding_cache"); err != nil {
			return report, fmt.Errorf("clear embedding cache: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO settings (key, value)
		VALUES ('vector_dimension', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()
	`, fmt.Sprintf("%d", expectedDim)); err != nil {
		return report, fmt.Errorf("store vector dimension: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO settings (key, value)
		VALUES ('embedding_model', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()
	`, expectedModel); err != nil {
		return report, fmt.Errorf("store embedding model: %w", err)
	}

	for _, idx := range hnswIndexes {
		var exists bool
		if err := tx.QueryRowContext(ctx,
			"SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = $1)", idx.name,
		).Scan(&exists); err != nil {
			return report, fmt.Errorf("check hnsw index %s: %w", idx.name, err)
		}
		if !exists {
			ddl := fmt.Sprintf(
				"CREATE INDEX %s ON %s USING hnsw (%s vector_cosine_ops)",
				idx.name, idx.table, idx.column,
			)
			if _, err := tx.ExecContext(ctx, ddl); err != nil {
				return report, fmt.Errorf("create hnsw index %s: %w", idx.name, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("commit embedding storage update: %w", err)
	}
	return report, nil
}

func vectorColumnDimension(ctx context.Context, sqlDB *sql.DB, vc vectorColumn) (int, error) {
	var currentDim int
	err := sqlDB.QueryRowContext(ctx,
		"SELECT atttypmod FROM pg_attribute a JOIN pg_class c ON a.attrelid = c.oid WHERE c.relname = $1 AND a.attname = $2",
		vc.table, vc.column,
	).Scan(&currentDim)
	if err != nil {
		return 0, fmt.Errorf("check vector dim %s.%s: %w", vc.table, vc.column, err)
	}
	return currentDim, nil
}

func settingValue(ctx context.Context, sqlDB *sql.DB, key string) (string, bool, error) {
	var value string
	err := sqlDB.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = $1", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func countRowsWithDifferentModel(ctx context.Context, sqlDB *sql.DB, expectedModel string) (int64, error) {
	var count int64
	for _, table := range []string{"episodes", "facts"} {
		var tableCount int64
		if err := sqlDB.QueryRowContext(ctx, fmt.Sprintf(
			"SELECT count(*) FROM %s WHERE embedding IS NOT NULL AND deleted_at IS NULL AND embedding_model IS DISTINCT FROM $1",
			table,
		), expectedModel).Scan(&tableCount); err != nil {
			return 0, fmt.Errorf("check %s embedding models: %w", table, err)
		}
		count += tableCount
	}
	return count, nil
}
