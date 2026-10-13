package agent

import (
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// This file is the shared session-state projection used by every gateway host
// (aicli TUI, runtime-server). Hosts own only:
//
//   - content assembly (title / preview / last message), which is host-specific, and
//   - event mapping: which host event means "turn started", "approval pending",
//     "generating response", and so on.
//
// The store owns everything both hosts must agree on (docs/gateway/
// 02-architecture.md §3.2, 08-notifications.md §3):
//
//   - attention ↔ state coupling (approval → review, question → waiting, …)
//   - busy / terminal transitions (completed / interrupted / error)
//   - change detection so a host pushes a SESSION_UPDATE only on real changes
//     (an unchanged per-second status tick must not spam the gateway)
//   - StatusText + StartedAt: the console advances the elapsed clock locally,
//     so hosts never stream near-identical clock updates.
//
// Both hosts previously had private copies of this store; keeping it here is
// what makes "one core, two hosts" real instead of aspirational.

// Attention values (wire.SessionSummary.Attention). Attention is the "operator
// must act" axis; State is the "what is the session doing" axis.
const (
	AttentionCompleted = "completed"
	AttentionError     = "error"
	AttentionApproval  = "approval"
	AttentionQuestion  = "question"
	AttentionWaiting   = "waiting"
)

// SessionState is one session's gateway projection.
type SessionState struct {
	// Attention is what the operator must act on ("" = nothing).
	Attention string
	// Busy reports whether a turn is currently in flight.
	Busy bool
	// State is the canonical activity state (wire.SessionState*): what the
	// session is doing right now (running/thinking/answering/waiting/review)
	// or how the last turn ended (completed/interrupted/error).
	State string
	// StatusText mirrors the host's one-line activity text; it carries no
	// clock (the console advances elapsed locally from StartedAt).
	StatusText string
	// StartedAt anchors the elapsed timer of the current State/activity.
	StartedAt time.Time
	// Tail is the display-only tail of the host's in-progress content while a
	// turn is active (bounded to SessionTailMaxRunes, newest runes kept).
	Tail string
	// TailAt is when Tail was last updated.
	TailAt time.Time
}

// SessionTailMaxRunes bounds the live-output tail carried to the gateway.
// Truncation lives here (not in the hosts) so aicli and runtime-server cannot
// drift on the bound or on which end is kept.
const SessionTailMaxRunes = 512

// StatusTextWaitingSubagents is the one-line activity mirror for a parked
// managed turn (§6.12). Both hosts project it together with attention=waiting
// per docs/gateway/08-notifications.md §3 ("等待子任务"), so the console can tell
// "waiting for subagents" apart from a finished turn.
const StatusTextWaitingSubagents = "Waiting for subagents"

// TruncateSessionTail keeps the newest runes of s (the tail of the output).
func TruncateSessionTail(s string) string {
	if s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= SessionTailMaxRunes {
		return s
	}
	return string(runes[len(runes)-SessionTailMaxRunes:])
}

// SessionStateStore is a concurrency-safe sessionID→SessionState map. It is
// process-local and never persisted: the gateway registry is the durable
// projection (rebuilt from the next SESSION_UPDATE / REGISTER after a
// reconnect).
type SessionStateStore struct {
	mu     sync.Mutex
	values map[string]SessionState
}

// NewSessionStateStore creates an empty store.
func NewSessionStateStore() *SessionStateStore {
	return &SessionStateStore{values: make(map[string]SessionState)}
}

// Get returns the projection for sessionID (zero value when absent).
func (s *SessionStateStore) Get(sessionID string) SessionState {
	if s == nil {
		return SessionState{}
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionState{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[sessionID]
}

// BeginTurn records that a turn started: busy, running, attention cleared and
// the elapsed clock anchored at now.
func (s *SessionStateStore) BeginTurn(sessionID string, now time.Time) bool {
	sessionID = strings.TrimSpace(sessionID)
	if s == nil || sessionID == "" {
		return false
	}
	next := SessionState{
		Busy:      true,
		State:     wire.SessionStateRunning,
		StartedAt: now,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sessionStateEqual(s.values[sessionID], next) {
		return false
	}
	s.values[sessionID] = next
	return true
}

// EndTurn records the terminal state of a finished turn. outcome is one of
// wire.SessionStateCompleted (default), wire.SessionStateInterrupted, or
// wire.SessionStateError; attention is derived so hosts cannot disagree on
// whether a cancel deserves a badge (it does not) or a failure does (it does).
func (s *SessionStateStore) EndTurn(sessionID, outcome string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if s == nil || sessionID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.values[sessionID]
	next := st
	next.Busy = false
	next.StatusText = ""
	next.StartedAt = time.Time{}
	switch outcome {
	case wire.SessionStateInterrupted:
		next.State = wire.SessionStateInterrupted
		next.Attention = ""
	case wire.SessionStateError:
		next.State = wire.SessionStateError
		next.Attention = AttentionError
	default:
		next.State = wire.SessionStateCompleted
		next.Attention = AttentionCompleted
	}
	next.Tail = ""
	next.TailAt = time.Time{}
	if sessionStateEqual(st, next) {
		return false
	}
	s.values[sessionID] = next
	return true
}

// SetTail records the live-output tail for an active session. The value is
// truncated with the shared bound (newest runes kept); clearing the tail on
// turn end is handled by EndTurn.
func (s *SessionStateStore) SetTail(sessionID, tail string, now time.Time) bool {
	sessionID = strings.TrimSpace(sessionID)
	if s == nil || sessionID == "" {
		return false
	}
	tail = TruncateSessionTail(tail)
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.values[sessionID]
	if st.Tail == tail && !st.TailAt.IsZero() {
		return false
	}
	st.Tail = tail
	st.TailAt = now
	s.values[sessionID] = st
	return true
}

// SetAttention updates the attention axis and couples the activity state:
// approval means review, question means waiting, clearing either returns a
// busy session to running. Terminal states (completed/interrupted/error) are
// owned by EndTurn and are left untouched by unrelated attention changes.
func (s *SessionStateStore) SetAttention(sessionID, attention string) bool {
	sessionID = strings.TrimSpace(sessionID)
	attention = strings.TrimSpace(attention)
	if s == nil || sessionID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.values[sessionID]
	if st.Attention == attention {
		return false
	}
	st.Attention = attention
	switch attention {
	case AttentionApproval:
		st.State = wire.SessionStateReview
	case AttentionQuestion:
		st.State = wire.SessionStateWaiting
	default:
		if st.Busy && (st.State == wire.SessionStateReview || st.State == wire.SessionStateWaiting) {
			st.State = wire.SessionStateRunning
		}
	}
	s.values[sessionID] = st
	return true
}

// SetSurface merges the host's live activity projection (state + one-line
// text). It reports whether a visible field changed: the caller pushes a
// SESSION_UPDATE only on real transitions, so a per-second status tick stays
// free. startedAt anchors the console's local elapsed clock; a zero value
// falls back to now.
func (s *SessionStateStore) SetSurface(sessionID, state, text string, startedAt, now time.Time) bool {
	sessionID = strings.TrimSpace(sessionID)
	state = strings.TrimSpace(state)
	text = strings.TrimSpace(text)
	if s == nil || sessionID == "" || (state == "" && text == "") {
		return false
	}
	if startedAt.IsZero() {
		startedAt = now
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.values[sessionID]
	if st.State == state && st.StatusText == text {
		return false
	}
	st.State = state
	st.StatusText = text
	st.StartedAt = startedAt
	s.values[sessionID] = st
	return true
}

// Clear drops a session from the projection (session ended / removed).
func (s *SessionStateStore) Clear(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if s == nil || sessionID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.values[sessionID]; !ok {
		return false
	}
	delete(s.values, sessionID)
	return true
}

// Apply merges the projection into a content summary built by the host. The
// host-provided State (when any) is overridden by the tracked state; a zero
// projection still yields the canonical idle/running default so both hosts
// report the same baseline.
func (st SessionState) Apply(summary *wire.SessionSummary) {
	if summary == nil {
		return
	}
	if state := strings.TrimSpace(st.State); state != "" {
		summary.State = state
	} else if strings.TrimSpace(summary.State) == "" {
		if st.Busy {
			summary.State = wire.SessionStateRunning
		} else {
			summary.State = wire.SessionStateIdle
		}
	}
	summary.Attention = st.Attention
	summary.Busy = st.Busy
	summary.StatusText = st.StatusText
	summary.StatusTail = st.Tail
	if !st.StartedAt.IsZero() {
		summary.StatusStartedAt = st.StartedAt
	}
}

// SessionStateActive reports whether state is an in-flight phase (as opposed
// to idle or a terminal outcome). Hosts and the console use the same rule for
// "needs a spinner / running filter".
func SessionStateActive(state string) bool {
	switch strings.TrimSpace(state) {
	case wire.SessionStateRunning, wire.SessionStateThinking,
		wire.SessionStateAnswering, wire.SessionStateWaiting, wire.SessionStateReview:
		return true
	}
	return false
}

func sessionStateEqual(a, b SessionState) bool {
	return a.Attention == b.Attention &&
		a.Busy == b.Busy &&
		a.State == b.State &&
		a.StatusText == b.StatusText &&
		a.StartedAt.Equal(b.StartedAt) &&
		a.Tail == b.Tail &&
		a.TailAt.Equal(b.TailAt)
}
