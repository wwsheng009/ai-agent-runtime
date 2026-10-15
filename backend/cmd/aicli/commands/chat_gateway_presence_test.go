package commands

import (
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// TestChatGatewaySessionsViewingGate：在场 host 源上报门禁（docs/bots/10 §4）
// ——仅 TUI（InteractiveUI）且非脚本化时报 Viewing；headless/脚本不报。
func TestChatGatewaySessionsViewingGate(t *testing.T) {
	tui := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "s-tui"}, InteractiveUI: true}
	if got := chatGatewaySessions(tui); len(got) == 0 || !got[0].Viewing {
		t.Fatalf("TUI 会话应上报 Viewing：%+v", got)
	}

	scripted := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "s-script"}, InteractiveUI: true,
		ScriptedInput: newChatScriptedInput([]string{"echo hi"})}
	if got := chatGatewaySessions(scripted); len(got) == 0 || got[0].Viewing {
		t.Fatalf("脚本化会话不得上报 Viewing：%+v", got)
	}

	headless := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "s-headless"}}
	if got := chatGatewaySessions(headless); len(got) == 0 || got[0].Viewing {
		t.Fatalf("非 TUI 会话不得上报 Viewing：%+v", got)
	}
}

// TestSessionDigestsCarryViewing：心跳摘要携带 Viewing（高频续期通道，§3.1）。
func TestSessionDigestsCarryViewing(t *testing.T) {
	c := &gatewayagent.Controller{}
	c.UpdateSessions([]wire.SessionSummary{{SessionID: "s-digest-1", Viewing: true}})
	ds := c.SessionDigests()
	if len(ds) != 1 || !ds[0].Viewing {
		t.Fatalf("digests = %+v", ds)
	}
}
