package ui

import (
	"bytes"
	"io"
	"testing"
)

func TestFixedBottomSurfacePhysicalWritesDefaultEnabled(t *testing.T) {
	surface := &FixedBottomSurface{}
	if !surface.PhysicalWritesEnabled() {
		t.Fatal("zero-value surface must preserve the legacy enabled default")
	}

	var output bytes.Buffer
	surface.mu.Lock()
	WithTerminalWriteLock(func() {
		if err := surface.flushHoldingLock(&output, func(w io.Writer) {
			_, _ = io.WriteString(w, "legacy-frame")
		}); err != nil {
			t.Fatalf("flushHoldingLock returned error: %v", err)
		}
	})
	surface.mu.Unlock()
	if got := output.String(); got != "legacy-frame" {
		t.Fatalf("legacy default output = %q, want %q", got, "legacy-frame")
	}
}

func TestFixedBottomSurfacePhysicalWritesFenceSuppressesOwnedOutput(t *testing.T) {
	surface := NewFixedBottomSurface(NewTerminal())
	surface.EnableForTest(40, 12)
	surface.SetPhysicalWritesEnabled(false)
	if surface.PhysicalWritesEnabled() {
		t.Fatal("physical writer fence remained enabled")
	}

	var output bytes.Buffer
	n, err, handled := surface.WriteOutput(&output, "retained line\n")
	if err != nil || !handled {
		t.Fatalf("fenced WriteOutput: handled=%t n=%d err=%v", handled, n, err)
	}
	if output.Len() != 0 {
		t.Fatalf("fenced WriteOutput emitted %q", output.String())
	}
	if n == 0 || len(surface.HistoryRowsSnapshot()) == 0 {
		t.Fatal("fenced WriteOutput did not retain logical history state")
	}
	if n, err, handled := surface.WriteSoftTrackedOutput(&output, "mutable tail\n"); err != nil || !handled || n == 0 {
		t.Fatalf("fenced WriteSoftTrackedOutput: handled=%t n=%d err=%v", handled, n, err)
	}
	if output.Len() != 0 {
		t.Fatalf("fenced WriteSoftTrackedOutput emitted %q", output.String())
	}
	if !surface.SoftOutputTailValid() {
		t.Fatal("fenced soft output did not retain logical tail state")
	}

	// The same fence applies to the shared presenter path used by owned frames.
	surface.mu.Lock()
	WithTerminalWriteLock(func() {
		if err := surface.flushHoldingLock(&output, func(w io.Writer) {
			_, _ = io.WriteString(w, "owned-frame")
		}); err != nil {
			t.Fatalf("fenced flushHoldingLock returned error: %v", err)
		}
	})
	surface.mu.Unlock()
	if output.Len() != 0 {
		t.Fatalf("fenced owned flush emitted %q", output.String())
	}

	surface.mu.Lock()
	WithTerminalWriteLock(func() {
		if _, ok := surface.insertHistoryLinesInRegionLocked([]string{"handoff row"}, 4); ok {
			t.Fatal("fenced DECSTBM handoff reported a physical write")
		}
	})
	surface.mu.Unlock()
}

func TestFixedBottomSurfacePhysicalWritesFenceCanBeReenabled(t *testing.T) {
	surface := NewFixedBottomSurface(NewTerminal())
	surface.SetPhysicalWritesEnabled(false)
	surface.SetPhysicalWritesEnabled(true)
	if !surface.PhysicalWritesEnabled() {
		t.Fatal("physical writer fence did not re-enable")
	}

	var output bytes.Buffer
	surface.mu.Lock()
	WithTerminalWriteLock(func() {
		if err := surface.flushHoldingLock(&output, func(w io.Writer) {
			_, _ = io.WriteString(w, "enabled-again")
		}); err != nil {
			t.Fatalf("re-enabled flushHoldingLock returned error: %v", err)
		}
	})
	surface.mu.Unlock()
	if got := output.String(); got != "enabled-again" {
		t.Fatalf("re-enabled output = %q, want %q", got, "enabled-again")
	}
}
