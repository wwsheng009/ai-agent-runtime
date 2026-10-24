package remotetools

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	toolcoreoutput "github.com/wwsheng009/ai-agent-runtime/internal/toolcore/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/shellexec"
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

	captureLimit := DefaultMaxOutputBytes
	if t.cfg != nil && t.cfg.Limits.MaxOutputBytes > 0 {
		captureLimit = t.cfg.Limits.MaxOutputBytes
	}

	execResult, runErr := shellexec.Run(ctx, shellexec.Options{
		Command:         command,
		Workdir:         workdir,
		Env:             t.sandbox.FilterEnv(os.Environ()),
		Timeout:         timeout,
		CaptureLimit:    captureLimit,
		DisableArtifact: true,
		QuietNotice:     -1,
		Invocation:      remoteShellInvocation,
	})
	if execResult.Cancelled && !execResult.TimedOut {
		// A client disconnect cancels the request context. It is a control-plane
		// failure, never a content result, and must not be reported as a command
		// timeout: the command may have produced output before the disconnect.
		metadata := map[string]interface{}{
			"command":     command,
			"workdir":     t.cleanDisplayPath(workdir),
			"duration_ms": execResult.Duration.Milliseconds(),
			"timed_out":   false,
			"exit_code":   -1,
			"remote":      true,
			"workspace":   effective.Root(),
			"policy_rule": rule,
		}
		metadata["error_code"] = toolcore.ErrCancelled
		metadata["next_action"] = "retry the call if the command must complete"
		body, metadata := t.shapeModelOutput(ToolShell, execResult.Capture.Output, metadata)
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: "text",
			Content:    body,
			Error:      fmt.Errorf("request cancelled"),
			Metadata:   metadata,
		}, nil
	}
	if !execResult.Started && runErr != nil {
		return remoteErrorResult(newToolError(
			CodeCommandDenied,
			"check that the command exists on the target",
			"cannot start command: %v", runErr,
		)), nil
	}
	capture := execResult.Capture.Output
	exitCode := execResult.ExitCode
	timedOut := execResult.TimedOut
	duration := execResult.Duration

	display := capture
	switch {
	case timedOut:
		display = fmt.Sprintf("Command timed out after %s.\n\n%s", timeout, capture)
	case runErr != nil:
		display = fmt.Sprintf("Command failed: %v\n\n%s", runErr, capture)
	}
	metadata := map[string]interface{}{
		"command":              command,
		"workdir":              t.cleanDisplayPath(workdir),
		"duration_ms":          duration.Milliseconds(),
		"timeout_ms":           timeout.Milliseconds(),
		"timed_out":            timedOut,
		"exit_code":            exitCode,
		"output_bytes":         len(capture),
		"total_output_bytes":   execResult.Capture.TotalBytes,
		"total_output_lines":   execResult.Capture.TotalLines,
		"output_truncated":     execResult.Capture.Truncated,
		"output_capture_limit": execResult.Capture.CaptureLimitBytes,
		"remote":               true,
		"workspace":            effective.Root(),
		"policy_rule":          rule,
	}
	if execResult.Capture.CaptureLimitDisabled {
		metadata["output_capture_limit_disabled"] = true
	}
	if execResult.Capture.OmittedBytes > 0 {
		metadata["omitted_output_bytes"] = execResult.Capture.OmittedBytes
	}
	if execResult.GuardReport.TreeKill {
		metadata["process_tree_kill"] = true
		metadata["killed_pids"] = execResult.GuardReport.Killed
	}
	if strings.TrimSpace(execResult.GuardReport.Mode) != "" {
		metadata["process_tree_mode"] = execResult.GuardReport.Mode
	}
	if execResult.WaitDelayUsed {
		metadata["wait_delay_used"] = true
	}
	// Archive the complete capture before folding, so artifact_read can page the
	// omitted middle. The notice is charged against the same model-visible
	// window; folding first and then trimming the body to make room would drop
	// the tail verdicts the head+tail window exists to preserve.
	archiveNotice := ""
	if len(capture) > t.modelVisibleCap() && t.artifacts != nil {
		ref, archiveErr := t.artifacts.PutRef(t.cfg.TargetName, []byte(capture))
		if archiveErr != nil || ref.IsZero() {
			// The folded body is not recoverable without the archived capture.
			// Returning success here would silently lose the omitted middle, so
			// the call fails with the documented recovery code instead.
			metadata[toolcoreoutput.KeyErrorCode] = toolcore.ErrArtifactWriteFailed
			metadata[toolcoreoutput.KeyNextAction] = "reduce the command output or fix the target artifact store"
			body, _ := t.shapeModelOutput(ToolShell, display, metadata)
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: "text",
				Content:    body,
				Error:      fmt.Errorf("artifact archive failed: %w", archiveErr),
				Metadata:   metadata,
			}, nil
		}
		metadata[toolcoreoutput.KeyArtifactRef] = ref.String()
		metadata[toolcoreoutput.KeyArtifactID] = ref.ID
		metadata["artifact_total_bytes"] = len(capture)
		archiveNotice = fmt.Sprintf("\n[full capture archived as %s; page it with artifact_read artifact_id=%s]\n", ref.String(), ref.ID)
	}
	shapeBudget := 0
	if archiveNotice != "" {
		budget := toolcoreoutput.ResolveCap(ToolShell, 0, 0, t.modelVisibleCap())
		shapeBudget = budget - len(archiveNotice)
		if shapeBudget < 256 {
			shapeBudget = 0 // degenerate window: keep the ordinary fold
		}
	}
	pageBytes := toolcoreoutput.ShellOutputBudgetBytes - 512
	if pageBytes < 1024 {
		pageBytes = 1024
	}
	// archived tells the notice whether the omitted middle can be retrieved.
	// Without an artifact store nothing was written, and artifact_read is not
	// even registered on this surface - promising it would send the model after
	// a tool that does not exist.
	body, shapedMetadata, _, _ := toolcoreoutput.ShapeShellWindowWithArchive(
		ToolShell, display, metadata, shapeBudget, t.modelVisibleCap(), pageBytes, t.artifacts != nil)
	metadata = shapedMetadata
	if archiveNotice != "" {
		if shapeBudget > 0 {
			body += archiveNotice
		} else {
			body = toolcoreoutput.JoinNotice(body, archiveNotice, t.modelVisibleCap())
		}
		metadata[toolcoreoutput.KeyModelVisibleBudget] = t.modelVisibleCap()
	}

	switch {
	case timedOut:
		metadata["error_code"] = CodeTimeout
		metadata["next_action"] = "narrow the command or raise the target timeout"
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: "text",
			Content:    body,
			Error:      fmt.Errorf("command timed out after %s", timeout),
			Metadata:   metadata,
		}, nil
	case runErr != nil:
		metadata["error_code"] = CodeCommandFailed
		metadata["next_action"] = "check the command output and the target environment"
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: "text",
			Content:    body,
			Error:      fmt.Errorf("command failed: %w", runErr),
			Metadata:   metadata,
		}, nil
	}
	if boolMetadata(metadata, toolcoreoutput.KeyIsTruncated) {
		metadata["next_action"] = "narrow the command output (grep/tail) or raise max-output-bytes"
	}
	return remoteTextResult(body, metadata), nil
}

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
