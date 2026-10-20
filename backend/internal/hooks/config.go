package hooks

import (
	"strings"
	"time"
)

// Default handler timeouts, in seconds.
//
// The original default was 3s for every handler type. That is too tight for a
// real command hook: a cold Python/Node startup alone can approach a second on
// Windows, and a lint or API call easily exceeds it. Because failures default to
// fail-open, an under-budget hook silently does nothing.
const (
	// defaultCommandTimeout is applied to `command` handlers.
	defaultCommandTimeout = 10 * time.Second
	// defaultHTTPTimeout is applied to `http` handlers. Remote endpoints are
	// expected to answer quickly; a slow one should not stall a turn.
	defaultHTTPTimeout = 5 * time.Second
	// defaultHookTimeout is the value reported for handlers with no explicit
	// timeout (diagnostics/dry-run), matching the command default.
	defaultHookTimeout = defaultCommandTimeout
)

// HookConfig defines a single hook entry.
//
// The original fields (ID/Event/Match/Exec/Timeout/OnError) remain the native
// runtime contract. The additional fields model the Codex/Claude-style
// declarative hooks surface. They are optional and default to the legacy
// behavior so existing runtime.yaml files keep working unchanged.
type HookConfig struct {
	ID      string        `json:"id" yaml:"id"`
	Event   Event         `json:"event" yaml:"event"`
	Match   MatchConfig   `json:"match,omitempty" yaml:"match,omitempty"`
	Exec    ExecConfig    `json:"exec" yaml:"exec"`
	Timeout time.Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	OnError string        `json:"on_error,omitempty" yaml:"on_error,omitempty"` // fail_open | fail_closed

	// Async marks a handler that may run without blocking the enclosing turn.
	// Only observational effects (messages, telemetry) are applied; control
	// effects (block/modify/continue:false) are ignored for async handlers,
	// matching Codex's execution-mode contract.
	Async bool `json:"async,omitempty" yaml:"async,omitempty"`
	// StatusMessage is shown by hosts while a synchronous handler is running.
	StatusMessage string `json:"status_message,omitempty" yaml:"status_message,omitempty"`
	// AdditionalContextLimit is the approximate token threshold above which
	// additionalContext is spilled to disk. Zero uses the runtime default;
	// negative disables spilling for this handler.
	AdditionalContextLimit int `json:"additional_context_limit,omitempty" yaml:"additional_context_limit,omitempty"`
	// Source records where this handler was discovered (user/project/plugin/
	// managed/runtime). It is diagnostic metadata and never participates in
	// matching.
	Source string `json:"source,omitempty" yaml:"source,omitempty"`
	// SourcePath is the absolute config path that declared this handler.
	SourcePath string `json:"source_path,omitempty" yaml:"source_path,omitempty"`
	// Matcher is the raw Codex matcher pattern. MatchConfig remains the native
	// structured matcher; both are evaluated with AND semantics when present.
	Matcher string `json:"matcher,omitempty" yaml:"matcher,omitempty"`
	// Enabled is a tri-state override used by hooks state files. Nil means
	// "not specified"; false disables the handler even when trusted.
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
}

// MatchConfig controls hook matching.
type MatchConfig struct {
	Tools        []string `json:"tools,omitempty" yaml:"tools,omitempty"`
	PathGlobs    []string `json:"path_glob,omitempty" yaml:"path_glob,omitempty"`
	CommandGlobs []string `json:"command_glob,omitempty" yaml:"command_glob,omitempty"`
}

// EffectiveTimeout returns the timeout that will actually be applied when this
// handler runs.
//
// Executors used to pick their own default inline. That made diagnostics lie:
// `hooks list` and the harness API reported `timeout_seconds: 0` for a handler
// that really ran with 10s, so an operator could not tell "no timeout" from
// "unset, therefore 10s". Keeping the decision in one place removes that gap.
//
// mcp_tool handlers return 0: they are bounded by the caller's context, not by
// HookConfig.Timeout.
func (h HookConfig) EffectiveTimeout() time.Duration {
	if h.Timeout > 0 {
		return h.Timeout
	}
	switch strings.ToLower(strings.TrimSpace(h.Exec.Type)) {
	case "http":
		return defaultHTTPTimeout
	case "mcp":
		return 0
	default:
		return defaultCommandTimeout
	}
}

// ExecConfig describes how to run a hook.
type ExecConfig struct {
	Type string   `json:"type" yaml:"type"` // shell | http
	Cmd  []string `json:"cmd,omitempty" yaml:"cmd,omitempty"`
	// Command is the shell command string form used by Codex-compatible
	// hooks.json entries. When both Command and Cmd are set, Cmd wins.
	Command string `json:"command,omitempty" yaml:"command,omitempty"`
	// CommandWindows overrides Command on Windows, mirroring Codex's
	// commandWindows field.
	CommandWindows string `json:"command_windows,omitempty" yaml:"command_windows,omitempty"`
	// Server/Tool/Input describe a Codex mcp_tool handler. They are used when
	// Type is "mcp".
	Server  string                 `json:"server,omitempty" yaml:"server,omitempty"`
	Tool    string                 `json:"tool,omitempty" yaml:"tool,omitempty"`
	Input   map[string]interface{} `json:"input,omitempty" yaml:"input,omitempty"`
	URL     string                 `json:"url,omitempty" yaml:"url,omitempty"`
	Method  string                 `json:"method,omitempty" yaml:"method,omitempty"`
	Headers map[string]string      `json:"headers,omitempty" yaml:"headers,omitempty"`
}
