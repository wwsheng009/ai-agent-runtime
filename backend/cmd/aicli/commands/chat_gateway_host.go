package commands

import (
	"context"
	"os"
	"strings"
	"time"

	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// chatGatewayDefaultRoutes is the reverse-proxy allowlist when the config does
// not override it.
var chatGatewayDefaultRoutes = []string{"/web", "/web/api", "/debug/endpoints"}

// chatGatewayRegisterFunc supplies the REGISTER payload for this process. The
// session list reflects the session bound at dial time. The node id is the
// machine-stable identity (gatewayagent.LoadOrCreateNodeID) so a reconnect
// after a gateway failover replaces the stale registry entry on the standby
// instead of registering a second node.
func chatGatewayRegisterFunc(cfg gatewayagent.Config, routes []string, session *ChatSession) gatewayagent.RegisterFunc {
	nodeID := gatewayagent.LoadOrCreateNodeID()
	return func() wire.Register {
		if len(routes) == 0 {
			routes = chatGatewayDefaultRoutes
		}
		return wire.Register{
			NodeID:       nodeID,
			Kind:         "aicli",
			Name:         cfg.Name,
			DeviceName:   cfg.Name,
			Capabilities: []string{"manifest", "screen", "status", "events", "invoke", "input", "sessions"},
			Routes:       append([]string(nil), routes...),
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
	st := gatewaySessionStateFor(id)
	state := "idle"
	if st.Busy {
		state = "running"
	}
	return []wire.SessionSummary{{
		SessionID: id,
		Title:     chatMeshSessionTitle(session),
		State:     state,
		Attention: st.Attention,
		Busy:      st.Busy,
	}}
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
