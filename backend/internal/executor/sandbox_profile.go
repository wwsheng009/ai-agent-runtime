package executor

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Named application-layer sandbox profiles.
// These map product-facing agentdef/profile values onto SandboxConfig.
const (
	SandboxProfileOff       = "off"
	SandboxProfileWorkspace = "workspace"
	SandboxProfileReadOnly  = "read-only"
	SandboxProfileStrict    = "strict"
)

// SandboxProfileOptions controls materialization of a named profile.
type SandboxProfileOptions struct {
	// WorkspaceRoot is the session/project workspace used for path scoping.
	// Required for workspace/read-only/strict to fully enforce path bounds.
	WorkspaceRoot string
	// Explicit config fields from profile/agent maps are applied after the
	// named profile defaults (allowlists, denylists, timeouts, etc.).
	Override SandboxConfig
}

// SandboxProfileResult is the materialization outcome for a named profile.
type SandboxProfileResult struct {
	// Requested is the normalized profile name (or empty when raw-only).
	Requested string
	// Effective is the profile actually enforced after downgrade (if any).
	Effective string
	// Config is the resulting application-layer sandbox config.
	Config SandboxConfig
	// ReadOnly reports whether the profile implies tool-policy read-only.
	ReadOnly bool
	// Warnings contain explicit downgrade / incomplete-enforcement notices.
	// Callers must surface these; never treat a degraded profile as silent strict.
	Warnings []string
}

// NormalizeSandboxProfile canonicalizes product-facing sandbox profile names.
// Empty input returns empty (raw/manual config only).
func NormalizeSandboxProfile(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return "", nil
	case SandboxProfileOff, "disabled", "none":
		return SandboxProfileOff, nil
	case SandboxProfileWorkspace, "ws":
		return SandboxProfileWorkspace, nil
	case SandboxProfileReadOnly, "readonly", "ro":
		return SandboxProfileReadOnly, nil
	case SandboxProfileStrict:
		return SandboxProfileStrict, nil
	default:
		return "", fmt.Errorf("invalid sandbox profile %q (want off|workspace|read-only|strict)", raw)
	}
}

// ResolveSandboxProfile materializes a named application-layer sandbox profile.
//
// Mapping:
//   - off:       disabled; no path/network/command restrictions from the profile
//   - workspace: enabled; path allowlist = workspace root (writes allowed inside)
//   - read-only: enabled; path allowlist + readOnlyPaths = workspace; tool ReadOnly
//   - strict:    read-only + block network + deny interactive shells + env whitelist
//
// When a restrictive profile cannot fully enforce (e.g. missing workspace root),
// the result is explicitly downgraded with Warnings rather than silently claiming
// the requested profile.
func ResolveSandboxProfile(profile string, opts SandboxProfileOptions) (SandboxProfileResult, error) {
	requested, err := NormalizeSandboxProfile(profile)
	if err != nil {
		return SandboxProfileResult{}, err
	}

	workspace := strings.TrimSpace(opts.WorkspaceRoot)
	if workspace != "" {
		if abs, absErr := filepath.Abs(workspace); absErr == nil {
			workspace = abs
		}
		workspace = filepath.Clean(workspace)
	}

	result := SandboxProfileResult{
		Requested: requested,
		Effective: requested,
	}

	switch requested {
	case "", SandboxProfileOff:
		result.Effective = SandboxProfileOff
		result.Config = SandboxConfig{
			Enabled: false,
			Profile: SandboxProfileOff,
		}
	case SandboxProfileWorkspace:
		result.Config = SandboxConfig{
			Enabled: true,
			Profile: SandboxProfileWorkspace,
		}
		if workspace == "" {
			result.Effective = SandboxProfileOff
			result.Config = SandboxConfig{
				Enabled: false,
				Profile: SandboxProfileOff,
			}
			result.Warnings = append(result.Warnings,
				"sandbox profile workspace downgraded to off: workspace root is unset; path bounds cannot be enforced")
		} else {
			result.Config.AllowedPaths = []string{workspace}
		}
	case SandboxProfileReadOnly:
		result.ReadOnly = true
		result.Config = SandboxConfig{
			Enabled:        true,
			Profile:        SandboxProfileReadOnly,
			DeniedCommands: DefaultReadOnlyDeniedCommands(),
		}
		if workspace == "" {
			// Keep read-only tool policy + command denylist, but admit path
			// bounds are incomplete so callers do not claim full workspace isolation.
			result.Warnings = append(result.Warnings,
				"sandbox profile read-only partially enforced: workspace root is unset; path allow/read-only bounds are not applied")
		} else {
			result.Config.AllowedPaths = []string{workspace}
			result.Config.ReadOnlyPaths = []string{workspace}
		}
	case SandboxProfileStrict:
		result.ReadOnly = true
		result.Config = SandboxConfig{
			Enabled:         true,
			Profile:         SandboxProfileStrict,
			BlockNetwork:    true,
			DeniedCommands:  DefaultStrictDeniedCommands(),
			EnvWhitelist:    DefaultStrictEnvWhitelist(),
			AllowedCommands: DefaultStrictAllowedCommands(),
		}
		if workspace == "" {
			result.Effective = SandboxProfileReadOnly
			result.Config.Profile = SandboxProfileReadOnly
			// Strict without workspace cannot claim path isolation; keep
			// network/command/env restrictions and surface the downgrade.
			result.Warnings = append(result.Warnings,
				"sandbox profile strict downgraded to read-only: workspace root is unset; path bounds cannot be enforced (network/command restrictions still apply)")
		} else {
			result.Config.AllowedPaths = []string{workspace}
			result.Config.ReadOnlyPaths = []string{workspace}
		}
	}

	// Overlay explicit map/config fields after profile defaults.
	if SandboxConfigActive(opts.Override) || opts.Override.Enabled || opts.Override.BlockNetwork || opts.Override.Profile != "" {
		OverlaySandboxConfig(&result.Config, opts.Override)
		// Preserve effective profile label unless override sets one.
		if strings.TrimSpace(result.Config.Profile) == "" {
			result.Config.Profile = result.Effective
		}
		if result.Config.Enabled || SandboxConfigActive(result.Config) {
			result.Config.Enabled = true
		}
	}

	if result.Config.Enabled || SandboxConfigActive(result.Config) {
		result.Config.Enabled = true
	}
	if result.Effective == "" {
		result.Effective = SandboxProfileOff
	}
	if result.Config.Profile == "" {
		result.Config.Profile = result.Effective
	}
	return result, nil
}

// ResolveSandboxMap materializes sandbox config from a profile/agentdef sandbox map.
// Recognized keys: mode/profile (named profile) plus raw SandboxConfig fields.
func ResolveSandboxMap(raw map[string]interface{}, opts SandboxProfileOptions) (SandboxProfileResult, error) {
	if len(raw) == 0 {
		return ResolveSandboxProfile("", opts)
	}

	profile := firstSandboxMapString(raw, "mode", "profile", "sandbox")
	// Build override from remaining raw fields (ignore mode/profile labels).
	override, err := decodeSandboxMapConfig(raw)
	if err != nil {
		return SandboxProfileResult{}, err
	}
	opts.Override = override

	// If only raw fields and no named mode, treat as manual config.
	if strings.TrimSpace(profile) == "" {
		cfg := override
		if !cfg.Enabled {
			cfg.Enabled = SandboxConfigActive(cfg)
		}
		return SandboxProfileResult{
			Requested: "",
			Effective: strings.TrimSpace(cfg.Profile),
			Config:    cfg,
			ReadOnly:  false,
		}, nil
	}
	return ResolveSandboxProfile(profile, opts)
}

// DefaultReadOnlyDeniedCommands returns shell/interpreter launchers blocked under
// read-only application sandbox (mirrors API mutation policy defaults).
func DefaultReadOnlyDeniedCommands() []string {
	return []string{"sh", "bash", "zsh", "fish", "cmd", "powershell", "pwsh", "python", "python3", "node"}
}

// DefaultStrictDeniedCommands extends read-only denials for strict profile.
func DefaultStrictDeniedCommands() []string {
	return append(DefaultReadOnlyDeniedCommands(), "ruby", "perl", "php", "lua", "deno", "bun")
}

// DefaultStrictAllowedCommands is a conservative allowlist for strict shell use.
// Empty AllowedCommands means "no allowlist" in Sandbox.ValidateCommand; strict
// therefore sets an explicit allowlist so only known-safe read tools pass.
func DefaultStrictAllowedCommands() []string {
	return []string{"git", "rg", "grep", "findstr", "ls", "dir", "cat", "type", "head", "tail", "wc", "stat", "pwd", "echo", "where", "which"}
}

// DefaultStrictEnvWhitelist limits process environment under strict profile.
func DefaultStrictEnvWhitelist() []string {
	return []string{"PATH", "PATHEXT", "HOME", "USERPROFILE", "SystemRoot", "TEMP", "TMP", "LANG", "LC_ALL", "TERM"}
}

func firstSandboxMapString(raw map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			if s, ok := value.(string); ok {
				if trimmed := strings.TrimSpace(s); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

func firstMapString(raw map[string]interface{}, keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			if s, ok := value.(string); ok {
				if trimmed := strings.TrimSpace(s); trimmed != "" {
					return trimmed, true
				}
			}
		}
	}
	return "", false
}

// decodeSandboxMapConfig extracts SandboxConfig fields from a free-form map,
// ignoring named-profile keys (mode/profile/sandbox).
func decodeSandboxMapConfig(raw map[string]interface{}) (SandboxConfig, error) {
	if len(raw) == 0 {
		return SandboxConfig{}, nil
	}
	filtered := make(map[string]interface{}, len(raw))
	for key, value := range raw {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "mode", "profile", "sandbox":
			continue
		default:
			filtered[key] = value
		}
	}
	if len(filtered) == 0 {
		return SandboxConfig{}, nil
	}
	// Reuse yaml round-trip via existing overlay callers; keep local decode
	// free of yaml import by mapping known keys explicitly.
	cfg := SandboxConfig{}
	if v, ok := filtered["enabled"].(bool); ok {
		cfg.Enabled = v
	}
	if v, ok := filtered["blockNetwork"].(bool); ok {
		cfg.BlockNetwork = v
	}
	if v, ok := filtered["block_network"].(bool); ok {
		cfg.BlockNetwork = v
	}
	if v, ok := filtered["profile"].(string); ok {
		cfg.Profile = strings.TrimSpace(v)
	}
	if v, ok := firstMapString(filtered, "osSandbox", "os_sandbox", "osSandboxMode", "os_sandbox_mode"); ok {
		cfg.OSSandbox = v
	}
	cfg.AllowedPaths = coerceMapStringSlice(filtered, "allowedPaths", "allowed_paths")
	cfg.DeniedPaths = coerceMapStringSlice(filtered, "deniedPaths", "denied_paths")
	cfg.ReadOnlyPaths = coerceMapStringSlice(filtered, "readOnlyPaths", "read_only_paths")
	cfg.AllowedCommands = coerceMapStringSlice(filtered, "allowedCommands", "allowed_commands")
	cfg.DeniedCommands = coerceMapStringSlice(filtered, "deniedCommands", "denied_commands")
	cfg.EnvWhitelist = coerceMapStringSlice(filtered, "envWhitelist", "env_whitelist")
	cfg.AllowedHosts = coerceMapStringSlice(filtered, "allowedHosts", "allowed_hosts")
	cfg.DeniedHosts = coerceMapStringSlice(filtered, "deniedHosts", "denied_hosts")
	if rawDur, ok := filtered["maxExecutionTime"]; ok {
		switch typed := rawDur.(type) {
		case string:
			if d, err := time.ParseDuration(strings.TrimSpace(typed)); err == nil {
				cfg.MaxExecutionTime = d
			}
		case int:
			if typed > 0 {
				cfg.MaxExecutionTime = time.Duration(typed) * time.Second
			}
		case int64:
			if typed > 0 {
				cfg.MaxExecutionTime = time.Duration(typed) * time.Second
			}
		case float64:
			if typed > 0 {
				cfg.MaxExecutionTime = time.Duration(typed) * time.Second
			}
		case time.Duration:
			if typed > 0 {
				cfg.MaxExecutionTime = typed
			}
		}
	}
	return cfg, nil
}

func coerceMapStringSlice(raw map[string]interface{}, keys ...string) []string {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			if out, ok := coerceInterfaceStringSlice(value); ok {
				return out
			}
		}
	}
	return nil
}

func coerceInterfaceStringSlice(value interface{}) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...), true
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out, true
	default:
		return nil, false
	}
}
