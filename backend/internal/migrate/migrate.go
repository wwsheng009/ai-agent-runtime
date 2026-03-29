package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Migration describes a schema migration.
type Migration struct {
	Version int
	Name    string
	UpSQL   string
}

// Apply runs migrations against the given database.
func Apply(ctx context.Context, db *sql.DB, migrations []Migration) error {
	if db == nil {
		return fmt.Errorf("database is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ensureTable(ctx, db); err != nil {
		return err
	}
	applied, err := loadApplied(ctx, db)
	if err != nil {
		return err
	}
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	for _, mig := range migrations {
		if mig.Version <= 0 || strings.TrimSpace(mig.UpSQL) == "" {
			continue
		}
		if applied[mig.Version] {
			continue
		}
		if err := applyOne(ctx, db, mig); err != nil {
			return err
		}
	}
	return nil
}

func ensureTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	return nil
}

func loadApplied(ctx context.Context, db *sql.DB) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("query schema_migrations: %w", err)
	}
	defer rows.Close()
	applied := make(map[int]bool)
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[version] = true
	}
	return applied, rows.Err()
}

func applyOne(ctx context.Context, db *sql.DB, mig Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", mig.Version, err)
	}
	if _, err := tx.ExecContext(ctx, mig.UpSQL); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("apply migration %d (%s): %w", mig.Version, mig.Name, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO schema_migrations (version, name, applied_at)
		VALUES (?, ?, ?)
	`, mig.Version, mig.Name, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("record migration %d (%s): %w", mig.Version, mig.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", mig.Version, err)
	}
	return nil
}
