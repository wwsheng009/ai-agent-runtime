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

// —— R2：aicli 宿主信号即 Presence 权威（docs/bots/12-notify-consolidation-plan.md §4 R2）——

// TestChatSessionViewingPresenceContract 锁定 R2 具名判定契约：nil 会话 /
// headless / 脚本化 → false（内核门放行——宁可多推不误吞）；TUI 前台 → true
// （门抑制）。与 notify.Presence 语义逐字对齐（false = 无从判定/不在场）。
func TestChatSessionViewingPresenceContract(t *testing.T) {
	if chatSessionViewing(nil) {
		t.Fatal("nil 会话不得报在场")
	}
	headless := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "s1"}}
	if chatSessionViewing(headless) {
		t.Fatal("非 TUI（headless/降级）不得报在场")
	}
	scripted := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "s2"}, InteractiveUI: true,
		ScriptedInput: newChatScriptedInput([]string{"x"})}
	if chatSessionViewing(scripted) {
		t.Fatal("脚本化 TUI 不得报在场")
	}
	tui := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "s3"}, InteractiveUI: true}
	if !chatSessionViewing(tui) {
		t.Fatal("TUI 前台应报在场")
	}
}
