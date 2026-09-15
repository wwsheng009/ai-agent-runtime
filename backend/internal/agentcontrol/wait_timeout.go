package agentcontrol

import (
	"fmt"
	"strings"
)

// Wait observation defaults shared by every host (API runtime and CLI local
// runtime). They mirror the reference implementation: default 30s, minimum
// 10s, maximum 1h.
const (
	DefaultWaitTimeoutMs = 30000
	MinWaitTimeoutMs     = 10000
	MaxWaitTimeoutMs     = 3600000
)

// WaitTimeoutMode selects how out-of-range wait requests are handled.
const (
	// WaitTimeoutModeClamp pins an out-of-range request to the nearest bound.
	WaitTimeoutModeClamp = "clamp"
	// WaitTimeoutModeError rejects an out-of-range request with an actionable
	// message instead of silently shortening or extending the observation.
	WaitTimeoutModeError = "error"
)

// WaitTimeoutPolicy bounds every model-issued observation window: wait_agent,
// read_agent_events, and wait_team. The consuming host owns where it resolves
// the window (the session hosts resolve agent waits; the broker resolves team
// waits), but the bounds and the out-of-range behavior come from one place.
type WaitTimeoutPolicy struct {
	DefaultMs int
	MinMs     int
	MaxMs     int
	Mode      string
}

// WaitTimeoutResolution is the normalized wait window plus the request echo
// needed by the tool result so parents can see what actually happened.
type WaitTimeoutResolution struct {
	RequestedMs int
	EffectiveMs int
	Clamped     bool
}

// ResolveWaitTimeout normalizes a requested wait window into
// [MinMs, MaxMs]. A zero/negative request means "use the host default", which
// is itself clamped defensively but reported as not clamped unless the
// configured default was out of range.
func ResolveWaitTimeout(requestedMs int, policy WaitTimeoutPolicy) (WaitTimeoutResolution, error) {
	policy = policy.Normalize()
	resolution := WaitTimeoutResolution{RequestedMs: requestedMs}
	if requestedMs <= 0 {
		effective := clampWaitTimeout(policy.DefaultMs, policy.MinMs, policy.MaxMs)
		resolution.EffectiveMs = effective
		resolution.Clamped = effective != policy.DefaultMs
		return resolution, nil
	}
	if requestedMs < policy.MinMs {
		if policy.Mode == WaitTimeoutModeError {
			return resolution, fmt.Errorf(
				"timeout_ms=%d is below agents.minWaitTimeoutMs=%d; pass timeout_ms>=%d, pass 0 to use the default (%dms), or set agents.waitTimeoutMode=clamp",
				requestedMs, policy.MinMs, policy.MinMs, policy.DefaultMs,
			)
		}
		resolution.EffectiveMs = policy.MinMs
		resolution.Clamped = true
		return resolution, nil
	}
	if requestedMs > policy.MaxMs {
		if policy.Mode == WaitTimeoutModeError {
			return resolution, fmt.Errorf(
				"timeout_ms=%d exceeds agents.maxWaitTimeoutMs=%d; pass timeout_ms<=%d, or set agents.waitTimeoutMode=clamp to clamp automatically",
				requestedMs, policy.MaxMs, policy.MaxMs,
			)
		}
		resolution.EffectiveMs = policy.MaxMs
		resolution.Clamped = true
		return resolution, nil
	}
	resolution.EffectiveMs = requestedMs
	return resolution, nil
}

// Normalize fills zero-value bounds with the shared defaults and normalizes the
// mode string so callers can pass a partially-configured policy.
func (p WaitTimeoutPolicy) Normalize() WaitTimeoutPolicy {
	if p.MinMs <= 0 {
		p.MinMs = MinWaitTimeoutMs
	}
	if p.MaxMs <= 0 {
		p.MaxMs = MaxWaitTimeoutMs
	}
	if p.MinMs > p.MaxMs {
		p.MinMs = p.MaxMs
	}
	if p.DefaultMs <= 0 {
		p.DefaultMs = DefaultWaitTimeoutMs
	}
	if strings.EqualFold(strings.TrimSpace(p.Mode), WaitTimeoutModeError) {
		p.Mode = WaitTimeoutModeError
	} else {
		p.Mode = WaitTimeoutModeClamp
	}
	return p
}

func clampWaitTimeout(value, minMs, maxMs int) int {
	if value < minMs {
		return minMs
	}
	if value > maxMs {
		return maxMs
	}
	return value
}
