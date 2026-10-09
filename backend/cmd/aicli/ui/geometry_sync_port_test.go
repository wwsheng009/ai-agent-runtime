package ui

import (
	"testing"
	"time"
)

// TestFixedBottomSurface_GeometrySyncPortActiveBandWidth pins the read side of
// the L5-2 Batch A geometry facade: ActiveBandWidth must mirror
// ActiveBandViewportSize's cached width without issuing terminal probes.
func TestFixedBottomSurface_GeometrySyncPortActiveBandWidth(t *testing.T) {
	surface := newTestFixedBottomSurfaceWithSize(80, 24)

	if got, want := surface.ActiveBandWidth(), 80; got != want {
		t.Fatalf("ActiveBandWidth: got %d want %d", got, want)
	}
	if width, _ := surface.ActiveBandViewportSize(); surface.ActiveBandWidth() != width {
		t.Fatalf("ActiveBandWidth must mirror ActiveBandViewportSize: got %d want %d",
			surface.ActiveBandWidth(), width)
	}

	// Cached read: a pinned resize is visible without a probe call.
	term := surface.terminal
	before := term.SizeProbeCountForTest()
	term.SetSizeForTest(40, 20)
	if got, want := surface.ActiveBandWidth(), 40; got != want {
		t.Fatalf("ActiveBandWidth after pinned resize: got %d want %d", got, want)
	}
	if got := term.SizeProbeCountForTest() - before; got != 0 {
		t.Fatalf("ActiveBandWidth must not probe the terminal: probes=%d", got)
	}
}

// TestFixedBottomSurface_GeometrySyncPortThrottleContract pins the L5-2 Batch A
// facade throttle contract (100ms class): within minInterval the request skips
// without touching the terminal or advancing the probe clock; a zero interval
// forces an immediate probe; each probe calls RefreshSize exactly once.
func TestFixedBottomSurface_GeometrySyncPortThrottleContract(t *testing.T) {
	surface := newTestFixedBottomSurfaceWithSize(80, 24)
	term := surface.terminal

	// Seed the probe clock with a forced probe.
	var changed, probed bool
	before := term.SizeProbeCountForTest()
	captureUIStdout(t, func() {
		changed, probed = surface.RequestGeometrySync(0)
	})
	if !probed {
		t.Fatal("zero-interval RequestGeometrySync must probe")
	}
	if changed {
		t.Fatal("fresh-layout probe must not report sizeChanged")
	}
	if got := term.SizeProbeCountForTest() - before; got != 1 {
		t.Fatalf("forced probe RefreshSize calls: got %d want 1", got)
	}
	firstProbe := surface.lastGeometryProbeAt
	if firstProbe.IsZero() {
		t.Fatal("expected lastGeometryProbeAt after forced probe")
	}

	// Within the throttle window: skip, no terminal access, no clock advance.
	surface.lastWidth, surface.lastHeight = 80, 24
	term.SetSizeForTest(40, 20)
	before = term.SizeProbeCountForTest()
	captureUIStdout(t, func() {
		changed, probed = surface.RequestGeometrySync(time.Hour)
	})
	if probed {
		t.Fatal("throttled RequestGeometrySync must skip while minInterval has not elapsed")
	}
	if changed {
		t.Fatal("skipped request must not report sizeChanged")
	}
	if surface.lastWidth != 80 || surface.lastHeight != 24 {
		t.Fatalf("layout cache must stay stale across skipped probe: got %dx%d", surface.lastWidth, surface.lastHeight)
	}
	if !surface.lastGeometryProbeAt.Equal(firstProbe) {
		t.Fatal("skipped request must not advance lastGeometryProbeAt")
	}
	if got := term.SizeProbeCountForTest() - before; got != 0 {
		t.Fatalf("skipped request must not probe the terminal: probes=%d", got)
	}

	// Zero interval forces the probe and observes the pinned resize.
	before = term.SizeProbeCountForTest()
	captureUIStdout(t, func() {
		changed, probed = surface.RequestGeometrySync(0)
	})
	if !probed || !changed {
		t.Fatalf("forced request must probe and report size change: probed=%t changed=%t", probed, changed)
	}
	if got := term.SizeProbeCountForTest() - before; got != 1 {
		t.Fatalf("forced resize probe RefreshSize calls: got %d want 1", got)
	}
	if surface.lastWidth != 40 || surface.lastHeight != 20 {
		t.Fatalf("layout cache after forced probe: got %dx%d want 40x20", surface.lastWidth, surface.lastHeight)
	}
}

// TestFixedBottomSurface_GeometrySyncPortNilSafe guards the coordinator's
// "surface == nil 安全降级" contract: nil receivers must not panic.
func TestFixedBottomSurface_GeometrySyncPortNilSafe(t *testing.T) {
	var surface *FixedBottomSurface
	if changed, probed := surface.RequestGeometrySync(time.Millisecond); changed || probed {
		t.Fatalf("nil surface request must degrade to (false, false): changed=%t probed=%t", changed, probed)
	}
	if got := surface.ActiveBandWidth(); got != 0 {
		t.Fatalf("nil surface ActiveBandWidth: got %d want 0", got)
	}
}
