package commands

import (
	"fmt"
	"strings"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/buildinfo"
)

// ---------------------------------------------------------------------------
// 端点路径常量
// ---------------------------------------------------------------------------

const (
	ChatWebPath                   = "/web/"
	ChatWebAPIScreenPath          = "/web/api/screen"
	ChatWebAPIStatusPath          = "/web/api/status"
	ChatWebAPIEventsPath          = "/web/api/events"
	ChatWebAPIInputPath           = "/web/api/input"
	ChatWebAPISchemaPath          = "/web/api/events/schema"
	ChatWebAPISessionsPath        = "/web/api/sessions"
	ChatWebAPISessionsResumePath  = "/web/api/sessions/resume"
)

// chatWebSchemaVersion 是 SSE 事件 data 中 _event.schema_version 字段的值。
const chatWebSchemaVersion = "skill_runtime.sse.v1"

// ---------------------------------------------------------------------------
// EventBus → SSE 事件名称映射（§5.1）
// ---------------------------------------------------------------------------

// chatWebSSEMapping 描述一条 EventBus → SSE 事件映射。
type chatWebSSEMapping struct {
	BusEvent string // EventBus 原始事件类型
	SSEEvent string // 对外暴露的 SSE event 名称
	Desc     string // 中文描述
}

// chatWebSSEMappings 按 §5.1 表定义全部映射。
// 合成事件（connected / heartbeat / screen_refresh / error）不在此表中。
var chatWebSSEMappings = []chatWebSSEMapping{
	{BusEvent: runtimechat.EventSessionStart, SSEEvent: "session_start", Desc: "会话开始"},
	{BusEvent: runtimechat.EventSessionEnd, SSEEvent: "session_end", Desc: "会话结束"},
	{BusEvent: runtimechat.EventSessionInterrupted, SSEEvent: "session_interrupted", Desc: "会话中断"},
	{BusEvent: runtimechat.EventLLMRequestStarted, SSEEvent: "turn_start", Desc: "LLM 请求开始（turn 开始）"},
	{BusEvent: "llm.request.started", SSEEvent: "turn_start", Desc: "LLM 请求开始（turn 开始，技能 handler 点号名）"},
	{BusEvent: runtimechat.EventAssistantDelta, SSEEvent: "assistant_delta", Desc: "流式文本增量"},
	{BusEvent: runtimechat.EventAssistantReasoningDelta, SSEEvent: "reasoning_delta", Desc: "推理增量"},
	{BusEvent: runtimechat.EventAssistantReasoning, SSEEvent: "reasoning_delta", Desc: "推理增量（旧别名）"},
	{BusEvent: runtimechat.EventAssistantMessage, SSEEvent: "assistant_message", Desc: "助手完整消息"},
	{BusEvent: runtimechat.EventAssistantImageProgress, SSEEvent: "assistant_image_progress", Desc: "图像生成进度"},
	{BusEvent: runtimechat.EventLLMRequestFinished, SSEEvent: "turn_end", Desc: "LLM 请求完成"},
	{BusEvent: "llm.request.finished", SSEEvent: "turn_end", Desc: "LLM 请求完成（技能 handler 点号名）"},
	{BusEvent: runtimechat.EventToolStarted, SSEEvent: "tool_start", Desc: "工具调用开始"},
	{BusEvent: runtimechat.EventToolFinished, SSEEvent: "tool_end", Desc: "工具调用完成"},
	{BusEvent: runtimechat.EventApprovalRequested, SSEEvent: "approval_requested", Desc: "审批请求"},
	{BusEvent: runtimechat.EventApprovalResolved, SSEEvent: "approval_resolved", Desc: "审批已处理"},
	{BusEvent: runtimechat.EventQuestionAsked, SSEEvent: "question_asked", Desc: "询问用户"},
	{BusEvent: runtimechat.EventQuestionAnswered, SSEEvent: "question_answered", Desc: "用户已回答"},
	{BusEvent: runtimechat.EventCheckpointCreated, SSEEvent: "checkpoint_created", Desc: "Checkpoint 创建"},
	{BusEvent: runtimechat.EventSessionCompactStarted, SSEEvent: "compact_start", Desc: "会话压缩开始"},
	{BusEvent: runtimechat.EventSessionCompactCompleted, SSEEvent: "compact_end", Desc: "会话压缩完成"},
	{BusEvent: runtimechat.EventSessionCompactSkipped, SSEEvent: "compact_skipped", Desc: "会话压缩跳过"},
	{BusEvent: runtimechat.EventSessionCompactFailed, SSEEvent: "compact_failed", Desc: "会话压缩失败"},
	{BusEvent: runtimechat.EventRewindStarted, SSEEvent: "rewind_start", Desc: "回退开始"},
	{BusEvent: runtimechat.EventRewindFinished, SSEEvent: "rewind_end", Desc: "回退完成"},
	{BusEvent: runtimechat.EventBacktrackStarted, SSEEvent: "backtrack_start", Desc: "回溯开始"},
	{BusEvent: runtimechat.EventBacktrackFinished, SSEEvent: "backtrack_end", Desc: "回溯完成"},
	{BusEvent: runtimechat.EventJobStarted, SSEEvent: "job_started", Desc: "Job 开始"},
	{BusEvent: runtimechat.EventJobOutput, SSEEvent: "job_output", Desc: "Job 输出"},
	{BusEvent: runtimechat.EventJobFinished, SSEEvent: "job_finished", Desc: "Job 完成"},
	{BusEvent: runtimechat.EventJobCancelled, SSEEvent: "job_cancelled", Desc: "Job 取消"},
	{BusEvent: runtimechat.EventMailboxReceived, SSEEvent: "mailbox_received", Desc: "邮箱消息"},
	{BusEvent: runtimechat.EventContextReconciled, SSEEvent: "context_reconciled", Desc: "上下文调和"},
}

// chatWebSSEEventName 将 EventBus 事件类型映射为 SSE event 名称。
// 如果已知映射则返回 (sseEvent, true)；否则返回 (rawEvent, false)。
func chatWebSSEEventName(busEvent string) (string, bool) {
	for _, m := range chatWebSSEMappings {
		if m.BusEvent == busEvent {
			return m.SSEEvent, true
		}
	}
	return busEvent, false
}

// ---------------------------------------------------------------------------
// SSE 事件数据组装（§5.1 字段映射 + §5.2 合成事件）
// ---------------------------------------------------------------------------

// chatWebSSEDataForEvent 从 EventBus 事件提取 SSE data 字段。
// 返回的 map 在写入 SSE 前会被套上 _event 信封。
func chatWebSSEDataForEvent(ev runtimeevents.Event) map[string]interface{} {
	payload := ev.Payload
	if payload == nil {
		payload = make(map[string]interface{})
	}
	// 通用字段
	data := make(map[string]interface{}, 16)
	if ev.SessionID != "" {
		data["session_id"] = ev.SessionID
	}
	if ev.TraceID != "" {
		data["trace_id"] = ev.TraceID
	}

	// 按事件类型提取映射表约定的字段
	switch ev.Type {
	case runtimechat.EventSessionStart, runtimechat.EventSessionEnd:
		pickField(data, payload, "turn_id")
		pickField(data, payload, "session_id")

	case runtimechat.EventLLMRequestStarted, "llm.request.started":
		// turn_start
		pickField(data, payload, "turn_id")
		pickField(data, payload, "request_id")
		pickField(data, payload, "model")
		pickFieldDateTime(data, payload, "timestamp")

	case runtimechat.EventAssistantDelta:
		pickField(data, payload, "turn_id")
		pickField(data, payload, "stream_id")
		pickField(data, payload, "sequence")
		// 发布端（agent loop）把增量文本放在 "delta"（兼容 "content"）键，
		// 对外统一暴露为 "text"。
		if v, ok := payload["delta"]; ok && fmt.Sprint(v) != "" {
			data["text"] = v
		} else if v, ok := payload["content"]; ok && fmt.Sprint(v) != "" {
			data["text"] = v
		} else {
			pickField(data, payload, "text")
		}

	case runtimechat.EventAssistantReasoningDelta, runtimechat.EventAssistantReasoning:
		pickField(data, payload, "turn_id")
		pickField(data, payload, "stream_id")
		pickField(data, payload, "sequence")
		// 发布端把推理增量放在 "reasoning"（ReasoningBlock.ToMap()）嵌套对象里，
		// 对外统一暴露为 "content"。
		if v, ok := payload["reasoning"]; ok {
			if s, ok := chatWebReasoningText(v); ok {
				data["content"] = s
			}
		} else if v, ok := payload["content"]; ok && fmt.Sprint(v) != "" {
			data["content"] = v
		}

	case runtimechat.EventAssistantMessage:
		pickField(data, payload, "turn_id")
		pickField(data, payload, "content")

	case runtimechat.EventAssistantImageProgress:
		pickField(data, payload, "turn_id")
		pickField(data, payload, "status")
		// 透传 image 元数据（phase/image_id/response_id 等；含 URL/base64 时
		// 前端可直接预览，否则仅作进度提示）。
		pickField(data, payload, "image")

	case runtimechat.EventLLMRequestFinished, "llm.request.finished":
		// turn_end
		pickField(data, payload, "turn_id")
		pickField(data, payload, "request_id")
		pickField(data, payload, "finish_reason")
		pickField(data, payload, "usage")

	case runtimechat.EventToolStarted:
		pickField(data, payload, "turn_id")
		pickField(data, payload, "tool_name")
		pickField(data, payload, "tool_call_id")
		pickField(data, payload, "arguments")

	case runtimechat.EventToolFinished:
		pickField(data, payload, "turn_id")
		pickField(data, payload, "tool_name")
		pickField(data, payload, "tool_call_id")
		pickField(data, payload, "result_summary")

	case runtimechat.EventApprovalRequested:
		pickField(data, payload, "turn_id")
		pickField(data, payload, "request_id")
		pickField(data, payload, "tool_name")
		pickField(data, payload, "prompt")

	case runtimechat.EventApprovalResolved:
		pickField(data, payload, "turn_id")
		pickField(data, payload, "request_id")
		pickField(data, payload, "allowed")

	case runtimechat.EventQuestionAsked:
		pickField(data, payload, "turn_id")
		pickField(data, payload, "question_id")
		pickField(data, payload, "prompt")
		pickField(data, payload, "suggestions")

	case runtimechat.EventQuestionAnswered:
		pickField(data, payload, "turn_id")
		pickField(data, payload, "question_id")
		pickField(data, payload, "answer")

	case runtimechat.EventCheckpointCreated:
		pickField(data, payload, "turn_id")
		pickField(data, payload, "checkpoint_id")

	case runtimechat.EventSessionCompactStarted,
		runtimechat.EventSessionCompactCompleted,
		runtimechat.EventSessionCompactSkipped,
		runtimechat.EventSessionCompactFailed:
		pickField(data, payload, "turn_id")

	case runtimechat.EventRewindStarted, runtimechat.EventRewindFinished:
		pickField(data, payload, "turn_id")

	case runtimechat.EventBacktrackStarted, runtimechat.EventBacktrackFinished:
		pickField(data, payload, "turn_id")

	case runtimechat.EventJobStarted, runtimechat.EventJobOutput,
		runtimechat.EventJobFinished, runtimechat.EventJobCancelled:
		pickField(data, payload, "job_id")
		pickField(data, payload, "turn_id")

	case runtimechat.EventContextReconciled:
		pickField(data, payload, "turn_id")
		pickField(data, payload, "reason")

	default:
		// 未识别的类型：复制整个 payload 供前端日志
		for k, v := range payload {
			data[k] = v
		}
	}
	return data
}

func pickField(m map[string]interface{}, payload map[string]interface{}, key string) {
	if v, ok := payload[key]; ok {
		m[key] = v
	}
}

func pickFieldDateTime(m map[string]interface{}, payload map[string]interface{}, key string) {
	if v, ok := payload[key]; ok {
		switch t := v.(type) {
		case time.Time:
			m[key] = t.UTC().Format(time.RFC3339)
		default:
			m[key] = v
		}
	}
}

// chatWebReasoningText 从 ReasoningBlock.ToMap() 产物（map 或 string）提取可展示的推理文本。
// 优先找 "summary"（stream_delta 格式），其次找 "content"。
func chatWebReasoningText(v interface{}) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, t != ""
	case map[string]interface{}:
		for _, key := range []string{"summary", "content"} {
			if s, ok := t[key].(string); ok && s != "" {
				return s, true
			}
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// 合成事件 payload 构建
// ---------------------------------------------------------------------------

// chatWebConnectedPayload 构建 connected 事件的 data 字段（§5.2）。
func chatWebConnectedPayload(session *ChatSession) map[string]interface{} {
	payload := map[string]interface{}{
		"session_active": false,
		"session_id":     "",
		"session_busy":   false,
		"turn_id":        "",
		"last_sequence":  0,
		"server_version": chatWebServerVersion(),
	}
	if session == nil || session.RuntimeSession == nil {
		return payload
	}
	sessionID := currentRuntimeSessionID(session)
	payload["session_active"] = sessionID != ""
	payload["session_id"] = sessionID

	if actor := chatWebSessionActor(session); actor != nil {
		state := actor.State()
		if state != nil {
			payload["turn_id"] = state.CurrentTurnID
			payload["session_busy"] = state.Summary().Busy()
			if state.PendingApproval != nil {
				payload["pending_approval"] = map[string]interface{}{
					"request_id": state.PendingApproval.ID,
					"tool_name":  state.PendingApproval.ToolName,
					"prompt":     state.PendingApproval.Reason,
				}
			}
			if state.PendingQuestion != nil {
				payload["pending_question"] = map[string]interface{}{
					"question_id": state.PendingQuestion.ID,
					"prompt":      state.PendingQuestion.Prompt,
					"suggestions": state.PendingQuestion.Suggestions,
				}
			}
		}
	}
	return payload
}

// chatWebServerVersion 返回用于 server_version 字段的版本字符串。
func chatWebServerVersion() string {
	version := strings.TrimSpace(buildinfo.Backend().Version)
	if version == "" {
		return "aicli/dev"
	}
	return "aicli/" + version
}