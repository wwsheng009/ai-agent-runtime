package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Sentinel printed by every probe. A candidate is usable only when it starts,
// runs, exits 0 AND prints this token: the WSL launcher at
// C:\Windows\System32\bash.exe exits non-zero with an empty stdout when no
// distribution is installed, and exec.LookPath happily selects it.
//
// 实测见 docs/plan/windows-posix-shell-detection-and-config-plan-20261104.md §1.3
const shellProbeSentinel = "__aicli_shell_ok__"

// Candidate status values used by doctor, the API and the TUI.
const (
	ShellCandidatePass = "pass"
	ShellCandidateFail = "fail"
	ShellCandidateSkip = "skip"
)

// Shell resolution sources.
const (
	ShellSourceConfigPath = "config-path"
	ShellSourcePrefer     = "prefer"
	ShellSourceAutoDetect = "autodetect"
	ShellSourceFallback   = "fallback"
	ShellSourceLegacy     = "legacy"
)

// ErrShellUnavailable is returned when every candidate failed the probe and
// strictProbe is on. Callers that must keep running fall back to
// DefaultUserShell, which is the documented internal fallback.
var ErrShellUnavailable = errors.New("no usable shell passed the probe")

// ShellCandidateReport is one row of the resolution matrix.
type ShellCandidateReport struct {
	ID      string       `json:"id"`
	Status  string       `json:"status"`
	Path    string       `json:"path,omitempty"`
	Reason  string       `json:"reason,omitempty"`
	Version string       `json:"version,omitempty"`
	Dialect ShellDialect `json:"dialect,omitempty"`
	ProbeMS int64        `json:"probe_ms,omitempty"`
	// Guard marks a candidate skipped by a known-trap rule (the WSL stub).
	Guard bool `json:"guard,omitempty"`
}

// ShellResolution is the full outcome of one resolution pass: the selected
// shell plus every candidate that was considered, in order.
type ShellResolution struct {
	Shell      Shell                  `json:"shell"`
	Candidates []ShellCandidateReport `json:"candidates"`
	Strict     bool                   `json:"strict"`
	ProbeMS    int64                  `json:"probe_ms"`
	ResolvedAt time.Time              `json:"resolved_at"`
	Configured bool                   `json:"configured"`
}

// ID returns the selected candidate ID.
func (r *ShellResolution) ID() string {
	if r == nil {
		return ""
	}
	return r.Shell.ID
}

// candidateSpec is one planned candidate before location/probing.
type candidateSpec struct {
	id     string
	typ    ShellType
	prefix []string
	source string
	// skipReason short-circuits a candidate without touching the filesystem.
	skipReason string
}

// resolvePlan turns ShellConfig into an ordered candidate plan.
//
// Order of authority (plan §5.1):
//
//	explicit path > prefer chain > built-in default chain
func resolvePlan(cfg ShellConfig) []candidateSpec {
	if path := strings.TrimSpace(cfg.Path); path != "" {
		return []candidateSpec{{
			id:     shellIDForPath(path),
			typ:    shellTypeForPath(path),
			source: ShellSourceConfigPath,
		}}
	}

	prefer := cfg.NormalizedPrefer()
	if len(prefer) > 0 {
		plan := make([]candidateSpec, 0, len(prefer))
		for _, id := range prefer {
			plan = append(plan, specForID(id, ShellSourcePrefer))
		}
		return plan
	}

	if !cfg.ShellAutoDetect() {
		// Locked-down environments: no implicit chain at all.
		return nil
	}
	return defaultCandidatePlan()
}

// defaultCandidatePlan is the built-in chain. `wsl` is intentionally absent:
// it is only tried when the user names it in `prefer` (plan §3.3).
func defaultCandidatePlan() []candidateSpec {
	if runtime.GOOS != "windows" {
		return []candidateSpec{
			specForID(ShellIDSh, ShellSourceAutoDetect),
			specForID(ShellIDBash, ShellSourceAutoDetect),
			specForID(ShellIDZsh, ShellSourceAutoDetect),
		}
	}
	ids := []string{ShellIDGitBash, ShellIDMSYSBash, ShellIDBusybox, ShellIDPwsh, ShellIDPowerShell, ShellIDCmd}
	plan := make([]candidateSpec, 0, len(ids))
	for _, id := range ids {
		plan = append(plan, specForID(id, ShellSourceAutoDetect))
	}
	return plan
}

// specForID maps a stable ID to its shell type (and busybox applet prefix).
func specForID(id, source string) candidateSpec {
	id = strings.ToLower(strings.TrimSpace(id))
	switch id {
	case ShellIDGitBash, ShellIDMSYSBash, ShellIDWSL, ShellIDBash:
		return candidateSpec{id: id, typ: ShellTypeBash, source: source}
	case ShellIDZsh:
		return candidateSpec{id: id, typ: ShellTypeZsh, source: source}
	case ShellIDSh:
		return candidateSpec{id: id, typ: ShellTypeSh, source: source}
	case ShellIDBusybox:
		// busybox.exe exposes applets through argv[1]; `sh` selects the shell.
		return candidateSpec{id: id, typ: ShellTypeSh, prefix: []string{"sh"}, source: source}
	case ShellIDPwsh:
		return candidateSpec{id: id, typ: ShellTypePwsh, source: source}
	case ShellIDPowerShell:
		return candidateSpec{id: id, typ: ShellTypePowerShell, source: source}
	case ShellIDCmd:
		return candidateSpec{id: id, typ: ShellTypeCmd, source: source}
	default:
		return candidateSpec{id: id, typ: ShellTypeSh, source: source, skipReason: "unknown candidate id"}
	}
}

func shellIDForPath(path string) string {
	base := strings.ToLower(strings.TrimSuffix(shellBaseName(path), ".exe"))
	switch base {
	case "bash":
		if runtime.GOOS == "windows" && isWSLBashPath(path) {
			return ShellIDWSL
		}
		return ShellIDBash
	case "sh":
		return ShellIDSh
	case "zsh":
		return ShellIDZsh
	case "busybox":
		return ShellIDBusybox
	case "pwsh":
		return ShellIDPwsh
	case "powershell":
		return ShellIDPowerShell
	case "cmd":
		return ShellIDCmd
	default:
		return ""
	}
}

func shellTypeForPath(path string) ShellType {
	base := strings.ToLower(strings.TrimSuffix(shellBaseName(path), ".exe"))
	switch base {
	case "bash":
		return ShellTypeBash
	case "zsh":
		return ShellTypeZsh
	case "sh", "busybox":
		return ShellTypeSh
	case "pwsh":
		return ShellTypePwsh
	case "powershell":
		return ShellTypePowerShell
	case "cmd":
		return ShellTypeCmd
	default:
		return ShellTypeSh
	}
}

// isWSLBashPath reports whether path is one of the two WSL launchers. Both
// shapes exist on real machines (system stub + WindowsApps alias).
func isWSLBashPath(path string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(path), `\`, "/"))
	if normalized == "" {
		return false
	}
	if strings.Contains(normalized, "/windowsapps/bash.exe") {
		return true
	}
	if strings.HasSuffix(normalized, "/system32/bash.exe") {
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Location
// ---------------------------------------------------------------------------

// locateShellCandidate resolves the binary for one candidate ID and returns a
// human-readable reason when it is absent.
func locateShellCandidate(id string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case ShellIDGitBash:
		return locateGitBash()
	case ShellIDMSYSBash:
		return locateMSYSBash()
	case ShellIDBusybox:
		return locateBusybox()
	case ShellIDWSL:
		return locateWSLBash()
	case ShellIDPwsh:
		if path, err := exec.LookPath("pwsh"); err == nil {
			return path, ""
		}
		return "", "not found on PATH"
	case ShellIDPowerShell:
		return locateWindowsPowerShell()
	case ShellIDCmd:
		return locateCmd()
	case ShellIDBash:
		if path, err := exec.LookPath("bash"); err == nil {
			// Never hand back the WSL stub under the `bash` ID: it would skip
			// the `wsl` guard and silently do nothing.
			if runtime.GOOS == "windows" && isWSLBashPath(path) {
				return "", "PATH bash.exe is the WSL launcher; enable the `wsl` candidate explicitly"
			}
			return path, ""
		}
		for _, candidate := range []string{"/bin/bash", "/usr/bin/bash", "/usr/local/bin/bash"} {
			if isExecutableFile(candidate) {
				return candidate, ""
			}
		}
		return "", "not found on PATH"
	case ShellIDZsh:
		if path, err := exec.LookPath("zsh"); err == nil {
			return path, ""
		}
		for _, candidate := range []string{"/bin/zsh", "/usr/bin/zsh"} {
			if isExecutableFile(candidate) {
				return candidate, ""
			}
		}
		return "", "not found on PATH"
	case ShellIDSh:
		if path, err := exec.LookPath("sh"); err == nil {
			return path, ""
		}
		if isExecutableFile("/bin/sh") {
			return "/bin/sh", ""
		}
		return "", "not found on PATH"
	default:
		return "", "unknown candidate id"
	}
}

func locateGitBash() (string, string) {
	if runtime.GOOS != "windows" {
		return "", "Git Bash is Windows-only"
	}
	// bin\bash.exe (the MSYS launcher) is preferred over usr\bin\bash.exe: it
	// initialises /usr/bin and MSYSTEM so GNU tools are on PATH (plan §5.4).
	var roots []string
	for _, envKey := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
		root := strings.TrimSpace(os.Getenv(envKey))
		if root == "" {
			continue
		}
		switch envKey {
		case "LOCALAPPDATA":
			roots = append(roots, filepath.Join(root, "Programs", "Git"))
		default:
			roots = append(roots, filepath.Join(root, "Git"))
		}
	}
	if execPath, err := gitExecPath(); err == nil && execPath != "" {
		if root := gitRootFromExecPath(execPath); root != "" {
			roots = append(roots, root)
		}
	}
	for _, root := range roots {
		candidate := filepath.Join(root, "bin", "bash.exe")
		if isExecutableFile(candidate) {
			return candidate, ""
		}
	}
	if path, err := exec.LookPath("bash.exe"); err == nil && !isWSLBashPath(path) {
		return path, ""
	}
	if path, err := exec.LookPath("bash"); err == nil && !isWSLBashPath(path) {
		return path, ""
	}
	return "", "Git for Windows not installed (not in Program Files, LOCALAPPDATA or PATH)"
}

func locateMSYSBash() (string, string) {
	if runtime.GOOS != "windows" {
		return "", "MSYS2 is Windows-only"
	}
	var candidates []string
	if root := strings.TrimSpace(os.Getenv("MSYS2_ROOT")); root != "" {
		candidates = append(candidates, filepath.Join(root, "usr", "bin", "bash.exe"))
	}
	candidates = append(candidates,
		`C:\msys64\usr\bin\bash.exe`,
		`C:\msys32\usr\bin\bash.exe`,
	)
	for _, candidate := range candidates {
		if isExecutableFile(candidate) {
			return candidate, ""
		}
	}
	return "", "MSYS2 not installed"
}

func locateBusybox() (string, string) {
	if execPath, err := os.Executable(); err == nil {
		dir := filepath.Dir(execPath)
		for _, candidate := range []string{
			filepath.Join(dir, "vendor", "busybox", "busybox.exe"),
			filepath.Join(dir, "busybox.exe"),
		} {
			if isExecutableFile(candidate) {
				return candidate, ""
			}
		}
	}
	for _, name := range []string{"busybox.exe", "busybox"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, ""
		}
	}
	return "", "bundled binary absent (vendor/busybox/busybox.exe)"
}

func locateWSLBash() (string, string) {
	if runtime.GOOS != "windows" {
		return "", "WSL is Windows-only"
	}
	var candidates []string
	if systemRoot := strings.TrimSpace(os.Getenv("SystemRoot")); systemRoot != "" {
		candidates = append(candidates, filepath.Join(systemRoot, "System32", "bash.exe"))
	}
	candidates = append(candidates, `C:\Windows\System32\bash.exe`)
	if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
		candidates = append(candidates, filepath.Join(localAppData, "Microsoft", "WindowsApps", "bash.exe"))
	}
	for _, candidate := range candidates {
		if isExecutableFile(candidate) {
			return candidate, ""
		}
	}
	return "", "WSL launcher not found"
}

func locateWindowsPowerShell() (string, string) {
	if runtime.GOOS == "windows" {
		if systemRoot := strings.TrimSpace(os.Getenv("SystemRoot")); systemRoot != "" {
			candidate := filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
			if isExecutableFile(candidate) {
				return candidate, ""
			}
		}
	}
	if path, err := exec.LookPath("powershell"); err == nil {
		return path, ""
	}
	return "", "Windows PowerShell not installed"
}

func locateCmd() (string, string) {
	if runtime.GOOS == "windows" {
		if systemRoot := strings.TrimSpace(os.Getenv("SystemRoot")); systemRoot != "" {
			candidate := filepath.Join(systemRoot, "System32", "cmd.exe")
			if isExecutableFile(candidate) {
				return candidate, ""
			}
		}
	}
	if path, err := exec.LookPath("cmd"); err == nil {
		return path, ""
	}
	return "", "cmd.exe not found"
}

// gitExecPath asks git where its helper programs live. A portable Git
// distribution that is not in the standard Program Files locations is still
// findable this way.
func gitExecPath() (string, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", err
	}
	out, err := exec.Command(gitPath, "--exec-path").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitRootFromExecPath converts `<root>/mingw64/libexec/git-core` (or the
// 32-bit / plain lib directories) into `<root>`.
func gitRootFromExecPath(execPath string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(execPath), `\`, "/")
	normalized = strings.TrimSuffix(normalized, "/")
	for _, suffix := range []string{
		"/mingw64/libexec/git-core",
		"/mingw32/libexec/git-core",
		"/libexec/git-core",
		"/mingw64/bin",
		"/mingw32/bin",
		"/bin",
	} {
		if strings.HasSuffix(strings.ToLower(normalized), strings.ToLower(suffix)) {
			return filepath.FromSlash(normalized[:len(normalized)-len(suffix)])
		}
	}
	return ""
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Probing
// ---------------------------------------------------------------------------

// shellProbeResult is the outcome of one sentinel probe.
type shellProbeResult struct {
	Version string
	Elapsed time.Duration
	Err     error
}

// probeShell runs the sentinel script and validates stdout, not just the exit
// code: the WSL stub has been observed returning exit 0 with empty output as
// well as exit 1 (plan §5.2).
func probeShell(ctx context.Context, shell Shell, timeout time.Duration) shellProbeResult {
	if timeout <= 0 {
		timeout = DefaultShellProbeTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	argv := shell.probeArgv()
	if len(argv) == 0 {
		return shellProbeResult{Err: fmt.Errorf("no probe command for shell type %q", shell.Type)}
	}
	cmd := exec.CommandContext(probeCtx, argv[0], argv[1:]...)
	cmd.Env = os.Environ()
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	started := time.Now()
	err := cmd.Run()
	elapsed := time.Since(started)

	if probeCtx.Err() == context.DeadlineExceeded {
		return shellProbeResult{Elapsed: elapsed, Err: fmt.Errorf("probe timeout after %s", timeout)}
	}
	if err != nil {
		return shellProbeResult{Elapsed: elapsed, Err: fmt.Errorf("%s", probeReason(err, stderr.String()))}
	}
	out := strings.TrimSpace(stdout.String())
	if !strings.Contains(out, shellProbeSentinel) {
		// Empty output on exit 0 is the failure mode the sentinel exists for.
		reason := "probe produced no sentinel output"
		if out != "" {
			reason = "probe output missing sentinel: " + truncateProbeText(out)
		}
		return shellProbeResult{Elapsed: elapsed, Err: fmt.Errorf("%s", reason)}
	}
	return shellProbeResult{Version: parseProbeVersion(out), Elapsed: elapsed}
}

// probeArgv builds the sentinel command for one shell. The script is a
// compile-time constant: no config value is ever interpolated into it.
func (s Shell) probeArgv() []string {
	switch s.Type {
	case ShellTypeBash:
		return s.withPrefix("--noprofile", "--norc", "-c", shellProbeScriptPosix)
	case ShellTypeZsh, ShellTypeSh:
		return s.withPrefix("-c", shellProbeScriptPosix)
	case ShellTypePowerShell, ShellTypePwsh:
		return s.withPrefix("-NoProfile", "-NonInteractive", "-Command", PowerShellCommandPrefix+shellProbeScriptPowerShell)
	case ShellTypeCmd:
		return s.withPrefix("/c", "echo "+shellProbeSentinel)
	default:
		return nil
	}
}

const shellProbeScriptPosix = "printf '%s\\n' " + shellProbeSentinel + "; printf '%s' \"${BASH_VERSION:-}\""

const shellProbeScriptPowerShell = "Write-Output " + shellProbeSentinel + "; Write-Output $PSVersionTable.PSVersion.ToString()"

// parseProbeVersion returns the line after the sentinel, trimmed. Empty when
// the shell does not report a version.
func parseProbeVersion(stdout string) string {
	lines := strings.Split(stdout, "\n")
	for index, line := range lines {
		if strings.Contains(line, shellProbeSentinel) {
			for _, rest := range lines[index+1:] {
				value := strings.TrimSpace(rest)
				if value == "" {
					continue
				}
				// bash reports `5.3.15(1)-release`; keep the numeric part so the
				// version fits a table cell and stays comparable.
				if paren := strings.IndexByte(value, '('); paren > 0 {
					value = strings.TrimSpace(value[:paren])
				}
				return value
			}
			return ""
		}
	}
	return ""
}

func probeReason(err error, stderrText string) string {
	detail := strings.TrimSpace(stderrText)
	if detail == "" {
		return err.Error()
	}
	return err.Error() + ": " + truncateProbeText(detail)
}

// truncateProbeText bounds diagnostic text and drops invalid UTF-8: a localized
// (GBK) stub message must not corrupt the report.
func truncateProbeText(text string) string {
	text = strings.ToValidUTF8(strings.TrimSpace(text), "?")
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	if index := strings.Index(text, " "); index == 0 {
		text = strings.TrimSpace(text)
	}
	if len(text) > 200 {
		text = text[:200] + "..."
	}
	return text
}

// ---------------------------------------------------------------------------
// Resolution
// ---------------------------------------------------------------------------

// ShellCandidateChain returns the candidate IDs the resolver would try, in
// order. It performs no location or probing, so doctor/TUI/API can show the
// configured intent even when a binary is missing.
func ShellCandidateChain(cfg ShellConfig) []string {
	plan := resolvePlan(cfg)
	ids := make([]string, 0, len(plan))
	for _, spec := range plan {
		ids = append(ids, spec.id)
	}
	return ids
}

// ResolveShell plans, locates and probes the candidate chain, honouring the
// memoized result for the same config snapshot.
func ResolveShell(cfg ShellConfig) (*ShellResolution, error) {
	fingerprint := cfg.Fingerprint()
	memoMu.Lock()
	cached, ok := memo[fingerprint]
	memoMu.Unlock()
	if ok {
		return cached.resolution, cached.err
	}

	resolution, err := resolveShellUncached(context.Background(), cfg)
	memoMu.Lock()
	memo[fingerprint] = memoEntry{resolution: resolution, err: err}
	memoMu.Unlock()
	return resolution, err
}

// ResolveShellForce bypasses the memo. `doctor shell --refresh` and the
// settings page's probe button use it.
func ResolveShellForce(cfg ShellConfig) (*ShellResolution, error) {
	resolution, err := resolveShellUncached(context.Background(), cfg)
	memoMu.Lock()
	memo[cfg.Fingerprint()] = memoEntry{resolution: resolution, err: err}
	memoMu.Unlock()
	return resolution, err
}

// ProbeShellCandidates probes every planned candidate and reports the full
// matrix, still selecting the first one that passes. Normal resolution stops at
// the first success (startup latency); diagnostics need the whole picture, which
// is what doctor, the TUI and the settings page's "detect" button show.
func ProbeShellCandidates(cfg ShellConfig) (*ShellResolution, error) {
	return resolveShell(context.Background(), cfg, true)
}

// ProbeShellCandidatesCached memoizes the full matrix. Read-only surfaces (the
// settings page's GET) use it so reopening the page does not pay for a
// PowerShell cold start every time; the "detect" button uses the uncached form.
func ProbeShellCandidatesCached(cfg ShellConfig) (*ShellResolution, error) {
	key := cfg.Fingerprint() + "|matrix"
	memoMu.Lock()
	cached, ok := memo[key]
	memoMu.Unlock()
	if ok {
		return cached.resolution, cached.err
	}
	resolution, err := ProbeShellCandidates(cfg)
	memoMu.Lock()
	memo[key] = memoEntry{resolution: resolution, err: err}
	memoMu.Unlock()
	return resolution, err
}

// ResetShellMemo drops cached resolutions. Tests and config reloads use it.
func ResetShellMemo() {
	memoMu.Lock()
	memo = make(map[string]memoEntry)
	memoMu.Unlock()
}

type memoEntry struct {
	resolution *ShellResolution
	err        error
}

var (
	memoMu sync.Mutex
	memo   = make(map[string]memoEntry)
)

func resolveShellUncached(ctx context.Context, cfg ShellConfig) (*ShellResolution, error) {
	return resolveShell(ctx, cfg, false)
}

// resolveShell runs the plan. probeAll=false stops at the first passing
// candidate (production); probeAll=true probes the whole chain (diagnostics).
func resolveShell(ctx context.Context, cfg ShellConfig, probeAll bool) (*ShellResolution, error) {
	plan := resolvePlan(cfg)

	// Unix keeps its historical detection verbatim unless the config names
	// candidates. Probing every Unix start would add a process spawn for zero
	// benefit: the plan targets Windows.
	if runtime.GOOS != "windows" && strings.TrimSpace(cfg.Path) == "" && len(cfg.NormalizedPrefer()) == 0 {
		shell := DefaultUserShell()
		shell.Dialect = DialectFor(shell.Type, shell.ID)
		shell.Source = ShellSourceLegacy
		return &ShellResolution{
			Shell: shell,
			Candidates: []ShellCandidateReport{{
				ID:      shell.ID,
				Status:  ShellCandidatePass,
				Path:    shell.Path,
				Dialect: shell.Dialect,
			}},
			Strict:     cfg.ShellStrictProbe(),
			ResolvedAt: time.Now(),
			Configured: !cfg.isZero(),
		}, nil
	}

	resolution := &ShellResolution{
		Strict:     cfg.ShellStrictProbe(),
		ResolvedAt: time.Now(),
		Configured: !cfg.isZero(),
	}
	var probeTotal time.Duration
	var selected Shell
	var selectedProbe time.Duration

	for _, spec := range plan {
		report := ShellCandidateReport{ID: spec.id}
		if spec.skipReason != "" {
			report.Status = ShellCandidateSkip
			report.Reason = spec.skipReason
			resolution.Candidates = append(resolution.Candidates, report)
			continue
		}
		path, reason := locateShellCandidate(spec.id)
		if path == "" {
			report.Status = ShellCandidateSkip
			report.Reason = reason
			resolution.Candidates = append(resolution.Candidates, report)
			continue
		}
		report.Path = path
		// Guard rule: a `bash` candidate that resolves to a WSL launcher is
		// only admissible under the explicit `wsl` ID (plan §5.3).
		if spec.id != ShellIDWSL && isWSLBashPath(path) {
			report.Status = ShellCandidateSkip
			report.Guard = true
			report.Reason = "not in prefer (guard: WSL stub)"
			resolution.Candidates = append(resolution.Candidates, report)
			continue
		}

		shell := Shell{
			Path:    path,
			Type:    spec.typ,
			Prefix:  spec.prefix,
			ID:      spec.id,
			Dialect: DialectFor(spec.typ, spec.id),
		}
		probe := probeShell(ctx, shell, cfg.EffectiveProbeTimeout())
		probeTotal += probe.Elapsed
		report.ProbeMS = probe.Elapsed.Milliseconds()
		report.Dialect = shell.Dialect
		if probe.Err != nil {
			report.Status = ShellCandidateFail
			report.Reason = probe.Err.Error()
			resolution.Candidates = append(resolution.Candidates, report)
			continue
		}
		report.Status = ShellCandidatePass
		report.Version = probe.Version
		resolution.Candidates = append(resolution.Candidates, report)

		probed := shell
		probed.Version = probe.Version
		probed.Source = spec.source
		if selected.Path == "" {
			selected = probed
			selectedProbe = probe.Elapsed
		}
		if !probeAll {
			resolution.Shell = selected
			resolution.ProbeMS = selectedProbe.Milliseconds()
			return resolution, nil
		}
	}

	if selected.Path != "" {
		resolution.Shell = selected
		resolution.ProbeMS = selectedProbe.Milliseconds()
		return resolution, nil
	}

	// Nothing passed. Strict mode surfaces the failure; permissive mode records
	// the last-resort shell so the runtime stays usable.
	fallback := fallbackShell()
	fallback.Source = ShellSourceFallback
	fallback.Dialect = DialectFor(fallback.Type, fallback.ID)
	resolution.Shell = fallback
	resolution.ProbeMS = probeTotal.Milliseconds()
	if cfg.ShellStrictProbe() {
		return resolution, fmt.Errorf("%w: %d candidate(s) tried", ErrShellUnavailable, len(plan))
	}
	return resolution, nil
}

// fallbackShell is the documented last-resort shell. It is what
// DefaultUserShell already produced before this plan.
func fallbackShell() Shell {
	shell := DefaultUserShell()
	shell.ID = shellIDForPath(shell.Path)
	if shell.ID == "" && shell.Type == ShellTypeCmd {
		shell.ID = ShellIDCmd
	}
	return shell
}

// isZero reports whether the config block was left empty.
func (c ShellConfig) isZero() bool {
	return c.AutoDetect == nil && len(c.Prefer) == 0 && strings.TrimSpace(c.Path) == "" &&
		c.ProbeTimeout == 0 && c.StrictProbe == nil && c.ScriptDelivery == "" && len(c.Env) == 0
}

// ---------------------------------------------------------------------------
// Process-wide resolver
// ---------------------------------------------------------------------------

// The process-wide resolver is the single source of truth for "which shell do
// we execute with". Before it existed, DefaultUserShell() was called from seven
// independent places, so a configured shell could be overridden mid-flight (the
// worst case: foreground bash, promoted-to-detached PowerShell).
//
// 设计：docs/plan/windows-posix-shell-detection-and-config-plan-20261104.md §16.2
var shellResolver struct {
	mu          sync.Mutex
	cfg         ShellConfig
	fingerprint string
	resolution  *ShellResolution
	err         error
	loaded      bool
	// configured records that a runtime actually installed its shell config.
	// Until then CurrentShell keeps the historical DefaultUserShell behaviour,
	// so library-level callers (tests, embedders) are unaffected by the new
	// autodetection. Every real entry point installs during bootstrap.
	configured bool
}

// ConfigureShell installs the process-wide shell config. Repeating the same
// snapshot is a no-op, so every bootstrap point can call it unconditionally.
func ConfigureShell(cfg ShellConfig) {
	shellResolver.mu.Lock()
	defer shellResolver.mu.Unlock()
	fingerprint := cfg.Fingerprint()
	shellResolver.cfg = cfg
	if fingerprint == shellResolver.fingerprint && shellResolver.loaded && shellResolver.configured {
		return
	}
	shellResolver.fingerprint = fingerprint
	shellResolver.resolution = nil
	shellResolver.err = nil
	shellResolver.loaded = false
	shellResolver.configured = true
}

// ShellConfigSnapshot returns the installed config (zero when never configured).
func ShellConfigSnapshot() ShellConfig {
	shellResolver.mu.Lock()
	defer shellResolver.mu.Unlock()
	return shellResolver.cfg
}

// ShellEnvOverrides returns the configured env overrides for shell processes.
func ShellEnvOverrides() map[string]string {
	cfg := ShellConfigSnapshot()
	if len(cfg.Env) == 0 {
		return nil
	}
	out := make(map[string]string, len(cfg.Env))
	for key, value := range cfg.Env {
		out[key] = value
	}
	return out
}

// ApplyShellEnvOverrides layers the configured `shell.env` on top of env. It is
// a no-op when nothing is configured, so existing paths keep byte-identical
// environments.
func ApplyShellEnvOverrides(env []string) []string {
	overrides := ShellEnvOverrides()
	if len(overrides) == 0 {
		return env
	}
	return MergeEnvOverrides(env, overrides)
}

// CurrentScriptDelivery returns the configured long-script transport.
func CurrentScriptDelivery() ScriptDelivery {
	return ShellConfigSnapshot().EffectiveScriptDelivery()
}

// CurrentShell returns the shell every execution surface must use.
//
// It lazily performs the probe on first use and then serves the memoized
// resolution. When resolution fails (strict mode, nothing usable) it degrades to
// DefaultUserShell() so a broken config cannot brick the runtime — the failure
// is reported by CurrentResolution, doctor shell and the settings API.
func CurrentShell() Shell {
	resolution, _ := CurrentResolution()
	if resolution != nil && strings.TrimSpace(resolution.Shell.Path) != "" {
		return resolution.Shell
	}
	return DefaultUserShell()
}

// CurrentResolution returns the installed resolution, resolving lazily.
//
// Before any runtime calls ConfigureShell it reports the historical
// DefaultUserShell result with source=legacy: the autodetection chain only
// differs from the old priority order once a runtime opts in by installing its
// config.
func CurrentResolution() (*ShellResolution, error) {
	shellResolver.mu.Lock()
	defer shellResolver.mu.Unlock()
	if !shellResolver.configured {
		shell := DefaultUserShell()
		shell.Dialect = DialectFor(shell.Type, shell.ID)
		shell.Source = ShellSourceLegacy
		return &ShellResolution{
			Shell:      shell,
			Candidates: []ShellCandidateReport{{ID: shell.ID, Status: ShellCandidatePass, Path: shell.Path, Dialect: shell.Dialect}},
			ResolvedAt: time.Now(),
		}, nil
	}
	if shellResolver.loaded {
		return shellResolver.resolution, shellResolver.err
	}
	resolution, err := ResolveShell(shellResolver.cfg)
	shellResolver.resolution = resolution
	shellResolver.err = err
	shellResolver.loaded = true
	return resolution, err
}

// InvalidateShellResolution forces the next CurrentShell call to re-probe. The
// settings API and TUI call it after saving.
func InvalidateShellResolution() {
	shellResolver.mu.Lock()
	shellResolver.resolution = nil
	shellResolver.err = nil
	shellResolver.loaded = false
	shellResolver.mu.Unlock()
	ResetShellMemo()
}
