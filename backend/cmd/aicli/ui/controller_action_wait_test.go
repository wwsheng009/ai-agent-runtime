package ui

import (
	"sync"
	"testing"
	"time"
)

// waitTestRecorder is a reducer/effect pair that lets a test hold effect
// delivery open while it probes the applied/visible fences.
type waitTestRecorder struct {
	mu      sync.Mutex
	applied int
	hold    chan struct{}
	holdOne bool
}

func (r *waitTestRecorder) apply(rev uint64, action UIAction) []Effect {
	r.mu.Lock()
	r.applied++
	r.mu.Unlock()
	if _, ok := action.(DrawRequested); ok {
		return []Effect{FlushEffect{}}
	}
	return nil
}

func (r *waitTestRecorder) effect(Effect) {
	if !r.holdOne {
		return
	}
	<-r.hold
}

func (r *waitTestRecorder) appliedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.applied
}

func newWaitTestController(rec *waitTestRecorder) *UIController {
	return NewUIController(UIControllerConfig{}, ReducerFunc(rec.apply), rec.effect)
}

func TestPostTrackedAdmittedReleasesAppliedWaiter(t *testing.T) {
	rec := &waitTestRecorder{}
	c := newWaitTestController(rec)
	go c.Run()
	defer c.Close()

	outcome, ticket := c.PostTracked(DrawRequested{Key: "wait-admitted"})
	if outcome != PostAdmitted || ticket == 0 {
		t.Fatalf("PostTracked = %v/%d, want admitted with ticket", outcome, ticket)
	}
	if !c.WaitActionApplied(ticket, 2*time.Second) {
		t.Fatal("WaitActionApplied timed out for an admitted action")
	}
	if got := rec.appliedCount(); got != 1 {
		t.Fatalf("reducer applied %d actions, want 1", got)
	}
	// The recorder returns a FlushEffect for DrawRequested; visible must also
	// converge once the batch delivery finishes.
	if !c.WaitActionVisible(ticket, 2*time.Second) {
		t.Fatal("WaitActionVisible timed out for a delivered action")
	}
}

func TestPostTrackedMergedReleasesBothTickets(t *testing.T) {
	rec := &waitTestRecorder{}
	c := newWaitTestController(rec)

	firstOutcome, firstTicket := c.PostTracked(DrawRequested{Key: "merge-key"})
	if firstOutcome != PostAdmitted {
		t.Fatalf("first PostTracked = %v, want admitted", firstOutcome)
	}
	secondOutcome, secondTicket := c.PostTracked(DrawRequested{Key: "merge-key"})
	if secondOutcome != PostMerged || secondTicket <= firstTicket {
		t.Fatalf("second PostTracked = %v/%d, want merged with a higher ticket than %d",
			secondOutcome, secondTicket, firstTicket)
	}

	go c.Run()
	defer c.Close()

	if !c.WaitActionApplied(firstTicket, 2*time.Second) {
		t.Fatal("first (merged) ticket never released")
	}
	if !c.WaitActionApplied(secondTicket, 2*time.Second) {
		t.Fatal("second (merge carrier) ticket never released")
	}
	if got := rec.appliedCount(); got != 1 {
		t.Fatalf("merged posts applied %d actions, want 1", got)
	}
}

func TestPostTrackedDroppedWhenClosed(t *testing.T) {
	rec := &waitTestRecorder{}
	c := newWaitTestController(rec)
	c.Close()

	outcome, ticket := c.PostTracked(DrawRequested{Key: "closed"})
	if outcome != PostDropped || ticket != 0 {
		t.Fatalf("PostTracked after close = %v/%d, want dropped/0", outcome, ticket)
	}
}

func TestTryPostTrackedFullMailboxOutcomes(t *testing.T) {
	rec := &waitTestRecorder{}
	c := NewUIController(UIControllerConfig{MailboxSize: 1}, ReducerFunc(rec.apply), rec.effect)

	if outcome, _ := c.TryPostTracked(DrawRequested{Key: "a"}); outcome != PostAdmitted {
		t.Fatalf("first TryPostTracked = %v, want admitted", outcome)
	}
	// A second coalescable action with the same key merges into the queued slot
	// even though the mailbox is full.
	if outcome, _ := c.TryPostTracked(DrawRequested{Key: "a"}); outcome != PostMerged {
		t.Fatalf("coalescable TryPostTracked = %v, want merged", outcome)
	}
	// A durable action cannot merge and the mailbox is full: the producer must
	// treat this as its own wake-up rather than waiting for a revision.
	if outcome, ticket := c.TryPostTracked(Resize{Width: 10, Height: 4}); outcome != PostDropped || ticket != 0 {
		t.Fatalf("full-mailbox TryPostTracked = %v/%d, want dropped/0", outcome, ticket)
	}
	c.Close()
}

func TestPostDeferredTrackedAdmittedAndApplied(t *testing.T) {
	rec := &waitTestRecorder{}
	c := newWaitTestController(rec)

	outcome, ticket := c.PostDeferredTracked(DrawRequested{Key: "deferred"})
	if outcome != PostAdmitted || ticket == 0 {
		t.Fatalf("PostDeferredTracked = %v/%d, want admitted with ticket", outcome, ticket)
	}
	go c.Run()
	defer c.Close()
	if !c.WaitActionApplied(ticket, 2*time.Second) {
		t.Fatal("deferred ticket never released")
	}
}

func TestWaitActionAppliedTimesOutWithoutRun(t *testing.T) {
	rec := &waitTestRecorder{}
	c := newWaitTestController(rec)
	outcome, ticket := c.PostTracked(DrawRequested{Key: "no-run"})
	if outcome != PostAdmitted {
		t.Fatalf("PostTracked = %v, want admitted", outcome)
	}
	start := time.Now()
	if c.WaitActionApplied(ticket, 60*time.Millisecond) {
		t.Fatal("WaitActionApplied reported success without a running actor")
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("WaitActionApplied returned after %v, want a bounded timeout", elapsed)
	}
	c.Close()
}

func TestWaitActionVisibleWaitsForEffectDelivery(t *testing.T) {
	rec := &waitTestRecorder{hold: make(chan struct{}), holdOne: true}
	c := newWaitTestController(rec)
	go c.Run()
	defer c.Close()

	outcome, ticket := c.PostTracked(DrawRequested{Key: "visible"})
	if outcome != PostAdmitted {
		t.Fatalf("PostTracked = %v, want admitted", outcome)
	}
	if !c.WaitActionApplied(ticket, 2*time.Second) {
		t.Fatal("applied fence must open before delivery")
	}
	if c.WaitActionVisible(ticket, 50*time.Millisecond) {
		t.Fatal("visible fence opened while the effect consumer was blocked")
	}
	close(rec.hold)
	if !c.WaitActionVisible(ticket, 2*time.Second) {
		t.Fatal("visible fence never opened after delivery")
	}
}

// TestLastAcceptedTicketIsTheCapacityFence pins the invariant the runtime-event
// bridge relies on: the newest *accepted* ticket (never a dropped post's) is
// the watermark whose apply proves the mailbox has capacity again.
func TestLastAcceptedTicketIsTheCapacityFence(t *testing.T) {
	c := NewUIController(
		UIControllerConfig{MailboxSize: 1},
		ReducerFunc(func(uint64, UIAction) []Effect { return nil }),
		func(Effect) {},
	)
	defer c.Close()

	outcome, first := c.PostTracked(DrawRequested{Key: "fence-1"})
	if outcome != PostAdmitted {
		t.Fatalf("first PostTracked = %v, want admitted", outcome)
	}
	if got := c.LastAcceptedTicket(); got != first {
		t.Fatalf("LastAcceptedTicket = %d, want %d", got, first)
	}
	if outcome, ticket := c.TryPostTracked(Resize{Width: 8, Height: 3}); outcome != PostDropped || ticket != 0 {
		t.Fatalf("full-mailbox TryPostTracked = %v/%d, want dropped/0", outcome, ticket)
	}
	if got := c.LastAcceptedTicket(); got != first {
		t.Fatalf("LastAcceptedTicket advanced on a dropped post: %d, want %d", got, first)
	}

	go c.Run()
	if !c.WaitActionApplied(first, 2*time.Second) {
		t.Fatal("applied fence never opened for the accepted ticket")
	}
	outcome, second := c.TryPostTracked(Resize{Width: 8, Height: 3})
	if outcome != PostAdmitted || second <= first {
		t.Fatalf("TryPostTracked after drain = %v/%d, want admitted with ticket > %d", outcome, second, first)
	}
}
