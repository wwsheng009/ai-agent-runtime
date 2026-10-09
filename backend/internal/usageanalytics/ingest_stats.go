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
	parentSessionID  string
	rootSessionID    string
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
		sessionID:       record.SessionID,
		parentSessionID: record.ParentSessionID,
		rootSessionID:   normalizeRequestRoot(record.SessionID, record.RootSessionID),
		durationMS:      record.DurationMS,
		firstTokenMS:    record.FirstTokenMS,
		startedAtNano:   startedAt.UnixNano(),
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
	err := tx.QueryRow(`SELECT session_id, parent_session_id, root_session_id, trace_id, turn_id, success, usage_available, prompt_tokens, completion_tokens,
       total_tokens, cache_read_tokens, reasoning_tokens, duration_ms, first_token_ms, started_at_unix_nano
FROM usage_requests WHERE llm_request_id = ?`, llmRequestID).Scan(
		&values.sessionID,
		&values.parentSessionID,
		&values.rootSessionID,
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
		// 重复终态/无载荷回放：新解析结果缺父链时保留已落库的父链，避免把
		// 子代理请求从根会话回退掉（跨进程回放、载荷丢帧场景）。
		if found {
			if newValues.parentSessionID == "" {
				newValues.parentSessionID = old.parentSessionID
			}
			if old.rootSessionID != "" && old.rootSessionID != sessionID &&
				(newValues.rootSessionID == "" || newValues.rootSessionID == sessionID) {
				newValues.rootSessionID = old.rootSessionID
			}
		}
		// 双目标归集（schema v9）：请求计入自身会话 + 根会话（去重）。
		// 子代理请求因此同时进入子会话行与父会话行；父会话列表/概览只读根行，
		// 全局统计不重复计数（子会话行从默认列表隐藏）。
		newTargets := requestSessionTargets(sessionID, newValues.rootSessionID)
		for _, target := range newTargets {
			// 根行可能尚未存在（采集从子代理请求开始）：UPDATE 静默影响 0 行会
			// 丢计数，先幂等补齐行骨架。
			if err := ensureSessionRowTx(tx, target, startedAt); err != nil {
				return err
			}
		}
		var oldTargets []string
		if found {
			oldTargets = requestSessionTargets(old.sessionID, old.rootSessionID)
		}
		// 对「旧目标 ∪ 新目标」逐一调整贡献：delta = 新贡献 - 旧贡献。
		// 覆盖写/换会话/换 root 都退化为同一套逐目标差分，不再需要特判分支。
		for _, target := range unionSessionTargets(oldTargets, newTargets) {
			newMember := containsSessionTarget(newTargets, target)
			oldMember := containsSessionTarget(oldTargets, target)
			oldContribution := requestStatsValues{}
			if oldMember {
				oldContribution = old
			}
			if !newMember {
				// 请求迁出该目标：按旧贡献做带下限的减除（MAX(...,0)），避免
				// 历史漂移被放大成负数。
				if err := rollbackSessionStatsTx(tx, target, oldContribution); err != nil {
					return err
				}
			} else if err := applySessionStatsDeltaTx(tx, target, oldContribution, oldMember, newValues); err != nil {
				return err
			}
			// turn 键（per-target）：旧键迁出/变更 → 清理；新键 → 维护。
			oldKey := ""
			if oldMember {
				oldKey = turnKeyForParts(old.traceID, old.turnID, record.LLMRequestID)
			}
			newKey := ""
			if newMember {
				newKey = turnKey
			}
			if oldKey != "" && oldKey != newKey {
				scope := targetScopeColumn(target, old.sessionID, old.rootSessionID)
				if err := removeStaleTurnKeyTx(tx, target, scope, oldKey, record.LLMRequestID, true); err != nil {
					return err
				}
			}
			if newKey != "" {
				scope := targetScopeColumn(target, sessionID, newValues.rootSessionID)
				if err := applyTurnKeyTx(tx, target, scope, newKey, oldContribution, oldMember && oldKey == newKey, failed); err != nil {
					return err
				}
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

// rollbackSessionStatsTx 把某个目标会话里该请求的贡献减回去（请求迁出目标 /
// 跨会话覆盖）。first/last 为 MIN/MAX，无法安全回退，交由 rebuild-stats 修正
// （§6.3）；turn 键的清理由调用方按目标作用域单独执行。
func rollbackSessionStatsTx(tx *sql.Tx, sessionID string, old requestStatsValues) error {
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
		sessionID,
	); err != nil {
		return fmt.Errorf("rollback old session stats: %w", err)
	}
	return nil
}

// removeStaleTurnKeyTx 删除不再被任何请求引用的 turn 键并回退计数。
// mustExist=true 时键不存在也算成功（回退路径）。excludeRequestID 排除的
// 请求视为已迁走（调用点保证 raw 行已不在该键下）。scopeColumn 是目标作用域
// 列（"session_id" 自身视图 / "root_session_id" 根视图），决定"还有没有别的
// 请求引用该键"的判定范围。
func removeStaleTurnKeyTx(tx *sql.Tx, sessionID, scopeColumn, turnKey, excludeRequestID string, tolerateMissing bool) error {
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
     WHERE r.`+scopeColumn+` = ? AND r.llm_request_id <> ? AND `+aliasedTurnKeyExpr+` = ?
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
// scopeColumn 决定"失败重算"的请求范围（自身视图 session_id / 根视图
// root_session_id），与 removeStaleTurnKeyTx 的作用域口径一致。
func applyTurnKeyTx(tx *sql.Tx, sessionID, scopeColumn, turnKey string, old requestStatsValues, found bool, failed int64) error {
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
		remaining, err := countFailedTurnRequestsTx(tx, sessionID, scopeColumn, turnKey)
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

func countFailedTurnRequestsTx(tx *sql.Tx, sessionID, scopeColumn, turnKey string) (int64, error) {
	var count int64
	if err := tx.QueryRow(`SELECT COUNT(*) FROM usage_requests r
WHERE r.`+scopeColumn+` = ? AND r.success = 0 AND `+statsTurnKeyExpr+` = ?`, sessionID, turnKey).Scan(&count); err != nil {
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

// normalizeRequestRoot 归一化根会话：空值或等于自身时按顶层处理（root=自身）。
func normalizeRequestRoot(sessionID, rootSessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	rootSessionID = strings.TrimSpace(rootSessionID)
	if rootSessionID == "" {
		return sessionID
	}
	return rootSessionID
}

// requestSessionTargets 返回请求计入的会话目标：自身 + 根（去重）。
func requestSessionTargets(sessionID, rootSessionID string) []string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	rootSessionID = strings.TrimSpace(rootSessionID)
	if rootSessionID == "" || rootSessionID == sessionID {
		return []string{sessionID}
	}
	return []string{sessionID, rootSessionID}
}

// unionSessionTargets 合并新旧目标（保持出现顺序、去重）。
func unionSessionTargets(oldTargets, newTargets []string) []string {
	seen := make(map[string]bool, len(oldTargets)+len(newTargets))
	union := make([]string, 0, len(oldTargets)+len(newTargets))
	for _, target := range append(append([]string{}, oldTargets...), newTargets...) {
		if target == "" || seen[target] {
			continue
		}
		seen[target] = true
		union = append(union, target)
	}
	return union
}

// containsSessionTarget 判断目标是否在集合中。
func containsSessionTarget(targets []string, target string) bool {
	for _, candidate := range targets {
		if candidate == target {
			return true
		}
	}
	return false
}

// targetScopeColumn 返回目标视图下 turn 键判定的请求作用域列：
// 目标是根且不等于请求自身会话时用 root_session_id，否则用 session_id。
func targetScopeColumn(target, sessionID, rootSessionID string) string {
	target = strings.TrimSpace(target)
	sessionID = strings.TrimSpace(sessionID)
	rootSessionID = strings.TrimSpace(rootSessionID)
	if target != "" && target != sessionID && target == rootSessionID {
		return "root_session_id"
	}
	return "session_id"
}

// ensureSessionRowTx 幂等补齐目标会话行骨架（子请求先于根行落库的边界：
// 增量 UPDATE 影响 0 行会静默丢计数）。
func ensureSessionRowTx(tx *sql.Tx, sessionID string, startedAt time.Time) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	nano := int64(0)
	if !startedAt.IsZero() {
		nano = startedAt.UnixNano()
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO usage_sessions(session_id, started_at_unix_nano, updated_at_unix_nano) VALUES(?,?,?)`,
		sessionID, nano, nano); err != nil {
		return fmt.Errorf("ensure usage_sessions row: %w", err)
	}
	return nil
}
