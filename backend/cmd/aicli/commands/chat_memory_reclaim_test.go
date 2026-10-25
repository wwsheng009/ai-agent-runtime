package commands

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestScheduleChatTranscriptMemoryReclaimDebounces(t *testing.T) {
	previous := chatTranscriptReclaimNow
	t.Cleanup(func() {
		chatTranscriptReclaimNow = previous
		chatTranscriptReclaimMu.Lock()
		chatTranscriptReclaimArmed = false
		chatTranscriptReclaimLastAt = time.Time{}
		chatTranscriptReclaimMu.Unlock()
	})
	chatTranscriptReclaimMu.Lock()
	chatTranscriptReclaimArmed = false
	chatTranscriptReclaimLastAt = time.Time{}
	chatTranscriptReclaimMu.Unlock()

	var calls atomic.Int32
	chatTranscriptReclaimNow = func() { calls.Add(1) }

	if !scheduleChatTranscriptMemoryReclaim() {
		t.Fatal("首次大收缩应安排归还")
	}
	if scheduleChatTranscriptMemoryReclaim() {
		t.Fatal("armed/冷却窗口内不得重复安排")
	}

	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("FreeOSMemory hook calls=%d, want 1", calls.Load())
	}
	if scheduleChatTranscriptMemoryReclaim() {
		t.Fatal("两分钟冷却窗口内不得再次安排")
	}
}
