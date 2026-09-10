package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
)

// ============================================================================
// cache_requests 镜像表读写（cache.analytics.v1，方案 §375 Phase 3）。
//
// SQLiteRuntimeStore 实现 cacheanalytics.RequestStore（结构化类型，挂载方
// 以接口注入，internal/chat → cacheanalytics 单向依赖，无环）。
//
// 语义：
//   - 终态记录不可变：SaveRequest 按 llm_request_id INSERT OR REPLACE，
//     重复终态事件（幂等回放）不产生重复行；
//   - message id 回填发生在查询期（cacheanalytics.LiveSource + HistoryLookup），
//     不落库；重启后回放的记录由同一查询期路径再次回填；
//   - 会话删除经 DeleteState 级联清理（见 session_runtime_store.go）。
// ============================================================================

// 编译期断言：SQLiteRuntimeStore 必须持续满足 cacheanalytics.RequestStore，
// 挂载方的类型断言（cmd/aicli/commands、internal/api/skills）才能注入成功。
var _ cacheanalytics.RequestStore = (*SQLiteRuntimeStore)(nil)

// SaveRequest 幂等写入一条终态缓存请求记录（cache_requests 镜像表）。
func (s *SQLiteRuntimeStore) SaveRequest(record cacheanalytics.CacheRequestRecord) error {
	if s == nil {
		return fmt.Errorf("runtime store is not initialized")
	}
	record.LLMRequestID = strings.TrimSpace(record.LLMRequestID)
	record.SessionID = strings.TrimSpace(record.SessionID)
	if record.LLMRequestID == "" || record.SessionID == "" {
		return fmt.Errorf("cache request requires llm_request_id and session_id")
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode cache request %s: %w", record.LLMRequestID, err)
	}
	startedNano := int64(0)
	if !record.StartedAt.IsZero() {
		startedNano = record.StartedAt.UnixNano()
	}
	if err := s.ensureCtx(context.Background()); err != nil {
		return err
	}
	_, err = s.db.ExecContext(context.Background(), `
		INSERT OR REPLACE INTO cache_requests
			(llm_request_id, session_id, started_at_unix_nano, record_json, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, record.LLMRequestID, record.SessionID, startedNano, payload, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save cache request %s: %w", record.LLMRequestID, err)
	}
	return nil
}

// LoadSessionRequests 加载指定会话的全部持久化缓存请求记录（started_at 升序）。
// 单行损坏（JSON 解码失败）跳过该行，不拖垮整体回放；会话无记录返回空切片。
func (s *SQLiteRuntimeStore) LoadSessionRequests(sessionID string) ([]cacheanalytics.CacheRequestRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("runtime store is not initialized")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return []cacheanalytics.CacheRequestRecord{}, nil
	}
	if err := s.ensureCtx(context.Background()); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT record_json
		FROM cache_requests
		WHERE session_id = ?
		ORDER BY started_at_unix_nano ASC, llm_request_id ASC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load cache requests for session %s: %w", sessionID, err)
	}
	defer rows.Close()

	records := []cacheanalytics.CacheRequestRecord{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan cache request: %w", err)
		}
		var record cacheanalytics.CacheRequestRecord
		if err := json.Unmarshal(payload, &record); err != nil {
			// 镜像表单行损坏不拖垮整体回放：跳过并继续。
			continue
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("iterate cache requests: %w", err)
	}
	return records, nil
}

// deleteSessionCacheRequests 清理会话的缓存请求镜像行（DeleteState 级联）。
func (s *SQLiteRuntimeStore) deleteSessionCacheRequests(ctx context.Context, sessionID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM cache_requests WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("delete cache requests: %w", err)
	}
	return nil
}
