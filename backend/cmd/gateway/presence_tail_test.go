package main

import (
	"testing"
	"time"
)

// TestPresenceTailWatched pins the P1-b presence split: any presence suppresses
// push notifications, but only tail=true entries gate live-output streams.
func TestPresenceTailWatched(t *testing.T) {
	p := newPresenceRegistry(time.Minute)
	p.Touch("n1", "s1", false)
	p.Touch("n1", "s2", true)
	p.Touch("n2", "s3", true)

	if !p.Active("n1", "s1") {
		t.Fatal("plain presence must still suppress pushes")
	}
	watched := p.TailWatched("n1")
	if len(watched) != 1 || watched[0] != "s2" {
		t.Fatalf("TailWatched(n1) = %v", watched)
	}
	if other := p.TailWatched("n2"); len(other) != 1 || other[0] != "s3" {
		t.Fatalf("TailWatched(n2) = %v", other)
	}

	// Releasing the tail watch keeps the (push-suppressing) presence entry.
	p.Touch("n1", "s2", false)
	if got := p.TailWatched("n1"); len(got) != 0 {
		t.Fatalf("released watch still returned: %v", got)
	}
	if !p.Active("n1", "s2") {
		t.Fatal("releasing tail must keep presence")
	}

	// Entries expire with the TTL.
	short := newPresenceRegistry(20 * time.Millisecond)
	short.Touch("n1", "s1", true)
	time.Sleep(50 * time.Millisecond)
	if got := short.TailWatched("n1"); len(got) != 0 {
		t.Fatalf("expired watch returned: %v", got)
	}
	if short.Active("n1", "s1") {
		t.Fatal("expired presence must be inactive")
	}
}
