// Package eventbridge 把 LSP 池事件投影为运行时事件（lsp.*，live-only）。
//
// 投影规则与 internal/runtimeobserve 的字段白名单严格对齐：只发标量/短枚举
// （trigger/outcome/duration_ms/diag_count/appended_bytes/omitted_*/server/state/
// pid/count），源码正文、诊断文本与路径不进入载荷。aicli chat 与 runtime-server
// 共用这一份投影，避免两处漂移。
//
// 会话归属：优先取事件自带的 SessionID（Bridge 从工具执行 ctx 解析，见
// toolctx.SessionID），其次用 Options.FallbackSessionID（aicli 单会话 host 的
// 兜底）；都没有时作为无会话事件进入 observe 流（生命周期事件天然如此）。
package eventbridge

import (
	"strings"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimelsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

// Options 控制会话归属回退。
type Options struct {
	// FallbackSessionID 在事件自身无会话归属时使用；为空表示不回退。
	FallbackSessionID string
}

// Observer 返回把池事件发布到 bus 的观察者；bus 为 nil 时返回 nil。
// 观察者可能在 LSP 热路径（transport reader / turn goroutine）上被调用，
// 因此只做标量投影与同步 Publish，不做 I/O、不阻塞。
func Observer(bus runtimeevents.Publisher, opts Options) runtimelsp.Observer {
	if bus == nil {
		return nil
	}
	fallback := strings.TrimSpace(opts.FallbackSessionID)
	return func(event runtimelsp.Event) {
		sessionID := strings.TrimSpace(event.SessionID)
		if sessionID == "" {
			sessionID = fallback
		}
		switch event.Kind {
		case runtimelsp.EventRequest:
			payload := map[string]interface{}{
				"trigger":          event.Trigger,
				"outcome":          event.Outcome,
				"duration_ms":      event.DurationMS,
				"diag_count":       event.DiagCount,
				"appended_bytes":   event.AppendedBytes,
				"omitted_items":    event.OmittedItems,
				"omitted_by_chars": event.OmittedByChars,
				"server":           event.Server,
			}
			// A6 decision data: only meaningful when the file actually carried
			// diagnostics; a clean file stays byte-identical to before.
			if event.TotalDiagCount > 0 {
				payload["total_diag_count"] = event.TotalDiagCount
				payload["new_diag_count"] = event.NewDiagCount
			}
			// 冷探针分类标记（O11）：no_fresh 事件必须显式携带 cold_fast_fail
			// （true=重复探针，false=首探针）。缺省 omitempty 会把 false 丢掉，
			// 离线基线只能看到 true，首探针样本与旧构建的缺字段样本无法区分
			// （实测：lsp_cold_first_probe_ratio 结构性恒为 first 0）。
			if event.ColdFastFail || event.Outcome == "degraded_no_fresh" {
				payload["cold_fast_fail"] = event.ColdFastFail
			}
			// Join keys (plan §3.1): request → tool call / turn. The raw path
			// is projected to path_fingerprint by the observe plane and never
			// persisted as text.
			if event.ToolCallID != "" {
				payload["tool_call_id"] = event.ToolCallID
			}
			if event.TurnID != "" {
				payload["turn_id"] = event.TurnID
			}
			if event.ReasonCategory != "" {
				payload["reason_category"] = event.ReasonCategory
			}
			pathFingerprint := event.PathFingerprint
			if pathFingerprint == "" && event.Path != "" {
				pathFingerprint = runtimelsp.FingerprintPath(event.Path)
			}
			if pathFingerprint != "" {
				payload["path_fingerprint"] = pathFingerprint
			}
			if event.DiagFingerprint != "" {
				payload["diag_fingerprint"] = event.DiagFingerprint
			}
			// Append breakdown（§3.3）：诊断正文与协议噪声分列，0 不落盘。
			if event.AppendedDiagBytes > 0 {
				payload["appended_diag_bytes"] = event.AppendedDiagBytes
			}
			if event.AppendedNoteBytes > 0 {
				payload["appended_note_bytes"] = event.AppendedNoteBytes
			}
			if event.AppendedEmptyBytes > 0 {
				payload["appended_empty_bytes"] = event.AppendedEmptyBytes
			}
			if event.ColdFastFail {
				payload["cold_fast_fail"] = true
			}
			if event.AttemptedMembers > 1 {
				payload["attempted_members"] = event.AttemptedMembers
			}
			bus.Publish(runtimeevents.Event{
				Type:      runtimeevents.EventLSPRequestFinished,
				SessionID: sessionID,
				Timestamp: event.Time,
				Payload:   payload,
			})
		case runtimelsp.EventServerState:
			reason := event.Status.Reason
			if reason == "" {
				reason = event.Status.LastError
			}
			payload := map[string]interface{}{
				"server": event.Status.Name,
				"state":  string(event.Status.State),
				"pid":    event.Status.PID,
			}
			if category := runtimelsp.ReasonCategory(reason); category != "" {
				payload["reason_category"] = category
			}
			if event.Status.FirstPublishMS > 0 {
				payload["first_publish_ms"] = event.Status.FirstPublishMS
			}
			bus.Publish(runtimeevents.Event{
				Type:      runtimeevents.EventLSPServerState,
				SessionID: sessionID,
				Timestamp: event.Time,
				Payload:   payload,
			})
		case runtimelsp.EventDiagnostics:
			payload := map[string]interface{}{
				"server": event.Server,
				"count":  event.Count,
			}
			pathFingerprint := event.PathFingerprint
			if pathFingerprint == "" && event.Path != "" {
				pathFingerprint = runtimelsp.FingerprintPath(event.Path)
			}
			if pathFingerprint != "" {
				payload["path_fingerprint"] = pathFingerprint
			}
			if event.HasVersion {
				payload["version"] = event.Version
				payload["has_version"] = true
			}
			if event.DiagFingerprint != "" {
				payload["diag_fingerprint"] = event.DiagFingerprint
			}
			bus.Publish(runtimeevents.Event{
				Type:      runtimeevents.EventLSPDiagnosticsUpdated,
				SessionID: sessionID,
				Timestamp: event.Time,
				Payload:   payload,
			})
		}
	}
}
