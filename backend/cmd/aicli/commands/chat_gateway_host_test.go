package commands

import (
	"testing"

	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
)

func TestChatGatewayConfigFromEnv(t *testing.T) {
	env := map[string]string{
		chatGatewayEnvURL:         "  wss://gw.example.com ",
		chatGatewayEnvToken:       "agw_secret",
		chatGatewayEnvName:        "laptop",
		chatGatewayEnvAutoConnect: "false",
	}
	getenv := func(key string) string { return env[key] }
	got := chatGatewayConfigFromEnv(getenv)
	if got.URL != "wss://gw.example.com" || got.Token != "agw_secret" || got.Name != "laptop" {
		t.Fatalf("unexpected config: %+v", got)
	}
	if got.AutoConnect {
		t.Fatal("AICLI_GATEWAY_AUTO_CONNECT=false must disable auto-connect")
	}
	if !got.enabled() {
		t.Fatal("url+token present should be enabled")
	}
}

func TestChatGatewayConfigAutoConnectDefaultsTrue(t *testing.T) {
	got := chatGatewayConfigFromEnv(func(string) string { return "" })
	if !got.AutoConnect {
		t.Fatal("auto-connect should default to true")
	}
	if got.enabled() {
		t.Fatal("empty env must not be enabled")
	}
}

func TestNewChatGatewayControllerDisabled(t *testing.T) {
	if c := newChatGatewayController(chatGatewayConfig{URL: "wss://gw"}, nil); c != nil {
		t.Fatal("missing token must yield a nil controller")
	}
	if c := newChatGatewayController(chatGatewayConfig{Token: "agw_x"}, nil); c != nil {
		t.Fatal("missing url must yield a nil controller")
	}
}

func TestNewChatGatewayControllerEnabled(t *testing.T) {
	c := newChatGatewayController(chatGatewayConfig{URL: "wss://gw", Token: "agw_x"}, nil)
	if c == nil {
		t.Fatal("expected a controller")
	}
	if got := c.Status().State; got != gatewayagent.StateDisconnected {
		t.Fatalf("state = %s, want disconnected", got)
	}
	if got := c.Status().TokenHint; got != "agw_…" {
		t.Fatalf("token hint = %q, want agw_…", got)
	}
}

func TestAttachChatGatewayUnconfigured(t *testing.T) {
	t.Setenv(chatGatewayEnvURL, "")
	t.Setenv(chatGatewayEnvToken, "")
	session := &ChatSession{}
	attachChatGateway(session)
	if session.Gateway != nil {
		t.Fatal("unconfigured gateway must leave session.Gateway nil")
	}
}

func TestAttachChatGatewayConfiguredNoAutoConnect(t *testing.T) {
	t.Setenv(chatGatewayEnvURL, "wss://gw.example.com")
	t.Setenv(chatGatewayEnvToken, "agw_x")
	t.Setenv(chatGatewayEnvName, "laptop")
	t.Setenv(chatGatewayEnvAutoConnect, "false")
	session := &ChatSession{}
	attachChatGateway(session)
	if session.Gateway == nil {
		t.Fatal("configured gateway should attach a controller")
	}
	if got := session.Gateway.Status().State; got != gatewayagent.StateDisconnected {
		t.Fatalf("state = %s, want disconnected (no auto-connect)", got)
	}
}

func TestAttachChatGatewayNilSessionIsSafe(t *testing.T) {
	attachChatGateway(nil)
}
