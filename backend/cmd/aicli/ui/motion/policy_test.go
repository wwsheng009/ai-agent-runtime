package motion

import (
	"testing"
	"time"
)

func TestPolicyOffNoTicker(t *testing.T) {
	p := NewPolicy(Config{Forced: ForceMode(ModeOff), Interactive: true})
	if p.NeedsNextFrame() {
		t.Fatal("Off should not need frames")
	}
	if p.Interval() != 0 {
		t.Fatalf("interval=%v", p.Interval())
	}
	if got := p.ActivityFrame(time.Now()); got != " " {
		t.Fatalf("frame=%q", got)
	}
}

func TestPolicyReducedStatic(t *testing.T) {
	p := NewPolicy(Config{Forced: ForceMode(ModeReduced), Interactive: true})
	if p.NeedsNextFrame() {
		t.Fatal("reduced should be static")
	}
	a := p.ActivityFrame(time.Unix(0, 0))
	b := p.ActivityFrame(time.Unix(100, 0))
	if a != b || a != "•" {
		t.Fatalf("a=%q b=%q", a, b)
	}
}

func TestPolicyFullAdvances(t *testing.T) {
	p := NewPolicy(Config{
		Forced:   ForceMode(ModeFull),
		Frames:   []string{"A", "B", "C"},
		Interval: 50 * time.Millisecond,
	})
	if !p.NeedsNextFrame() {
		t.Fatal("expected NeedsNextFrame")
	}
	f0 := p.ActivityFrame(time.Unix(0, 0))
	f1 := p.ActivityFrame(time.Unix(0, int64(50*time.Millisecond)))
	if f0 == "" || f1 == "" {
		t.Fatal("empty frames")
	}
	// Same timestamp => same frame (stable across callers).
	if p.ActivityFrame(time.Unix(0, 0)) != f0 {
		t.Fatal("unstable frame for same time")
	}
}

func TestDetectModeNonInteractive(t *testing.T) {
	if DetectMode(Config{Interactive: false}) != ModeOff {
		t.Fatal("expected Off")
	}
}
