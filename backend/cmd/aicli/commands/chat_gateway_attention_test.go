package commands

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// TestGatewaySurfaceStatusParkedProjectsWaiting pins the §6.12 parked
// projection: Parked is not part of chatSurfaceStatus.isRunning() (the TUI
// "Worked for …" freeze needs it non-running) but the gateway must still see
// the wait (docs/gateway/08-notifications.md §3).
func TestGatewaySurfaceStatusParkedProjectsWaiting(t *testing.T) {
	state, text := gatewaySurfaceStatusFor(chatSurfaceStatus{kind: chatSurfaceStatusParked}, chatInputModeChat)
	if state != "running" || text != gatewayParkedStatusText {
		t.Fatalf("parked surface = (%q,%q), want (%q,%q)", state, text, "running", gatewayParkedStatusText)
	}
}

// TestGatewayAttentionParkedTurnKeepsWaiting pins the run-end finisher carve-out:
// a run that ends while the durable parked record still exists must NOT be
// projected as completed (that would badge the console "已完成" and push
// "回合完成" while subagents still run); it reports waiting instead, and the
// next turn start clears it.
func TestGatewayAttentionParkedTurnKeepsWaiting(t *testing.T) {
	store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "batches.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const sessionID = "s-attn-parked"
	if err := store.ParkTurnSuspension(context.Background(), &subagentbatch.TurnSuspension{
		TurnID:        "turn-parked",
		SessionID:     sessionID,
		RootScopeID:   sessionID,
		ObligationIDs: []string{"batch-1"},
		ResumeQueue:   []string{"batch-1"},
	}); err != nil {
		t.Fatal(err)
	}

	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: sessionID}}
	session.LocalRuntimeHost = &localChatRuntimeHost{SubagentBatches: store}

	finish := beginChatGatewayTurn(session)
	finish(nil)
	got := chatGatewaySessions(session)[0]
	if got.State != "running" || got.Attention != gatewayAttentionWaiting ||
		got.StatusText != gatewayParkedStatusText || !got.Busy {
		t.Fatalf("parked run end: %+v", got)
	}

	// The resume episode starts a new turn: BeginTurn clears the waiting badge.
	finishResume := beginChatGatewayTurn(session)
	got = chatGatewaySessions(session)[0]
	if got.Attention != "" || got.State != "running" {
		t.Fatalf("resume after parked: %+v", got)
	}
	finishResume(nil)
}

// TestClearGatewayWaitingAttentionPreservesOtherBadges pins the conditional
// clear: the waiting badge owns only itself.
func TestClearGatewayWaitingAttentionPreservesOtherBadges(t *testing.T) {
	setGatewayAttention("s-wait-1", gatewayAttentionApproval)
	if clearGatewayWaitingAttention("s-wait-1") {
		t.Fatal("clearing waiting must not report a change for an approval badge")
	}
	if got := gatewaySessionStateFor("s-wait-1").Attention; got != gatewayAttentionApproval {
		t.Fatalf("approval badge disturbed: %q", got)
	}
	setGatewayAttention("s-wait-2", gatewayAttentionWaiting)
	if !clearGatewayWaitingAttention("s-wait-2") {
		t.Fatal("expected the waiting badge to clear")
	}
	if got := gatewaySessionStateFor("s-wait-2").Attention; got != "" {
		t.Fatalf("waiting badge not cleared: %q", got)
	}
}

func TestGatewayAttentionTurnLifecycle(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "s-attn-1"}}

	// A fresh session reports idle with no attention.
	summaries := chatGatewaySessions(session)
	if len(summaries) != 1 {
		t.Fatalf("summaries = %d, want 1", len(summaries))
	}
	if summaries[0].Attention != "" || summaries[0].Busy {
		t.Fatalf("fresh session: attention=%q busy=%v", summaries[0].Attention, summaries[0].Busy)
	}

	// Turn start clears attention and marks the session busy/running.
	finish := beginChatGatewayTurn(session)
	summaries = chatGatewaySessions(session)
	if summaries[0].Attention != "" || !summaries[0].Busy || summaries[0].State != "running" {
		t.Fatalf("turn start: %+v", summaries[0])
	}

	// Successful turn end records completed and clears busy; the state stays
	// "completed" (a terminal outcome, not idle) until the next turn starts.
	finish(nil)
	summaries = chatGatewaySessions(session)
	if summaries[0].Attention != gatewayAttentionCompleted || summaries[0].Busy || summaries[0].State != "completed" {
		t.Fatalf("turn end: %+v", summaries[0])
	}

	// The finisher is idempotent.
	finish(nil)
	if got := chatGatewaySessions(session)[0].Attention; got != gatewayAttentionCompleted {
		t.Fatalf("idempotent finish changed attention to %q", got)
	}
}

func TestGatewayAttentionTurnFailure(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "s-attn-2"}}
	finish := beginChatGatewayTurn(session)
	finish(errors.New("boom"))
	got := chatGatewaySessions(session)[0]
	if got.Attention != gatewayAttentionError || got.State != "error" || got.Busy {
		t.Fatalf("turn failure: %+v", got)
	}
}

func TestGatewayAttentionTurnInterrupted(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "s-attn-3"}}
	finish := beginChatGatewayTurn(session)
	session.interrupted.Store(true)
	finish(errors.New("context canceled"))
	got := chatGatewaySessions(session)[0]
	// A user cancel is a state, not a failure: no attention badge.
	if got.Attention != "" || got.State != "interrupted" || got.Busy {
		t.Fatalf("turn interrupted: %+v", got)
	}
}

func TestGatewayAttentionNoSessionIsSafe(t *testing.T) {
	// nil session and a session without a runtime session are both no-ops.
	if finish := beginChatGatewayTurn(nil); finish == nil {
		t.Fatal("expected a non-nil finisher")
	} else {
		finish(nil)
	}
	empty := &ChatSession{}
	beginChatGatewayTurn(empty)(nil)
	if got := chatGatewaySessions(empty); got != nil {
		t.Fatalf("empty session summaries = %v, want nil", got)
	}
}

func TestGatewayAttentionStoreIsolation(t *testing.T) {
	setGatewayAttention("s-a", gatewayAttentionApproval)
	setGatewayAttention("s-b", gatewayAttentionQuestion)
	if got := gatewaySessionStateFor("s-a").Attention; got != gatewayAttentionApproval {
		t.Fatalf("s-a attention = %q", got)
	}
	if got := gatewaySessionStateFor("s-b").Attention; got != gatewayAttentionQuestion {
		t.Fatalf("s-b attention = %q", got)
	}
	setGatewayAttention("s-a", "")
	if got := gatewaySessionStateFor("s-a").Attention; got != "" {
		t.Fatalf("s-a attention not cleared: %q", got)
	}
	if got := gatewaySessionStateFor("s-b").Attention; got != gatewayAttentionQuestion {
		t.Fatalf("s-b attention disturbed: %q", got)
	}
}
