package renderengine

import (
	"testing"
	"time"
)

func TestFrameClockCoalescesAndHonorsBudget(t *testing.T) {
	clock := NewFrameClock(10)
	start := time.Unix(100, 0)

	clock.Request("assistant.delta")
	clock.Request("assistant.delta")
	if !clock.Pending() {
		t.Fatal("expected coalesced frame to be pending")
	}
	if !clock.Consume(start) {
		t.Fatal("first pending frame must be consumed")
	}
	if clock.Pending() {
		t.Fatal("frame must not remain pending after Consume")
	}

	clock.Request("assistant.delta")
	if clock.Consume(start.Add(25 * time.Millisecond)) {
		t.Fatal("clock ignored its 10 FPS frame budget")
	}
	if delay, pending := clock.NextDelay(start.Add(25 * time.Millisecond)); !pending || delay != 75*time.Millisecond {
		t.Fatalf("NextDelay=(%s,%t), want (75ms,true)", delay, pending)
	}
	if !clock.Consume(start.Add(100 * time.Millisecond)) {
		t.Fatal("pending frame must be consumed at the FPS deadline")
	}
}

func TestFrameClockForceConsume(t *testing.T) {
	clock := NewFrameClock(10)
	start := time.Unix(100, 0)
	clock.Request("initial")
	if !clock.Consume(start) {
		t.Fatal("initial frame must be consumed")
	}
	clock.Request("resize")
	if !clock.ForceConsume(start.Add(time.Millisecond)) {
		t.Fatal("ForceConsume must bypass the normal FPS gap")
	}
	if clock.Pending() {
		t.Fatal("forced frame must clear the pending state")
	}
}
