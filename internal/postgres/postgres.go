// Package postgres stores content in PostgreSQL and runs the search queries.
package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Connect opens a connection pool and checks that the database answers.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("opening postgres pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	return pool, nil
}

// Migrate applies any pending migrations. Several replicas may start at once,
// so goose holds a Postgres advisory lock and the others wait their turn.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("creating migration lock: %w", err)
	}
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	migrator, err := goose.NewProvider(goose.DialectPostgres, db, files, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("loading migrations: %w", err)
	}
	if _, err := migrator.Up(ctx); err != nil {
		return fmt.Errorf("applying migrations: %w", err)
	}
	return nil
}

// Store reads and writes content.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a Store backed by pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Ping checks that the database answers, for the readiness probe.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
