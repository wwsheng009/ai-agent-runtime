package usageanalytics

import (
	"database/sql"
	"fmt"
	"time"
)

// ============================================================================
// 保留期清理（方案 §10 / §13 Phase 4）：usage_requests 与 turn 去重键按时间窗
// 清理，并重建受影响会话的预聚合统计。
//
// 空间回收：本包不提供在线文件级压缩（2026-09 数据库损坏事故后整体移除）；
// 需要时请先退出使用进程，再执行 `aicli storage compact --target analytics`。
//
// 语义边界：
//   - 只删 usage_requests（原始明细）与不再引用的 turn 去重键；
//   - usage_sessions 元数据保留（列表仍可见历史会话，计数按剩余请求重建）；
//   - cutoff 按 started_at_unix_nano 比较，started_at=0 的行不清理（无法判龄）。
// ============================================================================

// PruneBefore 删除 cutoff 之前的请求明细并重建全库预聚合统计。
// 返回删除的请求行数；只读/空库返回错误。
func (s *Store) PruneBefore(cutoff time.Time) (int64, error) {
	if s == nil || s.db == nil || s.empty || s.readOnly {
		return 0, fmt.Errorf("prune usage analytics: store is not writable")
	}
	if cutoff.IsZero() {
		return 0, fmt.Errorf("prune usage analytics: cutoff is required")
	}
	var deleted int64
	if err := s.withWriteTx(func(tx *sql.Tx) error {
		result, err := tx.Exec(`DELETE FROM usage_requests
WHERE started_at_unix_nano > 0 AND started_at_unix_nano < ?`, cutoff.UnixNano())
		if err != nil {
			return fmt.Errorf("prune usage_requests: %w", err)
		}
		deleted, err = result.RowsAffected()
		if err != nil {
			return fmt.Errorf("prune usage_requests rows: %w", err)
		}
		return s.rebuildSessionStatsTx(tx, "")
	}); err != nil {
		return 0, err
	}
	s.statsGen.Add(1)
	return deleted, nil
}
