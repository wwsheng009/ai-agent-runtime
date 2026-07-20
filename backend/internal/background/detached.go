package background

import (
	"context"
	stderrors "errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

const (
	backgroundMetaPID             = "pid"
	backgroundMetaStatusPath      = "status_path"
	backgroundMetaRunnerPath      = "runner_path"
	backgroundMetaHeartbeatPath   = "heartbeat_path"
	backgroundMetaProcessIdentity = "process_identity"
	backgroundMetaWatchdogState   = "watchdog_state"
	backgroundMetaWatchdogCode    = "watchdog_error_code"
	backgroundMetaRecoveryAttempt = "recovery_attempt"
	backgroundMetaRecoveryMax     = "recovery_max_attempts"
	backgroundMetaNextRecoveryAt  = "next_recovery_at"

	watchdogStateStalled   = "stalled"
	watchdogStateZombie    = "zombie"
	watchdogStatePIDReused = "pid_reused"
	watchdogStateMissing   = "missing"

	watchdogCodeProcessStalled = "PROCESS_STALLED"
	watchdogCodeProcessZombie  = "PROCESS_ZOMBIE"
	watchdogCodePIDReused      = "PID_REUSED"
	watchdogCodeProcessLost    = "PROCESS_LOST"
)

var detachedShellLauncher = launchDetachedShell

type detachedLaunchError struct {
	err       error
	retryable bool
}

func (e *detachedLaunchError) Error() string {
	if e == nil || e.err == nil {
		return "detached launch failed"
	}
	return e.err.Error()
}

func (e *detachedLaunchError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (m *Manager) canUseDetachedExecution(managed *managedJob) bool {
	if m == nil || managed == nil {
		return false
	}
	return strings.TrimSpace(managed.logPath) != ""
}

func (m *Manager) runDetachedJob(managed *managedJob) {
	if managed == nil {
		return
	}
	ctx := managed.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	req := managed.request
	startedAt := time.Now().UTC()

	launch, err := m.launchDetachedShellWithRetry(ctx, managed)
	if err != nil {
		if ctx.Err() != nil {
			m.markCancelled(context.Background(), managed, ctx.Err().Error())
			return
		}
		if m.scheduleDetachedRecovery(managed, "launch_failed", err.Error()) {
			return
		}
		m.failJob(managed, err)
		return
	}

	managed.mu.Lock()
	if managed.info.Metadata == nil {
		managed.info.Metadata = map[string]interface{}{}
	}
	managed.info.Metadata[backgroundMetaPID] = launch.PID
	managed.info.Metadata[backgroundMetaStatusPath] = launch.StatusPath
	managed.info.Metadata[backgroundMetaRunnerPath] = launch.RunnerPath
	managed.info.Metadata[backgroundMetaHeartbeatPath] = launch.HeartbeatPath
	delete(managed.info.Metadata, backgroundMetaWatchdogState)
	delete(managed.info.Metadata, backgroundMetaWatchdogCode)
	delete(managed.info.Metadata, backgroundMetaNextRecoveryAt)
	delete(managed.info.Metadata, "watchdog_detected_at")
	if health := inspectProcess(launch.PID); health.Identity != "" {
		managed.info.Metadata[backgroundMetaProcessIdentity] = health.Identity
	}
	managed.info.Status = StatusRunning
	managed.info.StartedAt = &startedAt
	managed.info.Message = ""
	managed.info.ExitCode = nil
	managed.info.FinishedAt = nil
	managed.scheduled = false
	managed.mu.Unlock()

	if m.store != nil {
		_ = m.store.UpdateJob(context.Background(), managed.info)
	}
	m.appendJobEvent(context.Background(), managed.info.ID, "running", map[string]interface{}{
		"status": managed.info.Status,
		"pid":    launch.PID,
	})

	var deadline time.Time
	timeout := time.Duration(req.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = m.config.DefaultTimeout
	}
	if timeout > 0 {
		deadline = startedAt.Add(timeout)
	}
	m.monitorDetachedJob(ctx, managed, launch.PID, launch.StatusPath, deadline)
}

func (m *Manager) recoverDetachedRunningJob(job Job) bool {
	pid, ok := detachedPID(job.Metadata)
	if !ok {
		return false
	}
	statusPath, ok := stringMetadataValue(job.Metadata, backgroundMetaStatusPath)
	if !ok {
		return false
	}

	managed := m.managedJobFromStored(job)
	if managed == nil {
		return true
	}

	if exitCode, ok := readDetachedExitCode(statusPath); ok {
		m.mu.Lock()
		if _, exists := m.jobs[job.ID]; !exists {
			m.jobs[job.ID] = managed
		}
		m.mu.Unlock()
		m.emitLogOutput(context.Background(), managed)
		if exitCode == 0 {
			m.completeJob(managed, exitCode)
		} else {
			m.failJobWithCode(managed, exitCode, fmt.Sprintf("command exited with code %d", exitCode))
		}
		return true
	}

	health := inspectProcess(pid)
	if !detachedProcessMatches(job.Metadata, health) {
		state := watchdogStateMissing
		if health.Zombie {
			state = watchdogStateZombie
		} else if health.Running {
			state = watchdogStatePIDReused
		}
		setDetachedWatchdogMetadata(job.Metadata, state, time.Now().UTC())
		return false
	}

	m.mu.Lock()
	if _, exists := m.jobs[job.ID]; !exists {
		m.jobs[job.ID] = managed
	}
	m.mu.Unlock()
	m.appendJobEvent(context.Background(), job.ID, "running", map[string]interface{}{
		"status":    StatusRunning,
		"pid":       pid,
		"recovered": true,
	})

	var deadline time.Time
	timeout := time.Duration(managed.request.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = m.config.DefaultTimeout
	}
	if timeout > 0 && job.StartedAt != nil && !job.StartedAt.IsZero() {
		deadline = job.StartedAt.Add(timeout)
	}

	m.jobWG.Add(1)
	go func() {
		defer m.jobWG.Done()
		m.monitorDetachedJob(managed.ctx, managed, pid, statusPath, deadline)
	}()
	return true
}

func (m *Manager) monitorDetachedJob(ctx context.Context, managed *managedJob, pid int, statusPath string, deadline time.Time) {
	if managed == nil {
		return
	}
	ticker := time.NewTicker(m.config.MonitorInterval)
	defer ticker.Stop()

	missingStatusSince := time.Time{}
	startedMonitoringAt := time.Now()
	heartbeatMonitor := newDetachedHeartbeatMonitor(startedMonitoringAt)
	for {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
		}

		m.emitLogOutput(context.Background(), managed)

		if !deadline.IsZero() && time.Now().After(deadline) {
			_ = terminateProcess(pid)
			m.markTimedOut(managed, "command timed out")
			return
		}
		if exitCode, ok := readDetachedExitCode(statusPath); ok {
			m.emitLogOutput(context.Background(), managed)
			if exitCode == 0 {
				m.completeJob(managed, exitCode)
			} else {
				m.failJobWithCode(managed, exitCode, fmt.Sprintf("command exited with code %d", exitCode))
			}
			return
		}
		health := inspectProcess(pid)
		if health.Zombie {
			m.markDetachedWatchdogFailure(managed, watchdogStateZombie, "background process became a zombie before recording an exit status")
			return
		}
		if !managedDetachedProcessMatches(managed, health) {
			if missingStatusSince.IsZero() {
				missingStatusSince = time.Now().UTC()
			} else if time.Since(missingStatusSince) >= 500*time.Millisecond {
				state := watchdogStateMissing
				message := "background process exited before recording an exit status"
				if health.Running {
					state = watchdogStatePIDReused
					message = "background process PID was reused before an exit status was recorded"
				}
				m.markDetachedWatchdogFailure(managed, state, message)
				return
			}
		} else {
			missingStatusSince = time.Time{}
			if m.detachedHeartbeatStalledWithMonitor(managed, heartbeatMonitor, time.Now()) {
				_ = terminateProcess(pid)
				m.markDetachedWatchdogFailure(managed, watchdogStateStalled, "background process heartbeat stalled")
				return
			}
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

func (m *Manager) launchDetachedShellWithRetry(ctx context.Context, managed *managedJob) (*detachedLaunch, error) {
	if managed == nil {
		return nil, fmt.Errorf("background job is nil")
	}
	maxAttempts := m.config.LaunchMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	var lastErr error
	attempts := 0
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		attempts = attempt
		managed.mu.Lock()
		if managed.info.Metadata == nil {
			managed.info.Metadata = map[string]interface{}{}
		}
		managed.info.Metadata["launch_attempt"] = attempt
		managed.info.Metadata["launch_max_attempts"] = maxAttempts
		managed.mu.Unlock()
		launch, err := detachedShellLauncher(managed.info.Command, managed.info.Cwd, managed.logPath)
		if err == nil {
			return launch, nil
		}
		lastErr = err
		if attempt >= maxAttempts || !detachedLaunchRetryable(err) {
			break
		}
		m.appendJobEvent(context.Background(), managed.info.ID, "launch_retry", map[string]interface{}{
			"attempt":      attempt,
			"next_attempt": attempt + 1,
			"max_attempts": maxAttempts,
			"error":        err.Error(),
		})
		delay := time.Duration(attempt) * m.config.RetryBackoff
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("launch detached command after %d attempt(s): %w", attempts, lastErr)
}

func detachedLaunchRetryable(err error) bool {
	if err == nil {
		return false
	}
	var launchErr *detachedLaunchError
	if stderrors.As(err, &launchErr) {
		return launchErr.retryable
	}
	// Injected/custom launchers return ordinary errors and are assumed to fail
	// before process creation. The built-in launcher always classifies errors.
	return true
}

func detachedProcessMatches(metadata map[string]interface{}, health processHealth) bool {
	if !health.Running || health.Zombie {
		return false
	}
	expected, _ := stringMetadataValue(metadata, backgroundMetaProcessIdentity)
	return expected == "" || health.Identity == "" || expected == health.Identity
}

func managedDetachedProcessMatches(managed *managedJob, health processHealth) bool {
	if managed == nil {
		return false
	}
	managed.mu.RLock()
	expected, _ := stringMetadataValue(managed.info.Metadata, backgroundMetaProcessIdentity)
	managed.mu.RUnlock()
	return health.Running && !health.Zombie && (expected == "" || health.Identity == "" || expected == health.Identity)
}

type detachedHeartbeatMonitor struct {
	lastValue   string
	lastAdvance time.Time
}

func newDetachedHeartbeatMonitor(startedAt time.Time) *detachedHeartbeatMonitor {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return &detachedHeartbeatMonitor{lastAdvance: startedAt}
}

func (m *Manager) detachedHeartbeatStalledWithMonitor(managed *managedJob, monitor *detachedHeartbeatMonitor, now time.Time) bool {
	if m == nil || managed == nil || monitor == nil || m.config.HeartbeatTimeout <= 0 {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	managed.mu.RLock()
	heartbeatPath, _ := stringMetadataValue(managed.info.Metadata, backgroundMetaHeartbeatPath)
	managed.mu.RUnlock()
	if heartbeatPath == "" {
		return false
	}
	if data, err := os.ReadFile(heartbeatPath); err == nil {
		value := strings.TrimSpace(string(data))
		if value != "" && value != monitor.lastValue {
			monitor.lastValue = value
			monitor.lastAdvance = now
		}
	}
	return now.Sub(monitor.lastAdvance) > m.config.HeartbeatTimeout
}

func (m *Manager) markDetachedWatchdogFailure(managed *managedJob, state, message string) {
	if managed == nil {
		return
	}
	managed.mu.Lock()
	if managed.info.Metadata == nil {
		managed.info.Metadata = map[string]interface{}{}
	}
	setDetachedWatchdogMetadata(managed.info.Metadata, state, time.Now().UTC())
	jobID := managed.info.ID
	code, _ := stringMetadataValue(managed.info.Metadata, backgroundMetaWatchdogCode)
	managed.mu.Unlock()
	m.appendJobEvent(context.Background(), jobID, "watchdog_detected", map[string]interface{}{
		"state":      state,
		"error_code": code,
		"error":      strings.TrimSpace(message),
	})
	if m.scheduleDetachedRecovery(managed, state, message) {
		return
	}
	if state == watchdogStateStalled {
		m.markTimedOut(managed, message)
		return
	}
	m.orphanJob(managed, message)
}

func (m *Manager) scheduleDetachedRecovery(managed *managedJob, reason, message string) bool {
	if m == nil || managed == nil || normalizeRestartPolicy(managed.request.RestartPolicy) != RestartPolicyRerun {
		return false
	}

	managed.mu.Lock()
	if managed.info.Status == StatusCancelled {
		managed.scheduled = false
		managed.mu.Unlock()
		return true
	}
	if managed.info.Metadata == nil {
		managed.info.Metadata = map[string]interface{}{}
	}
	previousAttempts, _ := intMetadataValue(managed.info.Metadata, backgroundMetaRecoveryAttempt)
	attempt := previousAttempts + 1
	maxAttempts := m.config.RecoveryMaxAttempts
	if maxAttempts > 0 && attempt > maxAttempts {
		managed.mu.Unlock()
		return false
	}
	delay := backgroundRecoveryDelay(m.config.RecoveryBackoffSchedule, attempt)
	nextRecoveryAt := time.Now().UTC().Add(delay)
	managed.info.Metadata[backgroundMetaRecoveryAttempt] = attempt
	managed.info.Metadata[backgroundMetaRecoveryMax] = maxAttempts
	managed.info.Metadata[backgroundMetaNextRecoveryAt] = nextRecoveryAt.Format(time.RFC3339Nano)
	managed.info.Metadata["recovery_reason"] = strings.TrimSpace(reason)
	delete(managed.info.Metadata, backgroundMetaPID)
	delete(managed.info.Metadata, backgroundMetaProcessIdentity)
	managed.info.Message = fmt.Sprintf("automatic recovery %d scheduled in %s: %s", attempt, delay, strings.TrimSpace(message))
	managed.scheduled = true
	snapshot := managed.info
	managed.mu.Unlock()

	if m.store != nil {
		_ = m.store.UpdateJob(context.Background(), snapshot)
	}
	m.appendJobEvent(context.Background(), snapshot.ID, "recovery_scheduled", map[string]interface{}{
		"attempt":          attempt,
		"max_attempts":     maxAttempts,
		"delay_ms":         delay.Milliseconds(),
		"next_recovery_at": nextRecoveryAt.Format(time.RFC3339Nano),
		"reason":           strings.TrimSpace(reason),
	})
	return m.waitAndQueueDetachedRecovery(managed, attempt, reason, delay)
}

func (m *Manager) waitAndQueueDetachedRecovery(managed *managedJob, attempt int, reason string, delay time.Duration) bool {
	if m == nil || managed == nil {
		return false
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	ctx := managed.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-timer.C:
	case <-ctx.Done():
		return true
	case <-m.stopCh:
		return true
	}

	managed.mu.Lock()
	if managed.info.Status == StatusCancelled {
		managed.scheduled = false
		managed.mu.Unlock()
		return true
	}
	managed.info.Status = StatusPending
	managed.info.StartedAt = nil
	managed.info.FinishedAt = nil
	managed.info.ExitCode = nil
	managed.info.Message = fmt.Sprintf("automatic recovery %d queued", attempt)
	managed.scheduled = false
	delete(managed.info.Metadata, backgroundMetaPID)
	delete(managed.info.Metadata, backgroundMetaProcessIdentity)
	delete(managed.info.Metadata, backgroundMetaNextRecoveryAt)
	snapshot := managed.info
	managed.mu.Unlock()

	if m.store != nil {
		_ = m.store.UpdateJob(context.Background(), snapshot)
	}
	m.appendJobEvent(context.Background(), snapshot.ID, "recovery_queued", map[string]interface{}{
		"attempt": attempt,
		"reason":  strings.TrimSpace(reason),
	})
	m.notifyDispatcher()
	return true
}

func (m *Manager) resumeDetachedRecovery(managed *managedJob, reason string) bool {
	if m == nil || managed == nil {
		return false
	}
	managed.mu.Lock()
	attempt, attemptOK := intMetadataValue(managed.info.Metadata, backgroundMetaRecoveryAttempt)
	nextText, nextOK := stringMetadataValue(managed.info.Metadata, backgroundMetaNextRecoveryAt)
	nextRecoveryAt, parseErr := time.Parse(time.RFC3339Nano, nextText)
	if !attemptOK || attempt < 1 || !nextOK || parseErr != nil {
		managed.mu.Unlock()
		return false
	}
	delay := time.Until(nextRecoveryAt)
	if delay < 0 {
		delay = 0
	}
	managed.scheduled = true
	managed.info.Message = fmt.Sprintf("automatic recovery %d resumed; queued in %s", attempt, delay)
	snapshot := managed.info
	managed.mu.Unlock()

	if m.store != nil {
		_ = m.store.UpdateJob(context.Background(), snapshot)
	}
	m.appendJobEvent(context.Background(), snapshot.ID, "recovery_resumed", map[string]interface{}{
		"attempt":          attempt,
		"delay_ms":         delay.Milliseconds(),
		"next_recovery_at": nextRecoveryAt.Format(time.RFC3339Nano),
		"reason":           strings.TrimSpace(reason),
	})
	return m.waitAndQueueDetachedRecovery(managed, attempt, reason, delay)
}

func setDetachedWatchdogMetadata(metadata map[string]interface{}, state string, detectedAt time.Time) {
	if metadata == nil {
		return
	}
	metadata[backgroundMetaWatchdogState] = state
	metadata[backgroundMetaWatchdogCode] = detachedWatchdogErrorCode(state)
	metadata["watchdog_detected_at"] = detectedAt.Format(time.RFC3339Nano)
}

func detachedWatchdogErrorCode(state string) string {
	switch state {
	case watchdogStateStalled:
		return watchdogCodeProcessStalled
	case watchdogStateZombie:
		return watchdogCodeProcessZombie
	case watchdogStatePIDReused:
		return watchdogCodePIDReused
	default:
		return watchdogCodeProcessLost
	}
}

func (m *Manager) emitLogOutput(ctx context.Context, managed *managedJob) {
	if m == nil || managed == nil || strings.TrimSpace(managed.logPath) == "" {
		return
	}
	file, err := os.Open(managed.logPath)
	if err != nil {
		return
	}
	defer file.Close()

	managed.outputMu.Lock()
	defer managed.outputMu.Unlock()

	if _, err := file.Seek(managed.outputOffset, io.SeekStart); err != nil {
		return
	}

	buf := make([]byte, defaultOutputEventChunkBytes)
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			start := managed.outputOffset
			next := start + int64(n)
			_, _ = managed.output.Write(chunk)
			managed.outputOffset = next
			m.appendJobEvent(ctx, managed.info.ID, "output", map[string]interface{}{
				"offset":      start,
				"next_offset": next,
				"size":        len(chunk),
				"stream":      "combined",
				"chunk":       string(chunk),
			})
		}
		if readErr != nil {
			break
		}
	}
}

type detachedLaunch struct {
	PID           int
	StatusPath    string
	RunnerPath    string
	HeartbeatPath string
}

func launchDetachedShell(command, cwd, logPath string) (*detachedLaunch, error) {
	logPath = strings.TrimSpace(logPath)
	command = strings.TrimSpace(command)
	if logPath == "" || command == "" {
		return nil, &detachedLaunchError{err: fmt.Errorf("detached launch requires command and log path")}
	}
	statusPath := strings.TrimSuffix(logPath, filepath.Ext(logPath)) + ".status"
	heartbeatPath := statusPath + ".heartbeat"
	runnerPath := strings.TrimSuffix(logPath, filepath.Ext(logPath))
	shell := runtimeexecutor.DefaultUserShell()
	if runtime.GOOS == "windows" {
		runnerPath += ".ps1"
	} else {
		runnerPath += ".sh"
	}

	if err := os.Remove(statusPath); err != nil && !os.IsNotExist(err) {
		return nil, &detachedLaunchError{err: err, retryable: true}
	}
	if err := os.Remove(heartbeatPath); err != nil && !os.IsNotExist(err) {
		return nil, &detachedLaunchError{err: err, retryable: true}
	}
	if err := writeDetachedRunner(runnerPath, shell, command, cwd, logPath, statusPath); err != nil {
		return nil, &detachedLaunchError{err: err, retryable: true}
	}
	pid, err := startDetachedRunner(runnerPath)
	if err != nil {
		return nil, &detachedLaunchError{err: err, retryable: detachedRunnerStartRetryable(err)}
	}
	return &detachedLaunch{
		PID:           pid,
		StatusPath:    statusPath,
		RunnerPath:    runnerPath,
		HeartbeatPath: heartbeatPath,
	}, nil
}

func detachedRunnerStartRetryable(err error) bool {
	if err == nil {
		return false
	}
	var exitErr *exec.ExitError
	if stderrors.As(err, &exitErr) {
		return false
	}
	var parseErr *strconv.NumError
	return !stderrors.As(err, &parseErr)
}

func writeDetachedRunner(path string, shell runtimeexecutor.Shell, command, cwd, logPath, statusPath string) error {
	if runtime.GOOS == "windows" {
		return writeWindowsDetachedRunner(path, shell, command, cwd, logPath, statusPath)
	}
	return writeUnixDetachedRunner(path, shell, command, cwd, logPath, statusPath)
}

func writeUnixDetachedRunner(path string, shell runtimeexecutor.Shell, command, cwd, logPath, statusPath string) error {
	heartbeatPath := statusPath + ".heartbeat"
	lines := []string{
		"#!/bin/sh",
		"set +e",
		fmt.Sprintf("heartbeat_path=%s", shellQuote(heartbeatPath)),
		"printf '%s' \"$(date +%s)\" > \"$heartbeat_path\"",
		"(while kill -0 $$ 2>/dev/null; do printf '%s' \"$(date +%s)\" > \"$heartbeat_path\"; sleep 1; done) &",
		"heartbeat_pid=$!",
		"trap 'kill \"$heartbeat_pid\" 2>/dev/null || true' EXIT",
	}
	if strings.TrimSpace(cwd) != "" {
		lines = append(lines,
			fmt.Sprintf("if ! cd %s; then", shellQuote(cwd)),
			fmt.Sprintf("  printf 'failed to change directory: %%s\\n' %s >> %s 2>&1", shellQuote(cwd), shellQuote(logPath)),
			fmt.Sprintf("  printf \"%%s\" \"1\" > %s", shellQuote(statusPath)),
			"  exit 0",
			"fi",
		)
	}
	commandLine := buildShellCommandLine(shell.DeriveExecArgs(command, false))
	lines = append(lines,
		fmt.Sprintf("%s >> %s 2>&1", commandLine, shellQuote(logPath)),
		`code=$?`,
		"printf '%s' \"$(date +%s)\" > \"$heartbeat_path\"",
		fmt.Sprintf("printf \"%%s\" \"$code\" > %s", shellQuote(statusPath)),
	)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o755); err != nil {
		return err
	}
	return os.Chmod(path, 0o755)
}

func writeWindowsDetachedRunner(path string, shell runtimeexecutor.Shell, command, cwd, logPath, statusPath string) error {
	content := buildWindowsDetachedRunnerContent(shell, command, cwd, logPath, statusPath)
	return os.WriteFile(path, []byte(content), 0o644)
}

func startDetachedRunner(path string) (int, error) {
	if runtime.GOOS == "windows" {
		launcher := windowsPowerShellHost()
		script := fmt.Sprintf("$p = Start-Process -FilePath '%s' -ArgumentList @('-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', '%s') -WindowStyle Hidden -PassThru; [Console]::Out.Write($p.Id)", escapePowerShellSingleQuotes(launcher), escapePowerShellSingleQuotes(path))
		out, err := exec.Command(launcher, "-NoProfile", "-NonInteractive", "-Command", script).Output()
		if err != nil {
			return 0, err
		}
		return strconv.Atoi(strings.TrimSpace(string(out)))
	}
	script := fmt.Sprintf("if command -v setsid >/dev/null 2>&1; then setsid /bin/sh %s >/dev/null 2>&1 < /dev/null & echo $!; else nohup /bin/sh %s >/dev/null 2>&1 < /dev/null & echo $!; fi", shellQuote(path), shellQuote(path))
	out, err := exec.Command("/bin/sh", "-c", script).Output()
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

func readDetachedExitCode(path string) (int, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return 0, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	return code, true
}

func detachedPID(metadata map[string]interface{}) (int, bool) {
	return intMetadataValue(metadata, backgroundMetaPID)
}

func terminateProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	if runtime.GOOS == "windows" {
		return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
	}
	script := fmt.Sprintf("(kill -TERM -%d 2>/dev/null || kill -TERM %d 2>/dev/null || true); sleep 0.2; (kill -KILL -%d 2>/dev/null || kill -KILL %d 2>/dev/null || true)", pid, pid, pid, pid)
	return exec.Command("/bin/sh", "-c", script).Run()
}

func shellQuote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}

func escapeBatchPath(text string) string {
	return strings.ReplaceAll(text, `"`, `""`)
}

func escapePowerShellSingleQuotes(text string) string {
	return strings.ReplaceAll(text, `'`, `''`)
}

func buildShellCommandLine(args []string) string {
	if len(args) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func buildWindowsDetachedRunnerContent(shell runtimeexecutor.Shell, command, cwd, logPath, statusPath string) string {
	heartbeatPath := statusPath + ".heartbeat"
	commandArgs := shell.DeriveExecArgs(command, false)
	quotedArgs := make([]string, 0, len(commandArgs)-1)
	for _, arg := range commandArgs[1:] {
		quotedArgs = append(quotedArgs, powershellQuote(arg))
	}

	lines := []string{"$ErrorActionPreference = 'Stop'"}
	lines = append(lines, fmt.Sprintf("$shellPath = %s", powershellQuote(shell.Path)))
	lines = append(lines, fmt.Sprintf("$shellArgs = @(%s)", strings.Join(quotedArgs, ", ")))
	lines = append(lines,
		`$systemRoot = [Environment]::GetEnvironmentVariable('SystemRoot')`,
		`$env:PATH = "$systemRoot\System32\WindowsPowerShell\v1.0;$systemRoot\System32;$systemRoot;$env:PATH"`,
		fmt.Sprintf("$heartbeatPath = %s", powershellQuote(heartbeatPath)),
		"[System.IO.File]::WriteAllText($heartbeatPath, [DateTimeOffset]::UtcNow.Ticks.ToString())",
		fmt.Sprintf("$writer = [System.IO.StreamWriter]::new(%s, $true, (New-Object System.Text.UTF8Encoding $false))", powershellQuote(logPath)),
		"$scriptExitCode = 0",
		"$ownerPid = $PID",
		"$heartbeatJob = Start-Job -ArgumentList $heartbeatPath, $ownerPid -ScriptBlock {",
		"  param($path, $parentPid)",
		"  while ($null -ne (Get-Process -Id $parentPid -ErrorAction SilentlyContinue)) {",
		"    try { [System.IO.File]::WriteAllText([string]$path, [DateTimeOffset]::UtcNow.Ticks.ToString()) } catch {}",
		"    Start-Sleep -Seconds 1",
		"  }",
		"}",
		"try {",
	)
	if strings.TrimSpace(cwd) != "" {
		lines = append(lines, fmt.Sprintf("  Set-Location -LiteralPath %s", powershellQuote(cwd)))
	}
	lines = append(lines,
		"  & $shellPath @shellArgs 2>&1 | ForEach-Object { $writer.WriteLine($_) }",
		"  if ($null -ne $LASTEXITCODE) { $scriptExitCode = $LASTEXITCODE }",
		"} catch {",
		"  $writer.WriteLine($_.ToString())",
		"  $scriptExitCode = 1",
		"} finally {",
		"  Stop-Job -Job $heartbeatJob -ErrorAction SilentlyContinue",
		"  Remove-Job -Job $heartbeatJob -Force -ErrorAction SilentlyContinue",
		"  [System.IO.File]::WriteAllText($heartbeatPath, [DateTimeOffset]::UtcNow.Ticks.ToString())",
		"  $writer.Dispose()",
		"}",
		fmt.Sprintf("[System.IO.File]::WriteAllText(%s, [string]$scriptExitCode, (New-Object System.Text.UTF8Encoding $false))", powershellQuote(statusPath)),
		"exit 0",
	)
	return strings.Join(lines, "\r\n") + "\r\n"
}

func powershellQuote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", "''") + "'"
}

func windowsPowerShellHost() string {
	if path, err := exec.LookPath("pwsh"); err == nil {
		return path
	}
	if path, err := exec.LookPath("powershell"); err == nil {
		return path
	}
	return "powershell"
}
