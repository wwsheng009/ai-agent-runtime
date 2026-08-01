package renderengine

import "time"

// Engine is the renderengine facade (Stage A skeleton).
//
// Later stages grow it into the single render authority: ScreenModel compose
// (Stage B), row ownership arbitration (Stage C), and the shared RenderCache
// (Stage D) will all hang off this type. For now it only exposes the unified
// scheduling entry point so every render intent converges on the FramePump
// instead of scattered timers.
type Engine struct {
	pump *FramePump
}

// NewEngine creates an Engine with a live FramePump.
func NewEngine() *Engine {
	return &Engine{pump: NewFramePump()}
}

// Pump returns the underlying FramePump. Exposed for coordinator wiring and
// tests; production callers should prefer Invalidate.
func (e *Engine) Pump() *FramePump {
	return e.pump
}

// Invalidate is the unified render-intent entry point: it coalesces a render
// callback under key (replacing any pending job for the same key) and arms it
// after delay. reason is diagnostic only and is reserved for the future frame
// statistics surface.
func (e *Engine) Invalidate(key, reason string, delay time.Duration, render func()) {
	e.pump.Schedule(key, delay, render)
}

// Cancel disarms a pending render intent for key.
func (e *Engine) Cancel(key string) {
	e.pump.Cancel(key)
}

// Pending reports whether a render intent for key is armed.
func (e *Engine) Pending(key string) bool {
	return e.pump.Pending(key)
}

// Shutdown disarms all pending intents and stops the pump executor.
// Idempotent.
func (e *Engine) Shutdown() {
	e.pump.Shutdown()
}
