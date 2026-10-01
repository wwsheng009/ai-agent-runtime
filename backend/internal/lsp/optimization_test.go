package lsp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// applyDidChange applies one incremental didChange to old text using the same
// encoding boundary helpers the client uses (test-side property check).
func applyDidChange(t *testing.T, old string, change textDocumentContentChangeEvent, enc PositionEncoding) string {
	t.Helper()
	if change.Range == nil {
		return change.Text
	}
	content := []byte(old)
	start := CanonicalToOffset(content, ProtocolToCanonical(content, enc, change.Range.Start))
	end := CanonicalToOffset(content, ProtocolToCanonical(content, enc, change.Range.End))
	if start > end {
		t.Fatalf("invalid change range: %d > %d", start, end)
	}
	return string(content[:start]) + change.Text + string(content[end:])
}

// TestEmptyPublishEarlyAcceptWithConfirmWindow pins the clean fast path: a
// version-stamped empty publish from an opted-in server concludes the wait
// after the debounce instead of burning the full budget.
func TestEmptyPublishEarlyAcceptWithConfirmWindow(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t) // nil diagnosticsFor → stamped empty publish
	cfg := testConfig(t, func(c *Config) {
		c.Servers[0].EmptyPublishConclusive = boolPtr(true)
		c.Diagnostics.WaitMS = 900
		c.Diagnostics.EmptyConfirmMS = 120
	})
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{Dial: fake.dial()})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	start := time.Now()
	out := bridge.AppendToResult(ctx, "edit\n", []string{path})
	elapsed := time.Since(start)
	if !strings.Contains(out, `count="0"`) {
		t.Fatalf("expected a clean block, got:\n%s", out)
	}
	if elapsed >= 600*time.Millisecond {
		t.Fatalf("empty early-accept must not burn the wait budget, took %s", elapsed)
	}
}

// TestEmptyPublishConservativeWithoutServerOptIn pins the default: servers
// that did not declare conclusive empty publishes keep waiting (settle at the
// deadline) so interim empties never mask real diagnostics.
func TestEmptyPublishConservativeWithoutServerOptIn(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	cfg := testConfig(t, func(c *Config) {
		c.Diagnostics.WaitMS = 700
	})
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{Dial: fake.dial()})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	start := time.Now()
	out := bridge.AppendToResult(ctx, "edit\n", []string{path})
	elapsed := time.Since(start)
	if !strings.Contains(out, `count="0"`) {
		t.Fatalf("expected a clean block, got:\n%s", out)
	}
	if elapsed < 600*time.Millisecond {
		t.Fatalf("non-opted-in server must wait for the deadline, took %s", elapsed)
	}
}

// TestRestartWindowResetsBudget pins the sliding recovery window: after the
// budget is exhausted inside the window a member stays degraded, but a crash
// after a quiet period longer than restartWindow gets a fresh budget.
func TestRestartWindowResetsBudget(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	fake.diagnosticsFor = func(string, int) []fakeDiagnostic {
		return []fakeDiagnostic{{Line: 0, Char: 0, EndChar: 7, Severity: int(SeverityError), Message: "boom"}}
	}
	now := time.Now()
	limit := 1
	cfg := testConfig(t, func(c *Config) {
		c.RestartLimit = &limit
		c.RestartWindow = time.Minute
		c.Diagnostics.WaitMS = 800
	})
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{
		Dial: fake.dial(),
		Now:  func() time.Time { return now },
	})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	if out := bridge.AppendToResult(ctx, "e1\n", []string{path}); !strings.Contains(out, "boom") {
		t.Fatalf("first edit must deliver diagnostics, got:\n%s", out)
	}
	crashFakeServer(t, fake)
	waitFor(t, "first crash", func() bool { return bridge.Statuses()[0].State == StateCrashed })

	// First replacement consumes the budget (restartLimit=1).
	if out := bridge.AppendToResult(ctx, "e2\n", []string{path}); !strings.Contains(out, "boom") {
		t.Fatalf("first crash replacement must still deliver diagnostics, got:\n%s", out)
	}
	crashFakeServer(t, fake)
	waitFor(t, "second crash", func() bool { return bridge.Statuses()[0].State == StateCrashed })

	// Second crash inside the window: budget exhausted → degrade.
	if out := bridge.AppendToResult(ctx, "e3\n", []string{path}); !strings.Contains(out, "<lsp_note") {
		t.Fatalf("exhausted budget must degrade inside the window, got:\n%s", out)
	}

	// Quiet period longer than the window resets the budget.
	now = now.Add(2 * time.Minute)
	if out := bridge.AppendToResult(ctx, "e4\n", []string{path}); !strings.Contains(out, "boom") {
		t.Fatalf("restart window must grant a fresh budget, got:\n%s", out)
	}
}

// TestDocumentLRUEvictionSendsDidClose pins the open-document bound: opening
// more documents than the cap evicts the least recently used one and notifies
// the server with didClose.
func TestDocumentLRUEvictionSendsDidClose(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeServer(t)
	cfg := testConfig(t, func(c *Config) {
		c.MaxTrackedDocs = 2
		c.Diagnostics.WaitMS = 150
	})
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{Dial: fake.dial()})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	paths := []string{
		writeTestFile(t, dir, "a.go", "package main\n"),
		writeTestFile(t, dir, "b.go", "package main\n"),
		writeTestFile(t, dir, "c.go", "package main\n"),
	}
	for _, path := range paths {
		bridge.AppendToResult(ctx, "edit\n", []string{path})
	}
	waitFor(t, "didClose", func() bool { return fake.count("didClose") >= 1 })

	client := bridge.registry.Servers()[0].Client()
	if client == nil {
		t.Fatal("expected a started client")
	}
	if tracked := client.TrackedCount(); tracked > 2 {
		t.Fatalf("tracked documents = %d, want <= 2", tracked)
	}
}

// TestIncrementalDidChangeSendsMinimalRange pins the incremental sync path:
// an already-open document is updated with a single rune-aligned range whose
// replacement reconstructs the new text under the negotiated encoding.
func TestIncrementalDidChangeSendsMinimalRange(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\nvar s = \"hé\"\n")
	fake := newFakeServer(t)
	fake.syncKind = 2 // incremental
	fake.encoding = "utf-16"
	cfg := testConfig(t, func(c *Config) { c.Diagnostics.WaitMS = 150 })
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{Dial: fake.dial()})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	bridge.AppendToResult(ctx, "edit\n", []string{path})

	oldText := "package main\nvar s = \"hé\"\n"
	newText := "package main\nvar s = \"hé!\"\n"
	if err := os.WriteFile(path, []byte(newText), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	bridge.AppendToResult(ctx, "edit\n", []string{path})

	waitFor(t, "didChange", func() bool { return fake.count("didChange") == 1 })
	change, ok := fake.lastChange(PathToURI(path))
	if !ok || len(change.ContentChanges) != 1 {
		t.Fatalf("didChange params = %+v, want exactly one change", change)
	}
	edit := change.ContentChanges[0]
	if edit.Range == nil {
		t.Fatalf("incremental sync must send a range, got full text %q", edit.Text)
	}
	if edit.Text != "!" {
		t.Fatalf("incremental replacement = %q, want the minimal %q", edit.Text, "!")
	}
	if applied := applyDidChange(t, oldText, edit, EncodingUTF16); applied != newText {
		t.Fatalf("applying the range change yields %q, want %q", applied, newText)
	}
}

// TestHintOnceDedupesInlineAppend pins the noise reduction: the same
// degradation reason is explained once per session, later edits stay clean.
func TestHintOnceDedupesInlineAppend(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	cfg := testConfig(t, nil)
	bridge := NewBridge(cfg, dir, nil, dialError(errors.New(`exec: "fake-lsp": executable file not found`)))
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	const original = "edit succeeded\n"
	first := bridge.AppendToResult(ctx, original, []string{path})
	if !strings.Contains(first, "<lsp_note") {
		t.Fatalf("first degradation must explain itself, got:\n%s", first)
	}
	second := bridge.AppendToResult(ctx, original, []string{path})
	if second != original {
		t.Fatalf("repeated degradation must stay silent, got:\n%s", second)
	}
}

// TestDiagnosticsEventDedupedByFingerprint pins the event-volume guard: a
// server re-publishing the same set does not emit a second diagnostics event.
func TestDiagnosticsEventDedupedByFingerprint(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	fake.diagnosticsFor = func(string, int) []fakeDiagnostic {
		return []fakeDiagnostic{{Line: 0, Char: 0, EndChar: 7, Severity: int(SeverityError), Message: "boom"}}
	}
	recorder := &eventRecorder{}
	cfg := testConfig(t, func(c *Config) { c.Diagnostics.WaitMS = 200 })
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{Dial: fake.dial(), Observer: recorder.observe})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	bridge.AppendToResult(ctx, "e1\n", []string{path})
	if err := os.WriteFile(path, []byte("package main\n// changed\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	bridge.AppendToResult(ctx, "e2\n", []string{path})

	waitFor(t, "didChange", func() bool { return fake.count("didChange") == 1 })
	time.Sleep(150 * time.Millisecond)
	emitted := 0
	for _, event := range recorder.snapshot() {
		if event.Kind == EventDiagnostics && strings.HasSuffix(event.Path, "main.go") {
			emitted++
		}
	}
	if emitted != 1 {
		t.Fatalf("diagnostics events = %d, want 1 (identical set must be deduped)", emitted)
	}
}

func TestFingerprintHelpers(t *testing.T) {
	first := FingerprintPath(`C:\Work\Demo\main.go`)
	second := FingerprintPath(`c:\work\demo\main.go`)
	if first == "" || first != second {
		t.Fatalf("path fingerprint must be stable and case-insensitive: %q vs %q", first, second)
	}
	if strings.Contains(first, "main") || FingerprintPath(`C:\other.go`) == first {
		t.Fatalf("path fingerprint must not leak the path: %q", first)
	}
	items := []Diagnostic{
		{Severity: SeverityError, Message: "b", Range: CanonicalRange{Start: CanonicalPos{Line: 1}, End: CanonicalPos{Line: 1, Column: 2}}},
		{Severity: SeverityWarning, Message: "a", Range: CanonicalRange{Start: CanonicalPos{Line: 0}, End: CanonicalPos{Line: 0, Column: 1}}},
	}
	reversed := []Diagnostic{items[1], items[0]}
	if DiagnosticsFingerprint(items) == "" || DiagnosticsFingerprint(items) != DiagnosticsFingerprint(reversed) {
		t.Fatal("diagnostics fingerprint must be order independent")
	}
	if DiagnosticsFingerprint(nil) != "" {
		t.Fatal("empty diagnostics set must fingerprint to empty string")
	}
}

func TestReasonCategory(t *testing.T) {
	cases := map[string]string{
		`lsp: start pyright: executable "pyright-langserver" not found`: "binary_missing",
		"lsp: gopls crashed; restart budget exhausted (restartLimit=1)": "restart_budget_exhausted",
		"server still starting: gopls":                                  "starting",
		"no fresh diagnostics within 1s":                                "wait_timeout",
		"no fresh diagnostics within 1s: server published nothing (file may be outside its module, in an ignored directory, or the server is still loading its first analysis; retry shortly)": "no_publish",
		"transport closed":              "transport_closed",
		"exit status 1: unexpected EOF": "crashed",
		"":                              "",
	}
	for reason, want := range cases {
		if got := ReasonCategory(reason); got != want {
			t.Fatalf("ReasonCategory(%q) = %q, want %q", reason, got, want)
		}
	}
}

// TestNoPublishReasonIsActionable pins the live-debugging finding: when a
// server never publishes anything (file outside its module, or in a directory
// the toolchain ignores), the timeout reason must say so instead of a bare
// "no fresh diagnostics".
func TestNoPublishReasonIsActionable(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	fake.suppressPublish = true
	cfg := testConfig(t, func(c *Config) { c.Diagnostics.WaitMS = 300 })
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{Dial: fake.dial()})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	out := bridge.AppendToResult(ctx, "edit\n", []string{path})
	if !strings.Contains(out, "server published nothing") || !strings.Contains(out, "retry shortly") {
		t.Fatalf("no-publish reason must be actionable, got:\n%s", out)
	}
	outcome := bridge.Diagnose(ctx, path)
	if got := classifyOutcome(outcome); got != "degraded_no_fresh" {
		t.Fatalf("outcome = %q, want degraded_no_fresh", got)
	}
	if !strings.Contains(outcome.Reason, "no fresh diagnostics") {
		t.Fatalf("reason must keep the no_fresh prefix for classification, got %q", outcome.Reason)
	}
	// The persisted/observable category must tell "never published" apart from
	// a plain wait timeout: they need different fixes (workspace vs budget).
	recent := bridge.MetricsSnapshot().RecentRequests
	if len(recent) == 0 || recent[0].ReasonCategory != "no_publish" {
		t.Fatalf("recent reason_category = %+v, want no_publish", recent)
	}
}

// TestColdGraceLetsLateFirstPublishLand pins the cold-view grace: a first
// publish that lands after the normal budget but inside the grace must
// conclude the request instead of degrading.
func TestColdGraceLetsLateFirstPublishLand(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	fake.publishDelay = 450 * time.Millisecond
	cfg := testConfig(t, func(c *Config) {
		c.Servers[0].EmptyPublishConclusive = boolPtr(true)
		c.Diagnostics.WaitMS = 200
		c.Diagnostics.EmptyConfirmMS = 50
		c.Diagnostics.ColdStartGraceMS = 600
	})
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{Dial: fake.dial()})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	start := time.Now()
	out := bridge.AppendToResult(ctx, "edit\n", []string{path})
	elapsed := time.Since(start)
	if !strings.Contains(out, `count="0"`) {
		t.Fatalf("cold grace must let the late first publish conclude the wait, got:\n%s", out)
	}
	if elapsed < 420*time.Millisecond || elapsed > 1500*time.Millisecond {
		t.Fatalf("elapsed = %v, want the publish (~500ms) to conclude the request", elapsed)
	}
}

// TestColdGraceGrantedOncePerPath bounds the extension: a path that never
// publishes (ignored directory / outside the view) pays the grace once, then
// returns to the normal budget instead of degrading every edit slowly.
func TestColdGraceGrantedOncePerPath(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	fake.suppressPublish = true
	cfg := testConfig(t, func(c *Config) {
		c.Diagnostics.WaitMS = 200
		c.Diagnostics.ColdStartGraceMS = 400
	})
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{Dial: fake.dial()})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	start := time.Now()
	first := bridge.AppendToResult(ctx, "edit\n", []string{path})
	firstElapsed := time.Since(start)
	if !strings.Contains(first, "LSP diagnostics unavailable") {
		t.Fatalf("expected a degraded first edit, got:\n%s", first)
	}
	if firstElapsed < 520*time.Millisecond {
		t.Fatalf("first wait = %v, want the one-time grace to apply (~600ms)", firstElapsed)
	}

	start = time.Now()
	_ = bridge.AppendToResult(ctx, "edit2\n", []string{path})
	secondElapsed := time.Since(start)
	// The note itself is deduped (HintOnce), so only the timing is observable.
	if secondElapsed > 450*time.Millisecond {
		t.Fatalf("second wait = %v, want the normal budget (~200ms) after the one-time grace", secondElapsed)
	}
}

// TestPrewarmOpensDocumentAfterColdStart pins the background prewarm: an edit
// that degrades while the server is handshaking must not wait for the next
// edit to open the document — the first analysis starts as soon as the server
// is ready.
func TestPrewarmOpensDocumentAfterColdStart(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	fake.initDelay = 300 * time.Millisecond
	cfg := testConfig(t, func(c *Config) {
		c.Diagnostics.WaitMS = 400
		c.Diagnostics.StartWaitMS = 50
	})
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{Dial: fake.dial()})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	out := bridge.AppendToResult(ctx, "edit\n", []string{path})
	if !strings.Contains(out, "server still starting") {
		t.Fatalf("expected a fast cold-start degrade, got:\n%s", out)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if fake.count("didOpen") >= 1 && fake.count("didSave") >= 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("prewarm never opened/saved the document: didOpen=%d didSave=%d",
		fake.count("didOpen"), fake.count("didSave"))
}

// TestClientStatusReportsFirstPublishMS pins the cold-start observable: the
// status carries the delay from server start to the first publish.
func TestClientStatusReportsFirstPublishMS(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	fake.publishDelay = 80 * time.Millisecond
	cfg := testConfig(t, nil)
	var events []Event
	client, err := NewClient(ClientOptions{
		Spec:            cfg.Servers[0],
		Root:            dir,
		Dial:            fake.dial(),
		Observer:        func(event Event) { events = append(events, event) },
		MaxTrackedDocs:  8,
		StartupTimeout:  2 * time.Second,
		ShutdownTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()
	t.Cleanup(func() { client.Shutdown(ctx) })
	if err := client.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	version, err := client.OpenOrUpdate(ctx, path, []byte("package main\n"))
	if err != nil {
		t.Fatalf("OpenOrUpdate: %v", err)
	}
	if err := client.Save(ctx, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, fresh := client.WaitDiagnostics(ctx, path, version, time.Second); !fresh {
		t.Fatal("expected the fake publish to arrive")
	}
	if got := client.Status().FirstPublishMS; got < 40 {
		t.Fatalf("FirstPublishMS = %d, want the ~80ms publish delay to be visible", got)
	}
	// 冷启动延迟必须落盘（事件面），否则基线跨会话拿不到它：首个发布时补发的
	// 状态事件要带 first_publish_ms。
	found := false
	for _, event := range events {
		if event.Kind == EventServerState && event.Status.FirstPublishMS >= 40 {
			found = true
		}
	}
	if !found {
		t.Fatalf("no server-state event carried first_publish_ms: %+v", events)
	}
}

// oneProblem returns a single-diagnostic set for the fake server.
func oneProblem(string, int) []fakeDiagnostic {
	return []fakeDiagnostic{{Line: 0, Char: 0, EndChar: 1, Severity: 1, Message: "boom"}}
}

// TestEarlyAcceptSkippedAfterProblemsForSameVersion pins the false-clean
// guard: an empty publish for a version that already produced diagnostics is a
// re-analysis artifact (live evidence: gopls republished empty then the real
// set 1.3-7.9s later for the same version), so the wait must not conclude
// clean from it.
func TestEarlyAcceptSkippedAfterProblemsForSameVersion(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	fake.diagnosticsFor = oneProblem
	cfg := testConfig(t, func(c *Config) {
		c.Servers[0].EmptyPublishConclusive = boolPtr(true)
		c.Diagnostics.WaitMS = 400
		c.Diagnostics.EmptyConfirmMS = 50
	})
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{Dial: fake.dial()})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	first := bridge.AppendToResult(ctx, "edit\n", []string{path})
	if !strings.Contains(first, `count="1"`) {
		t.Fatalf("expected the injected diagnostic, got:\n%s", first)
	}
	// Churn: the same version republishes empty, then the real set again.
	uri := PathToURI(path)
	fake.publish(fake.connection(), uri, 1, nil)
	time.AfterFunc(120*time.Millisecond, func() {
		fake.publish(fake.connection(), uri, 1, oneProblem)
	})
	start := time.Now()
	out := bridge.Diagnose(ctx, path)
	elapsed := time.Since(start)
	if !out.Fresh || len(out.Items) != 1 {
		t.Fatalf("churn empty must not conclude clean; want the real set, got %+v", out)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("elapsed = %v, want the wait to continue past the churn empty", elapsed)
	}
}

// TestSettleRefusesChurnEmpty pins the deadline path: when the only snapshot
// is a churn empty for a version that had problems, the wait degrades instead
// of claiming clean.
func TestSettleRefusesChurnEmpty(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	fake.diagnosticsFor = oneProblem
	cfg := testConfig(t, func(c *Config) {
		c.Servers[0].EmptyPublishConclusive = boolPtr(true)
		c.Diagnostics.WaitMS = 250
		c.Diagnostics.EmptyConfirmMS = 50
	})
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{Dial: fake.dial()})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	if first := bridge.AppendToResult(ctx, "edit\n", []string{path}); !strings.Contains(first, `count="1"`) {
		t.Fatalf("expected the injected diagnostic, got:\n%s", first)
	}
	fake.publish(fake.connection(), PathToURI(path), 1, nil)
	out := bridge.Diagnose(ctx, path)
	if !out.Degraded || out.Fresh {
		t.Fatalf("churn empty must degrade, not claim clean: %+v", out)
	}
	if !strings.Contains(out.Reason, "no fresh diagnostics") {
		t.Fatalf("reason = %q, want the no_fresh classification", out.Reason)
	}
}

// TestFalseCleanCounterCountsSupersededEmpty pins the regression guard: a
// conclusive-empty answer contradicted by a non-empty publish for the same
// version is counted in the server status.
func TestFalseCleanCounterCountsSupersededEmpty(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t) // stamped empty publishes
	cfg := testConfig(t, func(c *Config) {
		c.Servers[0].EmptyPublishConclusive = boolPtr(true)
		c.Diagnostics.WaitMS = 400
		c.Diagnostics.EmptyConfirmMS = 50
	})
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{Dial: fake.dial()})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	if out := bridge.AppendToResult(ctx, "edit\n", []string{path}); !strings.Contains(out, `count="0"`) {
		t.Fatalf("expected the fast clean path, got:\n%s", out)
	}
	fake.publish(fake.connection(), PathToURI(path), 1, oneProblem)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := bridge.Statuses()[0].EmptyAcceptSuperseded; got == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("empty_accept_superseded = %d, want 1", bridge.Statuses()[0].EmptyAcceptSuperseded)
}

// TestColdRetryAfterGraceTimeout pins the long-cold-window behavior: once the
// one-time grace is spent with nothing ever published, later edits fail fast
// (cold_retry_ms) instead of re-paying the full budget on every edit, and the
// first publish restores the normal path.
func TestColdRetryAfterGraceTimeout(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	fake.setSuppressPublish(true)
	cfg := testConfig(t, func(c *Config) {
		c.Servers[0].EmptyPublishConclusive = boolPtr(true)
		c.Diagnostics.WaitMS = 200
		c.Diagnostics.StartWaitMS = 50
		c.Diagnostics.ColdStartGraceMS = 400
		c.Diagnostics.ColdRetryMS = 100
		c.Diagnostics.EmptyConfirmMS = 40
	})
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{Dial: fake.dial()})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	start := time.Now()
	first := bridge.AppendToResult(ctx, "e1\n", []string{path})
	firstElapsed := time.Since(start)
	if !strings.Contains(first, "LSP diagnostics unavailable") {
		t.Fatalf("expected a degrade, got:\n%s", first)
	}
	if firstElapsed < 550*time.Millisecond {
		t.Fatalf("first wait = %v, want the grace-extended budget (~600ms)", firstElapsed)
	}

	start = time.Now()
	_ = bridge.AppendToResult(ctx, "e2\n", []string{path})
	secondElapsed := time.Since(start)
	if secondElapsed > 400*time.Millisecond {
		t.Fatalf("second wait = %v, want the reduced cold retry (~100ms)", secondElapsed)
	}

	// A publish ends the cold state and restores the normal fast path. The
	// file content never changed, so the document version is still 1.
	fake.publish(fake.connection(), PathToURI(path), 1, nil)
	start = time.Now()
	third := bridge.AppendToResult(ctx, "e3\n", []string{path})
	thirdElapsed := time.Since(start)
	if !strings.Contains(third, `count="0"`) {
		t.Fatalf("expected the restored fast clean path, got:\n%s", third)
	}
	if thirdElapsed > 300*time.Millisecond {
		t.Fatalf("third wait = %v, want the normal early-accept path", thirdElapsed)
	}
}

// TestColdRetryAppliesPerPathOnWarmConnection pins the per-path fast-fail: a
// path that never published anything fails fast on later edits even when the
// connection is already warm for other paths. Live window evidence: 11 of 20
// no_fresh requests sat on never-published paths while gopls had already
// published for other paths (one path burned the full 1000ms budget six
// times), because the old condition required the *connection* to have never
// published. The connection-level grace stays reserved for cold connections:
// the first edit on such a path pays the plain budget, and the path's own
// publish restores the normal route.
func TestColdRetryAppliesPerPathOnWarmConnection(t *testing.T) {
	dir := t.TempDir()
	warmPath := writeTestFile(t, dir, "warm.go", "package main\n")
	coldPath := writeTestFile(t, dir, "cold.go", "package main\n")
	fake := newFakeServer(t)
	fake.setSuppressPublish(true)
	cfg := testConfig(t, func(c *Config) {
		c.Servers[0].EmptyPublishConclusive = boolPtr(true)
		c.Diagnostics.WaitMS = 400
		c.Diagnostics.StartWaitMS = 50
		c.Diagnostics.ColdStartGraceMS = 200
		c.Diagnostics.ColdRetryMS = 50
		c.Diagnostics.EmptyConfirmMS = 40
	})
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{Dial: fake.dial()})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	// First request establishes the connection (and spends warmPath's grace);
	// the publish for warmPath then makes the connection warm, which used to
	// disable the fast-fail for every path.
	_ = bridge.AppendToResult(ctx, "w1\n", []string{warmPath})
	fake.publish(fake.connection(), PathToURI(warmPath), 1, nil)

	start := time.Now()
	first := bridge.AppendToResult(ctx, "c1\n", []string{coldPath})
	firstElapsed := time.Since(start)
	// 同一会话内重复的同类降级只解释一次（claimDegradeHint），所以这里断言
	// 指标面（outcome）而不是文本里的 note。
	if recent := bridge.MetricsSnapshot().RecentRequests; len(recent) == 0 ||
		recent[0].Outcome != "degraded_no_fresh" {
		t.Fatalf("first cold-path request must degrade, recent = %+v, text = %q", recent, first)
	}
	if firstElapsed < 300*time.Millisecond {
		t.Fatalf("first cold-path wait = %v, want the plain budget (~400ms, no connection-level grace)", firstElapsed)
	}

	start = time.Now()
	second := bridge.AppendToResult(ctx, "c2\n", []string{coldPath})
	secondElapsed := time.Since(start)
	if recent := bridge.MetricsSnapshot().RecentRequests; len(recent) == 0 ||
		recent[0].Outcome != "degraded_no_fresh" {
		t.Fatalf("second cold-path request must stay degraded, recent = %+v, text = %q", recent, second)
	}
	if secondElapsed > 200*time.Millisecond {
		t.Fatalf("second cold-path wait = %v, want the reduced cold retry (~50ms)", secondElapsed)
	}

	// The path's own publish clears the cold mark: the next edit takes the
	// normal route (a conclusive empty publish answers immediately). The file
	// content never changed, so the document version is still 1.
	fake.publish(fake.connection(), PathToURI(coldPath), 1, nil)
	start = time.Now()
	third := bridge.AppendToResult(ctx, "c3\n", []string{coldPath})
	thirdElapsed := time.Since(start)
	if recent := bridge.MetricsSnapshot().RecentRequests; len(recent) == 0 ||
		recent[0].Outcome != "clean" {
		t.Fatalf("publish must restore the fast clean path, recent = %+v, text = %q", recent, third)
	}
	if thirdElapsed > 300*time.Millisecond {
		t.Fatalf("third wait = %v, want the normal early-accept path", thirdElapsed)
	}
}
