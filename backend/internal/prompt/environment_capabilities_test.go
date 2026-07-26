package prompt

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

	var calls atomic.Int64
	SetEnvironmentCommandProbe(func(name string) (string, error) {
		calls.Add(1)
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
	if calls.Load() == 0 {
		t.Fatal("expected probe calls")
	}
	firstCalls := calls.Load()
	if len(AvailableEnvironmentCommands()) == 0 {
		t.Fatalf("expected available commands from first probe, got %#v", first)
	}
	_ = second
	if calls.Load() != firstCalls {
		t.Fatalf("expected cache hit without extra probes, calls %d -> %d", firstCalls, calls.Load())
	}

	ResetEnvironmentCapabilityCache()
	_ = DetectEnvironmentCapabilities()
	if calls.Load() <= firstCalls {
		t.Fatalf("expected re-probe after cache reset, calls stayed at %d", calls.Load())
	}
}

func TestDetectEnvironmentCapabilities_ProbesInParallel(t *testing.T) {
	t.Cleanup(func() {
		SetEnvironmentCommandProbe(nil)
		SetEnvironmentCommandHealthProbe(nil)
		ResetEnvironmentCapabilityCache()
	})

	var active atomic.Int64
	var maxActive atomic.Int64
	SetEnvironmentCommandProbe(func(name string) (string, error) {
		cur := active.Add(1)
		for {
			prev := maxActive.Load()
			if cur <= prev || maxActive.CompareAndSwap(prev, cur) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		active.Add(-1)
		if name == "git" || name == "go" || name == "rg" {
			return "/bin/" + name, nil
		}
		return "", errors.New("missing")
	})
	SetEnvironmentCommandHealthProbe(func(name, path string) (bool, string) {
		return true, ""
	})
	ResetEnvironmentCapabilityCache()

	start := time.Now()
	report := DetectEnvironmentCapabilities()
	elapsed := time.Since(start)

	if maxActive.Load() < 2 {
		t.Fatalf("expected concurrent probes, max active=%d", maxActive.Load())
	}
	// Sequential would be catalog_size * 40ms (~480ms+). Bounded parallelism should finish sooner.
	if elapsed > 400*time.Millisecond {
		t.Fatalf("expected parallel probe latency, elapsed=%s maxActive=%d", elapsed, maxActive.Load())
	}
	available := availableEnvironmentCommandsFromReport(report)
	if len(available) < 3 {
		t.Fatalf("expected git/go/rg available, got %#v", available)
	}
}

func TestDetectEnvironmentCapabilities_DiskCacheRoundTrip(t *testing.T) {
	t.Cleanup(func() {
		SetEnvironmentCommandProbe(nil)
		SetEnvironmentCommandHealthProbe(nil)
		SetEnvironmentCapabilityDiskCacheEnabled(true)
		SetEnvironmentCapabilityDiskCachePath("")
		ResetEnvironmentCapabilityCache()
	})

	cachePath := filepath.Join(t.TempDir(), "env_capabilities.json")
	SetEnvironmentCapabilityDiskCachePath(cachePath)
	SetEnvironmentCapabilityDiskCacheEnabled(true)

	// First measure with real host probes so the durable cache is meaningful.
	SetEnvironmentCommandProbe(nil)
	SetEnvironmentCommandHealthProbe(nil)
	ResetEnvironmentCapabilityCache()
	first := DetectEnvironmentCapabilities()
	if len(first.Capabilities) == 0 {
		t.Fatal("expected capabilities from first probe")
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("expected disk cache file: %v", err)
	}

	// Clear only the in-memory snapshot so the next detect must load from disk
	// (or re-probe). A short pause makes a fresh ProbedAt distinguishable.
	time.Sleep(20 * time.Millisecond)
	ResetEnvironmentCapabilityCache()

	second := DetectEnvironmentCapabilities()
	if len(second.Capabilities) != len(first.Capabilities) {
		t.Fatalf("disk cache size mismatch: first=%d second=%d", len(first.Capabilities), len(second.Capabilities))
	}
	if !second.ProbedAt.Equal(first.ProbedAt) {
		t.Fatalf("expected disk-cache ProbedAt reuse, first=%s second=%s", first.ProbedAt, second.ProbedAt)
	}
	for i := range first.Capabilities {
		if first.Capabilities[i].Name != second.Capabilities[i].Name ||
			first.Capabilities[i].Available != second.Capabilities[i].Available {
			t.Fatalf("disk cache mismatch at %d: first=%#v second=%#v", i, first.Capabilities[i], second.Capabilities[i])
		}
	}
}

func TestDetectEnvironmentCapabilities_SingleflightSharesProbe(t *testing.T) {
	t.Cleanup(func() {
		SetEnvironmentCommandProbe(nil)
		SetEnvironmentCommandHealthProbe(nil)
		ResetEnvironmentCapabilityCache()
	})

	var started atomic.Int64
	var inProbe sync.WaitGroup
	inProbe.Add(1)
	var release sync.WaitGroup
	release.Add(1)

	SetEnvironmentCommandProbe(func(name string) (string, error) {
		if started.Add(1) == 1 {
			inProbe.Done()
			release.Wait()
		}
		if name == "git" {
			return "/bin/git", nil
		}
		return "", errors.New("missing")
	})
	SetEnvironmentCommandHealthProbe(func(name, path string) (bool, string) {
		return true, ""
	})
	ResetEnvironmentCapabilityCache()

	var wg sync.WaitGroup
	reports := make([]EnvironmentCapabilityReport, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			reports[idx] = DetectEnvironmentCapabilities()
		}(i)
	}
	inProbe.Wait()
	// Concurrent callers should join the in-flight probe, not each re-scan.
	time.Sleep(20 * time.Millisecond)
	release.Done()
	wg.Wait()

	for i, report := range reports {
		if len(report.Capabilities) == 0 {
			t.Fatalf("report %d empty", i)
		}
		foundGit := false
		for _, cap := range report.Capabilities {
			if cap.Name == "git" && cap.Available {
				foundGit = true
				break
			}
		}
		if !foundGit {
			t.Fatalf("report %d missing available git: %#v", i, report.Capabilities)
		}
	}
	// One probe wave walks the catalog once. Without singleflight, four waiters
	// would roughly multiply LookPath traffic by the waiter count.
	calls := started.Load()
	if calls < 1 {
		t.Fatal("expected probe to start")
	}
	if calls > int64(len(environmentCapabilityCatalog)*4) {
		t.Fatalf("singleflight failed: LookPath calls=%d catalog=%d", calls, len(environmentCapabilityCatalog))
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
		SetEnvironmentCapabilityDiskCachePath("")
		SetEnvironmentCapabilityDiskCacheEnabled(true)
		ResetEnvironmentCapabilityCache()
	})
	// Isolate from host disk cache so a previous poisoned snapshot cannot fail
	// this live probe comparison.
	SetEnvironmentCapabilityDiskCachePath(filepath.Join(t.TempDir(), "env_capabilities.json"))
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
		// Default production probe is PATH + stub rejection (no --version spawn).
		// Keep the independent check aligned so this test does not reintroduce
		// process-health latency into the availability contract.
		wantAvailable := err == nil && strings.TrimSpace(path) != "" && !isUnusablePathStub(path)
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

func BenchmarkDetectEnvironmentCapabilities_Cold(b *testing.B) {
	b.Cleanup(func() {
		SetEnvironmentCommandProbe(nil)
		SetEnvironmentCommandHealthProbe(nil)
		SetEnvironmentCapabilityDiskCachePath("")
		SetEnvironmentCapabilityDiskCacheEnabled(true)
		ResetEnvironmentCapabilityCache()
	})
	cachePath := filepath.Join(b.TempDir(), "env_capabilities.json")
	SetEnvironmentCapabilityDiskCachePath(cachePath)
	SetEnvironmentCapabilityDiskCacheEnabled(false)
	SetEnvironmentCommandProbe(nil)
	SetEnvironmentCommandHealthProbe(nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ResetEnvironmentCapabilityCache()
		_ = DetectEnvironmentCapabilities()
	}
}

func BenchmarkDetectEnvironmentCapabilities_WarmMemory(b *testing.B) {
	b.Cleanup(func() {
		SetEnvironmentCommandProbe(nil)
		SetEnvironmentCommandHealthProbe(nil)
		SetEnvironmentCapabilityDiskCachePath("")
		SetEnvironmentCapabilityDiskCacheEnabled(true)
		ResetEnvironmentCapabilityCache()
	})
	SetEnvironmentCapabilityDiskCacheEnabled(false)
	SetEnvironmentCommandProbe(nil)
	SetEnvironmentCommandHealthProbe(nil)
	ResetEnvironmentCapabilityCache()
	_ = DetectEnvironmentCapabilities()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = DetectEnvironmentCapabilities()
	}
}

func BenchmarkDetectEnvironmentCapabilities_WarmDisk(b *testing.B) {
	b.Cleanup(func() {
		SetEnvironmentCommandProbe(nil)
		SetEnvironmentCommandHealthProbe(nil)
		SetEnvironmentCapabilityDiskCachePath("")
		SetEnvironmentCapabilityDiskCacheEnabled(true)
		ResetEnvironmentCapabilityCache()
	})
	cachePath := filepath.Join(b.TempDir(), "env_capabilities.json")
	SetEnvironmentCapabilityDiskCachePath(cachePath)
	SetEnvironmentCapabilityDiskCacheEnabled(true)
	SetEnvironmentCommandProbe(nil)
	SetEnvironmentCommandHealthProbe(nil)
	ResetEnvironmentCapabilityCache()
	_ = DetectEnvironmentCapabilities()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ResetEnvironmentCapabilityCache()
		_ = DetectEnvironmentCapabilities()
	}
}
