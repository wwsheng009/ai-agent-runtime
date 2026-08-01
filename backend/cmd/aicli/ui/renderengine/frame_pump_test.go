package renderengine

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitFor polls cond until it is true or the timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}

func TestScheduleExecutesAfterDelay(t *testing.T) {
	p := NewFramePump()
	defer p.Shutdown()
	var ran atomic.Bool
	p.Schedule("k", 10*time.Millisecond, func() { ran.Store(true) })
	waitFor(t, time.Second, func() bool { return ran.Load() })
}

func TestScheduleReplacesSameKey(t *testing.T) {
	p := NewFramePump()
	defer p.Shutdown()
	var first, second atomic.Int32
	p.Schedule("k", 5*time.Millisecond, func() { first.Add(1) })
	p.Schedule("k", 10*time.Millisecond, func() { second.Add(1) })
	waitFor(t, time.Second, func() bool { return second.Load() == 1 })
	// Give the replaced timer a chance to fire if the merge failed.
	time.Sleep(30 * time.Millisecond)
	if first.Load() != 0 {
		t.Fatalf("replaced job executed: first=%d", first.Load())
	}
	if second.Load() != 1 {
		t.Fatalf("replacing job executed more than once: second=%d", second.Load())
	}
}

func TestCancelPreventsPendingJob(t *testing.T) {
	p := NewFramePump()
	defer p.Shutdown()
	var ran atomic.Bool
	p.Schedule("k", 10*time.Millisecond, func() { ran.Store(true) })
	p.Cancel("k")
	time.Sleep(40 * time.Millisecond)
	if ran.Load() {
		t.Fatal("cancelled job executed")
	}
	if p.Pending("k") {
		t.Fatal("cancelled job still reported pending")
	}
}

func TestPendingState(t *testing.T) {
	p := NewFramePump()
	defer p.Shutdown()
	if p.Pending("k") {
		t.Fatal("no job should be pending initially")
	}
	done := make(chan struct{})
	p.Schedule("k", 10*time.Millisecond, func() { close(done) })
	if !p.Pending("k") {
		t.Fatal("armed job should be pending")
	}
	if p.PendingCount() != 1 {
		t.Fatalf("PendingCount = %d, want 1", p.PendingCount())
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("job did not fire")
	}
	waitFor(t, time.Second, func() bool { return !p.Pending("k") })
	if p.PendingCount() != 0 {
		t.Fatalf("PendingCount = %d after fire, want 0", p.PendingCount())
	}
}

func TestRescheduleAfterFire(t *testing.T) {
	p := NewFramePump()
	defer p.Shutdown()
	var count atomic.Int32
	p.Schedule("k", 5*time.Millisecond, func() { count.Add(1) })
	waitFor(t, time.Second, func() bool { return count.Load() == 1 })
	// After firing, the key is free again: a new schedule must run again.
	p.Schedule("k", 5*time.Millisecond, func() { count.Add(1) })
	waitFor(t, time.Second, func() bool { return count.Load() == 2 })
}

func TestShutdownCancelsAndMakesScheduleNoOp(t *testing.T) {
	p := NewFramePump()
	var ran atomic.Bool
	p.Schedule("k", 5*time.Millisecond, func() { ran.Store(true) })
	p.Shutdown()
	time.Sleep(30 * time.Millisecond)
	if ran.Load() {
		t.Fatal("job executed after Shutdown")
	}
	p.Schedule("k2", 1*time.Millisecond, func() { ran.Store(true) })
	time.Sleep(20 * time.Millisecond)
	if ran.Load() {
		t.Fatal("Schedule after Shutdown executed")
	}
	p.Shutdown() // idempotent
}

func TestSerialExecution(t *testing.T) {
	p := NewFramePump()
	defer p.Shutdown()
	var active atomic.Int32
	var maxActive atomic.Int32
	var done sync.WaitGroup
	for i := 0; i < 8; i++ {
		done.Add(1)
		key := string(rune('a' + i))
		p.Schedule(key, 2*time.Millisecond, func() {
			cur := active.Add(1)
			for {
				prev := maxActive.Load()
				if cur <= prev || maxActive.CompareAndSwap(prev, cur) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			active.Add(-1)
			done.Done()
		})
	}
	doneCh := make(chan struct{})
	go func() { done.Wait(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("callbacks did not all complete")
	}
	if maxActive.Load() > 1 {
		t.Fatalf("callbacks ran concurrently: maxActive=%d", maxActive.Load())
	}
}

func TestScheduleIgnoresInvalidArgs(t *testing.T) {
	p := NewFramePump()
	defer p.Shutdown()
	p.Schedule("", 5*time.Millisecond, func() {})   // empty key
	p.Schedule("k", -1*time.Millisecond, func() {}) // negative delay
	p.Schedule("k2", 5*time.Millisecond, nil)       // nil fn
	if p.PendingCount() != 0 {
		t.Fatalf("invalid schedules armed jobs: %d", p.PendingCount())
	}
}

func TestEngineInvalidateAndCancel(t *testing.T) {
	e := NewEngine()
	defer e.Shutdown()
	var ran atomic.Bool
	e.Invalidate("k", "test", 10*time.Millisecond, func() { ran.Store(true) })
	if !e.Pending("k") {
		t.Fatal("Invalidate did not arm a job")
	}
	e.Cancel("k")
	time.Sleep(30 * time.Millisecond)
	if ran.Load() {
		t.Fatal("cancelled Engine job executed")
	}
	// Re-arm after cancel to prove the key is reusable.
	e.Invalidate("k", "test", 5*time.Millisecond, func() { ran.Store(true) })
	waitFor(t, time.Second, func() bool { return ran.Load() })
}

func TestFramePumpTracksDirtyUnionAndReplacement(t *testing.T) {
	p := NewFramePumpWithConfig(FramePumpConfig{MaxFPS: -1})
	defer p.Shutdown()
	p.ScheduleDirty("status", DirtyStatus, time.Hour, func() {})
	p.ScheduleDirty("prompt", DirtyPrompt, time.Hour, func() {})
	if got, want := p.Dirty(), DirtyStatus|DirtyPrompt; got != want {
		t.Fatalf("Dirty = %#x, want %#x", got, want)
	}
	p.ScheduleDirty("status", DirtyGeometry, time.Hour, func() {})
	stats := p.Stats()
	if stats.Scheduled != 3 || stats.Replaced != 1 || stats.Pending != 2 {
		t.Fatalf("stats = %+v, want scheduled=3 replaced=1 pending=2", stats)
	}
	p.Cancel("status")
	if got, want := p.Dirty(), DirtyPrompt; got != want {
		t.Fatalf("Dirty after cancel = %#x, want %#x", got, want)
	}
	p.Cancel("prompt")
	if p.Dirty() != DirtyNone {
		t.Fatalf("Dirty after cancelling all jobs = %#x, want zero", p.Dirty())
	}
}

func TestFramePumpHonorsFrameBudget(t *testing.T) {
	p := NewFramePumpWithConfig(FramePumpConfig{MaxFPS: 20})
	defer p.Shutdown()
	first := make(chan time.Time, 1)
	second := make(chan time.Time, 1)
	p.Schedule("first", 0, func() { first <- time.Now() })
	firstAt := <-first
	p.Schedule("second", 0, func() { second <- time.Now() })
	secondAt := <-second
	if elapsed := secondAt.Sub(firstAt); elapsed < 35*time.Millisecond {
		t.Fatalf("second frame ran after %v, want at least one budget interval", elapsed)
	}
	if stats := p.Stats(); stats.Frames != 2 || stats.Fired != 2 {
		t.Fatalf("stats = %+v, want two frames and callbacks", stats)
	}
}

func TestEngineInvalidateClassifiesReason(t *testing.T) {
	e := NewEngine()
	defer e.Shutdown()
	e.Invalidate("status", "dynamic-status", time.Hour, func() {})
	e.Invalidate("band", "active-frame", time.Hour, func() {})
	if got, want := e.Dirty(), DirtyStatus|DirtyBand|DirtyContent; got != want {
		t.Fatalf("Engine Dirty = %#x, want %#x", got, want)
	}
}

func TestFramePumpSetMaxFPSWakesScheduler(t *testing.T) {
	p := NewFramePumpWithConfig(FramePumpConfig{MaxFPS: -1})
	defer p.Shutdown()
	p.SetMaxFPS(25)
	if got := p.MaxFPS(); got != 25 {
		t.Fatalf("MaxFPS = %d, want 25", got)
	}
	p.SetMaxFPS(0)
	if got := p.MaxFPS(); got != defaultFrameMaxFPS {
		t.Fatalf("MaxFPS after zero reset = %d, want %d", got, defaultFrameMaxFPS)
	}
}
