package usageanalytics

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
)

// ============================================================================
// schema v3 写入路径：请求终态 = 同一事务内
//   ① 读取旧行（幂等 delta 的基准）
//   ② UPSERT usage_requests（语义与旧实现完全一致）
//   ③ UPSERT usage_sessions 元数据
//   ④ 会话预聚合计数增量更新（§5.3）
//   ⑤ turn 去重键维护（§5.2）
//
// 重复终态事件（同一 llm_request_id 覆盖写）不会重复计数：delta 按
// "生效后 - 生效前" 计算，重复写同一载荷时 delta 恒为 0。
// ============================================================================

// persistRequestTerminal 写入请求终态：schema v3 库走事务 + 增量维护；
// 旧库/逃生场景回退原有的两条独立 UPSERT。
func (c *collector) persistRequestTerminal(record cacheanalytics.CacheRequestRecord, meta SessionMeta, startedAt, endedAt time.Time) {
	if c == nil || c.store == nil {
		return
	}
	if !c.store.statsColumnsReady() {
		c.upsertRequest(record)
		c.upsertSession(record.SessionID, meta, startedAt, endedAt)
		return
	}
	if err := c.storeRequestTerminalStats(record, meta, startedAt, endedAt); err != nil {
		// 非锁冲突失败：退化为非原子写入，保证原始行不丢；计数漂移由
		// `aicli usage-analytics rebuild-stats` 显式修复（§6.3）。
		c.reportWriteFailure("usage_requests", err)
		c.upsertRequest(record)
		c.upsertSession(record.SessionID, meta, startedAt, endedAt)
	}
}

// requestStatsValues 是与预聚合列一一对应的请求级取值快照。
type requestStatsValues struct {
	sessionID        string
	traceID          string
	turnID           string
	success          int64
	usageAvailable   int64
	promptTokens     int64
	completionTokens int64
	totalTokens      int64
	cacheReadTokens  int64
	reasoningTokens  int64
	durationMS       int64
	firstTokenMS     int64
	startedAtNano    int64
}

// requestStatsValuesFromRecord 归一化新终态记录的计数值。
func requestStatsValuesFromRecord(record cacheanalytics.CacheRequestRecord, startedAt time.Time) requestStatsValues {
	values := requestStatsValues{
		sessionID:     record.SessionID,
		durationMS:    record.DurationMS,
		firstTokenMS:  record.FirstTokenMS,
		startedAtNano: startedAt.UnixNano(),
	}
	if record.Status == cacheanalytics.RequestStatusSuccess {
		values.success = 1
	}
	if record.Usage != nil {
		values.usageAvailable = 1
		values.promptTokens = record.Usage.PromptTokens
		values.completionTokens = record.Usage.CompletionTokens
		values.totalTokens = record.Usage.TotalTokens
		values.cacheReadTokens = record.Usage.CacheReadTokens
		values.reasoningTokens = record.Usage.ReasoningTokens
	}
	return values
}

// readRequestStatsTx 读取请求行当前生效值；不存在时 found=false。
func readRequestStatsTx(tx *sql.Tx, llmRequestID string) (requestStatsValues, bool, error) {
	var values requestStatsValues
	err := tx.QueryRow(`SELECT session_id, trace_id, turn_id, success, usage_available, prompt_tokens, completion_tokens,
       total_tokens, cache_read_tokens, reasoning_tokens, duration_ms, first_token_ms, started_at_unix_nano
FROM usage_requests WHERE llm_request_id = ?`, llmRequestID).Scan(
		&values.sessionID,
		&values.traceID,
		&values.turnID,
		&values.success,
		&values.usageAvailable,
		&values.promptTokens,
		&values.completionTokens,
		&values.totalTokens,
		&values.cacheReadTokens,
		&values.reasoningTokens,
		&values.durationMS,
		&values.firstTokenMS,
		&values.startedAtNano,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return requestStatsValues{}, false, nil
	}
	if err != nil {
		return requestStatsValues{}, false, fmt.Errorf("read usage_requests stats: %w", err)
	}
	return values, true, nil
}

// storeRequestTerminalStats 在单事务内完成 §5.3 的完整写入序列。
func (c *collector) storeRequestTerminalStats(record cacheanalytics.CacheRequestRecord, meta SessionMeta, startedAt, endedAt time.Time) error {
	if c == nil || c.store == nil {
		return nil
	}
	sessionID := strings.TrimSpace(record.SessionID)
	if sessionID == "" {
		return nil
	}
	requestSQL, requestArgs := c.requestUpsertStatement(record)
	sessionSQL, sessionArgs := c.sessionUpsertStatement(sessionID, meta, startedAt, endedAt)
	newValues := requestStatsValuesFromRecord(record, startedAt)
	turnKey := turnKeyForRecord(record)
	failed := int64(0)
	if newValues.success == 0 {
		failed = 1
	}
	if err := c.store.withWriteTx(func(tx *sql.Tx) error {
		old, found, err := readRequestStatsTx(tx, record.LLMRequestID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(requestSQL, requestArgs...); err != nil {
			return fmt.Errorf("upsert usage_requests: %w", err)
		}
		if _, err := tx.Exec(sessionSQL, sessionArgs...); err != nil {
			return fmt.Errorf("upsert usage_sessions: %w", err)
		}
		// 极端场景：同一 llm_request_id 从会话 A 覆盖到会话 B（重试换会话）。
		if found && old.sessionID != "" && old.sessionID != sessionID {
			if err := rollbackSessionStatsTx(tx, old, record.LLMRequestID); err != nil {
				return err
			}
			old, found = requestStatsValues{}, false
		}
		// turn 键变更（同一请求先以 trace/turn 归属、后以回退键覆盖）：
		// 旧键不再被任何请求引用时移除并回退 turn 计数（消除 §5.2 已知偏差）。
		if found {
			oldKey := turnKeyForParts(old.traceID, old.turnID, record.LLMRequestID)
			if oldKey != "" && turnKey != "" && oldKey != turnKey {
				if err := removeStaleTurnKeyTx(tx, sessionID, oldKey, record.LLMRequestID, false); err != nil {
					return err
				}
			}
		}
		if err := applySessionStatsDeltaTx(tx, sessionID, old, found, newValues); err != nil {
			return err
		}
		if turnKey != "" {
			if err := applyTurnKeyTx(tx, sessionID, turnKey, old, found, failed); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	c.store.statsGen.Add(1)
	return nil
}

const sessionDeltaSQL = `UPDATE usage_sessions SET
  c_total_requests      = c_total_requests + ?,
  c_llm_successes       = c_llm_successes + ?,
  c_llm_errors          = c_llm_errors + ?,
  c_requests_with_usage = c_requests_with_usage + ?,
  c_total_tokens        = c_total_tokens + ?,
  c_prompt_tokens       = c_prompt_tokens + ?,
  c_completion_tokens   = c_completion_tokens + ?,
  c_cached_tokens       = c_cached_tokens + ?,
  c_reasoning_tokens    = c_reasoning_tokens + ?,
  c_total_duration_ms   = c_total_duration_ms + ?,
  c_duration_samples    = c_duration_samples + ?,
  c_total_first_token_ms = c_total_first_token_ms + ?,
  c_first_token_samples  = c_first_token_samples + ?,
  c_first_started_at    = CASE
    WHEN ? = 0 THEN c_first_started_at
    WHEN c_first_started_at = 0 THEN ?
    ELSE MIN(c_first_started_at, ?) END,
  c_last_started_at     = MAX(c_last_started_at, ?)
WHERE session_id = ?`

// applySessionStatsDeltaTx 把「生效后 - 生效前」的差值写回会话计数列。
func applySessionStatsDeltaTx(tx *sql.Tx, sessionID string, old requestStatsValues, found bool, newValues requestStatsValues) error {
	totalDelta := int64(0)
	if !found {
		totalDelta = 1
	}
	successDelta := newValues.success
	errorDelta := 1 - newValues.success
	usageDelta := newValues.usageAvailable
	promptDelta := newValues.promptTokens
	completionDelta := newValues.completionTokens
	totalTokensDelta := newValues.totalTokens
	cachedDelta := newValues.cacheReadTokens
	reasoningDelta := newValues.reasoningTokens
	durationDelta := newValues.durationMS
	samplesDelta := nonZeroSample(newValues.durationMS)
	firstTokenDelta := newValues.firstTokenMS
	firstTokenSamplesDelta := nonZeroSample(newValues.firstTokenMS)
	firstCandidate := newValues.startedAtNano
	if found {
		successDelta -= old.success
		errorDelta -= 1 - old.success
		usageDelta -= old.usageAvailable
		promptDelta -= old.promptTokens
		completionDelta -= old.completionTokens
		totalTokensDelta -= old.totalTokens
		cachedDelta -= old.cacheReadTokens
		reasoningDelta -= old.reasoningTokens
		durationDelta -= old.durationMS
		samplesDelta -= nonZeroSample(old.durationMS)
		// 首字时间：明细列的 UPSERT 对已有非零观测有保护（新终态为 0 时不覆盖，
		// 见 requestUpsertStatement），增量必须同语义——否则失败终态会把明细行
		// 仍在的首字观测从 c_total_first_token_ms 里减掉，列值与计数互相漂移。
		effectiveFirstToken := newValues.firstTokenMS
		if effectiveFirstToken == 0 {
			effectiveFirstToken = old.firstTokenMS
		}
		firstTokenDelta = effectiveFirstToken - old.firstTokenMS
		firstTokenSamplesDelta = nonZeroSample(effectiveFirstToken) - nonZeroSample(old.firstTokenMS)
		if old.startedAtNano != 0 {
			// UPSERT 对已有非零 started_at 保持原值（见 requestUpsertStatement）。
			firstCandidate = old.startedAtNano
		}
	}
	if _, err := tx.Exec(sessionDeltaSQL,
		totalDelta, successDelta, errorDelta, usageDelta,
		totalTokensDelta, promptDelta, completionDelta, cachedDelta, reasoningDelta,
		durationDelta, samplesDelta,
		firstTokenDelta, firstTokenSamplesDelta,
		firstCandidate, firstCandidate, firstCandidate,
		firstCandidate,
		sessionID,
	); err != nil {
		return fmt.Errorf("update usage_sessions stats: %w", err)
	}
	return nil
}

// rollbackSessionStatsTx 处理请求跨会话覆盖：把旧会话里该请求的贡献减回去。
// first/last 为 MIN/MAX，无法安全回退，交由 rebuild-stats 修正（§6.3）。
func rollbackSessionStatsTx(tx *sql.Tx, old requestStatsValues, llmRequestID string) error {
	if _, err := tx.Exec(`UPDATE usage_sessions SET
  c_total_requests      = MAX(c_total_requests - 1, 0),
  c_llm_successes       = MAX(c_llm_successes - ?, 0),
  c_llm_errors          = MAX(c_llm_errors - ?, 0),
  c_requests_with_usage = MAX(c_requests_with_usage - ?, 0),
  c_total_tokens        = MAX(c_total_tokens - ?, 0),
  c_prompt_tokens       = MAX(c_prompt_tokens - ?, 0),
  c_completion_tokens   = MAX(c_completion_tokens - ?, 0),
  c_cached_tokens       = MAX(c_cached_tokens - ?, 0),
  c_reasoning_tokens    = MAX(c_reasoning_tokens - ?, 0),
  c_total_duration_ms   = MAX(c_total_duration_ms - ?, 0),
  c_duration_samples    = MAX(c_duration_samples - ?, 0),
  c_total_first_token_ms = MAX(c_total_first_token_ms - ?, 0),
  c_first_token_samples  = MAX(c_first_token_samples - ?, 0)
WHERE session_id = ?`,
		old.success, 1-old.success, old.usageAvailable,
		old.totalTokens, old.promptTokens, old.completionTokens, old.cacheReadTokens, old.reasoningTokens,
		old.durationMS, nonZeroSample(old.durationMS),
		old.firstTokenMS, nonZeroSample(old.firstTokenMS),
		old.sessionID,
	); err != nil {
		return fmt.Errorf("rollback old session stats: %w", err)
	}
	oldKey := turnKeyForParts(old.traceID, old.turnID, llmRequestID)
	if oldKey == "" {
		return nil
	}
	return removeStaleTurnKeyTx(tx, old.sessionID, oldKey, llmRequestID, true)
}

// removeStaleTurnKeyTx 删除不再被任何请求引用的 turn 键并回退计数。
// mustExist=true 时键不存在也算成功（回退路径）。excludeRequestID 排除的
// 请求视为已迁走（调用点保证 raw 行已不在该键下）。
func removeStaleTurnKeyTx(tx *sql.Tx, sessionID, turnKey, excludeRequestID string, tolerateMissing bool) error {
	var oldFailed int64
	err := tx.QueryRow(`SELECT failed FROM `+statsTurnKeysTable+` WHERE session_id = ? AND turn_key = ?`,
		sessionID, turnKey).Scan(&oldFailed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		if tolerateMissing && errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("read stale turn key: %w", err)
	}
	result, err := tx.Exec(`DELETE FROM `+statsTurnKeysTable+`
 WHERE session_id = ? AND turn_key = ?
   AND NOT EXISTS (
     SELECT 1 FROM usage_requests r
     WHERE r.session_id = ? AND r.llm_request_id <> ? AND `+aliasedTurnKeyExpr+` = ?
   )`,
		sessionID, turnKey, sessionID, excludeRequestID, turnKey,
	)
	if err != nil {
		return fmt.Errorf("remove stale turn key: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil || deleted == 0 {
		return nil
	}
	return bumpTurnCountsTx(tx, sessionID, -1, -oldFailed)
}

const turnCountsSQL = `UPDATE usage_sessions SET
  c_turn_count  = MAX(c_turn_count + ?, 0),
  c_failed_turns = MAX(c_failed_turns + ?, 0)
WHERE session_id = ?`

func bumpTurnCountsTx(tx *sql.Tx, sessionID string, turnDelta, failedDelta int64) error {
	if turnDelta == 0 && failedDelta == 0 {
		return nil
	}
	if _, err := tx.Exec(turnCountsSQL, turnDelta, failedDelta, sessionID); err != nil {
		return fmt.Errorf("update usage_sessions turn counts: %w", err)
	}
	return nil
}

// applyTurnKeyTx 维护 (session_id, turn_key) 去重行与 turn 计数（§5.2）。
func applyTurnKeyTx(tx *sql.Tx, sessionID, turnKey string, old requestStatsValues, found bool, failed int64) error {
	result, err := tx.Exec(`INSERT OR IGNORE INTO `+statsTurnKeysTable+`(session_id, turn_key, failed) VALUES(?,?,?)`,
		sessionID, turnKey, failed)
	if err != nil {
		return fmt.Errorf("insert turn key: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("insert turn key rows affected: %w", err)
	}
	if inserted > 0 {
		return bumpTurnCountsTx(tx, sessionID, 1, failed)
	}
	var existingFailed int64
	if err := tx.QueryRow(`SELECT failed FROM `+statsTurnKeysTable+` WHERE session_id = ? AND turn_key = ?`,
		sessionID, turnKey).Scan(&existingFailed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("read turn key: %w", err)
	}
	wantFailed := existingFailed
	switch {
	case existingFailed == 0 && failed == 1:
		wantFailed = 1
	case existingFailed == 1 && failed == 0 && found && old.success == 0:
		// 失败 → 成功：按 usage_requests 现状态重算该 turn 是否仍有失败请求。
		remaining, err := countFailedTurnRequestsTx(tx, sessionID, turnKey)
		if err != nil {
			return err
		}
		if remaining == 0 {
			wantFailed = 0
		}
	}
	if wantFailed == existingFailed {
		return nil
	}
	if _, err := tx.Exec(`UPDATE `+statsTurnKeysTable+` SET failed = ? WHERE session_id = ? AND turn_key = ?`,
		wantFailed, sessionID, turnKey); err != nil {
		return fmt.Errorf("update turn key: %w", err)
	}
	return bumpTurnCountsTx(tx, sessionID, 0, wantFailed-existingFailed)
}

func countFailedTurnRequestsTx(tx *sql.Tx, sessionID, turnKey string) (int64, error) {
	var count int64
	if err := tx.QueryRow(`SELECT COUNT(*) FROM usage_requests
WHERE session_id = ? AND success = 0 AND `+statsTurnKeyExpr+` = ?`, sessionID, turnKey).Scan(&count); err != nil {
		return 0, fmt.Errorf("recount failed turn requests: %w", err)
	}
	return count, nil
}

// turnKeyForRecord 与旧 sessionSelect 的 COALESCE(NULLIF(trace_id,”),NULLIF(turn_id,”),llm_request_id) 对齐。
func turnKeyForRecord(record cacheanalytics.CacheRequestRecord) string {
	return turnKeyForParts(record.TraceID, record.TurnID, record.LLMRequestID)
}

func turnKeyForParts(traceID, turnID, llmRequestID string) string {
	if traceID != "" {
		return traceID
	}
	if turnID != "" {
		return turnID
	}
	return llmRequestID
}

func nonZeroSample(durationMS int64) int64 {
	if durationMS != 0 {
		return 1
	}
	return 0
}
