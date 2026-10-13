package commands

import (
	"context"
	"os"
	"strings"
	"time"

	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// chatGatewayDefaultRoutes is the reverse-proxy allowlist when the config does
// not override it.
var chatGatewayDefaultRoutes = []string{"/web", "/web/api", "/debug/endpoints"}

// chatGatewayCoreCapabilities are the capability bits that stay truthful
// without a local Web origin: they describe the registry / control-plane
// projection of this process, not the reverse-proxied Web API.
var chatGatewayCoreCapabilities = []string{"manifest", "status", "sessions"}

// chatGatewayWebCapabilities are served through the local Web client's
// /web/api/* endpoints. They must not be advertised while that client is off:
// the console gates the reply form on "input", and the gateway would otherwise
// accept proxy requests for services this process cannot serve.
var chatGatewayWebCapabilities = []string{"screen", "events", "invoke", "input"}

// chatGatewayEffectiveRegistration trims the REGISTER metadata to what this
// process can actually serve. Without a recorded local Web origin (the loopback
// Web client was never enabled, e.g. a chat started without --web) every
// reverse-proxy route would be streamed to the agent only to fail there with
// "no local base url" (502). Fail closed instead: the gateway rejects
// /n/{node}/web/... up front with 403 route_not_allowed
// (docs/gateway/04-security.md §3) and the console hides the open action.
func chatGatewayEffectiveRegistration(routes []string, localBaseURL string) ([]string, []string) {
	if len(routes) == 0 {
		routes = chatGatewayDefaultRoutes
	}
	caps := make([]string, 0, len(chatGatewayCoreCapabilities)+len(chatGatewayWebCapabilities))
	caps = append(caps, chatGatewayCoreCapabilities...)
	caps = append(caps, chatGatewayWebCapabilities...)
	if strings.TrimRight(strings.TrimSpace(localBaseURL), "/") == "" {
		// No local service to forward to: no route is servable, and the
		// Web-API capability bits would be a lie.
		return nil, append([]string(nil), chatGatewayCoreCapabilities...)
	}
	return append([]string(nil), routes...), caps
}

// chatGatewayRegisterFunc supplies the REGISTER payload for this process. The
// session list reflects the session bound at dial time. The node id is the
// process identity (gatewayagent.ProcessNodeID): stable across reconnections
// and failover re-dials within this process lifetime, unique per process so two
// concurrent TUIs register as two nodes instead of overwriting each other.
func chatGatewayRegisterFunc(cfg gatewayagent.Config, routes []string, session *ChatSession) gatewayagent.RegisterFunc {
	nodeID := gatewayagent.ProcessNodeID()
	return func() wire.Register {
		// Evaluated per dial: the Web client may finish starting after the
		// controller was built (SetChatGatewayLoopbackEndpoint then reconnects),
		// so the advertised capability must reflect the current target.
		effectiveRoutes, effectiveCapabilities := chatGatewayEffectiveRegistration(
			routes, chatGatewayForwardConfig().LocalBaseURL)
		return wire.Register{
			NodeID:       nodeID,
			Kind:         "aicli",
			Name:         cfg.Name,
			DeviceName:   cfg.Name,
			Capabilities: effectiveCapabilities,
			Routes:       effectiveRoutes,
			Sessions:     chatGatewaySessions(session),
		}
	}
}

// chatGatewaySessions projects the process's active session for the gateway.
func chatGatewaySessions(session *ChatSession) []wire.SessionSummary {
	if session == nil || session.RuntimeSession == nil {
		return nil
	}
	id := strings.TrimSpace(session.RuntimeSession.ID)
	if id == "" {
		return nil
	}
	// Last-message summary for the gateway console session row (docs/gateway/
	// 02-architecture.md §3.2 preview: truncated, provided at the local agent's
	// discretion — it never leaves the machine un-truncated and contains no
	// secrets beyond what the session itself already shows in its own web UI).
	var preview string
	if p := session.RuntimeSession.BuildPreview(); p != nil {
		preview = strings.TrimSpace(p.Summary)
	}
	lastRole, lastMessage := chatGatewayLastMessage(session)
	updatedAt := session.RuntimeSession.UpdatedAt
	summary := wire.SessionSummary{
		SessionID:       id,
		Title:           chatMeshSessionTitle(session),
		Preview:         preview,
		LastMessageRole: lastRole,
		LastMessage:     lastMessage,
		UpdatedAt:       updatedAt,
	}
	// Overlay the shared state projection (state/attention/busy/status text);
	// the store defaults to idle/running so both hosts report the same baseline.
	gatewaySessionStateFor(id).Apply(&summary)
	summaries := []wire.SessionSummary{summary}
	// 子代理行（display-only，默认 off）：把本进程仍在跑的 subagent 任务作为子会话
	// 行追加在父会话之后，控制台据此展开/折叠（docs/gateway/07-console.md）。
	if gatewayagent.ReportSubagentsEnabled(nil) {
		summaries = append(summaries, chatGatewaySubagentSessions(session, id)...)
	}
	return summaries
}

// chatGatewaySubagentSessions appends the live subagent rows for one parent
// session. Bounded by gatewayagent.SubagentSessionMax and best-effort: a missing
// store or a read error yields no child rows (the parent row is unaffected).
// Terminal tasks are never listed — a finished child is reported by the parent
// turn (docs/gateway/07-console.md).
func chatGatewaySubagentSessions(session *ChatSession, parentSessionID string) []wire.SessionSummary {
	if session == nil || session.LocalRuntimeHost == nil {
		return nil
	}
	store := session.LocalRuntimeHost.SubagentBatches
	if store == nil || strings.TrimSpace(parentSessionID) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	batches, err := store.ListBatches(ctx, subagentbatch.BatchFilter{
		ParentSessionID: parentSessionID,
		Status: []subagentbatch.BatchStatus{
			subagentbatch.BatchQueued,
			subagentbatch.BatchRunning,
			subagentbatch.BatchPartiallyCompleted,
		},
		Limit: gatewayagent.SubagentSessionMax,
	})
	if err != nil {
		return nil
	}
	out := make([]wire.SessionSummary, 0, gatewayagent.SubagentSessionMax)
	for _, batch := range batches {
		tasks, err := store.ListTasks(ctx, batch.BatchID)
		if err != nil {
			continue
		}
		for _, task := range tasks {
			if task.Status.Terminal() {
				continue
			}
			childID := strings.TrimSpace(task.ChildSessionID)
			if childID == "" {
				continue
			}
			title := strings.TrimSpace(task.TaskSubject)
			if title == "" {
				title = strings.TrimSpace(task.TaskID)
			}
			var started time.Time
			if task.StartedAt != nil {
				started = *task.StartedAt
			}
			out = append(out, gatewayagent.SubagentSessionSummary(
				parentSessionID, childID, task.TaskID, task.TaskType, title, started))
			if len(out) >= gatewayagent.SubagentSessionMax {
				return out
			}
		}
	}
	return out
}

// chatGatewayLastMessageLimit caps the inline "last message" payload sent to
// the gateway. It is deliberately larger than the 120-char preview (the console
// renders the full text) but bounded so heartbeats/registry snapshots and the
// SSE broadcast stay cheap even for huge transcripts.
const chatGatewayLastMessageLimit = 2000

// chatGatewayLastMessage returns the truncated role+content of the session's
// last visible message for the gateway console session row.
func chatGatewayLastMessage(session *ChatSession) (role, content string) {
	if session == nil || session.RuntimeSession == nil {
		return "", ""
	}
	msg := session.RuntimeSession.LastMessage()
	if msg == nil {
		return "", ""
	}
	content = strings.TrimSpace(msg.Content)
	if content == "" && len(msg.ContentParts) > 0 {
		// Multimodal turns: surface the text parts; image-only messages fall
		// back to a placeholder instead of an empty block.
		var parts []string
		for _, part := range msg.ContentParts {
			if strings.TrimSpace(part.Text) != "" {
				parts = append(parts, strings.TrimSpace(part.Text))
			}
		}
		content = strings.Join(parts, "\n")
		if content == "" {
			content = "[附件/图片]"
		}
	}
	runes := []rune(content)
	if len(runes) > chatGatewayLastMessageLimit {
		content = string(runes[:chatGatewayLastMessageLimit]) + "…"
	}
	return strings.TrimSpace(msg.Role), content
}

// syncChatGatewaySession pushes the current session list to the gateway. It is
// a no-op when the gateway is not attached or not connected.
func syncChatGatewaySession(session *ChatSession) {
	if session == nil || session.Gateway == nil {
		return
	}
	session.Gateway.UpdateSessions(chatGatewaySessions(session))
}

// notifyChatGatewaySession emits a NOTIFY event (best-effort) alongside the
// persistent SESSION_UPDATE attention state (docs/gateway/08-notifications.md §4.1).
func notifyChatGatewaySession(session *ChatSession, kind, priority string) {
	if session == nil || session.Gateway == nil {
		return
	}
	id := chatSessionID(session)
	if id == "" {
		return
	}
	session.Gateway.SendNotify(wire.Notify{
		SessionID: id,
		Kind:      kind,
		Priority:  priority,
		Title:     chatMeshSessionTitle(session),
		TS:        time.Now().UTC(),
	})
}

// chatGatewayResolveConfig merges the layered gateway.* config with the
// AICLI_GATEWAY_* environment overrides (non-empty env wins).
func chatGatewayResolveConfig(session *ChatSession) gatewayagent.Config {
	cfg := gatewayagent.Config{AutoConnect: true}
	if session != nil && session.Config != nil && session.Config.Gateway != nil {
		gatewayCfg := session.Config.Gateway
		cfg.URL = strings.TrimSpace(gatewayCfg.URL)
		cfg.Token = strings.TrimSpace(gatewayCfg.Token)
		cfg.Name = strings.TrimSpace(gatewayCfg.Name)
		cfg.ProxyURL = strings.TrimSpace(gatewayCfg.ProxyURL)
		if gatewayCfg.AutoConnect != nil {
			cfg.AutoConnect = *gatewayCfg.AutoConnect
		}
	}
	if v := strings.TrimSpace(os.Getenv(gatewayagent.EnvURL)); v != "" {
		cfg.URL = v
	}
	if v := strings.TrimSpace(os.Getenv(gatewayagent.EnvToken)); v != "" {
		cfg.Token = v
	}
	if v := strings.TrimSpace(os.Getenv(gatewayagent.EnvName)); v != "" {
		cfg.Name = v
	}
	if v := strings.TrimSpace(os.Getenv(gatewayagent.EnvProxyURL)); v != "" {
		cfg.ProxyURL = v
	}
	if v := strings.TrimSpace(os.Getenv(gatewayagent.EnvAutoConnect)); v != "" {
		cfg.AutoConnect = !strings.EqualFold(v, "false") && v != "0"
	}
	cfg.Enabled = cfg.URL != "" && cfg.Token != ""
	return cfg
}

// chatGatewayRoutes returns the configured reverse-proxy allowlist (nil = default).
func chatGatewayRoutes(session *ChatSession) []string {
	if session != nil && session.Config != nil && session.Config.Gateway != nil && len(session.Config.Gateway.Routes) > 0 {
		return append([]string(nil), session.Config.Gateway.Routes...)
	}
	return nil
}

// attachChatGateway wires the gateway control plane into the session. It never
// fails the chat: an unconfigured gateway leaves session.Gateway nil (the
// /gateway command then explains how to enable it), and a failed auto-connect
// is left for /gateway status to surface.
func attachChatGateway(session *ChatSession) {
	if session == nil {
		return
	}
	cfg := chatGatewayResolveConfig(session)
	if !cfg.Enabled {
		return
	}
	controller := gatewayagent.NewController(cfg, chatGatewayRegisterFunc(cfg, chatGatewayRoutes(session), session))
	// Point the agent at this process's loopback web server (recorded by
	// cmd/aicli once it is listening) before the auto-connect dial.
	controller.SetForward(chatGatewayForwardConfig())
	session.Gateway = controller
	if !cfg.AutoConnect {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), gatewayCommandTimeout)
		defer cancel()
		// Best-effort: status/errors are surfaced through /gateway.
		_ = controller.Connect(ctx)
	}()
}
