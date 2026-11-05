package executor

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ShellDialect is the command-language family the model must write for the
// selected shell. It is deliberately coarser than ShellType: bash and zsh
// accept the same agent-authored scripts, busybox ash does not.
//
// 设计：docs/plan/windows-posix-shell-detection-and-config-plan-20261104.md §6.1
type ShellDialect string

const (
	// ShellDialectPosixFull is bash/zsh: arrays, [[ ]], pipefail are available.
	ShellDialectPosixFull ShellDialect = "posix-full"
	// ShellDialectPosixNarrow is busybox ash / dash: plain POSIX sh only.
	ShellDialectPosixNarrow ShellDialect = "posix-narrow"
	// ShellDialectPowerShell is pwsh / Windows PowerShell.
	ShellDialectPowerShell ShellDialect = "powershell"
	// ShellDialectCmd is cmd.exe.
	ShellDialectCmd ShellDialect = "cmd"
)

// ScriptDelivery selects how a command string reaches the shell process.
type ScriptDelivery string

const (
	// ScriptDeliveryStdin passes long scripts over stdin (POSIX: `-s`,
	// PowerShell: a fixed `-Command` launcher plus a stdin script block). Short
	// commands stay on argv.
	ScriptDeliveryStdin ScriptDelivery = "stdin"
	// ScriptDeliveryArgv always appends the script to argv. Diagnostic only:
	// it runs into the 8191 (cmd) / 32767 (CreateProcess) limits.
	ScriptDeliveryArgv ScriptDelivery = "argv"
)

// Stable candidate IDs. Config, doctor, TUI and the web API all speak these
// IDs; ShellType stays an internal detail.
//
// 设计：docs/plan/windows-posix-shell-detection-and-config-plan-20261104.md §3.1
const (
	ShellIDGitBash    = "git-bash"
	ShellIDMSYSBash   = "msys-bash"
	ShellIDBusybox    = "busybox"
	ShellIDWSL        = "wsl"
	ShellIDPwsh       = "pwsh"
	ShellIDPowerShell = "powershell"
	ShellIDCmd        = "cmd"
	ShellIDBash       = "bash"
	ShellIDZsh        = "zsh"
	ShellIDSh         = "sh"
)

// shellCandidateIDs is the closed set accepted by config validation and the
// settings API. Rejecting unknown IDs keeps a typo from silently degrading the
// resolution chain.
var shellCandidateIDs = []string{
	ShellIDGitBash, ShellIDMSYSBash, ShellIDBusybox, ShellIDWSL,
	ShellIDPwsh, ShellIDPowerShell, ShellIDCmd,
	ShellIDBash, ShellIDZsh, ShellIDSh,
}

// ShellCandidateIDs returns the stable IDs accepted by ShellConfig.Prefer, in
// display order. The returned slice is a copy.
func ShellCandidateIDs() []string {
	out := make([]string, len(shellCandidateIDs))
	copy(out, shellCandidateIDs)
	return out
}

// IsKnownShellCandidateID reports whether id is part of the stable set.
func IsKnownShellCandidateID(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, known := range shellCandidateIDs {
		if known == id {
			return true
		}
	}
	return false
}

// DefaultShellProbeTimeout bounds one sentinel probe. 3s covers a cold
// PowerShell start (measured 1.15-1.42s on the reference machine).
const DefaultShellProbeTimeout = 3 * time.Second

// ShellConfig is the `shell:` block of runtime.yaml.
//
// AutoDetect and StrictProbe are pointers so an absent key means "use the
// default" instead of Go's zero value. Without that, a config that only sets
// `prefer:` would silently turn autodetection off.
type ShellConfig struct {
	// AutoDetect enables the built-in fallback chain. When true, an explicit
	// Prefer list still wins as-is (no tail append) so the setup is
	// reproducible; AutoDetect only decides whether a chain exists at all
	// when Prefer is empty.
	AutoDetect *bool `yaml:"autoDetect,omitempty" json:"autoDetect,omitempty"`
	// Prefer is the ordered candidate chain using stable IDs.
	Prefer []string `yaml:"prefer,omitempty" json:"prefer,omitempty"`
	// Path forces one binary. It still has to pass the sentinel probe.
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
	// ProbeTimeout bounds a single sentinel probe.
	ProbeTimeout time.Duration `yaml:"probeTimeout,omitempty" json:"probeTimeout,omitempty"`
	// StrictProbe turns "no usable shell" into an error instead of a silent
	// fallback to cmd/powershell.
	StrictProbe *bool `yaml:"strictProbe,omitempty" json:"strictProbe,omitempty"`
	// ScriptDelivery selects the long-script transport.
	ScriptDelivery ScriptDelivery `yaml:"scriptDelivery,omitempty" json:"scriptDelivery,omitempty"`
	// Env is injected into shell processes. It must not carry styling or pager
	// variables: NeutralizeConsoleStylingEnv already forces those.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
}

// ShellAutoDetect reports the effective autodetect switch (default true).
func (c ShellConfig) ShellAutoDetect() bool {
	return c.AutoDetect == nil || *c.AutoDetect
}

// ShellStrictProbe reports the effective strict-probe switch (default true).
func (c ShellConfig) ShellStrictProbe() bool {
	return c.StrictProbe == nil || *c.StrictProbe
}

// EffectiveProbeTimeout returns the configured timeout or the default.
func (c ShellConfig) EffectiveProbeTimeout() time.Duration {
	if c.ProbeTimeout <= 0 {
		return DefaultShellProbeTimeout
	}
	return c.ProbeTimeout
}

// EffectiveScriptDelivery returns the configured transport or the default.
func (c ShellConfig) EffectiveScriptDelivery() ScriptDelivery {
	switch ScriptDelivery(strings.ToLower(strings.TrimSpace(string(c.ScriptDelivery)))) {
	case ScriptDeliveryArgv:
		return ScriptDeliveryArgv
	case ScriptDeliveryStdin:
		return ScriptDeliveryStdin
	default:
		return ScriptDeliveryStdin
	}
}

// NormalizedPrefer returns Prefer lower-cased, trimmed, de-duplicated and with
// unknown IDs dropped. Callers that need validation use ValidateShellConfig.
func (c ShellConfig) NormalizedPrefer() []string {
	if len(c.Prefer) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(c.Prefer))
	out := make([]string, 0, len(c.Prefer))
	for _, raw := range c.Prefer {
		id := strings.ToLower(strings.TrimSpace(raw))
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// Fingerprint identifies a config snapshot for memo invalidation. Only fields
// that can change the resolution participate.
func (c ShellConfig) Fingerprint() string {
	var b strings.Builder
	b.WriteString("auto=")
	b.WriteString(boolToken(c.ShellAutoDetect()))
	b.WriteString(";strict=")
	b.WriteString(boolToken(c.ShellStrictProbe()))
	b.WriteString(";path=")
	b.WriteString(strings.TrimSpace(c.Path))
	b.WriteString(";timeout=")
	b.WriteString(c.EffectiveProbeTimeout().String())
	b.WriteString(";prefer=")
	b.WriteString(strings.Join(c.NormalizedPrefer(), ","))
	b.WriteString(";env=")
	keys := make([]string, 0, len(c.Env))
	for key := range c.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		b.WriteString(key)
		b.WriteString("=")
		b.WriteString(c.Env[key])
		b.WriteString(";")
	}
	return b.String()
}

func boolToken(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

var shellEnvKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateShellConfig rejects values that would make the resolver
// non-deterministic or turn `shell.path` into an arbitrary program launcher.
//
// 安全约束见 docs/plan/windows-posix-shell-detection-and-config-plan-20261104.md §13
func ValidateShellConfig(cfg *ShellConfig) error {
	if cfg == nil {
		return nil
	}
	if cfg.ProbeTimeout < 0 {
		return fmt.Errorf("shell.probeTimeout must not be negative")
	}
	switch strings.ToLower(strings.TrimSpace(string(cfg.ScriptDelivery))) {
	case "", string(ScriptDeliveryStdin), string(ScriptDeliveryArgv):
	default:
		return fmt.Errorf("shell.scriptDelivery must be stdin or argv, got %q", cfg.ScriptDelivery)
	}
	for _, id := range cfg.NormalizedPrefer() {
		if !IsKnownShellCandidateID(id) {
			return fmt.Errorf("shell.prefer contains unknown candidate %q", id)
		}
	}
	if path := strings.TrimSpace(cfg.Path); path != "" {
		base := strings.ToLower(shellBaseName(path))
		base = strings.TrimSuffix(base, ".exe")
		switch base {
		case "bash", "sh", "zsh", "pwsh", "powershell", "cmd", "busybox":
		default:
			// Guard against `shell.path: evil.exe` turning the agent into a
			// generic program launcher.
			return fmt.Errorf("shell.path must point at a known shell binary (bash/sh/zsh/pwsh/powershell/cmd/busybox), got %q", path)
		}
		if hasShellPathDirectory(path) && !isAbsoluteShellPath(path) {
			return fmt.Errorf("shell.path with a directory must be absolute, got %q", path)
		}
	}
	for key, value := range cfg.Env {
		if !shellEnvKeyPattern.MatchString(key) {
			return fmt.Errorf("shell.env key %q must match [A-Za-z_][A-Za-z0-9_]*", key)
		}
		if strings.EqualFold(key, "PATH") {
			return fmt.Errorf("shell.env must not override PATH")
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("shell.env[%q] must not contain newlines", key)
		}
	}
	return nil
}

// hasShellPathDirectory reports whether path contains a directory component.
// A bare `bash` is intentionally allowed: resolution then uses LookPath and
// still enforces the known-binary-name guard above.
func hasShellPathDirectory(path string) bool {
	return strings.ContainsAny(path, `/\`)
}

// isAbsoluteShellPath accepts both native absolute paths and Windows drive /
// UNC paths even when validation runs on a non-Windows build host (the same
// runtime.yaml can be generated and validated on another OS).
func isAbsoluteShellPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	if filepath.IsAbs(path) {
		return true
	}
	if len(path) >= 3 && isASCIIAlpha(path[0]) && path[1] == ':' &&
		(path[2] == '\\' || path[2] == '/') {
		return true
	}
	return strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//")
}

func isASCIIAlpha(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

// ShellEnvSlice applies the configured env overrides on top of base.
//
// Styling/pager variables are intentionally not filtered here: callers run
// NeutralizeConsoleStylingEnv afterwards, which is the single owner of that
// contract.
func (c ShellConfig) ApplyEnv(base []string) []string {
	if len(c.Env) == 0 {
		return base
	}
	return MergeEnvOverrides(base, c.Env)
}

// MergeEnvOverrides returns base with overrides applied (key match is
// case-sensitive on Unix and case-insensitive on Windows).
func MergeEnvOverrides(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(overrides))
	applied := make(map[string]struct{}, len(overrides))
	for _, entry := range base {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			out = append(out, entry)
			continue
		}
		match := ""
		for overrideKey := range overrides {
			if envKeyEqual(key, overrideKey) {
				match = overrideKey
				break
			}
		}
		if match == "" {
			out = append(out, entry)
			continue
		}
		applied[match] = struct{}{}
		out = append(out, key+"="+overrides[match])
	}
	for key, value := range overrides {
		if _, done := applied[key]; done {
			continue
		}
		out = append(out, key+"="+value)
	}
	return out
}

func envKeyEqual(a, b string) bool {
	if IsWindows() {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// DialectFor maps a shell type (plus its candidate ID) to the dialect the model
// must be told about.
func DialectFor(shellType ShellType, id string) ShellDialect {
	switch shellType {
	case ShellTypeBash, ShellTypeZsh:
		return ShellDialectPosixFull
	case ShellTypeSh:
		if strings.EqualFold(strings.TrimSpace(id), ShellIDBusybox) {
			return ShellDialectPosixNarrow
		}
		// A plain `sh` is usually dash/ash/bsh: treat it as narrow POSIX so the
		// model does not emit bash-only syntax.
		return ShellDialectPosixNarrow
	case ShellTypePowerShell, ShellTypePwsh:
		return ShellDialectPowerShell
	case ShellTypeCmd:
		return ShellDialectCmd
	default:
		return ""
	}
}

// IsPosixDialect reports whether the dialect speaks shell, not PowerShell/cmd.
func (d ShellDialect) IsPosixDialect() bool {
	return d == ShellDialectPosixFull || d == ShellDialectPosixNarrow
}
