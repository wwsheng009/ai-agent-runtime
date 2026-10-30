package remotetools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// Background job and artifact tool names.
const (
	ToolJobStart     = "job_start"
	ToolJobOutput    = "job_output"
	ToolJobCancel    = "job_cancel"
	ToolJobList      = "job_list"
	ToolArtifactRead = "artifact_read"
)

// jobTools is the command-control surface backed by the core job manager.
var jobTools = []string{ToolJobStart, ToolJobOutput, ToolJobCancel, ToolJobList}

// artifactTools is enabled whenever the artifact store is configured.
var artifactTools = []string{ToolArtifactRead}

// remoteJobOwner returns the stable logical owner for a remote job. It is the
// MCP session id when the transport supplies one; direct tool calls may bind the
// same fact through toolctx. An empty owner is fail-closed.
func remoteJobOwner(ctx context.Context, cfg *Config) string {
	if info := RequestInfoFromContext(ctx); strings.TrimSpace(info.SessionID) != "" {
		return strings.TrimSpace(info.SessionID)
	}
	if owner := strings.TrimSpace(toolctx.SessionID(ctx)); owner != "" {
		return owner
	}
	// stdio is one process per client and has no durable MCP session id. The
	// process principal is the stable owner there. HTTP stateless requests must
	// not fall back to a shared token: they have no stable owner and fail closed.
	if cfg == nil || strings.EqualFold(strings.TrimSpace(cfg.Transport), "http") {
		return ""
	}
	return strings.TrimSpace(RequestInfoFromContext(ctx).Principal)
}

func requireRemoteJobOwner(ctx context.Context, cfg *Config) (string, error) {
	owner := remoteJobOwner(ctx, cfg)
	if owner == "" {
		return "", newToolError(
			CodeUnsupported,
			"call the tool from a session-bound MCP client; a stateless request cannot own a job",
			"remote job requires a stable session owner",
		)
	}
	return owner, nil
}

func remoteJobManagerError(err error) error {
	if err == nil {
		return nil
	}
	if runtimeerrors.Is(err, runtimeerrors.ErrJobNotFound) {
		// Jobs live in the manager of the process that started them. A restarted
		// server therefore has no record of an older job, and saying only "not
		// found" leaves the caller no way to tell that apart from a typo. Naming
		// the lifecycle keeps the message diagnosable without revealing whether
		// some other session owns the id.
		return newToolError(CodeNotFound,
			"use job_list to discover jobs owned by this session; background jobs live only in the server process that started them, so they do not survive a server restart",
			"%v", err)
	}
	return newToolError(CodeInternal, "retry the call or inspect jobs.root", "%v", err)
}

func requireOwnedJob(ctx context.Context, manager *background.Manager, jobID, owner string) (*background.Job, error) {
	job, err := manager.GetJob(ctx, jobID)
	if err != nil {
		return nil, remoteJobManagerError(err)
	}
	if strings.TrimSpace(job.SessionID) != strings.TrimSpace(owner) {
		// Do not reveal the existence or status of another session's job.
		return nil, newToolError(CodeNotFound, "use job_list to discover jobs owned by this session", "job not found: %s", jobID)
	}
	return job, nil
}

// remoteJobStartTool starts a command without waiting for it.
type remoteJobStartTool struct {
	*toolkit.BaseTool
	remoteToolBase
	sandbox *Sandbox
	jobs    *background.Manager
}

func newRemoteJobStartTool(cfg *Config, workspace *Workspace, sandbox *Sandbox, jobs *background.Manager) *remoteJobStartTool {
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
				"description": "Starting directory for the background job. An absolute path is used as-is; a relative path is resolved against the session workspace root (NOT against a previous shell workdir). Omit to use the session workspace root. The directory must already exist.",
			},
		},
		"required":             []string{"command"},
		"additionalProperties": false,
	}
	tool := &remoteJobStartTool{
		remoteToolBase: remoteToolBase{cfg: cfg, workspace: workspace},
		sandbox:        sandbox,
		jobs:           jobs,
	}
	tool.BaseTool = toolkit.NewBaseTool(
		ToolJobStart,
		tool.describe(shellToolDescription(
			"Start a command on the remote host without waiting. Returns a job_id; poll with job_output. This uses the same job runtime as shell.",
			shell)),
		"2.0.0",
		parameters,
		true,
	)
	return tool
}

func (t *remoteJobStartTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if err := contextErr(ctx); err != nil {
		return remoteErrorResult(err), nil
	}
	command, _ := params["command"].(string)
	if strings.TrimSpace(command) == "" {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "pass command", "command is required")), nil
	}
	if t.jobs == nil {
		return remoteErrorResult(newToolError(CodeUnsupported, "configure jobs.root on the target", "background jobs are not enabled")), nil
	}
	if t.sandbox == nil {
		return remoteErrorResult(newToolError(CodeCommandDenied, "use profile all/ops", "shell policy is not configured")), nil
	}
	owner, err := requireRemoteJobOwner(ctx, t.cfg)
	if err != nil {
		return remoteErrorResult(err), nil
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

	effective := workspaceForCall(ctx, t.workspace)
	workdir := effective.Root()
	if raw, ok := params["workdir"].(string); ok && strings.TrimSpace(raw) != "" {
		resolved, resolveErr := t.resolve(ctx, raw)
		if resolveErr != nil {
			return remoteErrorResult(resolveErr), nil
		}
		isDir, dirErr := isDirectory(resolved)
		if dirErr != nil || !isDir {
			return remoteErrorResult(newToolError(CodeInvalidArgument, "pass a directory", "workdir is not a directory")), nil
		}
		workdir = resolved
	}

	job, err := t.jobs.SubmitShell(ctx, owner, background.BackgroundTaskArgs{
		Command: command,
		Cwd:     workdir,
	})
	if err != nil {
		return remoteErrorResult(remoteJobManagerError(err)), nil
	}
	shell := resolvedShellForConfig(t.cfg)
	metadata := map[string]interface{}{
		"job_id":      job.ID,
		"state":       string(job.Status),
		"command":     command,
		"workdir":     t.cleanDisplayPath(workdir),
		"started_at":  job.CreatedAt.Format(time.RFC3339),
		"remote":      true,
		"workspace":   effective.Root(),
		"policy_rule": rule,
		"shell":       shellLabel(shell),
		"shell_type":  string(shell.Type),
		"owner":       owner,
	}
	body := fmt.Sprintf("Started job %s\nPoll with job_output job_id=%s offset=0.\n", job.ID, job.ID)
	return remoteTextResult(body, metadata), nil
}

// remoteJobOutputTool reads a job's output window and state.
type remoteJobOutputTool struct {
	*toolkit.BaseTool
	remoteToolBase
	jobs *background.Manager
}

func newRemoteJobOutputTool(cfg *Config, workspace *Workspace, jobs *background.Manager) *remoteJobOutputTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"job_id": map[string]interface{}{
				"type":        "string",
				"description": "Job id returned by job_start or a promoted shell result.",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "Byte offset to read from. Default 0. Use the returned next_offset to continue.",
			},
			"tail_bytes": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum bytes to return. Default 8192, capped at 65536.",
			},
		},
		"required":             []string{"job_id"},
		"additionalProperties": false,
	}
	tool := &remoteJobOutputTool{
		remoteToolBase: remoteToolBase{cfg: cfg, workspace: workspace},
		jobs:           jobs,
	}
	tool.BaseTool = toolkit.NewBaseTool(
		ToolJobOutput,
		tool.describe("Read the state and an output window of a remote background job, using the same offset cursor as the core job runtime."),
		"2.0.0",
		parameters,
		true,
	)
	return tool
}

func (t *remoteJobOutputTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if err := contextErr(ctx); err != nil {
		return remoteErrorResult(err), nil
	}
	jobID, _ := params["job_id"].(string)
	if strings.TrimSpace(jobID) == "" {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "pass job_id", "job_id is required")), nil
	}
	if t.jobs == nil {
		return remoteErrorResult(newToolError(CodeUnsupported, "configure jobs.root on the target", "background jobs are not enabled")), nil
	}
	owner, err := requireRemoteJobOwner(ctx, t.cfg)
	if err != nil {
		return remoteErrorResult(err), nil
	}
	offset := int64(intParam(params, "offset", 0))
	limit := intParam(params, "tail_bytes", 8192)
	if limit > artifactReadMaxBytes {
		limit = artifactReadMaxBytes
	}
	job, err := requireOwnedJob(ctx, t.jobs, jobID, owner)
	if err != nil {
		return remoteErrorResult(err), nil
	}
	result, err := t.jobs.ReadOutput(ctx, background.TaskOutputArgs{
		JobID:  job.ID,
		Offset: offset,
		Limit:  limit,
	})
	if err != nil {
		return remoteErrorResult(remoteJobManagerError(err)), nil
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "=== job %s: %s", result.JobID, result.Status)
	if result.ExitCode != nil {
		fmt.Fprintf(&builder, " (exit %d)", *result.ExitCode)
	}
	fmt.Fprintf(&builder, " ===\n")
	if result.Output == "" {
		switch background.JobStatus(result.Status) {
		case background.StatusCompleted:
			builder.WriteString("(no output; job completed)\n")
		case background.StatusFailed:
			builder.WriteString("(no output; job failed)\n")
		case background.StatusCancelled:
			builder.WriteString("(no output; job was cancelled)\n")
		case background.StatusPending:
			builder.WriteString("(no output yet; job is queued)\n")
		default:
			builder.WriteString("(no output yet)\n")
		}
	} else {
		builder.WriteString(result.Output)
		if !strings.HasSuffix(result.Output, "\n") {
			builder.WriteByte('\n')
		}
	}
	metadata := map[string]interface{}{
		"job_id":       result.JobID,
		"state":        result.Status,
		"exit_code":    result.ExitCode,
		"offset":       offset,
		"next_offset":  result.NextOffset,
		"output_bytes": len(result.Output),
		"remote":       true,
	}
	if background.IsTerminalStatus(background.JobStatus(result.Status)) {
		metadata["finished"] = true
	} else {
		metadata["next_action"] = fmt.Sprintf("job_output job_id=%s offset=%d", result.JobID, result.NextOffset)
	}
	return remoteTextResult(builder.String(), metadata), nil
}

// remoteJobCancelTool stops a running job.
type remoteJobCancelTool struct {
	*toolkit.BaseTool
	remoteToolBase
	jobs *background.Manager
}

func newRemoteJobCancelTool(cfg *Config, workspace *Workspace, jobs *background.Manager) *remoteJobCancelTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"job_id": map[string]interface{}{
				"type":        "string",
				"description": "Job id to stop.",
			},
		},
		"required":             []string{"job_id"},
		"additionalProperties": false,
	}
	tool := &remoteJobCancelTool{
		remoteToolBase: remoteToolBase{cfg: cfg, workspace: workspace},
		jobs:           jobs,
	}
	tool.BaseTool = toolkit.NewBaseTool(
		ToolJobCancel,
		tool.describe("Stop a remote background job through the core manager's cancellation path."),
		"2.0.0",
		parameters,
		true,
	)
	return tool
}

func (t *remoteJobCancelTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if err := contextErr(ctx); err != nil {
		return remoteErrorResult(err), nil
	}
	jobID, _ := params["job_id"].(string)
	if strings.TrimSpace(jobID) == "" {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "pass job_id", "job_id is required")), nil
	}
	if t.jobs == nil {
		return remoteErrorResult(newToolError(CodeUnsupported, "configure jobs.root on the target", "background jobs are not enabled")), nil
	}
	owner, err := requireRemoteJobOwner(ctx, t.cfg)
	if err != nil {
		return remoteErrorResult(err), nil
	}
	if _, err := requireOwnedJob(ctx, t.jobs, jobID, owner); err != nil {
		return remoteErrorResult(err), nil
	}
	job, err := t.jobs.CancelJob(ctx, jobID)
	if err != nil {
		return remoteErrorResult(remoteJobManagerError(err)), nil
	}
	metadata := map[string]interface{}{
		"job_id":    job.ID,
		"state":     string(job.Status),
		"exit_code": job.ExitCode,
		"remote":    true,
	}
	return remoteTextResult(fmt.Sprintf("Job %s is now %s.\n", job.ID, job.Status), metadata), nil
}

// remoteJobListTool lists recent jobs owned by the calling session.
type remoteJobListTool struct {
	*toolkit.BaseTool
	remoteToolBase
	jobs *background.Manager
}

func newRemoteJobListTool(cfg *Config, workspace *Workspace, jobs *background.Manager) *remoteJobListTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"limit": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "Maximum jobs to list. Default 20.",
			},
		},
		"additionalProperties": false,
	}
	tool := &remoteJobListTool{
		remoteToolBase: remoteToolBase{cfg: cfg, workspace: workspace},
		jobs:           jobs,
	}
	tool.BaseTool = toolkit.NewBaseTool(
		ToolJobList,
		tool.describe("List recent remote background jobs owned by this session, newest first."),
		"2.0.0",
		parameters,
		true,
	)
	return tool
}

func (t *remoteJobListTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if err := contextErr(ctx); err != nil {
		return remoteErrorResult(err), nil
	}
	if t.jobs == nil {
		return remoteErrorResult(newToolError(CodeUnsupported, "configure jobs.root on the target", "background jobs are not enabled")), nil
	}
	owner, err := requireRemoteJobOwner(ctx, t.cfg)
	if err != nil {
		return remoteErrorResult(err), nil
	}
	limit := intParam(params, "limit", 20)
	if limit <= 0 {
		limit = 20
	}
	jobs, err := t.jobs.ListJobs(ctx, background.JobFilter{SessionID: owner, Limit: limit})
	if err != nil {
		return remoteErrorResult(remoteJobManagerError(err)), nil
	}
	sort.SliceStable(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})
	var builder strings.Builder
	builder.WriteString("=== remote jobs ===\n")
	for _, job := range jobs {
		fmt.Fprintf(&builder, "%s  %-10s %s\n", job.ID, job.Status, truncateForDisplay(job.Command, 80))
	}
	if len(jobs) == 0 {
		builder.WriteString("(no jobs)\n")
	}
	metadata := map[string]interface{}{
		"jobs":   len(jobs),
		"remote": true,
	}
	return remoteTextResult(builder.String(), metadata), nil
}

// truncateForDisplay shortens one line for a table.
func truncateForDisplay(value string, limit int) string {
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
