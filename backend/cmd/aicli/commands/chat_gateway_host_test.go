package commands

import (
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
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
