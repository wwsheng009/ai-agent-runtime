package commands

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// 需求：Windows 控制台没有 SIGWINCH，统一渲染器的 presenter 只在 flush
// effect 里探测几何，因此空闲会话缩放窗口后永远收不到 Resize，布局会一直
// 停留在旧宽度（直到下一次按键触发一帧）。看门狗必须把真实探测到的尺寸
// 作为 Applied Resize 投递到 actor。
func TestGeometryWatcherPublishesIdleResizeToActor(t *testing.T) {
	coord := newTestChatInteractionCoordinator(t, &ChatSession{})
	t.Cleanup(coord.Shutdown)

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	coord.SetSurface(surface)
	actor := coord.ensureUIActor()
	if actor == nil {
		t.Fatal("expected UI actor")
	}
	if !actor.Post(ui.Resize{Width: 80, Height: 24, Generation: 1}) {
		t.Fatal("seed geometry")
	}
	coord.waitUIActorIdle()

	var width, height atomic.Int64
	width.Store(120)
	height.Store(40)
	coord.geometryWatchProbe = func() (int, int, bool) {
		return int(width.Load()), int(height.Load()), true
	}

	if !coord.pollUnifiedGeometry(0) {
		t.Fatal("idle resize was not forwarded to the actor")
	}
	coord.waitUIActorIdle()

	geometry := actor.Geometry()
	if geometry.Width != 120 || geometry.Height != 40 {
		t.Fatalf("actor geometry = %+v, want 120x40", geometry)
	}

	// 同一尺寸不得重复投递：重复 Resize 会推进 layout generation，把
	// history effect 反复作废。
	if coord.pollUnifiedGeometry(0) {
		t.Fatal("unchanged geometry was reposted as a Resize")
	}
}

// 需求：探测失败（非 TTY/管道）不得改写 actor 几何，也不得报错。
func TestGeometryWatcherIgnoresFailedProbe(t *testing.T) {
	coord := newTestChatInteractionCoordinator(t, &ChatSession{})
	t.Cleanup(coord.Shutdown)

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	coord.SetSurface(surface)
	actor := coord.ensureUIActor()
	if !actor.Post(ui.Resize{Width: 80, Height: 24, Generation: 1}) {
		t.Fatal("seed geometry")
	}
	coord.waitUIActorIdle()

	coord.geometryWatchProbe = func() (int, int, bool) { return 0, 0, false }
	if coord.pollUnifiedGeometry(0) {
		t.Fatal("failed probe must not post a Resize")
	}
	if geometry := actor.Geometry(); geometry.Width != 80 || geometry.Height != 24 {
		t.Fatalf("failed probe changed geometry: %+v", geometry)
	}
}

// 需求：看门狗只在 presenter attach 之后运行，且 Shutdown 后必须停止。
func TestGeometryWatcherLifecycle(t *testing.T) {
	coord := newTestChatInteractionCoordinator(t, &ChatSession{})
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	coord.SetSurface(surface)

	coord.startGeometryWatcher()
	coord.geometryWatcherMu.Lock()
	first := coord.geometryWatcherStop
	coord.geometryWatcherMu.Unlock()
	if first == nil {
		t.Fatal("watcher did not start")
	}
	// 幂等：重复启动不得创建第二个 goroutine/通道。
	coord.startGeometryWatcher()
	coord.geometryWatcherMu.Lock()
	second := coord.geometryWatcherStop
	coord.geometryWatcherMu.Unlock()
	if first != second {
		t.Fatal("watcher started twice")
	}

	coord.stopGeometryWatcher()
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("watcher did not observe its stop signal")
	}
	coord.stopGeometryWatcher() // idempotent

	// A stopped watcher must be restartable (session replacement) and must not
	// leave the old goroutine probing the new surface.
	coord.startGeometryWatcher()
	coord.geometryWatcherMu.Lock()
	restarted := coord.geometryWatcherStop != nil && coord.geometryWatcherStop != first
	coord.geometryWatcherMu.Unlock()
	if !restarted {
		t.Fatal("watcher did not restart after stop")
	}
	coord.stopGeometryWatcher()
}

// 需求：拖动窗口会逐帧报告不同宽度。逐值发布会让 layout generation 反复
// 前进（每次都会 rebase/重绘），缩放过程中界面抖动、历史反复重折。看门狗
// 必须要求连续两次观测一致后才发布。
func TestGeometryWatcherSettleFilterCollapsesDrag(t *testing.T) {
	coord := newTestChatInteractionCoordinator(t, &ChatSession{})
	t.Cleanup(coord.Shutdown)

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	coord.SetSurface(surface)
	actor := coord.ensureUIActor()
	if !actor.Post(ui.Resize{Width: 80, Height: 24, Generation: 1}) {
		t.Fatal("seed geometry")
	}
	coord.waitUIActorIdle()

	width := 100
	coord.geometryWatchProbe = func() (int, int, bool) {
		return width, 30, true
	}

	// First observation only arms the filter; nothing is published yet.
	coord.pollUnifiedGeometrySettled()
	if geometry := actor.Geometry(); geometry.Width != 80 {
		t.Fatalf("first observation published %+v, want the previous 80", geometry)
	}
	// The drag continues: a different width restarts the window.
	width = 110
	coord.pollUnifiedGeometrySettled()
	if geometry := actor.Geometry(); geometry.Width != 80 {
		t.Fatalf("intermediate drag width published %+v", geometry)
	}
	// The user stops at 110: the next identical tick settles and publishes once.
	coord.pollUnifiedGeometrySettled()
	coord.waitUIActorIdle()
	if geometry := actor.Geometry(); geometry.Width != 110 {
		t.Fatalf("settled width not published: %+v", geometry)
	}
	// Further identical ticks must not advance the generation again.
	generation := actor.Geometry().Generation
	coord.pollUnifiedGeometrySettled()
	coord.waitUIActorIdle()
	if got := actor.Geometry().Generation; got != generation {
		t.Fatalf("idle tick advanced layout generation %d -> %d", generation, got)
	}
}

// 需求：Applied Resize 的 reducer 适配器在 reduceUIControllerState 之前执行，
// 此刻 AppState.Geometry 仍是旧宽度。适配器不得在这个时刻 reflow（会把源文本
// 按旧宽度折行，随后新宽度再折一次，正是缩放后重复/错乱的成因），而应把
// reflow 作为因果 follow-up 排到该 Resize 归约之后。
func TestAppliedResizeDefersReflowUntilGeometryIsReduced(t *testing.T) {
	coord := newTestChatInteractionCoordinator(t, &ChatSession{})
	t.Cleanup(coord.Shutdown)

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	coord.SetSurface(surface)
	actor := coord.ensureUIActor()
	if !actor.Post(ui.Resize{Width: 80, Height: 24, Generation: 1}) {
		t.Fatal("seed geometry")
	}
	coord.waitUIActorIdle()

	coord.mu.Lock()
	coord.unifiedRenderer = true
	// The geometry facade reads the lock-free writer snapshot, so publish it the
	// same way SetPrimaryPresenter does.
	coord.refreshTerminalWriterSnapshotLocked()
	coord.activeStream = ui.NewActiveStreamController(80, ui.ActiveBandRows(24))
	coord.activeStream.BeginAssistant("assistant")
	coord.mu.Unlock()

	// The surface measurement is already applied by the probe, so the adapter
	// itself must not reflow at the pre-reduction geometry; the reflow runs as a
	// causal follow-up once AppState carries 120.
	if !actor.Post(ui.Resize{Width: 120, Height: 40, Applied: true}) {
		t.Fatal("post applied resize")
	}
	coord.waitUIActorIdle()
	coord.mu.Lock()
	gotWidth := 0
	if coord.activeStream != nil {
		gotWidth = coord.activeStream.ViewportWidth()
	}
	coord.mu.Unlock()
	if gotWidth != 120 {
		t.Fatalf("deferred reflow width = %d, want 120", gotWidth)
	}
}
