package hooks

import "time"

// HookConfig defines a single hook entry.
type HookConfig struct {
	ID      string        `json:"id" yaml:"id"`
	Event   Event         `json:"event" yaml:"event"`
	Match   MatchConfig   `json:"match,omitempty" yaml:"match,omitempty"`
	Exec    ExecConfig    `json:"exec" yaml:"exec"`
	Timeout time.Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	OnError string        `json:"on_error,omitempty" yaml:"on_error,omitempty"` // fail_open | fail_closed
}

// MatchConfig controls hook matching.
type MatchConfig struct {
	Tools        []string `json:"tools,omitempty" yaml:"tools,omitempty"`
	PathGlobs    []string `json:"path_glob,omitempty" yaml:"path_glob,omitempty"`
	CommandGlobs []string `json:"command_glob,omitempty" yaml:"command_glob,omitempty"`
}

// ExecConfig describes how to run a hook.
type ExecConfig struct {
	Type    string            `json:"type" yaml:"type"` // shell | http
	Cmd     []string          `json:"cmd,omitempty" yaml:"cmd,omitempty"`
	URL     string            `json:"url,omitempty" yaml:"url,omitempty"`
	Method  string            `json:"method,omitempty" yaml:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
}
