package usageanalytics

import (
	"strings"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// 子代理完成原因（completion_reason）：与 §5.2 映射表一致。
const (
	SubagentCompletionCompleted = "completed"
	SubagentCompletionFailed    = "failed"
	SubagentCompletionStopped   = "stopped"
	SubagentCompletionUnknown   = "unknown"
)

// 生产者来源标记（source）。
const (
	SubagentSourceScheduler       = "scheduler"
	SubagentSourceAgentController = "agent_controller"
)

// SubagentCompletion 是 subagent.completed 事件归一化后的载荷契约（D4/§5.2）。
//
// 两个生产者（agent/scheduler 写 success bool；api/skills 镜像写 status string）
// 与 ingest 读取侧共用这一处归一化实现；历史数据缺字段时以 unknown 单列，
// 绝不把 unknown 计入失败。
type SubagentCompletion struct {
	SubagentID      string
	ParentSessionID string
	ChildSessionID  string
	Role            string
	// TaskType/TaskSubject（schema v7 增量列）：子代理分类轴与短说明，
	// 由生产方在 subagent.completed / subagent.route.resolved 等事件载荷上附带；
	// 历史载荷缺字段时留空串（不猜测、不从 role 反推）。
	TaskType         string
	TaskSubject      string
	ReadOnly         bool
	ReadOnlyKnown    bool
	Success          *bool
	CompletionReason string
	FailureCategory  string
	ErrorCode        string
	Attempt          int
	MaxAttempts      int
	RetryReason      string
	RetryAdvice      string
	DurationMS       int64
	StartedAt        time.Time
	CompletedAt      time.Time
	UsageTotalTokens int64
	Source           string
	// Conflict 报告 success 与 status 语义冲突（按 D4 以 success 为准）。
	Conflict bool
}

// NormalizeSubagentCompletion 把事件载荷归一化为统一契约。
// 第二个返回值表示载荷是否可识别为一次子代理完成（至少含一个标识字段）。
func NormalizeSubagentCompletion(event runtimeevents.Event) (SubagentCompletion, bool) {
	payload := event.Payload
	normalized := SubagentCompletion{
		SubagentID:      firstPayloadString(payload, "subagent_id", "agent_id", "id"),
		ParentSessionID: firstPayloadString(payload, "parent_session_id", "root_session_id"),
		ChildSessionID:  firstPayloadString(payload, "child_session_id", "session_id", "agent_id"),
		Role:            firstPayloadString(payload, "role", "agent_type", "target_role"),
		TaskType:        firstPayloadString(payload, "task_type"),
		TaskSubject:     firstPayloadString(payload, "task_subject"),
		ErrorCode:       firstPayloadString(payload, "error_code"),
		RetryReason:     firstPayloadString(payload, "retry_reason"),
		RetryAdvice:     firstPayloadString(payload, "retry_advice"),
		Source:          firstPayloadString(payload, "source"),
	}
	if normalized.ParentSessionID == "" {
		normalized.ParentSessionID = strings.TrimSpace(event.SessionID)
	}
	if normalized.ChildSessionID == "" {
		normalized.ChildSessionID = normalized.SubagentID
	}
	if normalized.SubagentID == "" {
		normalized.SubagentID = normalized.ChildSessionID
	}
	if normalized.SubagentID == "" && normalized.Role != "" && normalized.ParentSessionID != "" {
		// 缺 id 时用 child_session_id+role 派生稳定 ID（§5.2 批次 2.2）。
		normalized.SubagentID = normalized.ChildSessionID + ":" + normalized.Role
	}
	if normalized.SubagentID == "" || normalized.ParentSessionID == "" {
		return SubagentCompletion{}, false
	}
	if normalized.Source == "" {
		if _, hasControlAction := payload["control_action"]; hasControlAction {
			normalized.Source = SubagentSourceAgentController
		} else if strings.EqualFold(strings.TrimSpace(event.AgentName), "agent-controller") {
			normalized.Source = SubagentSourceAgentController
		} else {
			normalized.Source = SubagentSourceScheduler
		}
	}

	success, hasSuccess := payloadBool(payload, "success")
	status := strings.ToLower(firstPayloadString(payload, "status", "completion_reason"))
	// 中间重试尝试（§6.2 的 attempt 事件）在分析侧按 conflict_count 计，
	// 不作为最终结果；最终结果由最后一次尝试覆盖。
	if intermediate, hasIntermediate := payloadBoolValue(payload, "intermediate_attempt"); hasIntermediate && intermediate {
		normalized.Conflict = true
	}
	normalized.Success = success
	if success != nil {
		normalized.CompletionReason = SubagentCompletionCompleted
		if !*success {
			normalized.CompletionReason = SubagentCompletionFailed
		}
	}
	if status != "" {
		statusReason := completionReasonFromStatus(status)
		if !hasSuccess {
			normalized.CompletionReason = statusReason
			flag := statusReason == SubagentCompletionCompleted
			normalized.Success = &flag
		} else if successReasonConflicts(*success, statusReason) {
			normalized.Conflict = true
		}
	}
	if normalized.CompletionReason == "" {
		normalized.CompletionReason = SubagentCompletionUnknown
	}
	if normalized.Success == nil {
		// 历史行（26 行）两者都缺：unknown 单列，不得计入失败。
		normalized.CompletionReason = SubagentCompletionUnknown
	}

	failureCategory := llm.NormalizeFailureCategory(firstPayloadString(payload, "failure_category"))
	if failureCategory == "" {
		failureCategory = llm.FailureCategoryFromErrorCode(normalized.ErrorCode)
	}
	if failureCategory == "" {
		failureCategory = llm.FailureCategoryUnknown
	}
	if normalized.Success != nil && *normalized.Success {
		failureCategory = ""
	} else if normalized.Success == nil {
		failureCategory = ""
	}
	normalized.FailureCategory = failureCategory

	normalized.ReadOnly, normalized.ReadOnlyKnown = payloadBoolValue(payload, "read_only")
	normalized.Attempt = payloadInt(payload, "attempt")
	if normalized.Attempt <= 0 {
		normalized.Attempt = 1
	}
	normalized.MaxAttempts = payloadInt(payload, "max_attempts")
	if normalized.MaxAttempts <= 0 {
		normalized.MaxAttempts = normalized.Attempt
	}
	normalized.DurationMS = payloadInt64(payload, "duration_ms", "duration")
	normalized.UsageTotalTokens = payloadInt64(payload, "usage_total_tokens", "total_tokens")
	normalized.StartedAt = payloadTime(payload, "started_at", "source_event_timestamp")
	normalized.CompletedAt = payloadTime(payload, "completed_at")
	return normalized, true
}

// NormalizeSubagentCompletionPayload 是**写侧**的共享归一化入口（方案 §2.1 /
// D4）：三个生产者（agent/scheduler、api/skills agent-controller 镜像、
// aicli 本地镜像）在发布 subagent.completed 前调用它补齐规范化字段。
//
// 语义：只补缺失字段、不覆盖生产者已显式写入的值（success 为权威字段，
// status 保留为兼容别名）；statusHint 用于在载荷无 status/success 时给出
// 生产者侧已知的终态。返回同一 map 以便链式调用。
func NormalizeSubagentCompletionPayload(payload map[string]interface{}, statusHint string) map[string]interface{} {
	if payload == nil {
		return payload
	}
	status := strings.ToLower(strings.TrimSpace(payloadString(payload, "status")))
	if status == "" {
		// 生产者只写了 success（scheduler 老形态）时，由 success 反推兼容别名。
		if flag, ok := payloadBoolValue(payload, "success"); ok {
			status = "failed"
			if flag {
				status = "completed"
			}
		}
	}
	if status == "" {
		status = strings.ToLower(strings.TrimSpace(statusHint))
	}
	if status == "" {
		status = "idle"
	}
	if strings.TrimSpace(payloadString(payload, "status")) == "" {
		payload["status"] = status
	}

	success, hasSuccess := payloadBoolValue(payload, "success")
	if !hasSuccess {
		success = completionReasonFromStatus(status) == SubagentCompletionCompleted
		payload["success"] = success
	}
	reason := completionReasonFromStatus(status)
	if _, exists := payload["completion_reason"]; !exists && strings.TrimSpace(payloadString(payload, "completion_reason")) == "" {
		payload["completion_reason"] = reason
	}
	// source 由生产者显式标注（scheduler / agent_controller）；读取侧在缺失时
	// 才按事件来源推断，避免这里用默认值把生产者标错。
	if !success {
		if strings.TrimSpace(payloadString(payload, "failure_category")) == "" {
			category := llm.FailureCategoryFromErrorCode(payloadString(payload, "error_code"))
			if category == "" {
				category = llm.FailureCategoryUnknown
			}
			payload["failure_category"] = category
		}
	}
	if _, exists := payload["attempt"]; !exists {
		payload["attempt"] = 1
	}
	if _, exists := payload["max_attempts"]; !exists {
		payload["max_attempts"] = payload["attempt"]
	}
	return payload
}

// completionReasonFromStatus 实现 §5.2 的状态映射表。
func completionReasonFromStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "idle", "completed", "success", "succeeded", "done":
		return SubagentCompletionCompleted
	case "stopped", "interrupted", "canceled", "cancelled":
		return SubagentCompletionStopped
	case "failed", "error", "failure":
		return SubagentCompletionFailed
	case "timeout", "timed_out":
		return SubagentCompletionFailed
	default:
		return SubagentCompletionUnknown
	}
}

// successReasonConflicts 报告 success bool 与 status 推导出的终态是否冲突。
func successReasonConflicts(success bool, statusReason string) bool {
	if statusReason == SubagentCompletionUnknown {
		return false
	}
	if success {
		return statusReason != SubagentCompletionCompleted
	}
	return statusReason == SubagentCompletionCompleted
}

// ---------------------------------------------------------------------------
// 载荷读取辅助（与 ingest.go 的 payloadString 同语义；这里保持局部，避免
// 归一化函数被 ingest 内部状态耦合）。
// ---------------------------------------------------------------------------

func firstPayloadString(payload map[string]interface{}, keys ...string) string {
	return payloadString(payload, keys...)
}

func payloadBool(payload map[string]interface{}, keys ...string) (*bool, bool) {
	value, ok := payloadBoolValue(payload, keys...)
	if !ok {
		return nil, false
	}
	return &value, true
}

func payloadBoolValue(payload map[string]interface{}, keys ...string) (bool, bool) {
	for _, key := range keys {
		raw, ok := payload[key]
		if !ok || raw == nil {
			continue
		}
		switch value := raw.(type) {
		case bool:
			return value, true
		case string:
			text := strings.ToLower(strings.TrimSpace(value))
			if text == "true" || text == "1" {
				return true, true
			}
			if text == "false" || text == "0" {
				return false, true
			}
		case float64:
			return value != 0, true
		case int:
			return value != 0, true
		case int64:
			return value != 0, true
		}
	}
	return false, false
}

func payloadInt64(payload map[string]interface{}, keys ...string) int64 {
	for _, key := range keys {
		raw, ok := payload[key]
		if !ok || raw == nil {
			continue
		}
		switch value := raw.(type) {
		case int:
			return int64(value)
		case int64:
			return value
		case float64:
			return int64(value)
		}
	}
	return 0
}

func payloadTime(payload map[string]interface{}, keys ...string) time.Time {
	for _, key := range keys {
		raw, ok := payload[key]
		if !ok || raw == nil {
			continue
		}
		switch value := raw.(type) {
		case time.Time:
			return value.UTC()
		case string:
			text := strings.TrimSpace(value)
			if text == "" {
				continue
			}
			for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
				if parsed, err := time.Parse(layout, text); err == nil {
					return parsed.UTC()
				}
			}
		case float64:
			return time.Unix(0, int64(value)).UTC()
		case int64:
			return time.Unix(0, value).UTC()
		}
	}
	return time.Time{}
}
