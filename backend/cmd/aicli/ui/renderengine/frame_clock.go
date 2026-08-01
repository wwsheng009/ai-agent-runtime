package renderengine

import (
	"sync"
	"time"
)

// FrameGate is the pull-driven half of frame scheduling. It coalesces state
// changes and applies an FPS budget, while its owner decides when to paint.
//
// FramePump owns timed callbacks for cross-component invalidations. Active
// streams are polled by the coordinator, so they use this smaller contract
// without creating a second timer or render goroutine. The interface also
// keeps legacy test schedulers injectable during the migration.
type FrameGate interface {
	Request(reason string)
	Pending() bool
	NextDelay(now time.Time) (time.Duration, bool)
	Consume(now time.Time) bool
	ForceConsume(now time.Time) bool
}

// FrameClock is the RenderEngine-owned implementation of a pull-driven
// FrameGate. It deliberately does not invoke callbacks or write a terminal.
type FrameClock struct {
	mu       sync.Mutex
	maxFPS   int
	minGap   time.Duration
	pending  bool
	lastEmit time.Time
}

// NewFrameClock creates a FrameGate with maxFPS frames per second. A
// non-positive value uses the active-stream compatibility default of 30 FPS.
func NewFrameClock(maxFPS int) *FrameClock {
	if maxFPS <= 0 {
		maxFPS = 30
	}
	return &FrameClock{
		maxFPS: maxFPS,
		minGap: time.Second / time.Duration(maxFPS),
	}
}

// Request records a redraw intent. Reasons are consumed by FramePump's
// diagnostics; FrameClock only needs to know that a new frame is pending.
func (c *FrameClock) Request(_ string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.pending = true
	c.mu.Unlock()
}

// Pending reports whether a redraw has been requested but not consumed.
func (c *FrameClock) Pending() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pending
}

// NextDelay reports the remaining delay before a pending frame may be
// consumed. The bool is false when the clock is idle.
func (c *FrameClock) NextDelay(now time.Time) (time.Duration, bool) {
	if c == nil {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.pending {
		return 0, false
	}
	if c.lastEmit.IsZero() {
		return 0, true
	}
	remaining := c.minGap - now.Sub(c.lastEmit)
	if remaining < 0 {
		remaining = 0
	}
	return remaining, true
}

// Consume consumes one pending frame when its FPS budget permits it.
func (c *FrameClock) Consume(now time.Time) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.pending || (!c.lastEmit.IsZero() && now.Sub(c.lastEmit) < c.minGap) {
		return false
	}
	c.pending = false
	c.lastEmit = now
	return true
}

// ForceConsume consumes one pending frame without applying the FPS budget.
func (c *FrameClock) ForceConsume(now time.Time) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.pending {
		return false
	}
	c.pending = false
	c.lastEmit = now
	return true
}

// MaxFPS returns the clock's current frame budget.
func (c *FrameClock) MaxFPS() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxFPS
}
