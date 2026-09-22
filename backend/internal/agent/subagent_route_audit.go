package agent

import (
	"encoding/json"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelrouting"
)

// 子代理开工路由审计（方案 docs/plan/task-difficulty-routing-audit-hardening-plan-20260921.md
// §5.1 的 G1 修复）：把决策点已解析出的 RouteDecision 以**父会话归属**落成一条
// A 通道事件，让取消 / 超时 / 孤儿 / 崩溃的子代理也能反查「当时被路由到哪个模型」。
//
// 与 subagent.started 的关系：后者是 D 通道（不落盘）且挂子会话 id，被父会话
// bridge 的 primary-session 过滤挡掉，两层都到不了父会话；本事件是纯增量，
// 不改 subagent.started 的任何行为。
const (
	// subagentRouteAuditTextLimit 是 goal / difficulty_rationale 的单字段字符上限。
	subagentRouteAuditTextLimit = 256
	// subagentRouteAuditPayloadByteLimit 是整行载荷的字节预算（方案 §5.1 的载荷约束）。
	// 256 字符的中文自由文本最坏可达 ~1.5 KB，两个字段叠加会顶破预算，因此除按
	// 字符截断外再做一次按字节的收敛（只收缩自由文本，审计字段与 id 永不裁）。
	subagentRouteAuditPayloadByteLimit = 2048
)

// buildSubagentRouteResolvedPayload 组装 subagent.route.resolved 载荷：
// 路由审计字段（difficulty / difficulty_source / route_* / route_warnings /
// fallback_*）来自 mergeRouteAuditPayload，自由文本按字符截断后整体再按字节收敛。
func buildSubagentRouteResolvedPayload(base map[string]interface{}, decision modelrouting.RouteDecision) map[string]interface{} {
	payload := mergeRouteAuditPayload(base, decision)
	for _, key := range []string{"goal", "difficulty_rationale", "task_subject"} {
		if text, ok := payload[key].(string); ok && text != "" {
			payload[key] = truncateSubagentParentText(text, subagentRouteAuditTextLimit)
		}
	}
	return boundSubagentRouteAuditPayload(payload, subagentRouteAuditPayloadByteLimit)
}

// boundSubagentRouteAuditPayload 保证整行载荷不超预算：按字符截断后仍可能超
// （中文 3 字节/字符），故按 192/128/96/64/32/0 逐级收缩两个自由文本字段，
// 直到 json 编码落回预算内。
func boundSubagentRouteAuditPayload(payload map[string]interface{}, limit int) map[string]interface{} {
	if payload == nil {
		return nil
	}
	if encoded, err := json.Marshal(payload); err == nil && len(encoded) <= limit {
		return payload
	}
	for _, budget := range []int{192, 128, 96, 64, 32, 0} {
		for _, key := range []string{"goal", "difficulty_rationale", "task_subject"} {
			if text, ok := payload[key].(string); ok && text != "" {
				payload[key] = truncateSubagentParentText(text, budget)
			}
		}
		if encoded, err := json.Marshal(payload); err == nil && len(encoded) <= limit {
			return payload
		}
	}
	return payload
}

// emitSubagentRouteResolved 发射一条开工路由审计。每次 attempt 发一行并带
// attempt 序号（与 subagent_retry.go 的多次发射形态一致），不做去重，保留完整
// 重试轨迹。归属 options.ParentSessionID：治理数据必须能被父会话事件流反查。
func (s *SubagentScheduler) emitSubagentRouteResolved(options SubagentRunOptions, task SubagentTask, spec ChildAgentSpec, childSessionID, childAgentName string, attempt, maxAttempts int) {
	if s == nil || s.parent == nil {
		return
	}
	base := map[string]interface{}{
		"subagent_id":       task.ID,
		"role":              task.Role,
		"task_type":         task.TaskType,
		"task_subject":      task.TaskSubject,
		"goal":              task.Goal,
		"parent_session_id": options.ParentSessionID,
		"child_session_id":  childSessionID,
		"child_agent_name":  childAgentName,
		"attempt":           attempt,
		"max_attempts":      maxAttempts,
		"trace_id":          options.TraceID,
		// expert_limit 让"expert 并发闸门是否真的生效"永远可见（G6）：
		// unlimited / 十进制上限。0 与 -1 都是 unlimited，但语义不同——
		// -1 是显式不限，0 是历史遗留写法（配置校验会给出告警）。
		"expert_limit": modelrouting.ExpertLimitLabel(s.config.Routing),
	}
	if options.BatchID != "" {
		base["batch_id"] = options.BatchID
	}
	s.parent.emitRuntimeEvent(runtimeevents.EventSubagentRouteResolved, options.ParentSessionID, "", buildSubagentRouteResolvedPayload(base, spec.Decision))
}
