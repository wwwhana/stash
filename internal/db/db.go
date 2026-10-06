package db

import (
	"context"
	"embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}
func (discardLogger) Fatalf(string, ...any) {}

//go:embed migrations/*.sql
var embedMigrations embed.FS

// Open creates a pgxpool, runs goose migrations, and prepares embedding storage
// for the given model and vector dimension. Tests and one-shot commands use
// it; the server opens the pool first and prepares storage once model routing
// has resolved the effective embedding model.
func Open(ctx context.Context, dsn string, expectedModel string, vectorDim int) (*pgxpool.Pool, error) {
	pool, err := OpenPool(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if _, err := PrepareEmbeddingStorage(ctx, pool, expectedModel, vectorDim); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// EmbeddingStorageReport describes automatic storage preparation. ReindexQueued
// counts non-deleted episodes and facts whose vectors were cleared and queued
// for the background embedding worker.
type EmbeddingStorageReport struct {
	DimensionChanged bool
	ModelChanged     bool
	ReindexQueued    int64
}

// OpenPool creates a pgxpool and runs goose migrations. The caller owns the pool.
func OpenPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.ParseConfig: %w", err)
	}

	config.MaxConns = 25
	config.MinConns = 5
	config.MaxConnLifetime = 30 * time.Minute
	config.MaxConnIdleTime = 5 * time.Minute
	config.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.NewWithConfig: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgxpool.Ping: %w", err)
	}

	// Open a *sql.DB backed by pgx for goose migrations.
	sqlDB := stdlib.OpenDB(*pool.Config().ConnConfig)
	defer sqlDB.Close()

	goose.SetBaseFS(embedMigrations)
	goose.SetLogger(discardLogger{})

	if err := goose.SetDialect("postgres"); err != nil {
		pool.Close()
		return nil, fmt.Errorf("goose.SetDialect: %w", err)
	}

	if err := goose.Up(sqlDB, "migrations"); err != nil {
		pool.Close()
		return nil, fmt.Errorf("goose.Up: %w", err)
	}
	return pool, nil
}

// PrepareEmbeddingStorage keeps the vector columns, settings lock, and stored
// vectors consistent with the effective embedding model. If the model or
// dimension changed, it queues a background reindex while preserving content.
func PrepareEmbeddingStorage(ctx context.Context, pool *pgxpool.Pool, expectedModel string, vectorDim int) (EmbeddingStorageReport, error) {
	var report EmbeddingStorageReport
	if vectorDim <= 0 {
		return report, fmt.Errorf("vector dimension must be greater than zero")
	}
	// Stash stores the standard pgvector `vector` type and builds cosine HNSW
	// indexes for it. pgvector limits that type/index combination to 2,000
	// dimensions; fail before touching the schema instead of leaving a
	// half-initialized database with a misleading migration error.
	if vectorDim > 2000 {
		return report, fmt.Errorf("vector dimension %d is unsupported: standard pgvector indexes support at most 2000 dimensions", vectorDim)
	}
	sqlDB := stdlib.OpenDB(*pool.Config().ConnConfig)
	defer sqlDB.Close()

	report, err := prepareEmbeddingStorage(ctx, sqlDB, expectedModel, vectorDim)
	if err != nil {
		return report, fmt.Errorf("prepare embedding storage: %w", err)
	}
	return report, nil
}
