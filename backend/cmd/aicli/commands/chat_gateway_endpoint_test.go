package commands

import (
	"testing"

	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
)

func resetChatGatewayEndpoint(t *testing.T) {
	t.Helper()
	chatGatewayEndpointMu.Lock()
	chatGatewayLocalBaseURL = ""
	chatGatewayLocalToken = ""
	chatGatewayEndpointMu.Unlock()
}

func TestSetChatGatewayLoopbackEndpointNormalizesWildcard(t *testing.T) {
	resetChatGatewayEndpoint(t)
	SetChatGatewayLoopbackEndpoint("0.0.0.0", 12345, "tok")
	got := chatGatewayForwardConfig()
	if got.LocalBaseURL != "http://127.0.0.1:12345" || got.LocalToken != "tok" {
		t.Fatalf("forward = %+v", got)
	}
}

func TestSetChatGatewayLoopbackEndpointIgnoresZeroPort(t *testing.T) {
	resetChatGatewayEndpoint(t)
	SetChatGatewayLoopbackEndpoint("127.0.0.1", 0, "tok")
	if got := chatGatewayForwardConfig(); got.LocalBaseURL != "" {
		t.Fatalf("zero port must be ignored, got %+v", got)
	}
}

func TestSetChatGatewayLoopbackEndpointUpdatesSession(t *testing.T) {
	resetChatGatewayEndpoint(t)
	session := &ChatSession{Gateway: gatewayagent.New(gatewayagent.Config{Enabled: false}, nil)}
	prev := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return session }
	t.Cleanup(func() {
		chatDebugDisplaySessionProvider = prev
		resetChatGatewayEndpoint(t)
	})

	SetChatGatewayLoopbackEndpoint("127.0.0.1", 5555, "tok")
	got := session.Gateway.ForwardConfig()
	if got.LocalBaseURL != "http://127.0.0.1:5555" || got.LocalToken != "tok" {
		t.Fatalf("session forward not updated: %+v", got)
	}
}
