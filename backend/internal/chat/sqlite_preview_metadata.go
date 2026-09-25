package chat

import (
	"context"
	"fmt"
)

// UpdatePreviewMetadata 只更新 sessions 单行的预览列（title / title_source /
// summary）。不触碰 updated_at、message_count、head_offset 与任何消息表：
// 懒修复不得改变列表排序，也不得让计数偏离真实投影。
//
// 语句自带「值不同才写」条件：列表路径每次渲染都可能调用它，幂等且无写入
// 放大（相同值的重复调用不产生 WAL 帧）。
func (s *SQLiteSessionStorage) UpdatePreviewMetadata(ctx context.Context, sessionID, title, titleSource, summary string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("session storage is not initialized")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sessionID = sanitizeSessionID(sessionID)
	if sessionID == "" {
		return ErrInvalidSession
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE sessions
		SET title = ?, title_source = ?, summary = ?
		WHERE id = ?
		  AND (title IS NOT ? OR title_source IS NOT ? OR summary IS NOT ?)
	`, title, titleSource, summary, sessionID, title, titleSource, summary); err != nil {
		return fmt.Errorf("update session preview metadata: %w", err)
	}
	return nil
}
