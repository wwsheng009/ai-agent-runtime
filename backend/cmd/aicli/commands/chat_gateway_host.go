package commands

import (
	"context"
	"os"
	"strings"

	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// Environment keys for the gateway control plane (docs/gateway/12-connection-control.md
// §6.2). The layered-config keys (gateway.*) land in a later step; the env
// surface is the first wiring and is what attachChatGateway reads.
const (
	chatGatewayEnvURL         = "AICLI_GATEWAY_URL"
	chatGatewayEnvToken       = "AICLI_GATEWAY_TOKEN"
	chatGatewayEnvName        = "AICLI_GATEWAY_NAME"
	chatGatewayEnvAutoConnect = "AICLI_GATEWAY_AUTO_CONNECT"
)

// chatGatewayConfig is the resolved gateway configuration for a chat process.
type chatGatewayConfig struct {
	URL         string
	Token       string
	Name        string
	AutoConnect bool
}

// enabled reports whether a URL and token are present (presence implies the
// gateway is enabled; there is no separate env switch).
func (c chatGatewayConfig) enabled() bool {
	return strings.TrimSpace(c.URL) != "" && strings.TrimSpace(c.Token) != ""
}

// chatGatewayConfigFromEnv resolves the configuration from environment
// variables. AutoConnect defaults to true unless explicitly disabled.
func chatGatewayConfigFromEnv(getenv func(string) string) chatGatewayConfig {
	if getenv == nil {
		getenv = os.Getenv
	}
	auto := true
	if raw := strings.TrimSpace(getenv(chatGatewayEnvAutoConnect)); raw != "" {
		auto = !strings.EqualFold(raw, "false") && raw != "0"
	}
	return chatGatewayConfig{
		URL:         strings.TrimSpace(getenv(chatGatewayEnvURL)),
		Token:       strings.TrimSpace(getenv(chatGatewayEnvToken)),
		Name:        strings.TrimSpace(getenv(chatGatewayEnvName)),
		AutoConnect: auto,
	}
}

// chatGatewayRegisterFunc supplies the REGISTER payload for this process. The
// session list is populated by a later step; node identity, capabilities and
// the reverse-proxy route allowlist are set here.
func chatGatewayRegisterFunc(cfg chatGatewayConfig) gatewayagent.RegisterFunc {
	return func() wire.Register {
		return wire.Register{
			Kind:         "aicli",
			Name:         cfg.Name,
			DeviceName:   cfg.Name,
			Capabilities: []string{"manifest", "screen", "status", "events", "invoke", "input", "sessions"},
			Routes:       []string{"/web", "/web/api", "/debug/endpoints"},
		}
	}
}

// newChatGatewayController builds a controller when the configuration is
// usable, and returns nil otherwise (the caller leaves session.Gateway unset).
func newChatGatewayController(cfg chatGatewayConfig, register gatewayagent.RegisterFunc) *gatewayagent.Controller {
	if !cfg.enabled() {
		return nil
	}
	if register == nil {
		register = chatGatewayRegisterFunc(cfg)
	}
	return gatewayagent.NewController(gatewayagent.Config{
		Enabled:     true,
		URL:         cfg.URL,
		Token:       cfg.Token,
		Name:        cfg.Name,
		AutoConnect: cfg.AutoConnect,
	}, register)
}

// attachChatGateway wires the gateway control plane into the session. It never
// fails the chat: an unconfigured gateway leaves session.Gateway nil (the
// /gateway command then explains how to enable it), and a failed auto-connect
// is left for /gateway status to surface.
func attachChatGateway(session *ChatSession) {
	if session == nil {
		return
	}
	cfg := chatGatewayConfigFromEnv(os.Getenv)
	controller := newChatGatewayController(cfg, nil)
	if controller == nil {
		return
	}
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
