package commands

import (
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
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

func TestChatGatewaySessionsCarriesLastMessage(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "s-last-1"}}
	session.RuntimeSession.History = []runtimetypes.Message{
		*runtimetypes.NewUserMessage("第一条用户消息"),
		{
			Role:    "assistant",
			Content: "",
			ContentParts: []runtimetypes.ContentPart{
				{Type: runtimetypes.ContentPartText, Text: "这是最后一条助手回复"},
			},
		},
	}
	summaries := chatGatewaySessions(session)
	if len(summaries) != 1 {
		t.Fatalf("summaries = %d, want 1", len(summaries))
	}
	if summaries[0].LastMessageRole != "assistant" {
		t.Fatalf("last_message_role = %q, want assistant", summaries[0].LastMessageRole)
	}
	if summaries[0].LastMessage != "这是最后一条助手回复" {
		t.Fatalf("last_message = %q", summaries[0].LastMessage)
	}
	// Preview still works alongside the new fields.
	if summaries[0].Preview == "" {
		t.Fatal("preview must stay populated")
	}
}

func TestChatGatewayLastMessageTruncates(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "s-last-2"}}
	long := make([]rune, chatGatewayLastMessageLimit+50)
	for i := range long {
		long[i] = '字'
	}
	session.RuntimeSession.History = []runtimetypes.Message{
		*runtimetypes.NewUserMessage(string(long)),
	}
	role, content := chatGatewayLastMessage(session)
	if role != "user" {
		t.Fatalf("role = %q, want user", role)
	}
	runes := []rune(content)
	if len(runes) != chatGatewayLastMessageLimit+1 || runes[chatGatewayLastMessageLimit] != '…' {
		t.Fatalf("truncated length = %d, want %d with ellipsis suffix", len(runes), chatGatewayLastMessageLimit+1)
	}
}

func TestChatGatewayLastMessageEmptyIsSafe(t *testing.T) {
	if role, content := chatGatewayLastMessage(nil); role != "" || content != "" {
		t.Fatalf("nil session: %q/%q", role, content)
	}
	empty := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "s-last-3"}}
	if role, content := chatGatewayLastMessage(empty); role != "" || content != "" {
		t.Fatalf("empty history: %q/%q", role, content)
	}
	// Image-only last message gets a placeholder instead of an empty block.
	img := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "s-last-4"}}
	img.RuntimeSession.History = []runtimetypes.Message{
		{
			Role: "user",
			ContentParts: []runtimetypes.ContentPart{
				{Type: runtimetypes.ContentPartImage, ImageURL: "https://example.com/a.png"},
			},
		},
	}
	role, content := chatGatewayLastMessage(img)
	if role != "user" || content != "[附件/图片]" {
		t.Fatalf("image-only: %q/%q", role, content)
	}
}

func TestChatGatewayEffectiveRegistrationTrimsWithoutWeb(t *testing.T) {
	routes, caps := chatGatewayEffectiveRegistration(nil, "")
	if len(routes) != 0 {
		t.Fatalf("routes = %v, want none (fail closed without a local Web origin)", routes)
	}
	for _, web := range chatGatewayWebCapabilities {
		if containsString(caps, web) {
			t.Fatalf("capabilities = %v, must not advertise %q without a Web client", caps, web)
		}
	}
	for _, core := range chatGatewayCoreCapabilities {
		if !containsString(caps, core) {
			t.Fatalf("capabilities = %v, missing core bit %q", caps, core)
		}
	}

	// With a local Web origin the defaults (and the Web-API bits) come back;
	// a trailing slash on the recorded origin must not change that.
	routes, caps = chatGatewayEffectiveRegistration(nil, "http://127.0.0.1:5555/")
	if !containsString(routes, "/web") || !containsString(routes, "/web/api") {
		t.Fatalf("routes = %v, want the default /web allowlist", routes)
	}
	if !containsString(caps, "input") {
		t.Fatalf("capabilities = %v, want the Web-API bits with a live target", caps)
	}

	// Configured routes win over the defaults and are copied, not aliased.
	custom := []string{"/web/api"}
	routes, _ = chatGatewayEffectiveRegistration(custom, "http://127.0.0.1:5555")
	if len(routes) != 1 || routes[0] != "/web/api" {
		t.Fatalf("routes = %v, want the configured allowlist", routes)
	}
	routes[0] = "/mutated"
	if custom[0] != "/web/api" {
		t.Fatalf("configured routes must be copied, got alias to %v", custom)
	}
}

func TestChatGatewayRegisterFuncReflectsForwardTarget(t *testing.T) {
	resetChatGatewayEndpoint(t)
	prev := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return nil }
	t.Cleanup(func() {
		chatDebugDisplaySessionProvider = prev
		resetChatGatewayEndpoint(t)
	})

	register := chatGatewayRegisterFunc(gatewayagent.Config{Name: "laptop"}, nil, &ChatSession{})
	got := register()
	if got.NodeID == "" || got.Kind != "aicli" {
		t.Fatalf("register = %+v", got)
	}
	if len(got.Routes) != 0 {
		t.Fatalf("routes = %v, want none before the Web client is recorded", got.Routes)
	}
	if containsString(got.Capabilities, "input") {
		t.Fatalf("capabilities = %v, want no Web-API bits before the Web client is recorded", got.Capabilities)
	}

	SetChatGatewayLoopbackEndpoint("127.0.0.1", 5555, "tok")
	got = register()
	if !containsString(got.Routes, "/web") {
		t.Fatalf("routes = %v, want the default allowlist once the Web client is recorded", got.Routes)
	}
	if !containsString(got.Capabilities, "input") {
		t.Fatalf("capabilities = %v, want the Web-API bits once the Web client is recorded", got.Capabilities)
	}
}
