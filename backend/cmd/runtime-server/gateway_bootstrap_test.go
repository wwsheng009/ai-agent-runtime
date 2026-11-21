package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/webui"
)

func TestRuntimeServerGatewayRoutesOnlyAdvertiseWebUIWhenAvailable(t *testing.T) {
	base := []string{"/api/runtime", "/api/agent", "/healthz"}
	tests := []struct {
		name      string
		available bool
		want      []string
	}{
		{name: "without embedded frontend", want: base},
		{name: "with embedded frontend", available: true, want: append(append([]string(nil), base...), "/")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runtimeServerGatewayRoutes(tt.available); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("routes = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRuntimeServerGatewayRegisterTrimsRoutesUntilForwardReady(t *testing.T) {
	base := ""
	register := runtimeServerGatewayRegister(gatewayagent.Config{Name: "box"}, nil, func() gatewayagent.ForwardConfig {
		return gatewayagent.ForwardConfig{LocalBaseURL: base}
	})

	got := register()
	if got.Kind != "runtime-server" || got.NodeID == "" {
		t.Fatalf("register = %+v", got)
	}
	if len(got.Routes) != 0 {
		t.Fatalf("routes = %v, want none while the local forwarding target is unset", got.Routes)
	}

	base = "http://127.0.0.1:8123"
	got = register()
	want := runtimeServerGatewayRoutes(webui.Available())
	if len(got.Routes) != len(want) {
		t.Fatalf("routes = %v, want %v once the target is known", got.Routes, want)
	}
	for i := range want {
		if got.Routes[i] != want[i] {
			t.Fatalf("routes = %v, want %v once the target is known", got.Routes, want)
		}
	}
}

func TestRuntimeServerGatewayRegisterNilForwardKeepsAllowlist(t *testing.T) {
	got := runtimeServerGatewayRegister(gatewayagent.Config{}, nil, nil)()
	want := runtimeServerGatewayRoutes(webui.Available())
	if !reflect.DeepEqual(got.Routes, want) {
		t.Fatalf("routes = %v, want %v without a forward provider", got.Routes, want)
	}
}

func TestRuntimeServerSetGatewayForwardKeepsAdminTokenOutOfRegister(t *testing.T) {
	t.Setenv("AICLI_GATEWAY_TOKEN", "")
	controller := gatewayagent.NewController(gatewayagent.Config{}, nil)
	app := &runtimeServerApp{
		gatewayController: controller,
		cfg:               &config.Config{Gateway: &config.GatewayConfig{Token: "  gw-device-secret  "}},
		skillsCfg:         &config.SkillsRuntimeConfig{AdminToken: "  node-admin-secret  "},
	}

	app.setGatewayForward("0.0.0.0:8123")
	forward := controller.ForwardConfig()
	if forward.LocalBaseURL != "http://127.0.0.1:8123" {
		t.Fatalf("LocalBaseURL = %q", forward.LocalBaseURL)
	}
	if forward.AnalyticsAdminToken != "node-admin-secret" {
		t.Fatalf("AnalyticsAdminToken = %q, want trimmed node-local token", forward.AnalyticsAdminToken)
	}
	if forward.GatewayToken != "gw-device-secret" {
		t.Fatalf("GatewayToken = %q, want trimmed gateway device token", forward.GatewayToken)
	}

	register := runtimeServerGatewayRegister(gatewayagent.Config{Name: "box"}, nil, controller.ForwardConfig)()
	encoded, err := json.Marshal(register)
	if err != nil {
		t.Fatalf("marshal register: %v", err)
	}
	if bytes.Contains(encoded, []byte("node-admin-secret")) {
		t.Fatalf("admin token leaked into REGISTER payload: %s", encoded)
	}
	if bytes.Contains(encoded, []byte("gw-device-secret")) {
		t.Fatalf("gateway token leaked into REGISTER payload: %s", encoded)
	}
	encodedStatus, err := json.Marshal(controller.Status())
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if bytes.Contains(encodedStatus, []byte("node-admin-secret")) {
		t.Fatalf("admin token leaked into gateway status: %s", encodedStatus)
	}
	if bytes.Contains(encodedStatus, []byte("gw-device-secret")) {
		t.Fatalf("gateway token leaked into gateway status: %s", encodedStatus)
	}
}

func TestRuntimeServerSetGatewayForwardWithoutSkillsConfigHasNoAdminToken(t *testing.T) {
	controller := gatewayagent.NewController(gatewayagent.Config{}, nil)
	app := &runtimeServerApp{gatewayController: controller}

	app.setGatewayForward("127.0.0.1:8123")
	forward := controller.ForwardConfig()
	if forward.AnalyticsAdminToken != "" {
		t.Fatalf("nil skills config injected an admin token: %q", forward.AnalyticsAdminToken)
	}
}
