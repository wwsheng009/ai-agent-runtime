package knowledge

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// GCDeleted 物理清理 deleted_at 早于 cutoff 的软删除文件及其派生行
// （04 §5 Phase 5 交付 4）。返回本次清理的行数与库大小快照。
//
// 为什么符号必须显式删、不能只靠外键级联：symbols_fts 的同步靠 symbols 上的
// AFTER DELETE 触发器，而 SQLite 的外键级联动作**不触发**触发器——只删 files
// 会留下永远搜得到的幽灵符号。因此顺序固定为：
//
//  1. 显式删 symbols（触发器把 symbols_fts 对应行一并清掉，级联清掉
//     symbol_versions / symbol_aliases）；
//  2. 删 files（级联清掉 refs）。
//
// 悬空引用：refs.to_symbol_id 没有外键（是尽力而为的绑定），指向被清理符号的
// 引用会悬空——读路径按 join / workspace 过滤，不会误报；引用方文件下一次
// 重建时自然修正。
func (s *sqliteStore) GCDeleted(ctx context.Context, workspaceID string, cutoff time.Time) (GCReport, error) {
	report := GCReport{WorkspaceID: workspaceID, Cutoff: cutoff.UTC(), RanAt: time.Now().UTC()}
	if s.readOnly {
		return report, ErrReadOnlyStore
	}
	if ctx == nil {
		ctx = context.Background()
	}
	report.BytesBefore = s.fileSizeBytes()
	cutoffMillis := unixMillis(cutoff)
	err := s.execWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		symbols, err := execRowsAffected(ctx, tx, `
			DELETE FROM symbols
			WHERE workspace_id = ? AND file_id IN (
				SELECT id FROM files
				WHERE workspace_id = ? AND deleted_at IS NOT NULL AND deleted_at <= ?
			)`, workspaceID, workspaceID, cutoffMillis)
		if err != nil {
			return fmt.Errorf("knowledge: gc symbols: %w", err)
		}
		report.Symbols = symbols

		// refs 会被第 2 步的级联删除带走，SQLite 不报告级联行数：先数再删。
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM refs
			WHERE file_id IN (
				SELECT id FROM files
				WHERE workspace_id = ? AND deleted_at IS NOT NULL AND deleted_at <= ?
			)`, workspaceID, cutoffMillis).Scan(&report.Refs); err != nil {
			return fmt.Errorf("knowledge: gc count refs: %w", err)
		}

		files, err := execRowsAffected(ctx, tx, `
			DELETE FROM files
			WHERE workspace_id = ? AND deleted_at IS NOT NULL AND deleted_at <= ?`,
			workspaceID, cutoffMillis)
		if err != nil {
			return fmt.Errorf("knowledge: gc files: %w", err)
		}
		report.Files = files
		return nil
	})
	if err != nil {
		return report, err
	}
	report.BytesAfter = s.fileSizeBytes()
	return report, nil
}

// execRowsAffected 执行写语句并返回受影响行数。
func execRowsAffected(ctx context.Context, tx *sql.Tx, query string, args ...any) (int, error) {
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// fileSizeBytes 返回主库文件大小；内存 DSN 或 stat 失败时为 0。
func (s *sqliteStore) fileSizeBytes() int64 {
	if s == nil || s.path == "" || sqliteutil.IsMemoryDSN(s.path) {
		return 0
	}
	info, err := os.Stat(s.path)
	if err != nil {
		return 0
	}
	return info.Size()
}
