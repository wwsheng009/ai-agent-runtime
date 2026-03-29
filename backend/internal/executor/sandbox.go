package executor

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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
	config SandboxConfig
}

// NewSandbox creates a sandbox from the provided config.
func NewSandbox(config *SandboxConfig) *Sandbox {
	if config == nil {
		config = &SandboxConfig{}
	}
	return &Sandbox{
		config: SandboxConfig{
			Enabled:          config.Enabled,
			MaxExecutionTime: config.MaxExecutionTime,
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
	absPath, err := filepath.Abs(strings.TrimSpace(targetPath))
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	for _, denied := range s.config.DeniedPaths {
		if pathWithinBase(absPath, denied) {
			return fmt.Errorf("path denied by sandbox policy: %s", absPath)
		}
	}

	if len(s.config.AllowedPaths) > 0 {
		allowed := false
		for _, allowedPath := range s.config.AllowedPaths {
			if pathWithinBase(absPath, allowedPath) {
				allowed = true
				break
			}
		}
		if !allowed {
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
	if s.config.Enabled {
		return true
	}
	return s.config.MaxExecutionTime > 0 ||
		len(s.config.AllowedPaths) > 0 ||
		len(s.config.DeniedPaths) > 0 ||
		len(s.config.ReadOnlyPaths) > 0 ||
		len(s.config.AllowedCommands) > 0 ||
		len(s.config.DeniedCommands) > 0 ||
		len(s.config.EnvWhitelist) > 0 ||
		len(s.config.AllowedHosts) > 0 ||
		len(s.config.DeniedHosts) > 0
}

// ExecuteCommand runs a local process under sandbox policy.
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

	cmd := exec.CommandContext(execCtx, command, args...)
	if strings.TrimSpace(workDir) != "" {
		cmd.Dir = workDir
	}
	if s != nil {
		cmd.Env = s.FilterEnv(os.Environ())
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			return string(output), fmt.Errorf("sandbox command timed out after %v", s.config.MaxExecutionTime)
		}
		return string(output), err
	}
	return string(output), nil
}

func normalizeCommandName(command string) string {
	return strings.ToLower(filepath.Base(strings.TrimSpace(command)))
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
