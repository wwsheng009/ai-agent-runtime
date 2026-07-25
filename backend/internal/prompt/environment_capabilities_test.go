package prompt

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectEnvironmentCapabilities_UsesInjectedProbe(t *testing.T) {
	t.Cleanup(func() {
		SetEnvironmentCommandProbe(nil)
		SetEnvironmentCommandHealthProbe(nil)
		ResetEnvironmentCapabilityCache()
	})

	available := map[string]string{
		"git": "C:\\Program Files\\Git\\cmd\\git.exe",
		"rg":  "D:\\tools\\rg.exe",
		"go":  "C:\\Go\\bin\\go.exe",
	}
	SetEnvironmentCommandProbe(func(name string) (string, error) {
		if path, ok := available[name]; ok {
			return path, nil
		}
		return "", errors.New("not found")
	})
	// Injected health always succeeds so unit tests stay process-free.
	SetEnvironmentCommandHealthProbe(func(name, path string) (bool, string) {
		return true, ""
	})
	ResetEnvironmentCapabilityCache()

	report := DetectEnvironmentCapabilities()
	gotAvailable := map[string]bool{}
	for _, cap := range report.Capabilities {
		gotAvailable[cap.Name] = cap.Available
		if cap.Name == "git" && (!cap.Available || !strings.Contains(cap.Path, "git.exe")) {
			t.Fatalf("expected git available with path, got %#v", cap)
		}
	}
	if !gotAvailable["git"] || !gotAvailable["rg"] || !gotAvailable["go"] {
		t.Fatalf("expected git/rg/go available, got %#v", report.Capabilities)
	}
	if gotAvailable["gh"] {
		t.Fatalf("gh must remain unavailable when probe rejects it: %#v", report.Capabilities)
	}

	guidance := RenderEnvironmentCapabilityGuidance()
	for _, want := range []string{
		"Environment capabilities (measured via PATH + lightweight health probe",
		"Available: git, go, rg",
		"Not found or unhealthy on PATH: cargo, docker, gh, node, python",
		"gh is not available",
		"rg is available in shell, but toolkit `grep` remains preferred",
		"git is available",
		"python is not usable",
	} {
		if !strings.Contains(guidance, want) {
			t.Fatalf("guidance missing %q:\n%s", want, guidance)
		}
	}
}

func TestDetectEnvironmentCapabilities_SkipsWindowsStoreStubAndBrokenHealth(t *testing.T) {
	t.Cleanup(func() {
		SetEnvironmentCommandProbe(nil)
		SetEnvironmentCommandHealthProbe(nil)
		ResetEnvironmentCapabilityCache()
	})

	// Synthetic 0-byte WindowsApps-like path under temp. This exercises the
	// portable placeholder heuristic and does not depend on the local machine.
	stubDir := filepath.Join(t.TempDir(), "WindowsApps")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatalf("mkdir stub dir: %v", err)
	}
	stubPath := filepath.Join(stubDir, "python.exe")
	if err := os.WriteFile(stubPath, nil, 0o644); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	healthyPython := filepath.Join(t.TempDir(), "python.exe")
	if err := os.WriteFile(healthyPython, []byte("fake-python"), 0o755); err != nil {
		t.Fatalf("write healthy python: %v", err)
	}

	SetEnvironmentCommandProbe(func(name string) (string, error) {
		switch name {
		case "python":
			return stubPath, nil
		case "python3":
			return healthyPython, nil
		case "git":
			return "/usr/bin/git", nil
		default:
			return "", errors.New("missing")
		}
	})
	SetEnvironmentCommandHealthProbe(func(name, path string) (bool, string) {
		if path == healthyPython {
			return true, ""
		}
		if path == stubPath {
			t.Fatalf("health probe must not be called for store stub path %q", path)
		}
		// git and others: accept
		return true, ""
	})
	ResetEnvironmentCapabilityCache()

	report := DetectEnvironmentCapabilities()
	byName := map[string]EnvironmentCapability{}
	for _, cap := range report.Capabilities {
		byName[cap.Name] = cap
	}
	python := byName["python"]
	if !python.Available {
		t.Fatalf("expected python available via healthy alias after skipping stub, got %#v", python)
	}
	if python.Path != healthyPython {
		t.Fatalf("expected healthy python path, got %#v", python)
	}

	// Now only a broken shim remains: mark unavailable with note.
	SetEnvironmentCommandProbe(func(name string) (string, error) {
		if name == "python" || name == "python3" || name == "py" {
			return stubPath, nil
		}
		return "", errors.New("missing")
	})
	SetEnvironmentCommandHealthProbe(func(name, path string) (bool, string) {
		return false, "broken shim"
	})
	ResetEnvironmentCapabilityCache()

	report = DetectEnvironmentCapabilities()
	byName = map[string]EnvironmentCapability{}
	for _, cap := range report.Capabilities {
		byName[cap.Name] = cap
	}
	python = byName["python"]
	if python.Available {
		t.Fatalf("expected python unavailable for store stub only, got %#v", python)
	}
	if !strings.Contains(strings.ToLower(python.Note), "placeholder") && !strings.Contains(strings.ToLower(python.Note), "stub") {
		t.Fatalf("expected unusable placeholder note, got %#v", python)
	}

	guidance := RenderEnvironmentCapabilityGuidance()
	if !strings.Contains(guidance, "python is not usable") {
		t.Fatalf("expected python unusable guidance:\n%s", guidance)
	}
	if !strings.Contains(guidance, "Unusable candidate detail: python:") {
		t.Fatalf("expected unusable candidate detail:\n%s", guidance)
	}
}

func TestDetectEnvironmentCapabilities_HealthFailureMarksUnavailable(t *testing.T) {
	t.Cleanup(func() {
		SetEnvironmentCommandProbe(nil)
		SetEnvironmentCommandHealthProbe(nil)
		ResetEnvironmentCapabilityCache()
	})

	SetEnvironmentCommandProbe(func(name string) (string, error) {
		if name == "node" {
			return "C:\\broken\\node.exe", nil
		}
		if name == "git" {
			return "C:\\Program Files\\Git\\cmd\\git.exe", nil
		}
		return "", errors.New("missing")
	})
	SetEnvironmentCommandHealthProbe(func(name, path string) (bool, string) {
		if name == "node" {
			return false, "health check failed (node.exe --version): not a valid Win32 application"
		}
		return true, ""
	})
	ResetEnvironmentCapabilityCache()

	report := DetectEnvironmentCapabilities()
	byName := map[string]EnvironmentCapability{}
	for _, cap := range report.Capabilities {
		byName[cap.Name] = cap
	}
	if byName["node"].Available {
		t.Fatalf("node should be unavailable after health failure: %#v", byName["node"])
	}
	if !strings.Contains(byName["node"].Note, "health check failed") {
		t.Fatalf("expected health failure note: %#v", byName["node"])
	}
	if !byName["git"].Available {
		t.Fatalf("git should remain available: %#v", byName["git"])
	}
}

func TestRenderEnvironmentContextBlock_IncludesMeasuredCommands(t *testing.T) {
	t.Cleanup(func() {
		SetEnvironmentCommandProbe(nil)
		SetEnvironmentCommandHealthProbe(nil)
		ResetEnvironmentCapabilityCache()
	})

	SetEnvironmentCommandProbe(func(name string) (string, error) {
		switch name {
		case "git":
			return "/usr/bin/git", nil
		case "rg", "rg.exe":
			return "/usr/bin/rg", nil
		default:
			return "", errors.New("missing")
		}
	})
	SetEnvironmentCommandHealthProbe(func(name, path string) (bool, string) {
		return true, ""
	})
	ResetEnvironmentCapabilityCache()

	got := RenderEnvironmentContextBlock(`E:\projects\demo`)
	for _, want := range []string{
		"<cwd>E:\\projects\\demo</cwd>",
		"<available_commands>git,rg",
		"<unavailable_commands>",
		"gh",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("environment block missing %q:\n%s", want, got)
		}
	}

	shellGuidance := RenderShellExecutionGuidance()
	if !strings.Contains(shellGuidance, "Environment capabilities (measured via PATH + lightweight health probe") {
		t.Fatalf("expected measured capability section in shell guidance:\n%s", shellGuidance)
	}
	if !strings.Contains(shellGuidance, "Available: git, rg") {
		t.Fatalf("expected available commands from probe:\n%s", shellGuidance)
	}
	if !strings.Contains(shellGuidance, "Not found or unhealthy on PATH") || !strings.Contains(shellGuidance, "gh") {
		t.Fatalf("expected unavailable high-value tools from probe:\n%s", shellGuidance)
	}
}

func TestDetectEnvironmentCapabilities_CachesUntilReset(t *testing.T) {
	t.Cleanup(func() {
		SetEnvironmentCommandProbe(nil)
		SetEnvironmentCommandHealthProbe(nil)
		ResetEnvironmentCapabilityCache()
	})

	calls := 0
	SetEnvironmentCommandProbe(func(name string) (string, error) {
		calls++
		if name == "git" {
			return "/bin/git", nil
		}
		return "", errors.New("missing")
	})
	SetEnvironmentCommandHealthProbe(func(name, path string) (bool, string) {
		return true, ""
	})
	ResetEnvironmentCapabilityCache()

	first := DetectEnvironmentCapabilities()
	second := DetectEnvironmentCapabilities()
	if calls == 0 {
		t.Fatal("expected probe calls")
	}
	firstCalls := calls
	if len(AvailableEnvironmentCommands()) == 0 {
		t.Fatalf("expected available commands from first probe, got %#v", first)
	}
	_ = second
	if calls != firstCalls {
		t.Fatalf("expected cache hit without extra probes, calls %d -> %d", firstCalls, calls)
	}

	ResetEnvironmentCapabilityCache()
	_ = DetectEnvironmentCapabilities()
	if calls <= firstCalls {
		t.Fatalf("expected re-probe after cache reset, calls stayed at %d", calls)
	}
}

func TestIsUnusablePathStub(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Microsoft", "WindowsApps")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stub := filepath.Join(dir, "python.exe")
	if err := os.WriteFile(stub, nil, 0o644); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	if !isUnusablePathStub(stub) {
		t.Fatalf("expected 0-byte WindowsApps path to be unusable placeholder: %s", stub)
	}

	realish := filepath.Join(t.TempDir(), "python.exe")
	if err := os.WriteFile(realish, []byte("not-empty"), 0o755); err != nil {
		t.Fatalf("write realish: %v", err)
	}
	if isUnusablePathStub(realish) {
		t.Fatalf("ordinary non-placeholder path must not be treated as stub: %s", realish)
	}
}

func TestDetectEnvironmentCapabilities_RealHostProbe(t *testing.T) {
	// Integration-style check: force default LookPath + health probe and assert
	// the report matches an independent PATH probe for high-signal tools.
	t.Cleanup(func() {
		SetEnvironmentCommandProbe(nil)
		SetEnvironmentCommandHealthProbe(nil)
		ResetEnvironmentCapabilityCache()
	})
	SetEnvironmentCommandProbe(nil)
	SetEnvironmentCommandHealthProbe(nil)
	ResetEnvironmentCapabilityCache()

	report := DetectEnvironmentCapabilities()
	if len(report.Capabilities) == 0 {
		t.Fatal("expected catalog capabilities from real host probe")
	}

	byName := map[string]EnvironmentCapability{}
	for _, cap := range report.Capabilities {
		byName[cap.Name] = cap
	}

	// Spot-check a few tools against an independent LookPath + version probe so
	// the capability preview cannot drift into hard-coded availability claims.
	for _, name := range []string{"git", "rg", "gh", "go"} {
		cap, ok := byName[name]
		if !ok {
			t.Fatalf("catalog missing %s: %#v", name, report.Capabilities)
		}
		path, err := exec.LookPath(name)
		if name == "rg" && err != nil {
			// Windows installs may only expose rg.exe.
			path, err = exec.LookPath("rg.exe")
		}
		wantAvailable := false
		if err == nil && strings.TrimSpace(path) != "" && !isUnusablePathStub(path) {
			// Independent health check using the same default probe semantics.
			okHealth, _ := defaultEnvironmentCommandHealthProbe(name, path)
			wantAvailable = okHealth
		}
		if cap.Available != wantAvailable {
			t.Fatalf("%s availability mismatch: report=%v independent_want=%v path=%q note=%q", name, cap.Available, wantAvailable, path, cap.Note)
		}
		if wantAvailable && strings.TrimSpace(cap.Path) == "" {
			t.Fatalf("%s available but path empty: %#v", name, cap)
		}
	}

	guidance := RenderEnvironmentCapabilityGuidance()
	if !strings.Contains(guidance, "measured via PATH + lightweight health probe") {
		t.Fatalf("expected measured guidance header:\n%s", guidance)
	}
	// Guidance tips are driven only by the measured report for this process,
	// never by assumptions about a particular developer machine.
	if byName["git"].Available && !strings.Contains(guidance, "git is available") {
		t.Fatalf("git available but guidance omitted git tip:\n%s", guidance)
	}
	if !byName["gh"].Available && !strings.Contains(guidance, "gh is not available") {
		t.Fatalf("gh missing but guidance omitted gh tip:\n%s", guidance)
	}
}
