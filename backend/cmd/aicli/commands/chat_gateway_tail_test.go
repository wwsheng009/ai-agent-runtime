package commands

import (
	"strings"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// Regression for the unified-renderer tail starvation: publishGatewayTailLocked
// used to gate on c.activeStream.Active(), but the ActiveStreamController only
// ever becomes active on the legacy (non-unified) render path. Under the
// unified renderer — the production interactive default — the guard stayed
// false even while streamBuffer held the streaming output, so the live tail
// was never pushed. The sampler must read streamBuffer directly.
func TestPublishGatewayTailSamplesStreamBufferWithoutActiveController(t *testing.T) {
	sessionID := "tail-guard-unified-renderer"
	defer gatewayTailThrottle.Forget(sessionID)
	defer gatewayAttentionStore.Clear(sessionID)

	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: sessionID}}
	c := &chatInteractionCoordinator{session: session}
	if c.activeStream != nil && c.activeStream.Active() {
		t.Fatalf("precondition: test must run without an active stream controller")
	}

	c.streamBuffer.WriteString("streaming tail chunk")
	now := time.Now()
	c.publishGatewayTailLocked(now)

	got := gatewayAttentionStore.Get(sessionID)
	if got.Tail != "streaming tail chunk" {
		t.Fatalf("tail after publish = %q, want %q", got.Tail, "streaming tail chunk")
	}
	if got.TailAt.IsZero() {
		t.Fatalf("TailAt not stamped on push")
	}

	// The host mapping must forward the same shared tail into the summary the
	// gateway receives (chatGatewaySessions → SessionState.Apply).
	summaries := chatGatewaySessions(session)
	if len(summaries) != 1 || summaries[0].StatusTail != "streaming tail chunk" {
		t.Fatalf("gateway summary must carry the shared tail, got %+v", summaries)
	}

	// Turn end resets the buffer; the next sample (past the 1s throttle
	// cadence) clears the stored tail through change detection.
	c.streamBuffer.Reset()
	c.publishGatewayTailLocked(now.Add(2 * time.Second))
	if cleared := gatewayAttentionStore.Get(sessionID); cleared.Tail != "" {
		t.Fatalf("tail after turn-end reset = %q, want cleared", cleared.Tail)
	}
}

// The sampled buffer still flows through the shared 512-rune display bound and
// keeps the newest runes.
func TestPublishGatewayTailKeepsSharedTruncation(t *testing.T) {
	sessionID := "tail-guard-truncation"
	defer gatewayTailThrottle.Forget(sessionID)
	defer gatewayAttentionStore.Clear(sessionID)

	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: sessionID}}
	c := &chatInteractionCoordinator{session: session}
	c.streamBuffer.WriteString(strings.Repeat("0123456789", 80)) // 800 runes
	c.publishGatewayTailLocked(time.Now())

	got := gatewayAttentionStore.Get(sessionID)
	if n := len([]rune(got.Tail)); n != 512 {
		t.Fatalf("tail length = %d runes, want 512", n)
	}
	if !strings.HasSuffix(got.Tail, "0123456789") {
		t.Fatalf("tail must keep the newest runes, got suffix %q", got.Tail)
	}
}
