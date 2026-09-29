package commands

import (
	"context"
	"strings"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

const (
	chatEventInputQueueDetected  = "input.queue.detected"
	chatEventInputQueueDiscarded = "input.queue.discarded"
	chatEventInputQueueDrained   = "input.queue.drained"
	chatInputQueueAgentName      = "aicli-input-queue"
	// chatEventRuntimeInteraction 是统一运行时交互的审计事件（P2-3/T33）：
	// 每次宿主交互记录 命令/模式/生效域/结果/耗时。
	chatEventRuntimeInteraction = "aicli.chat.runtime_interaction"
)

// isChatInputQueueDiagnosticEvent 判断事件是否为输入队列本地诊断事件
// （排队输入被接收/丢弃/排空）。
//
// 这些事件是 chat 进程内部编排诊断，只对 TUI timeline 与调试日志有意义：
// 统一渲染编码器对未映射事件会按 KindSystem 兜底 append 进 Scene
// （transcript），导致 web 消息信息流出现 "queued input drained" 等系统
// 消息噪声、干扰真正的用户/助手消息。渲染数据面（encoder）应跳过它们，
// eventLog / timeline / SSE 转发保持原样。
func isChatInputQueueDiagnosticEvent(eventType string) bool {
	switch eventType {
	case chatEventInputQueueDetected, chatEventInputQueueDiscarded, chatEventInputQueueDrained:
		return true
	}
	return false
}

// isChatRenderDataPlaneSuppressedEvent 判断事件是否应被挡在统一渲染
// 数据面（Scene/transcript → web 消息信息流）之外。
//
// 目前包含两类：
//   - input.queue.* 输入队列本地诊断事件：只对 TUI timeline / 调试日志
//     有意义，编码器对未映射事件按 KindSystem 兜底 append 会产生
//     "queued input drained" 系统消息噪声；
//   - aicli.chat.dynamic_status 动态状态栏镜像事件：其消费端是 SSE
//     "dynamic_status" 转发（web 状态栏），进入 Scene 只会产生
//     KindSystem 单元格、以 "aicli.chat.dynamic_status" 系统消息身份
//     污染消息信息流；
//   - aicli.chat.user_submitted 用户输入提交镜像事件：其消费端是 SSE
//     "screen_refresh" 映射（前端立即重拉 /web/api/screen 确认 pending
//     气泡），用户 cell 已由 submitUserInput 经 SubmitUserInput 注入，
//     再次 Encode 只会产生 KindSystem 单元格噪声。
//
// 被抑制的事件仍会写入事件日志（eventLog / replay / TUI timeline）并
// 经 SSE 转发，只影响渲染数据面。
func isChatRenderDataPlaneSuppressedEvent(eventType string) bool {
	return isChatInputQueueDiagnosticEvent(eventType) || eventType == chatEventRuntimeInteraction ||
		eventType == chatWebDynamicStatusBusEvent || eventType == chatWebUserSubmittedBusEvent ||
		isLSPObservationBusEvent(eventType)
}

// isLSPObservationBusEvent 返回 LSP 观测事件（方案 §3/§4.3）：
//   - lsp.request.finished  请求级埋点，基线（离线脚本 / /lsp baseline / 页签 F）的
//     唯一事实源，必须落事件日志；
//   - lsp.server.state / lsp.diagnostics.updated  池生命周期与诊断发布。
//
// 它们与轮次无关（池在工具执行期与会话绑定发布，载荷无 turn_id），只应进入事件
// 日志与 observe/SSE 平面；进入渲染数据面只会以未映射类型的 KindSystem 单元格
// 污染消息流（与 aicli.chat.dynamic_status 同类）。
func isLSPObservationBusEvent(eventType string) bool {
	switch eventType {
	case runtimeevents.EventLSPRequestFinished,
		runtimeevents.EventLSPServerState,
		runtimeevents.EventLSPDiagnosticsUpdated:
		return true
	default:
		return false
	}
}

func publishLocalChatDiagnosticEvent(session *ChatSession, eventType string, payload map[string]interface{}) {
	if session == nil || session.LocalRuntimeHost == nil || strings.TrimSpace(eventType) == "" {
		return
	}
	sessionID := ""
	if session.RuntimeSession != nil {
		sessionID = strings.TrimSpace(session.RuntimeSession.ID)
	}
	if sessionID == "" {
		return
	}
	event := runtimeevents.Event{
		Type:      strings.TrimSpace(eventType),
		AgentName: chatInputQueueAgentName,
		SessionID: sessionID,
		Payload:   payload,
		Timestamp: time.Now().UTC(),
	}
	if session.LocalRuntimeHost.EventStore != nil {
		if seq, err := session.LocalRuntimeHost.EventStore.AppendEvent(context.Background(), event); err == nil {
			if event.Payload == nil {
				event.Payload = map[string]interface{}{}
			}
			event.Payload["seq"] = seq
		}
	}
	if session.LocalRuntimeHost.EventBus != nil {
		session.LocalRuntimeHost.EventBus.Publish(event)
	}
}
