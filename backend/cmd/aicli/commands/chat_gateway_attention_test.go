package commands

import (
	"errors"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

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
