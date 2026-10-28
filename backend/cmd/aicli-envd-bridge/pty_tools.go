package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
)

const (
	ptyBufferLimit  = 256 << 10
	ptyDefaultCols  = 80
	ptyDefaultRows  = 24
	ptyDefaultWait  = time.Second
	ptyMaxWait      = 30 * time.Second
	ptyMaxDimension = 1000
)

// ptySession is one interactive process started under a pty.
//
// Output is buffered (tail-bounded) so a chatty session cannot grow memory
// without limit; pty_read drains the buffer.
type ptySession struct {
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

func newPtySession(pid uint32, stream *envdclient.StreamReader, cancel context.CancelFunc) *ptySession {
	return &ptySession{
		pid:    pid,
		stream: stream,
		cancel: cancel,
		notify: make(chan struct{}, 1),
	}
}

func (s *ptySession) signal() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *ptySession) append(p []byte) {
	if len(p) == 0 {
		return
	}
	s.mu.Lock()
	s.data = append(s.data, p...)
	if len(s.data) > ptyBufferLimit {
		drop := len(s.data) - ptyBufferLimit
		// Copy the tail so the buffer does not retain the trimmed prefix.
		s.data = append([]byte(nil), s.data[drop:]...)
		s.dropped += drop
	}
	s.mu.Unlock()
	s.signal()
}

func (s *ptySession) finish(exit int32, status string) {
	s.mu.Lock()
	s.done = true
	s.exit = exit
	s.status = status
	s.mu.Unlock()
	s.signal()
}

// pump drains the stream into the buffer until the process ends.
func (s *ptySession) pump() {
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
func (s *ptySession) waitRead(wait time.Duration) ([]byte, int, bool, int32, string) {
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

func (s *ptySession) close() {
	s.cancel()
	_ = s.stream.Close()
}

// toolPtyStart starts an interactive pty session.
//
// The initial command runs through /bin/sh -c and is checked by the same command
// policy as shell. Once an interactive shell is running, pty_write is not filtered:
// do not allowlist a shell unless the session itself is intended.
func (b *bridge) toolPtyStart(ctx context.Context, args json.RawMessage) callResult {
	var in struct {
		Command string `json:"command"`
		Cwd     string `json:"cwd"`
		Cols    uint32 `json:"cols"`
		Rows    uint32 `json:"rows"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return errorResult("invalid arguments: %v", err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return errorResult("command is required")
	}
	rule, err := b.commands.CheckCommand(in.Command)
	if err != nil {
		return errorResult("command denied (%s): %v", rule, err)
	}
	cwd := b.workspace
	if strings.TrimSpace(in.Cwd) != "" {
		resolved, err := b.resolvePath(ctx, in.Cwd)
		if err != nil {
			return errorResult("cwd: %v", err)
		}
		cwd = resolved
	}
	cols, rows := in.Cols, in.Rows
	if cols == 0 {
		cols = ptyDefaultCols
	}
	if rows == 0 {
		rows = ptyDefaultRows
	}
	if cols > ptyMaxDimension || rows > ptyMaxDimension {
		return errorResult("pty size %dx%d is above the %d limit", cols, rows, ptyMaxDimension)
	}

	sessionCtx, cancel := context.WithCancel(context.Background())
	stream, err := b.client.Start(sessionCtx, envdclient.StartRequest{
		Process: envdclient.ProcessConfig{
			Cmd:  "/bin/sh",
			Args: []string{"-c", in.Command},
			Cwd:  cwd,
		},
		Pty: &envdclient.PTY{Size: &envdclient.PTYSize{Cols: cols, Rows: rows}},
	})
	if err != nil {
		cancel()
		return errorResult("pty_start: %v", err)
	}

	// Read the start event with the caller's deadline, then hand the stream to the
	// session pump. The stream itself uses a background context so it survives the
	// tool call that created it.
	type firstResult struct {
		event envdclient.ProcessEvent
		err   error
	}
	firstCh := make(chan firstResult, 1)
	go func() {
		var event envdclient.ProcessEvent
		err := stream.Recv(&event)
		firstCh <- firstResult{event: event, err: err}
	}()

	select {
	case <-ctx.Done():
		cancel()
		_ = stream.Close()
		return errorResult("pty_start: %v", ctx.Err())
	case first := <-firstCh:
		if first.err != nil {
			cancel()
			_ = stream.Close()
			return errorResult("pty_start: %v", first.err)
		}
		if first.event.Kind != envdclient.EventStart || first.event.Start == nil {
			cancel()
			_ = stream.Close()
			return errorResult("pty_start: expected a start event, got %v", first.event.Kind)
		}
		pid := first.event.Start.Pid
		session := newPtySession(pid, stream, cancel)
		b.sessionMu.Lock()
		b.sessions[pid] = session
		b.sessionMu.Unlock()
		go session.pump()
		return textResult("session started: pid=%d (pty_write/pty_read/pty_resize/pty_close)", pid)
	}
}

func (b *bridge) findSession(pid uint32) (*ptySession, error) {
	b.sessionMu.Lock()
	defer b.sessionMu.Unlock()
	session := b.sessions[pid]
	if session == nil {
		return nil, fmt.Errorf("no pty session with pid %d", pid)
	}
	return session, nil
}

// toolPtyWrite sends bytes to a session's pty.
func (b *bridge) toolPtyWrite(ctx context.Context, args json.RawMessage) callResult {
	var in struct {
		Pid  uint32 `json:"pid"`
		Data string `json:"data"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return errorResult("invalid arguments: %v", err)
	}
	if in.Pid == 0 {
		return errorResult("pid is required")
	}
	if _, err := b.findSession(in.Pid); err != nil {
		return errorResult("%v", err)
	}
	if err := b.client.SendPtyInput(ctx, in.Pid, []byte(in.Data)); err != nil {
		return errorResult("pty_write: %v", err)
	}
	return textResult("wrote %d bytes to pid %d", len(in.Data), in.Pid)
}

// toolPtyRead drains buffered output, optionally waiting for new data first.
func (b *bridge) toolPtyRead(ctx context.Context, args json.RawMessage) callResult {
	var in struct {
		Pid    uint32 `json:"pid"`
		WaitMS int64  `json:"wait_ms"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return errorResult("invalid arguments: %v", err)
	}
	if in.Pid == 0 {
		return errorResult("pid is required")
	}
	session, err := b.findSession(in.Pid)
	if err != nil {
		return errorResult("%v", err)
	}
	wait := ptyDefaultWait
	if in.WaitMS > 0 {
		wait = time.Duration(in.WaitMS) * time.Millisecond
	}
	if wait > ptyMaxWait {
		wait = ptyMaxWait
	}
	_ = ctx // the wait is explicitly bounded and shorter than the call deadline

	data, dropped, done, exit, status := session.waitRead(wait)
	var sb strings.Builder
	fmt.Fprintf(&sb, "pid=%d state=%s\n", in.Pid, map[bool]string{true: "exited", false: "running"}[done])
	if len(data) > 0 {
		sb.Write(data)
	} else {
		sb.WriteString("(no output)")
	}
	if dropped > 0 {
		fmt.Fprintf(&sb, "\n[dropped %d bytes]", dropped)
	}
	if done {
		fmt.Fprintf(&sb, "\n[exit %d", exit)
		if status != "" {
			fmt.Fprintf(&sb, " %s", status)
		}
		sb.WriteString("]")
	}
	return textResult("%s", sb.String())
}

// toolPtyResize resizes a session's terminal.
func (b *bridge) toolPtyResize(ctx context.Context, args json.RawMessage) callResult {
	var in struct {
		Pid  uint32 `json:"pid"`
		Cols uint32 `json:"cols"`
		Rows uint32 `json:"rows"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return errorResult("invalid arguments: %v", err)
	}
	if in.Pid == 0 {
		return errorResult("pid is required")
	}
	if in.Cols == 0 || in.Rows == 0 {
		return errorResult("cols and rows are required")
	}
	if in.Cols > ptyMaxDimension || in.Rows > ptyMaxDimension {
		return errorResult("pty size %dx%d is above the %d limit", in.Cols, in.Rows, ptyMaxDimension)
	}
	if _, err := b.findSession(in.Pid); err != nil {
		return errorResult("%v", err)
	}
	if err := b.client.UpdatePty(ctx, in.Pid, in.Cols, in.Rows); err != nil {
		return errorResult("pty_resize: %v", err)
	}
	return textResult("resized pid %d to %dx%d", in.Pid, in.Cols, in.Rows)
}

// toolPtyClose terminates a session and drops it from the registry.
func (b *bridge) toolPtyClose(ctx context.Context, args json.RawMessage) callResult {
	var in struct {
		Pid uint32 `json:"pid"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return errorResult("invalid arguments: %v", err)
	}
	if in.Pid == 0 {
		return errorResult("pid is required")
	}
	b.sessionMu.Lock()
	session := b.sessions[in.Pid]
	delete(b.sessions, in.Pid)
	b.sessionMu.Unlock()
	if session == nil {
		return errorResult("no pty session with pid %d", in.Pid)
	}
	// SIGTERM is best effort: the process may already have exited.
	_ = b.client.SendSignal(ctx, in.Pid, "SIGTERM")
	session.close()
	return textResult("closed pid %d", in.Pid)
}

// closeAllSessions tears down every session when the bridge exits.
func (b *bridge) closeAllSessions() {
	b.sessionMu.Lock()
	sessions := make([]*ptySession, 0, len(b.sessions))
	for _, session := range b.sessions {
		sessions = append(sessions, session)
	}
	b.sessions = nil
	b.sessionMu.Unlock()
	for _, session := range sessions {
		session.close()
	}
}

func init() {
	toolDefs["pty_start"] = toolDef{
		Name: "pty_start",
		Description: "Start an interactive process under a pty (ops only). The command runs via /bin/sh -c; " +
			"the bridge applies its command allowlist, while input sent later via pty_write is not filtered. " +
			"Returns a pid for pty_write/pty_read/pty_resize/pty_close.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": stringProp("Shell command to run under the pty"),
				"cwd":     stringProp("Working directory (jailed; defaults to the workspace)"),
				"cols":    map[string]any{"type": "integer", "description": "Terminal columns (default 80)"},
				"rows":    map[string]any{"type": "integer", "description": "Terminal rows (default 24)"},
			},
			"required": []string{"command"},
		},
	}
	toolDefs["pty_write"] = toolDef{
		Name:        "pty_write",
		Description: "Write bytes to a pty session's terminal.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pid":  map[string]any{"type": "integer", "description": "Session pid from pty_start"},
				"data": stringProp("Bytes to write (a trailing newline is usually needed)"),
			},
			"required": []string{"pid", "data"},
		},
	}
	toolDefs["pty_read"] = toolDef{
		Name:        "pty_read",
		Description: "Drain buffered pty output, optionally waiting up to wait_ms for new data (max 30000).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pid":     map[string]any{"type": "integer", "description": "Session pid from pty_start"},
				"wait_ms": map[string]any{"type": "integer", "description": "Maximum wait for new output in milliseconds (default 1000)"},
			},
			"required": []string{"pid"},
		},
	}
	toolDefs["pty_resize"] = toolDef{
		Name:        "pty_resize",
		Description: "Resize a pty session's terminal.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pid":  map[string]any{"type": "integer", "description": "Session pid from pty_start"},
				"cols": map[string]any{"type": "integer", "description": "Terminal columns"},
				"rows": map[string]any{"type": "integer", "description": "Terminal rows"},
			},
			"required": []string{"pid", "cols", "rows"},
		},
	}
	toolDefs["pty_close"] = toolDef{
		Name:        "pty_close",
		Description: "Terminate a pty session and remove it from the registry.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pid": map[string]any{"type": "integer", "description": "Session pid from pty_start"},
			},
			"required": []string{"pid"},
		},
	}
}
