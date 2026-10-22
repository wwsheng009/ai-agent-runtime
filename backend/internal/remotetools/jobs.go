package remotetools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Job states reported to the model.
const (
	JobRunning = "running"
	JobExited  = "exited"
	JobKilled  = "killed"
	JobLost    = "lost"
)

// jobRetention is how long a finished job survives on disk. P3 mirrors the
// SSH-MCP precedent: a week is long enough to debug a nightly build, short enough
// that a busy target does not accumulate directories forever.
const jobRetention = 7 * 24 * time.Hour

// jobPruneInterval bounds how often the retention sweep runs.
const jobPruneInterval = time.Hour

// Job describes one background command.
//
// The record is persisted as JSON next to the job's output file so it survives a
// server restart, an SSH disconnect, or a crash. Command text is stored for audit
// but never returned to the model verbatim (it is echoed only from the caller's
// own argument).
type Job struct {
	ID         string    `json:"id"`
	Command    string    `json:"command"`
	Workdir    string    `json:"workdir"`
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	State      string    `json:"state"`
	ExitCode   int       `json:"exit_code"`
	Error      string    `json:"error,omitempty"`
	OutputPath string    `json:"output_path"`
	// BytesWritten mirrors the output file size at the last state update.
	BytesWritten int64 `json:"bytes_written"`
}

// JobManager owns the on-disk job directory.
//
// Layout under <root>/jobs/<id>/:
//
//	job.json     record (state, pid, exit code)
//	output.log   combined stdout+stderr, appended live
//
// The process itself is detached from the MCP request: the manager starts it with
// its own process group, records the PID, then a supervisor goroutine reaps it and
// stamps the final state. A server restart can still report `exited` because the
// record was written before the process was spawned.
type JobManager struct {
	root      string
	mu        sync.Mutex
	active    map[string]*activeJob
	lastPrune time.Time
}

// activeJob is the in-process handle for a running job.
type activeJob struct {
	job  *Job
	cmd  *exec.Cmd
	done chan struct{}
}

// NewJobManager prepares the job root. An empty root disables background jobs.
func NewJobManager(root string) (*JobManager, error) {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return nil, nil
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return nil, fmt.Errorf("resolve job root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("create job root: %w", err)
	}
	return &JobManager{root: abs, active: map[string]*activeJob{}}, nil
}

// jobDir returns the directory for one job.
func (m *JobManager) jobDir(id string) string {
	return filepath.Join(m.root, "jobs", id)
}

// Start launches a detached command and returns its immutable starting record.
func (m *JobManager) Start(ctx context.Context, command, workdir string, sandbox *Sandbox) (*Job, error) {
	if m == nil {
		return nil, newToolError(CodeUnsupported, "enable backgroundJobs on the target", "background jobs are not enabled")
	}
	if err := m.pruneIfDue(); err != nil {
		// Pruning is housekeeping: never fail a start because an old directory
		// could not be removed.
		fmt.Fprintf(os.Stderr, "aicli-remote-tools: job prune failed: %v\n", err)
	}

	id := newJobID()
	dir := m.jobDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, newToolError(CodeInternal, "check jobRoot permissions", "create job dir: %v", err)
	}
	outputPath := filepath.Join(dir, "output.log")
	output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, newToolError(CodeInternal, "check jobRoot permissions", "create job output: %v", err)
	}

	shell, args := remoteShellInvocation(command)
	cmd := exec.Command(shell, args...)
	cmd.Dir = workdir
	cmd.Env = sandbox.FilterEnv(os.Environ())
	cmd.Stdout = output
	cmd.Stderr = output
	configureProcessGroup(cmd)

	if err := cmd.Start(); err != nil {
		_ = output.Close()
		_ = os.RemoveAll(dir)
		return nil, newToolError(CodeCommandDenied, "check that the command exists on the target", "cannot start job: %v", err)
	}

	job := &Job{
		ID:         id,
		Command:    command,
		Workdir:    workdir,
		PID:        cmd.Process.Pid,
		StartedAt:  time.Now(),
		State:      JobRunning,
		OutputPath: outputPath,
	}
	// Persist the running record before returning, so a crash still leaves a
	// discoverable job rather than an unknown PID.
	if err := writeJobRecord(dir, job); err != nil {
		_ = output.Close()
		_ = cmd.Process.Kill()
		return nil, newToolError(CodeInternal, "check jobRoot permissions", "persist job record: %v", err)
	}

	handle := &activeJob{job: job, cmd: cmd, done: make(chan struct{})}
	m.mu.Lock()
	m.active[id] = handle
	m.mu.Unlock()

	go m.supervise(handle, output)
	return job, nil
}

// supervise waits for the process and stamps the terminal state.
func (m *JobManager) supervise(handle *activeJob, output *os.File) {
	defer close(handle.done)
	err := handle.cmd.Wait()
	_ = output.Close()

	handle.job.FinishedAt = time.Now()
	switch {
	case err == nil:
		handle.job.State = JobExited
		handle.job.ExitCode = 0
	default:
		handle.job.State = JobExited
		handle.job.ExitCode = -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			handle.job.ExitCode = exitErr.ExitCode()
		} else {
			handle.job.Error = err.Error()
		}
	}
	if info, statErr := os.Stat(handle.job.OutputPath); statErr == nil {
		handle.job.BytesWritten = info.Size()
	}
	_ = writeJobRecord(m.jobDir(handle.job.ID), handle.job)

	m.mu.Lock()
	delete(m.active, handle.job.ID)
	m.mu.Unlock()
}

// Status returns the current record for one job.
func (m *JobManager) Status(id string) (*Job, error) {
	if m == nil {
		return nil, newToolError(CodeUnsupported, "enable backgroundJobs on the target", "background jobs are not enabled")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, newToolError(CodeInvalidArgument, "pass job_id", "job_id is required")
	}
	dir := m.jobDir(id)
	job, err := readJobRecord(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, newToolError(CodeNotFound, "use job_list to discover ids", "job not found: %s", id)
		}
		return nil, newToolError(CodeInternal, "retry the call", "read job record: %v", err)
	}
	if info, statErr := os.Stat(job.OutputPath); statErr == nil {
		job.BytesWritten = info.Size()
	}
	return job, nil
}

// ReadOutput returns a byte window of the job's output.
//
// offset=0 with no explicit offset means "tail": the model usually wants the end
// of a failing build, not the first 8 KiB of a compiler banner.
func (m *JobManager) ReadOutput(id string, offset int64, tailBytes int) (string, *Job, int64, error) {
	job, err := m.Status(id)
	if err != nil {
		return "", nil, 0, err
	}
	file, openErr := os.Open(job.OutputPath)
	if openErr != nil {
		if os.IsNotExist(openErr) {
			return "", job, 0, nil
		}
		return "", job, 0, newToolError(CodeInternal, "retry the call", "open job output: %v", openErr)
	}
	defer file.Close()

	info, statErr := file.Stat()
	if statErr != nil {
		return "", job, 0, newToolError(CodeInternal, "retry the call", "stat job output: %v", statErr)
	}
	size := info.Size()
	if tailBytes <= 0 {
		tailBytes = 8192
	}
	start := offset
	if start <= 0 {
		start = size - int64(tailBytes)
		if start < 0 {
			start = 0
		}
	}
	if start > size {
		start = size
	}
	length := int64(tailBytes)
	if start+length > size {
		length = size - start
	}
	if length <= 0 {
		return "", job, size, nil
	}
	buffer := make([]byte, length)
	n, readErr := file.ReadAt(buffer, start)
	if readErr != nil && n == 0 {
		return "", job, size, newToolError(CodeInternal, "retry the call", "read job output: %v", readErr)
	}
	// Starting mid-rune would produce replacement characters; nudge forward.
	for len(buffer) > 0 && !isUTF8Start(buffer[0]) {
		buffer = buffer[1:]
	}
	return string(buffer[:n]), job, size, nil
}

// Cancel stops a running job.
//
// A job that already finished is reported as such rather than failing: the caller
// asked to make sure it is not running, and it is not.
func (m *JobManager) Cancel(id string) (*Job, error) {
	job, err := m.Status(id)
	if err != nil {
		return nil, err
	}
	if job.State != JobRunning {
		return job, nil
	}

	m.mu.Lock()
	handle, ok := m.active[job.ID]
	m.mu.Unlock()

	if ok && handle != nil {
		// Graceful first: SIGTERM lets a build flush its own state.
		terminateProcessGroup(handle.cmd)
		select {
		case <-handle.done:
		case <-time.After(5 * time.Second):
			killProcessGroup(handle.cmd)
			select {
			case <-handle.done:
			case <-time.After(5 * time.Second):
			}
		}
	}

	// The supervisor may not have run yet (or this is a restart where the PID was
	// reused by an unrelated process). Mark the record killed only when the state
	// is still running.
	updated, statusErr := m.Status(id)
	if statusErr != nil {
		return nil, statusErr
	}
	if updated.State == JobRunning {
		updated.State = JobKilled
		updated.FinishedAt = time.Now()
		updated.ExitCode = -1
		_ = writeJobRecord(m.jobDir(id), updated)
	}
	return updated, nil
}

// List returns recent jobs, newest first.
func (m *JobManager) List(limit int) ([]*Job, error) {
	if m == nil {
		return nil, nil
	}
	dir := filepath.Join(m.root, "jobs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, newToolError(CodeInternal, "retry the call", "read job root: %v", err)
	}
	jobs := make([]*Job, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		job, readErr := readJobRecord(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			continue
		}
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].StartedAt.After(jobs[j].StartedAt) })
	if limit > 0 && len(jobs) > limit {
		jobs = jobs[:limit]
	}
	return jobs, nil
}

// pruneIfDue removes job directories older than the retention window.
func (m *JobManager) pruneIfDue() error {
	m.mu.Lock()
	if time.Since(m.lastPrune) < jobPruneInterval {
		m.mu.Unlock()
		return nil
	}
	m.lastPrune = time.Now()
	m.mu.Unlock()

	jobs, err := m.List(0)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-jobRetention)
	for _, job := range jobs {
		if job.State == JobRunning {
			continue
		}
		finished := job.FinishedAt
		if finished.IsZero() {
			finished = job.StartedAt
		}
		if finished.After(cutoff) {
			continue
		}
		_ = os.RemoveAll(m.jobDir(job.ID))
	}
	return nil
}

// Close terminates every running job. It is called on server shutdown so a
// process does not outlive the daemon that owns it.
func (m *JobManager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	handles := make([]*activeJob, 0, len(m.active))
	for _, handle := range m.active {
		handles = append(handles, handle)
	}
	m.mu.Unlock()
	for _, handle := range handles {
		killProcessGroup(handle.cmd)
	}
}

// writeJobRecord persists the record atomically.
//
// Rename is retried briefly: on Windows a rename over a file that another
// goroutine is concurrently reading fails with a sharing violation. The window is
// tiny (Status does one ReadFile) but dropping the terminal state would leave a
// finished job reported as running forever, so the retry is not optional.
func writeJobRecord(dir string, job *Job) error {
	encoded, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "job.json")
	temp := path + ".tmp"
	if err := os.WriteFile(temp, encoded, 0o600); err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		if err := os.Rename(temp, path); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(time.Duration(5+attempt*2) * time.Millisecond)
	}
	_ = os.Remove(temp)
	return lastErr
}

// readJobRecord loads one record.
func readJobRecord(dir string) (*Job, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "job.json"))
	if err != nil {
		// A rename in flight may briefly make the file unreadable on Windows.
		// Retry once before reporting NOT_FOUND, otherwise a poll that races the
		// supervisor reports a finished job as missing.
		time.Sleep(10 * time.Millisecond)
		raw, err = os.ReadFile(filepath.Join(dir, "job.json"))
	}
	if err != nil {
		return nil, err
	}
	var job Job
	if err := json.Unmarshal(raw, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

// newJobID generates a sortable, collision-resistant id.
func newJobID() string {
	return "job_" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// isUTF8Start reports whether b is a UTF-8 leading byte.
func isUTF8Start(b byte) bool {
	return b&0xC0 != 0x80
}
