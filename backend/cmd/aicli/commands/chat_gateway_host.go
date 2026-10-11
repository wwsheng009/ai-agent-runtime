package commands

import (
	"context"
	"os"
	"strings"

	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// chatGatewayRegisterFunc supplies the REGISTER payload for this process. The
// session list reflects the session bound at dial time (a later step pushes
// SESSION_UPDATE on switch).
func chatGatewayRegisterFunc(cfg gatewayagent.Config, session *ChatSession) gatewayagent.RegisterFunc {
	return func() wire.Register {
		return wire.Register{
			Kind:         "aicli",
			Name:         cfg.Name,
			DeviceName:   cfg.Name,
			Capabilities: []string{"manifest", "screen", "status", "events", "invoke", "input", "sessions"},
			Routes:       []string{"/web", "/web/api", "/debug/endpoints"},
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
	summary := wire.SessionSummary{
		SessionID: id,
		Title:     chatMeshSessionTitle(session),
		State:     "idle",
	}
	return []wire.SessionSummary{summary}
}

// syncChatGatewaySession pushes the current session list to the gateway. It is
// a no-op when the gateway is not attached or not connected.
func syncChatGatewaySession(session *ChatSession) {
	if session == nil || session.Gateway == nil {
		return
	}
	session.Gateway.UpdateSessions(chatGatewaySessions(session))
}

// attachChatGateway wires the gateway control plane into the session from
// environment configuration (AICLI_GATEWAY_URL/TOKEN/NAME/AUTO_CONNECT, see
// gatewayagent.ConfigFromEnv). It never fails the chat: an unconfigured gateway
// leaves session.Gateway nil (the /gateway command then explains how to enable
// it), and a failed auto-connect is left for /gateway status to surface.
func attachChatGateway(session *ChatSession) {
	if session == nil {
		return
	}
	cfg := gatewayagent.ConfigFromEnv(os.Getenv)
	if !cfg.Enabled {
		return
	}
	controller := gatewayagent.NewController(cfg, chatGatewayRegisterFunc(cfg, session))
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
