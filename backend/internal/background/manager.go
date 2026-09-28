package background

import (
	"context"
	"errors"
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
	// InstanceID identifies this runtime process in the shared background
	// store. Empty generates a fresh per-process id (2026-09-28).
	InstanceID string
	// LeaseTTL bounds how long jobs stay owned after their owner stops
	// heartbeating. Default 60s.
	LeaseTTL time.Duration
	// HeartbeatInterval is how often the owner refreshes its instance
	// heartbeat and job leases. Default 10s.
	HeartbeatInterval time.Duration
	// QueueTimeout bounds how long a job may stay pending before it becomes
	// terminal "expired". Default 30m; a negative value disables expiry.
	QueueTimeout time.Duration
	// RecoverPendingOnStart re-queues persisted pending jobs when a manager
	// starts. It defaults to false: startup recovery marks pending jobs as
	// interrupted instead of resurrecting work whose owning process is gone
	// (2026-09-28).
	RecoverPendingOnStart bool
	EventHandler          func(JobEvent)
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
		RecoveryMaxAttempts:     3,
		RecoveryBackoffSchedule: defaultBackgroundRecoverySchedule(),
		StorePath:               "",
		StoreDSN:                "",
		LogDir:                  "",
		MaxConcurrentJobs:       4,
		Retention:               30 * 24 * time.Hour,
		CleanupInterval:         time.Hour,
		LeaseTTL:                60 * time.Second,
		HeartbeatInterval:       10 * time.Second,
		QueueTimeout:            30 * time.Minute,
		RecoverPendingOnStart:   false,
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
	runJobImplMu      sync.RWMutex
	runJobImpl        func(*Manager, *managedJob)
	// lastCreatedAt makes creation ordering deterministic even when the
	// platform clock has coarser resolution than two adjacent submissions.
	// Queue dispatch and diagnostics both use CreatedAt as their FIFO tie
	// breaker, so allowing equal timestamps would make insertion order depend
	// on the random UUID tie breaker.
	lastCreatedAt time.Time
	// waiters counts in-flight task_output long-polls so a terminal transition
	// during a wait can be recognized as "already observed" (see
	// observation.go). Lazily created; nil is a valid "no waiters" state.
	waiters *outputWaitRegistry
	// monitorRegistry holds the per-job monitor timers (see monitor.go). It is
	// lazily created for the same reason as waiters, and in-memory on purpose:
	// a monitor only nudges a live session.
	monitorRegistry *monitorRegistry
	// instanceID anchors job ownership and leases in the shared store
	// (2026-09-28). Registrations are lazy so an unused manager never creates
	// the store file.
	instanceID         string
	instanceHost       string
	instanceStartedAt  time.Time
	instanceMu         sync.Mutex
	instanceRegistered bool
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
	cancel      context.CancelFunc
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
	if strings.TrimSpace(cfg.InstanceID) == "" {
		cfg.InstanceID = "inst_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = DefaultConfig().LeaseTTL
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = DefaultConfig().HeartbeatInterval
	}
	if cfg.HeartbeatInterval > cfg.LeaseTTL {
		cfg.HeartbeatInterval = cfg.LeaseTTL / 2
	}
	if cfg.QueueTimeout == 0 {
		cfg.QueueTimeout = DefaultConfig().QueueTimeout
	}
	manager := &Manager{
		config:            cfg,
		jobs:              make(map[string]*managedJob),
		dispatchCh:        make(chan struct{}, 1),
		maxConcurrentJobs: cfg.MaxConcurrentJobs,
		stopCh:            make(chan struct{}),
		doneCh:            make(chan struct{}),
		instanceID:        strings.TrimSpace(cfg.InstanceID),
		instanceStartedAt: time.Now().UTC(),
	}
	if host, err := os.Hostname(); err == nil {
		manager.instanceHost = strings.TrimSpace(host)
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
		m.markInstanceStopped(context.Background())
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
	// Anchor the job to the session workspace root the policy resolved the cwd
	// argument against; otherwise exec.Cmd runs the job in the server process
	// directory (see resolveJobCwd).
	req.Cwd = resolveJobCwd(ctx, req.Cwd)
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
	if !now.After(m.lastCreatedAt) {
		now = m.lastCreatedAt.Add(time.Nanosecond)
		managed.info.CreatedAt = now
	}
	m.lastCreatedAt = now
	if m.instanceID != "" {
		managed.info.OwnerInstanceID = m.instanceID
	}
	queuedAt := managed.info.CreatedAt
	managed.info.QueuedAt = &queuedAt
	if m.config.QueueTimeout > 0 {
		deadline := queuedAt.Add(m.config.QueueTimeout)
		managed.info.DeadlineAt = &deadline
	}
	m.jobs[jobID] = managed
	m.mu.Unlock()

	m.ensureInstanceRegistered(ctx)
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
		pendingDiag = m.pendingQueueDiagnostics(queuePosition, active, maxConcurrent, info)
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
	return m.cancelJobWithSource(ctx, jobID, "user_request")
}

// cancelJobWithSource is the shared cancellation path: callers tag why the job
// was terminated (user request vs a monitor deadline) so the terminal evidence
// can tell the two apart.
func (m *Manager) cancelJobWithSource(ctx context.Context, jobID, cancelSource string) (*Job, error) {
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
		// The job is not in this instance's in-memory map (for example it was
		// created by another aicli process sharing the same store). Cancel
		// through the store so cross-instance cancellation works; task_kill
		// used to fail with JOB_NOT_FOUND here (2026-09-28).
		return m.cancelStoredJob(ctx, jobID, cancelSource)
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
	managed.info.Metadata["cancel_source"] = cancelSource
	managed.mu.Unlock()

	// Mark the job terminal *before* killing the process: once the signal
	// lands, the wait path may return immediately and try to finalize the job
	// from the signal exit code, racing this cancellation into a spurious
	// "completed with exit -1" (2026-09-27: TestMonitorMaxDurationCancelsJob).
	// Every completion path checks isTerminalStatus, so the terminal write
	// wins regardless of which goroutine gets there first.
	m.markCancelled(ctx, managed, "cancelled")
	if hasPID {
		_ = terminateJobProcess(pid)
	}
	if cancel != nil {
		cancel()
	}
	return managed.snapshot(), nil
}

// cancelStoredJob cancels a job that is not owned by this instance's in-memory
// map. The terminal transition is written through the store with CAS so a
// stale writer cannot overwrite it afterwards; the process kill is best effort
// (the pid may be unknown or already gone).
func (m *Manager) cancelStoredJob(ctx context.Context, jobID, cancelSource string) (*Job, error) {
	if m == nil || m.store == nil {
		return nil, jobNotFoundError(jobID)
	}
	for attempt := 0; attempt < 2; attempt++ {
		stored, err := m.store.GetJob(ctx, jobID)
		if err != nil {
			return nil, err
		}
		if stored == nil {
			return nil, jobNotFoundError(jobID)
		}
		if isTerminalStatus(stored.Status) {
			return stored, fmt.Errorf("job already finished: %s", stored.Status)
		}
		finishedAt := time.Now().UTC()
		exitCode := -1
		stored.Status = StatusCancelled
		stored.Message = "cancelled"
		stored.ExitCode = &exitCode
		stored.FinishedAt = &finishedAt
		if stored.Metadata == nil {
			stored.Metadata = map[string]interface{}{}
		}
		stored.Metadata["error_code"] = string(runtimeerrors.ErrAgentRunCanceled)
		stored.Metadata["cancel_source"] = cancelSource
		for _, key := range []string{backgroundMetaRecoveryAttempt, backgroundMetaRecoveryMax, backgroundMetaNextRecoveryAt, "recovery_reason"} {
			delete(stored.Metadata, key)
		}
		updated, err := m.updateStoredJobCAS(ctx, *stored, stored.StateVersion)
		if err != nil {
			return nil, err
		}
		if !updated {
			continue
		}
		stored.StateVersion++
		m.appendJobEvent(ctx, jobID, "cancelled", map[string]interface{}{
			"status":        stored.Status,
			"reason":        "cancelled",
			"error_code":    string(runtimeerrors.ErrAgentRunCanceled),
			"cancel_source": cancelSource,
		})
		if pid, ok := detachedPID(stored.Metadata); ok {
			if killErr := terminateJobProcess(pid); killErr != nil {
				m.appendJobEvent(ctx, jobID, "kill_failed", map[string]interface{}{
					"pid":   pid,
					"error": killErr.Error(),
				})
			}
		}
		return stored, nil
	}
	current, readErr := m.store.GetJob(ctx, jobID)
	if readErr == nil && current != nil {
		return current, fmt.Errorf("cancel raced with a concurrent update: %s", current.Status)
	}
	return nil, jobNotFoundError(jobID)
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

// setRunJobImpl is a test seam for injecting panic/block behavior into
// runJobSafely. Production leaves it nil and routes through (*Manager).runJob.
func (m *Manager) setRunJobImpl(impl func(*Manager, *managedJob)) {
	if m == nil {
		return
	}
	m.runJobImplMu.Lock()
	m.runJobImpl = impl
	m.runJobImplMu.Unlock()
}

func (m *Manager) currentRunJobImpl() func(*Manager, *managedJob) {
	if m == nil {
		return nil
	}
	m.runJobImplMu.RLock()
	impl := m.runJobImpl
	m.runJobImplMu.RUnlock()
	if impl == nil {
		impl = func(m *Manager, managed *managedJob) {
			m.runJob(managed)
		}
	}
	return impl
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
	m.currentRunJobImpl()(m, managed)
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

	// Wire the output sinks before Start and let os/exec drive them: with
	// StdoutPipe/StderrPipe, Wait closes the read end as soon as the process
	// exits, so a fast command's buffered output could be dropped before the
	// reader goroutines drained it (2026-09-27: `echo retry-succeeded` came
	// back with empty output while its status was already completed).
	var (
		logFile *os.File
	)
	if managed.logPath != "" {
		if file, err := os.OpenFile(managed.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			logFile = file
		}
	}
	cmd.Stdout = m.newJobOutputWriter(ctx, managed, logFile, "stdout")
	cmd.Stderr = m.newJobOutputWriter(ctx, managed, logFile, "stderr")

	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
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

	waitErr := cmd.Wait()

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
		if errors.Is(err, errProcessExitedBeforeAcceptance) && startup.Probe == StartupProbeProcess {
			// A one-shot command that finished inside its process-probe window is
			// done, not broken: `echo ok` must complete with its real exit code
			// instead of failing the healthcheck (2026-09-27: every fast
			// background command was reported failed here). Hand it to the
			// normal wait path as running so the wait decides the outcome and
			// the dispatch/event contract still sees it.
			managed.mu.Lock()
			managed.info.Status = StatusRunning
			managed.info.Metadata[backgroundMetaLaunchState] = launchStateExitedBeforeAcceptance
			managed.info.Metadata[backgroundMetaHealthcheckState] = healthcheckStateNotConfigured
			delete(managed.info.Metadata, backgroundMetaHealthcheckError)
			managed.scheduled = false
			managed.mu.Unlock()
			m.persistManagedJob(managed)
			m.appendJobEvent(context.Background(), managed.info.ID, "startup_exited_before_acceptance", map[string]interface{}{
				"status": StatusRunning,
				"pid":    pid,
			})
			m.appendJobEvent(context.Background(), managed.info.ID, "running", map[string]interface{}{
				"status": StatusRunning,
				"pid":    pid,
			})
			return true
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
	_ = m.persistJobStateCAS(managed)
}

// casStoreWriter is implemented by stores that support optimistic-concurrency
// updates (the SQLite store). Stores without it fall back to a best-effort
// overwrite so alternative Store implementations keep working.
type casStoreWriter interface {
	UpdateJobCAS(ctx context.Context, job Job, expectedVersion int64) (bool, error)
}

func (m *Manager) updateStoredJobCAS(ctx context.Context, job Job, expectedVersion int64) (bool, error) {
	if m == nil || m.store == nil {
		return false, fmt.Errorf("background store is not configured")
	}
	if cas, ok := m.store.(casStoreWriter); ok {
		return cas.UpdateJobCAS(ctx, job, expectedVersion)
	}
	if err := m.store.UpdateJob(ctx, job); err != nil {
		return false, err
	}
	return true, nil
}

// persistJobStateCAS writes the job's current in-memory state through the store
// with optimistic concurrency, and reports whether the write landed.
//
// On a version conflict the persisted row was changed by another writer
// (another manager instance sharing the database, or a racing goroutine), so
// the helper re-reads it: a terminal row is adopted locally and the job stops
// writing (terminal states are absorbing); a non-terminal row is retried once
// with the fresh version.
func (m *Manager) persistJobStateCAS(managed *managedJob) bool {
	if managed == nil {
		return false
	}
	if m == nil || m.store == nil {
		return true // in-memory manager: the local state is authoritative
	}
	for attempt := 0; attempt < 2; attempt++ {
		snapshot := managed.snapshot()
		if snapshot == nil {
			return false
		}
		updated, err := m.updateStoredJobCAS(context.Background(), *snapshot, snapshot.StateVersion)
		if err != nil {
			return false
		}
		if updated {
			managed.mu.Lock()
			if managed.info.StateVersion == snapshot.StateVersion {
				managed.info.StateVersion = snapshot.StateVersion + 1
			}
			managed.mu.Unlock()
			return true
		}
		current, readErr := m.store.GetJob(context.Background(), snapshot.ID)
		if readErr != nil || current == nil {
			return false
		}
		if isTerminalStatus(current.Status) {
			// The persisted terminal state is authoritative: the local write
			// lost the CAS race, so adopt the stored outcome unconditionally
			// (2026-09-28 — otherwise the losing writer keeps a divergent
			// terminal status locally and records a misleading event).
			m.adoptStoredJob(managed, *current)
			m.notifyDispatcher()
			return false
		}
		managed.mu.Lock()
		managed.info.StateVersion = current.StateVersion
		managed.mu.Unlock()
	}
	return false
}

// terminalTransitionVisible reports whether the terminal status this writer
// intended is the one currently visible on the managed job. When a CAS persist
// lost the race against another writer, the losing transition must not record a
// terminal event (2026-09-28: cancelled rows were followed by bogus orphaned
// events from stale watchdogs).
func (m *Manager) terminalTransitionVisible(managed *managedJob, intended JobStatus) bool {
	if managed == nil {
		return false
	}
	managed.mu.RLock()
	defer managed.mu.RUnlock()
	return managed.info.Status == intended
}

// instanceStoreWriter is implemented by the SQLite store: instance heartbeats,
// lease renewal and clean shutdown records for the ownership model.
type instanceStoreWriter interface {
	UpsertRuntimeInstance(ctx context.Context, instance RuntimeInstance) error
	MarkRuntimeInstanceStopped(ctx context.Context, instanceID string, at time.Time) error
	RenewJobLeases(ctx context.Context, ownerInstanceID string, leaseExpiresAt time.Time) (int64, error)
	ReleaseJobLeases(ctx context.Context, ownerInstanceID string, releasedAt time.Time) (int64, error)
}

// ensureInstanceRegistered registers this runtime process in the shared store.
// Registration is lazy on purpose: an unused manager must not create the store
// file (empty-chat bootstrap stays side-effect free).
func (m *Manager) ensureInstanceRegistered(ctx context.Context) {
	if m == nil || m.store == nil || m.instanceID == "" {
		return
	}
	writer, ok := m.store.(instanceStoreWriter)
	if !ok {
		return
	}
	m.instanceMu.Lock()
	registered := m.instanceRegistered
	m.instanceMu.Unlock()
	if registered {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	instance := RuntimeInstance{
		ID:          m.instanceID,
		PID:         os.Getpid(),
		Host:        m.instanceHost,
		StartedAt:   m.instanceStartedAt,
		HeartbeatAt: time.Now().UTC(),
		State:       "running",
	}
	if err := writer.UpsertRuntimeInstance(ctx, instance); err != nil {
		return
	}
	m.instanceMu.Lock()
	m.instanceRegistered = true
	m.instanceMu.Unlock()
}

// heartbeatInstance refreshes this instance's liveness and renews the leases of
// the non-terminal jobs it owns.
func (m *Manager) heartbeatInstance(ctx context.Context) {
	if m == nil || m.store == nil || m.instanceID == "" {
		return
	}
	m.instanceMu.Lock()
	registered := m.instanceRegistered
	m.instanceMu.Unlock()
	if !registered {
		return
	}
	writer, ok := m.store.(instanceStoreWriter)
	if !ok {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()
	_ = writer.UpsertRuntimeInstance(ctx, RuntimeInstance{
		ID:          m.instanceID,
		PID:         os.Getpid(),
		Host:        m.instanceHost,
		StartedAt:   m.instanceStartedAt,
		HeartbeatAt: now,
		State:       "running",
	})
	if m.config.LeaseTTL > 0 {
		_, _ = writer.RenewJobLeases(ctx, m.instanceID, now.Add(m.config.LeaseTTL))
	}
}

// markInstanceStopped records a clean shutdown so peers stop treating this
// instance's leases as live.
func (m *Manager) markInstanceStopped(ctx context.Context) {
	if m == nil || m.store == nil || m.instanceID == "" {
		return
	}
	m.instanceMu.Lock()
	registered := m.instanceRegistered
	m.instanceMu.Unlock()
	if !registered {
		return
	}
	if writer, ok := m.store.(instanceStoreWriter); ok {
		if ctx == nil {
			ctx = context.Background()
		}
		now := time.Now().UTC()
		_ = writer.MarkRuntimeInstanceStopped(ctx, m.instanceID, now)
		// A clean shutdown releases ownership immediately instead of making
		// peers wait out the full TTL before they can recover still-running
		// detached processes.
		_, _ = writer.ReleaseJobLeases(ctx, m.instanceID, now)
	}
}

// ownedByLivePeer reports whether another runtime instance still holds a live
// lease on the job: such jobs must never be recovered or rewritten here
// (2026-09-28).
func (m *Manager) ownedByLivePeer(job Job) bool {
	if m == nil {
		return false
	}
	owner := strings.TrimSpace(job.OwnerInstanceID)
	if owner == "" || owner == m.instanceID {
		return false
	}
	return job.LeaseExpiresAt != nil && !job.LeaseExpiresAt.IsZero() && time.Now().UTC().Before(*job.LeaseExpiresAt)
}

// adoptStoredJob replaces the local view with the persisted row (status,
// timestamps, metadata, version). It is how a losing writer converges on the
// authoritative terminal state, and how the dispatcher drops jobs it no longer
// owns.
func (m *Manager) adoptStoredJob(managed *managedJob, stored Job) {
	if managed == nil {
		return
	}
	managed.mu.Lock()
	managed.info.Status = stored.Status
	managed.info.Message = stored.Message
	managed.info.FinishedAt = stored.FinishedAt
	managed.info.ExitCode = stored.ExitCode
	managed.info.StateVersion = stored.StateVersion
	managed.info.OwnerInstanceID = stored.OwnerInstanceID
	managed.info.LeaseExpiresAt = stored.LeaseExpiresAt
	managed.info.QueuedAt = stored.QueuedAt
	managed.info.DeadlineAt = stored.DeadlineAt
	if managed.info.Metadata == nil {
		managed.info.Metadata = map[string]interface{}{}
	}
	for key, value := range stored.Metadata {
		managed.info.Metadata[key] = cloneJobMetadataValue(value)
	}
	if isTerminalStatus(stored.Status) {
		managed.scheduled = false
	}
	managed.mu.Unlock()
}

// refreshFromStore re-reads the persisted row before dispatch and reports
// whether this instance may still run the job. Terminal rows are adopted
// locally; rows owned by another live instance, or past their queue deadline,
// are dropped or expired (2026-09-28).
func (m *Manager) refreshFromStore(managed *managedJob) bool {
	if managed == nil || m == nil {
		return false
	}
	if m.store == nil {
		if m.deadlinePassed(managed) {
			m.expireQueuedJob(managed)
			return false
		}
		return true
	}
	snapshot := managed.snapshot()
	if snapshot == nil {
		return false
	}
	stored, err := m.store.GetJob(context.Background(), snapshot.ID)
	if err != nil || stored == nil {
		return false
	}
	if isTerminalStatus(stored.Status) {
		m.adoptStoredJob(managed, *stored)
		return false
	}
	if owner := strings.TrimSpace(stored.OwnerInstanceID); owner != "" && owner != m.instanceID {
		// Another instance owns this job (for example an opted-in recovery
		// picked it up first): drop the local handle instead of racing it.
		m.adoptStoredJob(managed, *stored)
		return false
	}
	if stored.DeadlineAt != nil && !stored.DeadlineAt.IsZero() && time.Now().UTC().After(*stored.DeadlineAt) {
		m.expireQueuedJob(managed)
		return false
	}
	managed.mu.Lock()
	managed.info.StateVersion = stored.StateVersion
	managed.mu.Unlock()
	return true
}

func (m *Manager) deadlinePassed(managed *managedJob) bool {
	if managed == nil {
		return false
	}
	managed.mu.RLock()
	deadline := managed.info.DeadlineAt
	managed.mu.RUnlock()
	return deadline != nil && !deadline.IsZero() && time.Now().UTC().After(*deadline)
}

// expireQueuedJob marks a pending job that outlived its queue deadline as
// terminal "expired" (2026-09-28): bounded queues never leave jobs pending
// forever.
func (m *Manager) expireQueuedJob(managed *managedJob) {
	if managed == nil {
		return
	}
	finishedAt := time.Now().UTC()
	managed.mu.Lock()
	if isTerminalStatus(managed.info.Status) {
		managed.scheduled = false
		managed.mu.Unlock()
		return
	}
	// A dispatched job that is still starting (scheduled) has left the queue:
	// the queue deadline no longer applies to it (2026-09-28).
	if managed.scheduled {
		managed.mu.Unlock()
		return
	}
	managed.scheduled = false
	managed.info.Status = StatusExpired
	managed.info.Message = "queue deadline exceeded; job was never dispatched"
	managed.info.ExitCode = nil
	managed.info.FinishedAt = &finishedAt
	managed.mu.Unlock()
	m.persistJobStateCAS(managed)
	if !m.terminalTransitionVisible(managed, StatusExpired) {
		m.notifyDispatcher()
		return
	}
	m.appendJobEvent(context.Background(), managed.info.ID, "expired", map[string]interface{}{
		"status": StatusExpired,
		"reason": "queue deadline exceeded",
	})
	m.notifyDispatcher()
}

// expireStoredJob expires a persisted pending job without loading it into
// memory (startup recovery path).
func (m *Manager) expireStoredJob(ctx context.Context, job Job) bool {
	if m == nil || m.store == nil || isTerminalStatus(job.Status) {
		return false
	}
	if job.DeadlineAt == nil || job.DeadlineAt.IsZero() || !time.Now().UTC().After(*job.DeadlineAt) {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	finishedAt := time.Now().UTC()
	updated := job
	updated.Status = StatusExpired
	updated.Message = "queue deadline exceeded; job was never dispatched"
	updated.ExitCode = nil
	updated.FinishedAt = &finishedAt
	ok, err := m.updateStoredJobCAS(ctx, updated, job.StateVersion)
	if err != nil || !ok {
		return false
	}
	m.appendJobEvent(ctx, job.ID, "expired", map[string]interface{}{
		"status": StatusExpired,
		"reason": "queue deadline exceeded",
	})
	return true
}

// expireOverdueQueuedJobs expires in-memory pending jobs past their deadline;
// it runs on the watchdog tick so jobs blocked by a saturated queue still reach
// a terminal state.
func (m *Manager) expireOverdueQueuedJobs() {
	if m == nil {
		return
	}
	now := time.Now().UTC()
	overdue := make([]*managedJob, 0)
	m.mu.RLock()
	for _, managed := range m.jobs {
		if managed == nil {
			continue
		}
		managed.mu.RLock()
		status := managed.info.Status
		scheduled := managed.scheduled
		deadline := managed.info.DeadlineAt
		managed.mu.RUnlock()
		if status != StatusPending || scheduled || deadline == nil || deadline.IsZero() {
			continue
		}
		if now.After(*deadline) {
			overdue = append(overdue, managed)
		}
	}
	m.mu.RUnlock()
	for _, managed := range overdue {
		m.expireQueuedJob(managed)
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
	m.persistJobStateCAS(managed)
	if !m.terminalTransitionVisible(managed, StatusCompleted) {
		m.notifyDispatcher()
		return
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
	m.persistJobStateCAS(managed)
	if !m.terminalTransitionVisible(managed, StatusFailed) {
		m.notifyDispatcher()
		return
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
	m.persistJobStateCAS(managed)
	if !m.terminalTransitionVisible(managed, StatusTimedOut) {
		m.notifyDispatcher()
		return
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
	m.persistJobStateCAS(managed)
	if !m.terminalTransitionVisible(managed, StatusOrphaned) {
		m.notifyDispatcher()
		return
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
	// Cancellation is final: drop any scheduled recovery so a later recovery
	// pass cannot resurrect the job (2026-09-28).
	for _, key := range []string{backgroundMetaRecoveryAttempt, backgroundMetaRecoveryMax, backgroundMetaNextRecoveryAt, "recovery_reason"} {
		delete(managed.info.Metadata, key)
	}
	managed.mu.Unlock()
	m.persistJobStateCAS(managed)
	if !m.terminalTransitionVisible(managed, StatusCancelled) {
		m.notifyDispatcher()
		return
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
	if IsTerminalStatus(JobStatus(eventType)) {
		// 终态事件先打「已被 in-flight wait 观察」标记：宿主投影据此只落
		// durable 记录、不再调度一次冗余的唤醒 turn（模型马上就能从
		// task_output 的结果里读到同一份终态）。
		m.markTerminalObservedByWaiter(jobID)
		// 终态迁移后，该 job 的 monitor 计时器不再有意义：迟到的 check 只会在
		// 终态唤醒之后再补一次重复提示。
		m.cancelJobMonitors(jobID)
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
	var heartbeatTicker *time.Ticker
	var heartbeat <-chan time.Time
	if m.config.HeartbeatInterval > 0 {
		heartbeatTicker = time.NewTicker(m.config.HeartbeatInterval)
		heartbeat = heartbeatTicker.C
		defer heartbeatTicker.Stop()
	}
	for {
		select {
		case <-m.stopCh:
			return
		case <-m.dispatchCh:
			m.dispatchPendingSafely()
		case <-watchdog:
			m.reclaimStuckScheduled()
			m.expireOverdueQueuedJobs()
		case <-heartbeat:
			m.heartbeatInstance(context.Background())
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
			// Re-read the shared row right before dispatch: a job cancelled or
			// claimed elsewhere must not start here (2026-09-28).
			if !m.refreshFromStore(managed) {
				continue
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
func (m *Manager) pendingQueueDiagnostics(queuePosition, active, maxConcurrent int, job Job) TaskOutputResult {
	diag := TaskOutputResult{
		QueuePosition: queuePosition,
		ActiveJobs:    active,
		MaxConcurrent: maxConcurrent,
	}
	if job.QueuedAt != nil && !job.QueuedAt.IsZero() {
		diag.QueuedAt = job.QueuedAt.UTC().Format(time.RFC3339Nano)
	}
	deadlineNote := ""
	if job.DeadlineAt != nil && !job.DeadlineAt.IsZero() {
		diag.DeadlineAt = job.DeadlineAt.UTC().Format(time.RFC3339Nano)
		if remaining := time.Until(*job.DeadlineAt); remaining > 0 {
			deadlineNote = fmt.Sprintf(" (queue deadline in %s)", remaining.Round(time.Second))
		} else {
			deadlineNote = " (queue deadline exceeded; the job is about to expire)"
		}
	}
	if _, recovering := stringMetadataValue(job.Metadata, backgroundMetaNextRecoveryAt); recovering && queuePosition == 0 {
		diag.SchedulerState = "recovering"
		diag.NextAction = "job is in automatic recovery backoff; wait for recovery or query again after next_recovery_at"
		return diag
	}
	switch {
	case maxConcurrent > 0 && active >= maxConcurrent:
		diag.SchedulerState = "saturated"
		diag.NextAction = fmt.Sprintf("queue saturated: %d/%d slots active; wait for a slot to free or cancel a stuck job%s", active, maxConcurrent, deadlineNote)
	case queuePosition > 1:
		diag.SchedulerState = "queued"
		diag.NextAction = fmt.Sprintf("job queued at position %d; retry task_output shortly%s", queuePosition, deadlineNote)
	default:
		diag.SchedulerState = "dispatched"
		diag.NextAction = "job is next in line or starting; retry task_output shortly" + deadlineNote
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
	// Register only when the shared store actually opened (an existing file);
	// unused managers must keep bootstrap side-effect free.
	if opener, ok := m.store.(interface{ Opened() bool }); !ok || opener.Opened() {
		m.ensureInstanceRegistered(ctx)
	}
	for i := len(jobs) - 1; i >= 0; i-- {
		job := jobs[i]
		if m.ownedByLivePeer(job) {
			// Another runtime instance still heartbeats this job: leave it
			// alone instead of racing it (2026-09-28).
			continue
		}
		switch job.Status {
		case StatusPending:
			if m.config.RecoverPendingOnStart || strings.TrimSpace(job.OwnerInstanceID) == m.instanceID {
				if m.expireStoredJob(ctx, job) {
					continue
				}
				// Explicit opt-in (legacy behavior): re-queue pending jobs on
				// startup. Off by default — startup must not resurrect work
				// whose owning process is gone (2026-09-28).
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
				continue
			}
			m.interruptStoredJob(context.Background(), job, "owner process gone; not auto-resumed")
		case StatusRunning:
			if m.recoverDetachedRunningJob(job) {
				continue
			}
			// The recorded process is gone. Rerun policy no longer re-queues on
			// restart: a job whose owner died (or that was cancelled) must not
			// be resurrected by the next instance that opens the store
			// (2026-09-28). Callers can re-submit explicitly instead.
			recovered := job
			recovered.RestartPolicy = normalizeRestartPolicy(requestFromJob(job).RestartPolicy)
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

// interruptStoredJob marks a persisted pending job as interrupted instead of
// re-queueing it. Startup recovery must never resurrect work from a process
// that is gone (2026-09-28: pending jobs sat for days and then ran en masse on
// the next aicli start).
func (m *Manager) interruptStoredJob(ctx context.Context, job Job, message string) {
	if m == nil || m.store == nil || isTerminalStatus(job.Status) {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	finishedAt := time.Now().UTC()
	updated := job
	updated.Status = StatusInterrupted
	updated.Message = strings.TrimSpace(message)
	updated.ExitCode = nil
	updated.FinishedAt = &finishedAt
	if updated.Metadata == nil {
		updated.Metadata = map[string]interface{}{}
	}
	for _, key := range []string{backgroundMetaRecoveryAttempt, backgroundMetaRecoveryMax, backgroundMetaNextRecoveryAt, "recovery_reason"} {
		delete(updated.Metadata, key)
	}
	ok, err := m.updateStoredJobCAS(ctx, updated, job.StateVersion)
	if err != nil || !ok {
		return
	}
	m.appendJobEvent(ctx, job.ID, "recovered_pending_ignored", map[string]interface{}{
		"status":          StatusInterrupted,
		"previous_status": StatusPending,
		"reason":          strings.TrimSpace(message),
	})
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
	if job.QueuedAt != nil && !job.QueuedAt.IsZero() {
		result.QueuedAt = job.QueuedAt.UTC().Format(time.RFC3339Nano)
	}
	if job.DeadlineAt != nil && !job.DeadlineAt.IsZero() {
		result.DeadlineAt = job.DeadlineAt.UTC().Format(time.RFC3339Nano)
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

// IsTerminalStatus reports whether a job status is final: no further state
// transition will happen, so readers can stop waiting.
func IsTerminalStatus(status JobStatus) bool {
	switch status {
	case StatusCompleted, StatusFailed, StatusTimedOut, StatusCancelled, StatusOrphaned,
		StatusInterrupted, StatusExpired, StatusAbandoned:
		return true
	default:
		return false
	}
}

func isTerminalStatus(status JobStatus) bool {
	return IsTerminalStatus(status)
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
