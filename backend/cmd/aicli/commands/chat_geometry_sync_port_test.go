package commands

import (
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// fakeGeometrySyncPort 是 ui.GeometrySyncPort 的记录式替身：L5-2 Batch A 的
// 迁移契约测试用它断言 commands 侧确实经门面请求探针（而不是直读 surface），
// 并固定 minInterval 路由（软漂移=0 强制探针 / 稳态=100ms 节流）。
type fakeGeometrySyncPort struct {
	mu          sync.Mutex
	intervals   []time.Duration
	sizeChanged bool
	probed      bool
	width       int
}

func (f *fakeGeometrySyncPort) RequestGeometrySync(minInterval time.Duration) (sizeChanged, probed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.intervals = append(f.intervals, minInterval)
	return f.sizeChanged, f.probed
}

func (f *fakeGeometrySyncPort) ActiveBandWidth() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.width
}

func (f *fakeGeometrySyncPort) requestIntervals() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.intervals...)
}

// TestChatInteractionCoordinatorGeometrySyncPortInjection pins the L5-2 Batch A
// wiring: SetSurface injects the surface as the geometry port; unloading the
// surface (or having none) clears it and ActiveBandWidth degrades to 0 without
// panicking.
func TestChatInteractionCoordinatorGeometrySyncPortInjection(t *testing.T) {
	captureSurfaceStdout(t, func() {
		session := &ChatSession{}
		coord := newTestChatInteractionCoordinator(t, session)

		if got := coord.ActiveBandWidth(); got != 0 {
			t.Fatalf("no surface: ActiveBandWidth must degrade to 0, got %d", got)
		}

		surface := ui.NewFixedBottomSurface(ui.NewTerminal())
		surface.EnableForTest(80, 24)
		coord.SetSurface(surface)

		coord.mu.Lock()
		port := coord.geometrySync
		coord.mu.Unlock()
		if port == nil {
			t.Fatal("SetSurface must inject the geometry port")
		}
		if port != surface {
			t.Fatalf("geometry port must be the mounted surface itself: %T", port)
		}
		if got := coord.ActiveBandWidth(); got != 80 {
			t.Fatalf("ActiveBandWidth via injected port: got %d want 80", got)
		}

		coord.SetSurface(nil)
		coord.mu.Lock()
		port = coord.geometrySync
		coord.mu.Unlock()
		if port != nil {
			t.Fatalf("surface unload must clear the geometry port: %T", port)
		}
		if got := coord.ActiveBandWidth(); got != 0 {
			t.Fatalf("after unload ActiveBandWidth must degrade to 0, got %d", got)
		}
	})
}

// TestChatInteractionCoordinatorMaybeRefreshStreamGeometryUsesPort pins the
// paint-path probe routing: soft-width drift forces an unthrottled request
// (minInterval=0), steady state uses DefaultGeometryProbeMinInterval, and the
// requests reach the injected port instead of the surface.
func TestChatInteractionCoordinatorMaybeRefreshStreamGeometryUsesPort(t *testing.T) {
	captureSurfaceStdout(t, func() {
		session := &ChatSession{}
		coord := newTestChatInteractionCoordinator(t, session)

		surface := ui.NewFixedBottomSurface(ui.NewTerminal())
		surface.EnableForTest(40, 24)
		coord.SetSurface(surface)

		stream := ui.NewActiveStreamController(40, ui.ActiveBandRows(24))
		stream.BeginAssistant("assistant")
		fake := &fakeGeometrySyncPort{probed: true, width: 40}

		coord.mu.Lock()
		coord.activeStream = stream
		coord.geometrySync = fake
		// Soft ownership at width 20 disagrees with the surface layout width
		// (40): the paint path must reflow now and force an immediate probe.
		coord.softEmittedSourceStart = 0
		coord.softEmittedSourceEnd = 4
		coord.softEmittedLines = []string{"a", "b"}
		coord.softEmittedWidth = 20
		refreshed := coord.maybeRefreshStreamGeometryLocked()
		coord.mu.Unlock()

		if !refreshed {
			t.Fatal("soft drift must refresh the viewport")
		}
		if got := fake.requestIntervals(); len(got) != 1 || got[0] != 0 {
			t.Fatalf("soft drift must force an unthrottled probe (minInterval=0): %v", got)
		}

		// Steady state: no soft drift, so the 30 FPS paint loop must use the
		// throttled interval instead of probing every frame.
		coord.mu.Lock()
		coord.softEmittedSourceStart = 0
		coord.softEmittedSourceEnd = 0
		coord.softEmittedLines = nil
		coord.softEmittedWidth = 0
		_ = coord.maybeRefreshStreamGeometryLocked()
		coord.mu.Unlock()

		got := fake.requestIntervals()
		if len(got) != 2 || got[1] != ui.DefaultGeometryProbeMinInterval {
			t.Fatalf("steady paint must use the throttled probe interval: %v", got)
		}
	})
}

// TestChatInteractionCoordinatorGeometryPortNilSafeDegradation pins the
// "surface == nil / port == nil 安全降级" contract of the migrated call sites:
// no probe is attempted and no panic occurs; soft drift still reflows from the
// cached width.
func TestChatInteractionCoordinatorGeometryPortNilSafeDegradation(t *testing.T) {
	captureSurfaceStdout(t, func() {
		session := &ChatSession{}
		coord := newTestChatInteractionCoordinator(t, session)

		stream := ui.NewActiveStreamController(40, ui.ActiveBandRows(24))
		stream.BeginAssistant("assistant")
		coord.mu.Lock()
		coord.activeStream = stream
		coord.geometrySync = nil
		refreshed := coord.maybeRefreshStreamGeometryLocked()
		coord.mu.Unlock()
		if refreshed {
			t.Fatal("no surface + no soft window must not refresh")
		}

		// Surface mounted but the port explicitly cleared (white-box
		// degradation path): the probe is skipped, the cached width still
		// drives soft reflow.
		surface := ui.NewFixedBottomSurface(ui.NewTerminal())
		surface.EnableForTest(40, 24)
		coord.SetSurface(surface)
		coord.mu.Lock()
		coord.geometrySync = nil
		coord.softEmittedSourceStart = 0
		coord.softEmittedSourceEnd = 4
		coord.softEmittedLines = []string{"a", "b"}
		coord.softEmittedWidth = 20
		refreshed = coord.maybeRefreshStreamGeometryLocked()
		coord.mu.Unlock()
		if !refreshed {
			t.Fatal("soft drift must still reflow from the cached width when the port is absent")
		}
	})
}

// TestChatInteractionCoordinatorRefreshForkUsesGeometryPort pins the
// unified/legacy fork on the migrated explicit-refresh path with the probe
// routed through a fake port:
//   - legacy: the measured surface geometry is reported directly to AppState;
//   - unified: no direct report (Resize 统一走 presenter probe → AppState 链路).
func TestChatInteractionCoordinatorRefreshForkUsesGeometryPort(t *testing.T) {
	for _, unified := range []bool{false, true} {
		name := "legacy"
		if unified {
			name = "unified"
		}
		t.Run(name, func(t *testing.T) {
			captureSurfaceStdout(t, func() {
				session := &ChatSession{}
				coord := newTestChatInteractionCoordinator(t, session)
				session.Interaction = coord

				surface := ui.NewFixedBottomSurface(ui.NewTerminal())
				surface.EnableForTest(91, 31)
				coord.SetSurface(surface)

				fake := &fakeGeometrySyncPort{probed: true}
				coord.mu.Lock()
				coord.unifiedRenderer = unified
				coord.geometrySync = fake
				coord.mu.Unlock()

				coord.RefreshActiveStreamViewport()
				coord.waitUIActorIdle()

				if got := fake.requestIntervals(); len(got) != 1 || got[0] != 0 {
					t.Fatalf("explicit refresh must probe through the port (minInterval=0): %v", got)
				}
				state := coord.uiActor.AppState()
				if unified {
					if state.Geometry.Width != 0 || state.Geometry.Height != 0 {
						t.Fatalf("unified refresh must not report surface geometry directly: %+v", state.Geometry)
					}
				} else if state.Geometry.Width != 91 || state.Geometry.Height != 31 {
					t.Fatalf("legacy refresh must report measured surface geometry: %+v", state.Geometry)
				}
			})
		})
	}
}

// TestChatDebugDocumentWidthUsesGeometryPort pins the debug-document width read
// migration: the width comes from the injected geometry port instead of a
// direct session.Surface.ActiveBandViewportSize read.
func TestChatDebugDocumentWidthUsesGeometryPort(t *testing.T) {
	captureSurfaceStdout(t, func() {
		session := &ChatSession{}
		coord := newTestChatInteractionCoordinator(t, session)
		session.Interaction = coord

		surface := ui.NewFixedBottomSurface(ui.NewTerminal())
		surface.EnableForTest(64, 24)
		coord.SetSurface(surface)

		if got := chatDebugDocumentWidth(session); got != 64 {
			t.Fatalf("debug document width must come from the geometry port: got %d want 64", got)
		}
	})
}
