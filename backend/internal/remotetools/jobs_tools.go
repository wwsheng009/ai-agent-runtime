package remotetools

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
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

// jobTools is the P3 background surface, enabled by profile ops.
var jobTools = []string{ToolJobStart, ToolJobOutput, ToolJobCancel, ToolJobList}

// artifactTools is enabled whenever the artifact store is configured.
var artifactTools = []string{ToolArtifactRead}

// remoteJobStartTool starts a detached command.
type remoteJobStartTool struct {
	*toolkit.BaseTool
	remoteToolBase
	sandbox *Sandbox
	jobs    *JobManager
}

func newRemoteJobStartTool(cfg *Config, workspace *Workspace, sandbox *Sandbox, jobs *JobManager) *remoteJobStartTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Command to run in the background. Subject to the same command policy as shell.",
			},
			"workdir": map[string]interface{}{
				"type":        "string",
				"description": "Working directory, relative to the remote workspace.",
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
		tool.describe("Start a long-running command on the remote host. Returns a job_id; poll with job_output."),
		"1.0.0",
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
		return remoteErrorResult(newToolError(CodeUnsupported, "enable backgroundJobs in the target config", "background jobs are not enabled")), nil
	}
	if t.sandbox == nil {
		return remoteErrorResult(newToolError(CodeCommandDenied, "use profile ops", "shell policy is not configured")), nil
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

	// Same rule as shell: a roots-narrowed session must not start the job in the
	// server's base workspace, which lies outside the client's declaration.
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

	job, err := t.jobs.Start(ctx, command, workdir, t.sandbox)
	if err != nil {
		return remoteErrorResult(err), nil
	}
	metadata := map[string]interface{}{
		"job_id":      job.ID,
		"pid":         job.PID,
		"state":       job.State,
		"command":     command,
		"workdir":     t.cleanDisplayPath(workdir),
		"started_at":  job.StartedAt.Format("2006-01-02T15:04:05Z07:00"),
		"remote":      true,
		"workspace":   effective.Root(),
		"policy_rule": rule,
	}
	body := fmt.Sprintf("Started job %s (pid %d).\nPoll with job_output job_id=%s.\n", job.ID, job.PID, job.ID)
	return remoteTextResult(body, metadata), nil
}

// remoteJobOutputTool reads a job's output window and state.
type remoteJobOutputTool struct {
	*toolkit.BaseTool
	remoteToolBase
	jobs *JobManager
}

func newRemoteJobOutputTool(cfg *Config, workspace *Workspace, jobs *JobManager) *remoteJobOutputTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"job_id": map[string]interface{}{
				"type":        "string",
				"description": "Job id returned by job_start.",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "Byte offset to read from. Omit for the tail.",
			},
			"tail_bytes": map[string]interface{}{
				"type":        "integer",
				"description": "How many bytes to return. Default 8192, capped at 65536.",
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
		tool.describe("Read the state and recent output of a remote background job."),
		"1.0.0",
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
	offset := int64(intParam(params, "offset", 0))
	tail := intParam(params, "tail_bytes", 8192)
	if tail > artifactReadMaxBytes {
		tail = artifactReadMaxBytes
	}

	output, job, size, err := t.jobs.ReadOutput(jobID, offset, tail)
	if err != nil {
		return remoteErrorResult(err), nil
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "=== job %s: %s", job.ID, job.State)
	if job.State == JobExited {
		fmt.Fprintf(&builder, " (exit %d)", job.ExitCode)
	}
	fmt.Fprintf(&builder, " ===\n")
	if output == "" {
		builder.WriteString("(no output yet)\n")
	} else {
		builder.WriteString(output)
		if !strings.HasSuffix(output, "\n") {
			builder.WriteByte('\n')
		}
	}
	metadata := map[string]interface{}{
		"job_id":       job.ID,
		"state":        job.State,
		"exit_code":    job.ExitCode,
		"output_bytes": size,
		"remote":       true,
	}
	if job.State == JobRunning {
		metadata["next_action"] = "poll again with job_output; the job is still running"
	} else {
		metadata["finished"] = true
	}
	return remoteTextResult(builder.String(), metadata), nil
}

// remoteJobCancelTool stops a running job.
type remoteJobCancelTool struct {
	*toolkit.BaseTool
	remoteToolBase
	jobs *JobManager
}

func newRemoteJobCancelTool(cfg *Config, workspace *Workspace, jobs *JobManager) *remoteJobCancelTool {
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
		tool.describe("Stop a running remote background job (SIGTERM, then a tree kill)."),
		"1.0.0",
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
	job, err := t.jobs.Cancel(jobID)
	if err != nil {
		return remoteErrorResult(err), nil
	}
	metadata := map[string]interface{}{
		"job_id":    job.ID,
		"state":     job.State,
		"exit_code": job.ExitCode,
		"remote":    true,
	}
	return remoteTextResult(fmt.Sprintf("Job %s is now %s.\n", job.ID, job.State), metadata), nil
}

// remoteJobListTool lists recent jobs.
type remoteJobListTool struct {
	*toolkit.BaseTool
	remoteToolBase
	jobs *JobManager
}

func newRemoteJobListTool(cfg *Config, workspace *Workspace, jobs *JobManager) *remoteJobListTool {
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
		tool.describe("List recent remote background jobs, newest first."),
		"1.0.0",
		parameters,
		true,
	)
	return tool
}

func (t *remoteJobListTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if err := contextErr(ctx); err != nil {
		return remoteErrorResult(err), nil
	}
	limit := intParam(params, "limit", 20)
	jobs, err := t.jobs.List(limit)
	if err != nil {
		return remoteErrorResult(err), nil
	}
	var builder strings.Builder
	builder.WriteString("=== remote jobs ===\n")
	for _, job := range jobs {
		fmt.Fprintf(&builder, "%s  %-8s pid=%-7d %s\n", job.ID, job.State, job.PID, truncateForDisplay(job.Command, 80))
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

// remoteArtifactReadTool pages a stored artifact.
type remoteArtifactReadTool struct {
	*toolkit.BaseTool
	remoteToolBase
	artifacts *ArtifactStore
}

func newRemoteArtifactReadTool(cfg *Config, workspace *Workspace, artifacts *ArtifactStore) *remoteArtifactReadTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"artifact_ref": map[string]interface{}{
				"type":        "string",
				"description": "Namespaced artifact ref (remote:<target>:art_…) or bare art_… id.",
			},
			"artifact_id": map[string]interface{}{
				"type":        "string",
				"description": "Bare artifact id (art_…) from a truncated tool result.",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"minimum":     0,
				"description": "0-based byte offset. Default 0.",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "Maximum bytes to return. Default 65536.",
			},
		},
		"additionalProperties": false,
	}
	tool := &remoteArtifactReadTool{
		remoteToolBase: remoteToolBase{cfg: cfg, workspace: workspace},
		artifacts:      artifacts,
	}
	tool.BaseTool = toolkit.NewBaseTool(
		ToolArtifactRead,
		tool.describe("Read a byte window of a full remote tool capture that was folded before reaching the model."),
		"1.0.0",
		parameters,
		true,
	)
	return tool
}

func (t *remoteArtifactReadTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if err := contextErr(ctx); err != nil {
		return remoteErrorResult(err), nil
	}
	artifactRef, _ := params["artifact_ref"].(string)
	artifactID, _ := params["artifact_id"].(string)
	if strings.TrimSpace(artifactRef) != "" {
		artifactID = strings.TrimSpace(artifactRef)
	}
	if strings.TrimSpace(artifactID) == "" {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "pass artifact_ref or artifact_id", "artifact_ref/artifact_id is required")), nil
	}
	// A target-internal read never accepts a namespace: the caller already
	// selected this target, and accepting remote:<other>: here would turn the
	// tool into a cross-scope router.
	if strings.HasPrefix(strings.TrimSpace(artifactID), "remote:") {
		return remoteErrorResult(newToolError(toolcore.ErrArtifactScopeDenied, "pass the bare art_… id", "remote namespace is not accepted by a target-internal artifact_read")), nil
	}
	if !toolcore.ValidArtifactID(artifactID) {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "pass the art_… id from the tool output", "invalid artifact id: %q", artifactID)), nil
	}
	offset := intParam(params, "offset", 0)
	limit := intParam(params, "limit", artifactReadMaxBytes)
	content, size, eof, err := t.artifacts.Read(artifactID, offset, limit)
	if err != nil {
		return remoteErrorResult(err), nil
	}
	metadata := map[string]interface{}{
		"artifact_id":  artifactID,
		"offset":       offset,
		"bytes":        len(content),
		"total_bytes":  size,
		"eof":          eof,
		"is_truncated": !eof,
		"remote":       true,
	}
	if !eof {
		metadata["suggested_next_offset"] = offset + len(content)
	}
	body, metadata := t.shapeModelOutput(ToolArtifactRead, content, metadata)
	return remoteTextResult(body, metadata), nil
}

// truncateForDisplay shortens one line for a table.
func truncateForDisplay(value string, limit int) string {
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
