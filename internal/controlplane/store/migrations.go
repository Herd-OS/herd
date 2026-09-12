package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const migrationsTable = "controlplane_schema_migrations"

// ApplyMigrations applies embedded Postgres migrations inside transactions.
// Service startup should normally call ValidateMigrations and fail when schema
// files have not been applied; automatic migration is available only through
// WithMigrateOnStart for deployments that intentionally own schema changes.
func ApplyMigrations(ctx context.Context, db *sql.DB) error {
	names, err := migrationNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := applyMigration(ctx, db, name); err != nil {
			return err
		}
	}
	return nil
}

// ValidateMigrations returns an error if any embedded migration has not been
// recorded in controlplane_schema_migrations.
func ValidateMigrations(ctx context.Context, db *sql.DB) error {
	names, err := migrationNames()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "SELECT 1 FROM "+migrationsTable+" LIMIT 1"); err != nil {
		return fmt.Errorf("control-plane migrations are not applied: %w", err)
	}
	applied := map[string]bool{}
	rows, err := db.QueryContext(ctx, "SELECT name FROM "+migrationsTable)
	if err != nil {
		return fmt.Errorf("query applied migrations: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("scan applied migration: %w", err)
		}
		applied[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate applied migrations: %w", err)
	}
	for _, name := range names {
		if !applied[name] {
			return fmt.Errorf("control-plane migration %q is not applied", name)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, name string) error {
	body, err := migrationFiles.ReadFile(filepath.ToSlash(filepath.Join("migrations", name)))
	if err != nil {
		return fmt.Errorf("read migration %s: %w", name, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if _, err := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+migrationsTable+" (name TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())"); err != nil {
		return fmt.Errorf("ensure migrations table: %w", err)
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM "+migrationsTable+" WHERE name = $1)", name).Scan(&exists); err != nil {
		return fmt.Errorf("check migration %s: %w", name, err)
	}
	if exists {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO "+migrationsTable+" (name) VALUES ($1)", name); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}
	return tx.Commit()
}

func migrationNames() ([]string, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, errors.New("no embedded control-plane migrations found")
	}
	return names, nil
}
