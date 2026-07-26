package prompt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// EnvironmentCommandProbe is the pluggable PATH lookup used by capability
// previews. Tests inject fakes so availability is never hard-coded.
type EnvironmentCommandProbe func(name string) (string, error)

// EnvironmentCommandHealthProbe validates that a resolved executable actually
// runs. Tests inject fakes so health checks do not spawn real processes.
// Returning ok=false means "found on PATH but unusable" (store stubs, broken
// shims, missing runtimes).
type EnvironmentCommandHealthProbe func(name, path string) (ok bool, detail string)

// EnvironmentCapability describes one shell-side tool detected on the host.
type EnvironmentCapability struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	// Note explains non-available outcomes such as failed health checks or
	// platform-specific unusable PATH entries (for example Windows store stubs).
	Note string `json:"note,omitempty"`
}

// EnvironmentCapabilityReport is the measured availability snapshot shown in
// prompts. It is derived from the current process PATH + lightweight health
// probes at runtime. Availability is never hard-coded for a specific machine.
type EnvironmentCapabilityReport struct {
	ProbedAt     time.Time               `json:"probed_at"`
	Capabilities []EnvironmentCapability `json:"capabilities"`
}

// Bump when probe semantics change so durable snapshots from older builds are
// discarded instead of reusing PATH-only vs process-health mismatches.
const environmentCapabilityCacheVersion = 5

var (
	environmentProbeMu      sync.Mutex
	environmentCommandProbe EnvironmentCommandProbe = exec.LookPath
	environmentHealthProbe  EnvironmentCommandHealthProbe
	// customEnvironmentProbes is true when tests inject LookPath/health fakes.
	// Disk cache is disabled while custom probes are active so host cache cannot
	// leak into assertions and injected results are never persisted.
	customCommandProbe       bool
	customHealthProbe        bool
	environmentCapabilityTTL = 5 * time.Minute
	// Keep health probes short so one broken shim cannot dominate startup.
	// Real tools typically answer --version well under this budget; leave a
	// little headroom for Windows process-create latency under load.
	environmentHealthTimeout = 900 * time.Millisecond
	// Cap concurrent health probes so cold Windows startups do not thrash
	// process creation and produce false health-check failures.
	environmentProbeConcurrency = 3
	// Disk cache survives process restarts until PATH/catalog fingerprint changes.
	environmentDiskCacheTTL          = 24 * time.Hour
	environmentDiskCacheEnabled      = true
	environmentDiskCachePathOverride string
	cachedEnvironmentCaps            EnvironmentCapabilityReport
	cachedEnvironmentAt              time.Time
	cachedEnvironmentValid           bool
	// Singleflight: concurrent first-turn callers share one probe.
	environmentProbeInFlight *environmentProbeFlight
)

type environmentProbeFlight struct {
	done   chan struct{}
	report EnvironmentCapabilityReport
}

type environmentCapabilityDiskCache struct {
	Version      int                     `json:"version"`
	Fingerprint  string                  `json:"fingerprint"`
	ProbedAt     time.Time               `json:"probed_at"`
	Capabilities []EnvironmentCapability `json:"capabilities"`
}

// environmentCapabilityCatalog is the ordered set of common developer tools we
// surface when present. Only tools that actually resolve via PATH and pass a
// lightweight health check are listed as available; missing/broken tools may
// appear in the unavailable list when useful for planning.
//
// Startup probing prefers LookPath + 0-byte WindowsApps stub rejection.
// Process --version spawns are intentionally not the default path: npm alone
// can exceed 300ms and would dominate chat startup. Tests may still inject a
// health probe to exercise broken-shim behavior for the whole catalog.
var environmentCapabilityCatalog = []struct {
	name        string
	aliases     []string
	description string
	// healthArgs is the version/identity invocation used when a custom health
	// probe is active or when requireHealthProcess is true.
	healthArgs           []string
	requireHealthProcess bool
}{
	{name: "git", description: "version control", healthArgs: []string{"--version"}},
	{name: "rg", aliases: []string{"rg.exe"}, description: "ripgrep content search", healthArgs: []string{"--version"}},
	{name: "gh", description: "GitHub CLI", healthArgs: []string{"--version"}},
	{name: "go", description: "Go toolchain", healthArgs: []string{"version"}},
	{name: "node", description: "Node.js runtime", healthArgs: []string{"--version"}},
	{name: "npm", description: "Node package manager", healthArgs: []string{"--version"}},
	// python commonly resolves to 0-byte WindowsApps aliases; stub detection
	// covers that without spawning python --version on every chat start.
	{name: "python", aliases: []string{"python3", "py"}, description: "Python runtime", healthArgs: []string{"--version"}},
	{name: "docker", description: "container CLI", healthArgs: []string{"--version"}},
	{name: "cargo", description: "Rust package manager", healthArgs: []string{"--version"}},
	{name: "make", description: "build driver", healthArgs: []string{"--version"}},
	{name: "jq", description: "JSON processor", healthArgs: []string{"--version"}},
	{name: "curl", description: "HTTP client", healthArgs: []string{"--version"}},
}

// SetEnvironmentCommandProbe overrides PATH probing. Pass nil to restore the
// default exec.LookPath implementation. Tests should restore after use.
func SetEnvironmentCommandProbe(probe EnvironmentCommandProbe) {
	environmentProbeMu.Lock()
	defer environmentProbeMu.Unlock()
	if probe == nil {
		environmentCommandProbe = exec.LookPath
		customCommandProbe = false
	} else {
		environmentCommandProbe = probe
		customCommandProbe = true
	}
	invalidateEnvironmentCapabilityCacheLocked()
}

// SetEnvironmentCommandHealthProbe overrides executable health probing. Pass
// nil to restore the default short --version/version invocation.
func SetEnvironmentCommandHealthProbe(probe EnvironmentCommandHealthProbe) {
	environmentProbeMu.Lock()
	defer environmentProbeMu.Unlock()
	if probe == nil {
		environmentHealthProbe = nil
		customHealthProbe = false
	} else {
		environmentHealthProbe = probe
		customHealthProbe = true
	}
	invalidateEnvironmentCapabilityCacheLocked()
}

// ResetEnvironmentCapabilityCache clears the measured capability snapshot so
// the next render re-probes the host.
func ResetEnvironmentCapabilityCache() {
	environmentProbeMu.Lock()
	defer environmentProbeMu.Unlock()
	invalidateEnvironmentCapabilityCacheLocked()
}

// SetEnvironmentCapabilityDiskCacheEnabled toggles durable disk cache. Tests
// disable it when injecting probes so host cache cannot leak into assertions.
func SetEnvironmentCapabilityDiskCacheEnabled(enabled bool) {
	environmentProbeMu.Lock()
	defer environmentProbeMu.Unlock()
	environmentDiskCacheEnabled = enabled
	invalidateEnvironmentCapabilityCacheLocked()
}

// SetEnvironmentCapabilityDiskCachePath overrides the disk cache file path.
// Pass empty to restore the default ~/.aicli/cache/env_capabilities.json path.
func SetEnvironmentCapabilityDiskCachePath(path string) {
	environmentProbeMu.Lock()
	defer environmentProbeMu.Unlock()
	environmentDiskCachePathOverride = strings.TrimSpace(path)
	invalidateEnvironmentCapabilityCacheLocked()
}

func invalidateEnvironmentCapabilityCacheLocked() {
	cachedEnvironmentCaps = EnvironmentCapabilityReport{}
	cachedEnvironmentAt = time.Time{}
	cachedEnvironmentValid = false
	environmentProbeInFlight = nil
}

// DetectEnvironmentCapabilities probes the current host for common developer
// tools. Results are cached in-memory and on disk so chat startup does not
// repeatedly spawn version probes.
func DetectEnvironmentCapabilities() EnvironmentCapabilityReport {
	return detectEnvironmentCapabilities(time.Now())
}

// WarmEnvironmentCapabilitiesAsync starts a background capability probe so the
// first chat system-prompt freeze can usually reuse an in-memory or disk hit.
// Safe to call multiple times; concurrent warmers share a singleflight probe.
func WarmEnvironmentCapabilitiesAsync() {
	go func() {
		_ = DetectEnvironmentCapabilities()
	}()
}

func detectEnvironmentCapabilities(now time.Time) EnvironmentCapabilityReport {
	environmentProbeMu.Lock()
	if cachedEnvironmentValid && !cachedEnvironmentAt.IsZero() && now.Sub(cachedEnvironmentAt) < environmentCapabilityTTL {
		report := cloneEnvironmentCapabilityReport(cachedEnvironmentCaps)
		environmentProbeMu.Unlock()
		return report
	}

	// Join an in-flight probe instead of launching concurrent PATH/health storms.
	if flight := environmentProbeInFlight; flight != nil {
		environmentProbeMu.Unlock()
		<-flight.done
		return cloneEnvironmentCapabilityReport(flight.report)
	}

	flight := &environmentProbeFlight{done: make(chan struct{})}
	environmentProbeInFlight = flight

	probe := environmentCommandProbe
	if probe == nil {
		probe = exec.LookPath
	}
	health := environmentHealthProbe
	if health == nil {
		health = defaultEnvironmentCommandHealthProbe
	}
	// Only the production LookPath+health pair may reuse durable disk cache.
	// Injected test probes must never read/write host cache files.
	useDiskCache := environmentDiskCacheEnabled && !customCommandProbe && !customHealthProbe
	diskPath := environmentDiskCachePathOverride
	environmentProbeMu.Unlock()

	var report EnvironmentCapabilityReport
	if useDiskCache {
		if cached, ok := loadEnvironmentCapabilityDiskCache(diskPath, now); ok {
			report = cached
		}
	}
	if len(report.Capabilities) == 0 {
		report = probeEnvironmentCapabilities(now, probe, health)
		if useDiskCache {
			saveEnvironmentCapabilityDiskCache(diskPath, report)
		}
	}

	environmentProbeMu.Lock()
	cachedEnvironmentCaps = cloneEnvironmentCapabilityReport(report)
	cachedEnvironmentAt = now
	cachedEnvironmentValid = true
	flight.report = cloneEnvironmentCapabilityReport(report)
	if environmentProbeInFlight == flight {
		environmentProbeInFlight = nil
	}
	close(flight.done)
	environmentProbeMu.Unlock()

	return cloneEnvironmentCapabilityReport(report)
}

func probeEnvironmentCapabilities(now time.Time, probe EnvironmentCommandProbe, health EnvironmentCommandHealthProbe) EnvironmentCapabilityReport {
	if probe == nil {
		probe = exec.LookPath
	}
	if health == nil {
		health = defaultEnvironmentCommandHealthProbe
	}

	// Production cold starts on large Windows PATH lists are dominated by
	// repeated LookPath directory walks. Build one name->path index, then resolve
	// the catalog against it. Custom/injected probes keep the old per-name path
	// so unit tests remain deterministic.
	var pathIndex map[string]string
	if !customCommandProbe {
		pathIndex = buildEnvironmentPathIndex()
	}

	caps := make([]EnvironmentCapability, len(environmentCapabilityCatalog))
	workers := environmentProbeConcurrency
	if workers < 1 {
		workers = 1
	}
	if workers > len(environmentCapabilityCatalog) {
		workers = len(environmentCapabilityCatalog)
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := range environmentCapabilityCatalog {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			entry := environmentCapabilityCatalog[index]
			caps[index] = probeOneEnvironmentCapability(entry.name, entry.aliases, entry.requireHealthProcess, probe, health, pathIndex)
		}(i)
	}
	wg.Wait()

	return EnvironmentCapabilityReport{
		ProbedAt:     now,
		Capabilities: caps,
	}
}

func probeOneEnvironmentCapability(name string, aliases []string, requireHealthProcess bool, probe EnvironmentCommandProbe, health EnvironmentCommandHealthProbe, pathIndex map[string]string) EnvironmentCapability {
	names := make([]string, 0, 1+len(aliases))
	names = append(names, name)
	names = append(names, aliases...)
	cap := EnvironmentCapability{Name: name}

	var (
		foundPath   string
		foundNote   string
		foundBroken bool
	)
	// Prefer healthy candidates. If every candidate is a stub/broken shim,
	// keep the first resolved path + note so planners see "present but unusable".
	for _, candidate := range names {
		path := resolveEnvironmentCommandPath(candidate, probe, pathIndex)
		if strings.TrimSpace(path) == "" {
			continue
		}
		if isUnusablePathStub(path) {
			if foundPath == "" {
				foundPath = path
				foundNote = unusablePathStubNote(path)
				foundBroken = true
			}
			continue
		}
		// Fast path: most developer tools only need LookPath + stub rejection.
		// Process health is reserved for tools that commonly have false PATH hits.
		if !requireHealthProcess && !customHealthProbeActive(health) {
			cap.Available = true
			cap.Path = path
			foundBroken = false
			break
		}
		ok, detail := health(name, path)
		if !ok {
			if foundPath == "" {
				foundPath = path
				if strings.TrimSpace(detail) != "" {
					foundNote = detail
				} else {
					foundNote = "resolved on PATH but failed health check"
				}
				foundBroken = true
			}
			continue
		}
		cap.Available = true
		cap.Path = path
		if strings.TrimSpace(detail) != "" {
			cap.Note = detail
		}
		foundBroken = false
		break
	}
	if !cap.Available && foundBroken {
		cap.Path = foundPath
		cap.Note = foundNote
	}
	return cap
}

func resolveEnvironmentCommandPath(name string, probe EnvironmentCommandProbe, pathIndex map[string]string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if pathIndex != nil {
		if path := pathIndex[normalizeEnvironmentCommandKey(name)]; strings.TrimSpace(path) != "" {
			return path
		}
		// Indexed miss: do not fall back to LookPath. The single-pass index is
		// the authoritative production resolver for this probe wave.
		return ""
	}
	if probe == nil {
		return ""
	}
	path, err := probe(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(path)
}

func customHealthProbeActive(health EnvironmentCommandHealthProbe) bool {
	// When tests inject a health probe, always honor it so unit coverage of
	// health-failure paths remains valid for the whole catalog.
	return health != nil && customHealthProbe
}

// buildEnvironmentPathIndex walks PATH once and records the first executable
// match for each basename (case-insensitive on Windows). This replaces N full
// LookPath scans with one directory walk for production cold probes.
func buildEnvironmentPathIndex() map[string]string {
	wanted := make(map[string]struct{}, len(environmentCapabilityCatalog)*2)
	for _, entry := range environmentCapabilityCatalog {
		wanted[normalizeEnvironmentCommandKey(entry.name)] = struct{}{}
		for _, alias := range entry.aliases {
			wanted[normalizeEnvironmentCommandKey(alias)] = struct{}{}
		}
	}

	index := make(map[string]string, len(wanted))
	pathEnv := os.Getenv("PATH")
	if strings.TrimSpace(pathEnv) == "" {
		return index
	}
	exts := environmentExecutableExtensions()
	for _, dir := range filepath.SplitList(pathEnv) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			base := entry.Name()
			baseKey := normalizeEnvironmentCommandKey(base)
			stemKey := normalizeEnvironmentCommandKey(strings.TrimSuffix(base, filepath.Ext(base)))
			// Match either exact basename (rg.exe) or stem (rg) when the file
			// has an executable extension on this platform.
			var keys []string
			if _, ok := wanted[baseKey]; ok {
				keys = append(keys, baseKey)
			}
			if stemKey != baseKey && hasEnvironmentExecutableExtension(base, exts) {
				if _, ok := wanted[stemKey]; ok {
					keys = append(keys, stemKey)
				}
			}
			if len(keys) == 0 {
				continue
			}
			full := filepath.Join(dir, base)
			for _, key := range keys {
				if _, exists := index[key]; exists {
					continue
				}
				index[key] = full
			}
			if len(index) == len(wanted) {
				return index
			}
		}
	}
	return index
}

func environmentExecutableExtensions() []string {
	if runtime.GOOS != "windows" {
		return []string{""}
	}
	raw := strings.TrimSpace(os.Getenv("PATHEXT"))
	if raw == "" {
		return []string{".com", ".exe", ".bat", ".cmd"}
	}
	parts := strings.Split(raw, ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.HasPrefix(part, ".") {
			part = "." + part
		}
		out = append(out, strings.ToLower(part))
	}
	if len(out) == 0 {
		return []string{".com", ".exe", ".bat", ".cmd"}
	}
	return out
}

func hasEnvironmentExecutableExtension(name string, exts []string) bool {
	if runtime.GOOS != "windows" {
		return true
	}
	ext := strings.ToLower(filepath.Ext(name))
	for _, candidate := range exts {
		if ext == strings.ToLower(candidate) {
			return true
		}
	}
	return false
}

func normalizeEnvironmentCommandKey(name string) string {
	name = strings.TrimSpace(name)
	if runtime.GOOS == "windows" {
		return strings.ToLower(name)
	}
	return name
}

func cloneEnvironmentCapabilityReport(report EnvironmentCapabilityReport) EnvironmentCapabilityReport {
	out := EnvironmentCapabilityReport{
		ProbedAt: report.ProbedAt,
	}
	if len(report.Capabilities) == 0 {
		return out
	}
	out.Capabilities = append([]EnvironmentCapability(nil), report.Capabilities...)
	return out
}

// AvailableEnvironmentCommands returns measured available command names.
func AvailableEnvironmentCommands() []string {
	return availableEnvironmentCommandsFromReport(DetectEnvironmentCapabilities())
}

func availableEnvironmentCommandsFromReport(report EnvironmentCapabilityReport) []string {
	names := make([]string, 0, len(report.Capabilities))
	for _, cap := range report.Capabilities {
		if cap.Available {
			names = append(names, cap.Name)
		}
	}
	return names
}

// UnavailableEnvironmentCommands returns measured unavailable command names.
func UnavailableEnvironmentCommands() []string {
	return unavailableEnvironmentCommandsFromReport(DetectEnvironmentCapabilities())
}

func unavailableEnvironmentCommandsFromReport(report EnvironmentCapabilityReport) []string {
	names := make([]string, 0, len(report.Capabilities))
	for _, cap := range report.Capabilities {
		if !cap.Available {
			names = append(names, cap.Name)
		}
	}
	return names
}

// RenderEnvironmentCapabilityGuidance renders a short, measured capability
// preview. Only tools proven available via PATH + health probe are recommended;
// missing/broken tools are called out so the model does not invent presence.
func RenderEnvironmentCapabilityGuidance() string {
	return renderEnvironmentCapabilityGuidanceFromReport(DetectEnvironmentCapabilities())
}

func renderEnvironmentCapabilityGuidanceFromReport(report EnvironmentCapabilityReport) string {
	available := make([]string, 0, len(report.Capabilities))
	unavailable := make([]string, 0, len(report.Capabilities))
	brokenNotes := make([]string, 0, 2)
	for _, cap := range report.Capabilities {
		if cap.Available {
			available = append(available, cap.Name)
			continue
		}
		// Keep unavailable noise low: only surface high-value planner tools.
		switch cap.Name {
		case "git", "rg", "gh", "go", "node", "python", "docker", "cargo":
			unavailable = append(unavailable, cap.Name)
			if note := strings.TrimSpace(cap.Note); note != "" && (cap.Name == "python" || cap.Name == "node") {
				brokenNotes = append(brokenNotes, fmt.Sprintf("%s: %s", cap.Name, note))
			}
		}
	}
	sort.Strings(available)
	sort.Strings(unavailable)

	if len(available) == 0 && len(unavailable) == 0 {
		return ""
	}

	lines := []string{
		"Environment capabilities (measured via PATH + lightweight health probe; re-check if PATH changes):",
	}
	if len(available) > 0 {
		lines = append(lines, fmt.Sprintf("- Available: %s.", strings.Join(available, ", ")))
		lines = append(lines, "- Prefer these shell tools only when a dedicated toolkit tool is not a better fit (builds/tests/package managers/git stay on shell; code search prefers toolkit `grep`/`glob`/`ls`/`view`).")
	} else {
		lines = append(lines, "- Available: none of the common developer tools were found healthy on PATH.")
	}
	if len(unavailable) > 0 {
		lines = append(lines, fmt.Sprintf("- Not found or unhealthy on PATH: %s. Do not assume these commands exist; install them, fix broken shims/stubs, or use toolkit alternatives.", strings.Join(unavailable, ", ")))
	}
	for _, note := range brokenNotes {
		lines = append(lines, fmt.Sprintf("- Unusable candidate detail: %s.", note))
	}
	if containsString(available, "git") {
		lines = append(lines, "- git is available: use `git status`/`git diff`/`git log` for repo inspection. If a path is gitignored, use `git check-ignore -v <path>` or `git add -f` only when force-adding is intentional; do not retry the same ignored path unchanged.")
	}
	if containsString(available, "rg") {
		lines = append(lines, "- rg is available in shell, but toolkit `grep` remains preferred for code search (structured args, empty-result contract, fewer quoting pitfalls).")
	}
	if containsString(available, "python") {
		lines = append(lines, "- python is available: prefer `python -c` for short checks; avoid bash heredoc on Windows PowerShell/cmd.")
	}
	if containsString(unavailable, "python") {
		lines = append(lines, "- python is not usable on PATH: install a real Python runtime, fix broken shims/aliases, and ensure the healthy executable is first on PATH. Do not call `python` until a measured healthy candidate exists.")
	}
	if containsString(unavailable, "gh") {
		lines = append(lines, "- gh is not available: use `git` remote/web URLs or the GitHub API via curl/fetch only when needed; do not call `gh`.")
	}
	return strings.Join(lines, "\n")
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// isUnusablePathStub detects known unusable PATH placeholders that LookPath can
// still resolve. This is a portable heuristic based on path shape + file size,
// not a host-specific allow/deny list.
func isUnusablePathStub(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	cleaned := strings.ToLower(filepath.Clean(path))
	// Windows App Execution Alias placeholders live under *\WindowsApps\*.
	// On non-Windows hosts this path pattern is simply never matched.
	if !isWindowsAppsPath(cleaned) {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		// Unreadable placeholder path is not a trustworthy runtime.
		return true
	}
	if info.IsDir() {
		return true
	}
	// Real installs are non-zero; App Execution Alias stubs are typically 0 bytes.
	return info.Size() == 0
}

func isWindowsAppsPath(cleanedLowerPath string) bool {
	sep := string(filepath.Separator)
	return strings.Contains(cleanedLowerPath, sep+"windowsapps"+sep) ||
		strings.Contains(cleanedLowerPath, `\windowsapps\`) ||
		strings.Contains(cleanedLowerPath, `/windowsapps/`)
}

func unusablePathStubNote(path string) string {
	cleaned := strings.ToLower(filepath.Clean(strings.TrimSpace(path)))
	if isWindowsAppsPath(cleaned) {
		return "unusable PATH placeholder (0-byte WindowsApps app-execution alias)"
	}
	return "unusable PATH placeholder"
}

func defaultEnvironmentCommandHealthProbe(name, path string) (bool, string) {
	name = strings.TrimSpace(name)
	path = strings.TrimSpace(path)
	if path == "" {
		return false, "empty executable path"
	}
	if isUnusablePathStub(path) {
		return false, unusablePathStubNote(path)
	}

	args := healthArgsForCommand(name)
	if len(args) == 0 {
		// PATH presence is enough when no health invocation is defined.
		return true, ""
	}

	// One quick retry absorbs transient Windows process-create / antivirus races
	// that show up under concurrent cold probes but not sequential checks.
	var lastDetail string
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(25 * time.Millisecond)
		}
		ctx, cancel := context.WithTimeout(context.Background(), environmentHealthTimeout)
		cmd := exec.CommandContext(ctx, path, args...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		// Avoid inheriting interactive prompts / pager behavior.
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "PAGER=cat", "CI=1")
		err := cmd.Run()
		cancel()
		if err == nil {
			return true, ""
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail == "" {
			detail = err.Error()
		}
		lastDetail = compactProbeDetail(detail)
	}
	return false, fmt.Sprintf("health check failed (%s %s): %s", filepath.Base(path), strings.Join(args, " "), lastDetail)
}

func healthArgsForCommand(name string) []string {
	for _, entry := range environmentCapabilityCatalog {
		if entry.name == name {
			if len(entry.healthArgs) == 0 {
				return nil
			}
			out := make([]string, len(entry.healthArgs))
			copy(out, entry.healthArgs)
			return out
		}
	}
	return []string{"--version"}
}

func compactProbeDetail(detail string) string {
	detail = strings.ReplaceAll(detail, "\r\n", "\n")
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return ""
	}
	if idx := strings.IndexByte(detail, '\n'); idx >= 0 {
		detail = strings.TrimSpace(detail[:idx])
	}
	const max = 160
	if len(detail) > max {
		return detail[:max] + "..."
	}
	return detail
}

func environmentCapabilityFingerprint() string {
	names := make([]string, 0, len(environmentCapabilityCatalog))
	for _, entry := range environmentCapabilityCatalog {
		names = append(names, entry.name)
		if len(entry.aliases) > 0 {
			names = append(names, entry.aliases...)
		}
	}
	payload := strings.Join([]string{
		fmt.Sprintf("v=%d", environmentCapabilityCacheVersion),
		"os=" + runtime.GOOS,
		"arch=" + runtime.GOARCH,
		"path=" + os.Getenv("PATH"),
		"path_ext=" + os.Getenv("PATHEXT"),
		"catalog=" + strings.Join(names, ","),
	}, "\n")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func defaultEnvironmentCapabilityDiskCachePath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return filepath.Join(".", ".aicli", "cache", "env_capabilities.json")
	}
	return filepath.Join(homeDir, ".aicli", "cache", "env_capabilities.json")
}

func resolveEnvironmentCapabilityDiskCachePath(override string) string {
	if path := strings.TrimSpace(override); path != "" {
		return path
	}
	return defaultEnvironmentCapabilityDiskCachePath()
}

func loadEnvironmentCapabilityDiskCache(pathOverride string, now time.Time) (EnvironmentCapabilityReport, bool) {
	path := resolveEnvironmentCapabilityDiskCachePath(pathOverride)
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return EnvironmentCapabilityReport{}, false
	}
	var cached environmentCapabilityDiskCache
	if err := json.Unmarshal(raw, &cached); err != nil {
		return EnvironmentCapabilityReport{}, false
	}
	if cached.Version != environmentCapabilityCacheVersion {
		return EnvironmentCapabilityReport{}, false
	}
	if strings.TrimSpace(cached.Fingerprint) != environmentCapabilityFingerprint() {
		return EnvironmentCapabilityReport{}, false
	}
	if cached.ProbedAt.IsZero() || now.Sub(cached.ProbedAt) > environmentDiskCacheTTL {
		return EnvironmentCapabilityReport{}, false
	}
	if len(cached.Capabilities) == 0 {
		return EnvironmentCapabilityReport{}, false
	}
	// Cheap path existence check: available tools must still resolve to a file.
	// Also reject snapshots that recorded health-check failures: those are often
	// transient under concurrent cold process creation and must not freeze for
	// the full disk TTL. Keep the rest of the cache usable for PATH-only tools.
	for _, cap := range cached.Capabilities {
		if strings.Contains(strings.ToLower(cap.Note), "health check failed") {
			return EnvironmentCapabilityReport{}, false
		}
		if !cap.Available {
			continue
		}
		if strings.TrimSpace(cap.Path) == "" {
			return EnvironmentCapabilityReport{}, false
		}
		info, err := os.Stat(cap.Path)
		if err != nil || info.IsDir() {
			return EnvironmentCapabilityReport{}, false
		}
		if isUnusablePathStub(cap.Path) {
			return EnvironmentCapabilityReport{}, false
		}
	}
	return EnvironmentCapabilityReport{
		ProbedAt:     cached.ProbedAt,
		Capabilities: append([]EnvironmentCapability(nil), cached.Capabilities...),
	}, true
}

func saveEnvironmentCapabilityDiskCache(pathOverride string, report EnvironmentCapabilityReport) {
	path := resolveEnvironmentCapabilityDiskCachePath(pathOverride)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	payload := environmentCapabilityDiskCache{
		Version:      environmentCapabilityCacheVersion,
		Fingerprint:  environmentCapabilityFingerprint(),
		ProbedAt:     report.ProbedAt,
		Capabilities: append([]EnvironmentCapability(nil), report.Capabilities...),
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}
