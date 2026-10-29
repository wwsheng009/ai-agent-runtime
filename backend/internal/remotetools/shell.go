package remotetools

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/shelljob"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	toolcoreoutput "github.com/wwsheng009/ai-agent-runtime/internal/toolcore/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// ToolShell is the canonical name of the remote command tool.
const ToolShell = "shell"

const emptyCommandOutputNotice = "Command completed successfully with no output."

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
	shell   runtimeexecutor.Shell
	// artifacts archives the full capture before folding. Without it a folded
	// output loses its middle permanently, because a remote call has no local
	// artifact store to page from (design §D6).
	artifacts *ArtifactStore
	// jobs is the single target-side core job runtime shared with job_*.
	jobs *background.Manager
}

func newRemoteShellTool(cfg *Config, workspace *Workspace, sandbox *Sandbox, artifacts *ArtifactStore, jobs *background.Manager) *remoteShellTool {
	shell := resolvedShellForConfig(cfg)
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": shellCommandParameterDescription(shell),
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
		shell:          shell,
		artifacts:      artifacts,
		jobs:           jobs,
	}
	tool.BaseTool = toolkit.NewBaseTool(
		ToolShell,
		tool.describe(shellToolDescription(
			"Run one command on the remote host inside the workspace, subject to the target's command policy. Every call starts a core background job and waits for it; timeout_ms only bounds that wait, so a timed-out command keeps running as a job.",
			shell)),
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
		return remoteErrorResult(newToolError(CodeCommandDenied, "enable profile all/ops and configure allowedCommands", "shell is not enabled on this target")), nil
	}
	rule, err := t.sandbox.CheckCommand(command)
	if err != nil {
		result := remoteErrorResult(err)
		if result.Metadata == nil {
			result.Metadata = map[string]interface{}{}
		}
		result.Metadata["policy_rule"] = rule
		addCommandDiagnostics(result.Metadata, command)
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

	if t.jobs == nil {
		return remoteErrorResult(newToolError(CodeUnsupported, "configure jobs.root on the target", "background job runtime is not configured")), nil
	}
	owner := remoteJobOwner(ctx, t.cfg)
	if owner == "" {
		return remoteErrorResult(newToolError(
			CodeUnsupported,
			"call the tool from a session-bound MCP client; a stateless request cannot own a job",
			"remote shell requires a stable session owner",
		)), nil
	}

	started := time.Now()
	execution, execErr := shelljob.Execute(ctx, shelljob.NewManagerRuntime(t.jobs), shelljob.Request{
		SessionID:   owner,
		Command:     command,
		Workdir:     workdir,
		WaitTimeout: timeout,
		OutputLimit: t.captureLimit(),
		KeepJob:     true,
	})
	if execErr != nil {
		stage := shelljob.StageOf(execErr)
		jobID := shelljob.JobIDOf(execErr)
		switch {
		case stage == "owner":
			return remoteErrorResult(newToolError(
				CodeUnsupported,
				"call the tool from a session-bound MCP client",
				"remote shell requires a stable session owner",
			)), nil
		case stage == "wait" && ctx.Err() != nil:
			metadata := map[string]interface{}{
				"command":     command,
				"workdir":     t.cleanDisplayPath(workdir),
				"duration_ms": time.Since(started).Milliseconds(),
				"timed_out":   false,
				"exit_code":   -1,
				"job_id":      jobID,
				"remote":      true,
				"workspace":   effective.Root(),
				"policy_rule": rule,
				"shell":       shellLabel(t.shell),
				"shell_type":  string(t.shell.Type),
			}
			metadata["error_code"] = toolcore.ErrCancelled
			metadata["next_action"] = "retry the call if the command must complete"
			addCommandDiagnostics(metadata, command)
			body, metadata := t.shapeModelOutput(ToolShell, "", metadata)
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: "text",
				Content:    body,
				Error:      fmt.Errorf("request cancelled"),
				Metadata:   metadata,
			}, nil
		case stage == "read":
			return remoteErrorResult(newToolError(
				CodeInternal,
				fmt.Sprintf("retry job_output job_id=%s offset=0", jobID),
				"read shell job output: %v", execErr,
			)), nil
		default:
			return remoteErrorResult(newToolError(
				CodeCommandDenied,
				"check that the command exists on the target and the jobs.root is writable",
				"cannot start shell job: %v", execErr,
			)), nil
		}
	}

	duration := time.Since(started)
	if !execution.TimedOut {
		// The foreground shell hides a settled job after its output has been
		// archived/formatted. Promoted jobs stay visible for job_output.
		defer func() {
			_ = t.jobs.ForgetJob(context.Background(), execution.JobID, owner)
		}()
	}
	capture := execution.Output
	exitCode := -1
	if execution.ExitCode != nil {
		exitCode = *execution.ExitCode
	}
	display := capture
	switch {
	case execution.TimedOut:
		display = fmt.Sprintf("Command still running after %s; moved to background job %s\n\n%s",
			timeout, execution.JobID, capture)
	case execution.Status == "completed" && exitCode != 0:
		display = fmt.Sprintf("Command failed: exit status %d\n\n%s", exitCode, capture)
	case execution.Status == "failed":
		display = fmt.Sprintf("Command failed: job status failed\n\n%s", capture)
	case execution.Status == "cancelled":
		display = fmt.Sprintf("Command cancelled.\n\n%s", capture)
	case strings.TrimSpace(capture) == "":
		display = emptyCommandOutputNotice
	}
	metadata := map[string]interface{}{
		"command":              command,
		"workdir":              t.cleanDisplayPath(workdir),
		"duration_ms":          duration.Milliseconds(),
		"timeout_ms":           timeout.Milliseconds(),
		"timed_out":            execution.TimedOut,
		"exit_code":            exitCode,
		"output_bytes":         len(capture),
		"total_output_bytes":   len(capture),
		"total_output_lines":   strings.Count(capture, "\n"),
		"output_capture_limit": t.captureLimit(),
		"job_first":            true,
		"remote":               true,
		"workspace":            effective.Root(),
		"policy_rule":          rule,
		"shell":                shellLabel(t.shell),
		"shell_type":           string(t.shell.Type),
	}
	addCommandDiagnostics(metadata, command)
	if execution.TimedOut {
		metadata["job_id"] = execution.JobID
		metadata["job_status"] = execution.Status
		metadata["next_offset"] = execution.NextOffset
		metadata["promoted"] = true
		metadata["next_action"] = fmt.Sprintf("job_output job_id=%s offset=%d", execution.JobID, execution.NextOffset)
	} else if execution.Status == "completed" && strings.TrimSpace(capture) == "" {
		metadata["output_empty"] = true
	}

	body, metadata, failure := t.finalizeShellOutput(capture, display, execution.LogPath, metadata)
	if failure != nil {
		return failure, nil
	}
	if execution.TimedOut {
		return remoteTextResult(body, metadata), nil
	}
	if execution.Status == "completed" && exitCode == 0 {
		if boolMetadata(metadata, toolcoreoutput.KeyIsTruncated) {
			metadata["next_action"] = "narrow the command output (grep/tail) or raise max-output-bytes"
		}
		return remoteTextResult(body, metadata), nil
	}

	metadata["error_code"] = CodeCommandFailed
	if execution.Status == "cancelled" {
		metadata["error_code"] = toolcore.ErrCancelled
		metadata["next_action"] = "start a new shell call if the command is still needed"
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: "text",
			Content:    body,
			Error:      shellFailureError(fmt.Errorf("command cancelled"), toolcore.ErrCancelled, "start a new shell call if the command is still needed"),
			Metadata:   metadata,
		}, nil
	}
	metadata["next_action"] = "check the command output and the target environment"
	return &toolkit.ToolResult{
		Success:    false,
		OutputKind: "text",
		Content:    body,
		Error: shellFailureError(
			fmt.Errorf("command failed: %s", execution.Status),
			CodeCommandFailed, "check the command output and the target environment"),
		Metadata: metadata,
	}, nil
}

func (t *remoteShellTool) captureLimit() int {
	if t == nil || t.cfg == nil {
		return DefaultMaxOutputBytes
	}
	if t.cfg.Limits.MaxOutputBytes > 0 {
		return t.cfg.Limits.MaxOutputBytes
	}
	if t.cfg.Limits.CaptureLimit > 0 {
		return t.cfg.Limits.CaptureLimit
	}
	return DefaultMaxOutputBytes
}

// finalizeShellOutput archives the raw capture before folding. The job output
// may be larger than the model-visible window; archiving first keeps the
// omitted middle pageable through artifact_read.
func (t *remoteShellTool) finalizeShellOutput(capture, display, logPath string, metadata map[string]interface{}) (string, map[string]interface{}, *toolkit.ToolResult) {
	archiveBytes := []byte(capture)
	if strings.TrimSpace(logPath) != "" {
		if full, readErr := os.ReadFile(logPath); readErr == nil {
			archiveBytes = full
		}
	}
	if len(archiveBytes) > len(capture) {
		metadata["total_output_bytes"] = len(archiveBytes)
		metadata["output_truncated"] = true
	}
	archiveNotice := ""
	if len(archiveBytes) > t.modelVisibleCap() && t.artifacts != nil && t.cfg != nil {
		ref, archiveErr := t.artifacts.PutRef(t.cfg.TargetName, archiveBytes)
		if archiveErr != nil || ref.IsZero() {
			if archiveErr == nil {
				archiveErr = fmt.Errorf("artifact store returned an empty reference")
			}
			metadata[toolcoreoutput.KeyErrorCode] = toolcore.ErrArtifactWriteFailed
			metadata[toolcoreoutput.KeyNextAction] = "reduce the command output or fix the target artifact store"
			body, _ := t.shapeModelOutput(ToolShell, display, metadata)
			return body, metadata, &toolkit.ToolResult{
				Success:    false,
				OutputKind: "text",
				Content:    body,
				Error: shellFailureError(
					fmt.Errorf("artifact archive failed: %w", archiveErr),
					toolcore.ErrArtifactWriteFailed,
					"reduce the command output or fix the target artifact store"),
				Metadata: metadata,
			}
		}
		metadata[toolcoreoutput.KeyArtifactRef] = ref.String()
		metadata[toolcoreoutput.KeyArtifactID] = ref.ID
		metadata["artifact_total_bytes"] = len(archiveBytes)
		archiveNotice = fmt.Sprintf("\n[full capture archived as %s; page it with artifact_read artifact_id=%s]\n", ref.String(), ref.ID)
	}
	shapeBudget := 0
	if archiveNotice != "" {
		budget := toolcoreoutput.ResolveCap(ToolShell, 0, 0, t.modelVisibleCap())
		shapeBudget = budget - len(archiveNotice)
		if shapeBudget < 256 {
			shapeBudget = 0
		}
	}
	pageBytes := toolcoreoutput.ShellOutputBudgetBytes - 512
	if pageBytes < 1024 {
		pageBytes = 1024
	}
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
	return body, metadata, nil
}

// shellFailureError builds the error a failed shell call reports.
//
// The code and recovery hint go into the error text on purpose. The MCP
// projection includes Content for failed results, but some clients render only
// Error; keeping the class here means those clients still see an actionable
// failure instead of a bare "command failed: exit status 1".
func shellFailureError(cause error, code, nextAction string) error {
	message := cause.Error()
	if trimmed := strings.TrimSpace(code); trimmed != "" {
		message = trimmed + ": " + message
	}
	if trimmed := strings.TrimSpace(nextAction); trimmed != "" {
		message += " (next: " + trimmed + ")"
	}
	return fmt.Errorf("%s", message)
}

// addCommandDiagnostics exposes the exact command bytes for client-vs-server
// escape diagnosis without changing the model-visible body.
//
// A client that sends a single backslash inside a JSON string can turn
// `common\tenantctx` into a literal TAB before the command reaches the server.
// Conversely, a client that decodes the returned TextContent a second time can
// turn the server's backslash into a TAB while rendering. These fields let the
// next report distinguish the two cases:
//
//	command_has_tab / command_tab_count: the server received a real TAB.
//	command_has_backslash / command_backslash_count: the server received `\`.
//	command_escaped: Go-quoted form; a literal backslash is `\\`, a TAB is `\t`.
//
// Metadata only: the body and success/error contract are untouched.
func addCommandDiagnostics(metadata map[string]interface{}, command string) {
	if metadata == nil {
		return
	}
	metadata["command_escaped"] = fmt.Sprintf("%q", command)
	metadata["command_has_tab"] = strings.ContainsRune(command, '\t')
	metadata["command_tab_count"] = strings.Count(command, "\t")
	metadata["command_has_newline"] = strings.ContainsRune(command, '\n')
	metadata["command_newline_count"] = strings.Count(command, "\n")
	metadata["command_has_backslash"] = strings.Contains(command, `\`)
	metadata["command_backslash_count"] = strings.Count(command, `\`)
}
