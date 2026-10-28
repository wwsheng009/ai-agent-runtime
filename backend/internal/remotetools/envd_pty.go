package remotetools

// envd_pty.go adds the interactive terminal surface to the envd backend.
//
// A pty session outlives the tool call that created it, which is why the session
// owns a cancellable background context and a pump goroutine rather than the
// request context: the stream must survive the MCP call returning, and it must
// die with the server. Server.Close therefore tears the registry down.
//
// Two boundaries carried over from the bridge, both deliberate:
//   - pty_start is gated by the shared CommandPolicy, but nothing inside the
//     session is — once an interactive shell is running, pty_write is not
//     filtered line by line. The description says so instead of implying a
//     guarantee the backend cannot keep.
//   - the output buffer is tail-bounded, so a chatty session (yes, top) cannot
//     grow the process's memory without limit.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// PTY session limits.
const (
	envdPtyBufferLimit  = 256 << 10
	envdPtyDefaultCols  = 80
	envdPtyDefaultRows  = 24
	envdPtyDefaultWait  = time.Second
	envdPtyMaxWait      = 30 * time.Second
	envdPtyMaxDimension = 1000
)

// Tool names that exist only on the envd backend.
const (
	ToolPtyStart  = "pty_start"
	ToolPtyWrite  = "pty_write"
	ToolPtyRead   = "pty_read"
	ToolPtyResize = "pty_resize"
	ToolPtyClose  = "pty_close"
)

// envdPtySession is one interactive process under a pty.
type envdPtySession struct {
	pid    uint32
	stream *envdclient.StreamReader
	cancel context.CancelFunc

	mu      sync.Mutex
	data    []byte
	dropped int
	done    bool
	exit    int32
	status  string
	notify  chan struct{}
}

func newEnvdPtySession(pid uint32, stream *envdclient.StreamReader, cancel context.CancelFunc) *envdPtySession {
	return &envdPtySession{
		pid:    pid,
		stream: stream,
		cancel: cancel,
		notify: make(chan struct{}, 1),
	}
}

func (s *envdPtySession) signal() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *envdPtySession) append(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	s.mu.Lock()
	s.data = append(s.data, chunk...)
	if len(s.data) > envdPtyBufferLimit {
		drop := len(s.data) - envdPtyBufferLimit
		// Copy the tail so the buffer does not retain the trimmed prefix.
		s.data = append([]byte(nil), s.data[drop:]...)
		s.dropped += drop
	}
	s.mu.Unlock()
	s.signal()
}

func (s *envdPtySession) finish(exit int32, status string) {
	s.mu.Lock()
	s.done = true
	s.exit = exit
	s.status = status
	s.mu.Unlock()
	s.signal()
}

// pump drains the stream into the bounded buffer until the process ends.
func (s *envdPtySession) pump() {
	defer func() { _ = s.stream.Close() }()
	for {
		var event envdclient.ProcessEvent
		if err := s.stream.Recv(&event); err != nil {
			s.finish(-1, "stream closed: "+err.Error())
			return
		}
		switch event.Kind {
		case envdclient.EventData:
			s.append(event.Pty)
			s.append(event.Stdout)
			s.append(event.Stderr)
		case envdclient.EventEnd:
			if event.End == nil {
				s.finish(-1, "ended without a status")
				return
			}
			s.finish(event.End.ExitStatus(), event.End.Status)
			return
		}
	}
}

// waitRead waits up to wait for output, then drains the buffer.
func (s *envdPtySession) waitRead(wait time.Duration) ([]byte, int, bool, int32, string) {
	deadline := time.Now().Add(wait)
	for {
		s.mu.Lock()
		if len(s.data) > 0 || s.done {
			data := append([]byte(nil), s.data...)
			s.data = nil
			dropped := s.dropped
			s.dropped = 0
			done, exit, status := s.done, s.exit, s.status
			s.mu.Unlock()
			return data, dropped, done, exit, status
		}
		s.mu.Unlock()

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, 0, false, 0, ""
		}
		timer := time.NewTimer(remaining)
		select {
		case <-s.notify:
		case <-timer.C:
		}
		timer.Stop()
	}
}

func (s *envdPtySession) close() {
	s.cancel()
	_ = s.stream.Close()
}

// findSession looks one session up by pid.
func (be *envdBackend) findSession(pid uint32) (*envdPtySession, error) {
	be.sessionMu.Lock()
	defer be.sessionMu.Unlock()
	session := be.sessions[pid]
	if session == nil {
		return nil, fmt.Errorf("no pty session with pid %d", pid)
	}
	return session, nil
}

// closeAllSessions tears every session down; Server.Close calls it so an exiting
// server does not leave an interactive process running on the target.
func (be *envdBackend) closeAllSessions() {
	if be == nil {
		return
	}
	be.sessionMu.Lock()
	sessions := make([]*envdPtySession, 0, len(be.sessions))
	for _, session := range be.sessions {
		sessions = append(sessions, session)
	}
	be.sessions = map[uint32]*envdPtySession{}
	be.sessionMu.Unlock()
	for _, session := range sessions {
		session.close()
	}
}

// envdPtyToolSpecs publishes the interactive surface (ops tier only).
func envdPtyToolSpecs(base remoteToolBase) []envdToolSpec {
	intProp := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "integer", "description": desc}
	}
	pidProp := intProp("Session pid from pty_start.")
	return []envdToolSpec{
		{
			name:        ToolPtyStart,
			description: base.describe("Start an interactive process under a pty (ops only). The command runs via /bin/sh -c and passes the shared command policy, but input written later is not filtered line by line. Returns a pid for pty_write/pty_read/pty_resize/pty_close."),
			version:     "1.0.0",
			parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": envdStringProp("Command to run under the pty."),
					"cwd":     envdStringProp("Working directory (jailed; defaults to the workspace)."),
					"cols":    intProp("Terminal columns (default 80)."),
					"rows":    intProp("Terminal rows (default 24)."),
				},
				"required": []string{"command"},
			},
			call: envdPtyStart,
		},
		{
			name:        ToolPtyWrite,
			description: base.describe("Write bytes to a pty session's terminal."),
			version:     "1.0.0",
			parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pid":  pidProp,
					"data": envdStringProp("Bytes to write (a trailing newline is usually needed)."),
				},
				"required": []string{"pid", "data"},
			},
			call: envdPtyWrite,
		},
		{
			name:        ToolPtyRead,
			description: base.describe("Drain buffered pty output. The buffer keeps only the most recent 256 KiB, so earlier bytes of a chatty session may have been dropped."),
			version:     "1.0.0",
			parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pid":     pidProp,
					"wait_ms": intProp("How long to wait for output, in milliseconds (default 1000, max 30000)."),
				},
				"required": []string{"pid"},
			},
			call: envdPtyRead,
		},
		{
			name:        ToolPtyResize,
			description: base.describe("Resize a pty session's terminal."),
			version:     "1.0.0",
			parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pid":  pidProp,
					"cols": intProp("New column count."),
					"rows": intProp("New row count."),
				},
				"required": []string{"pid", "cols", "rows"},
			},
			call: envdPtyResize,
		},
		{
			name:        ToolPtyClose,
			description: base.describe("Terminate a pty session (SIGTERM, best effort) and drop it from the registry."),
			version:     "1.0.0",
			parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"pid": pidProp},
				"required":   []string{"pid"},
			},
			call: envdPtyClose,
		},
	}
}

// envdPtyStart starts the process, hands the stream to a pump, and returns the pid.
//
// The stream is created on a background context because it must outlive this
// call; the first event is still read under the caller's deadline so a dead
// target surfaces as a failed call rather than a phantom session.
func envdPtyStart(ctx context.Context, be *envdBackend, params map[string]interface{}) (*toolkit.ToolResult, error) {
	command, hasCommand := envdParamString(params, "command")
	if !hasCommand || strings.TrimSpace(command) == "" {
		return envdErrorResult("command is required"), nil
	}
	rule, err := be.commands.CheckCommand(command)
	if err != nil {
		return envdErrorResult("command denied (%s): %v", rule, err), nil
	}
	cwd := be.workspace
	if requested := envdStringParam(params, "cwd"); requested != "" {
		resolved, resolveErr := be.resolvePath(ctx, requested)
		if resolveErr != nil {
			return envdErrorResult("cwd: %v", resolveErr), nil
		}
		cwd = resolved
	}
	cols := uint32(envdIntParam(params, "cols", envdPtyDefaultCols))
	rows := uint32(envdIntParam(params, "rows", envdPtyDefaultRows))
	if cols > envdPtyMaxDimension || rows > envdPtyMaxDimension {
		return envdErrorResult("pty size %dx%d is above the %d limit", cols, rows, envdPtyMaxDimension), nil
	}

	sessionCtx, cancel := context.WithCancel(context.Background())
	stream, err := be.client.Start(sessionCtx, envdclient.StartRequest{
		Process: envdclient.ProcessConfig{
			Cmd:  "/bin/sh",
			Args: []string{"-c", command},
			Cwd:  cwd,
		},
		Pty: &envdclient.PTY{Size: &envdclient.PTYSize{Cols: cols, Rows: rows}},
	})
	if err != nil {
		cancel()
		return envdErrorResult("pty_start: %v", err), nil
	}

	type firstResult struct {
		event envdclient.ProcessEvent
		err   error
	}
	firstCh := make(chan firstResult, 1)
	go func() {
		var event envdclient.ProcessEvent
		firstCh <- firstResult{event: event, err: stream.Recv(&event)}
	}()
	select {
	case <-ctx.Done():
		cancel()
		_ = stream.Close()
		return envdErrorResult("pty_start: %v", ctx.Err()), nil
	case first := <-firstCh:
		if first.err != nil {
			cancel()
			_ = stream.Close()
			return envdErrorResult("pty_start: %v", first.err), nil
		}
		if first.event.Kind != envdclient.EventStart || first.event.Start == nil {
			cancel()
			_ = stream.Close()
			return envdErrorResult("pty_start: expected a start event, got %v", first.event.Kind), nil
		}
		pid := first.event.Start.Pid
		session := newEnvdPtySession(pid, stream, cancel)
		be.sessionMu.Lock()
		if be.sessions == nil {
			be.sessions = map[uint32]*envdPtySession{}
		}
		be.sessions[pid] = session
		be.sessionMu.Unlock()
		go session.pump()
		return envdTextResult(fmt.Sprintf("session started: pid=%d (pty_write/pty_read/pty_resize/pty_close)", pid)), nil
	}
}

func envdPtyWrite(ctx context.Context, be *envdBackend, params map[string]interface{}) (*toolkit.ToolResult, error) {
	pid := uint32(envdIntParam(params, "pid", 0))
	if pid == 0 {
		return envdErrorResult("pid is required"), nil
	}
	if _, err := be.findSession(pid); err != nil {
		return envdErrorResult("%v", err), nil
	}
	data, _ := envdParamString(params, "data")
	if err := be.client.SendPtyInput(ctx, pid, []byte(data)); err != nil {
		return envdErrorResult("pty_write: %v", err), nil
	}
	return envdTextResult(fmt.Sprintf("wrote %d bytes to pid %d", len(data), pid)), nil
}

func envdPtyRead(ctx context.Context, be *envdBackend, params map[string]interface{}) (*toolkit.ToolResult, error) {
	pid := uint32(envdIntParam(params, "pid", 0))
	if pid == 0 {
		return envdErrorResult("pid is required"), nil
	}
	session, err := be.findSession(pid)
	if err != nil {
		return envdErrorResult("%v", err), nil
	}
	wait := time.Duration(envdIntParam(params, "wait_ms", int(envdPtyDefaultWait/time.Millisecond))) * time.Millisecond
	if wait <= 0 {
		wait = envdPtyDefaultWait
	}
	if wait > envdPtyMaxWait {
		wait = envdPtyMaxWait
	}
	data, dropped, done, exit, status := session.waitRead(wait)

	var builder strings.Builder
	state := "running"
	if done {
		state = "exited"
	}
	fmt.Fprintf(&builder, "pid=%d state=%s\n", pid, state)
	if len(data) > 0 {
		builder.Write(data)
	} else {
		builder.WriteString("(no output)")
	}
	if dropped > 0 {
		fmt.Fprintf(&builder, "\n[dropped %d bytes]", dropped)
	}
	if done {
		fmt.Fprintf(&builder, "\n[exit %d", exit)
		if status != "" {
			fmt.Fprintf(&builder, " %s", status)
		}
		builder.WriteString("]")
	}
	return envdTextResult(strings.TrimRight(builder.String(), "\n")), nil
}

func envdPtyResize(ctx context.Context, be *envdBackend, params map[string]interface{}) (*toolkit.ToolResult, error) {
	pid := uint32(envdIntParam(params, "pid", 0))
	cols := uint32(envdIntParam(params, "cols", 0))
	rows := uint32(envdIntParam(params, "rows", 0))
	if pid == 0 {
		return envdErrorResult("pid is required"), nil
	}
	if cols == 0 || rows == 0 {
		return envdErrorResult("cols and rows are required"), nil
	}
	if cols > envdPtyMaxDimension || rows > envdPtyMaxDimension {
		return envdErrorResult("pty size %dx%d is above the %d limit", cols, rows, envdPtyMaxDimension), nil
	}
	if _, err := be.findSession(pid); err != nil {
		return envdErrorResult("%v", err), nil
	}
	if err := be.client.UpdatePty(ctx, pid, cols, rows); err != nil {
		return envdErrorResult("pty_resize: %v", err), nil
	}
	return envdTextResult(fmt.Sprintf("resized pid %d to %dx%d", pid, cols, rows)), nil
}

func envdPtyClose(ctx context.Context, be *envdBackend, params map[string]interface{}) (*toolkit.ToolResult, error) {
	pid := uint32(envdIntParam(params, "pid", 0))
	if pid == 0 {
		return envdErrorResult("pid is required"), nil
	}
	be.sessionMu.Lock()
	session := be.sessions[pid]
	delete(be.sessions, pid)
	be.sessionMu.Unlock()
	if session == nil {
		return envdErrorResult("no pty session with pid %d", pid), nil
	}
	// SIGTERM is best effort: the process may already have exited.
	_ = be.client.SendSignal(ctx, pid, "SIGTERM")
	session.close()
	return envdTextResult(fmt.Sprintf("closed pid %d", pid)), nil
}
