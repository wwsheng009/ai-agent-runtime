package remotetools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// ToolShell is the canonical name of the remote command tool.
const ToolShell = "shell"

// remoteShellTool runs one command on the remote host under the command policy.
//
// Design notes (review S4 / D7):
//
//   - the command string is executed through the platform shell, because that is
//     what "run this command" means to a model, but the policy checks deny,
//     allowlist, metachars and destructive classification first;
//   - the process runs in its own process group so a timeout kills the whole
//     tree instead of leaving grandchildren behind;
//   - output is folded to the model-visible window and the truncation is stated
//     explicitly (a remote call has no artifact store to page from).
type remoteShellTool struct {
	*toolkit.BaseTool
	remoteToolBase
	sandbox *Sandbox
	// artifacts archives the full capture before folding. Without it a folded
	// output loses its middle permanently, because a remote call has no local
	// artifact store to page from (design §D6).
	artifacts *ArtifactStore
}

func newRemoteShellTool(cfg *Config, workspace *Workspace, sandbox *Sandbox, artifacts *ArtifactStore) *remoteShellTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Command to run on the remote host. One command; pipes, redirection and substitution are refused by policy.",
			},
			"workdir": map[string]interface{}{
				"type":        "string",
				"description": "Working directory, relative to the remote workspace. Default: workspace root.",
			},
			"timeout_ms": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "Optional command timeout in milliseconds. Defaults to the server-configured limit.",
			},
		},
		"required":             []string{"command"},
		"additionalProperties": false,
	}
	tool := &remoteShellTool{
		remoteToolBase: remoteToolBase{cfg: cfg, workspace: workspace},
		sandbox:        sandbox,
		artifacts:      artifacts,
	}
	tool.BaseTool = toolkit.NewBaseTool(
		ToolShell,
		tool.describe("Run one command on the remote host inside the workspace, subject to the target's command policy."),
		"1.0.0",
		parameters,
		true,
	)
	return tool
}

func (t *remoteShellTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if err := contextErr(ctx); err != nil {
		return remoteErrorResult(err), nil
	}
	command, _ := params["command"].(string)
	if strings.TrimSpace(command) == "" {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "pass command", "command is required")), nil
	}
	if t.sandbox == nil {
		return remoteErrorResult(newToolError(CodeCommandDenied, "enable profile ops and configure allowedCommands", "shell is not enabled on this target")), nil
	}
	rule, err := t.sandbox.CheckCommand(command)
	if err != nil {
		result := remoteErrorResult(err)
		if result.Metadata == nil {
			result.Metadata = map[string]interface{}{}
		}
		result.Metadata["policy_rule"] = rule
		return result, nil
	}

	// The default cwd follows the session's effective workspace. When the client
	// narrowed the session with MCP roots, falling back to the server's base root
	// would hand the command a directory outside the declaration.
	effective := workspaceForCall(ctx, t.workspace)
	workdir := effective.Root()
	if raw, ok := params["workdir"].(string); ok && strings.TrimSpace(raw) != "" {
		resolved, resolveErr := t.resolve(ctx, raw)
		if resolveErr != nil {
			return remoteErrorResult(resolveErr), nil
		}
		isDir, dirErr := isDirectory(resolved)
		if dirErr != nil {
			return remoteErrorResult(newToolError(CodeInvalidArgument, "check the directory", "cannot stat workdir: %v", dirErr)), nil
		}
		if !isDir {
			return remoteErrorResult(newToolError(CodeInvalidArgument, "pass a directory path", "workdir is not a directory: %s", t.cleanDisplayPath(resolved))), nil
		}
		workdir = resolved
	}

	timeout := t.sandbox.Timeout()
	if requested := intParam(params, "timeout_ms", 0); requested > 0 {
		timeout = time.Duration(requested) * time.Millisecond
	}

	limit := t.cfg.Limits.MaxOutputBytes
	if limit <= 0 {
		limit = DefaultMaxOutputBytes
	}

	started := time.Now()
	capture, exitCode, runErr, timedOut := runRemoteCommand(ctx, command, workdir, timeout, t.sandbox, limit)
	duration := time.Since(started)

	body, truncated := foldRemoteOutput(capture, limit)
	metadata := map[string]interface{}{
		"command":      command,
		"workdir":      t.cleanDisplayPath(workdir),
		"duration_ms":  duration.Milliseconds(),
		"timeout_ms":   timeout.Milliseconds(),
		"timed_out":    timedOut,
		"exit_code":    exitCode,
		"output_bytes": len(capture),
		"is_truncated": truncated,
		"remote":       true,
		"workspace":    effective.Root(),
		"policy_rule":  rule,
	}
	if truncated && t.artifacts != nil {
		// Archive the complete capture before the model sees only the window, so
		// artifact_read can page the omitted middle. A failure here must not fail
		// the command: the folded body is still accurate about what was dropped.
		if artifactID, archiveErr := t.artifacts.Put([]byte(capture)); archiveErr == nil && artifactID != "" {
			metadata["artifact_id"] = artifactID
			metadata["artifact_total_bytes"] = len(capture)
			body += fmt.Sprintf("\n[full capture archived as %s; page it with artifact_read artifact_id=%s]\n", artifactID, artifactID)
		} else if archiveErr != nil {
			fmt.Fprintf(os.Stderr, "aicli-remote-tools: artifact archive failed: %v\n", archiveErr)
		}
	}

	switch {
	case timedOut:
		metadata["error_code"] = CodeTimeout
		metadata["next_action"] = "narrow the command or raise the target timeout"
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: "text",
			Content:    fmt.Sprintf("Command timed out after %s.\n\n%s", timeout, body),
			Error:      fmt.Errorf("command timed out after %s", timeout),
			Metadata:   metadata,
		}, nil
	case runErr != nil:
		metadata["error_code"] = CodeCommandFailed
		metadata["next_action"] = "check the command output and the target environment"
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: "text",
			Content:    fmt.Sprintf("Command failed: %v\n\n%s", runErr, body),
			Error:      fmt.Errorf("command failed: %w", runErr),
			Metadata:   metadata,
		}, nil
	}
	if truncated {
		metadata["next_action"] = "narrow the command output (grep/tail) or raise max-output-bytes"
	}
	return remoteTextResult(body, metadata), nil
}

// runRemoteCommand executes one command and returns its combined output.
//
// The result reports whether the timeout fired so the caller can classify it as
// TIMEOUT rather than as a generic failure.
func runRemoteCommand(ctx context.Context, command, workdir string, timeout time.Duration, sandbox *Sandbox, limit int) (string, int, error, bool) {
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shell, args := remoteShellInvocation(command)
	cmd := exec.CommandContext(execCtx, shell, args...)
	cmd.Dir = workdir
	// Start from the server environment and keep only the configured names: a
	// remote command must not inherit credentials, but PATH/HOME/LANG are exactly
	// the variables an allowlist exists to forward. Passing nil here would make
	// the whitelist a no-op and leave every command with an empty environment.
	cmd.Env = sandbox.FilterEnv(os.Environ())
	configureProcessGroup(cmd)

	var buffer bytes.Buffer
	// Bounded writer: capture at most limit+headroom so a runaway command cannot
	// exhaust memory before the timeout fires.
	writer := newBoundedWriter(&buffer, limit+remoteOutputHeadroom)
	cmd.Stdout = writer
	cmd.Stderr = writer

	if err := cmd.Start(); err != nil {
		return "", -1, newToolError(CodeCommandDenied, "check that the command exists on the target", "cannot start command: %v", err), false
	}
	// On timeout, exec.CommandContext kills only the direct child. The whole
	// process group must be terminated explicitly, otherwise a timed-out build
	// leaves orphaned compilers running on the target.
	waitErr := cmd.Wait()
	exitCode := 0
	if waitErr != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	// A client disconnect cancels the request context just like a timeout. Kill
	// the whole process group in both cases: exec.CommandContext only kills the
	// direct child, and a build's grandchildren would otherwise survive.
	if execCtx.Err() != nil || (ctx != nil && ctx.Err() != nil) {
		killProcessGroup(cmd)
	}

	if execCtx.Err() == context.DeadlineExceeded {
		return buffer.String(), -1, newToolError(CodeTimeout, "narrow the command or raise the timeout", "command timed out"), true
	}
	if ctx != nil && ctx.Err() != nil {
		return buffer.String(), -1, newToolError(CodeTimeout, "retry the call", "request cancelled"), true
	}
	return buffer.String(), exitCode, waitErr, false
}

// remoteOutputHeadroom keeps a little extra text beyond the model-visible window
// so the fold can show a tail without a second read.
const remoteOutputHeadroom = 32 * 1024

// remoteShellInvocation returns the platform shell and its arguments.
//
// A POSIX target uses `sh -c`; Windows targets are out of scope for P2 (design
// §12 item 4) but the branch keeps the tool from silently doing nothing there.
func remoteShellInvocation(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return windowsShellInvocation(command)
	}
	return "/bin/sh", []string{"-c", command}
}

// foldRemoteOutput folds a capture to the model-visible window.
//
// Unlike the local shell tool there is no artifact store to page the omitted
// middle from, so the fold reports exactly what was dropped and tells the model
// how to narrow the command instead of implying the text is recoverable.
func foldRemoteOutput(capture string, limit int) (string, bool) {
	if limit <= 0 {
		limit = DefaultMaxOutputBytes
	}
	if len(capture) <= limit {
		return capture, false
	}
	headBudget := limit / 2
	tailBudget := limit - headBudget

	head, _ := truncateUTF8Safe(capture, headBudget)
	tailStart := len(capture) - tailBudget
	if tailStart < 0 {
		tailStart = 0
	}
	tail := capture[tailStart:]

	var builder strings.Builder
	builder.WriteString(head)
	fmt.Fprintf(&builder,
		"\n\n…[%d bytes omitted: output truncated at the %d-byte limit; re-run with a narrower command (grep/tail/head) or raise max-output-bytes]…\n\n",
		len(capture)-len(head)-len(tail), limit)
	builder.WriteString(tail)
	return builder.String(), true
}

// boundedWriter keeps at most max bytes, then counts the remainder.
type boundedWriter struct {
	buffer    *bytes.Buffer
	max       int
	discarded int
}

func newBoundedWriter(buffer *bytes.Buffer, max int) *boundedWriter {
	return &boundedWriter{buffer: buffer, max: max}
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if w.buffer == nil {
		return len(p), nil
	}
	remaining := w.max - w.buffer.Len()
	if remaining <= 0 {
		w.discarded += len(p)
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = w.buffer.Write(p[:remaining])
		w.discarded += len(p) - remaining
		return len(p), nil
	}
	_, _ = w.buffer.Write(p)
	return len(p), nil
}

// ensure the executor import is used even when the sandbox path is refactored.
var _ = runtimeexecutor.OpWrite
