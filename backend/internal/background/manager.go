package background

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Config controls background execution defaults.
type Config struct {
	MaxOutputBytes    int
	DefaultTimeout    time.Duration
	StorePath         string
	StoreDSN          string
	LogDir            string
	MaxConcurrentJobs int
	EventHandler      func(JobEvent)
}

// DefaultConfig returns a conservative default config.
func DefaultConfig() Config {
	return Config{
		MaxOutputBytes:    1 * 1024 * 1024, // 1MB
		DefaultTimeout:    0,
		StorePath:         "",
		StoreDSN:          "",
		LogDir:            "",
		MaxConcurrentJobs: 2,
		EventHandler:      nil,
	}
}

// Manager executes background tasks and retains output.
type Manager struct {
	mu                sync.RWMutex
	config            Config
	jobs              map[string]*managedJob
	store             Store
	logDir            string
	dispatchCh        chan struct{}
	maxConcurrentJobs int
	eventHandler      func(JobEvent)
	stopCh            chan struct{}
	doneCh            chan struct{}
	closeOnce         sync.Once
	jobWG             sync.WaitGroup
}

type managedJob struct {
	mu           sync.RWMutex
	ctx          context.Context
	info         Job
	request      BackgroundTaskArgs
	output       *outputBuffer
	logPath      string
	outputMu     sync.Mutex
	outputOffset int64
	scheduled    bool
	cancel       context.CancelFunc
}

// NewManager creates a new background manager.
func NewManager(cfg Config) *Manager {
	if cfg.MaxOutputBytes <= 0 {
		cfg.MaxOutputBytes = DefaultConfig().MaxOutputBytes
	}
	if cfg.MaxConcurrentJobs <= 0 {
		cfg.MaxConcurrentJobs = DefaultConfig().MaxConcurrentJobs
	}
	manager := &Manager{
		config:            cfg,
		jobs:              make(map[string]*managedJob),
		dispatchCh:        make(chan struct{}, 1),
		maxConcurrentJobs: cfg.MaxConcurrentJobs,
		stopCh:            make(chan struct{}),
		doneCh:            make(chan struct{}),
	}
	manager.eventHandler = cfg.EventHandler
	if strings.TrimSpace(cfg.StorePath) != "" || strings.TrimSpace(cfg.StoreDSN) != "" {
		if store, err := NewSQLiteStore(&StoreConfig{Path: cfg.StorePath, DSN: cfg.StoreDSN}); err == nil {
			manager.store = store
			if strings.TrimSpace(cfg.LogDir) == "" {
				baseDir := filepath.Dir(strings.TrimSpace(cfg.StorePath))
				if baseDir == "." || baseDir == "" {
					baseDir = "."
				}
				manager.logDir = filepath.Join(baseDir, "background_logs")
			}
		}
	}
	if manager.logDir == "" && strings.TrimSpace(cfg.LogDir) != "" {
		manager.logDir = strings.TrimSpace(cfg.LogDir)
	}
	if manager.logDir != "" {
		_ = os.MkdirAll(manager.logDir, 0o755)
	}
	go manager.dispatchLoop()
	manager.recoverPersistedJobs(context.Background())
	manager.notifyDispatcher()
	return manager
}

// Close stops background scheduling, cancels managed jobs, waits for workers to exit, and closes the store.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	var closeErr error
	m.closeOnce.Do(func() {
		close(m.stopCh)
		cancels := make([]context.CancelFunc, 0)
		m.mu.RLock()
		for _, job := range m.jobs {
			if job == nil {
				continue
			}
			job.mu.RLock()
			cancel := job.cancel
			job.mu.RUnlock()
			if cancel != nil {
				cancels = append(cancels, cancel)
			}
		}
		m.mu.RUnlock()
		for _, cancel := range cancels {
			cancel()
		}
		<-m.doneCh
		m.jobWG.Wait()
		if closer, ok := m.store.(interface{ Close() error }); ok {
			closeErr = closer.Close()
		}
	})
	return closeErr
}

// SubmitShell runs a shell command in the background.
func (m *Manager) SubmitShell(ctx context.Context, sessionID string, req BackgroundTaskArgs) (*Job, error) {
	if m == nil {
		return nil, fmt.Errorf("background manager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := strings.TrimSpace(req.Command)
	if command == "" {
		return nil, fmt.Errorf("command is required")
	}

	jobID := "job_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	now := time.Now().UTC()
	logPath := ""
	if m.logDir != "" {
		logPath = filepath.Join(m.logDir, jobID+".log")
		_ = os.WriteFile(logPath, []byte{}, 0o644)
	}
	req = sanitizeBackgroundTaskArgs(req)
	jobCtx, cancel := context.WithCancel(context.Background())
	managed := &managedJob{
		ctx: jobCtx,
		info: Job{
			ID:            jobID,
			SessionID:     strings.TrimSpace(sessionID),
			Kind:          "shell",
			Command:       command,
			Cwd:           strings.TrimSpace(req.Cwd),
			Priority:      req.Priority,
			RestartPolicy: req.RestartPolicy,
			Status:        StatusPending,
			CreatedAt:     now,
			LogPath:       logPath,
			Metadata:      metadataFromRequest(req),
		},
		request: sanitizeBackgroundTaskArgs(req),
		output:  newOutputBuffer(m.config.MaxOutputBytes),
		logPath: logPath,
		cancel:  cancel,
	}
	managed.outputOffset = currentLogSize(logPath)

	m.mu.Lock()
	m.jobs[jobID] = managed
	m.mu.Unlock()

	if m.store != nil {
		_ = m.store.SaveJob(ctx, managed.info)
	}
	m.appendJobEvent(ctx, managed.info.ID, "queued", map[string]interface{}{
		"status": managed.info.Status,
	})

	m.notifyDispatcher()
	return managed.snapshot(), nil
}

// ReadOutput returns output for a job.
func (m *Manager) ReadOutput(ctx context.Context, req TaskOutputArgs) (TaskOutputResult, error) {
	if m == nil {
		return TaskOutputResult{}, fmt.Errorf("background manager is nil")
	}
	if err := ctx.Err(); err != nil {
		return TaskOutputResult{}, err
	}
	jobID := strings.TrimSpace(req.JobID)
	if jobID == "" {
		return TaskOutputResult{}, fmt.Errorf("job_id is required")
	}

	managed := m.getJob(jobID)
	if managed == nil && m.store != nil {
		job, err := m.store.GetJob(ctx, jobID)
		if err != nil {
			return TaskOutputResult{}, err
		}
		if job != nil {
			return m.readOutputFromLog(job.LogPath, jobID, job.Status, job.ExitCode, req.Offset, req.Limit)
		}
	}
	if managed == nil {
		return TaskOutputResult{}, fmt.Errorf("job not found: %s", jobID)
	}

	managed.mu.RLock()
	status := managed.info.Status
	exitCode := managed.info.ExitCode
	logPath := managed.logPath
	managed.mu.RUnlock()

	if logPath != "" {
		return m.readOutputFromLog(logPath, jobID, status, exitCode, req.Offset, req.Limit)
	}

	output, nextOffset := managed.output.Read(req.Offset, req.Limit)
	return TaskOutputResult{
		JobID:      jobID,
		Status:     string(status),
		Output:     output,
		NextOffset: nextOffset,
		ExitCode:   exitCode,
	}, nil
}

// GetJob returns a background job by id.
func (m *Manager) GetJob(ctx context.Context, jobID string) (*Job, error) {
	if m == nil {
		return nil, fmt.Errorf("background manager is nil")
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, fmt.Errorf("job_id is required")
	}
	if managed := m.getJob(jobID); managed != nil {
		return managed.snapshot(), nil
	}
	if m.store != nil {
		return m.store.GetJob(ctx, jobID)
	}
	return nil, fmt.Errorf("job not found: %s", jobID)
}

// CancelJob requests cancellation of a background job.
func (m *Manager) CancelJob(ctx context.Context, jobID string) (*Job, error) {
	if m == nil {
		return nil, fmt.Errorf("background manager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, fmt.Errorf("job_id is required")
	}
	managed := m.getJob(jobID)
	if managed == nil {
		return nil, fmt.Errorf("job not found: %s", jobID)
	}

	managed.mu.RLock()
	status := managed.info.Status
	managed.mu.RUnlock()
	if isTerminalStatus(status) {
		return managed.snapshot(), fmt.Errorf("job already finished: %s", status)
	}
	managed.mu.Lock()
	cancel := managed.cancel
	pid, hasPID := detachedPID(managed.info.Metadata)
	managed.mu.Unlock()

	if hasPID {
		_ = terminateProcess(pid)
	}
	if cancel != nil {
		cancel()
	}
	m.markCancelled(ctx, managed, "cancelled")
	return managed.snapshot(), nil
}

// ListJobs returns jobs matching the filter.
func (m *Manager) ListJobs(ctx context.Context, filter JobFilter) ([]Job, error) {
	if m == nil {
		return nil, fmt.Errorf("background manager is nil")
	}
	if m.store != nil {
		if lister, ok := m.store.(JobLister); ok {
			return lister.ListJobs(ctx, filter)
		}
	}
	m.mu.RLock()
	list := make([]*managedJob, 0, len(m.jobs))
	for _, job := range m.jobs {
		list = append(list, job)
	}
	m.mu.RUnlock()

	trimmedSession := strings.TrimSpace(filter.SessionID)
	statusFilter := make(map[JobStatus]bool)
	for _, status := range filter.Status {
		if strings.TrimSpace(string(status)) == "" {
			continue
		}
		statusFilter[status] = true
	}
	results := make([]Job, 0, len(list))
	for _, managed := range list {
		if managed == nil {
			continue
		}
		snapshot := managed.snapshot()
		if snapshot == nil {
			continue
		}
		if trimmedSession != "" && strings.TrimSpace(snapshot.SessionID) != trimmedSession {
			continue
		}
		if len(statusFilter) > 0 && !statusFilter[snapshot.Status] {
			continue
		}
		results = append(results, *snapshot)
	}
	if filter.Offset > 0 && filter.Offset < len(results) {
		results = results[filter.Offset:]
	} else if filter.Offset >= len(results) {
		return []Job{}, nil
	}
	if filter.Limit > 0 && filter.Limit < len(results) {
		results = results[:filter.Limit]
	}
	return results, nil
}

// ListEvents returns background job events for a job.
func (m *Manager) ListEvents(ctx context.Context, jobID string, afterSeq int64, limit int) ([]JobEvent, error) {
	if m == nil {
		return nil, fmt.Errorf("background manager is nil")
	}
	if m.store == nil {
		return nil, fmt.Errorf("background store is not configured")
	}
	reader, ok := m.store.(EventReader)
	if !ok {
		return nil, fmt.Errorf("background store does not support event queries")
	}
	return reader.ListEvents(ctx, jobID, afterSeq, limit)
}

func (m *Manager) runJob(managed *managedJob) {
	if managed == nil {
		return
	}
	if m.canUseDetachedExecution(managed) {
		m.runDetachedJob(managed)
		return
	}
	ctx := managed.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	req := managed.request
	managed.mu.Lock()
	if managed.info.Status == StatusCancelled {
		managed.scheduled = false
		managed.mu.Unlock()
		m.notifyDispatcher()
		return
	}
	managed.mu.Unlock()
	if err := ctx.Err(); err != nil {
		m.markCancelled(ctx, managed, err.Error())
		return
	}
	timeout := time.Duration(req.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = m.config.DefaultTimeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		m.markCancelled(ctx, managed, err.Error())
		return
	}

	startedAt := time.Now().UTC()
	managed.mu.Lock()
	if managed.info.Status == StatusCancelled {
		managed.mu.Unlock()
		m.notifyDispatcher()
		return
	}
	managed.info.Status = StatusRunning
	managed.info.StartedAt = &startedAt
	managed.info.Message = ""
	managed.mu.Unlock()
	if m.store != nil {
		_ = m.store.UpdateJob(context.Background(), managed.info)
	}
	m.appendJobEvent(context.Background(), managed.info.ID, "running", map[string]interface{}{
		"status": managed.info.Status,
	})

	cmd := buildShellCommand(ctx, managed.info.Command)
	if cmd == nil {
		m.failJob(managed, fmt.Errorf("unsupported shell command"))
		return
	}
	if managed.info.Cwd != "" {
		cmd.Dir = managed.info.Cwd
	}

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		if ctx.Err() == context.Canceled {
			m.markCancelled(ctx, managed, "cancelled")
			return
		}
		m.failJob(managed, err)
		return
	}

	var (
		logFile *os.File
	)
	if managed.logPath != "" {
		if file, err := os.OpenFile(managed.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			logFile = file
		}
	}
	writer := m.newJobOutputWriter(ctx, managed, logFile, "stdout")
	errWriter := m.newJobOutputWriter(ctx, managed, logFile, "stderr")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if stdout != nil {
			_, _ = io.Copy(writer, stdout)
		}
	}()
	go func() {
		defer wg.Done()
		if stderr != nil {
			_, _ = io.Copy(errWriter, stderr)
		}
	}()

	waitErr := cmd.Wait()
	wg.Wait()

	if logFile != nil {
		_ = logFile.Close()
	}
	if ctx.Err() == context.Canceled {
		m.markCancelled(ctx, managed, "cancelled")
		return
	}
	if ctx.Err() == context.DeadlineExceeded {
		m.failJob(managed, fmt.Errorf("command timed out"))
		return
	}
	if waitErr != nil {
		m.failJob(managed, waitErr)
		return
	}
	m.completeJob(managed, 0)
}

func (m *Manager) completeJob(managed *managedJob, exitCode int) {
	finishedAt := time.Now().UTC()
	managed.mu.Lock()
	if managed.info.Status == StatusCancelled {
		managed.scheduled = false
		managed.mu.Unlock()
		m.notifyDispatcher()
		return
	}
	managed.scheduled = false
	managed.info.Status = StatusCompleted
	managed.info.Message = ""
	managed.info.ExitCode = &exitCode
	managed.info.FinishedAt = &finishedAt
	managed.mu.Unlock()
	if m.store != nil {
		_ = m.store.UpdateJob(context.Background(), managed.info)
	}
	m.appendJobEvent(context.Background(), managed.info.ID, "completed", map[string]interface{}{
		"status":    managed.info.Status,
		"exit_code": exitCode,
	})
	m.notifyDispatcher()
}

func (m *Manager) failJob(managed *managedJob, err error) {
	message := ""
	if err != nil {
		message = err.Error()
	}
	m.failJobWithCode(managed, exitCodeFromError(err), message)
	if err != nil {
		managed.output.Write([]byte("\n" + err.Error()))
	}
}

func (m *Manager) failJobWithCode(managed *managedJob, exitCode int, message string) {
	finishedAt := time.Now().UTC()
	managed.mu.Lock()
	if managed.info.Status == StatusCancelled {
		managed.scheduled = false
		managed.mu.Unlock()
		m.notifyDispatcher()
		return
	}
	managed.scheduled = false
	managed.info.Status = StatusFailed
	managed.info.Message = strings.TrimSpace(message)
	managed.info.ExitCode = &exitCode
	managed.info.FinishedAt = &finishedAt
	managed.mu.Unlock()
	if m.store != nil {
		_ = m.store.UpdateJob(context.Background(), managed.info)
	}
	m.appendJobEvent(context.Background(), managed.info.ID, "failed", map[string]interface{}{
		"status":    managed.info.Status,
		"exit_code": exitCode,
		"error":     managed.info.Message,
	})
	m.notifyDispatcher()
}

func (m *Manager) markCancelled(ctx context.Context, managed *managedJob, reason string) {
	if managed == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(reason) == "" {
		reason = "cancelled"
	}
	finishedAt := time.Now().UTC()
	exitCode := -1
	managed.mu.Lock()
	if managed.info.Status == StatusCancelled {
		managed.scheduled = false
		managed.mu.Unlock()
		m.notifyDispatcher()
		return
	}
	if isTerminalStatus(managed.info.Status) {
		managed.scheduled = false
		managed.mu.Unlock()
		m.notifyDispatcher()
		return
	}
	managed.scheduled = false
	managed.info.Status = StatusCancelled
	managed.info.Message = reason
	managed.info.ExitCode = &exitCode
	managed.info.FinishedAt = &finishedAt
	managed.mu.Unlock()
	if m.store != nil {
		_ = m.store.UpdateJob(context.Background(), managed.info)
	}
	m.appendJobEvent(context.Background(), managed.info.ID, "cancelled", map[string]interface{}{
		"status": managed.info.Status,
		"reason": reason,
	})
	m.notifyDispatcher()
}

func (m *Manager) appendJobEvent(ctx context.Context, jobID, eventType string, payload map[string]interface{}) {
	if m == nil {
		return
	}
	normalizedPayload := make(map[string]interface{}, len(payload)+2)
	for key, value := range payload {
		normalizedPayload[key] = value
	}
	normalizedPayload["job_id"] = jobID
	if job := m.getJob(jobID); job != nil {
		job.mu.RLock()
		if strings.TrimSpace(job.info.SessionID) != "" {
			normalizedPayload["session_id"] = job.info.SessionID
		}
		job.mu.RUnlock()
	}
	event := JobEvent{
		JobID:     jobID,
		Type:      eventType,
		Payload:   normalizedPayload,
		CreatedAt: time.Now().UTC(),
	}
	if m.eventHandler != nil {
		m.eventHandler(event)
	}
	if m.store == nil {
		return
	}
	writer, ok := m.store.(EventWriter)
	if !ok {
		return
	}
	_ = writer.AppendEvent(ctx, jobID, eventType, normalizedPayload)
}

func (m *Manager) getJob(jobID string) *managedJob {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.jobs[jobID]
}

func (m *Manager) dispatchLoop() {
	defer close(m.doneCh)
	for {
		select {
		case <-m.stopCh:
			return
		case <-m.dispatchCh:
			m.dispatchPending()
		}
	}
}

func (m *Manager) notifyDispatcher() {
	if m == nil || m.dispatchCh == nil {
		return
	}
	select {
	case <-m.stopCh:
		return
	case m.dispatchCh <- struct{}{}:
	default:
	}
}

func (m *Manager) dispatchPending() {
	if m == nil {
		return
	}
	for {
		capacity, pending := m.pendingCandidates()
		if capacity <= 0 || len(pending) == 0 {
			return
		}
		sort.SliceStable(pending, func(i, j int) bool {
			left := pending[i]
			right := pending[j]
			left.mu.RLock()
			leftPriority := left.info.Priority
			leftCreated := left.info.CreatedAt
			leftID := left.info.ID
			left.mu.RUnlock()
			right.mu.RLock()
			rightPriority := right.info.Priority
			rightCreated := right.info.CreatedAt
			rightID := right.info.ID
			right.mu.RUnlock()
			if leftPriority != rightPriority {
				return leftPriority > rightPriority
			}
			if !leftCreated.Equal(rightCreated) {
				return leftCreated.Before(rightCreated)
			}
			return leftID < rightID
		})
		launched := false
		for _, managed := range pending {
			if capacity <= 0 {
				break
			}
			if !m.markScheduled(managed) {
				continue
			}
			capacity--
			launched = true
			m.jobWG.Add(1)
			go func(job *managedJob) {
				defer m.jobWG.Done()
				m.runJob(job)
			}(managed)
		}
		if !launched {
			return
		}
	}
}

func (m *Manager) pendingCandidates() (int, []*managedJob) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pending := make([]*managedJob, 0, len(m.jobs))
	active := 0
	for _, managed := range m.jobs {
		if managed == nil {
			continue
		}
		managed.mu.RLock()
		status := managed.info.Status
		scheduled := managed.scheduled
		managed.mu.RUnlock()
		if status == StatusRunning || scheduled {
			active++
			continue
		}
		if status == StatusPending {
			pending = append(pending, managed)
		}
	}
	return m.maxConcurrentJobs - active, pending
}

func (m *Manager) markScheduled(managed *managedJob) bool {
	if managed == nil {
		return false
	}
	managed.mu.Lock()
	defer managed.mu.Unlock()
	if managed.info.Status != StatusPending || managed.scheduled {
		return false
	}
	managed.scheduled = true
	return true
}

func (m *Manager) recoverPersistedJobs(ctx context.Context) {
	if m == nil || m.store == nil {
		return
	}
	lister, ok := m.store.(JobLister)
	if !ok {
		return
	}
	jobs, err := lister.ListJobs(ctx, JobFilter{
		Status: []JobStatus{StatusPending, StatusRunning},
	})
	if err != nil {
		return
	}
	for i := len(jobs) - 1; i >= 0; i-- {
		job := jobs[i]
		switch job.Status {
		case StatusPending:
			managed := m.managedJobFromStored(job)
			if managed == nil {
				continue
			}
			m.mu.Lock()
			if _, exists := m.jobs[job.ID]; !exists {
				m.jobs[job.ID] = managed
			}
			m.mu.Unlock()
			m.appendJobEvent(context.Background(), job.ID, "recovered_queued", map[string]interface{}{
				"status":          StatusPending,
				"previous_status": StatusPending,
			})
		case StatusRunning:
			if m.recoverDetachedRunningJob(job) {
				continue
			}
			req := requestFromJob(job)
			if normalizeRestartPolicy(req.RestartPolicy) == RestartPolicyRerun {
				recovered := job
				recovered.Status = StatusPending
				recovered.RestartPolicy = RestartPolicyRerun
				recovered.Message = "background manager restarted; rerunning job"
				recovered.StartedAt = nil
				recovered.FinishedAt = nil
				recovered.ExitCode = nil
				if recovered.Metadata == nil {
					recovered.Metadata = map[string]interface{}{}
				}
				recovered.Metadata["recovery_reason"] = recovered.Message
				_ = m.store.UpdateJob(context.Background(), recovered)
				managed := m.managedJobFromStored(recovered)
				if managed == nil {
					continue
				}
				m.mu.Lock()
				if _, exists := m.jobs[job.ID]; !exists {
					m.jobs[job.ID] = managed
				}
				m.mu.Unlock()
				m.appendJobEvent(context.Background(), job.ID, "recovered_requeued", map[string]interface{}{
					"status":          recovered.Status,
					"previous_status": StatusRunning,
					"restart_policy":  string(RestartPolicyRerun),
				})
				continue
			}
			recovered := job
			recovered.RestartPolicy = normalizeRestartPolicy(req.RestartPolicy)
			recovered.Status = StatusFailed
			recovered.Message = "background manager restarted before job completion"
			exitCode := -1
			recovered.ExitCode = &exitCode
			finishedAt := time.Now().UTC()
			recovered.FinishedAt = &finishedAt
			if recovered.Metadata == nil {
				recovered.Metadata = map[string]interface{}{}
			}
			recovered.Metadata["recovery_reason"] = recovered.Message
			_ = m.store.UpdateJob(context.Background(), recovered)
			m.appendJobEvent(context.Background(), job.ID, "recovered_failed", map[string]interface{}{
				"status":          recovered.Status,
				"previous_status": StatusRunning,
				"reason":          recovered.Message,
			})
		}
	}
}

func (m *Manager) managedJobFromStored(job Job) *managedJob {
	jobCtx, cancel := context.WithCancel(context.Background())
	managed := &managedJob{
		ctx:       jobCtx,
		info:      job,
		request:   requestFromJob(job),
		output:    newOutputBuffer(m.config.MaxOutputBytes),
		logPath:   strings.TrimSpace(job.LogPath),
		cancel:    cancel,
		scheduled: false,
	}
	managed.outputOffset = currentLogSize(managed.logPath)
	return managed
}

func (j *managedJob) snapshot() *Job {
	if j == nil {
		return nil
	}
	j.mu.RLock()
	defer j.mu.RUnlock()
	info := j.info
	return &info
}

func sanitizeBackgroundTaskArgs(req BackgroundTaskArgs) BackgroundTaskArgs {
	req.Command = strings.TrimSpace(req.Command)
	req.Cwd = strings.TrimSpace(req.Cwd)
	req.RestartPolicy = normalizeRestartPolicy(req.RestartPolicy)
	return req
}

func metadataFromRequest(req BackgroundTaskArgs) map[string]interface{} {
	metadata := make(map[string]interface{}, 2)
	if req.TimeoutSec > 0 {
		metadata["timeout_sec"] = req.TimeoutSec
	}
	if normalizeRestartPolicy(req.RestartPolicy) != RestartPolicyFail {
		metadata["restart_policy"] = string(normalizeRestartPolicy(req.RestartPolicy))
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func requestFromJob(job Job) BackgroundTaskArgs {
	req := sanitizeBackgroundTaskArgs(BackgroundTaskArgs{
		Command:       job.Command,
		Cwd:           job.Cwd,
		Priority:      job.Priority,
		RestartPolicy: job.RestartPolicy,
	})
	if timeoutSec, ok := intMetadataValue(job.Metadata, "timeout_sec"); ok {
		req.TimeoutSec = timeoutSec
	}
	if restartPolicy, ok := stringMetadataValue(job.Metadata, "restart_policy"); ok {
		req.RestartPolicy = RestartPolicy(restartPolicy)
	}
	return req
}

func intMetadataValue(metadata map[string]interface{}, key string) (int, bool) {
	if len(metadata) == 0 {
		return 0, false
	}
	value, ok := metadata[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func stringMetadataValue(metadata map[string]interface{}, key string) (string, bool) {
	if len(metadata) == 0 {
		return "", false
	}
	value, ok := metadata[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	return text, true
}

func normalizeRestartPolicy(policy RestartPolicy) RestartPolicy {
	switch RestartPolicy(strings.ToLower(strings.TrimSpace(string(policy)))) {
	case RestartPolicyRerun:
		return RestartPolicyRerun
	default:
		return RestartPolicyFail
	}
}

func currentLogSize(path string) int64 {
	path = strings.TrimSpace(path)
	if path == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func buildShellCommand(ctx context.Context, command string) *exec.Cmd {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, "/bin/sh", "-c", command)
}

func exitCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return -1
}

func isTerminalStatus(status JobStatus) bool {
	switch status {
	case StatusCompleted, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

type outputBuffer struct {
	mu         sync.RWMutex
	data       []byte
	baseOffset int64
	maxBytes   int
}

func newOutputBuffer(maxBytes int) *outputBuffer {
	if maxBytes <= 0 {
		maxBytes = DefaultConfig().MaxOutputBytes
	}
	return &outputBuffer{
		data:     make([]byte, 0, maxBytes),
		maxBytes: maxBytes,
	}
}

func (b *outputBuffer) Write(p []byte) (int, error) {
	if b == nil {
		return 0, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(p) == 0 {
		return 0, nil
	}
	if len(p) > b.maxBytes {
		p = p[len(p)-b.maxBytes:]
	}
	b.data = append(b.data, p...)
	if len(b.data) > b.maxBytes {
		overflow := len(b.data) - b.maxBytes
		b.data = append([]byte{}, b.data[overflow:]...)
		b.baseOffset += int64(overflow)
	}
	return len(p), nil
}

func (b *outputBuffer) Read(offset int64, limit int) (string, int64) {
	if b == nil {
		return "", 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()

	if offset < b.baseOffset {
		offset = b.baseOffset
	}
	start := int(offset - b.baseOffset)
	if start < 0 || start > len(b.data) {
		return "", b.baseOffset + int64(len(b.data))
	}
	end := len(b.data)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	chunk := b.data[start:end]
	next := b.baseOffset + int64(end)
	return string(chunk), next
}

func (m *Manager) readOutputFromLog(path, jobID string, status JobStatus, exitCode *int, offset int64, limit int) (TaskOutputResult, error) {
	if strings.TrimSpace(path) == "" {
		return TaskOutputResult{}, fmt.Errorf("log path not available for job %s", jobID)
	}
	file, err := os.Open(path)
	if err != nil {
		return TaskOutputResult{}, err
	}
	defer file.Close()

	if offset < 0 {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return TaskOutputResult{}, err
	}
	if limit <= 0 {
		limit = m.config.MaxOutputBytes
	}
	buf := make([]byte, limit)
	n, _ := file.Read(buf)
	nextOffset := offset + int64(n)
	return TaskOutputResult{
		JobID:      jobID,
		Status:     string(status),
		Output:     string(buf[:n]),
		NextOffset: nextOffset,
		ExitCode:   exitCode,
	}, nil
}

const defaultOutputEventChunkBytes = 4096

type jobOutputWriter struct {
	manager   *Manager
	job       *managedJob
	stream    string
	logFile   *os.File
	chunkSize int
	ctx       context.Context
}

func (m *Manager) newJobOutputWriter(ctx context.Context, job *managedJob, logFile *os.File, stream string) io.Writer {
	if ctx == nil {
		ctx = context.Background()
	}
	return &jobOutputWriter{
		manager:   m,
		job:       job,
		stream:    stream,
		logFile:   logFile,
		chunkSize: defaultOutputEventChunkBytes,
		ctx:       ctx,
	}
}

func (w *jobOutputWriter) Write(p []byte) (int, error) {
	if w == nil || w.job == nil {
		return 0, nil
	}
	if len(p) == 0 {
		return 0, nil
	}
	w.job.outputMu.Lock()
	defer w.job.outputMu.Unlock()

	_, _ = w.job.output.Write(p)
	if w.logFile != nil {
		_, _ = w.logFile.Write(p)
	}

	if w.manager == nil || w.manager.store == nil {
		w.job.outputOffset += int64(len(p))
		return len(p), nil
	}

	offset := w.job.outputOffset
	remaining := p
	for len(remaining) > 0 {
		chunkSize := w.chunkSize
		if chunkSize <= 0 {
			chunkSize = defaultOutputEventChunkBytes
		}
		if chunkSize > len(remaining) {
			chunkSize = len(remaining)
		}
		chunk := remaining[:chunkSize]
		remaining = remaining[chunkSize:]
		next := offset + int64(len(chunk))
		w.manager.appendJobEvent(w.ctx, w.job.info.ID, "output", map[string]interface{}{
			"offset":      offset,
			"next_offset": next,
			"size":        len(chunk),
			"stream":      w.stream,
			"chunk":       string(chunk),
		})
		offset = next
	}
	w.job.outputOffset = offset
	return len(p), nil
}
