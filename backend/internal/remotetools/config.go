// Package remotetools implements the remote tool server side of the aicli
// remote-tools design (docs/plan/aicli-remote-tools-ssh-http-design-20261021.md).
//
// One binary serves both transports over the same implementation:
//
//	aicli-remote-tools serve --stdio   # launched per connection over SSH
//	aicli-remote-tools serve --http    # long-lived daemon, loopback by default
//
// The tool surface, schema, path jail, output budget, error codes and audit
// records are shared; only process lifecycle differs. That is what makes the
// two transports comparable in tests (transport parity).
package remotetools

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// Profile selects the allowed tool family for a remote target.
type Profile string

const (
	// ProfileReadonly exposes view/ls/glob/grep only. It is the default.
	ProfileReadonly Profile = "readonly"
	// ProfileStandard adds file-mutating tools (write/edit/apply_patch/...).
	ProfileStandard Profile = "standard"
	// ProfileOps adds the shell tool on top of standard.
	ProfileOps Profile = "ops"
	// ProfileCustom exposes exactly the configured tool allowlist.
	ProfileCustom Profile = "custom"
)

// Limits bounds one remote invocation so a single call cannot exhaust the
// target machine or the model context.
type Limits struct {
	// MaxOutputBytes is the legacy capture limit for shell output. It no longer
	// controls the model-visible window; ModelVisibleCap does.
	MaxOutputBytes int `yaml:"maxOutputBytes" json:"maxOutputBytes"`
	// MaxBytesPerCall caps one view/grep payload before folding.
	MaxBytesPerCall int `yaml:"maxBytesPerCall" json:"maxBytesPerCall"`
	// CaptureLimit is the v1 name for the pre-fold capture ceiling. When set it
	// supersedes MaxBytesPerCall for read tools.
	CaptureLimit int `yaml:"captureLimit" json:"captureLimit"`
	// ModelVisibleCap bounds the model-visible body after folding. It can only
	// narrow the local tool budget and never exceeds 64 KiB.
	ModelVisibleCap int `yaml:"modelVisibleCap" json:"modelVisibleCap"`
	// CommandTimeout bounds one shell command (shell tool only).
	CommandTimeout Duration `yaml:"commandTimeout" json:"commandTimeout"`
	// MaxConcurrentCalls bounds in-flight requests in HTTP mode.
	MaxConcurrentCalls int `yaml:"maxConcurrentCalls" json:"maxConcurrentCalls"`
}

// Duration is a YAML/JSON friendly time.Duration.
type Duration struct {
	time.Duration
}

// UnmarshalYAML accepts "120s", "2m", or a bare nanosecond integer.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		return nil
	}
	var raw string
	if err := value.Decode(&raw); err == nil {
		parsed, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", raw, err)
		}
		d.Duration = parsed
		return nil
	}
	var nanos int64
	if err := value.Decode(&nanos); err != nil {
		return fmt.Errorf("invalid duration node")
	}
	d.Duration = time.Duration(nanos)
	return nil
}

// MarshalYAML renders the duration in its human form.
func (d Duration) MarshalYAML() (interface{}, error) {
	return d.Duration.String(), nil
}

// ShellConfig carries the command policy. The exec path is argv-based; this
// policy is the explicit opt-in for allowing a shell at all.
type ShellConfig struct {
	// AllowedCommands is an allowlist of argv prefixes. Empty means "deny all":
	// a target that enables the shell tool must name what it permits.
	AllowedCommands []string `yaml:"allowedCommands" json:"allowedCommands"`
	// AllowedCommandPatterns is an explicit opt-in wildcard allowlist matched
	// against the whole normalized command line. It exists for families such as
	// deploy238_*.sh that cannot be expressed as a fixed argv prefix; because a
	// program-position wildcard can match a writable script, operators must opt in
	// here rather than widening AllowedCommands.
	AllowedCommandPatterns []string `yaml:"allowedCommandPatterns" json:"allowedCommandPatterns"`
	// DeniedCommands always wins over the allowlist.
	DeniedCommands []string `yaml:"deniedCommands" json:"deniedCommands"`
	// DeniedCommandPatterns is the denial counterpart of AllowedCommandPatterns.
	// Deny patterns are evaluated before every allow rule.
	DeniedCommandPatterns []string `yaml:"deniedCommandPatterns" json:"deniedCommandPatterns"`
	// DenyShellMetachars rejects commands containing shell metacharacters so a
	// single allowed binary cannot be chained into an arbitrary command.
	DenyShellMetachars bool `yaml:"denyShellMetachars" json:"denyShellMetachars"`
	// EnvAllowlist lists environment variables passed through. Empty means no
	// inherited variable is forwarded.
	EnvAllowlist []string `yaml:"envAllowlist" json:"envAllowlist"`
}

// HTTPConfig describes the daemon listener.
type HTTPConfig struct {
	// Listen is host:port. Non-loopback addresses require a token.
	Listen string `yaml:"listen" json:"listen"`
	// Path is the MCP endpoint path.
	Path string `yaml:"path" json:"path"`
	// TokenEnv names the environment variable holding the bearer token. The
	// token is never read from the command line or written to logs.
	TokenEnv string `yaml:"tokenEnv" json:"tokenEnv"`
	// TLSCert / TLSKey enable HTTPS when both are set.
	TLSCert string `yaml:"tlsCert" json:"tlsCert"`
	TLSKey  string `yaml:"tlsKey" json:"tlsKey"`
	// ClientCAFile enables mutual TLS: when set, a connection must present a
	// client certificate signed by this CA. It is composable with the bearer
	// token: a proxy that strips headers still authenticates by certificate, and
	// a deployment without mTLS keeps using the token alone.
	ClientCAFile string `yaml:"clientCAFile" json:"clientCAFile"`
	// RequireClientCert makes the client certificate mandatory. It only takes
	// effect when ClientCAFile is set.
	RequireClientCert bool `yaml:"requireClientCert" json:"requireClientCert"`
	// AccessLog is an optional NDJSON request log (method/path/status/duration/
	// remote address). It records 401s too and never logs Authorization headers.
	AccessLog string `yaml:"accessLog" json:"accessLog"`
	// AccessLogMaxSizeMB and AccessLogMaxBackups control rotation; zero uses the
	// same defaults as the tool audit sink.
	AccessLogMaxSizeMB  int `yaml:"accessLogMaxSizeMB" json:"accessLogMaxSizeMB"`
	AccessLogMaxBackups int `yaml:"accessLogMaxBackups" json:"accessLogMaxBackups"`
}

// AuditConfig describes the NDJSON audit sink.
type AuditConfig struct {
	Path       string `yaml:"path" json:"path"`
	MaxSizeMB  int    `yaml:"maxSizeMB" json:"maxSizeMB"`
	MaxBackups int    `yaml:"maxBackups" json:"maxBackups"`
	// CommandPreviewBytes records the first N bytes of a shell/job_start command.
	// Zero keeps the default privacy posture: only a program name and hash.
	CommandPreviewBytes int `yaml:"commandPreviewBytes" json:"commandPreviewBytes"`
}

// JobsConfig describes the background job store.
//
// Root is required to enable the job_* tools: a job record and its output must
// survive the process, so an in-memory implementation would silently lose them on
// reconnect. Empty root leaves the tools unregistered (a host that cannot offer
// durable jobs should not advertise them).
type JobsConfig struct {
	Root string `yaml:"root" json:"root"`
}

// ArtifactConfig describes the full-capture store.
//
// The local runtime archives shell captures and hands the model an artifact_read
// pointer. A remote call needs the same so a folded output stays recoverable
// (design §D6). Empty Root disables archiving and the artifact_read tool.
type ArtifactConfig struct {
	Root     string `yaml:"root" json:"root"`
	MaxBytes int64  `yaml:"maxBytes" json:"maxBytes"`
}

// Config is the complete remote-tools configuration. Command-line flags override
// file values, and file values override the built-in defaults.
type Config struct {
	// Workspace is the root relative paths resolve against and the default jail
	// root. Required.
	Workspace string `yaml:"workspace" json:"workspace"`
	// AllowedRoots is the absolute path allowlist. The workspace is always
	// included implicitly.
	AllowedRoots []string `yaml:"allowedRoots" json:"allowedRoots"`
	// Profile selects the tool family.
	Profile Profile `yaml:"profile" json:"profile"`
	// ToolPrefix is prepended to every exposed tool name so a remote tool can
	// never collide with a local builtin (review G4).
	ToolPrefix string `yaml:"toolPrefix" json:"toolPrefix"`
	// Tools is the explicit tool allowlist used by ProfileCustom, and an extra
	// narrowing filter for every other profile.
	Tools []string `yaml:"tools" json:"tools"`
	// ReadOnly forces the read-only tool family regardless of Profile.
	ReadOnly bool `yaml:"readOnly" json:"readOnly"`
	// DescriptionStyle is "compact" or "explicit". Explicit prefixes every tool
	// description with [remote:<target>] so the model cannot mistake it for a
	// local path (design §6.4).
	DescriptionStyle string `yaml:"descriptionStyle" json:"descriptionStyle"`
	// TargetName labels the workspace in descriptions and audit records.
	TargetName string `yaml:"targetName" json:"targetName"`
	// Transport is the serving transport ("stdio" or "http"), stamped into audit
	// records. It is set by the entrypoint, not by the config file.
	Transport string `yaml:"-" json:"-"`

	Limits Limits      `yaml:"limits" json:"limits"`
	Shell  ShellConfig `yaml:"shell" json:"shell"`
	HTTP   HTTPConfig  `yaml:"http" json:"http"`
	Audit  AuditConfig `yaml:"audit" json:"audit"`
	Jobs   JobsConfig  `yaml:"jobs" json:"jobs"`
	// Artifacts stores full captures of folded tool output.
	Artifacts ArtifactConfig `yaml:"artifacts" json:"artifacts"`
}

// DefaultConfig returns the configuration a bare invocation would use.
func DefaultConfig() *Config {
	return &Config{
		Profile:          ProfileReadonly,
		DescriptionStyle: "compact",
		Limits: Limits{
			MaxOutputBytes:     DefaultMaxOutputBytes,
			MaxBytesPerCall:    DefaultMaxBytesPerCall,
			CaptureLimit:       DefaultMaxBytesPerCall,
			ModelVisibleCap:    DefaultModelVisibleCap,
			CommandTimeout:     Duration{120 * time.Second},
			MaxConcurrentCalls: 8,
		},
		Shell: ShellConfig{
			DenyShellMetachars: true,
		},
		HTTP: HTTPConfig{
			Listen:   "127.0.0.1:18765",
			Path:     "/mcp",
			TokenEnv: "AICLI_REMOTE_TOKEN",
		},
	}
}

// LoadConfigFile reads a YAML config file and merges it over the defaults.
func LoadConfigFile(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read remote tools config: %w", err)
	}
	cfg := DefaultConfig()
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse remote tools config: %w", err)
	}
	return cfg, nil
}

// LoadConfigBase64 decodes a base64-encoded JSON config.
//
// This is the shape the local expander uses for the SSH stdio transport: the
// whole remote configuration travels as one opaque token, so a workspace path
// containing spaces, quotes or `$` never enters the remote shell grammar
// (review B4). It accepts JSON rather than YAML because the encoder is our own
// code, not a hand-edited file.
func LoadConfigBase64(encoded string) (*Config, error) {
	trimmed := strings.TrimSpace(encoded)
	if trimmed == "" {
		return nil, fmt.Errorf("config payload is empty")
	}
	decoded, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return nil, fmt.Errorf("decode --config-b64: %w", err)
	}
	cfg := DefaultConfig()
	if err := json.Unmarshal(decoded, cfg); err != nil {
		return nil, fmt.Errorf("parse --config-b64: %w", err)
	}
	return cfg, nil
}

// Normalize fills defaults, resolves the workspace to an absolute path and
// validates the invariants that must hold before the server starts.
//
// It fails closed: an unusable workspace, an unknown profile or a non-loopback
// HTTP listener without a token are all startup errors rather than warnings.
func (c *Config) Normalize() error {
	if c == nil {
		return fmt.Errorf("config is nil")
	}
	c.Profile = Profile(strings.ToLower(strings.TrimSpace(string(c.Profile))))
	if c.Profile == "" {
		c.Profile = ProfileReadonly
	}
	switch c.Profile {
	case ProfileReadonly, ProfileStandard, ProfileOps, ProfileCustom:
	default:
		return fmt.Errorf("unknown profile %q (want readonly|standard|ops|custom)", c.Profile)
	}

	workspace := strings.TrimSpace(c.Workspace)
	if workspace == "" {
		return fmt.Errorf("workspace is required")
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return fmt.Errorf("resolve workspace %q: %w", workspace, err)
	}
	c.Workspace = filepath.Clean(absWorkspace)
	if info, statErr := os.Stat(c.Workspace); statErr != nil {
		return fmt.Errorf("workspace %q is not accessible: %w", c.Workspace, statErr)
	} else if !info.IsDir() {
		return fmt.Errorf("workspace %q is not a directory", c.Workspace)
	}

	// The workspace is always an admitted root; every other root must be
	// absolute so a relative entry cannot silently follow the process CWD.
	roots := make([]string, 0, len(c.AllowedRoots)+1)
	seen := map[string]bool{c.Workspace: true}
	roots = append(roots, c.Workspace)
	for _, root := range c.AllowedRoots {
		trimmed := strings.TrimSpace(root)
		if trimmed == "" {
			continue
		}
		abs, absErr := filepath.Abs(trimmed)
		if absErr != nil {
			return fmt.Errorf("resolve allowed root %q: %w", trimmed, absErr)
		}
		abs = filepath.Clean(abs)
		if seen[abs] {
			continue
		}
		seen[abs] = true
		roots = append(roots, abs)
	}
	c.AllowedRoots = roots

	c.ToolPrefix = strings.TrimSpace(c.ToolPrefix)
	if c.ToolPrefix != "" && !isPortableToolPrefix(c.ToolPrefix) {
		return fmt.Errorf("toolPrefix %q must match [A-Za-z0-9_-]+", c.ToolPrefix)
	}
	if err := validateCommandPatterns("allowedCommandPatterns", c.Shell.AllowedCommandPatterns); err != nil {
		return err
	}
	if err := validateCommandPatterns("deniedCommandPatterns", c.Shell.DeniedCommandPatterns); err != nil {
		return err
	}
	c.Shell.AllowedCommandPatterns = normalizeCommandPatterns(c.Shell.AllowedCommandPatterns)
	c.Shell.DeniedCommandPatterns = normalizeCommandPatterns(c.Shell.DeniedCommandPatterns)
	if c.TargetName == "" {
		c.TargetName = filepath.Base(c.Workspace)
	}
	c.TargetName = strings.ToLower(strings.TrimSpace(c.TargetName))
	if !toolcore.ValidArtifactTarget(c.TargetName) {
		return fmt.Errorf("targetName %q must match [a-z0-9][a-z0-9_.-]{0,63}", c.TargetName)
	}

	if c.Limits.MaxOutputBytes <= 0 {
		c.Limits.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if c.Limits.MaxBytesPerCall <= 0 {
		c.Limits.MaxBytesPerCall = DefaultMaxBytesPerCall
	}
	if c.Limits.CaptureLimit <= 0 {
		c.Limits.CaptureLimit = c.Limits.MaxBytesPerCall
	}
	if c.Limits.ModelVisibleCap <= 0 {
		c.Limits.ModelVisibleCap = DefaultModelVisibleCap
	}
	if c.Limits.ModelVisibleCap > DefaultRemoteOutputHardCap {
		c.Limits.ModelVisibleCap = DefaultRemoteOutputHardCap
	}
	if c.Limits.CommandTimeout.Duration <= 0 {
		c.Limits.CommandTimeout = Duration{120 * time.Second}
	}
	if c.Limits.MaxConcurrentCalls <= 0 {
		c.Limits.MaxConcurrentCalls = 8
	}

	switch strings.ToLower(strings.TrimSpace(c.DescriptionStyle)) {
	case "", "compact":
		c.DescriptionStyle = "compact"
	case "explicit":
		c.DescriptionStyle = "explicit"
	default:
		return fmt.Errorf("unknown descriptionStyle %q (want compact|explicit)", c.DescriptionStyle)
	}

	c.HTTP.Listen = strings.TrimSpace(c.HTTP.Listen)
	c.HTTP.Path = strings.TrimSpace(c.HTTP.Path)
	if c.HTTP.Path == "" {
		c.HTTP.Path = "/mcp"
	}
	if !strings.HasPrefix(c.HTTP.Path, "/") {
		return fmt.Errorf("http.path %q must start with /", c.HTTP.Path)
	}
	if c.HTTP.TokenEnv == "" {
		c.HTTP.TokenEnv = "AICLI_REMOTE_TOKEN"
	}
	if (strings.TrimSpace(c.HTTP.TLSCert) == "") != (strings.TrimSpace(c.HTTP.TLSKey) == "") {
		return fmt.Errorf("tlsCert and tlsKey must be set together")
	}
	if c.HTTP.RequireClientCert && strings.TrimSpace(c.HTTP.ClientCAFile) == "" {
		return fmt.Errorf("http.requireClientCert needs http.clientCAFile")
	}
	if caFile := strings.TrimSpace(c.HTTP.ClientCAFile); caFile != "" {
		if _, err := os.Stat(caFile); err != nil {
			return fmt.Errorf("http.clientCAFile %q is not readable: %w", caFile, err)
		}
		// mTLS without TLS would hand the client certificate over plaintext.
		if strings.TrimSpace(c.HTTP.TLSCert) == "" {
			return fmt.Errorf("http.clientCAFile requires tlsCert/tlsKey: client certificates must not travel over plaintext")
		}
	}
	if c.Audit.MaxSizeMB <= 0 {
		c.Audit.MaxSizeMB = 100
	}
	if c.Audit.MaxBackups < 0 {
		c.Audit.MaxBackups = 0
	}
	if c.Audit.CommandPreviewBytes < 0 {
		return fmt.Errorf("audit.commandPreviewBytes must not be negative")
	}
	if c.Audit.CommandPreviewBytes > 4096 {
		c.Audit.CommandPreviewBytes = 4096
	}
	c.HTTP.AccessLog = strings.TrimSpace(c.HTTP.AccessLog)
	if c.HTTP.AccessLogMaxSizeMB < 0 {
		return fmt.Errorf("http.accessLogMaxSizeMB must not be negative")
	}
	if c.HTTP.AccessLogMaxBackups < 0 {
		return fmt.Errorf("http.accessLogMaxBackups must not be negative")
	}
	return nil
}

// EffectiveAllowedRoots returns the admitted roots including the workspace.
func (c *Config) EffectiveAllowedRoots() []string {
	if c == nil {
		return nil
	}
	if len(c.AllowedRoots) == 0 {
		return []string{c.Workspace}
	}
	return append([]string(nil), c.AllowedRoots...)
}

// isPortableToolPrefix keeps the prefix inside the character set the MCP
// registry accepts (registry.providerToolNamePattern: [a-zA-Z0-9_-]{1,64}).
func isPortableToolPrefix(prefix string) bool {
	for _, r := range prefix {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return prefix != ""
}
