package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/buildinfo"
	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// ---------------------------------------------------------------------------
// 端点路径常量
// ---------------------------------------------------------------------------

const (
	ChatWebPath = "/web/"
	// ChatWebAPIHealthPath 是网格存活探针（架构 §5.2）：极轻量、不依赖会话与
	// 渲染器，无会话时同样 200；供网格探活、外部脚本就绪等待与 doctor 复用。
	ChatWebAPIHealthPath = "/web/api/health"
	// ChatWebAPIMeshSelfPath / ChatWebAPIMeshPeersPath 是网格控制面的只读端点
	// （架构 §5.3 / §5.4）：self=本节点自述，peers=网格聚合视图——也就是
	// `aicli-mesh ls` 的同一数据源（§7.5）。--mesh=false 时两条路由不注册（§9.7）。
	ChatWebAPIMeshSelfPath  = "/web/api/mesh/self"
	ChatWebAPIMeshPeersPath = "/web/api/mesh/peers"
	// ChatWebAPIMeshEventsPath 是网格实时事件流（SSE，架构 §5.5 / §6）：本节点
	// 作为扇入点，把 peer 事件并入本进程的流，浏览器只连自己的进程（§6.5）。
	// 路径与 internal/mesh 的订阅端共用同一常量，避免两侧漂移。
	ChatWebAPIMeshEventsPath = mesh.ChatWebMeshEventsPath
	// ChatWebAPIMeshCallPath 是网格调用端点（架构 §5.6）：调用方（CLI /
	// 其它节点 / 前端）以目标档案里的令牌发起，op 白名单与写操作
	// allow_write 由被调方校验。与 internal/mesh 的调用方共用同一常量。
	ChatWebAPIMeshCallPath = mesh.ChatWebMeshCallPath
	// ChatWebAPIMeshSpawnPath 是网格拉起端点（架构 §5.7）：四态
	// reused/started/not_running/failed，令牌只出现在返回的 url 里（M7）。
	ChatWebAPIMeshSpawnPath = mesh.ChatWebMeshSpawnPath
	// ChatWebAPIMeshStopPath 是网格停止端点（架构 §5.7）：graceful 投 /exit
	// 等目标自己收尾、force 终止进程；治理动作默认关（--mesh-allow-stop）。
	// 与 internal/mesh 的调用方共用同一常量。
	ChatWebAPIMeshStopPath  = mesh.ChatWebMeshStopPath
	ChatWebAPIScreenPath    = "/web/api/screen"
	ChatWebAPIStatusPath    = "/web/api/status"
	ChatWebAPIStatusBarPath = "/web/api/statusbar"
	ChatWebAPIEventsPath    = "/web/api/events"
	ChatWebAPIInputPath     = "/web/api/input"
	// ChatWebAPIInvokePath 是同步远程调用端点：一次请求内完成
	// "注入 prompt → 等待 turn 结束 → 返回状态与渲染"，供脚本/外部 Agent
	// 直接远程调用 aicli chat TUI（与异步的 /web/api/input 互补）。
	ChatWebAPIInvokePath         = "/web/api/invoke"
	ChatWebAPITurnPath           = "/web/api/turn"
	ChatWebAPISchemaPath         = "/web/api/events/schema"
	ChatWebAPISessionsPath       = "/web/api/sessions"
	ChatWebAPISessionsNewPath    = "/web/api/sessions/new"
	ChatWebAPISessionsResumePath = "/web/api/sessions/resume"
	ChatWebAPISessionsDeletePath = "/web/api/sessions/delete"
	ChatWebAPISessionsRenamePath = "/web/api/sessions/rename"
	// ChatWebAPIAnalysisPath 「分析」页签端点前缀（runtime.analytics.v1 契约，
	// 与 runtime-server /api/runtime/analytics/* 同一查询层、同一字段名）。
	// 子路径：/status、/tools、/subagents、/errors、/routing、/routing/events；
	// v1 不新增 SSE 事件（页签激活时按需拉取，见 web/js/analysis.js）。
	ChatWebAPIAnalysisPath = "/web/api/analysis"
	// ChatWebAPIMCPsPath MCP 管理端点前缀（MCP 页签）：
	//   GET/POST /web/api/mcps
	//   POST     /web/api/mcps/reload
	//   GET/PUT/DELETE /web/api/mcps/{name}
	//   POST     /web/api/mcps/{name}/enable|disable
	// 写操作由 ChatWebAuthGuard 统一要求写令牌；配置与 CLI、runtime-server 共用
	// internal/mcp/admin 同一套读写实现。
	ChatWebAPIMCPsPath = "/web/api/mcps"
	// ChatWebAPIExportPath 会话导出下载端点（顶部菜单栏「文件 → 导出会话」）：
	// GET /web/api/export?format=full|body|tools|trace[&session_id=<id>]，
	// 与 TUI /export、顶层 `aicli export` 共用同一套写出实现；只读端点，
	// 回环模式免写令牌（非回环模式由页面注入的 fetch 包装附 X-AICLI-Token）。
	ChatWebAPIExportPath = "/web/api/export"
)

// chatWebSchemaVersion 是 SSE 事件 data 中 _event.schema_version 字段的值。
const chatWebSchemaVersion = "skill_runtime.sse.v1"

// chatWebDynamicStatusBusEvent 是动态状态栏更新事件的 EventBus 类型名：
// chatInteractionCoordinator 在 TUI 动态状态行变化（开始/切换/结束）时发布，
// 经 chatWebSSEMappings 映射为 SSE "dynamic_status" 事件，web 客户端据此
// 同步显示 aicli chat 底部的活动状态行（Retrying / Analyzing / Running …）。
const chatWebDynamicStatusBusEvent = "aicli.chat.dynamic_status"

// chatWebModelSelectionChangedBusEvent 是 provider/model/reasoning 切换事件
// 的 EventBus 类型名：applyChatExecutionContext（provider/model 维度）与
// applyReasoningEffortCommandSelection（reasoning 单维度）落地后发布，经
// chatWebSSEMappings 映射为 SSE "model_changed" 事件，web 客户端据此重新
// 拉取 /web/api/runtime，补齐 TUI→web 的切换同步（web→TUI 方向由注入
// /model --direct + pollRuntimeMeta 轮询覆盖）。
const chatWebModelSelectionChangedBusEvent = "aicli.chat.model_selection_changed"

// chatWebUserSubmittedBusEvent 是用户输入提交镜像事件的 EventBus 类型名：
// chatRuntimeEventBridge.submitUserInput 在用户 cell 成功注入渲染数据面后
// 发布（renderMu 释放后发布，避免与桥自订阅 Handle 的 renderMu 重入死锁）。
// 经 chatWebSSEMappings 映射为 SSE "screen_refresh"，web 客户端据此立即
// 重拉 /web/api/screen 把 pending 气泡确认为已提交用户消息 —— 长 turn
// （无 tool_end / turn_end）时不再等到回合结束才刷新。
const chatWebUserSubmittedBusEvent = "aicli.chat.user_submitted"

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
// 合成事件（connected / heartbeat / error）不在此表中；screen_refresh 是
// 例外：除关键事件后附带合成（§8.6）外，aicli.chat.user_submitted 也映射
// 为 screen_refresh（见 chatWebUserSubmittedBusEvent），客户端复用同一
// 刷新路径，无需新增 JS 监听。
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
	{BusEvent: chatWebDynamicStatusBusEvent, SSEEvent: "dynamic_status", Desc: "动态状态栏更新（Retrying/Analyzing/Running…）"},
	{BusEvent: chatWebUserSubmittedBusEvent, SSEEvent: "screen_refresh", Desc: "用户输入提交（合成 screen_refresh：前端立即重拉确认 pending 气泡）"},
	{BusEvent: chatWebModelSelectionChangedBusEvent, SSEEvent: "model_changed", Desc: "provider/model/reasoning 切换落地"},
	{BusEvent: cacheanalytics.EventCacheRequestFinished, SSEEvent: "cache_request_finished", Desc: "LLM 缓存请求记录终态（cache.analytics.v1 投影，缓存页签增量刷新）"},
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
		pickField(data, payload, "mode")
		// 发布端把推理增量放在 "reasoning"（ReasoningBlock.ToMap()）嵌套对象里，
		// 对外统一暴露为 "content"，并补 "text" 别名（合帧帧只带 text）。
		if v, ok := payload["reasoning"]; ok {
			if s, ok := chatWebReasoningText(v); ok {
				data["content"] = s
				data["text"] = s
			}
		} else if v, ok := payload["content"]; ok && fmt.Sprint(v) != "" {
			data["content"] = v
			data["text"] = v
		}
		// 合帧（coalesced）形状：payload 只带顶层 "text"。
		if _, ok := data["content"]; !ok {
			if v, ok := payload["text"]; ok && fmt.Sprint(v) != "" {
				data["content"] = v
				data["text"] = v
			}
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

	case chatWebModelSelectionChangedBusEvent:
		// provider/model/reasoning 切换落地：web 客户端据此重新拉取
		// /web/api/runtime（字段仅作观测，权威值以 runtime 端点为准）。
		pickField(data, payload, "provider")
		pickField(data, payload, "model")
		pickField(data, payload, "reasoning_effort")
		pickField(data, payload, "base_url")

	case cacheanalytics.EventCacheRequestFinished:
		// cache_request_finished（§6.3 SSE 增量）：载荷为 CacheRequestRecord
		// 投影，字段与 /web/api/cache/requests 契约一致；web 客户端据此
		// 增量刷新缓存页签（防抖重拉 overview + requests）。
		pickField(data, payload, "llm_request_id")
		pickField(data, payload, "turn_id")
		pickField(data, payload, "step")
		pickField(data, payload, "provider")
		pickField(data, payload, "model")
		pickField(data, payload, "status")
		pickField(data, payload, "cache_status")
		pickField(data, payload, "cache_hit_ratio")
		pickField(data, payload, "cache_write_ratio")
		pickField(data, payload, "duration_ms")
		// first_token_ms：首字时间（TTFT），0=未采集（非流式/历史/首字前失败）。
		pickField(data, payload, "first_token_ms")
		pickField(data, payload, "error_category")
		pickField(data, payload, "user_message_id")
		pickField(data, payload, "assistant_message_id")
		pickField(data, payload, "correlation_source")
		pickField(data, payload, "usage")

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
