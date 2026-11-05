// Package shelljob contains the shared shell -> background-job orchestration.
//
// It deliberately contains no store, transport or tool implementation: the only
// production runtime is the adapter over internal/background.Manager. Shell
// surfaces (local BashTool and remote-tools shell) use this state machine so a
// timeout never duplicates a command and never falls back to a second launcher.
package shelljob

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
)

// Runtime is the thin lifecycle seam used by shell surfaces. It is not a job
// backend: NewManagerRuntime is the only production implementation.
type Runtime interface {
	SubmitShell(ctx context.Context, sessionID, command, workdir string) (jobID, status string, err error)
	WaitShell(ctx context.Context, jobID string, timeout time.Duration) (Snapshot, error)
	ReadShell(ctx context.Context, jobID string, offset int64, limit int) (Output, error)
	CancelShell(ctx context.Context, jobID string) error
	ForgetShell(ctx context.Context, jobID, sessionID string) error
}

// Snapshot is the terminal or timed-out view of one shell job.
type Snapshot struct {
	ID       string
	Status   string
	ExitCode *int
	TimedOut bool
}

// Output is one cursor read from a shell job.
type Output struct {
	Output     string
	NextOffset int64
	Status     string
	ExitCode   *int
	LogPath    string
	ErrorCode  string
	// Message carries the manager's terminal or pending explanation (queue
	// saturation, wait-delay settlement, watchdog diagnosis). Dropping it here
	// is what left remote callers with "still running" and no next step.
	Message string
	// WaitDelayUsed reports that the command process exited while descendants
	// kept the output pipe open; the captured tail may be incomplete.
	WaitDelayUsed bool
	WaitDelayMs   int64
	// Queue diagnostics for pending jobs. The core manager already computes
	// these; shells and job_output must surface them instead of guessing.
	QueuePosition  int
	ActiveJobs     int
	MaxConcurrent  int
	SchedulerState string
	NextAction     string
}

// Request describes one foreground shell invocation. WaitTimeout only bounds
// this wait; the job itself has no process deadline unless the manager is
// configured with one independently.
type Request struct {
	SessionID   string
	Command     string
	Workdir     string
	WaitTimeout time.Duration
	OutputLimit int
	// KeepJob leaves a settled job visible to job_list. The caller must then
	// call ForgetShell after it has finished archiving/formatting the output.
	// The default (false) hides a quick foreground job immediately.
	KeepJob bool
}

// Execution is the surface-neutral result of a shell call.
type Execution struct {
	JobID          string
	Status         string
	ExitCode       *int
	Output         string
	NextOffset     int64
	ErrorCode      string
	TimedOut       bool
	Promoted       bool
	LogPath        string
	Message        string
	WaitDelayUsed  bool
	WaitDelayMs    int64
	QueuePosition  int
	ActiveJobs     int
	MaxConcurrent  int
	SchedulerState string
	NextAction     string
}

// StageError identifies which part of Submit -> Wait -> Read failed. Callers
// use the stage to keep their existing failure classes and recovery hints.
type StageError struct {
	Stage  string
	JobID  string
	Status string
	Err    error
}

func (e *StageError) Error() string {
	if e == nil {
		return ""
	}
	if e.JobID != "" {
		return fmt.Sprintf("shell job %s (stage %s): %v", e.JobID, e.Stage, e.Err)
	}
	return fmt.Sprintf("shell job stage %s: %v", e.Stage, e.Err)
}

func (e *StageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// StageOf reports the stage carried by err, or empty when it is not a
// StageError.
func StageOf(err error) string {
	var stageErr *StageError
	if errors.As(err, &stageErr) {
		return stageErr.Stage
	}
	return ""
}

// JobIDOf reports the job id carried by err, if any.
func JobIDOf(err error) string {
	var stageErr *StageError
	if errors.As(err, &stageErr) {
		return stageErr.JobID
	}
	return ""
}

// Execute runs the single shell-job state machine:
//
//	Submit -> Wait(wait timeout) -> Read -> Forget (settled only)
//
// A wait timeout returns Promoted=true and leaves the job running. A caller
// cancellation cancels the job before returning the context error.
func Execute(ctx context.Context, runtime Runtime, req Request) (Execution, error) {
	if runtime == nil {
		return Execution{}, &StageError{Stage: "runtime", Err: fmt.Errorf("shell job runtime is not configured")}
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return Execution{}, &StageError{Stage: "owner", Err: fmt.Errorf("shell job requires a stable session owner")}
	}
	if strings.TrimSpace(req.Command) == "" {
		return Execution{}, &StageError{Stage: "submit", Err: fmt.Errorf("command is required")}
	}

	jobID, status, err := runtime.SubmitShell(ctx, sessionID, req.Command, req.Workdir)
	if err != nil {
		return Execution{}, &StageError{Stage: "submit", Err: err}
	}
	if strings.TrimSpace(jobID) == "" {
		return Execution{}, &StageError{Stage: "submit", Err: fmt.Errorf("shell job start returned no job id")}
	}

	snapshot, err := runtime.WaitShell(ctx, jobID, req.WaitTimeout)
	if err != nil {
		// A caller cancellation is a control-plane failure. The job must not be
		// left running after the caller that owns it has gone away.
		_ = runtime.CancelShell(context.Background(), jobID)
		return Execution{}, &StageError{Stage: "wait", JobID: jobID, Status: status, Err: err}
	}
	if strings.TrimSpace(snapshot.Status) == "" {
		snapshot.Status = status
	}

	output, readErr := runtime.ReadShell(ctx, jobID, 0, req.OutputLimit)
	if readErr != nil {
		return Execution{
			JobID:    jobID,
			Status:   snapshot.Status,
			ExitCode: snapshot.ExitCode,
			TimedOut: snapshot.TimedOut,
			Promoted: snapshot.TimedOut,
		}, &StageError{Stage: "read", JobID: jobID, Status: snapshot.Status, Err: readErr}
	}
	execution := Execution{
		JobID:          jobID,
		Status:         preferTerminalStatus(output.Status, snapshot.Status),
		ExitCode:       firstExitCode(output.ExitCode, snapshot.ExitCode),
		Output:         output.Output,
		NextOffset:     output.NextOffset,
		ErrorCode:      output.ErrorCode,
		TimedOut:       snapshot.TimedOut,
		Promoted:       snapshot.TimedOut,
		LogPath:        output.LogPath,
		Message:        output.Message,
		WaitDelayUsed:  output.WaitDelayUsed,
		WaitDelayMs:    output.WaitDelayMs,
		QueuePosition:  output.QueuePosition,
		ActiveJobs:     output.ActiveJobs,
		MaxConcurrent:  output.MaxConcurrent,
		SchedulerState: output.SchedulerState,
		NextAction:     output.NextAction,
	}
	if !snapshot.TimedOut && !req.KeepJob {
		// This job was an implementation detail of the foreground call. Hide it
		// from job_list after the caller has read its output.
		_ = runtime.ForgetShell(context.Background(), jobID, sessionID)
	}
	return execution, nil
}

// preferTerminalStatus keeps a terminal status the waiter already observed
// instead of letting a later store read downgrade it.
//
// ReadShell re-reads the persisted record, which may have been rewritten by the
// manager after the wait returned (for example a recovery sweep marking an
// already-finished job as orphaned). Without this guard a command whose real
// outcome was known would be reported as an unknown outcome.
func preferTerminalStatus(values ...string) string {
	for _, value := range values {
		if isTerminalJobStatus(value) {
			return strings.TrimSpace(value)
		}
	}
	return firstNonEmpty(values...)
}

func isTerminalJobStatus(status string) bool {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstExitCode(values ...*int) *int {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// ManagerRuntime adapts background.Manager to Runtime. It owns no state and
// exposes no second store, lifecycle or retention policy.
type ManagerRuntime struct {
	manager *background.Manager
}

// NewManagerRuntime returns the manager-backed runtime. A nil manager produces
// fail-closed errors, never a local direct path.
func NewManagerRuntime(manager *background.Manager) Runtime {
	return &ManagerRuntime{manager: manager}
}

func (r *ManagerRuntime) SubmitShell(ctx context.Context, sessionID, command, workdir string) (string, string, error) {
	if r == nil || r.manager == nil {
		return "", "", fmt.Errorf("background manager is not configured")
	}
	job, err := r.manager.SubmitShell(ctx, sessionID, background.BackgroundTaskArgs{
		Command: command,
		Cwd:     workdir,
	})
	if err != nil {
		return "", "", err
	}
	return job.ID, string(job.Status), nil
}

func (r *ManagerRuntime) WaitShell(ctx context.Context, jobID string, timeout time.Duration) (Snapshot, error) {
	if r == nil || r.manager == nil {
		return Snapshot{}, fmt.Errorf("background manager is not configured")
	}
	job, timedOut, err := r.manager.WaitJob(ctx, jobID, timeout)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		ID:       job.ID,
		Status:   string(job.Status),
		ExitCode: job.ExitCode,
		TimedOut: timedOut,
	}, nil
}

func (r *ManagerRuntime) ReadShell(ctx context.Context, jobID string, offset int64, limit int) (Output, error) {
	if r == nil || r.manager == nil {
		return Output{}, fmt.Errorf("background manager is not configured")
	}
	result, err := r.manager.ReadOutput(ctx, background.TaskOutputArgs{
		JobID:  jobID,
		Offset: offset,
		Limit:  limit,
	})
	if err != nil {
		return Output{}, err
	}
	logPath := ""
	waitDelayUsed := false
	waitDelayMs := int64(0)
	if job, jobErr := r.manager.GetJob(ctx, jobID); jobErr == nil && job != nil {
		logPath = job.LogPath
		if value, ok := job.Metadata["wait_delay_used"].(bool); ok {
			waitDelayUsed = value
		}
		switch value := job.Metadata["wait_delay_ms"].(type) {
		case int:
			waitDelayMs = int64(value)
		case int64:
			waitDelayMs = value
		case float64:
			waitDelayMs = int64(value)
		}
	}
	return Output{
		Output:         result.Output,
		NextOffset:     result.NextOffset,
		Status:         result.Status,
		ExitCode:       result.ExitCode,
		LogPath:        logPath,
		ErrorCode:      result.ErrorCode,
		Message:        result.Message,
		WaitDelayUsed:  waitDelayUsed,
		WaitDelayMs:    waitDelayMs,
		QueuePosition:  result.QueuePosition,
		ActiveJobs:     result.ActiveJobs,
		MaxConcurrent:  result.MaxConcurrent,
		SchedulerState: result.SchedulerState,
		NextAction:     result.NextAction,
	}, nil
}

func (r *ManagerRuntime) CancelShell(ctx context.Context, jobID string) error {
	if r == nil || r.manager == nil {
		return fmt.Errorf("background manager is not configured")
	}
	_, err := r.manager.CancelJob(ctx, jobID)
	return err
}

func (r *ManagerRuntime) ForgetShell(ctx context.Context, jobID, sessionID string) error {
	if r == nil || r.manager == nil {
		return fmt.Errorf("background manager is not configured")
	}
	return r.manager.ForgetJob(ctx, jobID, sessionID)
}
