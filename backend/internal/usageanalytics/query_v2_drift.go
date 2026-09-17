package usageanalytics

import "fmt"

// ============================================================================
// 预聚合列抽样对账（§6.3）：随机抽样 N 个会话，把 c_* 列与 usage_requests
// 实时聚合（旧 sessionSelect 的等价表达式）逐字段对比，回报 stats_drift。
//
// 成本：每个抽样会话走一次 idx_usage_requests_session_started 范围扫描
// （单会话 ≈ 百行量级），/status 可用；漂移修复走 CLI rebuild-stats。
// ============================================================================

// statsDriftSampleSize 每次健康快照抽样的会话数。
const statsDriftSampleSize = 3

// StatsDriftSample 单个字段的偏差样本。
type StatsDriftSample struct {
	SessionID string `json:"session_id"`
	Field     string `json:"field"`
	Stored    int64  `json:"stored"`
	Live      int64  `json:"live"`
}

// StatsDrift 预聚合列抽样对账结果。
type StatsDrift struct {
	Checked int               `json:"checked"`
	Drifted int               `json:"drifted"`
	Samples []StatsDriftSample `json:"samples"`
}

// statsDriftFields 是参与对账的字段：stored 为预聚合列，live 为实时聚合表达式。
var statsDriftFields = []struct {
	name   string
	stored string
	live   string
}{
	{"c_total_requests", "s.c_total_requests", "COALESCE(l.total_requests, 0)"},
	{"c_llm_successes", "s.c_llm_successes", "COALESCE(l.llm_successes, 0)"},
	{"c_llm_errors", "s.c_llm_errors", "COALESCE(l.llm_errors, 0)"},
	{"c_requests_with_usage", "s.c_requests_with_usage", "COALESCE(l.requests_with_usage, 0)"},
	{"c_total_tokens", "s.c_total_tokens", "COALESCE(l.total_tokens, 0)"},
	{"c_prompt_tokens", "s.c_prompt_tokens", "COALESCE(l.prompt_tokens, 0)"},
	{"c_completion_tokens", "s.c_completion_tokens", "COALESCE(l.completion_tokens, 0)"},
	{"c_cached_tokens", "s.c_cached_tokens", "COALESCE(l.cached_tokens, 0)"},
	{"c_reasoning_tokens", "s.c_reasoning_tokens", "COALESCE(l.reasoning_tokens, 0)"},
	{"c_total_duration_ms", "s.c_total_duration_ms", "COALESCE(l.total_duration_ms, 0)"},
	{"c_duration_samples", "s.c_duration_samples", "COALESCE(l.duration_samples, 0)"},
	{"c_turn_count", "s.c_turn_count", "COALESCE(l.turn_count, 0)"},
	{"c_failed_turns", "s.c_failed_turns", "COALESCE(l.failed_turns, 0)"},
	{"c_first_started_at", "s.c_first_started_at", "COALESCE(l.first_started, 0)"},
	{"c_last_started_at", "s.c_last_started_at", "COALESCE(l.last_started, 0)"},
}

// liveSessionStatsSelect 实时聚合（与旧 sessionSelect 的 req CTE 同式）。
const liveSessionStatsSelect = `SELECT
  COUNT(*) AS total_requests,
  SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END) AS llm_successes,
  SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END) AS llm_errors,
  SUM(CASE WHEN usage_available = 1 THEN 1 ELSE 0 END) AS requests_with_usage,
  COALESCE(SUM(total_tokens), 0) AS total_tokens,
  COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
  COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
  COALESCE(SUM(cache_read_tokens), 0) AS cached_tokens,
  COALESCE(SUM(reasoning_tokens), 0) AS reasoning_tokens,
  COALESCE(SUM(duration_ms), 0) AS total_duration_ms,
  SUM(CASE WHEN duration_ms <> 0 THEN 1 ELSE 0 END) AS duration_samples,
  COUNT(DISTINCT ` + statsTurnKeyExpr + `) AS turn_count,
  COUNT(DISTINCT CASE WHEN success = 0 THEN ` + statsTurnKeyExpr + ` END) AS failed_turns,
  COALESCE(MIN(NULLIF(started_at_unix_nano, 0)), 0) AS first_started,
  COALESCE(MAX(started_at_unix_nano), 0) AS last_started
FROM usage_requests WHERE session_id = ?`

// sampleStatsDrift 随机抽样会话做字段级对账；降级/无会话时返回空结果。
func (s *Store) sampleStatsDrift(limit int) *StatsDrift {
	drift := &StatsDrift{Samples: []StatsDriftSample{}}
	if limit <= 0 {
		limit = statsDriftSampleSize
	}
	rows, ok, err := s.query(`SELECT session_id FROM usage_sessions ORDER BY RANDOM() LIMIT ?`, limit)
	if err != nil || !ok {
		return drift
	}
	sessionIDs := make([]string, 0, limit)
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			_ = rows.Close()
			return drift
		}
		sessionIDs = append(sessionIDs, sessionID)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return drift
	}
	drift.Checked = len(sessionIDs)
	for _, sessionID := range sessionIDs {
		mismatches, err := s.compareSessionStats(sessionID)
		if err != nil {
			continue
		}
		if len(mismatches) == 0 {
			continue
		}
		drift.Drifted++
		drift.Samples = append(drift.Samples, mismatches...)
	}
	return drift
}

// compareSessionStats 返回单会话的字段级偏差（无偏差返回空切片）。
func (s *Store) compareSessionStats(sessionID string) ([]StatsDriftSample, error) {
	selects := make([]string, 0, len(statsDriftFields)*2)
	for _, field := range statsDriftFields {
		selects = append(selects, field.stored, field.live)
	}
	sqlText := `SELECT ` + joinComma(selects) + `
FROM usage_sessions s
LEFT JOIN (` + liveSessionStatsSelect + `) l ON 1=1
WHERE s.session_id = ?`
	rows, ok, err := s.query(sqlText, sessionID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("compare session stats: %w", err)
	}
	if !ok {
		return nil, nil
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	stored := make([]int64, len(statsDriftFields))
	live := make([]int64, len(statsDriftFields))
	dest := make([]interface{}, 0, len(statsDriftFields)*2)
	for i := range statsDriftFields {
		dest = append(dest, &stored[i], &live[i])
	}
	if err := rows.Scan(dest...); err != nil {
		return nil, fmt.Errorf("scan session stats comparison: %w", err)
	}
	samples := make([]StatsDriftSample, 0, 4)
	for i, field := range statsDriftFields {
		if stored[i] == live[i] {
			continue
		}
		samples = append(samples, StatsDriftSample{
			SessionID: sessionID,
			Field:     field.name,
			Stored:    stored[i],
			Live:      live[i],
		})
	}
	return samples, nil
}

func joinComma(values []string) string {
	if len(values) == 0 {
		return ""
	}
	out := values[0]
	for _, value := range values[1:] {
		out += ", " + value
	}
	return out
}
