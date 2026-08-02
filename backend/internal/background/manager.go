package background

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecution "github.com/wwsheng009/ai-agent-runtime/internal/execution"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// Config controls background execution defaults.
type Config struct {
	MaxOutputBytes          int
	DefaultTimeout          time.Duration
	MonitorInterval         time.Duration
	HeartbeatTimeout        time.Duration
	LaunchMaxAttempts       int
	RetryBackoff            time.Duration
	RecoveryMaxAttempts     int
	RecoveryBackoffSchedule []time.Duration
	StorePath               string
	StoreDSN                string
	LogDir                  string
	MaxConcurrentJobs       int
	Retention               time.Duration
	CleanupInterval         time.Duration
	EventHandler            func(JobEvent)
}

// DefaultConfig returns a conservative default config.
func DefaultConfig() Config {
	return Config{
		MaxOutputBytes:          1 * 1024 * 1024, // 1MB
		DefaultTimeout:          0,
		MonitorInterval:         250 * time.Millisecond,
		HeartbeatTimeout:        30 * time.Second,
		LaunchMaxAttempts:       3,
		RetryBackoff:            500 * time.Millisecond,
		RecoveryMaxAttempts:     -1,
		RecoveryBackoffSchedule: defaultBackgroundRecoverySchedule(),
		StorePath:               "",
		StoreDSN:                "",
		LogDir:                  "",
		MaxConcurrentJobs:       2,
		Retention:               30 * 24 * time.Hour,
		CleanupInterval:         time.Hour,
		EventHandler:            nil,
	}
}

func defaultBackgroundRecoverySchedule() []time.Duration {
	return []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 3 * time.Minute, 5 * time.Minute}
}

func normalizeBackgroundRecoverySchedule(schedule []time.Duration) []time.Duration {
	normalized := make([]time.Duration, 0, len(schedule))
	for _, delay := range schedule {
		if delay > 0 {
			normalized = append(normalized, delay)
		}
	}
	return normalized
}

func backgroundRecoveryDelay(schedule []time.Duration, attempt int) time.Duration {
	if len(schedule) == 0 {
		return 0
	}
	if attempt < 1 {
		attempt = 1
	}
	index := attempt - 1
	if index >= len(schedule) {
		index = len(schedule) - 1
	}
	return schedule[index]
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
	// scheduledAt records when the job was handed to a worker goroutine; the
	// watchdog uses it to reclaim slots that never transition to running.
	scheduledAt time.Time
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
	if cfg.MonitorInterval <= 0 {
		cfg.MonitorInterval = DefaultConfig().MonitorInterval
	}
	if cfg.HeartbeatTimeout <= 0 {
		cfg.HeartbeatTimeout = DefaultConfig().HeartbeatTimeout
	}
	if cfg.LaunchMaxAttempts <= 0 {
		cfg.LaunchMaxAttempts = DefaultConfig().LaunchMaxAttempts
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = DefaultConfig().RetryBackoff
	}
	if cfg.RecoveryMaxAttempts == 0 {
		cfg.RecoveryMaxAttempts = DefaultConfig().RecoveryMaxAttempts
	}
	cfg.RecoveryBackoffSchedule = normalizeBackgroundRecoverySchedule(cfg.RecoveryBackoffSchedule)
	if len(cfg.RecoveryBackoffSchedule) == 0 {
		cfg.RecoveryBackoffSchedule = defaultBackgroundRecoverySchedule()
	}
	if cfg.Retention == 0 {
		cfg.Retention = DefaultConfig().Retention
	}
	if cfg.CleanupInterval == 0 {
		cfg.CleanupInterval = DefaultConfig().CleanupInterval
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
	// Keep log-dir creation deferred until a job actually needs it so empty chat
	// bootstrap does not create background_logs just by wiring the manager.
	go manager.dispatchLoop()
	// recover/cleanup open the store only when a durable file already exists.
	manager.recoverPersistedJobs(context.Background())
	_, _ = manager.Cleanup(context.Background())
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
			job.mu.Lock()
			cancel := job.cancel
			if !isTerminalStatus(job.info.Status) {
				if job.info.Metadata == nil {
					job.info.Metadata = map[string]interface{}{}
				}
				job.info.Metadata["cancel_source"] = "runtime_shutdown"
			}
			job.mu.Unlock()
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
	startup := normalizeStartupAcceptance(req.Startup)
	if err := validateStartupAcceptance(startup); err != nil {
		return nil, runtimeerrors.WrapWithContext(runtimeerrors.ErrToolInvalidArgs, "invalid startup acceptance", err, nil)
	}
	req.Startup = &startup

	jobID := "job_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	now := time.Now().UTC()
	logPath := ""
	if m.logDir != "" {
		if err := os.MkdirAll(m.logDir, 0o755); err == nil {
			logPath = filepath.Join(m.logDir, jobID+".log")
			_ = os.WriteFile(logPath, []byte{}, 0o644)
		}
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
			Metadata:      metadataFromRequest(req, m.config.DefaultTimeout),
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
			result, readErr := m.readOutputFromLog(job.LogPath, jobID, job.Status, job.ExitCode, req.Offset, req.Limit)
			return decorateTaskOutputResult(result, *job), readErr
		}
	}
	if managed == nil {
		return TaskOutputResult{}, jobNotFoundError(jobID)
	}

	managed.mu.RLock()
	info := managed.info
	status := info.Status
	exitCode := info.ExitCode
	logPath := managed.logPath
	managed.mu.RUnlock()

	pendingDiag := TaskOutputResult{}
	if status == StatusPending {
		queuePosition, active, maxConcurrent := m.jobQueueDiagnostics(jobID)
		pendingDiag = m.pendingQueueDiagnostics(queuePosition, active, maxConcurrent, info.Metadata)
	}

	if logPath != "" {
		result, readErr := m.readOutputFromLog(logPath, jobID, status, exitCode, req.Offset, req.Limit)
		applyPendingQueueDiagnostics(&result, pendingDiag)
		return decorateTaskOutputResult(result, info), readErr
	}

	output, nextOffset := managed.output.Read(req.Offset, req.Limit)
	result := TaskOutputResult{
		JobID:      jobID,
		Status:     string(status),
		Output:     output,
		NextOffset: nextOffset,
		ExitCode:   exitCode,
	}
	applyPendingQueueDiagnostics(&result, pendingDiag)
	return decorateTaskOutputResult(result, info), nil
}

func applyPendingQueueDiagnostics(result *TaskOutputResult, diag TaskOutputResult) {
	if result == nil {
		return
	}
	result.QueuePosition = diag.QueuePosition
	result.ActiveJobs = diag.ActiveJobs
	result.MaxConcurrent = diag.MaxConcurrent
	result.SchedulerState = diag.SchedulerState
	result.NextAction = diag.NextAction
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
		job, err := m.store.GetJob(ctx, jobID)
		if err != nil {
			return nil, err
		}
		if job != nil {
			return job, nil
		}
	}
	return nil, jobNotFoundError(jobID)
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
		return nil, jobNotFoundError(jobID)
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
	if managed.info.Metadata == nil {
		managed.info.Metadata = map[string]interface{}{}
	}
	managed.info.Metadata["cancel_source"] = "user_request"
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

// Cleanup applies the configured retention policy to terminal job records and
// their manager-owned artifacts. A negative retention disables cleanup.
func (m *Manager) Cleanup(ctx context.Context) (int, error) {
	if m == nil || m.store == nil || m.config.Retention <= 0 {
		return 0, nil
	}
	pruner, ok := m.store.(JobPruner)
	if !ok {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	expired, err := pruner.PruneJobs(ctx, time.Now().UTC().Add(-m.config.Retention))
	if err != nil {
		return 0, err
	}
	for _, job := range expired {
		m.mu.Lock()
		delete(m.jobs, job.ID)
		m.mu.Unlock()
		m.removeOwnedJobArtifacts(job)
	}
	return len(expired), nil
}

func (m *Manager) removeOwnedJobArtifacts(job Job) {
	root := strings.TrimSpace(m.logDir)
	if root == "" {
		return
	}
	paths := []string{job.LogPath}
	for _, key := range []string{backgroundMetaStatusPath, backgroundMetaRunnerPath, backgroundMetaHeartbeatPath} {
		if path, ok := stringMetadataValue(job.Metadata, key); ok {
			paths = append(paths, path)
		}
	}
	for _, path := range paths {
		if pathWithinRoot(path, root) {
			_ = os.Remove(path)
		}
	}
}

func pathWithinRoot(path, root string) bool {
	path = strings.TrimSpace(path)
	root = strings.TrimSpace(root)
	if path == "" || root == "" {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(absRoot, absPath)
	if err != nil || relative == "." {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// runJobImpl is a seam used by tests to inject panic/block behavior into
// runJobSafely; production always routes through (*Manager).runJob.
var runJobImpl = func(m *Manager, managed *managedJob) {
	m.runJob(managed)
}

// runJobSafely runs a job in a worker goroutine and guarantees that a panic
// can never leak a scheduling slot: the job is failed and its slot released
// even if the execution path panics before reaching a terminal state.
func (m *Manager) runJobSafely(managed *managedJob) {
	defer func() {
		if r := recover(); r != nil {
			panicErr := fmt.Errorf("background job panicked: %v", r)
			m.failJobWithErrorCode(managed, runtimeerrors.ErrToolBrokerFailure, panicErr)
			// Defensive: guarantee the slot is released even if the failure
			// path above itself panicked.
			managed.mu.Lock()
			managed.scheduled = false
			if managed.info.Status == StatusPending {
				now := time.Now().UTC()
				managed.info.Status = StatusFailed
				managed.info.FinishedAt = &now
			}
			managed.mu.Unlock()
			m.notifyDispatcher()
		}
	}()
	runJobImpl(m, managed)
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
	if isTerminalStatus(managed.info.Status) {
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
		ctx, cancel = runtimeexecution.WithTimeoutSource(ctx, timeout, backgroundTimeoutSource(req))
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		m.markCancelled(ctx, managed, err.Error())
		return
	}

	cmd := buildShellCommand(ctx, managed.info.Command)
	if cmd == nil {
		m.failJobWithErrorCode(managed, runtimeerrors.ErrProcessStartFailed, fmt.Errorf("unsupported shell command"))
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
		m.failJobWithErrorCode(managed, runtimeerrors.ErrProcessStartFailed, err)
		return
	}

	startedAt := time.Now().UTC()
	processAlive := func() bool { return processIsAlive(cmd.Process) }
	if !m.acceptStartedProcess(ctx, managed, startedAt, cmd.Process.Pid, processAlive) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
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
		m.markTimedOut(managed, "command timed out")
		return
	}
	if waitErr != nil {
		// Process finished with a non-zero exit is content success: complete the
		// job and keep exit_code for callers. Only hard wait failures fail the job.
		if exitCode, ok := finishedProcessExitCode(waitErr); ok {
			m.completeJob(managed, exitCode)
			return
		}
		m.failJob(managed, waitErr)
		return
	}
	m.completeJob(managed, 0)
}

func processIsAlive(process *os.Process) bool {
	if process == nil || process.Pid <= 0 {
		return false
	}
	health := inspectProcess(process.Pid)
	return health.Running && !health.Zombie
}

func (m *Manager) acceptStartedProcess(ctx context.Context, managed *managedJob, startedAt time.Time, pid int, processAlive func() bool) bool {
	if managed == nil {
		return false
	}
	startup := normalizeStartupAcceptance(managed.request.Startup)
	managed.mu.Lock()
	if isTerminalStatus(managed.info.Status) {
		managed.scheduled = false
		managed.mu.Unlock()
		m.notifyDispatcher()
		return false
	}
	if managed.info.Metadata == nil {
		managed.info.Metadata = map[string]interface{}{}
	}
	managed.info.Metadata[backgroundMetaLaunchState] = launchStateProcessCreated
	managed.info.Metadata[backgroundMetaProcessStarted] = true
	managed.info.Metadata[backgroundMetaPID] = pid
	if health := inspectProcess(pid); health.Identity != "" {
		managed.info.Metadata[backgroundMetaProcessIdentity] = health.Identity
	}
	managed.info.StartedAt = &startedAt
	managed.info.Message = ""
	managed.info.ExitCode = nil
	managed.info.FinishedAt = nil
	managed.mu.Unlock()
	m.persistManagedJob(managed)
	m.appendJobEvent(context.Background(), managed.info.ID, "process_created", map[string]interface{}{
		"status": StatusPending,
		"pid":    pid,
	})

	managed.mu.Lock()
	managed.info.Metadata[backgroundMetaLaunchState] = launchStateAccepting
	if startup.Probe == StartupProbeNone {
		managed.info.Metadata[backgroundMetaHealthcheckState] = healthcheckStateNotConfigured
	} else {
		managed.info.Metadata[backgroundMetaHealthcheckState] = healthcheckStatePending
	}
	managed.mu.Unlock()
	m.persistManagedJob(managed)
	m.appendJobEvent(context.Background(), managed.info.ID, "startup_acceptance_pending", map[string]interface{}{
		"probe":           startup.Probe,
		"grace_period_ms": startup.GracePeriodMs,
		"timeout_ms":      startupProbeTimeout(startup).Milliseconds(),
	})

	if err := executeStartupProbe(ctx, startup, processAlive); err != nil {
		if ctx != nil && ctx.Err() != nil {
			if ctx.Err() == context.DeadlineExceeded {
				m.markTimedOut(managed, "command timed out during startup acceptance")
			} else {
				m.markCancelled(ctx, managed, "cancelled")
			}
			return false
		}
		m.failStartupAcceptance(managed, err)
		return false
	}

	acceptedAt := time.Now().UTC()
	managed.mu.Lock()
	if isTerminalStatus(managed.info.Status) {
		managed.scheduled = false
		managed.mu.Unlock()
		m.notifyDispatcher()
		return false
	}
	managed.info.Status = StatusRunning
	managed.info.Metadata[backgroundMetaLaunchState] = launchStateAccepted
	managed.info.Metadata[backgroundMetaStartupAcceptedAt] = acceptedAt.Format(time.RFC3339Nano)
	delete(managed.info.Metadata, backgroundMetaHealthcheckError)
	if startup.Probe == StartupProbeNone {
		managed.info.Metadata[backgroundMetaHealthcheckState] = healthcheckStateNotConfigured
	} else {
		managed.info.Metadata[backgroundMetaHealthcheckState] = healthcheckStatePassed
	}
	managed.scheduled = false
	managed.mu.Unlock()
	m.persistManagedJob(managed)
	m.appendJobEvent(context.Background(), managed.info.ID, "startup_accepted", map[string]interface{}{
		"status":      StatusRunning,
		"pid":         pid,
		"probe":       startup.Probe,
		"accepted_at": acceptedAt.Format(time.RFC3339Nano),
	})
	m.appendJobEvent(context.Background(), managed.info.ID, "running", map[string]interface{}{
		"status": StatusRunning,
		"pid":    pid,
	})
	return true
}

func (m *Manager) failStartupAcceptance(managed *managedJob, err error) {
	message := "startup acceptance failed"
	if err != nil {
		message = err.Error()
	}
	managed.mu.Lock()
	if managed.info.Metadata == nil {
		managed.info.Metadata = map[string]interface{}{}
	}
	managed.info.Metadata[backgroundMetaLaunchState] = launchStateFailed
	managed.info.Metadata[backgroundMetaHealthcheckState] = healthcheckStateFailed
	managed.info.Metadata[backgroundMetaHealthcheckError] = message
	managed.mu.Unlock()
	m.appendJobEvent(context.Background(), managed.info.ID, "startup_acceptance_failed", map[string]interface{}{
		"error_code": string(runtimeerrors.ErrProcessHealthcheck),
		"error":      message,
	})
	m.failJobWithErrorCode(managed, runtimeerrors.ErrProcessHealthcheck, err)
}

func (m *Manager) persistManagedJob(managed *managedJob) {
	if m == nil || m.store == nil || managed == nil {
		return
	}
	if snapshot := managed.snapshot(); snapshot != nil {
		_ = m.store.UpdateJob(context.Background(), *snapshot)
	}
}

func (m *Manager) completeJob(managed *managedJob, exitCode int) {
	m.completeJobWithMessage(managed, exitCode, "")
}

// completeJobWithMessage marks a finished process as completed regardless of exit
// code. Non-zero exits keep exit_code (and optional message/metadata) but do not
// set error_code / StatusFailed — those are reserved for hard failures.
func (m *Manager) completeJobWithMessage(managed *managedJob, exitCode int, message string) {
	finishedAt := time.Now().UTC()
	managed.mu.Lock()
	if isTerminalStatus(managed.info.Status) {
		managed.scheduled = false
		managed.mu.Unlock()
		m.notifyDispatcher()
		return
	}
	managed.scheduled = false
	managed.info.Status = StatusCompleted
	if strings.TrimSpace(message) == "" && exitCode != 0 {
		message = fmt.Sprintf("command exited with code %d", exitCode)
	}
	managed.info.Message = strings.TrimSpace(message)
	managed.info.ExitCode = &exitCode
	managed.info.FinishedAt = &finishedAt
	if managed.info.Metadata == nil {
		managed.info.Metadata = map[string]interface{}{}
	}
	if _, exists := managed.info.Metadata[backgroundMetaLaunchState]; !exists {
		managed.info.Metadata[backgroundMetaLaunchState] = launchStateAccepted
	}
	if exitCode != 0 {
		managed.info.Metadata["non_zero_exit"] = true
	} else {
		delete(managed.info.Metadata, "non_zero_exit")
	}
	// Finished processes are never hard tool failures; drop any stale error_code.
	delete(managed.info.Metadata, "error_code")
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
	m.failJobWithErrorCode(managed, runtimeerrors.ErrToolExecution, err)
}

func (m *Manager) failJobWithErrorCode(managed *managedJob, code runtimeerrors.ErrorCode, err error) {
	message := ""
	if err != nil {
		message = err.Error()
	}
	m.failJobWithCodeAndError(managed, exitCodeFromError(err), code, message)
	if err != nil {
		managed.output.Write([]byte("\n" + err.Error()))
	}
}

func (m *Manager) failJobWithCode(managed *managedJob, exitCode int, message string) {
	m.failJobWithCodeAndError(managed, exitCode, runtimeerrors.ErrToolExecution, message)
}

func (m *Manager) failJobWithCodeAndError(managed *managedJob, exitCode int, code runtimeerrors.ErrorCode, message string) {
	finishedAt := time.Now().UTC()
	managed.mu.Lock()
	if isTerminalStatus(managed.info.Status) {
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
	if managed.info.Metadata == nil {
		managed.info.Metadata = map[string]interface{}{}
	}
	managed.info.Metadata["error_code"] = string(code)
	if code == runtimeerrors.ErrProcessStartFailed || code == runtimeerrors.ErrProcessHealthcheck {
		managed.info.Metadata[backgroundMetaLaunchState] = launchStateFailed
	}
	managed.mu.Unlock()
	if m.store != nil {
		_ = m.store.UpdateJob(context.Background(), managed.info)
	}
	m.appendJobEvent(context.Background(), managed.info.ID, "failed", map[string]interface{}{
		"status":     managed.info.Status,
		"exit_code":  exitCode,
		"error_code": string(code),
		"error":      managed.info.Message,
	})
	m.notifyDispatcher()
}

func (m *Manager) markTimedOut(managed *managedJob, message string) {
	if managed == nil {
		return
	}
	finishedAt := time.Now().UTC()
	managed.mu.Lock()
	if isTerminalStatus(managed.info.Status) {
		managed.scheduled = false
		managed.mu.Unlock()
		m.notifyDispatcher()
		return
	}
	managed.scheduled = false
	managed.info.Status = StatusTimedOut
	managed.info.Message = strings.TrimSpace(message)
	managed.info.ExitCode = nil
	managed.info.FinishedAt = &finishedAt
	if managed.info.Metadata == nil {
		managed.info.Metadata = map[string]interface{}{}
	}
	managed.info.Metadata["error_code"] = string(runtimeerrors.ErrToolTimeout)
	managed.mu.Unlock()
	if m.store != nil {
		_ = m.store.UpdateJob(context.Background(), managed.info)
	}
	m.appendJobEvent(context.Background(), managed.info.ID, "timed_out", map[string]interface{}{
		"status":     managed.info.Status,
		"error_code": string(runtimeerrors.ErrToolTimeout),
		"error":      managed.info.Message,
	})
	m.notifyDispatcher()
}

func (m *Manager) orphanJob(managed *managedJob, message string) {
	if managed == nil {
		return
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = "background job outcome could not be determined"
	}
	finishedAt := time.Now().UTC()
	managed.mu.Lock()
	if isTerminalStatus(managed.info.Status) {
		managed.scheduled = false
		managed.mu.Unlock()
		return
	}
	managed.scheduled = false
	managed.info.Status = StatusOrphaned
	managed.info.Message = message
	managed.info.ExitCode = nil
	managed.info.FinishedAt = &finishedAt
	if managed.info.Metadata == nil {
		managed.info.Metadata = map[string]interface{}{}
	}
	managed.info.Metadata["error_code"] = string(runtimeerrors.ErrProcessHealthcheck)
	managed.mu.Unlock()
	if m.store != nil {
		_ = m.store.UpdateJob(context.Background(), managed.info)
	}
	m.appendJobEvent(context.Background(), managed.info.ID, "orphaned", map[string]interface{}{
		"status":     StatusOrphaned,
		"error_code": string(runtimeerrors.ErrProcessHealthcheck),
		"reason":     message,
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
	if managed.info.Metadata == nil {
		managed.info.Metadata = map[string]interface{}{}
	}
	managed.info.Metadata["error_code"] = string(runtimeerrors.ErrAgentRunCanceled)
	if _, exists := managed.info.Metadata["cancel_source"]; !exists {
		managed.info.Metadata["cancel_source"] = "parent_context"
	}
	managed.mu.Unlock()
	if m.store != nil {
		_ = m.store.UpdateJob(context.Background(), managed.info)
	}
	m.appendJobEvent(context.Background(), managed.info.ID, "cancelled", map[string]interface{}{
		"status":        managed.info.Status,
		"reason":        reason,
		"error_code":    string(runtimeerrors.ErrAgentRunCanceled),
		"cancel_source": managed.info.Metadata["cancel_source"],
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
	var cleanupTicker *time.Ticker
	var cleanup <-chan time.Time
	if m.config.Retention > 0 && m.config.CleanupInterval > 0 {
		cleanupTicker = time.NewTicker(m.config.CleanupInterval)
		cleanup = cleanupTicker.C
		defer cleanupTicker.Stop()
	}
	// The watchdog periodically reclaims slots held by jobs that were marked
	// scheduled but never transitioned to running (e.g. a worker goroutine
	// panicked or died). Without it, one leaked slot permanently freezes the
	// queue once MaxConcurrentJobs slots are exhausted.
	var watchdogTicker *time.Ticker
	var watchdog <-chan time.Time
	if m.config.MonitorInterval > 0 {
		watchdogTicker = time.NewTicker(m.config.MonitorInterval)
		watchdog = watchdogTicker.C
		defer watchdogTicker.Stop()
	}
	for {
		select {
		case <-m.stopCh:
			return
		case <-m.dispatchCh:
			m.dispatchPendingSafely()
		case <-watchdog:
			m.reclaimStuckScheduled()
		case <-cleanup:
			_, _ = m.Cleanup(context.Background())
		}
	}
}

// dispatchPendingSafely never lets a dispatch panic kill the scheduler loop:
// the loop goroutine must survive so later notifications can retry dispatch.
func (m *Manager) dispatchPendingSafely() {
	defer func() {
		if r := recover(); r != nil {
			if m.eventHandler != nil {
				m.eventHandler(JobEvent{
					JobID:     "",
					Type:      "scheduler_panic",
					Payload:   map[string]interface{}{"error": fmt.Sprintf("dispatch panic: %v", r)},
					CreatedAt: time.Now().UTC(),
				})
			}
		}
	}()
	m.dispatchPending()
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
				m.runJobSafely(job)
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
		_, waitingForRecovery := stringMetadataValue(managed.info.Metadata, backgroundMetaNextRecoveryAt)
		managed.mu.RUnlock()
		if scheduled && waitingForRecovery {
			continue
		}
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

// jobQueueDiagnostics reports, for a pending job, its 1-based dispatch queue
// position (0 when not queued), the number of jobs currently occupying a
// scheduling slot, and the configured slot capacity. The queue order mirrors
// dispatchPending: priority desc, then creation time asc.
func (m *Manager) jobQueueDiagnostics(jobID string) (queuePosition, active, maxConcurrent int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	maxConcurrent = m.maxConcurrentJobs
	type pendingEntry struct {
		id       string
		priority int
		created  time.Time
	}
	pending := make([]pendingEntry, 0)
	for _, managed := range m.jobs {
		if managed == nil {
			continue
		}
		managed.mu.RLock()
		status := managed.info.Status
		scheduled := managed.scheduled
		_, waitingForRecovery := stringMetadataValue(managed.info.Metadata, backgroundMetaNextRecoveryAt)
		priority := managed.info.Priority
		created := managed.info.CreatedAt
		id := managed.info.ID
		managed.mu.RUnlock()
		if scheduled && waitingForRecovery {
			continue
		}
		if status == StatusRunning || scheduled {
			active++
			continue
		}
		if status == StatusPending {
			pending = append(pending, pendingEntry{id: id, priority: priority, created: created})
		}
	}
	sort.SliceStable(pending, func(i, j int) bool {
		if pending[i].priority != pending[j].priority {
			return pending[i].priority > pending[j].priority
		}
		if !pending[i].created.Equal(pending[j].created) {
			return pending[i].created.Before(pending[j].created)
		}
		return pending[i].id < pending[j].id
	})
	for i, entry := range pending {
		if entry.id == jobID {
			queuePosition = i + 1
			break
		}
	}
	return queuePosition, active, maxConcurrent
}

// pendingQueueDiagnostics builds the caller-facing guidance for a job that is
// still pending, so LLM/tool callers can distinguish normal queuing from a
// saturated or recovering queue instead of guessing.
func (m *Manager) pendingQueueDiagnostics(queuePosition, active, maxConcurrent int, metadata map[string]interface{}) TaskOutputResult {
	diag := TaskOutputResult{
		QueuePosition: queuePosition,
		ActiveJobs:    active,
		MaxConcurrent: maxConcurrent,
	}
	if _, recovering := stringMetadataValue(metadata, backgroundMetaNextRecoveryAt); recovering && queuePosition == 0 {
		diag.SchedulerState = "recovering"
		diag.NextAction = "job is in automatic recovery backoff; wait for recovery or query again after next_recovery_at"
		return diag
	}
	switch {
	case maxConcurrent > 0 && active >= maxConcurrent:
		diag.SchedulerState = "saturated"
		diag.NextAction = fmt.Sprintf("queue saturated: %d/%d slots active; wait for a slot to free or cancel a stuck job", active, maxConcurrent)
	case queuePosition > 1:
		diag.SchedulerState = "queued"
		diag.NextAction = fmt.Sprintf("job queued at position %d; retry task_output shortly", queuePosition)
	default:
		diag.SchedulerState = "dispatched"
		diag.NextAction = "job is next in line or starting; retry task_output shortly"
	}
	return diag
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
	managed.scheduledAt = time.Now().UTC()
	return true
}

// reclaimStuckScheduled is the scheduling watchdog. It scans for jobs that
// were handed to a worker goroutine (scheduled) but never reached a running
// or terminal state within the stuck threshold, and reclaims their slots.
// Without this, a single panicked/dead worker goroutine would hold a slot
// forever and freeze the queue once MaxConcurrentJobs slots are exhausted.
func (m *Manager) reclaimStuckScheduled() {
	if m == nil {
		return
	}
	now := time.Now().UTC()
	stuck := make([]*managedJob, 0)
	m.mu.RLock()
	for _, managed := range m.jobs {
		if managed == nil {
			continue
		}
		managed.mu.RLock()
		scheduled := managed.scheduled
		status := managed.info.Status
		_, waitingForRecovery := stringMetadataValue(managed.info.Metadata, backgroundMetaNextRecoveryAt)
		elapsed := now.Sub(managed.scheduledAt)
		state, _ := stringMetadataValue(managed.info.Metadata, backgroundMetaLaunchState)
		startup := normalizeStartupAcceptance(managed.request.Startup)
		managed.mu.RUnlock()
		if !scheduled || status != StatusPending || waitingForRecovery {
			continue
		}
		threshold := m.scheduledStuckThreshold(state, startup)
		if elapsed >= threshold {
			stuck = append(stuck, managed)
		}
	}
	m.mu.RUnlock()
	for _, managed := range stuck {
		m.reclaimStuckJob(managed)
	}
}

// scheduledStuckThreshold returns how long a job may stay scheduled without
// transitioning to running before the watchdog reclaims its slot. While the
// startup probe is accepting (launch_state=accepting) the probe deadline
// dominates; otherwise HeartbeatTimeout (default 30s) is the budget.
func (m *Manager) scheduledStuckThreshold(state string, startup StartupAcceptance) time.Duration {
	if state == launchStateAccepting {
		// The probe self-terminates within startupProbeTimeout; add a margin
		// so a busy scheduler never reclaims a legitimately probing job.
		return startupProbeTimeout(startup) + 5*time.Second
	}
	if m.config.HeartbeatTimeout > 0 {
		return m.config.HeartbeatTimeout
	}
	return 30 * time.Second
}

// reclaimStuckJob fails a job whose worker goroutine never started execution
// and cancels its context so the goroutine (if alive) can unwind.
func (m *Manager) reclaimStuckJob(managed *managedJob) {
	if managed == nil {
		return
	}
	finishedAt := time.Now().UTC()
	managed.mu.Lock()
	if !managed.scheduled || managed.info.Status != StatusPending {
		managed.mu.Unlock()
		return
	}
	_, waitingForRecovery := stringMetadataValue(managed.info.Metadata, backgroundMetaNextRecoveryAt)
	if waitingForRecovery {
		managed.mu.Unlock()
		return
	}
	elapsed := time.Since(managed.scheduledAt)
	message := fmt.Sprintf("scheduler stuck: job scheduled %s ago but never started running", elapsed.Round(time.Second))
	managed.scheduled = false
	managed.info.Status = StatusFailed
	exitCode := -1
	managed.info.ExitCode = &exitCode
	managed.info.FinishedAt = &finishedAt
	managed.info.Message = message
	if managed.info.Metadata == nil {
		managed.info.Metadata = map[string]interface{}{}
	}
	managed.info.Metadata["error_code"] = string(runtimeerrors.ErrToolBrokerFailure)
	managed.info.Metadata[backgroundMetaLaunchState] = launchStateFailed
	cancel := managed.cancel
	managed.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.persistManagedJob(managed)
	m.appendJobEvent(context.Background(), managed.info.ID, "scheduler_stuck", map[string]interface{}{
		"status":     StatusFailed,
		"error_code": string(runtimeerrors.ErrToolBrokerFailure),
		"error":      message,
	})
	m.notifyDispatcher()
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
				recovered.RestartPolicy = RestartPolicyRerun
				if recovered.Metadata == nil {
					recovered.Metadata = map[string]interface{}{}
				}
				reason := "background manager restarted before the detached process outcome was recorded"
				recovered.Metadata["recovery_reason"] = reason
				managed := m.managedJobFromStored(recovered)
				if managed == nil {
					continue
				}
				m.mu.Lock()
				if _, exists := m.jobs[job.ID]; !exists {
					m.jobs[job.ID] = managed
				}
				m.mu.Unlock()
				m.jobWG.Add(1)
				go func(jobID string, recoveredJob *managedJob, recoveryReason string) {
					defer m.jobWG.Done()
					recoveryQueued := m.resumeDetachedRecovery(recoveredJob, recoveryReason)
					if !recoveryQueued {
						recoveryQueued = m.scheduleDetachedRecovery(recoveredJob, "manager_restarted", recoveryReason)
					}
					if !recoveryQueued {
						m.orphanJob(recoveredJob, "automatic recovery attempts exhausted after background manager restart")
						return
					}
					current := recoveredJob.snapshot()
					if current == nil || current.Status != StatusPending {
						return
					}
					m.appendJobEvent(context.Background(), jobID, "recovered_requeued", map[string]interface{}{
						"status":          current.Status,
						"previous_status": StatusRunning,
						"restart_policy":  string(RestartPolicyRerun),
					})
				}(job.ID, managed, reason)
				continue
			}
			recovered := job
			recovered.RestartPolicy = normalizeRestartPolicy(req.RestartPolicy)
			if recovered.Metadata == nil {
				recovered.Metadata = map[string]interface{}{}
			}
			recovered.Metadata["recovery_reason"] = "background manager restarted before job outcome was recorded"
			managed := m.managedJobFromStored(recovered)
			if managed == nil {
				continue
			}
			m.mu.Lock()
			if _, exists := m.jobs[job.ID]; !exists {
				m.jobs[job.ID] = managed
			}
			m.mu.Unlock()
			m.orphanJob(managed, "background manager restarted before job outcome was recorded")
		}
	}
}

func (m *Manager) managedJobFromStored(job Job) *managedJob {
	jobCtx, cancel := context.WithCancel(context.Background())
	request := requestFromJob(job)
	if job.Metadata == nil {
		job.Metadata = metadataFromRequest(request, m.config.DefaultTimeout)
	} else {
		defaults := metadataFromRequest(request, m.config.DefaultTimeout)
		for key, value := range defaults {
			if _, exists := job.Metadata[key]; !exists {
				job.Metadata[key] = value
			}
		}
	}
	managed := &managedJob{
		ctx:       jobCtx,
		info:      job,
		request:   request,
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
	info.Metadata = cloneJobMetadata(j.info.Metadata)
	if j.info.StartedAt != nil {
		startedAt := *j.info.StartedAt
		info.StartedAt = &startedAt
	}
	if j.info.FinishedAt != nil {
		finishedAt := *j.info.FinishedAt
		info.FinishedAt = &finishedAt
	}
	if j.info.ExitCode != nil {
		exitCode := *j.info.ExitCode
		info.ExitCode = &exitCode
	}
	return &info
}

func cloneJobMetadata(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}
	output := make(map[string]interface{}, len(input))
	for key, value := range input {
		output[key] = cloneJobMetadataValue(value)
	}
	return output
}

func cloneJobMetadataValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneJobMetadata(typed)
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneJobMetadataValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	case []map[string]interface{}:
		cloned := make([]map[string]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneJobMetadata(item)
		}
		return cloned
	default:
		return typed
	}
}

func sanitizeBackgroundTaskArgs(req BackgroundTaskArgs) BackgroundTaskArgs {
	req.Command = strings.TrimSpace(req.Command)
	req.Cwd = strings.TrimSpace(req.Cwd)
	req.RestartPolicy = normalizeRestartPolicy(req.RestartPolicy)
	startup := normalizeStartupAcceptance(req.Startup)
	req.Startup = &startup
	return req
}

func metadataFromRequest(req BackgroundTaskArgs, defaultTimeout time.Duration) map[string]interface{} {
	metadata := make(map[string]interface{}, 16)
	if req.TimeoutSec > 0 {
		metadata["timeout_sec"] = req.TimeoutSec
	}
	timeout := time.Duration(req.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	metadata["timeout_requested_ms"] = timeout.Milliseconds()
	metadata["timeout_effective_ms"] = timeout.Milliseconds()
	metadata["timeout_ms"] = timeout.Milliseconds()
	metadata["timeout_source"] = string(backgroundTimeoutSource(req))
	if normalizeRestartPolicy(req.RestartPolicy) != RestartPolicyFail {
		metadata["restart_policy"] = string(normalizeRestartPolicy(req.RestartPolicy))
	}
	startup := normalizeStartupAcceptance(req.Startup)
	metadata[backgroundMetaLaunchState] = launchStateQueued
	metadata[backgroundMetaProcessStarted] = false
	metadata[backgroundMetaStartupProbe] = string(startup.Probe)
	metadata[backgroundMetaStartupGraceMs] = startup.GracePeriodMs
	metadata[backgroundMetaStartupTimeoutMs] = startup.TimeoutMs
	if startup.Address != "" {
		metadata[backgroundMetaStartupAddress] = startup.Address
	}
	if startup.URL != "" {
		metadata[backgroundMetaStartupURL] = startup.URL
	}
	if startup.Probe == StartupProbeNone {
		metadata[backgroundMetaHealthcheckState] = healthcheckStateNotConfigured
	} else {
		metadata[backgroundMetaHealthcheckState] = healthcheckStatePending
	}
	return metadata
}

func backgroundTimeoutSource(req BackgroundTaskArgs) runtimeexecution.TimeoutSource {
	if req.TimeoutSec > 0 {
		return runtimeexecution.TimeoutSourceToolArgument
	}
	return runtimeexecution.TimeoutSourceToolDefault
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
	startup := normalizeStartupAcceptance(nil)
	if value, ok := stringMetadataValue(job.Metadata, backgroundMetaStartupProbe); ok {
		startup.Probe = StartupProbeType(value)
	}
	if value, ok := intMetadataValue(job.Metadata, backgroundMetaStartupGraceMs); ok {
		startup.GracePeriodMs = value
	}
	if value, ok := intMetadataValue(job.Metadata, backgroundMetaStartupTimeoutMs); ok {
		startup.TimeoutMs = value
	}
	startup.Address, _ = stringMetadataValue(job.Metadata, backgroundMetaStartupAddress)
	startup.URL, _ = stringMetadataValue(job.Metadata, backgroundMetaStartupURL)
	req.Startup = &startup
	return req
}

func decorateTaskOutputResult(result TaskOutputResult, job Job) TaskOutputResult {
	result.Message = strings.TrimSpace(job.Message)
	if value, ok := stringMetadataValue(job.Metadata, "error_code"); ok {
		result.ErrorCode = value
	}
	if value, ok := intMetadataValue(job.Metadata, "timeout_requested_ms"); ok {
		result.TimeoutRequestedMs = int64(value)
	}
	if value, ok := intMetadataValue(job.Metadata, "timeout_effective_ms"); ok {
		result.TimeoutEffectiveMs = int64(value)
	}
	if value, ok := stringMetadataValue(job.Metadata, "timeout_source"); ok {
		result.TimeoutSource = value
	}
	if value, ok := stringMetadataValue(job.Metadata, "cancel_source"); ok {
		result.CancelSource = value
	}
	if value, ok := stringMetadataValue(job.Metadata, backgroundMetaWatchdogState); ok {
		result.WatchdogState = value
	}
	if value, ok := stringMetadataValue(job.Metadata, backgroundMetaWatchdogCode); ok {
		result.WatchdogErrorCode = value
	}
	if value, ok := intMetadataValue(job.Metadata, "launch_attempt"); ok {
		result.LaunchAttempt = value
	}
	if value, ok := intMetadataValue(job.Metadata, "launch_max_attempts"); ok {
		result.LaunchMaxAttempts = value
	}
	if value, ok := intMetadataValue(job.Metadata, backgroundMetaRecoveryAttempt); ok {
		result.RecoveryAttempt = value
	}
	if value, ok := intMetadataValue(job.Metadata, backgroundMetaRecoveryMax); ok {
		result.RecoveryMaxAttempts = value
	}
	if value, ok := stringMetadataValue(job.Metadata, backgroundMetaNextRecoveryAt); ok {
		result.NextRecoveryAt = value
	}
	if value, ok := stringMetadataValue(job.Metadata, backgroundMetaLaunchState); ok {
		result.LaunchState = value
	}
	if value, ok := boolMetadataValue(job.Metadata, backgroundMetaProcessStarted); ok {
		result.ProcessStarted = value
	}
	if value, ok := stringMetadataValue(job.Metadata, backgroundMetaStartupProbe); ok {
		result.StartupProbe = value
	}
	if value, ok := intMetadataValue(job.Metadata, backgroundMetaStartupGraceMs); ok {
		result.StartupGraceMs = int64(value)
	}
	if value, ok := stringMetadataValue(job.Metadata, backgroundMetaStartupAcceptedAt); ok {
		result.StartupAcceptedAt = value
	}
	if value, ok := stringMetadataValue(job.Metadata, backgroundMetaHealthcheckState); ok {
		result.HealthcheckState = value
	}
	if value, ok := stringMetadataValue(job.Metadata, backgroundMetaHealthcheckError); ok {
		result.HealthcheckError = value
	}
	decorateTaskOutputHealth(&result, job, time.Now().UTC())
	return result
}

func decorateTaskOutputHealth(result *TaskOutputResult, job Job, now time.Time) {
	if result == nil {
		return
	}
	if pid, ok := detachedPID(job.Metadata); ok {
		health := inspectProcess(pid)
		alive := health.Running && !health.Zombie && detachedProcessMatches(job.Metadata, health)
		result.ProcessAlive = &alive
		switch {
		case health.Zombie:
			result.ProcessState = watchdogStateZombie
		case !health.Running:
			result.ProcessState = watchdogStateMissing
		case !detachedProcessMatches(job.Metadata, health):
			result.ProcessState = watchdogStatePIDReused
		default:
			result.ProcessState = "running"
		}
	}
	if path, ok := stringMetadataValue(job.Metadata, backgroundMetaHeartbeatPath); ok {
		if info, err := os.Stat(path); err == nil {
			result.HeartbeatAgeMs = nonNegativeDuration(now.Sub(info.ModTime())).Milliseconds()
		}
	}
	if info, err := os.Stat(strings.TrimSpace(job.LogPath)); err == nil && info.Size() > 0 {
		lastOutputAt := info.ModTime().UTC()
		result.LastOutputAt = lastOutputAt.Format(time.RFC3339Nano)
		result.QuietForMs = nonNegativeDuration(now.Sub(lastOutputAt)).Milliseconds()
	}
}

func boolMetadataValue(metadata map[string]interface{}, key string) (bool, bool) {
	if len(metadata) == 0 {
		return false, false
	}
	value, ok := metadata[key]
	if !ok {
		return false, false
	}
	typed, ok := value.(bool)
	return typed, ok
}

func nonNegativeDuration(value time.Duration) time.Duration {
	if value < 0 {
		return 0
	}
	return value
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
	// 使用智能 shell 检测，与 BashTool 保持一致
	shell := runtimeexecutor.DefaultUserShell()
	shellArgs := shell.DeriveExecArgs(command, false)
	return exec.CommandContext(ctx, shellArgs[0], shellArgs[1:]...)
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

// finishedProcessExitCode reports whether err represents a process that finished
// with a discrete exit status (including non-zero). Hard wait failures return false.
func finishedProcessExitCode(err error) (int, bool) {
	if err == nil {
		return 0, true
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), true
	}
	return -1, false
}

func isTerminalStatus(status JobStatus) bool {
	switch status {
	case StatusCompleted, StatusFailed, StatusTimedOut, StatusCancelled, StatusOrphaned:
		return true
	default:
		return false
	}
}

func jobNotFoundError(jobID string) error {
	return runtimeerrors.Newf(runtimeerrors.ErrJobNotFound, "background job not found: %s", strings.TrimSpace(jobID)).
		WithContext("job_id", strings.TrimSpace(jobID))
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
