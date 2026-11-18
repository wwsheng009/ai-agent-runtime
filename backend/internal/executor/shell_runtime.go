package executor

import (
	"fmt"
	"strings"
)

// ShellRuntime is one fully resolved shell execution environment.
//
// The process-wide resolver (ConfigureShell/CurrentShellE) is still the
// compatibility path for single-project hosts such as aicli. A long-lived
// runtime-server serves multiple workspace directories in one process, so it
// must carry the workspace-selected shell, script transport and environment
// with the session/tool invocation instead of mutating the global singleton.
//
// A ShellRuntime is immutable after construction.
type ShellRuntime struct {
	Shell       Shell
	Config      ShellConfig
	Resolution  *ShellResolution
	Delivery    ScriptDelivery
	Env         map[string]string
	SourcePath  string
	Fingerprint string
	// ResolveErr records a strictProbe/load failure. Execution callers must fail
	// closed on it; display surfaces may still use the fallback shell.
	ResolveErr error
}

// ResolveShellRuntime resolves cfg into an execution runtime.
//
// The resolution is memoized by ResolveShell on the config fingerprint, so
// callers may resolve per workspace/session without paying a probe per
// command. When strictProbe fails, the runtime is still returned (for
// diagnostics/display) together with the error; execution callers must fail
// closed on that error.
func ResolveShellRuntime(cfg ShellConfig) (*ShellRuntime, error) {
	resolution, err := ResolveShell(cfg)
	if resolution == nil {
		return nil, fmt.Errorf("%w: resolution returned no shell", ErrShellUnavailable)
	}
	runtime := shellRuntimeFromResolution(cfg, resolution, "")
	runtime.ResolveErr = err
	return runtime, err
}

// CurrentShellRuntime returns the process-wide configured runtime. It is the
// compatibility path for CLI/TUI and for tool contexts that did not attach a
// per-session runtime.
func CurrentShellRuntime() (*ShellRuntime, error) {
	resolution, err := CurrentResolution()
	if resolution == nil {
		return nil, fmt.Errorf("%w: resolution returned no shell", ErrShellUnavailable)
	}
	runtime := shellRuntimeFromResolution(ShellConfigSnapshot(), resolution, "")
	runtime.ResolveErr = err
	return runtime, err
}

// ShellRuntimeFromResolution builds a runtime from an already-probed
// resolution. It is exported for hosts that cache their own resolutions.
func ShellRuntimeFromResolution(cfg ShellConfig, resolution *ShellResolution, sourcePath string) *ShellRuntime {
	return shellRuntimeFromResolution(cfg, resolution, sourcePath)
}

func shellRuntimeFromResolution(cfg ShellConfig, resolution *ShellResolution, sourcePath string) *ShellRuntime {
	runtime := &ShellRuntime{
		Config:     cfg,
		Delivery:   cfg.EffectiveScriptDelivery(),
		Env:        cloneShellEnv(cfg.Env),
		SourcePath: strings.TrimSpace(sourcePath),
	}
	if resolution != nil {
		runtime.Resolution = resolution
		runtime.Shell = resolution.Shell
	}
	runtime.Fingerprint = shellRuntimeFingerprint(runtime)
	return runtime
}

// ApplyEnv layers the runtime's shell.env overrides on top of base.
func (r *ShellRuntime) ApplyEnv(base []string) []string {
	if r == nil || len(r.Env) == 0 {
		return base
	}
	return MergeEnvOverrides(base, r.Env)
}

// EffectiveDelivery returns the configured transport, defaulting to stdin.
func (r *ShellRuntime) EffectiveDelivery() ScriptDelivery {
	if r == nil || r.Delivery == "" {
		return ScriptDeliveryStdin
	}
	return r.Delivery
}

// EnvSnapshot returns a copy of the shell.env overrides.
func (r *ShellRuntime) EnvSnapshot() map[string]string {
	if r == nil || len(r.Env) == 0 {
		return nil
	}
	return cloneShellEnv(r.Env)
}

// Fingerprint identifies the exact runtime for caches. It includes fields that
// affect execution but not only candidate selection (scriptDelivery).
func (r *ShellRuntime) RuntimeFingerprint() string {
	if r == nil {
		return ""
	}
	if strings.TrimSpace(r.Fingerprint) != "" {
		return r.Fingerprint
	}
	return shellRuntimeFingerprint(r)
}

func shellRuntimeFingerprint(r *ShellRuntime) string {
	if r == nil {
		return ""
	}
	parts := []string{
		r.Config.Fingerprint(),
		"shell=" + strings.TrimSpace(r.Shell.Path),
		"type=" + string(r.Shell.Type),
		"id=" + strings.TrimSpace(r.Shell.ID),
		"delivery=" + string(r.EffectiveDelivery()),
		"source=" + strings.TrimSpace(r.SourcePath),
	}
	return strings.Join(parts, "|")
}

func cloneShellEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for key, value := range env {
		out[key] = value
	}
	return out
}
