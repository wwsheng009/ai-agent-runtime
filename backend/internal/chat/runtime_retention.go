package chat

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// RuntimeRetentionOptions 是离线 runtime 库清理的保留预算。
//
// 零值取与在线写入一致的默认值；负数没有“无限保留”语义，仍按默认值处理，
// 避免一条误配置让清理命令变成无操作。
type RuntimeRetentionOptions struct {
	ToolReceiptRetention  int
	ToolReceiptMaxBytes   int64
	CacheRequestRetention int
	EventRetention        int
	DryRun                bool
}

// RuntimeRetentionStats 是一次离线清理的可审计结果。
type RuntimeRetentionStats struct {
	SessionsScanned                    int64 `json:"sessions_scanned"`
	ToolReceiptsDeleted                int64 `json:"tool_receipts_deleted"`
	ToolReceiptBytesDeleted            int64 `json:"tool_receipt_bytes_deleted"`
	CacheRequestsDeleted               int64 `json:"cache_requests_deleted"`
	EventsDeleted                      int64 `json:"events_deleted"`
	EventPayloadBytesDeleted           int64 `json:"event_payload_bytes_deleted"`
	LegacyInlineSurfacesCompacted      int64 `json:"legacy_inline_surfaces_compacted"`
	LegacyInlineSurfaceBytesFreed      int64 `json:"legacy_inline_surface_bytes_freed"`
	RedundantSessionEventsIndexDropped bool  `json:"redundant_session_events_index_dropped"`
}

type runtimeRetentionQueryer interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

func normalizeRuntimeRetentionOptions(opts RuntimeRetentionOptions) RuntimeRetentionOptions {
	if opts.ToolReceiptRetention <= 0 {
		opts.ToolReceiptRetention = defaultRuntimeToolReceiptRetention
	}
	if opts.ToolReceiptMaxBytes <= 0 {
		opts.ToolReceiptMaxBytes = defaultRuntimeToolReceiptMaxBytes
	}
	if opts.CacheRequestRetention <= 0 {
		opts.CacheRequestRetention = defaultRuntimeCacheRequestRetention
	}
	if opts.EventRetention <= 0 {
		opts.EventRetention = defaultRuntimeEventRetention
	}
	return opts
}

// PruneRuntimeRetention 对已有 runtime 库做一次在线写入路径同口径的裁剪。
//
// 它用于清理“策略生效前已经写入”或“会话长期不再写入、无法触发在线裁剪”的
// 历史行。函数不执行 VACUUM；释放出的页进入 freelist，需要再运行
// `aicli storage compact --target runtime` 归还给操作系统。
func PruneRuntimeRetention(ctx context.Context, db *sql.DB, opts RuntimeRetentionOptions) (RuntimeRetentionStats, error) {
	if db == nil {
		return RuntimeRetentionStats{}, fmt.Errorf("runtime store database is nil")
	}
	opts = normalizeRuntimeRetentionOptions(opts)
	var stats RuntimeRetentionStats
	seenSessions := make(map[string]struct{})
	if !opts.DryRun {
		var indexCount int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_session_events_session_seq'`,
		).Scan(&indexCount); err != nil {
			return stats, fmt.Errorf("check redundant session_events index: %w", err)
		}
		if indexCount > 0 {
			if _, err := db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_session_events_session_seq`); err != nil {
				return stats, fmt.Errorf("drop redundant session_events index: %w", err)
			}
			stats.RedundantSessionEventsIndexDropped = true
		}
	}

	receiptSessions, err := runtimeDistinctSessions(ctx, db, "session_tool_receipts")
	if err != nil {
		return stats, err
	}
	for _, sessionID := range receiptSessions {
		seenSessions[sessionID] = struct{}{}
		deleted, bytes, err := pruneToolReceipts(ctx, db, sessionID, "", opts.ToolReceiptRetention, opts.ToolReceiptMaxBytes, opts.DryRun)
		if err != nil {
			return stats, fmt.Errorf("prune tool receipts for %s: %w", sessionID, err)
		}
		stats.ToolReceiptsDeleted += deleted
		stats.ToolReceiptBytesDeleted += bytes
	}

	cacheSessions, err := runtimeDistinctSessions(ctx, db, "cache_requests")
	if err != nil {
		return stats, err
	}
	for _, sessionID := range cacheSessions {
		seenSessions[sessionID] = struct{}{}
		deleted, err := pruneCacheRequests(ctx, db, sessionID, opts.CacheRequestRetention, opts.DryRun)
		if err != nil {
			return stats, fmt.Errorf("prune cache requests for %s: %w", sessionID, err)
		}
		stats.CacheRequestsDeleted += deleted
	}

	eventSessions, err := runtimeDistinctSessions(ctx, db, "session_events")
	if err != nil {
		return stats, err
	}
	for _, sessionID := range eventSessions {
		seenSessions[sessionID] = struct{}{}
		deleted, bytes, err := pruneSessionEvents(ctx, db, sessionID, opts.EventRetention, opts.DryRun)
		if err != nil {
			return stats, fmt.Errorf("prune session events for %s: %w", sessionID, err)
		}
		stats.EventsDeleted += deleted
		stats.EventPayloadBytesDeleted += bytes
	}

	compacted, freed, err := compactLegacyToolSurfaces(ctx, db, opts.DryRun)
	if err != nil {
		return stats, err
	}
	stats.LegacyInlineSurfacesCompacted = compacted
	stats.LegacyInlineSurfaceBytesFreed = freed
	stats.SessionsScanned = int64(len(seenSessions))
	return stats, nil
}

func runtimeDistinctSessions(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	if ok, err := runtimeTableExists(ctx, db, table); err != nil {
		return nil, err
	} else if !ok {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT DISTINCT session_id FROM %s WHERE session_id <> ''`, table))
	if err != nil {
		return nil, fmt.Errorf("list sessions from %s: %w", table, err)
	}
	defer rows.Close()
	var sessions []string
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			return nil, fmt.Errorf("scan session from %s: %w", table, err)
		}
		sessions = append(sessions, strings.TrimSpace(sessionID))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions from %s: %w", table, err)
	}
	return sessions, nil
}

func runtimeTableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
		return false, fmt.Errorf("check runtime table %s: %w", table, err)
	}
	return count > 0, nil
}

type runtimeReceiptRow struct {
	rowID      int64
	toolCallID string
	bytes      int64
}

// pruneToolReceipts 保留单会话最新的若干条回执，并满足总字节预算。
//
// 不删除最后一条回执：即使单条输出本身超过字节预算，也保留现场供崩溃恢复。
// keepToolCallID 是刚写入的当前回执，任何情况下都受到保护。
func pruneToolReceipts(
	ctx context.Context,
	q runtimeRetentionQueryer,
	sessionID string,
	keepToolCallID string,
	retention int,
	maxBytes int64,
	dryRun bool,
) (int64, int64, error) {
	if retention <= 0 && maxBytes <= 0 {
		return 0, 0, nil
	}
	rows, err := q.QueryContext(ctx, `
		SELECT rowid, tool_call_id, length(CAST(message_json AS BLOB))
		FROM session_tool_receipts
		WHERE session_id = ?
		ORDER BY created_at_unix_nano ASC, created_at ASC, tool_call_id ASC
	`, sessionID)
	if err != nil {
		return 0, 0, fmt.Errorf("list tool receipts for pruning: %w", err)
	}
	var candidates []runtimeReceiptRow
	var totalBytes int64
	for rows.Next() {
		var row runtimeReceiptRow
		if err := rows.Scan(&row.rowID, &row.toolCallID, &row.bytes); err != nil {
			_ = rows.Close()
			return 0, 0, fmt.Errorf("scan tool receipt for pruning: %w", err)
		}
		candidates = append(candidates, row)
		totalBytes += row.bytes
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, 0, fmt.Errorf("iterate tool receipts for pruning: %w", err)
	}
	_ = rows.Close()

	remainingCount := int64(len(candidates))
	remainingBytes := totalBytes
	if remainingCount <= int64(retention) && remainingBytes <= maxBytes {
		return 0, 0, nil
	}

	var deleteIDs []int64
	var deletedBytes int64
	for _, candidate := range candidates {
		if remainingCount <= 1 {
			break
		}
		if remainingCount <= int64(retention) && remainingBytes <= maxBytes {
			break
		}
		if keepToolCallID != "" && candidate.toolCallID == keepToolCallID {
			continue
		}
		deleteIDs = append(deleteIDs, candidate.rowID)
		remainingCount--
		remainingBytes -= candidate.bytes
		deletedBytes += candidate.bytes
	}
	if len(deleteIDs) == 0 {
		return 0, 0, nil
	}
	if dryRun {
		return int64(len(deleteIDs)), deletedBytes, nil
	}
	if err := deleteRowsByRowID(ctx, q, "session_tool_receipts", deleteIDs); err != nil {
		return 0, 0, err
	}
	return int64(len(deleteIDs)), deletedBytes, nil
}

// pruneCacheRequests 保留单会话最新的 retention 条缓存请求。
// 缓存请求是分析明细，不承载回放现场，因此按时间顺序直接删旧行。
func pruneCacheRequests(ctx context.Context, q runtimeRetentionQueryer, sessionID string, retention int, dryRun bool) (int64, error) {
	if retention <= 0 {
		return 0, nil
	}
	var count int64
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM cache_requests WHERE session_id = ?`, sessionID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count cache requests for pruning: %w", err)
	}
	excess := count - int64(retention)
	if excess <= 0 {
		return 0, nil
	}
	if dryRun {
		return excess, nil
	}
	result, err := q.ExecContext(ctx, `
		DELETE FROM cache_requests
		WHERE session_id = ?
		  AND rowid IN (
			SELECT rowid FROM cache_requests
			WHERE session_id = ?
			ORDER BY started_at_unix_nano ASC, created_at ASC, llm_request_id ASC
			LIMIT ?
		  )
	`, sessionID, sessionID, excess)
	if err != nil {
		return 0, fmt.Errorf("delete old cache requests: %w", err)
	}
	deleted, _ := result.RowsAffected()
	return deleted, nil
}

// pruneSessionEvents 保留单会话最近的 retention 个 seq；旧行释放出的页由离线
// compaction 回收。这里只处理 seq 水位，不尝试跨会话按时间裁剪。
func pruneSessionEvents(ctx context.Context, q runtimeRetentionQueryer, sessionID string, retention int, dryRun bool) (int64, int64, error) {
	if retention <= 0 {
		return 0, 0, nil
	}
	var maxSeq int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM session_events WHERE session_id = ?`, sessionID).Scan(&maxSeq); err != nil {
		return 0, 0, fmt.Errorf("read event high-water mark: %w", err)
	}
	if maxSeq <= int64(retention) {
		return 0, 0, nil
	}
	cutoff := maxSeq - int64(retention)
	var rows int64
	var bytes int64
	if err := q.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(length(CAST(payload_json AS BLOB))), 0)
		FROM session_events
		WHERE session_id = ? AND seq <= ?
	`, sessionID, cutoff).Scan(&rows, &bytes); err != nil {
		return 0, 0, fmt.Errorf("measure prunable events: %w", err)
	}
	if rows == 0 {
		return 0, 0, nil
	}
	if dryRun {
		return rows, bytes, nil
	}
	result, err := q.ExecContext(ctx, `
		DELETE FROM session_events
		WHERE session_id = ? AND seq <= ?
	`, sessionID, cutoff)
	if err != nil {
		return 0, 0, fmt.Errorf("delete old session events: %w", err)
	}
	deleted, _ := result.RowsAffected()
	return deleted, bytes, nil
}

func deleteRowsByRowID(ctx context.Context, q runtimeRetentionQueryer, table string, rowIDs []int64) error {
	const chunkSize = 256
	for start := 0; start < len(rowIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(rowIDs) {
			end = len(rowIDs)
		}
		placeholders := make([]string, 0, end-start)
		args := make([]interface{}, 0, end-start)
		for _, rowID := range rowIDs[start:end] {
			placeholders = append(placeholders, "?")
			args = append(args, rowID)
		}
		query := fmt.Sprintf("DELETE FROM %s WHERE rowid IN (%s)", table, strings.Join(placeholders, ","))
		if _, err := q.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("delete retained %s rows: %w", table, err)
		}
	}
	return nil
}

type legacyToolSurfaceRow struct {
	sessionID string
	ref       string
	payload   []byte
}

// compactLegacyToolSurfaces 把历史内联工具面迁到内容寻址表，并清空内联列。
// 这是纯空间优化：读侧原本就优先 ref、回退 inline；迁移后同一内容只保留一份。
func compactLegacyToolSurfaces(ctx context.Context, db *sql.DB, dryRun bool) (int64, int64, error) {
	if ok, err := runtimeTableExists(ctx, db, "session_runtime_state"); err != nil {
		return 0, 0, err
	} else if !ok {
		return 0, 0, nil
	}
	if ok, err := runtimeTableExists(ctx, db, "runtime_tool_surface_content"); err != nil {
		return 0, 0, err
	} else if !ok {
		return 0, 0, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT session_id, COALESCE(stable_tool_surface_ref, ''), stable_tool_surface_json
		FROM session_runtime_state
		WHERE stable_tool_surface_json IS NOT NULL
	`)
	if err != nil {
		return 0, 0, fmt.Errorf("list legacy inline tool surfaces: %w", err)
	}
	var legacy []legacyToolSurfaceRow
	for rows.Next() {
		var row legacyToolSurfaceRow
		if err := rows.Scan(&row.sessionID, &row.ref, &row.payload); err != nil {
			_ = rows.Close()
			return 0, 0, fmt.Errorf("scan legacy inline tool surface: %w", err)
		}
		legacy = append(legacy, row)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, 0, fmt.Errorf("iterate legacy inline tool surfaces: %w", err)
	}
	_ = rows.Close()

	var compacted int64
	var freed int64
	for _, row := range legacy {
		ref := strings.TrimSpace(row.ref)
		if dryRun {
			if ref != "" || len(row.payload) > 0 {
				compacted++
				freed += int64(len(row.payload))
			}
			continue
		}
		if ref == "" && len(row.payload) > 0 {
			sum := sha256.Sum256(row.payload)
			ref = hex.EncodeToString(sum[:])
			if _, err := db.ExecContext(ctx, `
				INSERT INTO runtime_tool_surface_content (fingerprint, payload_json, byte_count, created_at)
				VALUES (?, ?, ?, ?)
				ON CONFLICT(fingerprint) DO NOTHING
			`, ref, row.payload, len(row.payload), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				return compacted, freed, fmt.Errorf("store legacy tool surface content: %w", err)
			}
		}
		if ref == "" {
			continue
		}
		result, err := db.ExecContext(ctx, `
			UPDATE session_runtime_state
			SET stable_tool_surface_json = NULL,
			    stable_tool_surface_ref = ?
			WHERE session_id = ? AND stable_tool_surface_json IS NOT NULL
		`, ref, row.sessionID)
		if err != nil {
			return compacted, freed, fmt.Errorf("clear legacy inline tool surface: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected > 0 {
			compacted++
			freed += int64(len(row.payload))
		}
	}
	return compacted, freed, nil
}
