// Package motion centralizes spinner/shimmer timing so components do not
// create their own tickers (Codex motion.rs style).
package motion

import (
	"os"
	"strings"
	"sync"
	"time"
)

// Mode controls animation intensity.
type Mode int

const (
	// ModeFull allows stable-width spinners and periodic activity frames.
	ModeFull Mode = iota
	// ModeReduced uses a static bullet / stage text; no shimmer.
	ModeReduced
	// ModeOff never advances frames and reports NeedsNextFrame=false.
	ModeOff
)

// Policy decides activity glyphs and whether a next frame is needed.
type Policy interface {
	Mode() Mode
	// ActivityFrame returns a fixed-width activity marker for the given time.
	ActivityFrame(now time.Time) string
	// NeedsNextFrame reports whether the UI should schedule another animation tick.
	NeedsNextFrame() bool
	// Interval is the suggested redraw cadence for activity (0 when Off).
	Interval() time.Duration
}

// Config selects the motion mode. Explicit Mode wins over auto detection.
type Config struct {
	// Mode when non-nil forces that mode.
	Forced *Mode
	// Interactive is true for TTY chat sessions.
	Interactive bool
	// NoColor disables chromatic cues; motion still allowed unless Off.
	NoColor bool
	// Frames overrides the Full-mode glyph sequence (each must be same display width).
	Frames []string
	// Interval overrides the default Full interval (clamped to >= 33ms ≈ 30 FPS).
	Interval time.Duration
}

var (
	globalMu     sync.RWMutex
	globalPolicy Policy = NewPolicy(Config{Interactive: true})
)

// SetGlobal installs the process-wide default policy (used by Progress/Spinner).
func SetGlobal(p Policy) {
	if p == nil {
		p = NewPolicy(Config{})
	}
	globalMu.Lock()
	globalPolicy = p
	globalMu.Unlock()
}

// Global returns the process-wide policy.
func Global() Policy {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalPolicy
}

// DetectMode chooses a default mode from environment and config.
func DetectMode(cfg Config) Mode {
	if cfg.Forced != nil {
		return *cfg.Forced
	}
	if !cfg.Interactive {
		return ModeOff
	}
	// CI / dumb terminals: no animation.
	if os.Getenv("CI") != "" || strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return ModeOff
	}
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("AICLI_MOTION"))); v != "" {
		switch v {
		case "full", "on", "1", "true":
			return ModeFull
		case "reduced", "minimal":
			return ModeReduced
		case "off", "0", "false", "none":
			return ModeOff
		}
	}
	// prefers-reduced-motion style env used by some hosts.
	if strings.EqualFold(os.Getenv("AICLI_REDUCED_MOTION"), "1") ||
		strings.EqualFold(os.Getenv("AICLI_REDUCED_MOTION"), "true") {
		return ModeReduced
	}
	return ModeFull
}

// NewPolicy builds a Policy from config.
func NewPolicy(cfg Config) Policy {
	mode := DetectMode(cfg)
	frames := cfg.Frames
	if len(frames) == 0 {
		// Fixed-width braille-ish ASCII fallbacks that stay 1 cell each.
		frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = 80 * time.Millisecond
	}
	// Cap at ~30 FPS.
	minInterval := time.Second / 30
	if interval < minInterval {
		interval = minInterval
	}
	return &policy{
		mode:     mode,
		frames:   append([]string(nil), frames...),
		interval: interval,
	}
}

type policy struct {
	mode     Mode
	frames   []string
	interval time.Duration
}

func (p *policy) Mode() Mode { return p.mode }

func (p *policy) Interval() time.Duration {
	if p == nil || p.mode == ModeOff {
		return 0
	}
	if p.mode == ModeReduced {
		return 0
	}
	return p.interval
}

func (p *policy) NeedsNextFrame() bool {
	return p != nil && p.mode == ModeFull && len(p.frames) > 1
}

func (p *policy) ActivityFrame(now time.Time) string {
	if p == nil {
		return "•"
	}
	switch p.mode {
	case ModeOff:
		return " "
	case ModeReduced:
		return "•"
	default:
		if len(p.frames) == 0 {
			return "•"
		}
		if !p.NeedsNextFrame() {
			return p.frames[0]
		}
		// Time-based index so independent callers stay in sync without shared state.
		idx := int(now.UnixNano()/int64(p.interval)) % len(p.frames)
		if idx < 0 {
			idx = 0
		}
		return p.frames[idx]
	}
}

// ForceMode is a helper for tests and CLI overrides.
func ForceMode(m Mode) *Mode {
	v := m
	return &v
}
