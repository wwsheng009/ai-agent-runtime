package ui

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// historyTraceEnv names the env-gated history-effect trace sink. The value is a
// file path, not a boolean toggle: the trace must never be written to stderr,
// because stderr shares the alternate-screen terminal this trace is meant to
// observe, and a stray line desyncs the live frame it is supposed to diagnose.
// This matches the existing AIR_DUMP_PHYSICAL=<path> convention.
const historyTraceEnv = "AIR_TRACE_HISTORY"

// historyTraceSink appends diagnostic lines to the configured file.
//
// It is deliberately inert while the hook is unset — one getenv and one
// uncontended lock per decision, no allocation, no open — and it never affects
// terminal transactions: a failed open or write only disables the trace and is
// reported once on stderr, so an operator cannot mistake a silent no-op for an
// enabled hook.
type historyTraceSink struct {
	mu     sync.Mutex
	path   string
	file   *os.File
	warned bool
}

var historyTrace historyTraceSink

// traceHistoryReduction is the env-gated diagnostic for history-effect
// reduction: with AIR_TRACE_HISTORY=<path> it appends one line per
// ack/ack-batch/fail decision so a live session can be diffed against the ledger
// invariants after the fact. The line is only formatted when the sink is
// enabled, so the disabled path allocates nothing.
func traceHistoryReduction(state UIControllerState, format string, args ...any) {
	if !historyTrace.active() {
		return
	}
	historyTrace.append(fmt.Sprintf("[hist] gen=%d next=%d unknown=%t recon=%t pending=%d | %s",
		state.LayoutGeneration, state.HistoryEffects.NextToken,
		state.HistoryEffects.ProjectionUnknown, state.HistoryEffects.ReconciliationRequired,
		historyTracePendingCount(state.HistoryEffects),
		fmt.Sprintf(format, args...)))
}

// historyTracePendingCount is nil-safe: a diagnostic must not panic on the
// zero-value controller state (early startup, tests) where the ledger has not
// been created yet.
func historyTracePendingCount(effects HistoryEffectQueueState) int {
	if effects.ledger == nil {
		return 0
	}
	return effects.ledger.pendingCount
}

// active reports whether the hook currently has a usable path, emitting the
// one-time hint for a misconfigured value.
func (s *historyTraceSink) active() bool {
	value := strings.TrimSpace(os.Getenv(historyTraceEnv))
	s.mu.Lock()
	defer s.mu.Unlock()
	if value == "" {
		s.closeLocked()
		return false
	}
	if historyTraceToggleValue(value) {
		s.closeLocked()
		s.warnOnceLocked("expects a file path, got %q: tracing disabled", value)
		return false
	}
	return true
}

// append re-resolves the path on every decision so a live session can be
// redirected or stopped without restarting the process. Reopening is
// path-scoped: switching to a new file never appends into the previous one.
func (s *historyTraceSink) append(line string) {
	value := strings.TrimSpace(os.Getenv(historyTraceEnv))
	s.mu.Lock()
	defer s.mu.Unlock()
	if value == "" || historyTraceToggleValue(value) {
		s.closeLocked()
		return
	}
	if s.file == nil || s.path != value {
		s.closeLocked()
		file, err := os.OpenFile(value, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			s.warnOnceLocked("cannot open %s: %v", value, err)
			return
		}
		s.file, s.path = file, value
	}
	if _, err := s.file.WriteString(line + "\n"); err != nil {
		s.closeLocked()
		s.warnOnceLocked("write to %s failed: %v", value, err)
	}
}

// close releases the sink. Production relies on process exit; tests use it to
// release the handle before their temporary directory is removed.
func (s *historyTraceSink) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
}

func (s *historyTraceSink) closeLocked() {
	if s.file != nil {
		_ = s.file.Close()
	}
	s.file, s.path = nil, ""
}

func (s *historyTraceSink) warnOnceLocked(format string, args ...any) {
	if s.warned {
		return
	}
	s.warned = true
	fmt.Fprintf(os.Stderr, "[hist] AIR_TRACE_HISTORY %s\n", fmt.Sprintf(format, args...))
}

// historyTraceToggleValue reports whether the raw hook value is a boolean
// toggle rather than a path. The hook used to be a boolean, so treating "1" as
// a path would silently create a file named "1" in the working directory;
// rejecting the old spelling with a hint is the only behavior that cannot be
// mistaken for a working trace.
func historyTraceToggleValue(value string) bool {
	switch strings.ToLower(value) {
	case "0", "1", "true", "false", "on", "off", "yes", "no":
		return true
	default:
		return false
	}
}
