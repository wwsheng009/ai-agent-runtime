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
	// 结构化原因：live 路径曾把原始进程错误（"exit status 0xffffffff"）留作原因，
	// ReasonCategory 认不出它 → outcome 落回裸 degraded，note 也不可行动。
	// （HintOnce 只解释一次，所以断言落在本条 note 上。）
	out := bridge.AppendToResult(ctx, "e3\n", []string{path})
	if !strings.Contains(out, "<lsp_note") {
		t.Fatalf("exhausted budget must degrade inside the window, got:\n%s", out)
	}
	if !strings.Contains(out, "restart budget exhausted") {
		t.Fatalf("inline note must name the exhausted crash, got:\n%s", out)
	}
	if status := bridge.Statuses()[0]; status.State != StateCrashed ||
		!strings.Contains(status.Reason, "crashed; restart budget exhausted") {
		t.Fatalf("exhausted crash must expose the structured reason, got %+v", status)
	}
	if recent := bridge.MetricsSnapshot().RecentRequests; len(recent) == 0 ||
		recent[0].Outcome != "degraded_crashed" ||
		recent[0].ReasonCategory != "restart_budget_exhausted" {
		t.Fatalf("exhausted crash must classify as degraded_crashed, recent = %+v", recent)
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

// TestAppendToResultSkipsDeletedPaths pins that a delete/move in a patch does
// not turn into an "LSP diagnostics unavailable: read file" note: the path no
// longer exists, so there is nothing to diagnose and no request is recorded.
// Directories are skipped the same way (not documents).
func TestAppendToResultSkipsDeletedPaths(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	cfg := testConfig(t, func(c *Config) {})
	recorder := &eventRecorder{}
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{Dial: fake.dial(), Observer: recorder.observe})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if out := bridge.AppendToResult(ctx, "patched\n", []string{path}); out != "patched\n" {
		t.Fatalf("deleted path must not append anything, got:\n%s", out)
	}
	if out := bridge.AppendToResult(ctx, "patched\n", []string{dir}); out != "patched\n" {
		t.Fatalf("directory must not append anything, got:\n%s", out)
	}
	if events := recorder.snapshot(); len(events) != 0 {
		t.Fatalf("skipped paths must not record an LSP request, got %d event(s)", len(events))
	}
}

// TestDiagnoseCountsNewVsTotalDiagnostics pins the A6 decision data: every
// fresh outcome reports the full diagnostic count and how many items were new
// (absent from the pre-change snapshot), for scope=all as well — that pair is
// what tells the offline baseline how much of the injected payload a
// scope=changed default would drop.
func TestDiagnoseCountsNewVsTotalDiagnostics(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	fake.diagnosticsOnOpen = func(uri string, version int) []fakeDiagnostic {
		// Line 0 stays byte-identical across the edit (LSP lines are 0-based):
		// Key() includes the byte columns, so a diagnostic on a line whose
		// content changed would legitimately count as new (a moved error).
		return []fakeDiagnostic{{Line: 0, Char: 1, EndChar: 2, Severity: 1, Message: "first"}}
	}
	fake.diagnosticsFor = func(uri string, version int) []fakeDiagnostic {
		return []fakeDiagnostic{
			{Line: 0, Char: 1, EndChar: 2, Severity: 1, Message: "first"},
			{Line: 1, Char: 1, EndChar: 2, Severity: 1, Message: "second"},
		}
	}
	cfg := testConfig(t, func(c *Config) {})
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{Dial: fake.dial()})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	first := bridge.Diagnose(ctx, path)
	if !first.Fresh || first.TotalDiagCount != 1 || first.NewDiagCount != 1 {
		t.Fatalf("first outcome = fresh %v total %d new %d, want true/1/1",
			first.Fresh, first.TotalDiagCount, first.NewDiagCount)
	}
	// The second pass must be a real edit: unchanged content skips didChange
	// (no new publish), which would keep the old set and report 1/0.
	path = writeTestFile(t, dir, "main.go", "package main\n// edited\n")
	second := bridge.Diagnose(ctx, path)
	if !second.Fresh || second.TotalDiagCount != 2 || second.NewDiagCount != 1 {
		t.Fatalf("second outcome = fresh %v total %d new %d, want true/2/1",
			second.Fresh, second.TotalDiagCount, second.NewDiagCount)
	}
}

// TestColdGraceCoversNewPathOnWarmClient pins O10: a path that has never
// published still gets the one-time grace extension on a client that has
// already published for other paths. Live evidence (2026-10-01): a newly
// created Go file's first analysis landed 1.71s after didOpen and missed the
// plain 1.0s budget, so the model never saw the compile error of the file it
// had just written; the grace-extended budget covers it.
func TestColdGraceCoversNewPathOnWarmClient(t *testing.T) {
	dir := t.TempDir()
	warm := writeTestFile(t, dir, "warm.go", "package main\n")
	fresh := writeTestFile(t, dir, "fresh.go", "package main\n\n// fresh\n")
	fake := newFakeServer(t)
	cfg := testConfig(t, func(c *Config) {
		c.Servers[0].EmptyPublishConclusive = boolPtr(true)
		c.Diagnostics.WaitMS = 300
		c.Diagnostics.StartWaitMS = 100
		c.Diagnostics.ColdStartGraceMS = 900
		c.Diagnostics.ColdRetryMS = 100
		c.Diagnostics.EmptyConfirmMS = 40
	})
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{Dial: fake.dial()})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	// Warm the connection: this path publishes immediately (empty, stamped).
	if out := bridge.AppendToResult(ctx, "warm\n", []string{warm}); !strings.Contains(out, `count="0"`) {
		t.Fatalf("warm-up request must go clean, got:\n%s", out)
	}
	// The next path's first publish needs 700ms: inside the grace-extended
	// budget (300+900ms), past the plain one (300ms).
	fake.setPublishDelay(700 * time.Millisecond)
	start := time.Now()
	out := bridge.AppendToResult(ctx, "fresh\n", []string{fresh})
	elapsed := time.Since(start)
	if !strings.Contains(out, `count="0"`) {
		t.Fatalf("new path on a warm client must be covered by the grace, got:\n%s", out)
	}
	if elapsed < 700*time.Millisecond {
		t.Fatalf("elapsed = %v, want the delayed publish awaited (>=700ms)", elapsed)
	}
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
	recorder := &eventRecorder{}
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{Dial: fake.dial(), Observer: recorder.observe})
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
	second := bridge.AppendToResult(ctx, "e2\n", []string{path})
	secondElapsed := time.Since(start)
	if secondElapsed > 400*time.Millisecond {
		t.Fatalf("second wait = %v, want the reduced cold retry (~100ms)", secondElapsed)
	}
	// 降级文案必须报告实际预算（100ms），而不是配置的完整 wait_ms（200ms）。
	if !strings.Contains(second, "within 100ms") {
		t.Fatalf("cold fast-fail note must report the actual budget, got:\n%s", second)
	}
	coldFails := 0
	for _, event := range recorder.snapshot() {
		if event.Kind == EventRequest && event.ColdFastFail {
			coldFails++
		}
	}
	if coldFails != 1 {
		t.Fatalf("cold_fast_fail events = %d, want exactly the second edit", coldFails)
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

// TestColdProbeCapsFirstProbeBudget pins O12: the one-time first-probe
// wait is bounded by cold_probe_ms instead of the full grace-extended
// budget (wait_ms + cold_start_grace_ms). The cross-session baseline
// showed 96% of no_fresh requests are first probes
// (lsp_cold_first_probe_ratio 0.9608); the per-path cold fast-fail
// only covers repeats.
func TestColdProbeCapsFirstProbeBudget(t *testing.T) {
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
		c.Diagnostics.ColdProbeMS = 300
		c.Diagnostics.EmptyConfirmMS = 40
	})
	recorder := &eventRecorder{}
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{Dial: fake.dial(), Observer: recorder.observe})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	start := time.Now()
	first := bridge.AppendToResult(ctx, "e1\n", []string{path})
	firstElapsed := time.Since(start)
	if !strings.Contains(first, "LSP diagnostics unavailable") {
		t.Fatalf("expected a degrade, got:\n%s", first)
	}
	// The first probe pays the capped budget (300ms), not the full
	// grace-extended one (200+400=600ms).
	if firstElapsed < 280*time.Millisecond {
		t.Fatalf("first wait = %v, want the capped first-probe budget (~300ms)", firstElapsed)
	}
	if firstElapsed > 500*time.Millisecond {
		t.Fatalf("first wait = %v, want the cold_probe_ms cap (300ms) to bind, not the full 600ms budget", firstElapsed)
	}
	// The degrade note must report the actual (capped) budget.
	if !strings.Contains(first, "within 300ms") {
		t.Fatalf("first-probe note must report the actual budget, got:\n%s", first)
	}

	// The path is now known cold: the next edit still fast-fails at
	// cold_retry_ms (the cap does not change the repeat route).
	start = time.Now()
	second := bridge.AppendToResult(ctx, "e2\n", []string{path})
	secondElapsed := time.Since(start)
	if secondElapsed > 400*time.Millisecond {
		t.Fatalf("second wait = %v, want the reduced cold retry (~100ms)", secondElapsed)
	}
	if !strings.Contains(second, "within 100ms") {
		t.Fatalf("cold fast-fail note must report the actual budget, got:\n%s", second)
	}
	coldFails := 0
	for _, event := range recorder.snapshot() {
		if event.Kind == EventRequest && event.ColdFastFail {
			coldFails++
		}
	}
	if coldFails != 1 {
		t.Fatalf("cold_fast_fail events = %d, want exactly the second edit (the first probe is not a fast-fail)", coldFails)
	}
}

// TestColdProbeFloorRespectsWaitMS pins that the first-probe cap
// never goes below the configured base budget: a caller that raises
// wait_ms explicitly wants the cold path to keep paying it (live
// evidence: the real-server round-trip test sets wait_ms=60s for
// rust-analyzer's slow first analysis).
func TestColdProbeFloorRespectsWaitMS(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	fake.setSuppressPublish(true)
	cfg := testConfig(t, func(c *Config) {
		c.Servers[0].EmptyPublishConclusive = boolPtr(true)
		c.Diagnostics.WaitMS = 500
		c.Diagnostics.StartWaitMS = 50
		c.Diagnostics.ColdStartGraceMS = 200
		c.Diagnostics.ColdRetryMS = 100
		c.Diagnostics.ColdProbeMS = 300
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
	// The cap (300ms) is below wait_ms (500ms): the floor wins and
	// the first probe pays the base budget, not the cap.
	if firstElapsed < 480*time.Millisecond {
		t.Fatalf("first wait = %v, want the wait_ms floor (~500ms), not the 300ms cap", firstElapsed)
	}
	if firstElapsed > 650*time.Millisecond {
		t.Fatalf("first wait = %v, want the floor to bound the grace-extended budget (700ms uncapped)", firstElapsed)
	}
	if !strings.Contains(first, "within 500ms") {
		t.Fatalf("first-probe note must report the floored budget, got:\n%s", first)
	}
}

// TestColdProbeNegativeRestoresFullBudget pins the disable switch: a
// negative cold_probe_ms restores the pre-O12 full grace-extended
// first-probe budget.
func TestColdProbeNegativeRestoresFullBudget(t *testing.T) {
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
		c.Diagnostics.ColdProbeMS = -1
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
	// Cap disabled: the first probe pays the full wait_ms + grace
	// (200+400=600ms).
	if firstElapsed < 550*time.Millisecond {
		t.Fatalf("first wait = %v, want the full grace-extended budget (~600ms) with the cap disabled", firstElapsed)
	}
}

// TestRenderDiagnosticsEmptyStyle pins the clean-result marker: compact drops
// scope/servers (diagnostics.emptyStyle default), full keeps the legacy
// attributes for callers that need them.
func TestRenderDiagnosticsEmptyStyle(t *testing.T) {
	compact := RenderDiagnostics(nil, RenderOptions{File: "main.go", Servers: []string{"gopls"}, EmptyCompact: true})
	if want := "<lsp_diagnostics file=\"main.go\" count=\"0\"/>\n"; compact.Text != want {
		t.Fatalf("compact empty = %q, want %q", compact.Text, want)
	}
	full := RenderDiagnostics(nil, RenderOptions{File: "main.go", Servers: []string{"gopls"}})
	if want := "<lsp_diagnostics file=\"main.go\" count=\"0\" scope=\"all\" servers=\"gopls\"/>\n"; full.Text != want {
		t.Fatalf("full empty = %q, want %q", full.Text, want)
	}
}

// TestRegistrySkipsUnavailableMembers pins the executable preflight: a member
// whose binary is missing is not routed (no spawn attempt, no request event),
// stays visible as unavailable with the actionable binary-missing reason, and
// an available member keeps the normal route.
func TestRegistrySkipsUnavailableMembers(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	cfg := testConfig(t, func(c *Config) {
		c.Servers = []ServerSpec{{Name: "missing", Command: "definitely-missing-lsp", Extensions: []string{".go"}}}
	})
	missing := func(string) (string, error) { return "", errors.New("executable not found") }

	registry := NewRegistry(cfg, dir, RegistryOptions{LookPath: missing})
	if got := registry.ServersForPath(path); len(got) != 0 {
		t.Fatalf("unavailable member must not be routed, got %d", len(got))
	}
	statuses := registry.Statuses()
	if len(statuses) != 1 || statuses[0].State != StateUnavailable {
		t.Fatalf("statuses = %#v, want one unavailable member", statuses)
	}
	if !strings.Contains(statuses[0].Reason, "not found") {
		t.Fatalf("reason = %q, want actionable binary-missing text", statuses[0].Reason)
	}

	available := func(cmd string) (string, error) { return cmd, nil }
	registry2 := NewRegistry(cfg, dir, RegistryOptions{LookPath: available})
	if got := registry2.ServersForPath(path); len(got) != 1 {
		t.Fatalf("available member must be routed, got %d", len(got))
	}

	// Dial 注入的传输跳过预检（测试/自定义宿主一律视为可用）：即使 LookPath
	// 失败也必须正常路由。
	dialRegistry := NewRegistry(cfg, dir, RegistryOptions{
		Dial: func(context.Context, ServerSpec, string, Logger) (*DialResult, error) {
			return nil, errors.New("dial stub")
		},
		LookPath: missing,
	})
	if got := dialRegistry.ServersForPath(path); len(got) != 1 {
		t.Fatalf("Dial-injected transport must skip the executable preflight, got %d", len(got))
	}

	// Bridge 层：预检失败的成员既不认领文件，也不改动编辑回执。
	bridge := NewBridgeWithOptions(cfg, dir, BridgeOptions{LookPath: missing})
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })
	if bridge.Handles(path) {
		t.Fatalf("preflight-skipped member must not claim files")
	}
	if out := bridge.AppendToResult(ctx, "edit\n", []string{path}); out != "edit\n" {
		t.Fatalf("append must stay untouched, got %q", out)
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
