package remotetools

// envd_process.go adds the process surface to the envd backend: one-shot command
// execution and a bounded directory-watch window.
//
// Command authorization is NOT implemented here. envd filters nothing — it runs
// whatever argv it is handed as whatever user the credentials name — so every
// command goes through the same remotetools.CommandPolicy the ssh/http surface
// uses (deny list > non-empty allow list > shell metacharacters > destructive
// classifier). That sharing is the reason this file can stay short: the policy is
// one implementation for both backends, and an empty allowlist is fail-closed on
// both.
//
// watch_dir exists because envd's WatchDir is a server stream with no natural
// end: exposing the stream directly would leave it (and a goroutine on the
// target) alive after the MCP call returned. The tool therefore owns the stream
// for exactly one call — bounded by wait_ms and max_events, then closed.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// ToolWatchDir is an envd-only capability: the local backend has editing hooks,
// not a real filesystem watcher.
const ToolWatchDir = "watch_dir"

// Bounded-window defaults for watch_dir.
const (
	envdWatchDefaultWait  = 5 * time.Second
	envdWatchMaxWait      = 30 * time.Second
	envdWatchDefaultCount = 50
	envdWatchMaxCount     = 500
)

// envdProcessToolSpecs publishes the process-surface tools.
func envdProcessToolSpecs(base remoteToolBase) []envdToolSpec {
	return []envdToolSpec{
		{
			name:        ToolShell,
			description: base.describe("Run a command on the envd target and return its output. The command must pass the shared command policy (deny list, then an explicit allowlist, shell-metacharacter rejection and the destructive-command classifier); with no allowlist configured every command is refused."),
			version:     "1.0.0",
			parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": envdStringProp("Command line to execute on the target."),
					"workdir": envdStringProp("Working directory for THIS command only (equivalent to workdir on the local backend). An absolute path is used as-is; a relative path is resolved against the workspace root. Omit to use the workspace root. The choice is not remembered by later calls and the directory must already exist."),
					"cwd":     envdStringProp("Deprecated alias of workdir, kept for compatibility. If both workdir and cwd are supplied they must be identical."),
				},
				"required": []string{"command"},
			},
			call: envdShell,
		},
		{
			name:        ToolWatchDir,
			description: base.describe("Collect a bounded window of directory-change events on the envd target. The watch stream is owned by this one call and closed before it returns, so continuous monitoring means repeated calls."),
			version:     "1.0.0",
			parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": envdStringProp("Directory to watch."),
					"wait_ms": map[string]interface{}{
						"type":        "integer",
						"description": "How long to wait for events, in milliseconds (default 5000, max 30000).",
					},
					"max_events": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum events to return (default 50, max 500).",
					},
				},
				"required": []string{"path"},
			},
			call: envdWatchDir,
		},
	}
}

// envdShell runs one command through envd's process stream after the shared
// policy has accepted it. The working directory is resolved through the jail,
// because a command started outside the workspace can read and write anything the
// executing user can.
func envdShell(ctx context.Context, be *envdBackend, params map[string]interface{}) (*toolkit.ToolResult, error) {
	command, hasCommand := envdParamString(params, "command")
	if !hasCommand || strings.TrimSpace(command) == "" {
		return envdErrorResult("command is required"), nil
	}
	rule, err := be.commands.CheckCommand(command)
	if err != nil {
		return envdErrorResult("command denied (%s): %v", rule, err), nil
	}
	requestedDir, conflict := envdDirParam(params)
	if conflict != "" {
		return envdErrorResult("%s", conflict), nil
	}
	cwd := be.workspace
	if requestedDir != "" {
		resolved, resolveErr := be.resolvePath(ctx, requestedDir)
		if resolveErr != nil {
			return envdErrorResult("workdir: %v", resolveErr), nil
		}
		cwd = resolved
	}

	stdout, stderr, code, err := be.client.Run(ctx, envdclient.StartRequest{
		Process: envdclient.ProcessConfig{
			Cmd:  "/bin/sh",
			Args: []string{"-c", command},
			Cwd:  cwd,
		},
	})
	if err != nil {
		return envdErrorResult("shell: %v", err), nil
	}
	var builder strings.Builder
	if len(stdout) > 0 {
		builder.Write(stdout)
	}
	if len(stderr) > 0 {
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		fmt.Fprintf(&builder, "[stderr]\n%s", string(stderr))
	}
	fmt.Fprintf(&builder, "\n[exit %d]", code)
	return envdTextResult(builder.String()), nil
}

// envdWatchDir captures a bounded window of change events and always closes the
// stream it opened.
func envdWatchDir(ctx context.Context, be *envdBackend, params map[string]interface{}) (*toolkit.ToolResult, error) {
	raw := envdStringParam(params, "path")
	if raw == "" {
		return envdErrorResult("path is required"), nil
	}
	dir, err := be.resolvePath(ctx, raw)
	if err != nil {
		return envdErrorResult("%v", err), nil
	}
	wait := time.Duration(envdIntParam(params, "wait_ms", int(envdWatchDefaultWait/time.Millisecond))) * time.Millisecond
	if wait <= 0 {
		wait = envdWatchDefaultWait
	}
	if wait > envdWatchMaxWait {
		wait = envdWatchMaxWait
	}
	maxEvents := envdIntParam(params, "max_events", envdWatchDefaultCount)
	if maxEvents <= 0 {
		maxEvents = envdWatchDefaultCount
	}
	if maxEvents > envdWatchMaxCount {
		maxEvents = envdWatchMaxCount
	}

	watchCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	stream, err := be.client.WatchDir(watchCtx, dir)
	if err != nil {
		return envdErrorResult("watch %s: %v", dir, err), nil
	}
	defer func() { _ = stream.Close() }()

	started := time.Now()
	events := make([]envdclient.WatchEvent, 0, maxEvents)
	var (
		streamEnded bool
		streamErr   error
	)
	for len(events) < maxEvents {
		var event envdclient.WatchEvent
		if recvErr := stream.Recv(&event); recvErr != nil {
			switch {
			case errors.Is(recvErr, io.EOF):
				streamEnded = true
			case watchCtx.Err() != nil:
				// The bounded window expired: a normal stop, not a failure.
			default:
				streamErr = recvErr
			}
			break
		}
		if event.Start {
			continue
		}
		events = append(events, event)
	}

	if len(events) == 0 {
		switch {
		case streamErr != nil:
			return envdErrorResult("watch %s: %v", dir, streamErr), nil
		case streamEnded:
			return envdTextResult(fmt.Sprintf("watch %s: stream closed before any change event", dir)), nil
		default:
			return envdTextResult(fmt.Sprintf("watch %s: no change events within %s", dir, wait.Round(time.Millisecond))), nil
		}
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "watch %s: %d event(s) in %s\n", dir, len(events), time.Since(started).Round(time.Millisecond))
	for _, event := range events {
		kind := event.Type
		if kind == "" {
			kind = "(unknown)"
		}
		fmt.Fprintf(&builder, "%s %q\n", kind, event.Name)
	}
	if streamErr != nil {
		fmt.Fprintf(&builder, "[stream error: %v]", streamErr)
	}
	return envdTextResult(strings.TrimRight(builder.String(), "\n")), nil
}

// envdIntParam reads an integer parameter with a default.
func envdIntParam(params map[string]interface{}, key string, fallback int) int {
	if value, ok := envdParamInt(params, key); ok && value != 0 {
		return value
	}
	return fallback
}
