package main

import (
	"testing"
	"time"
)

func TestIdleMemoryReclaimDecision(t *testing.T) {
	now := time.Date(2026, 11, 8, 23, 0, 0, 0, time.Local)
	quiet := idleMemorySample{
		totalAlloc:   100 << 20,
		heapIdle:     200 << 20,
		heapReleased: 50 << 20, // committed idle = 150 MiB
	}

	t.Run("first sample arms the window", func(t *testing.T) {
		due, stable := idleMemoryReclaimDecision(now, time.Time{}, time.Time{}, quiet, quiet)
		if due {
			t.Fatal("first sample must not reclaim")
		}
		if !stable.Equal(now) {
			t.Fatalf("stableSince = %v, want %v", stable, now)
		}
	})

	t.Run("allocation traffic resets the window", func(t *testing.T) {
		previous := quiet
		current := quiet
		current.totalAlloc += idleMemoryReclaimMaxAllocDelta + 1
		due, stable := idleMemoryReclaimDecision(now, now.Add(-time.Hour), time.Time{}, previous, current)
		if due {
			t.Fatal("active allocation must not reclaim")
		}
		if !stable.Equal(now) {
			t.Fatalf("stableSince = %v, want reset to %v", stable, now)
		}
	})

	t.Run("counter rollback resets the window", func(t *testing.T) {
		previous := quiet
		current := quiet
		current.totalAlloc--
		due, stable := idleMemoryReclaimDecision(now, now.Add(-time.Hour), time.Time{}, previous, current)
		if due {
			t.Fatal("counter rollback must not reclaim")
		}
		if !stable.Equal(now) {
			t.Fatalf("stableSince = %v, want reset to %v", stable, now)
		}
	})

	t.Run("insufficient committed idle keeps the window", func(t *testing.T) {
		stableSince := now.Add(-time.Hour)
		current := quiet
		current.heapReleased = current.heapIdle - (idleMemoryReclaimMinCommittedIdle - 1)
		due, stable := idleMemoryReclaimDecision(now, stableSince, time.Time{}, quiet, current)
		if due {
			t.Fatal("insufficient committed idle must not reclaim")
		}
		if !stable.Equal(stableSince) {
			t.Fatalf("stableSince = %v, want preserved %v", stable, stableSince)
		}
	})

	t.Run("needs a stable idle period", func(t *testing.T) {
		stableSince := now.Add(-idleMemoryReclaimStableFor + time.Second)
		due, stable := idleMemoryReclaimDecision(now, stableSince, time.Time{}, quiet, quiet)
		if due {
			t.Fatal("must not reclaim before the stable window")
		}
		if !stable.Equal(stableSince) {
			t.Fatalf("stableSince = %v, want preserved %v", stable, stableSince)
		}
	})

	t.Run("cooldown suppresses repeated reclaim", func(t *testing.T) {
		stableSince := now.Add(-time.Hour)
		lastReclaim := now.Add(-idleMemoryReclaimCooldown + time.Second)
		due, _ := idleMemoryReclaimDecision(now, stableSince, lastReclaim, quiet, quiet)
		if due {
			t.Fatal("must not reclaim during cooldown")
		}
	})

	t.Run("due after stable idle", func(t *testing.T) {
		stableSince := now.Add(-idleMemoryReclaimStableFor)
		due, _ := idleMemoryReclaimDecision(now, stableSince, time.Time{}, quiet, quiet)
		if !due {
			t.Fatal("expected reclaim after stable idle")
		}
	})
}

func TestIdleMemoryReclaimLoopStops(t *testing.T) {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		idleMemoryReclaimLoop(stop, time.Hour)
		close(done)
	}()
	close(stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reclaim loop did not stop")
	}
}

func TestIdleMemoryReclaimEnabled(t *testing.T) {
	cases := []struct {
		name      string
		reclaim   string
		aicliGOGC string
		gogc      string
		want      bool
	}{
		{name: "default on", want: true},
		{name: "explicit off", reclaim: "off", want: false},
		{name: "explicit none", reclaim: "none", want: false},
		{name: "explicit on", reclaim: "on", want: true},
		{name: "AICLI_GOGC off", aicliGOGC: "off", want: false},
		{name: "GOGC off", gogc: "off", want: false},
		{name: "AICLI_GOGC wins over GOGC", aicliGOGC: "50", gogc: "off", want: true},
		{name: "numeric GOGC keeps reclaim", gogc: "50", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AICLI_MEMORY_RECLAIM", tc.reclaim)
			t.Setenv("AICLI_GOGC", tc.aicliGOGC)
			t.Setenv("GOGC", tc.gogc)
			if got := idleMemoryReclaimEnabled(); got != tc.want {
				t.Fatalf("idleMemoryReclaimEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}
