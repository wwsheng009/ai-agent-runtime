package main

import (
	"testing"

	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
)

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
	want := []string{"/api/runtime", "/api/agent", "/healthz", "/"}
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
	if len(got.Routes) == 0 {
		t.Fatal("nil forward provider must keep the historical allowlist")
	}
}
