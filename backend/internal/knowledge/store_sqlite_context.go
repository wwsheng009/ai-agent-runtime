package knowledge

// context_snapshots / context_items 的 SQLite 读写（06 §4 Phase 6 切片 5）。
//
// 不变量：
//   - 写路径统一走 execWrite（IMMEDIATE 事务 + 锁重试）：快照与其全部 items 在
//     同一事务落库，不会出现"有快照没条目"的中间态；reader 角色硬失败
//     ErrReadOnlyStore（ContextRecorder 会静默停用，不再重试）；
//   - 写入 append-only + 幂等：确定性主键 + ON CONFLICT DO NOTHING，重复记录
//     同一次编译不会产生重复行；
//   - 读路径只用 s.db（reader 可用）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 编译期断言：sqliteStore 直接满足快照落库与审计读的窄接口。
var (
	_ ContextSnapshotStore  = (*sqliteStore)(nil)
	_ ContextSnapshotReader = (*sqliteStore)(nil)
)

// RecordContextSnapshot 在一个事务内写入快照及其注入条目。
func (s *sqliteStore) RecordContextSnapshot(ctx context.Context, rec ContextSnapshotRecord) error {
	if s == nil || s.db == nil {
		return errors.New("knowledge: record context snapshot: store is not open")
	}
	if err := rec.Validate(); err != nil {
		return fmt.Errorf("knowledge: record context snapshot: %w", err)
	}
	if strings.TrimSpace(rec.ID) == "" {
		rec.ID = ContextSnapshotID(rec.SessionID, rec.TaskID, rec.KnowledgeVersion, rec.CompilerVersion, rec.Items)
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now()
	}
	err := s.execWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO context_snapshots
				(id, session_id, task_id, workspace_id, knowledge_version, compiler_version, budget_json, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO NOTHING
		`,
			rec.ID, rec.SessionID, nullableText(rec.TaskID), nullableText(rec.WorkspaceID),
			nullableText(rec.KnowledgeVersion), rec.CompilerVersion, nullableText(rec.BudgetJSON),
			unixMillis(rec.CreatedAt)); err != nil {
			return err
		}
		for _, item := range rec.Items {
			itemID := item.ID
			if strings.TrimSpace(itemID) == "" {
				itemID = ContextItemID(rec.ID, item.ItemType, item.RefID)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO context_items
					(id, snapshot_id, item_type, ref_id, source, trust, tokens, reason, stale,
					 version, confidence, tier, provisional, explanation)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(id) DO NOTHING
			`,
				itemID, rec.ID, item.ItemType, nullableText(item.RefID), item.Source, item.Trust,
				item.Tokens, nullableText(item.Reason), boolToInt(item.Stale),
				nullableText(item.Version), item.Confidence, nullableText(item.Tier),
				boolToInt(item.Provisional), nullableText(item.Explanation)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("knowledge: record context snapshot: %w", err)
	}
	return nil
}

// ContextSnapshotsBySession 按会话读取最近快照（created_at 降序；limit<=0 取 20）。
// 不加载 items——需要明细时再调 ContextItemsBySnapshot。
func (s *sqliteStore) ContextSnapshotsBySession(ctx context.Context, sessionID string, limit int) ([]ContextSnapshotRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("knowledge: context snapshots: store is not open")
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("knowledge: context snapshots: session_id is required")
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, COALESCE(task_id, ''), COALESCE(workspace_id, ''),
		       COALESCE(knowledge_version, ''), compiler_version, COALESCE(budget_json, ''), created_at
		FROM context_snapshots
		WHERE session_id = ?
		ORDER BY created_at DESC, id ASC
		LIMIT ?
	`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("knowledge: context snapshots: %w", err)
	}
	defer rows.Close()

	out := make([]ContextSnapshotRecord, 0)
	for rows.Next() {
		var (
			rec       ContextSnapshotRecord
			createdMS int64
		)
		if err := rows.Scan(&rec.ID, &rec.SessionID, &rec.TaskID, &rec.WorkspaceID,
			&rec.KnowledgeVersion, &rec.CompilerVersion, &rec.BudgetJSON, &createdMS); err != nil {
			return nil, fmt.Errorf("knowledge: context snapshots: scan: %w", err)
		}
		rec.CreatedAt = timeFromUnixMillis(createdMS)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("knowledge: context snapshots: %w", err)
	}
	return out, nil
}

// ContextItemsBySnapshot 读取一个快照的全部条目（按 id 升序，稳定可复算）。
func (s *sqliteStore) ContextItemsBySnapshot(ctx context.Context, snapshotID string) ([]ContextItemRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("knowledge: context items: store is not open")
	}
	if strings.TrimSpace(snapshotID) == "" {
		return nil, errors.New("knowledge: context items: snapshot_id is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, item_type, COALESCE(ref_id, ''), source, trust, tokens, COALESCE(reason, ''),
		       stale, COALESCE(version, ''), confidence, COALESCE(tier, ''), provisional, COALESCE(explanation, '')
		FROM context_items
		WHERE snapshot_id = ?
		ORDER BY id ASC
	`, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("knowledge: context items: %w", err)
	}
	defer rows.Close()

	out := make([]ContextItemRecord, 0)
	for rows.Next() {
		var (
			item        ContextItemRecord
			stale       int
			provisional int
		)
		if err := rows.Scan(&item.ID, &item.ItemType, &item.RefID, &item.Source, &item.Trust,
			&item.Tokens, &item.Reason, &stale, &item.Version, &item.Confidence, &item.Tier,
			&provisional, &item.Explanation); err != nil {
			return nil, fmt.Errorf("knowledge: context items: scan: %w", err)
		}
		item.Stale = stale != 0
		item.Provisional = provisional != 0
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("knowledge: context items: %w", err)
	}
	return out, nil
}
