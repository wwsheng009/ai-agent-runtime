package commands

import (
	"testing"

	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
)

func TestAttachChatGatewayUnconfigured(t *testing.T) {
	t.Setenv(gatewayagent.EnvURL, "")
	t.Setenv(gatewayagent.EnvToken, "")
	session := &ChatSession{}
	attachChatGateway(session)
	if session.Gateway != nil {
		t.Fatal("unconfigured gateway must leave session.Gateway nil")
	}
}

func TestAttachChatGatewayConfiguredNoAutoConnect(t *testing.T) {
	t.Setenv(gatewayagent.EnvURL, "wss://gw.example.com")
	t.Setenv(gatewayagent.EnvToken, "agw_x")
	t.Setenv(gatewayagent.EnvName, "laptop")
	t.Setenv(gatewayagent.EnvAutoConnect, "false")
	session := &ChatSession{}
	attachChatGateway(session)
	if session.Gateway == nil {
		t.Fatal("configured gateway should attach a controller")
	}
	if got := session.Gateway.Status().State; got != gatewayagent.StateDisconnected {
		t.Fatalf("state = %s, want disconnected (no auto-connect)", got)
	}
	if got := session.Gateway.Status().TokenHint; got != "agw_…" {
		t.Fatalf("token hint = %q, want agw_…", got)
	}
}

func TestAttachChatGatewayNilSessionIsSafe(t *testing.T) {
	attachChatGateway(nil)
}
