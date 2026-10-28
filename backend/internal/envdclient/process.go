package envdclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// processService is the envd service name for process control.
const processService = "process.Process"

// ProcessConfig describes what to run.
//
// The JSON shape mirrors envd's own request body: a nested "process" object with
// cmd/args/envs/cwd. Field names were confirmed from a live envd response, not
// guessed from a doc page.
type ProcessConfig struct {
	// Cmd is an absolute path to the executable envd should run. envd does not
	// consult a shell, so a relative name is not resolved through PATH.
	Cmd string `json:"cmd"`

	// Args are the arguments passed verbatim.
	Args []string `json:"args,omitempty"`

	// Envs are added to the child's environment.
	Envs map[string]string `json:"envs,omitempty"`

	// Cwd is the working directory. Empty means envd's own default.
	Cwd string `json:"cwd,omitempty"`
}

// StartRequest is the body of process.Process/Start.
type StartRequest struct {
	// Process is the command to run.
	Process ProcessConfig `json:"process"`

	// Stdin keeps stdin open so Input can be sent later. It requires HTTP/2 on
	// the transport; see StreamReader.ProtoMajor.
	Stdin bool `json:"stdin,omitempty"`

	// Tag labels the process so a caller can find it again in List output.
	Tag string `json:"tag,omitempty"`
}

// StartEvent is emitted once the process is spawned.
type StartEvent struct {
	Pid uint32 `json:"pid"`
}

// EndEvent is emitted when the process exits.
//
// ExitCode is omitempty in envd's schema, so a process that exits 0 omits the
// field entirely, and a process killed by a signal may omit it while carrying a
// `signal: ...` status. A caller must therefore treat "exited but no usable
// code" carefully, which is what EndEvent.ExitStatus does.
type EndEvent struct {
	ExitCode int32  `json:"exit_code,omitempty"`
	Exited   bool   `json:"exited,omitempty"`
	Status   string `json:"status,omitempty"`
}

// unknownExitStatus is reported when envd ended a process with a status this
// client cannot interpret.
//
// It exists so the fallback can never read as success: reporting a killed or
// otherwise abnormal process as exit 0 is worse than reporting an unexplained
// non-zero code.
const unknownExitStatus int32 = 255

// ExitStatus returns the numeric exit status.
//
// The numeric field wins when non-zero. Otherwise the status string is parsed:
// "exit status N" for a normal non-zero exit, "signal: killed" and friends for a
// process that died from a signal (reported as 128+signum, the shell convention).
// Only an empty status, or an explicit success, yields 0; anything unrecognised
// yields unknownExitStatus.
func (e *EndEvent) ExitStatus() int32 {
	if e == nil {
		return -1
	}
	if e.ExitCode != 0 {
		return e.ExitCode
	}
	// "exit status 3" is what Go's exec puts in Status for a non-zero exit when
	// the numeric field was omitted, so parse it as a fallback rather than
	// reporting 0 for a failed process.
	if code, ok := parseExitStatus(e.Status); ok {
		return code
	}
	if code, ok := parseSignalStatus(e.Status); ok {
		return code
	}
	if strings.TrimSpace(e.Status) == "" {
		return 0
	}
	return unknownExitStatus
}

// parseExitStatus pulls the code out of a Go exec status string.
func parseExitStatus(status string) (int32, bool) {
	const prefix = "exit status "
	if !strings.HasPrefix(status, prefix) {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(status[len(prefix):]), 10, 32)
	if err != nil {
		return 0, false
	}
	return int32(n), true
}

// signalExitCodes maps the signal names Go's exec prints ("signal: killed") to
// 128+signum.
//
// The keys are Go's descriptive names, not SIGKILL-style identifiers, because
// that is what envd puts in the status string. The table is the Linux signal set;
// a name outside it is refused rather than guessed.
var signalExitCodes = map[string]int32{
	"hangup":                   129, // 1 SIGHUP
	"interrupt":                130, // 2 SIGINT
	"quit":                     131, // 3 SIGQUIT
	"illegal instruction":      132, // 4 SIGILL
	"trace/breakpoint trap":    133, // 5 SIGTRAP
	"aborted":                  134, // 6 SIGABRT
	"bus error":                135, // 7 SIGBUS
	"floating point exception": 136, // 8 SIGFPE
	"killed":                   137, // 9 SIGKILL
	"user defined signal 1":    138, // 10 SIGUSR1
	"segmentation fault":       139, // 11 SIGSEGV
	"user defined signal 2":    140, // 12 SIGUSR2
	"broken pipe":              141, // 13 SIGPIPE
	"alarm clock":              142, // 14 SIGALRM
	"terminated":               143, // 15 SIGTERM
	"stack fault":              144, // 16 SIGSTKFLT
	"child exited":             146, // 17 SIGCHLD
	"continued":                148, // 18 SIGCONT
	"stopped":                  147, // 19 SIGSTOP
	"stopped (signal)":         149, // 20 SIGTSTP
	"stopped (tty input)":      150, // 21 SIGTTIN
	"stopped (tty output)":     151, // 22 SIGTTOU
	"urgent i/o condition":     152, // 23 SIGURG
	"cpu time limit exceeded":  153, // 24 SIGXCPU
	"file size limit exceeded": 154, // 25 SIGXFSZ
	"virtual timer expired":    155, // 26 SIGVTALRM
	"profiling timer expired":  156, // 27 SIGPROF
	"window changed":           157, // 28 SIGWINCH
	"i/o possible":             158, // 29 SIGIO
	"power failure":            159, // 30 SIGPWR
	"bad system call":          160, // 31 SIGSYS
}

// parseSignalStatus recognises a signal death from the status string.
func parseSignalStatus(status string) (int32, bool) {
	const prefix = "signal: "
	if !strings.HasPrefix(status, prefix) {
		return 0, false
	}
	code, ok := signalExitCodes[strings.ToLower(strings.TrimSpace(status[len(prefix):]))]
	return code, ok
}

// EventKind identifies which arm of the process event union was present.
type EventKind int

const (
	// EventUnknown means the frame carried no recognised arm.
	EventUnknown EventKind = iota
	// EventStart carries a pid.
	EventStart
	// EventData carries output bytes.
	EventData
	// EventEnd carries the exit status.
	EventEnd
)

// String renders the kind for logs.
func (k EventKind) String() string {
	switch k {
	case EventStart:
		return "start"
	case EventData:
		return "data"
	case EventEnd:
		return "end"
	default:
		return "unknown"
	}
}

// ProcessEvent is one frame from a process stream.
//
// The wire body is {"event":{"start":...}} / {"event":{"data":...}} /
// {"event":{"end":...}}. Output payloads are base64, which is a Connect/JSON
// convention for bytes fields, so decoding happens here rather than at each call
// site.
type ProcessEvent struct {
	Kind  EventKind
	Start *StartEvent
	End   *EndEvent

	// Stdout and Stderr hold decoded output bytes.
	Stdout []byte
	Stderr []byte

	// PtyFd is the pty index when the child ran under a pty.
	PtyFd int32

	// Raw keeps the original frame so a caller can log an arm this client does
	// not model instead of silently dropping it.
	Raw json.RawMessage
}

// wireEvent mirrors the outer {"event": ...} wrapper.
type wireEvent struct {
	Event *struct {
		Start *StartEvent `json:"start,omitempty"`
		Data  *struct {
			Stdout string `json:"stdout,omitempty"`
			Stderr string `json:"stderr,omitempty"`
			Pty    int32  `json:"pty,omitempty"`
		} `json:"data,omitempty"`
		End *EndEvent `json:"end,omitempty"`
	} `json:"event,omitempty"`
}

// UnmarshalJSON decodes one process event frame.
func (e *ProcessEvent) UnmarshalJSON(payload []byte) error {
	e.Raw = append(e.Raw[:0], payload...)

	var wire wireEvent
	if err := json.Unmarshal(payload, &wire); err != nil {
		return err
	}
	if wire.Event == nil {
		return fmt.Errorf("envdclient: process frame has no \"event\" arm: %s", previewBytes(payload))
	}

	switch {
	case wire.Event.Start != nil:
		e.Kind = EventStart
		e.Start = wire.Event.Start
	case wire.Event.Data != nil:
		e.Kind = EventData
		e.PtyFd = wire.Event.Data.Pty
		if wire.Event.Data.Stdout != "" {
			decoded, err := base64.StdEncoding.DecodeString(wire.Event.Data.Stdout)
			if err != nil {
				return fmt.Errorf("envdclient: decode base64 stdout: %w", err)
			}
			e.Stdout = decoded
		}
		if wire.Event.Data.Stderr != "" {
			decoded, err := base64.StdEncoding.DecodeString(wire.Event.Data.Stderr)
			if err != nil {
				return fmt.Errorf("envdclient: decode base64 stderr: %w", err)
			}
			e.Stderr = decoded
		}
	case wire.Event.End != nil:
		e.Kind = EventEnd
		e.End = wire.Event.End
	default:
		e.Kind = EventUnknown
	}
	return nil
}

// previewBytes renders a short excerpt of a payload for an error message.
func previewBytes(payload []byte) string {
	const limit = 160
	if len(payload) <= limit {
		return string(payload)
	}
	return string(payload[:limit]) + "..."
}

// Start launches a process and returns a stream of its lifecycle events.
//
// The returned reader is owned by the caller and must be closed. Cancelling ctx
// terminates the stream, which is the supported way to stop a run: envd reaps the
// child when its stream client disappears.
//
// Callers must validate req.Process.Cmd against their own command policy before
// calling this. envd runs whatever it is told to run.
func (c *Client) Start(ctx context.Context, req StartRequest) (*StreamReader, error) {
	if strings.TrimSpace(req.Process.Cmd) == "" {
		return nil, fmt.Errorf("envdclient: Start requires Process.Cmd")
	}
	return c.Stream(ctx, processService, "Start", req)
}

// Run executes a command to completion and returns its decoded stdout, stderr and
// exit code.
//
// This is the convenience path for the non-interactive case. It does not trim
// output; the caller applies its own limit, because a tool layer's output cap is
// a policy decision that belongs above this package.
//
// Retention is still bounded: a process may emit unbounded output, and letting
// that turn into unbounded caller memory is a footgun rather than a policy
// choice. Each stream keeps at most DefaultMaxBufferedOutput bytes; the rest is
// discarded while the stream keeps draining, so the exit status stays correct.
func (c *Client) Run(ctx context.Context, req StartRequest) (stdout, stderr []byte, exitCode int32, err error) {
	stream, err := c.Start(ctx, req)
	if err != nil {
		return nil, nil, -1, err
	}
	defer func() { _ = stream.Close() }()

	for {
		var event ProcessEvent
		if err := stream.Recv(&event); err != nil {
			if err == io.EOF {
				// The stream closed without an end event. Report the missing status
				// instead of inventing success.
				return stdout, stderr, -1, fmt.Errorf("envdclient: process stream ended without an exit event")
			}
			return stdout, stderr, -1, err
		}
		switch event.Kind {
		case EventData:
			stdout = appendLimited(stdout, event.Stdout, DefaultMaxBufferedOutput)
			stderr = appendLimited(stderr, event.Stderr, DefaultMaxBufferedOutput)
		case EventEnd:
			return stdout, stderr, event.End.ExitStatus(), nil
		}
	}
}

// DefaultMaxBufferedOutput caps the bytes Run retains per stream.
//
// It is deliberately far above any tool-layer output cap (the bridge shows
// 400 KiB and refuses to view more than 1 MiB): this is a memory safety bound,
// not a presentation choice.
const DefaultMaxBufferedOutput = 8 << 20

// appendLimited appends chunk without growing dst past limit.
//
// A non-positive limit means "no cap" so callers that really need everything can
// still ask for it; Run itself always passes DefaultMaxBufferedOutput.
func appendLimited(dst, chunk []byte, limit int) []byte {
	if limit <= 0 {
		return append(dst, chunk...)
	}
	if len(dst) >= limit {
		return dst
	}
	remaining := limit - len(dst)
	if len(chunk) > remaining {
		chunk = chunk[:remaining]
	}
	return append(dst, chunk...)
}

// ProcessInfo is one entry from process.Process/List.
type ProcessInfo struct {
	Config *ProcessConfig `json:"config,omitempty"`
	Pid    uint32         `json:"pid,omitempty"`
	Tag    string         `json:"tag,omitempty"`
}

// ListResponse is the body of process.Process/List.
type ListResponse struct {
	Processes []ProcessInfo `json:"processes"`
}

// List returns the processes envd is currently tracking.
//
// This is a unary call and needs no envelope or auth. It doubles as a cheap
// reachability probe.
func (c *Client) List(ctx context.Context) ([]ProcessInfo, error) {
	var resp ListResponse
	if err := c.doUnary(ctx, processService, "List", struct{}{}, &resp); err != nil {
		return nil, err
	}
	return resp.Processes, nil
}

// sendBidi reports the HTTP/2 requirement honestly rather than pretending to
// support a call the transport cannot carry.
//
// envd exposes SendInput / SendSignal / Update as client-streaming methods. Over
// plaintext HTTP/1.1 the server answers 501, so this checks the negotiated
// protocol first and returns an actionable error instead of a confusing status.
func (c *Client) sendBidi(ctx context.Context, method string, body any) error {
	stream, err := c.Stream(ctx, processService, method, body)
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()
	if proto := stream.ProtoMajor(); proto < 2 {
		return fmt.Errorf("envdclient: %s requires HTTP/2 but the connection negotiated HTTP/%d; enable TLS or use an h2c-capable transport", method, proto)
	}
	var event ProcessEvent
	if err := stream.Recv(&event); err != nil && err != io.EOF {
		return err
	}
	return nil
}

// SendSignal delivers a signal to a running process.
//
// Requires HTTP/2.
func (c *Client) SendSignal(ctx context.Context, pid uint32, signal string) error {
	if signal == "" {
		return fmt.Errorf("envdclient: SendSignal requires a signal name such as SIGTERM")
	}
	return c.sendBidi(ctx, "SendSignal", map[string]any{
		"process": map[string]any{"pid": pid, "signal": signal},
	})
}

// SendInput writes bytes to a process's stdin.
//
// Requires a stdin-enabled process (StartRequest.Stdin) and HTTP/2.
func (c *Client) SendInput(ctx context.Context, pid uint32, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	return c.sendBidi(ctx, "SendInput", map[string]any{
		"process": map[string]any{
			"pid":   pid,
			"input": map[string]any{"data": base64.StdEncoding.EncodeToString(data)},
		},
	})
}

// UpdatePty resizes a process's pty.
//
// Requires HTTP/2.
func (c *Client) UpdatePty(ctx context.Context, pid uint32, cols, rows uint32) error {
	if cols == 0 || rows == 0 {
		return fmt.Errorf("envdclient: UpdatePty requires non-zero cols and rows")
	}
	return c.sendBidi(ctx, "Update", map[string]any{
		"process": map[string]any{
			"pid": pid,
			"pty": map[string]any{"cols": cols, "rows": rows},
		},
	})
}
