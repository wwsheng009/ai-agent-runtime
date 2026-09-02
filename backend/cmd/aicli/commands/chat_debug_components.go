package commands

import (
	"fmt"
	"strings"

	runtimeobserve "github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

// appendChatDebugComponentLines 输出"组件:"子区块：运行时宿主核心组件（存在性
// 与就绪状态）以及观察平面组件（配置态）。HTTP 端点的完整清单（loopback +
// runtime-observe）统一在"HTTP 调试端点:"区块中列出，此处只显示组件状态。
func appendChatDebugComponentLines(builder *chatDebugDocumentBuilder, session *ChatSession) {
	builder.heading("运行时组件: (GET /debug/chat/status#components)")
	host := localChatRuntimeHostOf(session)
	if descriptor, ok := chatRuntimeExecutorDescriptor(session.ChatExecutor); ok {
		transport := strings.TrimSpace(descriptor.Transport)
		if transport == "" {
			transport = "in-process"
		}
		builder.meta("Runtime Core:", fmt.Sprintf("%s v%d transport=%s",
			descriptor.Core.Name, descriptor.Core.ContractVersion, transport))
	} else {
		builder.meta("Runtime Core:", "<none>")
	}
	chatDebugComponentReadyMeta(builder, "Actor Registry:", host != nil && host.ActorRegistry != nil, "")
	activeSessions := 0
	if host != nil && host.SessionHub != nil {
		activeSessions = len(host.SessionHub.ActiveSessionIDs(4096))
	}
	chatDebugComponentReadyMeta(builder, "Session Hub:", host != nil && host.SessionHub != nil, fmt.Sprintf("%d active", activeSessions))
	chatDebugComponentReadyMeta(builder, "Event Bus:", host != nil && host.EventBus != nil, "")
	chatDebugComponentReadyMeta(builder, "Event Store:", host != nil && host.EventStore != nil, "")
	chatDebugComponentReadyMeta(builder, "Supervision:", host != nil && host.Supervision != nil, "")
	chatDebugComponentReadyMeta(builder, "Team Store:", host != nil && host.TeamStore != nil, "")
	chatDebugComponentReadyMeta(builder, "Agent Control:", host != nil && host.AgentControl != nil, "")
	chatDebugComponentReadyMeta(builder, "Skills/MCP Surface:", host != nil && host.ToolSurface != nil, "")
	chatDebugComponentReadyMeta(builder, "Background:", host != nil && host.Background != nil, "")

	observe, ok := chatSessionObserveConfig(session)
	if !ok {
		return
	}
	observeStatus := "未启用"
	if observe.Enabled {
		observeStatus = "ready"
	}
	chatDebugObserveMeta(builder, "Observe Service:", observe.Enabled, observeStatus, "runtime-server 提供 HTTP 平面")
	chatDebugObserveMeta(builder, "Observe Collector:", observe.Enabled, observeStatus,
		fmt.Sprintf("retention=%d events / %d bytes / %s", observe.RetentionEvents, observe.RetentionBytes, observe.RetentionTTL))
	chatDebugObserveMeta(builder, "Observe Projector:", observe.Enabled, observeStatus,
		fmt.Sprintf("event_max=%d bytes snapshot_max=%d bytes query=%d..%d",
			observe.MaxEventBytes, observe.MaxSnapshotBytes, observe.DefaultQueryLimit, observe.MaxQueryLimit))
	chatDebugObserveMeta(builder, "Observe Redactor:", observe.Enabled, observeStatus,
		fmt.Sprintf("profile=%s key_ref=%s", observe.RedactionProfile, observe.HMACKeyRef))
	chatDebugObserveMeta(builder, "Observe Ingress:", observe.Enabled, observeStatus,
		fmt.Sprintf("%d events / %d bytes", observe.IngressQueueEvents, observe.IngressQueueBytes))
}

// localChatRuntimeHostOf 返回会话的本地运行时宿主（可能为 nil）。
func localChatRuntimeHostOf(session *ChatSession) *localChatRuntimeHost {
	if session == nil {
		return nil
	}
	return session.LocalRuntimeHost
}

// chatDebugObserveMeta 渲染观察平面组件行：启用时显示就绪状态与配置细节，
// 未启用时显示"未启用"（不展示细节，避免把配置态误当运行态）。
func chatDebugObserveMeta(builder *chatDebugDocumentBuilder, label string, enabled bool, status string, detail string) {
	if !enabled {
		builder.meta(label, "未启用")
		return
	}
	value := status
	if strings.TrimSpace(detail) != "" {
		value += " (" + strings.TrimSpace(detail) + ")"
	}
	builder.meta(label, value)
}

// chatDebugComponentReadyMeta 渲染单个组件行：就绪时显示 ready（可附带细节），
// 否则显示"未配置"。
func chatDebugComponentReadyMeta(builder *chatDebugDocumentBuilder, label string, ready bool, detail string) {
	value := "未配置"
	if ready {
		value = "ready"
		if strings.TrimSpace(detail) != "" {
			value += " (" + strings.TrimSpace(detail) + ")"
		}
	}
	builder.meta(label, value)
}

// chatSessionObserveConfig 返回当前会话运行时宿主的观察平面配置；宿主或运行时
// 配置尚未建立时返回 ok=false。
func chatSessionObserveConfig(session *ChatSession) (runtimeobserve.Config, bool) {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.RuntimeConfig == nil {
		return runtimeobserve.Config{}, false
	}
	return session.LocalRuntimeHost.RuntimeConfig.Observe, true
}

// chatObserveBaseURL 返回观察平面实际可访问的基础地址（含 scheme 与 host，
// 末尾不带 /）。仅当当前会话的 executor 是已连接的 runtime-server 客户端且
// 地址为合法 http(s) URL 时才返回非空；本地 in-process 模式无对外 HTTP 平面，
// 返回空串（调用方回退展示 route= 前缀）。
func chatObserveBaseURL(session *ChatSession) string {
	if session == nil {
		return ""
	}
	provider, ok := session.ChatExecutor.(runtimeServerURLProvider)
	if !ok {
		return ""
	}
	base := strings.TrimRight(strings.TrimSpace(provider.RuntimeServerURL()), "/")
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return ""
	}
	return base
}
