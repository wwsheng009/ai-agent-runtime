package driftguard_test

// These guards run under `make test` / `go test ./...`, which is how the rules in
// docs/plan/tool-layer-io-seam-design-20261028.md §7.4 stop being advice.
//
// They are written as ratchets, not as assertions about a finished refactor: the
// shared cores still reach the host filesystem today (that is what the seam is for
// removing), so demanding zero now would either block every change or get the test
// deleted. What must never happen is the number growing, a core learning to branch
// on a backend name, or a capability decision being copied into a second place.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/driftguard"
)

// coreBaselines is the measured number of direct host-filesystem calls per shared
// core, as of 2026-10-28. Measured, not remembered: the same set of calls is
// listed in the design document §3.1, where the four cores sum to 57 and
// internal/tools/code_index_resolver.go adds 3 more (60 in total), and this guard
// reproduces 57 with a different method (AST instead of grep) - agreement between
// two independent measurements is what makes the baseline trustworthy.
//
// Lower these numbers as the seam lands and never raise one without writing the
// reason here.
var coreBaselines = map[string]int{
	"viewcore":   5,
	"lscore":     1,
	"searchcore": 24,
	"mutatecore": 27,
}

// backendNames are the execution backends a core must never branch on.
var backendNames = []string{"envd", "ssh", "http", "local"}

func moduleRoot(t *testing.T) string {
	t.Helper()
	// Tests run in the package directory; walk up to the Go module root.
	path, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			t.Fatal("no go.mod found above the test's working directory")
		}
		path = parent
	}
}

func TestSharedCoresDoNotAddHostFilesystemCalls(t *testing.T) {
	root := moduleRoot(t)
	for core, baseline := range coreBaselines {
		core, baseline := core, baseline
		t.Run(core, func(t *testing.T) {
			dir := filepath.Join(root, "internal", "toolcore", core)
			total, sites, err := driftguard.FilesystemCallCounts(dir)
			if err != nil {
				t.Fatalf("count os.* calls under %s: %v", dir, err)
			}
			if total > baseline {
				t.Fatalf("%s makes %d direct host-filesystem calls, %d above the recorded baseline of %d.\n"+
					"A shared core must reach files through the I/O seam, not through os.*: every direct call is\n"+
					"a behaviour a non-local backend has to re-implement, which is how the envd file fork was born.\n"+
					"First finding:\n%s",
					core, total, total-baseline, baseline,
					driftguard.FormatFindings(sites, 5))
			}
			if total < baseline {
				// Ratchet down deliberately: leaving the ceiling high hides the
				// progress and lets the next change re-add calls unnoticed.
				t.Errorf("%s is down to %d host-filesystem calls from the baseline of %d: lower coreBaselines to %d so the improvement is locked in",
					core, total, baseline, total)
			}
		})
	}
}

func TestSharedCoresNeverBranchOnBackendNames(t *testing.T) {
	root := moduleRoot(t)
	for core := range coreBaselines {
		core := core
		t.Run(core, func(t *testing.T) {
			dir := filepath.Join(root, "internal", "toolcore", core)
			found, err := driftguard.BackendNameLiterals(dir, backendNames...)
			if err != nil {
				t.Fatalf("scan %s: %v", dir, err)
			}
			if len(found) > 0 {
				t.Fatalf("%s names a backend in a string literal, which means it branches on transport instead of capability:\n%s",
					core, driftguard.FormatFindings(found, 10))
			}
		})
	}
}

func TestCapabilityDecisionsStayCentralized(t *testing.T) {
	// Today the seam does not exist, so no core may ask for capabilities at all.
	// When Access lands, the answer must live in one resolver (design §4.1 rule 2);
	// raising this ceiling requires naming that resolver here.
	const baseline = 0
	root := moduleRoot(t)
	for core := range coreBaselines {
		core := core
		t.Run(core, func(t *testing.T) {
			dir := filepath.Join(root, "internal", "toolcore", core)
			total, sites, err := driftguard.CapabilityCallCounts(dir, "Capabilities")
			if err != nil {
				t.Fatalf("scan %s: %v", dir, err)
			}
			if total > baseline {
				t.Fatalf("%s asks for capabilities in %d places (ceiling %d): one downgrade rule per need, resolved in a single helper\n%s",
					core, total, baseline, driftguard.FormatFindings(sites, 10))
			}
		})
	}
}

// --- the guards themselves are tested ---
//
// A guard that miscounts is worse than no guard: it either blocks honest changes
// until someone disables it, or it silently measures nothing and reports success.

func writeGoFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestFilesystemCallCountsIgnoresNonCalls(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "real.go", `package fixture

import "os"

func Use() {
	_, _ = os.ReadFile("a")
	_ = os.Stat("b")
	_, _ = os.Getenv("NOT_A_FILESYSTEM_CALL")
}

const mention = "os.ReadFile appears inside a string literal"

// And in a comment: os.WriteFile should not be counted either.
`)
	writeGoFile(t, dir, "aliased.go", `package fixture

import aux "os"

func Other() { _ = aux.Stat("ignored: the source never says os.") }
`)
	writeGoFile(t, dir, "no_import.go", `package fixture

func Nothing() {}
`)
	writeGoFile(t, dir, "ignored_test.go", `package fixture

import "os"

func TestX(t *testing.T) { _ = os.Remove("tests are not shipped code") }
`)

	total, sites, err := driftguard.FilesystemCallCounts(dir)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 2 {
		t.Fatalf("counted %d, want 2 (os.ReadFile + os.Stat only):\n%s", total, driftguard.FormatFindings(sites, 10))
	}
	joined := driftguard.FormatFindings(sites, 10)
	if strings.Contains(joined, "Getenv") || strings.Contains(joined, "string literal") || strings.Contains(joined, "comment") {
		t.Fatalf("non-call sites leaked into the report: %s", joined)
	}
}

func TestFilesystemCallCountsReportsMissingRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if _, _, err := driftguard.FilesystemCallCounts(missing); err == nil {
		t.Fatal("a missing scan root must be an error, not a silent zero that passes forever")
	}
}

func TestFilesystemCallCountsRejectsFileRoot(t *testing.T) {
	dir := t.TempDir()
	path := writeGoFile(t, dir, "solo.go", "package solo\n")
	if _, _, err := driftguard.FilesystemCallCounts(path); err == nil {
		t.Fatal("a file is not a scan root; accepting it would silently measure nothing")
	}
}

func TestBackendNameLiteralsFindsOnlyLiterals(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "branch.go", `package fixture

import "os"

func Pick() string {
	if os.Getenv("BACKEND") == "envd" {
		return "fork"
	}
	return "shared"
}

// The word envd in prose is not a branch and must not be reported.
const prose = "this runs on envd too"
`)
	found, err := driftguard.BackendNameLiterals(dir, "envd")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	// Two literals: the comparison above. The comment is not code.
	if len(found) != 1 {
		t.Fatalf("found %d, want exactly 1 (the comparison):\n%s", len(found), driftguard.FormatFindings(found, 5))
	}
}

func TestCapabilityCallCounts(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "spread.go", `package fixture

type access struct{}

func (access) Capabilities() int { return 0 }

type core struct{ a access }

func (c core) First() bool  { return c.a.Capabilities() > 0 }
func (c core) Second() bool { return c.a.Capabilities() > 1 }
`)
	total, sites, err := driftguard.CapabilityCallCounts(dir, "Capabilities")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 2 {
		t.Fatalf("counted %d, want 2: the guard exists precisely to catch a decision copied into a second place\n%s",
			total, driftguard.FormatFindings(sites, 5))
	}
}

func TestFormatFindingsTruncates(t *testing.T) {
	findings := make([]driftguard.Finding, 0, 10)
	for index := 0; index < 10; index++ {
		findings = append(findings, driftguard.Finding{Pos: "file.go", Message: "x"})
	}
	report := driftguard.FormatFindings(findings, 3)
	if strings.Count(report, "file.go: x") != 3 {
		t.Fatalf("expected 3 shown findings, got:\n%s", report)
	}
	if !strings.Contains(report, "and 7 more") {
		t.Fatalf("the truncation notice is missing, so a reader cannot tell the list was cut:\n%s", report)
	}
	if driftguard.FormatFindings(nil, 3) != "(none)" {
		t.Fatal("an empty list must render as (none), not an empty string")
	}
}
