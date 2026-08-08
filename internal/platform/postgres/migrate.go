package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"sort"
	"strings"
)

// advisoryLockKey serialises migrations across concurrently starting instances.
// The value is arbitrary but must stay stable forever.
const advisoryLockKey = 8_071_144_920_154_001

// Migrate applies every unapplied migration from fsys in filename order.
//
// Each file runs inside its own transaction: a failure leaves the database at the
// last complete migration rather than half-way through one.
func Migrate(ctx context.Context, db *DB, fsys embed.FS, dir string, log *slog.Logger) error {
	conn, err := db.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquiring connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return fmt.Errorf("taking migration lock: %w", err)
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", advisoryLockKey); err != nil {
			log.Error("releasing migration lock", "error", err)
		}
	}()

	const createTable = `
		CREATE SCHEMA IF NOT EXISTS core;
		CREATE TABLE IF NOT EXISTS core.schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`
	if _, err := conn.Exec(ctx, createTable); err != nil {
		return fmt.Errorf("creating migrations table: %w", err)
	}

	applied := make(map[string]struct{})
	rows, err := conn.Query(ctx, "SELECT version FROM core.schema_migrations")
	if err != nil {
		return fmt.Errorf("reading applied migrations: %w", err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("scanning migration row: %w", err)
		}
		applied[v] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating applied migrations: %w", err)
	}

	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return fmt.Errorf("reading migrations dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		version := strings.TrimSuffix(name, ".sql")
		if _, done := applied[version]; done {
			continue
		}
		// path.Join, not string concatenation: embed.FS rejects unclean paths, so
		// a dir of "." would produce "./0001_core.sql" and fail to open.
		body, err := fs.ReadFile(fsys, path.Join(dir, name))
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", name, err)
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("beginning migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			return fmt.Errorf("applying migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO core.schema_migrations (version) VALUES ($1)", version); err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			return fmt.Errorf("recording migration %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("committing migration %s: %w", name, err)
		}
		log.Info("migration applied", "version", version)
	}
	return nil
}
