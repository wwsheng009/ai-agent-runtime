package ui

import (
	"bytes"
	"testing"
)

func TestFixedBottomSurfacePhysicalWritesDefaultEnabled(t *testing.T) {
	surface := &FixedBottomSurface{}
	if !surface.PhysicalWritesEnabled() {
		t.Fatal("zero-value surface must preserve the legacy enabled default")
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
}

func TestFixedBottomSurfacePhysicalWritesFenceCanBeReenabled(t *testing.T) {
	surface := NewFixedBottomSurface(NewTerminal())
	surface.SetPhysicalWritesEnabled(false)
	surface.SetPhysicalWritesEnabled(true)
	if !surface.PhysicalWritesEnabled() {
		t.Fatal("physical writer fence did not re-enable")
	}
}
