package usageanalytics

import (
	"database/sql"
	"fmt"
	"time"
)

// ============================================================================
// 保留期清理（方案 §10 / §13 Phase 4）：usage_requests 与 turn 去重键按时间窗
// 清理，并重建受影响会话的预聚合统计；可选 VACUUM 回收文件空间。
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
		return rebuildSessionStatsTx(tx, "")
	}); err != nil {
		return 0, err
	}
	s.statsGen.Add(1)
	return deleted, nil
}

// Vacuum 执行 VACUUM 回收已删除数据占用的文件空间（不能在事务内执行）。
func (s *Store) Vacuum() error {
	if s == nil || s.db == nil || s.empty || s.readOnly {
		return nil
	}
	if _, err := s.db.Exec("VACUUM"); err != nil {
		return fmt.Errorf("vacuum usage analytics db: %w", err)
	}
	return nil
}
