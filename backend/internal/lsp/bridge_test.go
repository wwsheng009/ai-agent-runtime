package lsp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testConfig(t *testing.T, mutate func(*Config)) Config {
	t.Helper()
	cfg := Config{
		Enabled: true,
		Servers: []ServerSpec{{
			Name:       "fake",
			Command:    "fake-lsp",
			Extensions: []string{".go"},
		}},
		Diagnostics: DiagnosticsConfig{
			Scope:       ScopeAll,
			MaxItems:    20,
			MaxChars:    2000,
			WaitMS:      2000,
			DegradeMode: DegradeHint,
		},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return cfg
}

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestHandlesFileSelector(t *testing.T) {
	cases := []struct {
		name string
		spec ServerSpec
		path string
		want bool
	}{
		{"extension match", ServerSpec{Name: "go", Extensions: []string{".go"}}, "main.go", true},
		{"extension case insensitive", ServerSpec{Name: "go", Extensions: []string{".GO"}}, "main.go", true},
		{"extension mismatch", ServerSpec{Name: "go", Extensions: []string{".go"}}, "main.py", false},
		{"filename match", ServerSpec{Name: "ts", Filenames: []string{"tsconfig.json"}}, "pkg/tsconfig.json", true},
		{"language match", ServerSpec{Name: "ts", Languages: []string{"typescript"}}, "src/app.ts", true},
		{"language mismatch", ServerSpec{Name: "ts", Languages: []string{"typescript"}}, "src/app.rs", false},
		{"no selector claims nothing", ServerSpec{Name: "empty"}, "main.go", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.spec.HandlesFile(tc.path); got != tc.want {
				t.Fatalf("HandlesFile(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestConfigNormalizeDefaults(t *testing.T) {
	cfg := Config{
		Servers: []ServerSpec{
			{Name: "go", Command: "gopls"},
			{Name: "go", Command: "duplicate"},
			{Name: "", Command: "nameless"},
		},
		Diagnostics: DiagnosticsConfig{Scope: "bogus", MaxItems: -1, MaxChars: 0, WaitMS: 0, DegradeMode: "bogus"},
	}
	normalized := cfg.Normalize()
	if normalized.Enabled {
		t.Fatalf("Enabled must stay false by default (A11)")
	}
	if len(normalized.Servers) != 1 || normalized.Servers[0].Command != "gopls" {
		t.Fatalf("duplicate/malformed specs must be dropped keeping the first: %+v", normalized.Servers)
	}
	diag := normalized.Diagnostics
	if diag.Scope != ScopeAll || diag.MaxItems != DefaultMaxItems || diag.MaxChars != DefaultMaxChars ||
		diag.WaitMS != DefaultWaitMS || diag.DegradeMode != DegradeHint {
		t.Fatalf("diagnostics defaults not applied: %+v", diag)
	}
}

func TestAppendToResultPreservesOutputAndAppendsDiagnostics(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	fake := newFakeServer(t)
	fake.diagnosticsFor = func(string, int) []fakeDiagnostic {
		return []fakeDiagnostic{{
			Line: 2, Char: 5, EndChar: 9, Severity: int(SeverityError),
			Message: "undefined: foo", Code: "E1",
		}}
	}
	bridge := NewBridge(testConfig(t, nil), dir, nil, fake.dial())
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	const original = "edited main.go\n"
	out := bridge.AppendToResult(ctx, original, []string{path})
	if !strings.HasPrefix(out, original) {
		t.Fatalf("original output must be preserved untouched, got:\n%s", out)
	}
	if !strings.Contains(out, `<lsp_diagnostics file="`+path+`"`) {
		t.Fatalf("missing diagnostics header:\n%s", out)
	}
	if !strings.Contains(out, `count="1"`) || !strings.Contains(out, `scope="all"`) {
		t.Fatalf("header must carry count/scope facts:\n%s", out)
	}
	if !strings.Contains(out, "undefined: foo") || !strings.Contains(out, "error 3:6") {
		t.Fatalf("missing rendered diagnostic line:\n%s", out)
	}
	if !strings.Contains(out, "</lsp_diagnostics>") {
		t.Fatalf("block must be closed:\n%s", out)
	}
	waitFor(t, "didSave notification", func() bool { return fake.count("didSave") == 1 })
	if got := fake.count("didOpen"); got != 1 {
		t.Fatalf("didOpen count = %d, want 1", got)
	}
	if got := fake.lastText(PathToURI(path)); !strings.Contains(got, "func main() {}") {
		t.Fatalf("server never received the document text, got %q", got)
	}
}

func TestAppendToResultIsAppendOnlyWhenOutputLacksNewline(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	fake.diagnosticsFor = func(string, int) []fakeDiagnostic {
		return []fakeDiagnostic{{Line: 0, Char: 0, EndChar: 7, Severity: int(SeverityWarning), Message: "unused"}}
	}
	bridge := NewBridge(testConfig(t, nil), dir, nil, fake.dial())
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	out := bridge.AppendToResult(ctx, "no trailing newline", []string{path})
	if !strings.HasPrefix(out, "no trailing newline\n<lsp_diagnostics") {
		t.Fatalf("appending must add exactly one newline, got:\n%s", out)
	}
}

func TestAppendToResultSkipsUnhandledFilesSilently(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "notes.txt", "hello\n")
	fake := newFakeServer(t)
	bridge := NewBridge(testConfig(t, nil), dir, nil, fake.dial())
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	if bridge.Handles(path) {
		t.Fatalf(".txt must not be claimed by a .go-only spec")
	}
	out := bridge.AppendToResult(ctx, "wrote notes\n", []string{path})
	if out != "wrote notes\n" {
		t.Fatalf("unhandled file must be a silent skip (A2), got:\n%s", out)
	}
	if records := fake.records(); len(records) != 0 {
		t.Fatalf("no server interaction expected for unhandled files, got %v", records)
	}
}

func TestAppendToResultScopeChangedOnlyReportsNewDiagnostics(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	fake := newFakeServer(t)
	fake.diagnosticsFor = func(_ string, version int) []fakeDiagnostic {
		items := []fakeDiagnostic{{
			Line: 0, Char: 0, EndChar: 7, Severity: int(SeverityWarning),
			Message: "pre-existing: unused import", Code: "W1",
		}}
		if version >= 2 {
			items = append(items, fakeDiagnostic{
				Line: 2, Char: 5, EndChar: 9, Severity: int(SeverityError),
				Message: "fresh: undefined foo", Code: "E1",
			})
		}
		return items
	}
	cfg := testConfig(t, func(c *Config) { c.Diagnostics.Scope = ScopeChanged })
	bridge := NewBridge(cfg, dir, nil, fake.dial())
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	first := bridge.AppendToResult(ctx, "first edit\n", []string{path})
	if !strings.Contains(first, "pre-existing: unused import") {
		t.Fatalf("first pass must report pre-existing diagnostics (empty baseline):\n%s", first)
	}

	writeTestFile(t, dir, "main.go", "package main\n\nfunc main() { foo() }\n")
	second := bridge.AppendToResult(ctx, "second edit\n", []string{path})
	if strings.Contains(second, "pre-existing: unused import") {
		t.Fatalf("scope=changed must filter baseline diagnostics:\n%s", second)
	}
	if !strings.Contains(second, "fresh: undefined foo") {
		t.Fatalf("scope=changed must keep new diagnostics:\n%s", second)
	}
	if !strings.Contains(second, `scope="changed"`) {
		t.Fatalf("header must record the scope:\n%s", second)
	}
	waitFor(t, "didChange notification", func() bool { return fake.count("didChange") == 1 })
}

func TestAppendToResultDegradeModes(t *testing.T) {
	cases := []struct {
		name     string
		mode     DegradeMode
		contains string
		empty    bool
	}{
		{name: "hint", mode: DegradeHint, contains: "<lsp_note"},
		{name: "error", mode: DegradeError, contains: "<lsp_error"},
		{name: "none", mode: DegradeNone, empty: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeTestFile(t, dir, "main.go", "package main\n")
			cfg := testConfig(t, func(c *Config) { c.Diagnostics.DegradeMode = tc.mode })
			bridge := NewBridge(cfg, dir, nil, dialError(errors.New("exec: \"fake-lsp\": executable file not found")))
			ctx := context.Background()
			t.Cleanup(func() { bridge.Stop(ctx) })

			outcome := bridge.Diagnose(ctx, path)
			if !outcome.Handled || !outcome.Degraded {
				t.Fatalf("outcome = %+v, want handled+degraded", outcome)
			}
			const original = "edit succeeded\n"
			out := bridge.AppendToResult(ctx, original, []string{path})
			if tc.empty {
				if out != original {
					t.Fatalf("degrade_mode=none must stay silent, got:\n%s", out)
				}
				return
			}
			if !strings.Contains(out, tc.contains) {
				t.Fatalf("degrade text must be appended, got:\n%s", out)
			}
			if !strings.HasPrefix(out, original) {
				t.Fatalf("degradation is still append-only, got:\n%s", out)
			}
			if outcome := bridge.Diagnose(ctx, path); outcome.Fresh {
				t.Fatalf("no fresh diagnostics can exist without a server: %+v", outcome)
			}
		})
	}
}

func TestRenderTruncationIsVisible(t *testing.T) {
	baseDiagnostics := func(string, int) []fakeDiagnostic {
		return []fakeDiagnostic{
			{Line: 0, Char: 0, EndChar: 1, Severity: int(SeverityError), Message: "first error"},
			{Line: 3, Char: 2, EndChar: 3, Severity: int(SeverityWarning), Message: "second warning"},
			{Line: 5, Char: 1, EndChar: 2, Severity: int(SeverityInformation), Message: "third info"},
		}
	}
	t.Run("max items", func(t *testing.T) {
		dir := t.TempDir()
		path := writeTestFile(t, dir, "main.go", "package main\n")
		fake := newFakeServer(t)
		fake.diagnosticsFor = baseDiagnostics
		cfg := testConfig(t, func(c *Config) { c.Diagnostics.MaxItems = 1 })
		bridge := NewBridge(cfg, dir, nil, fake.dial())
		ctx := context.Background()
		t.Cleanup(func() { bridge.Stop(ctx) })

		out := bridge.AppendToResult(ctx, "edit\n", []string{path})
		if !strings.Contains(out, `truncated="2"`) {
			t.Fatalf("header must expose omitted item count:\n%s", out)
		}
		if !strings.Contains(out, "first error") || strings.Contains(out, "second warning") {
			t.Fatalf("max_items must keep the highest severity items:\n%s", out)
		}
		if !strings.Contains(out, "[lsp] 2 more diagnostics omitted (max_items=1)") {
			t.Fatalf("omission note must be explicit:\n%s", out)
		}
	})
	t.Run("max chars", func(t *testing.T) {
		dir := t.TempDir()
		path := writeTestFile(t, dir, "main.go", "package main\n")
		fake := newFakeServer(t)
		fake.diagnosticsFor = baseDiagnostics
		cfg := testConfig(t, func(c *Config) { c.Diagnostics.MaxChars = 40 })
		bridge := NewBridge(cfg, dir, nil, fake.dial())
		ctx := context.Background()
		t.Cleanup(func() { bridge.Stop(ctx) })

		out := bridge.AppendToResult(ctx, "edit\n", []string{path})
		if !strings.Contains(out, "max_chars=40") {
			t.Fatalf("char budget omission must be visible:\n%s", out)
		}
		if !strings.Contains(out, `count="3"`) {
			t.Fatalf("count reflects the untruncated diagnostic count:\n%s", out)
		}
	})
}

func TestReport(t *testing.T) {
	dir := t.TempDir()
	goPath := writeTestFile(t, dir, "main.go", "package main\n")
	notesPath := writeTestFile(t, dir, "notes.txt", "hi\n")
	fake := newFakeServer(t)
	fake.diagnosticsFor = func(string, int) []fakeDiagnostic {
		return []fakeDiagnostic{{Line: 0, Char: 0, EndChar: 7, Severity: int(SeverityError), Message: "boom"}}
	}
	bridge := NewBridge(testConfig(t, nil), dir, nil, fake.dial())
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	if _, handled, err := bridge.Report(ctx, notesPath); err != nil || handled {
		t.Fatalf("unhandled file: handled=%v err=%v, want false/nil", handled, err)
	}
	text, handled, err := bridge.Report(ctx, goPath)
	if err != nil || !handled {
		t.Fatalf("handled file: handled=%v err=%v, want true/nil", handled, err)
	}
	if !strings.Contains(text, "boom") {
		t.Fatalf("report must contain the diagnostics:\n%s", text)
	}
}

func TestReportWithoutDiagnostics(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	bridge := NewBridge(testConfig(t, nil), dir, nil, fake.dial())
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	text, handled, err := bridge.Report(ctx, path)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v, want true/nil", handled, err)
	}
	if !strings.Contains(text, "No diagnostics reported") {
		t.Fatalf("clean file report must say so explicitly, got:\n%s", text)
	}
}

func TestStatusesExposeReadyServerAfterUse(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	bridge := NewBridge(testConfig(t, nil), dir, nil, fake.dial())
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	before := bridge.Statuses()
	if len(before) != 1 || before[0].State == StateReady {
		t.Fatalf("pool must not be ready before first use: %+v", before)
	}
	bridge.AppendToResult(ctx, "edit\n", []string{path})
	after := bridge.Statuses()
	if len(after) != 1 {
		t.Fatalf("statuses = %+v, want one server", after)
	}
	if after[0].State != StateReady {
		t.Fatalf("state = %s (%s), want ready", after[0].State, after[0].Reason)
	}
	if after[0].PID != 4242 {
		t.Fatalf("pid = %d, want 4242", after[0].PID)
	}
}

func TestDisabledBridgeIsInert(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	cfg := testConfig(t, func(c *Config) { c.Enabled = false })
	bridge := NewBridge(cfg, dir, nil, fake.dial())
	ctx := context.Background()

	if bridge.Enabled() {
		t.Fatalf("disabled config must produce an inert bridge (A11)")
	}
	if out := bridge.AppendToResult(ctx, "edit\n", []string{path}); out != "edit\n" {
		t.Fatalf("inert bridge must pass output through, got:\n%s", out)
	}
	if _, handled, err := bridge.Report(ctx, path); handled || err != nil {
		t.Fatalf("inert bridge report = handled=%v err=%v, want false/nil", handled, err)
	}
	if records := fake.records(); len(records) != 0 {
		t.Fatalf("inert bridge must not spawn servers, got %v", records)
	}
}

func TestAppendToResultDeduplicatesPaths(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "main.go", "package main\n")
	fake := newFakeServer(t)
	fake.diagnosticsFor = func(string, int) []fakeDiagnostic {
		return []fakeDiagnostic{{Line: 0, Char: 0, EndChar: 7, Severity: int(SeverityError), Message: "dup"}}
	}
	bridge := NewBridge(testConfig(t, nil), dir, nil, fake.dial())
	ctx := context.Background()
	t.Cleanup(func() { bridge.Stop(ctx) })

	out := bridge.AppendToResult(ctx, "edit\n", []string{path, path, " " + path + " "})
	if got := strings.Count(out, "<lsp_diagnostics"); got != 1 {
		t.Fatalf("diagnostics block count = %d, want 1:\n%s", got, out)
	}
}
