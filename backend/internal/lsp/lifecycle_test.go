package lsp

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// eventRecorder collects observer events across goroutines.
type eventRecorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *eventRecorder) observe(event Event) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *eventRecorder) snapshot() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

func (r *eventRecorder) has(kind EventKind, match func(Event) bool) bool {
	for _, event := range r.snapshot() {
		if event.Kind == kind && match(event) {
			return true
		}
	}
	return false
}

func TestObserverSeesLifecycleAndDiagnostics(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	fake.diagnosticsFor = func(string, int) []fakeDiagnostic {
		return []fakeDiagnostic{{
			Line: 0, Char: 0, EndChar: 7, Severity: int(SeverityError),
			Message: "boom", Code: "E1",
		}}
	}
	recorder := &eventRecorder{}
	bridge := NewBridgeWithOptions(testConfig(t, nil), dir, BridgeOptions{
		Dial:     fake.dial(),
		Observer: recorder.observe,
	})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	bridge.AppendToResult(ctx, "edit\n", []string{path})

	waitFor(t, "ready event", func() bool {
		return recorder.has(EventServerState, func(e Event) bool { return e.Status.State == StateReady })
	})
	if !recorder.has(EventServerState, func(e Event) bool { return e.Status.State == StateStarting }) {
		t.Fatalf("lifecycle events must include starting, got %+v", recorder.snapshot())
	}
	if !recorder.has(EventServerState, func(e Event) bool {
		return e.Status.State == StateReady && e.Status.PID == 4242 && e.Server == "fake"
	}) {
		t.Fatalf("ready event must carry the process facts, got %+v", recorder.snapshot())
	}
	if !recorder.has(EventDiagnostics, func(e Event) bool {
		return strings.EqualFold(e.Path, path) && e.Count == 1
	}) {
		t.Fatalf("diagnostics event must carry path and count, got %+v", recorder.snapshot())
	}
	if status := bridge.Statuses()[0]; status.LastActive.IsZero() {
		t.Fatalf("lsp_servers must expose last_active after a document operation: %+v", status)
	}
}

func TestCrashedServerIsReplacedWithinRestartBudget(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	fake.diagnosticsFor = func(string, int) []fakeDiagnostic {
		return []fakeDiagnostic{{
			Line: 0, Char: 0, EndChar: 7, Severity: int(SeverityError),
			Message: "boom", Code: "E1",
		}}
	}
	recorder := &eventRecorder{}
	bridge := NewBridgeWithOptions(testConfig(t, nil), dir, BridgeOptions{
		Dial:     fake.dial(),
		Observer: recorder.observe,
	})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	if out := bridge.AppendToResult(ctx, "edit\n", []string{path}); !strings.Contains(out, "boom") {
		t.Fatalf("first pass must deliver diagnostics:\n%s", out)
	}
	crashFakeServer(t, fake)
	waitFor(t, "crashed state", func() bool {
		statuses := bridge.Statuses()
		return len(statuses) == 1 && statuses[0].State == StateCrashed
	})
	if !recorder.has(EventServerState, func(e Event) bool { return e.Status.State == StateCrashed }) {
		t.Fatalf("crash must be observable as an event, got %+v", recorder.snapshot())
	}

	out := bridge.AppendToResult(ctx, "edit again\n", []string{path})
	if !strings.Contains(out, "boom") {
		t.Fatalf("a crashed server must be replaced automatically and still deliver diagnostics:\n%s", out)
	}
	if status := bridge.Statuses()[0]; status.State != StateReady || status.Restarts != 1 {
		t.Fatalf("status after recovery = %+v, want ready with restarts=1", status)
	}

	// Budget exhausted: a second crash degrades instead of looping.
	crashFakeServer(t, fake)
	waitFor(t, "second crash", func() bool {
		statuses := bridge.Statuses()
		return len(statuses) == 1 && statuses[0].State == StateCrashed
	})
	out = bridge.AppendToResult(ctx, "third edit\n", []string{path})
	if strings.Contains(out, "boom") {
		t.Fatalf("recovery budget must bound automatic restarts:\n%s", out)
	}
	if !strings.Contains(out, "<lsp_note") {
		t.Fatalf("exhausted recovery must degrade with the configured mode:\n%s", out)
	}
	if status := bridge.Statuses()[0]; status.Restarts != 1 || status.State != StateCrashed {
		t.Fatalf("status after exhausted budget = %+v, want crashed with restarts=1", status)
	}
}

func TestRestartLimitZeroDisablesAutomaticRecovery(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	fake.diagnosticsFor = func(string, int) []fakeDiagnostic {
		return []fakeDiagnostic{{Line: 0, Char: 0, EndChar: 7, Severity: int(SeverityError), Message: "boom"}}
	}
	zero := 0
	cfg := testConfig(t, func(c *Config) { c.RestartLimit = &zero })
	bridge := NewBridge(cfg, dir, nil, fake.dial())
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	if out := bridge.AppendToResult(ctx, "edit\n", []string{path}); !strings.Contains(out, "boom") {
		t.Fatalf("first pass must deliver diagnostics:\n%s", out)
	}
	crashFakeServer(t, fake)
	waitFor(t, "crashed state", func() bool {
		statuses := bridge.Statuses()
		return len(statuses) == 1 && statuses[0].State == StateCrashed
	})

	out := bridge.AppendToResult(ctx, "edit again\n", []string{path})
	if strings.Contains(out, "boom") {
		t.Fatalf("restartLimit=0 must disable automatic recovery:\n%s", out)
	}
	if status := bridge.Statuses()[0]; status.State != StateCrashed || status.Restarts != 0 {
		t.Fatalf("status = %+v, want crashed without restarts", status)
	}
	// Manual recovery stays available even with the automatic budget off.
	if err := bridge.Restart(ctx, "fake"); err != nil {
		t.Fatalf("manual restart must work with restartLimit=0: %v", err)
	}
	if out := bridge.AppendToResult(ctx, "edit after manual restart\n", []string{path}); !strings.Contains(out, "boom") {
		t.Fatalf("manual restart must restore diagnostics:\n%s", out)
	}
	if status := bridge.Statuses()[0]; status.State != StateReady || status.Restarts != 1 {
		t.Fatalf("status after manual restart = %+v, want ready with restarts=1", status)
	}
}

func TestConfigRestartLimitValue(t *testing.T) {
	if got := (Config{}).RestartLimitValue(); got != DefaultRestartLimit {
		t.Fatalf("unset restartLimit = %d, want %d", got, DefaultRestartLimit)
	}
	negative := -3
	if got := (Config{RestartLimit: &negative}).RestartLimitValue(); got != DefaultRestartLimit {
		t.Fatalf("negative restartLimit = %d, want %d", got, DefaultRestartLimit)
	}
	zero := 0
	if got := (Config{RestartLimit: &zero}).RestartLimitValue(); got != 0 {
		t.Fatalf("zero restartLimit = %d, want 0 (disabled)", got)
	}
	two := 2
	if got := (Config{RestartLimit: &two}).RestartLimitValue(); got != 2 {
		t.Fatalf("explicit restartLimit = %d, want 2", got)
	}
}

func crashFakeServer(t *testing.T, fake *fakeServer) {
	t.Helper()
	fake.mu.Lock()
	conn := fake.conn
	fake.mu.Unlock()
	if conn == nil {
		t.Fatalf("fake server has no connection to crash")
	}
	_ = conn.Close()
}
