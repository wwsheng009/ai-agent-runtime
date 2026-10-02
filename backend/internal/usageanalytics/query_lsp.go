package usageanalytics

import (
	"fmt"
	"strings"
	"time"
)

// LSP 基线的库内事实源（§4.3）。
//
// 这一层只负责"把库里的行读成结构体"，**不含任何口径**。口径在
// internal/lsp/baseline.Aggregate，那是唯一的实现，日志源与 SQL 源共用。
// 分层是刻意的：口径一旦分叉，同一指标就会有两套数字，而看板无法分辨该信哪个。

// LSPRow 是 usage_lsp_requests 的一行投影。
type LSPRow struct {
	SessionID           string
	PathFingerprint     string
	Server              string
	StartedAt           time.Time
	Trigger             string
	Outcome             string
	DurationMS          int
	DiagCount           int
	TotalDiagCount      int
	NewDiagCount        int
	AppendedBytes       int
	AppendedDiagBytes   int
	AppendedNoteBytes   int
	AppendedEmptyBytes  int
	OmittedItems        int
	OmittedByChars      int
	AttemptedMembers    int
	ColdProbeClassified bool
	ColdFastFail        bool
	DiagFingerprint     string
	ReasonCategory      string
}

// LSPFirstPublishRow 是 usage_lsp_first_publish 的一行投影。
type LSPFirstPublishRow struct {
	SessionID      string
	Server         string
	FirstPublishMS int
	ObservedAt     time.Time
}

// LSPEditCallRow 是一次编辑类工具调用对覆盖率/追加比的贡献。
type LSPEditCallRow struct {
	SessionID   string
	OutputBytes int
}

// lspWindowClause 把 since 翻成 SQL 片段与参数；零值 since = 全窗口。
func lspWindowClause(column string, since time.Time) (string, []interface{}) {
	if since.IsZero() {
		return "", nil
	}
	return " AND " + column + " >= ?", []interface{}{since.UnixNano()}
}

// LSPBaselineRequests 读出窗口内的全部 LSP 后写请求。
//
// 排序与日志路径对齐（started_unix_nano, path_fingerprint, server）：closure 的
// sort.SliceStable 对并列时间戳保留输入序，输入序必须确定，否则同样的数据可能
// 给出不同的 closure 计数。
func (s *Store) LSPBaselineRequests(since time.Time) ([]LSPRow, error) {
	where, args := lspWindowClause("started_unix_nano", since)
	rows, ok, err := s.query(`
SELECT session_id, path_fingerprint, server, started_unix_nano,
       trigger, outcome, duration_ms, diag_count, total_diag_count, new_diag_count,
       appended_bytes, appended_diag_bytes, appended_note_bytes, appended_empty_bytes,
       omitted_items, omitted_by_chars, attempted_members,
       cold_probe_classified, cold_fast_fail, diag_fingerprint, reason_category
FROM usage_lsp_requests
WHERE 1=1`+where+`
ORDER BY started_unix_nano, path_fingerprint, server`, args...)
	if err != nil {
		return nil, fmt.Errorf("query usage_lsp_requests: %w", err)
	}
	if !ok {
		return nil, nil
	}
	defer func() { _ = rows.Close() }()
	var out []LSPRow
	for rows.Next() {
		var row LSPRow
		var started int64
		if err := rows.Scan(
			&row.SessionID, &row.PathFingerprint, &row.Server, &started,
			&row.Trigger, &row.Outcome, &row.DurationMS, &row.DiagCount,
			&row.TotalDiagCount, &row.NewDiagCount,
			&row.AppendedBytes, &row.AppendedDiagBytes, &row.AppendedNoteBytes,
			&row.AppendedEmptyBytes, &row.OmittedItems, &row.OmittedByChars,
			&row.AttemptedMembers, &row.ColdProbeClassified, &row.ColdFastFail,
			&row.DiagFingerprint, &row.ReasonCategory,
		); err != nil {
			return nil, fmt.Errorf("scan usage_lsp_requests: %w", err)
		}
		row.StartedAt = time.Unix(0, started).UTC()
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage_lsp_requests: %w", err)
	}
	return out, nil
}

// LSPBaselineFirstPublish 读出窗口内的冷启动首发布观测。
func (s *Store) LSPBaselineFirstPublish(since time.Time) ([]LSPFirstPublishRow, error) {
	where, args := lspWindowClause("observed_unix_nano", since)
	rows, ok, err := s.query(`
SELECT session_id, server, first_publish_ms, observed_unix_nano
FROM usage_lsp_first_publish
WHERE 1=1`+where+`
ORDER BY observed_unix_nano, session_id, server`, args...)
	if err != nil {
		return nil, fmt.Errorf("query usage_lsp_first_publish: %w", err)
	}
	if !ok {
		return nil, nil
	}
	defer func() { _ = rows.Close() }()
	var out []LSPFirstPublishRow
	for rows.Next() {
		var row LSPFirstPublishRow
		var observed int64
		if err := rows.Scan(&row.SessionID, &row.Server, &row.FirstPublishMS, &observed); err != nil {
			return nil, fmt.Errorf("scan usage_lsp_first_publish: %w", err)
		}
		row.ObservedAt = time.Unix(0, observed).UTC()
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage_lsp_first_publish: %w", err)
	}
	return out, nil
}

// LSPBaselineEditCalls 读出窗口内编辑类工具调用的次数与输出字节。
//
// 覆盖率的**分母**与追加比的分子都来自这里（tool.completed 早已入库），所以切库
// 后这些读数不依赖日志。
//
// 输出字节的取值顺序与日志路径逐字一致：先 output_model_visible_bytes，为 0 时
// 回退 output_original_bytes。顺序反了会让追加比整体偏移。
func (s *Store) LSPBaselineEditCalls(since time.Time, editingTools []string) ([]LSPEditCallRow, error) {
	if len(editingTools) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(editingTools)), ",")
	where, args := lspWindowClause("completed_at_unix_nano", since)
	full := "SELECT COALESCE(session_id, ''), " + `
       COALESCE(json_extract(record_json, '$.output_model_visible_bytes'),
                json_extract(record_json, '$.output_original_bytes'), 0) AS output_bytes
FROM usage_tool_calls
WHERE tool_name IN (` + placeholders + `)` + where
	// 参数顺序必须与 SQL 里的占位符顺序一致：先是 IN 列表，再是 since。
	bound := make([]interface{}, 0, len(editingTools)+len(args))
	for _, tool := range editingTools {
		bound = append(bound, tool)
	}
	args = append(bound, args...)
	rows, ok, err := s.query(full, args...)
	if err != nil {
		return nil, fmt.Errorf("query usage_tool_calls editing: %w", err)
	}
	if !ok {
		return nil, nil
	}
	defer func() { _ = rows.Close() }()
	var out []LSPEditCallRow
	for rows.Next() {
		var row LSPEditCallRow
		if err := rows.Scan(&row.SessionID, &row.OutputBytes); err != nil {
			return nil, fmt.Errorf("scan usage_tool_calls editing: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage_tool_calls editing: %w", err)
	}
	return out, nil
}
