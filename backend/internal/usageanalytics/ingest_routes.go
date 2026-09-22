package usageanalytics

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// ---------------------------------------------------------------------------
// 路由切换观测采集（主 Agent / 子 Agent）→ usage_routes 单表。
//
// 事件来源（发射点见 internal/agent，常量见 internal/events）：
//   - subagent.route.resolved   → scope=subagent,   kind=applied
//   - main_agent.route_applied  → scope=main_agent, kind=applied
//   - main_agent.route_cleared  → scope=main_agent, kind=cleared（还原基线证据）
//   - main_agent.route_{prediction_invalid, prediction_unresolvable,
//     disabled_for_turn, cost_guard_tripped}
//     → scope=main_agent, kind=warning（路由机制自身的异常/护栏信号）
//
// 幂等：route_event_id 由维度自然键拼接（含 attempt，保留子代理重试轨迹），
// 重复投递走 ON CONFLICT 合并，不产生重复行。
// ---------------------------------------------------------------------------

// 路由观测的 scope 取值。
const (
	RouteScopeMainAgent = "main_agent"
	RouteScopeSubagent  = "subagent"
)

// 路由观测的 kind 取值：实际改道 / 还原基线 / 异常与护栏。
const (
	RouteKindApplied = "applied"
	RouteKindCleared = "cleared"
	RouteKindWarning = "warning"
)

// 子代理开工路由事件的 reason 常量（与发射点语义一致）。
const routeReasonResolved = "route_resolved"

// routeRecord 是 usage_routes 的一行（归一化后的列值）。
type routeRecord struct {
	scope            string
	kind             string
	sessionID        string
	parentSessionID  string
	childSessionID   string
	traceID          string
	agentID          string
	role             string
	goal             string
	taskType         string
	taskSubject      string
	step             int
	reason           string
	source           string
	difficulty       string
	difficultySource string
	provider         string
	model            string
	effort           string
	routeChanged     *bool
	fallbackUsed     *bool
	fallbackReason   string
	candidateCount   int
	warnings         []string
	candidatesJSON   string
	attempt          int
	maxAttempts      int
	batchID          string
	recordedAt       time.Time
}

// onSubagentRouteResolved 记录子代理开工时刻的路由决策。
//
// 归属父会话（发射点即如此），并保留 child_session_id / attempt，让「取消、超时、
// 孤儿」的子代理也能反查当时被路由到哪个 provider/model。
func (c *collector) onSubagentRouteResolved(event runtimeevents.Event) {
	payload := event.Payload
	record := routeRecord{
		scope:            RouteScopeSubagent,
		kind:             RouteKindApplied,
		sessionID:        firstNonEmpty(payloadString(payload, "parent_session_id"), event.SessionID),
		parentSessionID:  payloadString(payload, "parent_session_id", "session_id"),
		childSessionID:   payloadString(payload, "child_session_id"),
		traceID:          firstNonEmpty(payloadString(payload, "trace_id"), event.TraceID),
		agentID:          payloadString(payload, "subagent_id", "agent_id"),
		role:             payloadString(payload, "role"),
		goal:             payloadString(payload, "goal"),
		taskType:         payloadString(payload, "task_type"),
		taskSubject:      payloadString(payload, "task_subject"),
		step:             payloadInt(payload, "step"),
		reason:           routeReasonResolved,
		source:           payloadString(payload, "route_source"),
		difficulty:       payloadString(payload, "difficulty"),
		difficultySource: payloadString(payload, "difficulty_source"),
		provider:         payloadString(payload, "route_provider", "provider"),
		model:            payloadString(payload, "route_model", "model"),
		effort:           payloadString(payload, "route_reasoning_effort", "reasoning_effort"),
		fallbackReason:   payloadString(payload, "fallback_reason"),
		warnings:         routeWarningsFromPayload(payload),
		attempt:          payloadInt(payload, "attempt"),
		maxAttempts:      payloadInt(payload, "max_attempts"),
		batchID:          payloadString(payload, "batch_id"),
		recordedAt:       routeEventTime(event, c.now()),
	}
	if value, ok := payloadBoolValue(payload, "fallback_used"); ok {
		record.fallbackUsed = &value
	}
	c.insertRoute(record, payload)
}

// onMainAgentRouteApplied 记录主 Agent 某 step 实际使用的 route。
//
// reason 区分驱动来源（turn_floor / cost_guard / prediction）；route_changed=false
// 表示该 step 仍走基线，是「有多少 step 真的改道」的分母。
func (c *collector) onMainAgentRouteApplied(event runtimeevents.Event) {
	payload := event.Payload
	record := routeRecord{
		scope:            RouteScopeMainAgent,
		kind:             RouteKindApplied,
		sessionID:        firstNonEmpty(payloadString(payload, "session_id"), event.SessionID),
		traceID:          firstNonEmpty(payloadString(payload, "trace_id"), event.TraceID),
		step:             payloadInt(payload, "step"),
		reason:           firstNonEmpty(payloadString(payload, "reason"), "route_applied"),
		source:           payloadString(payload, "source"),
		difficulty:       payloadString(payload, "difficulty"),
		difficultySource: payloadString(payload, "difficulty_source"),
		provider:         payloadString(payload, "provider"),
		model:            payloadString(payload, "model"),
		effort:           payloadString(payload, "reasoning_effort"),
		taskType:         payloadString(payload, "task_type"),
		taskSubject:      payloadString(payload, "task_subject"),
		recordedAt:       routeEventTime(event, c.now()),
	}
	if value, ok := payloadBoolValue(payload, "route_changed"); ok {
		record.routeChanged = &value
	}
	record.candidateCount, record.candidatesJSON = routeCandidatesFromPayload(payload)
	c.insertRoute(record, payload)
}

// onMainAgentRouteCleared 记录 turn 结束还原基线的证据（与 route_applied 配对）。
//
// restored_* 是还原后的目标：provider/model/effort 落到统一列，前端可据此回答
// 「这个 turn 最后停在哪个模型上」。steps_with_override / cost_guard_trips 等
// 计数保留在 record_json。
func (c *collector) onMainAgentRouteCleared(event runtimeevents.Event) {
	payload := event.Payload
	record := routeRecord{
		scope:       RouteScopeMainAgent,
		kind:        RouteKindCleared,
		sessionID:   firstNonEmpty(payloadString(payload, "session_id"), event.SessionID),
		traceID:     firstNonEmpty(payloadString(payload, "trace_id"), event.TraceID),
		step:        payloadInt(payload, "steps_total"),
		reason:      RouteKindCleared,
		source:      "baseline",
		difficulty:  payloadString(payload, "final_difficulty"),
		provider:    payloadString(payload, "restored_provider"),
		model:       payloadString(payload, "restored_model"),
		effort:      payloadString(payload, "restored_effort"),
		taskType:    payloadString(payload, "task_type"),
		taskSubject: payloadString(payload, "task_subject"),
		recordedAt:  routeEventTime(event, c.now()),
	}
	c.insertRoute(record, payload)
}

// onMainAgentRouteWarning 记录主 Agent 路由机制自身的异常/护栏信号
// （非法上报、不可解析、本 turn 禁用、成本护栏触发）。
func (c *collector) onMainAgentRouteWarning(event runtimeevents.Event, reason string) {
	payload := event.Payload
	record := routeRecord{
		scope:       RouteScopeMainAgent,
		kind:        RouteKindWarning,
		sessionID:   firstNonEmpty(payloadString(payload, "session_id"), event.SessionID),
		traceID:     firstNonEmpty(payloadString(payload, "trace_id"), event.TraceID),
		step:        payloadInt(payload, "step"),
		reason:      reason,
		source:      payloadString(payload, "source"),
		difficulty:  payloadString(payload, "difficulty", "level"),
		provider:    payloadString(payload, "provider"),
		model:       payloadString(payload, "model"),
		effort:      payloadString(payload, "reasoning_effort"),
		taskType:    payloadString(payload, "task_type"),
		taskSubject: payloadString(payload, "task_subject"),
		recordedAt:  routeEventTime(event, c.now()),
	}
	c.insertRoute(record, payload)
}

// mainAgentRouteWarningReason 把事件类型映射为稳定的 reason 取值（前端按它分组）。
func mainAgentRouteWarningReason(eventType string) string {
	switch eventType {
	case runtimeevents.EventMainAgentRoutePredictionInvalid:
		return "prediction_invalid"
	case runtimeevents.EventMainAgentRoutePredictionUnresolvable:
		return "prediction_unresolvable"
	case runtimeevents.EventMainAgentRouteDisabledForTurn:
		return "disabled_for_turn"
	case runtimeevents.EventMainAgentRouteCostGuardTripped:
		return "cost_guard_tripped"
	default:
		return ""
	}
}

// insertRoute 幂等写入一行路由观测（重复投递合并，不重复计数）。
func (c *collector) insertRoute(record routeRecord, payload map[string]interface{}) {
	if c == nil || c.store == nil || strings.TrimSpace(record.scope) == "" {
		return
	}
	recordedAt := record.recordedAt
	if recordedAt.IsZero() {
		recordedAt = c.now()
	}
	var changedValue interface{}
	if record.routeChanged != nil {
		changedValue = boolToInt(*record.routeChanged)
	}
	var fallbackValue interface{}
	if record.fallbackUsed != nil {
		fallbackValue = boolToInt(*record.fallbackUsed)
	}
	warningsJSON := ""
	if len(record.warnings) > 0 {
		if encoded, err := json.Marshal(record.warnings); err == nil {
			warningsJSON = string(encoded)
		}
	}
	recordJSON := ""
	if encoded, err := json.Marshal(payload); err == nil {
		recordJSON = string(encoded)
	}
	if err := c.store.execWithLockRetry(`
INSERT INTO usage_routes (
  route_event_id, session_id, parent_session_id, child_session_id, trace_id, scope, kind,
  agent_id, role, goal, task_type, task_subject, step, reason, source, difficulty, difficulty_source, provider, model,
  reasoning_effort, route_changed, fallback_used, fallback_reason, candidate_count,
  warning_count, attempt, max_attempts, batch_id, recorded_at_unix_nano,
  warnings_json, candidates_json, record_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(route_event_id) DO UPDATE SET
  child_session_id = CASE WHEN excluded.child_session_id <> '' THEN excluded.child_session_id ELSE usage_routes.child_session_id END,
  goal = CASE WHEN excluded.goal <> '' THEN excluded.goal ELSE usage_routes.goal END,
  task_type = CASE WHEN excluded.task_type <> '' THEN excluded.task_type ELSE usage_routes.task_type END,
  task_subject = CASE WHEN excluded.task_subject <> '' THEN excluded.task_subject ELSE usage_routes.task_subject END,
  provider = CASE WHEN excluded.provider <> '' THEN excluded.provider ELSE usage_routes.provider END,
  model = CASE WHEN excluded.model <> '' THEN excluded.model ELSE usage_routes.model END,
  reasoning_effort = CASE WHEN excluded.reasoning_effort <> '' THEN excluded.reasoning_effort ELSE usage_routes.reasoning_effort END,
  route_changed = COALESCE(excluded.route_changed, usage_routes.route_changed),
  fallback_used = COALESCE(excluded.fallback_used, usage_routes.fallback_used),
  fallback_reason = CASE WHEN excluded.fallback_reason <> '' THEN excluded.fallback_reason ELSE usage_routes.fallback_reason END,
  candidate_count = MAX(usage_routes.candidate_count, excluded.candidate_count),
  warning_count = MAX(usage_routes.warning_count, excluded.warning_count),
  recorded_at_unix_nano = MAX(usage_routes.recorded_at_unix_nano, excluded.recorded_at_unix_nano),
  warnings_json = CASE WHEN excluded.warnings_json <> '' THEN excluded.warnings_json ELSE usage_routes.warnings_json END,
  candidates_json = CASE WHEN excluded.candidates_json <> '' THEN excluded.candidates_json ELSE usage_routes.candidates_json END,
  record_json = CASE WHEN excluded.record_json IS NOT NULL AND length(excluded.record_json) > 0 THEN excluded.record_json ELSE usage_routes.record_json END`,
		routeEventID(record),
		record.sessionID,
		record.parentSessionID,
		record.childSessionID,
		record.traceID,
		record.scope,
		record.kind,
		record.agentID,
		record.role,
		record.goal,
		record.taskType,
		record.taskSubject,
		record.step,
		record.reason,
		record.source,
		record.difficulty,
		record.difficultySource,
		record.provider,
		record.model,
		record.effort,
		changedValue,
		fallbackValue,
		record.fallbackReason,
		record.candidateCount,
		len(record.warnings),
		record.attempt,
		record.maxAttempts,
		record.batchID,
		recordedAt.UnixNano(),
		warningsJSON,
		record.candidatesJSON,
		recordJSON,
	); err != nil {
		c.reportWriteFailure("usage_routes", err)
	}
}

// routeEventID 生成幂等键：维度自然键（含 child_session_id 与 attempt，
// 保留子代理每次重试的独立轨迹）。
func routeEventID(record routeRecord) string {
	parts := []string{
		record.scope,
		record.kind,
		sanitizeRouteKeyPart(record.sessionID),
		sanitizeRouteKeyPart(record.childSessionID),
		sanitizeRouteKeyPart(record.traceID),
		strconv.Itoa(record.step),
		sanitizeRouteKeyPart(record.reason),
		sanitizeRouteKeyPart(record.agentID),
		strconv.Itoa(record.attempt),
		sanitizeRouteKeyPart(record.provider),
		sanitizeRouteKeyPart(record.model),
	}
	return strings.Join(parts, "|")
}

// sanitizeRouteKeyPart 去掉分隔符，避免维度值把自然键拼串（脏数据不放大）。
func sanitizeRouteKeyPart(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	return strings.NewReplacer("|", "_", "\x00", "_").Replace(trimmed)
}

// routeWarningsFromPayload 读取路由告警列表（[]string / []interface{} 两种形态）。
func routeWarningsFromPayload(payload map[string]interface{}) []string {
	raw, ok := payload["route_warnings"]
	if !ok || raw == nil {
		raw = payload["warnings"]
	}
	switch value := raw.(type) {
	case []string:
		return append([]string(nil), value...)
	case []interface{}:
		warnings := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				warnings = append(warnings, text)
			}
		}
		return warnings
	default:
		return nil
	}
}

// routeCandidatesFromPayload 归一化候选评估链（数量 + 原始 JSON）。
func routeCandidatesFromPayload(payload map[string]interface{}) (int, string) {
	raw, ok := payload["candidates"]
	if !ok || raw == nil {
		return 0, ""
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return 0, ""
	}
	var list []interface{}
	if err := json.Unmarshal(encoded, &list); err != nil {
		// 单对象形态也按一条候选计，原始值仍保留。
		return 1, string(encoded)
	}
	return len(list), string(encoded)
}

// routeEventTime 取事件时间；缺失时回退到采集器时钟。
func routeEventTime(event runtimeevents.Event, fallback time.Time) time.Time {
	if !event.Timestamp.IsZero() {
		return event.Timestamp
	}
	if !fallback.IsZero() {
		return fallback
	}
	return time.Now()
}
