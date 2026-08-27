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
// for the configured model and vector dimension. The caller owns the pool.
func Open(ctx context.Context, dsn string, expectedModel string, vectorDim int) (*pgxpool.Pool, error) {
	pool, _, err := OpenWithReport(ctx, dsn, expectedModel, vectorDim)
	return pool, err
}

// EmbeddingStorageReport describes automatic storage preparation performed at
// startup. ReindexQueued counts non-deleted episodes and facts whose vectors
// were cleared and queued for the background embedding worker.
type EmbeddingStorageReport struct {
	DimensionChanged bool
	ModelChanged     bool
	ReindexQueued    int64
}

// OpenWithReport creates a pgxpool, runs goose migrations, and prepares the
// embedding storage for the configured model and dimension. If either changes,
// it queues a background reindex while preserving the source content.
func OpenWithReport(ctx context.Context, dsn string, expectedModel string, vectorDim int) (*pgxpool.Pool, EmbeddingStorageReport, error) {
	var report EmbeddingStorageReport
	if vectorDim <= 0 {
		return nil, report, fmt.Errorf("vector dimension must be greater than zero")
	}
	// Stash stores the standard pgvector `vector` type and builds cosine HNSW
	// indexes for it. pgvector limits that type/index combination to 2,000
	// dimensions; fail before connecting instead of leaving a half-initialized
	// database with a misleading migration error.
	if vectorDim > 2000 {
		return nil, report, fmt.Errorf("vector dimension %d is unsupported: standard pgvector indexes support at most 2000 dimensions", vectorDim)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, report, fmt.Errorf("pgxpool.ParseConfig: %w", err)
	}

	config.MaxConns = 25
	config.MinConns = 5
	config.MaxConnLifetime = 30 * time.Minute
	config.MaxConnIdleTime = 5 * time.Minute
	config.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, report, fmt.Errorf("pgxpool.NewWithConfig: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, report, fmt.Errorf("pgxpool.Ping: %w", err)
	}

	// Open a *sql.DB backed by pgx for goose migrations.
	connConfig := pool.Config().ConnConfig
	sqlDB := stdlib.OpenDB(*connConfig)
	defer sqlDB.Close()

	goose.SetBaseFS(embedMigrations)
	goose.SetLogger(discardLogger{})

	if err := goose.SetDialect("postgres"); err != nil {
		pool.Close()
		return nil, report, fmt.Errorf("goose.SetDialect: %w", err)
	}

	if err := goose.Up(sqlDB, "migrations"); err != nil {
		pool.Close()
		return nil, report, fmt.Errorf("goose.Up: %w", err)
	}

	// Prepare dimensions, model metadata, and the durable reindex queue.
	report, err = prepareEmbeddingStorage(ctx, sqlDB, expectedModel, vectorDim)
	if err != nil {
		pool.Close()
		return nil, report, fmt.Errorf("prepare embedding storage: %w", err)
	}

	return pool, report, nil
}
