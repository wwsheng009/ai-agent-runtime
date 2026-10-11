package background

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/pty"
)

// Sentinel errors let callers (the tool broker, the REST layer) map a failed
// interactive call onto an actionable content result instead of a hard failure
// (2026-10-11).
var (
	// ErrNotInteractive marks a one-shot job addressed with write_stdin.
	ErrNotInteractive = errors.New("background job is not interactive")
	// ErrSessionNotAvailable marks a session whose handle is gone (closed,
	// exited past its grace, or never opened).
	ErrSessionNotAvailable = errors.New("interactive session is not available")
	// ErrSessionNotLocal marks a session owned by another runtime instance; the
	// stdin handle only exists in the owner, so it cannot be driven here.
	ErrSessionNotLocal = errors.New("interactive session is owned by another runtime instance")
	// ErrTTYUnsupported marks a resize on a session without a PTY.
	ErrTTYUnsupported = errors.New("PTY is not supported for this session")
)

// Interactive session yield bounds mirror Codex unified_exec
// (MIN_YIELD_TIME_MS / MAX_YIELD_TIME_MS / MIN_EMPTY_YIELD_TIME_MS). They bound
// how long one write_stdin call may wait for output before returning timeout.
const (
	TerminalYieldMinMs      = 250
	TerminalYieldMaxMs      = 30_000
	terminalYieldEmptyMinMs = 5_000
	terminalPollInterval    = 25 * time.Millisecond
	// terminalSessionGrace keeps a finished session's buffer readable for a
	// short window after exit so the caller can drain the final output before
	// the handle is reaped.
	terminalSessionGrace = 60 * time.Second
)

// interactiveSession is the in-memory handle for a long-lived process whose
// stdin stays open for write_stdin. It is intentionally not persisted: a
// restart cannot rebuild the stdin pipe, so recovery converges the job to
// orphaned instead (see Manager.recoverPersistedJobs).
type interactiveSession struct {
	job   *managedJob
	stdin io.WriteCloser
	// resizer is set only for PTY-backed sessions (2026-10-11).
	resizer terminalResizer

	mu        sync.Mutex
	offset    int64
	closed    bool
	exited    bool
	exitCode  *int
	closeOnce sync.Once
	doneCh    chan struct{}
}

// terminalResizer is implemented by a PTY handle that can change its size.
type terminalResizer interface {
	Resize(cols, rows int) error
}

func newInteractiveSession(job *managedJob, stdin io.WriteCloser) *interactiveSession {
	return &interactiveSession{
		job:    job,
		stdin:  stdin,
		doneCh: make(chan struct{}),
	}
}

func (s *interactiveSession) currentOffset() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.offset
}

func (s *interactiveSession) setOffset(next int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.offset = next
	s.mu.Unlock()
}

func (s *interactiveSession) hasExited() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exited
}

func (s *interactiveSession) markExited(code int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.exited {
		s.mu.Unlock()
		return
	}
	s.exited = true
	exitCode := code
	s.exitCode = &exitCode
	s.mu.Unlock()
	close(s.doneCh)
}

func (s *interactiveSession) exitCodePtr() *int {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exitCode == nil {
		return nil
	}
	code := *s.exitCode
	return &code
}

// closeStdin closes the write side once; subsequent writes are refused.
func (s *interactiveSession) closeStdin() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		stdin := s.stdin
		s.mu.Unlock()
		if stdin != nil {
			_ = stdin.Close()
		}
	})
}

func (s *interactiveSession) stdinClosed() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *interactiveSession) result(out string, offset, next int64, condition string, waited time.Duration) *TerminalReadResult {
	res := &TerminalReadResult{
		JobID:         s.job.info.ID,
		Output:        out,
		Offset:        offset,
		NextOffset:    next,
		WaitCondition: condition,
		WaitedMs:      waited.Milliseconds(),
	}
	s.job.mu.RLock()
	res.Status = string(s.job.info.Status)
	s.job.mu.RUnlock()
	if s.hasExited() {
		res.Exited = true
		res.ExitCode = s.exitCodePtr()
	}
	return res
}

// clampTerminalYield normalizes yield_ms: <=0 uses the empty/non-empty default,
// and out-of-range values clamp to the Codex bounds.
func clampTerminalYield(yieldMs int, empty bool) int {
	if yieldMs <= 0 {
		if empty {
			return terminalYieldEmptyMinMs
		}
		return TerminalYieldMinMs
	}
	if yieldMs < TerminalYieldMinMs {
		return TerminalYieldMinMs
	}
	if yieldMs > TerminalYieldMaxMs {
		return TerminalYieldMaxMs
	}
	return yieldMs
}

// OpenTerminal starts a long-lived interactive process and registers its stdin
// handle. Unlike SubmitShell it never enters the detached runner (which writes
// to a log file and cannot keep stdin open). The call returns only after the
// startup acceptance gate passes, so a follow-up write_stdin cannot race a
// process that has not reached running yet (2026-10-11).
func (m *Manager) OpenTerminal(ctx context.Context, sessionID string, args TerminalOpenArgs) (*Job, error) {
	if m == nil {
		return nil, fmt.Errorf("background manager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if existing := m.interactiveSessionBySession(sessionID); existing != nil && !existing.hasExited() {
		// Reuse an already-open live session for this session id (2026-10-11):
		// a second OpenTerminal returns the running session instead of starting
		// a second process.
		job := existing.job.snapshot()
		if job != nil {
			if job.Metadata == nil {
				job.Metadata = map[string]interface{}{}
			}
			job.Metadata["already_open"] = true
		}
		return job, nil
	}
	command := strings.TrimSpace(args.Command)
	if command == "" {
		return nil, fmt.Errorf("command is required")
	}

	m.mu.RLock()
	active := len(m.interactive)
	max := m.maxInteractive
	m.mu.RUnlock()
	if max > 0 && active >= max {
		return nil, fmt.Errorf("too many interactive sessions (%d); close one before opening another", max)
	}

	jobID := "job_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	now := time.Now().UTC()
	logPath := ""
	if m.logDir != "" {
		if err := os.MkdirAll(m.logDir, 0o755); err == nil {
			logPath = filepath.Join(m.logDir, jobID+".log")
			_ = os.WriteFile(logPath, []byte{}, 0o644)
		}
	}

	req := BackgroundTaskArgs{
		Command:     command,
		Cwd:         args.Cwd,
		TimeoutSec:  args.TimeoutSec,
		Interactive: true,
		TTY:         args.TTY,
		Cols:        args.Cols,
		Rows:        args.Rows,
	}
	req = sanitizeBackgroundTaskArgs(req)
	req.Cwd = resolveJobCwd(ctx, req.Cwd)

	jobCtx, cancel := context.WithCancel(context.Background())
	managed := &managedJob{
		ctx: jobCtx,
		info: Job{
			ID:          jobID,
			SessionID:   strings.TrimSpace(sessionID),
			Kind:        "terminal",
			Command:     command,
			Cwd:         strings.TrimSpace(req.Cwd),
			Status:      StatusPending,
			CreatedAt:   now,
			LogPath:     logPath,
			Interactive: true,
			TTY:         false,
			Cols:        args.Cols,
			Rows:        args.Rows,
			Metadata:    metadataFromRequest(req, m.config.DefaultTimeout),
		},
		request: req,
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
	m.jobs[jobID] = managed
	m.mu.Unlock()

	m.ensureInstanceRegistered(ctx)
	if m.store != nil {
		_ = m.store.SaveJob(ctx, managed.info)
	}
	m.appendJobEvent(ctx, jobID, "terminal_opened", map[string]interface{}{
		"status": StatusPending,
		"tty":    args.TTY,
	})

	usePTY := args.TTY && pty.Supported()
	if args.TTY && !usePTY {
		// A TTY was requested but the platform cannot host a PTY (e.g. Windows
		// without a validated ConPTY path): fall back to a pipe and record the
		// capability gap instead of pretending the session has a TTY.
		managed.mu.Lock()
		if managed.info.Metadata == nil {
			managed.info.Metadata = map[string]interface{}{}
		}
		managed.info.Metadata["tty_fallback"] = "pipe"
		managed.mu.Unlock()
	}
	if usePTY {
		return m.openTerminalPTY(ctx, managed, args)
	}

	cmd := buildShellCommand(jobCtx, command)
	if cmd == nil {
		m.failJobWithErrorCode(managed, runtimeerrors.ErrProcessStartFailed, fmt.Errorf("unsupported shell command"))
		return managed.snapshot(), nil
	}
	if managed.info.Cwd != "" {
		cmd.Dir = managed.info.Cwd
	}
	applyProcessGroup(cmd)

	var logFile *os.File
	if logPath != "" {
		if file, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			logFile = file
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		m.failJobWithErrorCode(managed, runtimeerrors.ErrProcessStartFailed, fmt.Errorf("open stdin pipe: %w", err))
		return managed.snapshot(), nil
	}
	// Wire output before Start: the existing writer mirrors to the log file,
	// fills the shared output buffer and emits write-driven "output" events.
	cmd.Stdout = m.newJobOutputWriter(jobCtx, managed, logFile, "stdout")
	cmd.Stderr = m.newJobOutputWriter(jobCtx, managed, logFile, "stderr")

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		if logFile != nil {
			_ = logFile.Close()
		}
		if jobCtx.Err() == context.Canceled {
			m.markCancelled(jobCtx, managed, "cancelled")
			return managed.snapshot(), nil
		}
		m.failJobWithErrorCode(managed, runtimeerrors.ErrProcessStartFailed, err)
		return managed.snapshot(), nil
	}

	// killOnClose=false: the session must survive the tool call that opened it.
	// With killOnClose=true the tree joins the per-call job object, which closes
	// as soon as the tool call returns — killing the session at end of turn
	// (live-session E2E bug: `more` died with cancel_source=parent_context the
	// moment the first turn ended, 2026-10-11). Same contract as the detached
	// runner path (detached.go: sessions must survive this runtime process).
	if tree, treeErr := attachProcessTree(cmd.Process.Pid, false); treeErr == nil {
		managed.mu.Lock()
		managed.tree = tree
		if managed.info.Metadata == nil {
			managed.info.Metadata = map[string]interface{}{}
		}
		managed.info.Metadata[backgroundMetaProcessGroup] = tree.GroupID()
		managed.mu.Unlock()
	}

	startedAt := time.Now().UTC()
	processAlive := func() bool { return processIsAlive(cmd.Process) }
	if !m.acceptStartedProcess(jobCtx, managed, startedAt, cmd.Process.Pid, processAlive) {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if logFile != nil {
			_ = logFile.Close()
		}
		return managed.snapshot(), nil
	}

	sess := newInteractiveSession(managed, stdin)
	m.mu.Lock()
	m.interactive[jobID] = sess
	if sid := strings.TrimSpace(sessionID); sid != "" {
		m.interactiveBySession[sid] = jobID
	}
	m.mu.Unlock()

	m.jobWG.Add(1)
	go func() {
		defer m.jobWG.Done()
		m.waitInteractive(jobCtx, managed, sess, cmd, logFile)
	}()

	return managed.snapshot(), nil
}

// waitInteractive blocks on the process, records the terminal state through the
// existing CAS path, and reaps the in-memory handle after a short grace so the
// caller can still drain the final output.
func (m *Manager) waitInteractive(ctx context.Context, managed *managedJob, sess *interactiveSession, cmd *exec.Cmd, logFile *os.File) {
	waitErr := cmd.Wait()
	if logFile != nil {
		_ = logFile.Close()
	}
	sess.closeStdin()

	code := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = -1
		}
	}
	sess.markExited(code)

	if ctx != nil && ctx.Err() == context.Canceled {
		m.markCancelled(context.Background(), managed, "cancelled")
	} else if ctx != nil && ctx.Err() == context.DeadlineExceeded {
		m.markTimedOut(managed, "command timed out")
	} else {
		m.completeJob(managed, code)
	}

	time.AfterFunc(terminalSessionGrace, func() {
		m.removeInteractive(managed.info.ID)
	})
}

// WriteStdin writes input to a running interactive session and returns the
// output produced since the previous exchange. The returned NextOffset is the
// single authoritative cursor the caller passes to task_output next.
func (m *Manager) WriteStdin(ctx context.Context, jobID, input string, yieldMs int) (*TerminalReadResult, error) {
	if m == nil {
		return nil, fmt.Errorf("background manager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, fmt.Errorf("job id is required")
	}

	sess := m.interactiveSession(jobID)
	if sess == nil {
		if job, _ := m.GetJob(ctx, jobID); job != nil {
			if !job.Interactive {
				return nil, fmt.Errorf("%w: %s; use task_output for one-shot jobs", ErrNotInteractive, jobID)
			}
			if owner := strings.TrimSpace(job.OwnerInstanceID); owner != "" && owner != m.instanceID {
				return nil, fmt.Errorf("%w: %s (owner=%s); cross-instance interaction is not supported", ErrSessionNotLocal, jobID, owner)
			}
		}
		return nil, fmt.Errorf("%w: %s (closed or exited)", ErrSessionNotAvailable, jobID)
	}

	if input != "" {
		if sess.stdinClosed() {
			return nil, fmt.Errorf("%w: %s stdin is closed", ErrSessionNotAvailable, jobID)
		}
		sess.mu.Lock()
		stdin := sess.stdin
		sess.mu.Unlock()
		if stdin == nil {
			return nil, fmt.Errorf("interactive session %s stdin is unavailable", jobID)
		}
		if _, err := stdin.Write([]byte(input)); err != nil {
			return nil, fmt.Errorf("write stdin: %w", err)
		}
	}

	started := time.Now()
	waitMs := clampTerminalYield(yieldMs, input == "")
	deadline := started.Add(time.Duration(waitMs) * time.Millisecond)
	finish := func(out string, offset, next int64, condition string) *TerminalReadResult {
		result := sess.result(out, offset, next, condition, time.Since(started))
		m.attachTerminalMetrics(result, sess)
		return result
	}
	for {
		cur := sess.currentOffset()
		out, next := sess.job.output.Read(cur, 0)
		if out != "" {
			sess.setOffset(next)
			return finish(out, cur, next, "output"), nil
		}
		if sess.hasExited() {
			sess.setOffset(next)
			return finish("", cur, next, "exit"), nil
		}
		if time.Now().After(deadline) {
			sess.setOffset(next)
			return finish("", cur, next, "timeout"), nil
		}
		select {
		case <-ctx.Done():
			sess.setOffset(next)
			return finish("", cur, next, "timeout"), nil
		case <-time.After(terminalPollInterval):
		}
	}
}

// CloseTerminal closes stdin and waits for a graceful exit; a process that
// ignores EOF is converged through the existing cancel path (CAS + tree kill).
func (m *Manager) CloseTerminal(ctx context.Context, jobID string) (*Job, error) {
	if m == nil {
		return nil, fmt.Errorf("background manager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, fmt.Errorf("job id is required")
	}
	sess := m.interactiveSession(jobID)
	if sess == nil {
		return m.GetJob(ctx, jobID)
	}
	sess.closeStdin()
	select {
	case <-sess.doneCh:
	case <-time.After(5 * time.Second):
		if _, err := m.CancelJob(ctx, jobID); err != nil {
			// The process may have exited between the check and the cancel;
			// fall through and report the current snapshot either way.
			_ = err
		}
		select {
		case <-sess.doneCh:
		case <-time.After(3 * time.Second):
		}
	}
	m.removeInteractive(jobID)
	return m.GetJob(ctx, jobID)
}

// CleanTerminals closes live interactive sessions and reports how many were
// closed. An empty sessionID cleans every session; a non-empty one cleans only
// that session's sessions (2026-10-11).
func (m *Manager) CleanTerminals(ctx context.Context, sessionID string) int {
	if m == nil {
		return 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID = strings.TrimSpace(sessionID)

	m.mu.RLock()
	targets := make([]string, 0, len(m.interactive))
	for jobID, sess := range m.interactive {
		if sess == nil {
			continue
		}
		if sessionID != "" {
			sess.job.mu.RLock()
			owner := strings.TrimSpace(sess.job.info.SessionID)
			sess.job.mu.RUnlock()
			if owner != sessionID {
				continue
			}
		}
		targets = append(targets, jobID)
	}
	m.mu.RUnlock()

	closed := 0
	for _, jobID := range targets {
		if _, err := m.CloseTerminal(ctx, jobID); err == nil {
			closed++
		}
	}
	return closed
}

// IsInteractive reports whether a job was opened as an interactive session.
func (m *Manager) IsInteractive(ctx context.Context, jobID string) bool {
	job, err := m.GetJob(ctx, jobID)
	return err == nil && job != nil && job.Interactive
}

func (m *Manager) interactiveSession(jobID string) *interactiveSession {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.interactive[strings.TrimSpace(jobID)]
}

func (m *Manager) removeInteractive(jobID string) {
	if m == nil {
		return
	}
	jobID = strings.TrimSpace(jobID)
	m.mu.Lock()
	delete(m.interactive, jobID)
	for sid, mapped := range m.interactiveBySession {
		if mapped == jobID {
			delete(m.interactiveBySession, sid)
		}
	}
	m.mu.Unlock()
}

// interactiveSessionBySession returns the live session registered for a session
// id, if any (2026-10-11).
func (m *Manager) interactiveSessionBySession(sessionID string) *interactiveSession {
	if m == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.interactive[m.interactiveBySession[sessionID]]
}

// Resize changes the terminal size of an interactive session. Without a PTY
// there is nothing to resize, so it reports ErrTTYUnsupported instead of
// silently ignoring the size (2026-10-11).
func (m *Manager) Resize(ctx context.Context, jobID string, cols, rows int) error {
	if m == nil {
		return fmt.Errorf("background manager is nil")
	}
	sess := m.interactiveSession(jobID)
	if sess == nil {
		return fmt.Errorf("%w: %s", ErrSessionNotAvailable, jobID)
	}
	if sess.resizer == nil {
		return ErrTTYUnsupported
	}
	return sess.resizer.Resize(cols, rows)
}

// openTerminalPTY starts an interactive session behind a pseudoterminal. It is
// only reached when pty.Supported() is true (2026-10-11).
func (m *Manager) openTerminalPTY(ctx context.Context, managed *managedJob, args TerminalOpenArgs) (*Job, error) {
	shell := runtimeexecutor.DefaultUserShell()
	argv := shell.DeriveExecArgs(managed.info.Command, false)
	if len(argv) == 0 {
		m.failJobWithErrorCode(managed, runtimeerrors.ErrProcessStartFailed, fmt.Errorf("unsupported shell command"))
		return managed.snapshot(), nil
	}
	var logFile *os.File
	if managed.logPath != "" {
		if file, err := os.OpenFile(managed.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			logFile = file
		}
	}
	options := make([]pty.Option, 0, 2)
	if managed.info.Cwd != "" {
		options = append(options, pty.WithWorkDir(managed.info.Cwd))
	}
	if args.Cols > 0 || args.Rows > 0 {
		options = append(options, pty.WithSize(args.Cols, args.Rows))
	}
	p, err := pty.Start(argv[0], argv[1:], options...)
	if err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		m.failJobWithErrorCode(managed, runtimeerrors.ErrProcessStartFailed, fmt.Errorf("start pty: %w", err))
		return managed.snapshot(), nil
	}

	// Feed PTY output through the same writer as the pipe path so the buffer,
	// log mirror and "output" events stay identical.
	writer := m.newJobOutputWriter(ctx, managed, logFile, "stdout")
	go func() {
		buf := make([]byte, 8192)
		for {
			n, readErr := p.Read(buf)
			if n > 0 {
				_, _ = writer.Write(buf[:n])
			}
			if readErr != nil {
				return
			}
		}
	}()

	// Same killOnClose=false contract as the pipe path above: PTY-backed
	// sessions must survive the opening tool call (2026-10-11 live-session fix).
	if tree, treeErr := attachProcessTree(p.Pid(), false); treeErr == nil {
		managed.mu.Lock()
		managed.tree = tree
		if managed.info.Metadata == nil {
			managed.info.Metadata = map[string]interface{}{}
		}
		managed.info.Metadata[backgroundMetaProcessGroup] = tree.GroupID()
		managed.mu.Unlock()
	}

	startedAt := time.Now().UTC()
	processAlive := func() bool { return inspectProcess(p.Pid()).Running }
	if !m.acceptStartedProcess(ctx, managed, startedAt, p.Pid(), processAlive) {
		_ = p.Close()
		if logFile != nil {
			_ = logFile.Close()
		}
		return managed.snapshot(), nil
	}

	managed.mu.Lock()
	managed.info.TTY = true
	managed.mu.Unlock()

	sess := newInteractiveSession(managed, p)
	sess.resizer = p
	m.mu.Lock()
	m.interactive[managed.info.ID] = sess
	if sid := strings.TrimSpace(managed.info.SessionID); sid != "" {
		m.interactiveBySession[sid] = managed.info.ID
	}
	m.mu.Unlock()

	m.jobWG.Add(1)
	go func() {
		defer m.jobWG.Done()
		m.waitInteractivePTY(ctx, managed, sess, p, logFile)
	}()

	return managed.snapshot(), nil
}

func (m *Manager) waitInteractivePTY(ctx context.Context, managed *managedJob, sess *interactiveSession, p *pty.PTY, logFile *os.File) {
	code, waitErr := p.Wait(ctx)
	_ = p.Close()
	if logFile != nil {
		_ = logFile.Close()
	}
	sess.closeStdin()
	if waitErr != nil {
		code = -1
	}
	sess.markExited(code)

	if ctx != nil && ctx.Err() == context.Canceled {
		m.markCancelled(context.Background(), managed, "cancelled")
	} else if ctx != nil && ctx.Err() == context.DeadlineExceeded {
		m.markTimedOut(managed, "command timed out")
	} else {
		m.completeJob(managed, code)
	}

	time.AfterFunc(terminalSessionGrace, func() {
		m.removeInteractive(managed.info.ID)
	})
}
