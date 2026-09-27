package executor

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// PermissionOp describes the type of filesystem access requested by a tool.
type PermissionOp string

const (
	OpRead    PermissionOp = "read"
	OpWrite   PermissionOp = "write"
	OpDelete  PermissionOp = "delete"
	OpExecute PermissionOp = "execute"
)

// SandboxConfig captures the local policy that can be enforced by the runtime.
type SandboxConfig struct {
	Enabled          bool          `yaml:"enabled" json:"enabled"`
	MaxExecutionTime time.Duration `yaml:"maxExecutionTime" json:"maxExecutionTime"`
	// Profile records the named application-layer sandbox profile that produced
	// this config (off|workspace|read-only|strict). Empty means raw/manual.
	Profile string `yaml:"profile,omitempty" json:"profile,omitempty"`
	// BlockNetwork denies all outbound URL checks when true (strict profile).
	BlockNetwork bool `yaml:"blockNetwork,omitempty" json:"blockNetwork,omitempty"`
	// OSSandbox selects optional OS-level process isolation for command launches:
	// off (default) | auto (use backend when available, else explicit degrade) |
	// require (fail-closed when OS isolation cannot be applied).
	// Application-layer path/network/command policy always remains active.
	OSSandbox string `yaml:"osSandbox,omitempty" json:"osSandbox,omitempty"`

	AllowedPaths  []string `yaml:"allowedPaths" json:"allowedPaths"`
	DeniedPaths   []string `yaml:"deniedPaths" json:"deniedPaths"`
	ReadOnlyPaths []string `yaml:"readOnlyPaths" json:"readOnlyPaths"`

	AllowedCommands []string `yaml:"allowedCommands" json:"allowedCommands"`
	DeniedCommands  []string `yaml:"deniedCommands" json:"deniedCommands"`
	EnvWhitelist    []string `yaml:"envWhitelist" json:"envWhitelist"`
	AllowedHosts    []string `yaml:"allowedHosts" json:"allowedHosts"`
	DeniedHosts     []string `yaml:"deniedHosts" json:"deniedHosts"`
}

// Sandbox is a reusable local execution policy wrapper.
type Sandbox struct {
	config    SandboxConfig
	osBackend OSSandboxBackend
}

// NewSandbox creates a sandbox from the provided config.
func NewSandbox(config *SandboxConfig) *Sandbox {
	if config == nil {
		config = &SandboxConfig{}
	}
	osMode, _ := NormalizeOSSandboxMode(config.OSSandbox)
	return &Sandbox{
		config: SandboxConfig{
			Enabled:          config.Enabled,
			MaxExecutionTime: config.MaxExecutionTime,
			Profile:          strings.TrimSpace(config.Profile),
			BlockNetwork:     config.BlockNetwork,
			OSSandbox:        osMode,
			AllowedPaths:     cloneStrings(config.AllowedPaths),
			DeniedPaths:      cloneStrings(config.DeniedPaths),
			ReadOnlyPaths:    cloneStrings(config.ReadOnlyPaths),
			AllowedCommands:  normalizeNames(config.AllowedCommands),
			DeniedCommands:   normalizeNames(config.DeniedCommands),
			EnvWhitelist:     cloneStrings(config.EnvWhitelist),
			AllowedHosts:     normalizeHosts(config.AllowedHosts),
			DeniedHosts:      normalizeHosts(config.DeniedHosts),
		},
	}
}

// Config returns a defensive copy of the sandbox config.
func (s *Sandbox) Config() SandboxConfig {
	if s == nil {
		return SandboxConfig{}
	}
	return SandboxConfig{
		Enabled:          s.config.Enabled,
		MaxExecutionTime: s.config.MaxExecutionTime,
		Profile:          s.config.Profile,
		BlockNetwork:     s.config.BlockNetwork,
		OSSandbox:        s.config.OSSandbox,
		AllowedPaths:     cloneStrings(s.config.AllowedPaths),
		DeniedPaths:      cloneStrings(s.config.DeniedPaths),
		ReadOnlyPaths:    cloneStrings(s.config.ReadOnlyPaths),
		AllowedCommands:  cloneStrings(s.config.AllowedCommands),
		DeniedCommands:   cloneStrings(s.config.DeniedCommands),
		EnvWhitelist:     cloneStrings(s.config.EnvWhitelist),
		AllowedHosts:     cloneStrings(s.config.AllowedHosts),
		DeniedHosts:      cloneStrings(s.config.DeniedHosts),
	}
}

// CheckPermission validates a filesystem operation against the configured policy.
func (s *Sandbox) CheckPermission(op PermissionOp, targetPath string) error {
	if s == nil || !s.active() {
		return nil
	}
	absPath, ok := absolutePathKeepingDotDot(strings.TrimSpace(targetPath))
	if !ok {
		return fmt.Errorf("resolve path: %s", strings.TrimSpace(targetPath))
	}
	// The lexical check sees the path as Clean would spell it (the same form
	// tools pass to os.Stat/os.Open), so an ordinary spelling stays a cheap
	// prefix test.
	if err := s.checkPathPolicy(op, filepath.Clean(absPath)); err != nil {
		return err
	}
	// The lexical check only sees the path as written; os.Stat/ReadFile then
	// follow symlinks (and apply ".." after them, not before). Re-check the
	// physical target, comparing resolved policy roots so a symlinked allowlist
	// entry keeps working. The check must run even when the resolution equals
	// the written path: a symlinked DeniedPaths/ReadOnlyPaths root would
	// otherwise be skipped entirely (2026-09-27 review H3), and the link/..
	// spelling only survives if the ".." reaches the resolver (H2) — hence
	// absolutePathKeepingDotDot instead of filepath.Abs, which would Clean the
	// ".." away before the kernel-order resolution below can see it.
	resolved, ok := resolvePhysicalPath(absPath)
	if !ok {
		return fmt.Errorf("path cannot be resolved to a physical location, denied by sandbox policy: %s", absPath)
	}
	if err := s.checkResolvedPathPolicy(op, resolved); err != nil {
		return err
	}
	return nil
}

// absolutePathKeepingDotDot makes path absolute without the lexical Clean that
// filepath.Abs performs, so the kernel-order resolver can still tell
// "link/../x" apart from "x". Returns false when the working directory is
// unavailable.
func absolutePathKeepingDotDot(path string) (string, bool) {
	if filepath.IsAbs(path) {
		return path, true
	}
	cwd, err := os.Getwd()
	if err != nil || strings.TrimSpace(cwd) == "" {
		return "", false
	}
	for len(path) > 0 && os.IsPathSeparator(path[0]) {
		path = path[1:]
	}
	if path == "" {
		return cwd, true
	}
	return cwd + string(os.PathSeparator) + path, true
}

// checkPathPolicy applies the lexically configured roots to one path.
func (s *Sandbox) checkPathPolicy(op PermissionOp, absPath string) error {
	for _, denied := range s.config.DeniedPaths {
		if pathWithinBase(absPath, denied) {
			return fmt.Errorf("path denied by sandbox policy: %s", absPath)
		}
	}

	if len(s.config.AllowedPaths) > 0 {
		if !pathWithinAnyBase(absPath, s.config.AllowedPaths) {
			return fmt.Errorf("path outside sandbox allowlist: %s", absPath)
		}
	}

	if op == OpWrite || op == OpDelete {
		for _, readOnly := range s.config.ReadOnlyPaths {
			if pathWithinBase(absPath, readOnly) {
				return fmt.Errorf("path is read-only under sandbox policy: %s", absPath)
			}
		}
	}

	return nil
}

// checkResolvedPathPolicy applies the same policy to a symlink-resolved target.
// Policy roots are resolved the same way, so a symlinked workspace root does not
// turn every legitimate access into a denial.
func (s *Sandbox) checkResolvedPathPolicy(op PermissionOp, resolved string) error {
	for _, denied := range resolveRoots(s.config.DeniedPaths) {
		if pathWithinBase(resolved, denied) {
			return fmt.Errorf("path resolves into a sandbox-denied location: %s", resolved)
		}
	}
	if len(s.config.AllowedPaths) > 0 && !pathWithinAnyBase(resolved, resolveRoots(s.config.AllowedPaths)) {
		return fmt.Errorf("path resolves outside the sandbox allowlist: %s", resolved)
	}
	if op == OpWrite || op == OpDelete {
		for _, readOnly := range resolveRoots(s.config.ReadOnlyPaths) {
			if pathWithinBase(resolved, readOnly) {
				return fmt.Errorf("path resolves into a read-only location: %s", resolved)
			}
		}
	}
	return nil
}

func pathWithinAnyBase(target string, roots []string) bool {
	for _, root := range roots {
		if pathWithinBase(target, root) {
			return true
		}
	}
	return false
}

// resolveRoots resolves policy roots so resolved targets are compared against
// the same physical form. A root that cannot be resolved (missing, permission
// denied) keeps its lexical value.
func resolveRoots(roots []string) []string {
	resolved := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if target, err := filepath.EvalSymlinks(root); err == nil && strings.TrimSpace(target) != "" {
			root = target
		}
		resolved = append(resolved, root)
	}
	return resolved
}

// maxSymlinkResolutionSteps bounds link chasing so a symlink loop cannot make
// permission checks hang.
const maxSymlinkResolutionSteps = 256

// resolvePhysicalPath returns the path the operating system will actually act
// on for absPath. Every existing component is followed through symlinks —
// including a dangling leaf link whose target does not exist yet, where
// filepath.EvalSymlinks gives up — and ".." is applied to the already-resolved
// prefix, matching kernel order (follow the link, then go up). Resolution stops
// at the first component that does not exist; the remaining components are
// rejoined lexically, because nothing below a missing entry can be a link the
// OS would follow.
//
// The second result is false when the path cannot be resolved safely (symlink
// loop, unreadable link target); callers must fail closed.
func resolvePhysicalPath(absPath string) (string, bool) {
	absPath = strings.TrimSpace(absPath)
	if absPath == "" {
		return "", false
	}
	volume := filepath.VolumeName(absPath)
	root := volume + string(os.PathSeparator)
	if root == string(os.PathSeparator) && !filepath.IsAbs(absPath) {
		return "", false
	}
	resolved := root
	pending := splitPathComponents(absPath[len(volume):])
	for steps := 0; len(pending) > 0; steps++ {
		if steps > maxSymlinkResolutionSteps {
			return "", false
		}
		component := pending[0]
		pending = pending[1:]
		switch component {
		case "", ".":
			continue
		case "..":
			// Kernel order: pop from the resolved prefix, not from the
			// lexical spelling. filepath.Dir keeps a volume root in place.
			if parent := filepath.Dir(resolved); parent != "" {
				resolved = parent
			}
			continue
		}
		candidate := filepath.Join(resolved, component)
		info, err := os.Lstat(candidate)
		if err != nil {
			// First missing component: rejoin the tail lexically.
			parts := append([]string{candidate}, pending...)
			return filepath.Join(parts...), true
		}
		if info.Mode()&os.ModeSymlink == 0 {
			resolved = candidate
			continue
		}
		target, err := os.Readlink(candidate)
		if err != nil || strings.TrimSpace(target) == "" {
			return "", false
		}
		if filepath.IsAbs(target) {
			targetVolume := filepath.VolumeName(target)
			resolved = targetVolume + string(os.PathSeparator)
			pending = append(splitPathComponents(target[len(targetVolume):]), pending...)
			continue
		}
		// A relative target is relative to the directory holding the link,
		// which is exactly the resolved prefix built so far.
		pending = append(splitPathComponents(target), pending...)
	}
	return filepath.Clean(resolved), true
}

// splitPathComponents splits a slash- or backslash-separated path on the
// host's separator rules. On Unix a backslash is an ordinary filename byte, so
// os.IsPathSeparator (not a hard-coded set) decides.
func splitPathComponents(path string) []string {
	parts := make([]string, 0, 8)
	start := 0
	for i := 0; i < len(path); i++ {
		if os.IsPathSeparator(path[i]) {
			if i > start {
				parts = append(parts, path[start:i])
			}
			start = i + 1
		}
	}
	if start < len(path) {
		parts = append(parts, path[start:])
	}
	return parts
}

// ValidateCommand validates the executable name against the configured policy.
func (s *Sandbox) ValidateCommand(command string) error {
	if s == nil || !s.active() {
		return nil
	}

	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return fmt.Errorf("command cannot be empty")
	}
	if err := s.CheckCommandDenied(trimmed); err != nil {
		return err
	}
	name := normalizeCommandName(trimmed)

	if len(s.config.AllowedCommands) == 0 {
		return nil
	}

	for _, allowed := range s.config.AllowedCommands {
		if name == allowed {
			return nil
		}
	}
	return fmt.Errorf("command not allowed by sandbox policy: %s", trimmed)
}

// CheckCommandDenied validates only the denylist portion of command policy.
func (s *Sandbox) CheckCommandDenied(command string) error {
	if s == nil || !s.active() {
		return nil
	}

	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return fmt.Errorf("command cannot be empty")
	}
	name := normalizeCommandName(trimmed)
	for _, denied := range s.config.DeniedCommands {
		if name == denied {
			return fmt.Errorf("command denied by sandbox policy: %s", trimmed)
		}
	}
	return nil
}

// CheckURL validates an outbound URL against the configured network policy.
func (s *Sandbox) CheckURL(rawURL string) error {
	if s == nil || !s.active() {
		return nil
	}
	if s.config.BlockNetwork {
		return fmt.Errorf("network access denied by sandbox policy")
	}

	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("url must include scheme and host")
	}

	host := strings.ToLower(strings.TrimSpace(parsed.Host))
	hostname := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" || hostname == "" {
		return fmt.Errorf("url must include a valid host")
	}

	for _, denied := range s.config.DeniedHosts {
		if hostMatchesPattern(host, hostname, denied) {
			return fmt.Errorf("url host denied by sandbox policy: %s", parsed.Host)
		}
	}

	if len(s.config.AllowedHosts) == 0 {
		return nil
	}
	for _, allowed := range s.config.AllowedHosts {
		if hostMatchesPattern(host, hostname, allowed) {
			return nil
		}
	}
	return fmt.Errorf("url host not allowed by sandbox policy: %s", parsed.Host)
}

// FilterEnv keeps only whitelisted environment variables.
// When the whitelist is empty, it returns an empty environment by default.
func (s *Sandbox) FilterEnv(env []string) []string {
	if s == nil || !s.active() {
		return cloneStrings(env)
	}
	if len(s.config.EnvWhitelist) == 0 {
		return []string{}
	}

	allowed := make(map[string]struct{}, len(s.config.EnvWhitelist))
	for _, key := range s.config.EnvWhitelist {
		key = strings.TrimSpace(key)
		if key != "" {
			allowed[key] = struct{}{}
		}
	}

	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 0 {
			continue
		}
		if _, ok := allowed[parts[0]]; ok {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func (s *Sandbox) active() bool {
	if s == nil {
		return false
	}
	return SandboxConfigActive(s.config)
}

// ExecuteCommand runs a local process under sandbox policy.
// When SandboxConfig.OSSandbox is auto/require, the launch may be rewritten by
// the optional OS backend (Linux bubblewrap). Failures degrade explicitly
// (auto) or fail-closed (require) — never silent "looks sandboxed but isn't".
func (s *Sandbox) ExecuteCommand(ctx context.Context, command string, args []string, workDir string) (string, error) {
	if err := s.ValidateCommand(command); err != nil {
		return "", err
	}

	if strings.TrimSpace(workDir) != "" {
		if err := s.CheckPermission(OpExecute, workDir); err != nil {
			return "", err
		}
	}

	execCtx := ctx
	cancel := func() {}
	if s != nil && s.config.MaxExecutionTime > 0 {
		execCtx, cancel = context.WithTimeout(ctx, s.config.MaxExecutionTime)
	}
	defer cancel()

	// BuildFilteredEnv only applies EnvWhitelist when present; OSSandbox-only
	// configs must not wipe the process environment.
	env := BuildFilteredEnv(s, os.Environ())
	launch, err := s.PrepareOSCommand(execCtx, command, args, workDir, env)
	if err != nil {
		return "", err
	}

	cmd := exec.CommandContext(execCtx, launch.Command, launch.Args...)
	if strings.TrimSpace(launch.WorkDir) != "" {
		cmd.Dir = launch.WorkDir
	} else if !launch.Applied && strings.TrimSpace(workDir) != "" {
		// Host Dir only when OS wrap did not already chdir inside the guest.
		cmd.Dir = workDir
	}
	if launch.Env != nil {
		cmd.Env = launch.Env
	} else if s != nil {
		cmd.Env = env
	}

	capture, err := CaptureCombinedOutputWithMirror(cmd, DefaultRetainedOutputBytes, OutputMirrorFromContext(ctx))
	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			return capture.Output, fmt.Errorf("sandbox command timed out after %v", s.config.MaxExecutionTime)
		}
		return capture.Output, err
	}
	return capture.Output, nil
}

func normalizeCommandName(command string) string {
	name := strings.ToLower(filepath.Base(strings.TrimSpace(command)))
	// On Windows, strip common executable extensions so that
	// "cmd.exe" matches a deny entry for "cmd".
	if runtime.GOOS == "windows" {
		for _, ext := range []string{".exe", ".cmd", ".bat"} {
			if strings.HasSuffix(name, ext) {
				name = name[:len(name)-len(ext)]
				break
			}
		}
	}
	return name
}

func normalizeNames(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = normalizeCommandName(value)
		if value != "" {
			normalized = append(normalized, value)
		}
	}
	return normalized
}

func normalizeHosts(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			normalized = append(normalized, value)
		}
	}
	return normalized
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func pathWithinBase(targetPath, basePath string) bool {
	baseAbs, err := filepath.Abs(strings.TrimSpace(basePath))
	if err != nil {
		return false
	}
	targetAbs, err := filepath.Abs(strings.TrimSpace(targetPath))
	if err != nil {
		return false
	}

	rel, err := filepath.Rel(baseAbs, targetAbs)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && rel != "..")
}

func hostMatchesPattern(host, hostname, pattern string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return false
	}
	if host == pattern || hostname == pattern {
		return true
	}
	if strings.Contains(pattern, ":") {
		return false
	}
	return strings.HasSuffix(hostname, "."+pattern)
}
