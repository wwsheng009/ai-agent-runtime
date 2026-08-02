package renderengine

import (
	"io"
	"time"
)

// Engine is the renderengine facade and the shared owner of the render
// pipeline's scheduler, presenter, screen model, composer, and markdown
// cache. The historical ui/viewport package now delegates to these owned
// primitives for source compatibility; SceneState remains the next migration
// boundary for production owned viewport paths.
type Engine struct {
	pump      *FramePump
	presenter *Presenter
	composer  *Composer
	cache     *RenderCache
	handoff   *HandoffFrontier
	trace     *PaintTrace
}

// NewEngine creates an Engine with a live FramePump.
func NewEngine() *Engine {
	return &Engine{
		pump:      NewFramePump(),
		presenter: NewPresenter(),
		composer:  NewComposer(),
		cache:     SharedRenderCache(),
		handoff:   NewHandoffFrontier(),
		trace:     NewPaintTrace(),
	}
}

// Pump returns the underlying FramePump. Exposed for coordinator wiring and
// tests; production callers should prefer Invalidate.
func (e *Engine) Pump() *FramePump {
	if e == nil {
		return nil
	}
	return e.pump
}

// Presenter returns the engine-owned terminal batcher. UI facades may adopt
// this instance so scheduling and presentation share one render authority.
func (e *Engine) Presenter() *Presenter {
	if e == nil {
		return nil
	}
	return e.presenter
}

// Composer returns the stateless layout facade used by owned viewport paths.
func (e *Engine) Composer() *Composer {
	if e == nil {
		return nil
	}
	return e.composer
}

// Cache returns the Engine-owned shared markdown cache.
func (e *Engine) Cache() *RenderCache {
	if e == nil {
		return nil
	}
	return e.cache
}

// HandoffFrontier returns the Engine-owned scrollback boundary. The surface
// and reconcile paths share this instance so handoff progress cannot diverge
// between presenters using one Engine.
func (e *Engine) HandoffFrontier() *HandoffFrontier {
	if e == nil {
		return nil
	}
	if e.handoff == nil {
		e.handoff = NewHandoffFrontier()
	}
	return e.handoff
}

// Trace returns the engine-owned paint reconciliation probe. The owned
// viewport backend is wired to this instance, so /debug on|off toggles one
// shared observability authority for the whole render path.
func (e *Engine) Trace() *PaintTrace {
	if e == nil {
		return nil
	}
	if e.trace == nil {
		e.trace = NewPaintTrace()
	}
	return e.trace
}

// NewScreenModel creates a screen model through the Engine boundary. The
// returned model is owned by the caller so lifecycle and locking remain
// explicit during the migration from FixedBottomSurface.
func (e *Engine) NewScreenModel(width, height int) *ScreenModel {
	if e == nil {
		return nil
	}
	return NewScreenModel(width, height)
}

// Flush emits one synchronously assembled frame through the engine presenter.
// It is primarily used by owned viewport and deterministic test paths; normal
// production scheduling still enters through Invalidate.
func (e *Engine) Flush(w io.Writer, render func(io.Writer)) error {
	if e == nil || e.presenter == nil {
		return nil
	}
	return e.presenter.Flush(w, render)
}

// FlushHoldingLock is the engine facade for already-locked viewport paths.
// Callers must hold the shared terminal write lock before invoking it.
func (e *Engine) FlushHoldingLock(w io.Writer, render func(io.Writer)) error {
	if e == nil || e.presenter == nil {
		return nil
	}
	return e.presenter.FlushHoldingLock(w, render)
}

// FlushHandoffHoldingLock emits one native-scrollback handoff through the
// engine-owned Presenter. Callers must already hold the shared terminal lock.
func (e *Engine) FlushHandoffHoldingLock(w io.Writer, plan HandoffPlan) error {
	if e == nil || e.presenter == nil {
		return nil
	}
	return e.presenter.FlushHandoffHoldingLock(w, plan)
}

// Invalidate is the unified render-intent entry point: it coalesces a render
// callback under key (replacing any pending job for the same key) and arms it
// after delay. reason is diagnostic only and is reserved for the future frame
// statistics surface.
func (e *Engine) Invalidate(key, reason string, delay time.Duration, render func()) {
	if e == nil || e.pump == nil {
		return
	}
	e.pump.ScheduleDirty(key, DirtyForReason(reason), delay, render)
}

// InvalidateDirty is the explicit form of Invalidate for callers that already
// have a dirty classification and do not need a diagnostic reason string.
func (e *Engine) InvalidateDirty(key string, dirty DirtyFlags, delay time.Duration, render func()) {
	if e == nil || e.pump == nil {
		return
	}
	e.pump.ScheduleDirty(key, dirty, delay, render)
}

// Cancel disarms a pending render intent for key.
func (e *Engine) Cancel(key string) {
	if e == nil || e.pump == nil {
		return
	}
	e.pump.Cancel(key)
}

// Pending reports whether a render intent for key is armed.
func (e *Engine) Pending(key string) bool {
	if e == nil || e.pump == nil {
		return false
	}
	return e.pump.Pending(key)
}

// Dirty reports the union of pending dirty regions.
func (e *Engine) Dirty() DirtyFlags {
	if e == nil || e.pump == nil {
		return DirtyNone
	}
	return e.pump.Dirty()
}

// Stats returns scheduler diagnostics for debug displays and tests.
func (e *Engine) Stats() PumpStats {
	if e == nil || e.pump == nil {
		return PumpStats{}
	}
	return e.pump.Stats()
}

// SetMaxFPS forwards the frame budget to the engine's scheduler.
func (e *Engine) SetMaxFPS(maxFPS int) {
	if e == nil || e.pump == nil {
		return
	}
	e.pump.SetMaxFPS(maxFPS)
}

// MaxFPS reports the engine's current frame budget.
func (e *Engine) MaxFPS() int {
	if e == nil || e.pump == nil {
		return 0
	}
	return e.pump.MaxFPS()
}

// Shutdown disarms all pending intents and stops the pump executor.
// Idempotent.
func (e *Engine) Shutdown() {
	if e == nil || e.pump == nil {
		return
	}
	e.pump.Shutdown()
}
