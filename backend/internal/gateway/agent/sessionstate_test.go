package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

func TestSessionStateStoreTurnLifecycle(t *testing.T) {
	store := NewSessionStateStore()
	now := time.Now()

	if changed := store.BeginTurn("s1", now); !changed {
		t.Fatal("BeginTurn must report a change on a fresh session")
	}
	st := store.Get("s1")
	if !st.Busy || st.State != wire.SessionStateRunning || st.Attention != "" || !st.StartedAt.Equal(now) {
		t.Fatalf("after begin: %+v", st)
	}
	if changed := store.BeginTurn("s1", now); changed {
		t.Fatal("BeginTurn must be idempotent for the same anchor")
	}

	// attention couples the activity state: approval → review.
	if changed := store.SetAttention("s1", AttentionApproval); !changed {
		t.Fatal("SetAttention must report a change")
	}
	if st := store.Get("s1"); st.Attention != AttentionApproval || st.State != wire.SessionStateReview {
		t.Fatalf("approval: %+v", st)
	}
	// resolving the approval returns a busy session to running.
	store.SetAttention("s1", "")
	if st := store.Get("s1"); st.Attention != "" || st.State != wire.SessionStateRunning {
		t.Fatalf("approval resolved: %+v", st)
	}
	// question → waiting.
	store.SetAttention("s1", AttentionQuestion)
	if st := store.Get("s1"); st.State != wire.SessionStateWaiting {
		t.Fatalf("question: %+v", st)
	}
	store.SetAttention("s1", "")

	// successful end: completed + completed attention, busy cleared.
	if changed := store.EndTurn("s1", wire.SessionStateCompleted); !changed {
		t.Fatal("EndTurn must report a change")
	}
	st = store.Get("s1")
	if st.Busy || st.State != wire.SessionStateCompleted || st.Attention != AttentionCompleted {
		t.Fatalf("completed: %+v", st)
	}
	if changed := store.EndTurn("s1", wire.SessionStateCompleted); changed {
		t.Fatal("EndTurn must be idempotent")
	}
	// An unrelated attention change must not downgrade a terminal state.
	store.SetAttention("s1", "")
	if st := store.Get("s1"); st.State != wire.SessionStateCompleted {
		t.Fatalf("terminal state downgraded: %+v", st)
	}
}

func TestSessionStateStoreOutcomes(t *testing.T) {
	store := NewSessionStateStore()
	now := time.Now()

	store.BeginTurn("cancel", now)
	store.EndTurn("cancel", wire.SessionStateInterrupted)
	if st := store.Get("cancel"); st.State != wire.SessionStateInterrupted || st.Attention != "" || st.Busy {
		t.Fatalf("interrupted: %+v", st)
	}

	store.BeginTurn("fail", now)
	store.EndTurn("fail", wire.SessionStateError)
	if st := store.Get("fail"); st.State != wire.SessionStateError || st.Attention != AttentionError || st.Busy {
		t.Fatalf("error: %+v", st)
	}
}

func TestSessionStateStoreSurfaceAndApply(t *testing.T) {
	store := NewSessionStateStore()
	now := time.Now()
	started := now.Add(-90 * time.Second)

	if changed := store.SetSurface("s2", wire.SessionStateRunning, "Running shell", started, now); !changed {
		t.Fatal("first surface projection must change")
	}
	if changed := store.SetSurface("s2", wire.SessionStateRunning, "Running shell", started, now); changed {
		t.Fatal("unchanged surface projection must not push (per-second tick)")
	}
	if changed := store.SetSurface("s2", wire.SessionStateAnswering, "Generating response", started.Add(5*time.Second), now); !changed {
		t.Fatal("phase transition must change")
	}

	summary := wire.SessionSummary{SessionID: "s2", Title: "demo"}
	store.Get("s2").Apply(&summary)
	if summary.State != wire.SessionStateAnswering || summary.StatusText != "Generating response" || summary.Busy {
		t.Fatalf("apply: %+v", summary)
	}
	if !summary.StatusStartedAt.Equal(started.Add(5 * time.Second)) {
		t.Fatalf("started at = %v", summary.StatusStartedAt)
	}

	// A zero projection still yields the canonical idle baseline.
	var zero SessionState
	baseline := wire.SessionSummary{SessionID: "s3"}
	zero.Apply(&baseline)
	if baseline.State != wire.SessionStateIdle || baseline.Busy || baseline.Attention != "" {
		t.Fatalf("baseline: %+v", baseline)
	}
}

func TestSessionStateStoreClear(t *testing.T) {
	store := NewSessionStateStore()
	store.BeginTurn("gone", time.Now())
	if changed := store.Clear("gone"); !changed {
		t.Fatal("Clear must report a change")
	}
	if changed := store.Clear("gone"); changed {
		t.Fatal("Clear must be idempotent")
	}
	if st := store.Get("gone"); st.Busy || st.State != "" {
		t.Fatalf("cleared state = %+v", st)
	}
}

func TestSessionStateActive(t *testing.T) {
	for _, state := range []string{
		wire.SessionStateRunning, wire.SessionStateThinking, wire.SessionStateAnswering,
		wire.SessionStateWaiting, wire.SessionStateReview,
	} {
		if !SessionStateActive(state) {
			t.Fatalf("%s should be active", state)
		}
	}
	for _, state := range []string{
		wire.SessionStateIdle, wire.SessionStateCompleted,
		wire.SessionStateInterrupted, wire.SessionStateError, "",
	} {
		if SessionStateActive(state) {
			t.Fatalf("%s should not be active", state)
		}
	}
}

func TestSessionTailTruncationAndLifecycle(t *testing.T) {
	// Truncation keeps the newest runes (the tail of the output).
	long := strings.Repeat("前", 400) + strings.Repeat("后", 400)
	got := TruncateSessionTail(long)
	if len([]rune(got)) != SessionTailMaxRunes {
		t.Fatalf("truncated length = %d", len([]rune(got)))
	}
	if !strings.HasPrefix(got, strings.Repeat("前", 112)) || !strings.HasSuffix(got, strings.Repeat("后", 400)) {
		t.Fatal("truncation must keep the newest runes")
	}
	if TruncateSessionTail("short") != "short" {
		t.Fatal("short tails must pass through unchanged")
	}

	store := NewSessionStateStore()
	now := time.Now()
	store.BeginTurn("s1", now)
	if changed := store.SetTail("s1", "hello", now); !changed {
		t.Fatal("first tail must change")
	}
	if changed := store.SetTail("s1", "hello", now.Add(2*time.Second)); changed {
		t.Fatal("identical tail must not change")
	}
	if changed := store.SetTail("s1", "hello world", now.Add(3*time.Second)); !changed {
		t.Fatal("new tail must change")
	}
	summary := wire.SessionSummary{SessionID: "s1"}
	store.Get("s1").Apply(&summary)
	if summary.StatusTail != "hello world" {
		t.Fatalf("apply tail = %q", summary.StatusTail)
	}
	// A new turn clears the tail; turn end clears it too.
	store.BeginTurn("s1", now.Add(4*time.Second))
	if store.Get("s1").Tail != "" {
		t.Fatal("BeginTurn must clear the tail")
	}
	store.SetTail("s1", "again", now.Add(5*time.Second))
	store.EndTurn("s1", wire.SessionStateCompleted)
	if st := store.Get("s1"); st.Tail != "" || !st.TailAt.IsZero() {
		t.Fatalf("EndTurn must clear tail: %+v", st)
	}
}

func TestTailThrottle(t *testing.T) {
	throttle := NewTailThrottle(time.Second)
	now := time.Now()
	if !throttle.Allow("s1", "a", now) {
		t.Fatal("first sample must pass")
	}
	if throttle.Allow("s1", "a", now.Add(2*time.Second)) {
		t.Fatal("unchanged tail must never pass")
	}
	if throttle.Allow("s1", "ab", now.Add(200*time.Millisecond)) {
		t.Fatal("interval must gate changed tails")
	}
	if !throttle.Allow("s1", "abc", now.Add(1500*time.Millisecond)) {
		t.Fatal("changed tail after the interval must pass")
	}
	if !throttle.Allow("s2", "x", now) {
		t.Fatal("sessions are independent")
	}
	throttle.Forget("s1")
	if !throttle.Allow("s1", "abc", now.Add(1600*time.Millisecond)) {
		t.Fatal("forget resets the throttle state")
	}
}

func TestControllerWatchGating(t *testing.T) {
	cfg := Config{URL: "ws://example.invalid", Token: "tok", Enabled: true}
	controller := New(cfg, nil)

	// Before the gateway advertises gating, tails always pass (P1-a behavior).
	if !controller.TailWanted("s1") {
		t.Fatal("pre-gating TailWanted must default to true")
	}

	// The gateway advertises participation with no watched sessions yet.
	controller.ApplyPending([]string{"tail-gating:on", "unknown:action"})
	if controller.TailWanted("s1") {
		t.Fatal("after gating, unwatched sessions must be gated")
	}

	controller.ApplyPending([]string{"watch:s1"})
	if !controller.TailWanted("s1") || controller.TailWanted("s2") {
		t.Fatal("watch must enable exactly the named session")
	}

	controller.ApplyPending([]string{"unwatch:s1"})
	if controller.TailWanted("s1") {
		t.Fatal("unwatch must disable the session")
	}
}
