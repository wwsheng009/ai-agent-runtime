package usageanalytics

import (
	"encoding/json"
	"strings"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// ---------------------------------------------------------------------------
// schema v2 采集：tool.requested / tool.completed / subagent.completed /
// session_start → usage_tool_calls / usage_subagents / usage_turns
// （方案 §4 批次 1.2 / §5.3）。
// ---------------------------------------------------------------------------

// turnToolStats 是回合内工具结果的进程内累计（服务重启即丢失，属已声明折衷：
// usage_turns 的列对不上时先写 0）。
type turnToolStats struct {
	// order 保留 tool_name 的出现顺序，便于失败→成功恢复配对。
	order     []string
	failed    map[string]int
	succeeded map[string]int
	// lastFailureIndex / lastSuccessIndex 记录同名工具最后一次失败/成功位置。
	lastFailureIndex map[string]int
	lastSuccessIndex map[string]int
	sequence         int
}

func newTurnToolStats() *turnToolStats {
	return &turnToolStats{
		failed:           make(map[string]int),
		succeeded:        make(map[string]int),
		lastFailureIndex: make(map[string]int),
		lastSuccessIndex: make(map[string]int),
	}
}

func (s *turnToolStats) record(toolName string, ok bool) {
	if s == nil || strings.TrimSpace(toolName) == "" {
		return
	}
	s.sequence++
	if _, seen := s.lastFailureIndex[toolName]; !seen {
		if _, seenSuccess := s.lastSuccessIndex[toolName]; !seenSuccess {
			s.order = append(s.order, toolName)
		}
	}
	if ok {
		s.succeeded[toolName]++
		s.lastSuccessIndex[toolName] = s.sequence
		return
	}
	s.failed[toolName]++
	s.lastFailureIndex[toolName] = s.sequence
}

// flush 返回（工具失败总数、失败后恢复数、未恢复数）。
func (s *turnToolStats) flush() (failedTotal, recovered, unrecovered int) {
	if s == nil {
		return 0, 0, 0
	}
	for _, name := range s.order {
		failed := s.failed[name]
		failedTotal += failed
		if failed == 0 {
			continue
		}
		failureIndex := s.lastFailureIndex[name]
		if successIndex, ok := s.lastSuccessIndex[name]; ok && successIndex > failureIndex {
			recovered++
			continue
		}
		unrecovered++
	}
	if unrecovered > failedTotal {
		unrecovered = failedTotal
	}
	return failedTotal, recovered, unrecovered
}

func (c *collector) turnStats(sessionID, turnID string) *turnToolStats {
	if c == nil || sessionID == "" || turnID == "" {
		return nil
	}
	key := sessionID + "\x00" + turnID
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.turnToolStats == nil {
		c.turnToolStats = make(map[string]*turnToolStats)
	}
	stats := c.turnToolStats[key]
	if stats == nil {
		stats = newTurnToolStats()
		c.turnToolStats[key] = stats
	}
	return stats
}

func (c *collector) takeTurnStats(sessionID, turnID string) *turnToolStats {
	if c == nil || sessionID == "" || turnID == "" {
		return nil
	}
	key := sessionID + "\x00" + turnID
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.turnToolStats == nil {
		return nil
	}
	stats := c.turnToolStats[key]
	delete(c.turnToolStats, key)
	return stats
}

// onSessionStart 建立 usage_turns 骨架（session_end 为权威终值）。
func (c *collector) onSessionStart(event runtimeevents.Event) {
	sessionID := firstNonEmpty(payloadString(event.Payload, "session_id"), event.SessionID)
	if sessionID == "" {
		return
	}
	c.upsertSession(sessionID, SessionMeta{Provider: payloadString(event.Payload, "provider"), Model: payloadString(event.Payload, "model")}, event.Timestamp, time.Time{})
	turnID := payloadString(event.Payload, "turn_id")
	if turnID == "" {
		return
	}
	started := event.Timestamp
	if started.IsZero() {
		started = c.now()
	}
	record, err := json.Marshal(event.Payload)
	if err != nil {
		record = nil
	}
	if err := c.store.execWithLockRetry(`
INSERT INTO usage_turns (session_id, turn_id, trace_id, started_at_unix_nano, record_json)
VALUES (?,?,?,?,?)
ON CONFLICT(session_id, turn_id) DO NOTHING`,
		sessionID, turnID, payloadString(event.Payload, "trace_id", "trace"), started.UnixNano(), string(record),
	); err != nil {
		c.reportWriteFailure("usage_turns", err)
	}
}

// onToolRequested 建立 usage_tool_calls 骨架行（completed 补全）。
func (c *collector) onToolRequested(event runtimeevents.Event) {
	payload := event.Payload
	toolCallID := payloadString(payload, "tool_call_id")
	if toolCallID == "" {
		return
	}
	started := event.Timestamp
	if started.IsZero() {
		started = c.now()
	}
	record, err := json.Marshal(payload)
	if err != nil {
		record = nil
	}
	if err := c.store.execWithLockRetry(`
INSERT INTO usage_tool_calls (
  tool_call_id, session_id, trace_id, turn_id, step, tool_name, source, kind, outcome,
  started_at_unix_nano, record_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(tool_call_id) DO UPDATE SET
  session_id = CASE WHEN excluded.session_id <> '' THEN excluded.session_id ELSE usage_tool_calls.session_id END,
  trace_id = CASE WHEN excluded.trace_id <> '' THEN excluded.trace_id ELSE usage_tool_calls.trace_id END,
  turn_id = CASE WHEN excluded.turn_id <> '' THEN excluded.turn_id ELSE usage_tool_calls.turn_id END,
  tool_name = CASE WHEN excluded.tool_name <> '' THEN excluded.tool_name ELSE usage_tool_calls.tool_name END,
  source = CASE WHEN excluded.source <> '' THEN excluded.source ELSE usage_tool_calls.source END,
  started_at_unix_nano = CASE WHEN usage_tool_calls.started_at_unix_nano = 0 THEN excluded.started_at_unix_nano ELSE usage_tool_calls.started_at_unix_nano END`,
		toolCallID,
		firstNonEmpty(payloadString(payload, "session_id"), event.SessionID),
		payloadString(payload, "trace_id", "trace"),
		payloadString(payload, "turn_id"),
		payloadInt(payload, "step"),
		payloadString(payload, "logical_tool", "tool_name"),
		payloadString(payload, "source", "tool_source"),
		payloadString(payload, "kind"),
		"",
		started.UnixNano(),
		string(record),
	); err != nil {
		c.reportWriteFailure("usage_tool_calls", err)
	}
}

// onToolCompleted 补全 usage_tool_calls（幂等：重复事件覆盖为同一终值）。
func (c *collector) onToolCompleted(event runtimeevents.Event) {
	payload := event.Payload
	toolCallID := payloadString(payload, "tool_call_id")
	if toolCallID == "" {
		return
	}
	sessionID := firstNonEmpty(payloadString(payload, "session_id"), event.SessionID)
	turnID := payloadString(payload, "turn_id")
	toolName := payloadString(payload, "logical_tool", "tool_name")
	completed := event.Timestamp
	if completed.IsZero() {
		completed = c.now()
	}
	outcome := payloadString(payload, "outcome")
	ok, hasOK := payloadBoolValue(payload, "ok")
	if !hasOK {
		// 兼容：老事件只有 error 文本（无 toolresult 处置元数据）时按
		// "有错误文本即失败"判定，与 toolresult.Diagnose 的默认一致。
		errorText := payloadString(payload, "error")
		ok = errorText == ""
		hasOK = true
		outcome = "success"
		if !ok {
			outcome = "failed"
		}
	}
	if outcome == "" {
		if ok {
			outcome = "success"
		} else {
			outcome = "failed"
		}
	}
	var okValue interface{}
	if hasOK {
		okValue = boolToInt(ok)
	}
	emptyResult := 0
	if flag, exists := payloadBoolValue(payload, "empty_result"); exists && flag {
		emptyResult = 1
	}
	var retryableValue interface{}
	if retryable, exists := payloadBoolValue(payload, "retryable"); exists {
		retryableValue = boolToInt(retryable)
	}
	duration := payloadInt64(payload, "duration_ms", "duration")
	record, err := json.Marshal(payload)
	if err != nil {
		record = nil
	}
	if err := c.store.execWithLockRetry(`
INSERT INTO usage_tool_calls (
  tool_call_id, session_id, trace_id, turn_id, step, tool_name, source, kind, outcome, ok,
  empty_result, error_code, retryable, failed_count, succeeded_count,
  started_at_unix_nano, completed_at_unix_nano, duration_ms, record_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(tool_call_id) DO UPDATE SET
  session_id = CASE WHEN excluded.session_id <> '' THEN excluded.session_id ELSE usage_tool_calls.session_id END,
  trace_id = CASE WHEN excluded.trace_id <> '' THEN excluded.trace_id ELSE usage_tool_calls.trace_id END,
  turn_id = CASE WHEN excluded.turn_id <> '' THEN excluded.turn_id ELSE usage_tool_calls.turn_id END,
  step = CASE WHEN excluded.step > 0 THEN excluded.step ELSE usage_tool_calls.step END,
  tool_name = CASE WHEN excluded.tool_name <> '' THEN excluded.tool_name ELSE usage_tool_calls.tool_name END,
  source = CASE WHEN excluded.source <> '' THEN excluded.source ELSE usage_tool_calls.source END,
  outcome = CASE WHEN excluded.outcome <> '' THEN excluded.outcome ELSE usage_tool_calls.outcome END,
  ok = COALESCE(excluded.ok, usage_tool_calls.ok),
  empty_result = MAX(usage_tool_calls.empty_result, excluded.empty_result),
  error_code = CASE WHEN excluded.error_code <> '' THEN excluded.error_code ELSE usage_tool_calls.error_code END,
  retryable = COALESCE(excluded.retryable, usage_tool_calls.retryable),
  failed_count = MAX(usage_tool_calls.failed_count, excluded.failed_count),
  succeeded_count = MAX(usage_tool_calls.succeeded_count, excluded.succeeded_count),
  started_at_unix_nano = CASE WHEN usage_tool_calls.started_at_unix_nano = 0 THEN excluded.started_at_unix_nano ELSE usage_tool_calls.started_at_unix_nano END,
  completed_at_unix_nano = MAX(usage_tool_calls.completed_at_unix_nano, excluded.completed_at_unix_nano),
  duration_ms = CASE WHEN excluded.duration_ms > 0 THEN excluded.duration_ms ELSE usage_tool_calls.duration_ms END,
  record_json = CASE WHEN excluded.record_json IS NOT NULL AND length(excluded.record_json) > 0 THEN excluded.record_json ELSE usage_tool_calls.record_json END`,
		toolCallID,
		sessionID,
		payloadString(payload, "trace_id", "trace"),
		turnID,
		payloadInt(payload, "step"),
		toolName,
		payloadString(payload, "source", "tool_source"),
		payloadString(payload, "kind"),
		outcome,
		okValue,
		emptyResult,
		payloadString(payload, "error_code"),
		retryableValue,
		payloadInt(payload, "failed_count"),
		payloadInt(payload, "succeeded_count"),
		completed.UnixNano(),
		completed.UnixNano(),
		duration,
		string(record),
	); err != nil {
		c.reportWriteFailure("usage_tool_calls", err)
	}
	if stats := c.turnStats(sessionID, turnID); stats != nil {
		stats.record(toolName, ok)
	}
}

// onSubagentCompleted 归一化写入 usage_subagents（幂等合并，§5.2 / 批次 2.2）。
func (c *collector) onSubagentCompleted(event runtimeevents.Event) {
	normalized, ok := NormalizeSubagentCompletion(event)
	if !ok {
		return
	}
	if normalized.CompletedAt.IsZero() {
		normalized.CompletedAt = event.Timestamp
	}
	if normalized.CompletedAt.IsZero() {
		normalized.CompletedAt = c.now()
	}
	idSynthesized := 0
	if payloadString(event.Payload, "subagent_id", "agent_id") == "" {
		idSynthesized = 1
	}
	conflictCount := 0
	if normalized.Conflict {
		conflictCount = 1
	}
	var successValue interface{}
	if normalized.Success != nil {
		successValue = boolToInt(*normalized.Success)
	}
	// read_only 列是 NOT NULL DEFAULT 0：未知按 0（非只读）落库，
	// 避免整行因 NULL 被拒（未知与"非只读"在该列语义下不可区分，已知偏差）。
	readOnlyValue := boolToInt(normalized.ReadOnly && normalized.ReadOnlyKnown)
	record, err := json.Marshal(event.Payload)
	if err != nil {
		record = nil
	}
	if err := c.store.execWithLockRetry(`
INSERT INTO usage_subagents (
  subagent_id, parent_session_id, child_session_id, role, read_only, success, completion_reason,
  failure_category, error_code, attempt, max_attempts, retry_reason, id_synthesized, duration_ms,
  started_at_unix_nano, completed_at_unix_nano, usage_total_tokens, source, conflict_count, record_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(subagent_id, parent_session_id) DO UPDATE SET
  child_session_id = CASE WHEN excluded.child_session_id <> '' THEN excluded.child_session_id ELSE usage_subagents.child_session_id END,
  role = CASE WHEN excluded.role <> '' THEN excluded.role ELSE usage_subagents.role END,
  read_only = COALESCE(excluded.read_only, usage_subagents.read_only),
  success = COALESCE(excluded.success, usage_subagents.success),
  completion_reason = CASE WHEN excluded.completion_reason <> '' AND excluded.completion_reason <> 'unknown' THEN excluded.completion_reason ELSE usage_subagents.completion_reason END,
  failure_category = CASE
    WHEN excluded.success = 1 THEN ''
    WHEN excluded.failure_category <> '' THEN excluded.failure_category
    ELSE usage_subagents.failure_category END,
  error_code = CASE
    WHEN excluded.success = 1 THEN ''
    WHEN excluded.error_code <> '' THEN excluded.error_code
    ELSE usage_subagents.error_code END,
  attempt = MAX(usage_subagents.attempt, excluded.attempt),
  max_attempts = MAX(usage_subagents.max_attempts, excluded.max_attempts),
  retry_reason = CASE WHEN excluded.retry_reason <> '' THEN excluded.retry_reason ELSE usage_subagents.retry_reason END,
  id_synthesized = MAX(usage_subagents.id_synthesized, excluded.id_synthesized),
  duration_ms = CASE WHEN excluded.duration_ms > 0 THEN excluded.duration_ms ELSE usage_subagents.duration_ms END,
  started_at_unix_nano = CASE WHEN usage_subagents.started_at_unix_nano = 0 THEN excluded.started_at_unix_nano ELSE usage_subagents.started_at_unix_nano END,
  completed_at_unix_nano = MAX(usage_subagents.completed_at_unix_nano, excluded.completed_at_unix_nano),
  usage_total_tokens = MAX(usage_subagents.usage_total_tokens, excluded.usage_total_tokens),
  source = CASE WHEN excluded.source <> '' THEN excluded.source ELSE usage_subagents.source END,
  conflict_count = usage_subagents.conflict_count + excluded.conflict_count,
  record_json = CASE WHEN excluded.record_json IS NOT NULL AND length(excluded.record_json) > 0 THEN excluded.record_json ELSE usage_subagents.record_json END`,
		normalized.SubagentID,
		normalized.ParentSessionID,
		normalized.ChildSessionID,
		normalized.Role,
		readOnlyValue,
		successValue,
		normalized.CompletionReason,
		normalized.FailureCategory,
		normalized.ErrorCode,
		normalized.Attempt,
		normalized.MaxAttempts,
		normalized.RetryReason,
		idSynthesized,
		normalized.DurationMS,
		normalized.StartedAt.UnixNano(),
		normalized.CompletedAt.UnixNano(),
		normalized.UsageTotalTokens,
		normalized.Source,
		conflictCount,
		string(record),
	); err != nil {
		c.reportWriteFailure("usage_subagents", err)
	}
}

// upsertTurnTerminal 用 session_end / session_interrupted 载荷写回合终值。
// 列名对不上时保持 0 并在 record_json 保留原始载荷（§5.1 实现注意）。
func (c *collector) upsertTurnTerminal(sessionID string, event runtimeevents.Event) {
	payload := event.Payload
	turnID := payloadString(payload, "turn_id")
	if turnID == "" {
		return
	}
	ended := event.Timestamp
	if ended.IsZero() {
		ended = c.now()
	}
	var successValue interface{}
	success, hasSuccess := payloadBoolValue(payload, "success")
	if hasSuccess {
		successValue = boolToInt(success)
	}
	completionReason := payloadString(payload, "status", "completion_reason")
	if successValue == nil {
		completionReason = SubagentCompletionUnknown
	} else if success {
		completionReason = SubagentCompletionCompleted
	} else {
		completionReason = SubagentCompletionFailed
		if reason := payloadString(payload, "status", "completion_reason"); reason != "" {
			completionReason = reason
		}
	}
	stats := c.takeTurnStats(sessionID, turnID)
	failedTotal, recovered, unrecovered := stats.flush()
	record, err := json.Marshal(payload)
	if err != nil {
		record = nil
	}
	if err := c.store.execWithLockRetry(`
INSERT INTO usage_turns (
  session_id, turn_id, trace_id, success, completion_reason, error_code, steps, duration_ms,
  tool_error_count, recovered_tool_error_count, unrecovered_tool_error_count,
  prompt_tokens, completion_tokens, total_tokens, cache_read_tokens, reasoning_tokens,
  started_at_unix_nano, ended_at_unix_nano, record_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(session_id, turn_id) DO UPDATE SET
  trace_id = CASE WHEN excluded.trace_id <> '' THEN excluded.trace_id ELSE usage_turns.trace_id END,
  success = COALESCE(excluded.success, usage_turns.success),
  completion_reason = CASE WHEN excluded.completion_reason <> '' AND excluded.completion_reason <> 'unknown' THEN excluded.completion_reason ELSE usage_turns.completion_reason END,
  error_code = CASE WHEN excluded.error_code <> '' THEN excluded.error_code ELSE usage_turns.error_code END,
  steps = MAX(usage_turns.steps, excluded.steps),
  duration_ms = CASE WHEN excluded.duration_ms > 0 THEN excluded.duration_ms ELSE usage_turns.duration_ms END,
  tool_error_count = MAX(usage_turns.tool_error_count, excluded.tool_error_count),
  recovered_tool_error_count = MAX(usage_turns.recovered_tool_error_count, excluded.recovered_tool_error_count),
  unrecovered_tool_error_count = MAX(usage_turns.unrecovered_tool_error_count, excluded.unrecovered_tool_error_count),
  prompt_tokens = MAX(usage_turns.prompt_tokens, excluded.prompt_tokens),
  completion_tokens = MAX(usage_turns.completion_tokens, excluded.completion_tokens),
  total_tokens = MAX(usage_turns.total_tokens, excluded.total_tokens),
  cache_read_tokens = MAX(usage_turns.cache_read_tokens, excluded.cache_read_tokens),
  reasoning_tokens = MAX(usage_turns.reasoning_tokens, excluded.reasoning_tokens),
  ended_at_unix_nano = MAX(usage_turns.ended_at_unix_nano, excluded.ended_at_unix_nano),
  record_json = CASE WHEN excluded.record_json IS NOT NULL AND length(excluded.record_json) > 0 THEN excluded.record_json ELSE usage_turns.record_json END`,
		sessionID,
		turnID,
		payloadString(payload, "trace_id", "trace"),
		successValue,
		completionReason,
		payloadString(payload, "error_code"),
		payloadInt(payload, "steps"),
		payloadInt64(payload, "duration", "duration_ms"),
		int64(failedTotal),
		int64(recovered),
		int64(unrecovered),
		payloadInt64(payload, "usage_prompt_tokens", "prompt_tokens"),
		payloadInt64(payload, "usage_completion_tokens", "completion_tokens"),
		payloadInt64(payload, "usage_total_tokens", "total_tokens"),
		payloadInt64(payload, "usage_cache_read_tokens", "cache_read_tokens"),
		payloadInt64(payload, "usage_reasoning_tokens", "reasoning_tokens"),
		ended.UnixNano(),
		ended.UnixNano(),
		string(record),
	); err != nil {
		c.reportWriteFailure("usage_turns", err)
	}
}

// errorCategoryForRequest 复用 D5 分类（llm 错误码 → 失败分类），
// 供 usage_requests.error_category 的读取侧诊断使用。
func errorCategoryForRequest(errorCode string) string {
	return llm.FailureCategoryFromErrorCode(errorCode)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
