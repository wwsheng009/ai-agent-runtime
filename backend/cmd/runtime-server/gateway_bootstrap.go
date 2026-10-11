package main

import (
	"context"
	"net"
	"os"
	"strings"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi"
	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// runtimeServerGatewayConnectTimeout bounds the startup auto-connect attempt.
const runtimeServerGatewayConnectTimeout = 20 * time.Second

// startRuntimeServerGateway builds the gateway connection control plane from
// environment configuration (docs/gateway/12-connection-control.md) and injects
// it into the handler. It never fails startup: an unconfigured gateway leaves
// the handler without a controller (the control API then returns 503).
func startRuntimeServerGateway(handler *runtimeapi.Handler, cfg *config.Config) *gatewayagent.Controller {
	if handler == nil {
		return nil
	}
	agentCfg := runtimeServerGatewayResolveConfig(cfg)
	if !agentCfg.Enabled {
		return nil
	}
	controller := gatewayagent.NewController(agentCfg, runtimeServerGatewayRegister(agentCfg, handler.GatewaySessions))
	handler.SetGatewayController(controller)
	// Derive per-session attention from the runtime event bus so the gateway can
	// push notifications (docs/gateway/08-notifications.md §3).
	handler.EnableGatewayAttention()
	if agentCfg.AutoConnect {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), runtimeServerGatewayConnectTimeout)
			defer cancel()
			// Best-effort: status/errors are surfaced through the control API.
			_ = controller.Connect(ctx)
		}()
	}
	return controller
}

// runtimeServerGatewayResolveConfig merges the layered gateway.* config with the
// AICLI_GATEWAY_* environment overrides (non-empty env wins).
func runtimeServerGatewayResolveConfig(cfg *config.Config) gatewayagent.Config {
	out := gatewayagent.Config{AutoConnect: true}
	if cfg != nil && cfg.Gateway != nil {
		gatewayCfg := cfg.Gateway
		out.URL = strings.TrimSpace(gatewayCfg.URL)
		out.Token = strings.TrimSpace(gatewayCfg.Token)
		out.Name = strings.TrimSpace(gatewayCfg.Name)
		if gatewayCfg.AutoConnect != nil {
			out.AutoConnect = *gatewayCfg.AutoConnect
		}
	}
	if v := strings.TrimSpace(os.Getenv(gatewayagent.EnvURL)); v != "" {
		out.URL = v
	}
	if v := strings.TrimSpace(os.Getenv(gatewayagent.EnvToken)); v != "" {
		out.Token = v
	}
	if v := strings.TrimSpace(os.Getenv(gatewayagent.EnvName)); v != "" {
		out.Name = v
	}
	if v := strings.TrimSpace(os.Getenv(gatewayagent.EnvAutoConnect)); v != "" {
		out.AutoConnect = !strings.EqualFold(v, "false") && v != "0"
	}
	out.Enabled = out.URL != "" && out.Token != ""
	return out
}

// runtimeServerGatewayRegister supplies the REGISTER payload for this process.
// The session list is populated by a later step; node identity, capabilities
// and the reverse-proxy route allowlist are set here.
func runtimeServerGatewayRegister(cfg gatewayagent.Config, sessions func() []wire.SessionSummary) gatewayagent.RegisterFunc {
	return func() wire.Register {
		reg := wire.Register{
			NodeID:       gatewayagent.LoadOrCreateNodeID(),
			Kind:         "runtime-server",
			Name:         cfg.Name,
			DeviceName:   cfg.Name,
			Capabilities: []string{"manifest", "runtime-api", "sessions"},
			Routes:       []string{"/api/runtime", "/api/agent", "/healthz", "/"},
		}
		if sessions != nil {
			reg.Sessions = sessions()
		}
		return reg
	}
}

// setGatewayForward points the gateway agent at this server's loopback address
// once it is listening, so reverse-proxied requests reach the local API/SPA.
// Called from runServe after the listen address is resolved.
func (a *runtimeServerApp) setGatewayForward(addr string) {
	if a == nil || a.gatewayController == nil {
		return
	}
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	a.gatewayController.SetForward(gatewayagent.ForwardConfig{
		LocalBaseURL: "http://" + net.JoinHostPort(host, port),
	})
	// Apply immediately to a live connection (the auto-connect may have run
	// before the address was known).
	if a.gatewayController.Status().Connected() {
		go func() { _ = a.gatewayController.Reconnect(context.Background()) }()
	}
}
