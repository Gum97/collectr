// Package postgres owns the connection pool and the transaction helpers every
// module uses. It is also where tenant isolation is enforced in practice: the
// RLS policies only bite if app.tenant_id is set, and InTenantTx is the only
// supported way to set it.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UniqueViolation is the PostgreSQL SQLSTATE for a unique constraint conflict.
const UniqueViolation = "23505"

// DB wraps a pgx pool with the project's transaction conventions.
type DB struct {
	*pgxpool.Pool
}

// Open creates and verifies a connection pool.
func Open(ctx context.Context, url string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parsing database url: %w", err)
	}
	// Sized for a single app node against one Postgres: enough for the hot path
	// plus background jobs, far below the server's max_connections.
	cfg.MaxConns = 20
	cfg.MinConns = 2
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 15 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("creating pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}
	return &DB{Pool: pool}, nil
}

// InTx runs fn inside a transaction, committing on success and rolling back on
// error or panic.
func (db *DB) InTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		// Rollback after a successful commit is a no-op, so this is safe to always run.
		_ = tx.Rollback(context.WithoutCancel(ctx))
	}()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}
	return nil
}

// InTenantTx runs fn inside a transaction scoped to one tenant.
//
// SET LOCAL is what activates the row-level security policies, so every query
// touching tenant data must go through here. Forgetting it makes queries return
// nothing rather than return someone else's rows -- a failure mode chosen
// deliberately over the alternative.
func (db *DB) InTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(pgx.Tx) error) error {
	return db.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID.String()); err != nil {
			return fmt.Errorf("setting tenant scope: %w", err)
		}
		return fn(tx)
	})
}

// IsUniqueViolation reports whether err is a unique constraint violation,
// optionally narrowed to a specific constraint name.
//
// Insert-and-catch is the only race-free way to claim a unique value; a prior
// SELECT can always be overtaken between check and write.
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != UniqueViolation {
		return false
	}
	return constraint == "" || pgErr.ConstraintName == constraint
}

// IsNoRows reports whether err means "query matched nothing".
func IsNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
