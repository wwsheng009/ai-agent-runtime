package prompt

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

var (
	environmentProbeMu       sync.Mutex
	environmentCommandProbe  EnvironmentCommandProbe = exec.LookPath
	environmentHealthProbe   EnvironmentCommandHealthProbe
	environmentCapabilityTTL                         = 30 * time.Second
	environmentHealthTimeout                         = 1200 * time.Millisecond
	cachedEnvironmentCaps    EnvironmentCapabilityReport
	cachedEnvironmentAt      time.Time
	cachedEnvironmentValid   bool
)

// environmentCapabilityCatalog is the ordered set of common developer tools we
// surface when present. Only tools that actually resolve via PATH and pass a
// lightweight health check are listed as available; missing/broken tools may
// appear in the unavailable list when useful for planning.
var environmentCapabilityCatalog = []struct {
	name        string
	aliases     []string
	description string
	// healthArgs is the version/identity invocation used after LookPath.
	// Empty means "PATH presence only" (rare; prefer an explicit probe).
	healthArgs []string
}{
	{name: "git", description: "version control", healthArgs: []string{"--version"}},
	{name: "rg", aliases: []string{"rg.exe"}, description: "ripgrep content search", healthArgs: []string{"--version"}},
	{name: "gh", description: "GitHub CLI", healthArgs: []string{"--version"}},
	{name: "go", description: "Go toolchain", healthArgs: []string{"version"}},
	{name: "node", description: "Node.js runtime", healthArgs: []string{"--version"}},
	{name: "npm", description: "Node package manager", healthArgs: []string{"--version"}},
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
	} else {
		environmentCommandProbe = probe
	}
	invalidateEnvironmentCapabilityCacheLocked()
}

// SetEnvironmentCommandHealthProbe overrides executable health probing. Pass
// nil to restore the default short --version/version invocation.
func SetEnvironmentCommandHealthProbe(probe EnvironmentCommandHealthProbe) {
	environmentProbeMu.Lock()
	defer environmentProbeMu.Unlock()
	environmentHealthProbe = probe
	invalidateEnvironmentCapabilityCacheLocked()
}

// ResetEnvironmentCapabilityCache clears the measured capability snapshot so
// the next render re-probes the host.
func ResetEnvironmentCapabilityCache() {
	environmentProbeMu.Lock()
	defer environmentProbeMu.Unlock()
	invalidateEnvironmentCapabilityCacheLocked()
}

func invalidateEnvironmentCapabilityCacheLocked() {
	cachedEnvironmentCaps = EnvironmentCapabilityReport{}
	cachedEnvironmentAt = time.Time{}
	cachedEnvironmentValid = false
}

// DetectEnvironmentCapabilities probes the current host for common developer
// tools. Results are cached briefly to avoid repeated PATH scans per turn.
func DetectEnvironmentCapabilities() EnvironmentCapabilityReport {
	return detectEnvironmentCapabilities(time.Now())
}

func detectEnvironmentCapabilities(now time.Time) EnvironmentCapabilityReport {
	environmentProbeMu.Lock()
	defer environmentProbeMu.Unlock()

	if cachedEnvironmentValid && !cachedEnvironmentAt.IsZero() && now.Sub(cachedEnvironmentAt) < environmentCapabilityTTL {
		return cloneEnvironmentCapabilityReport(cachedEnvironmentCaps)
	}

	probe := environmentCommandProbe
	if probe == nil {
		probe = exec.LookPath
	}
	health := environmentHealthProbe
	if health == nil {
		health = defaultEnvironmentCommandHealthProbe
	}

	report := EnvironmentCapabilityReport{
		ProbedAt:     now,
		Capabilities: make([]EnvironmentCapability, 0, len(environmentCapabilityCatalog)),
	}
	for _, entry := range environmentCapabilityCatalog {
		names := make([]string, 0, 1+len(entry.aliases))
		names = append(names, entry.name)
		names = append(names, entry.aliases...)
		cap := EnvironmentCapability{Name: entry.name}

		var (
			foundPath   string
			foundNote   string
			foundBroken bool
		)
		// Prefer healthy candidates. If every candidate is a stub/broken shim,
		// keep the first resolved path + note so planners see "present but unusable".
		for _, candidate := range names {
			path, err := probe(candidate)
			if err != nil || strings.TrimSpace(path) == "" {
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
			ok, detail := health(entry.name, path)
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
		report.Capabilities = append(report.Capabilities, cap)
	}

	cachedEnvironmentCaps = cloneEnvironmentCapabilityReport(report)
	cachedEnvironmentAt = now
	cachedEnvironmentValid = true
	return cloneEnvironmentCapabilityReport(report)
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
	report := DetectEnvironmentCapabilities()
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
	report := DetectEnvironmentCapabilities()
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
	report := DetectEnvironmentCapabilities()
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

	ctx, cancel := context.WithTimeout(context.Background(), environmentHealthTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Avoid inheriting interactive prompts / pager behavior.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "PAGER=cat", "CI=1")
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail == "" {
			detail = err.Error()
		}
		detail = compactProbeDetail(detail)
		return false, fmt.Sprintf("health check failed (%s %s): %s", filepath.Base(path), strings.Join(args, " "), detail)
	}
	return true, ""
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
