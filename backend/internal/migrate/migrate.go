package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// Migration describes a schema migration.
type Migration struct {
	Version int
	Name    string
	UpSQL   string
}

// ErrSchemaNewer 表示库里的 schema 版本高于本二进制已知的最新迁移——库是
// **更新的二进制**写下的，降级打开不安全（旧代码不认识新列/新表，会在任意
// 读写上失败，甚至写坏新结构）。
var ErrSchemaNewer = errors.New("schema is newer than this binary")

// LatestVersion 返回迁移集中的最高版本号（空集返回 0）。
func LatestVersion(migrations []Migration) int {
	latest := 0
	for _, m := range migrations {
		if m.Version > latest {
			latest = m.Version
		}
	}
	return latest
}

// Apply runs migrations against the given database.
//
// 已应用版本**高于**本二进制已知最新版本时拒绝执行（04 §5 Phase 5 交付 6 /
// 风险 R12"版本拒绝：新 DB 不被旧代码打开"）：这是所有 store 的公共收口点，
// 版本拒绝在这里做一次，六个 store 一起受益。
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
	if maxApplied := maxVersion(applied); maxApplied > LatestVersion(migrations) {
		return fmt.Errorf(
			"%w: database schema v%d is newer than this binary (v%d); refusing to open — use the newer binary or restore a backup",
			ErrSchemaNewer, maxApplied, LatestVersion(migrations))
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

// maxVersion 返回已应用版本集合中的最大值（空集返回 0）。
func maxVersion(applied map[int]bool) int {
	max := 0
	for version := range applied {
		if version > max {
			max = version
		}
	}
	return max
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
	// 迁移的 UpSQL 可能包含读后写语句：IMMEDIATE 避免并发写者把 deferred
	// 读快照顶成 SQLITE_BUSY_SNAPSHOT/517（迁移失败会中断启动）。
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
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
