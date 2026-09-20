// Real end-to-end ACP verification of the P1 gate for client-supplied MCP
// servers in an UNTRUSTED workspace (plan §6.2 acceptance criterion 2).
//
// The script runs the real aicli binary as `aicli agent stdio` with an isolated
// USERPROFILE/HOME, an isolated *untrusted* workspace and a local mock
// OpenAI-compatible provider, then drives the real NDJSON JSON-RPC traffic:
//
//  1. initialize advertises agentCapabilities.mcpCapabilities.http/sse = true
//     (P2 "capability set = promise"; the gate below must not come from a
//     missing capability bit)
//  2. session/new carries a stdio server whose command writes a marker file the
//     instant it starts. The workspace is untrusted (folder-trust feature ON,
//     repo-local .aicli/mcp.yaml present, no trust record in the isolated HOME,
//     headless ACP => no interactive prompt), so the gate must refuse it.
//  3. Assertions:
//     a. the marker file is absent => the stdio process was never spawned
//        (positive control: running the same command directly DOES create the
//        marker, so "absent" means "not started", not "broken probe");
//     b. session/new still returns a non-empty sessionId (the refusal is
//        per-entry, never a request failure);
//     c. the refusal is visible on the diagnostic channel with both the reason
//        and the server name ("refused ... untrusted workspace (<name>)"), in
//        stderr and/or the agent's log file;
//     d. a sibling entry with an unknown transport type is skipped per-entry
//        with its own diagnostic, and session/new still succeeds.
//
// Run (from backend/):
//
//	go run ./scripts/acp_e2e_mcp_reject_untrusted.go [aicli.exe]
//
// With no argument the real aicli binary is built into a temp dir with
// `go build -o <tmp>/aicli.exe ./cmd/aicli`.

//go:build ignore

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// minimal JSON-RPC harness (same shape as acp_e2e_session_mgmt.go)
// ---------------------------------------------------------------------------

type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) String() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("code=%d message=%q", e.Code, e.Message)
}

type inbound struct {
	msg  Message
	line string
}

func writeMsg(w io.Writer, msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

type e2e struct {
	mu      sync.Mutex
	nextID  int
	pending map[string]chan inbound
	stdin   io.Writer
}

func (e *e2e) begin(method string, params interface{}) (chan inbound, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	ch := make(chan inbound, 1)
	e.mu.Lock()
	e.nextID++
	id := e.nextID
	e.pending[fmt.Sprintf("%d", id)] = ch
	e.mu.Unlock()
	return ch, writeMsg(e.stdin, Message{
		JSONRPC: "2.0",
		ID:      json.RawMessage(fmt.Sprintf("%d", id)),
		Method:  method,
		Params:  raw,
	})
}

type rpcCall struct {
	Method string
	Result json.RawMessage
	Err    *rpcError
	Frame  string
}

func (c rpcCall) errorString() string {
	if c.Err == nil {
		return ""
	}
	return c.Err.String()
}

func (e *e2e) call(method string, params interface{}, timeout time.Duration) (rpcCall, error) {
	ch, err := e.begin(method, params)
	if err != nil {
		return rpcCall{Method: method}, err
	}
	select {
	case in := <-ch:
		out := rpcCall{Method: method, Result: in.msg.Result, Frame: in.line}
		if in.msg.Error != nil {
			var re rpcError
			if err := json.Unmarshal(in.msg.Error, &re); err != nil {
				out.Err = &rpcError{Message: string(in.msg.Error)}
			} else {
				out.Err = &re
			}
		}
		return out, nil
	case <-time.After(timeout):
		return rpcCall{Method: method}, fmt.Errorf("timeout after %s waiting for %s", timeout, method)
	}
}

// ---------------------------------------------------------------------------
// assertion bookkeeping
// ---------------------------------------------------------------------------

type checker struct {
	mu     sync.Mutex
	passed int
	failed int
	names  []string
}

func (c *checker) pass(name string, detail string, args ...interface{}) {
	c.mu.Lock()
	c.passed++
	c.names = append(c.names, name+": PASS")
	c.mu.Unlock()
	if detail == "" {
		fmt.Printf("PASS %s\n", name)
		return
	}
	fmt.Printf("PASS %s: "+detail+"\n", append([]interface{}{name}, args...)...)
}

func (c *checker) fail(name string, reason string, args ...interface{}) {
	c.mu.Lock()
	c.failed++
	c.names = append(c.names, name+": FAIL")
	c.mu.Unlock()
	fmt.Printf("FAIL %s: "+reason+"\n", append([]interface{}{name}, args...)...)
}

func (c *checker) check(name string, ok bool, reason string, args ...interface{}) bool {
	if ok {
		c.pass(name, "")
		return true
	}
	c.fail(name, reason, args...)
	return false
}

func (c *checker) summary() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Printf("E2E SUMMARY: %d passed, %d failed\n", c.passed, c.failed)
	for _, line := range c.names {
		fmt.Printf("  %s\n", line)
	}
	if c.failed > 0 {
		fmt.Println("E2E FAIL")
		return 1
	}
	fmt.Println("E2E PASS")
	return 0
}

// ---------------------------------------------------------------------------
// process / environment helpers
// ---------------------------------------------------------------------------

// captureStream tees a pipe into a buffer (and mirrors it to our stdout with a
// prefix) so assertions can search the agent's diagnostic channel afterwards.
type captureStream struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *captureStream) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.buf.Write(p)
	c.mu.Unlock()
	return len(p), nil
}

func (c *captureStream) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// buildAICLI compiles the real aicli binary into a temp dir when the caller did
// not pass one explicitly.
func buildAICLI() (string, func(), error) {
	dir, err := os.MkdirTemp("", "acp-e2e-bin")
	if err != nil {
		return "", nil, fmt.Errorf("temp bin dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(dir) }
	exe := filepath.Join(dir, "aicli.exe")
	cmd := exec.Command("go", "build", "-o", exe, "./cmd/aicli")
	out, err := cmd.CombinedOutput()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("go build ./cmd/aicli: %v\n%s", err, strings.TrimSpace(string(out)))
	}
	return exe, cleanup, nil
}

// buildMarkerProbe compiles a tiny stdlib-only helper that writes a marker file
// the instant it is executed.
//
// It is deliberately NOT an MCP server: if the gate ever lets the entry through,
// the process starts, the marker appears and the test fails. A compiled helper
// is used instead of a shell one-liner so the probe cannot be defeated by
// cmd.exe / sh argument quoting (which is what makes "marker absent" a
// trustworthy signal).
func buildMarkerProbe() (string, func(), error) {
	dir, err := os.MkdirTemp("", "acp-e2e-mcp-probe")
	if err != nil {
		return "", nil, fmt.Errorf("temp probe dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(dir) }
	src := `package main

import (
	"os"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	_ = os.WriteFile(os.Args[1], []byte("started\n"), 0o644)
	// Stay alive briefly so a real spawn is observable even if the parent
	// inspects the process table right after session/new.
	time.Sleep(500 * time.Millisecond)
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("write probe source: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module acpe2emcpprobe\n\ngo 1.21\n"), 0o644); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("write probe go.mod: %w", err)
	}
	exe := filepath.Join(dir, "mcp-marker-probe.exe")
	build := exec.Command("go", "build", "-o", exe, ".")
	build.Dir = dir
	// Offline, stdlib only, no toolchain download.
	build.Env = append(os.Environ(), "GOPROXY=off", "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("go build marker probe: %v\n%s", err, strings.TrimSpace(string(out)))
	}
	return exe, cleanup, nil
}

// scanRootsForSecret walks every file below root and returns the first file that
// contains needle (empty when none does). Used for the "credentials never reach
// the log" assertion; unreadable/binary files are skipped.
func firstFileContaining(root string, needles []string) (string, string) {
	var foundPath, foundNeedle string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil || info.Size() > 8<<20 {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for _, needle := range needles {
			if needle == "" {
				continue
			}
			if bytes.Contains(data, []byte(needle)) {
				foundPath, foundNeedle = path, needle
				return io.EOF // stop the walk
			}
		}
		return nil
	})
	return foundPath, foundNeedle
}

// logCorpus concatenates every log-ish artifact the agent produced: the
// captured stderr/stdout plus all files under the isolated HOME and workspace.
func logCorpus(home string, workspace string, stderr *captureStream, stdout *captureStream) string {
	var sb strings.Builder
	sb.WriteString(stderr.String())
	sb.WriteString("\n")
	sb.WriteString(stdout.String())
	for _, root := range []string{home, workspace} {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() {
				return nil
			}
			info, statErr := d.Info()
			if statErr != nil || info.Size() > 8<<20 {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			sb.WriteString("\n--- file: ")
			sb.WriteString(path)
			sb.WriteString(" ---\n")
			sb.Write(data)
			return nil
		})
	}
	return sb.String()
}

func main() {
	bin := ""
	if len(os.Args) >= 2 {
		bin = strings.TrimSpace(os.Args[1])
	} else {
		built, cleanup, err := buildAICLI()
		if err != nil {
			fmt.Printf("FAIL build aicli: %v\n", err)
			os.Exit(2)
		}
		defer cleanup()
		bin = built
		fmt.Printf("built aicli: %s\n", bin)
	}

	c := &checker{}

	// --- isolated untrusted workspace -------------------------------------
	// A repo-local trust-sensitive config (.aicli/mcp.yaml) makes the folder
	// "trust-worthy looking", so headless resolution yields UNTRUSTED unless a
	// durable trust record exists. The isolated HOME has none.
	workspace, err := os.MkdirTemp("", "acp-e2e-mcp-untrusted")
	if err != nil {
		fmt.Printf("FAIL temp workspace: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(workspace)
	if err := os.MkdirAll(filepath.Join(workspace, ".aicli"), 0o755); err != nil {
		fmt.Printf("FAIL mkdir workspace/.aicli: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".aicli", "mcp.yaml"),
		[]byte("mcpServers: {}\n"), 0o644); err != nil {
		fmt.Printf("FAIL write workspace/.aicli/mcp.yaml: %v\n", err)
		os.Exit(1)
	}

	home, err := os.MkdirTemp("", "acp-e2e-mcp-untrusted-home")
	if err != nil {
		fmt.Printf("FAIL temp home: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(home)
	aicliDir := filepath.Join(home, ".aicli")
	if err := os.MkdirAll(aicliDir, 0o755); err != nil {
		fmt.Printf("FAIL mkdir .aicli: %v\n", err)
		os.Exit(1)
	}

	// Marker lives outside the workspace so a stray config scan cannot trip it.
	markerDir, err := os.MkdirTemp("", "acp-e2e-mcp-marker")
	if err != nil {
		fmt.Printf("FAIL temp marker dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(markerDir)
	markerPath := filepath.Join(markerDir, "stdio-started.marker")

	probeExe, probeCleanup, err := buildMarkerProbe()
	if err != nil {
		fmt.Printf("FAIL build marker probe: %v\n", err)
		os.Exit(2)
	}
	defer probeCleanup()
	probeCommand := probeExe
	probeArgs := []string{markerPath}
	const probeName = "untrusted-stdio-probe"

	// --- positive control: the probe really does write its marker ----------
	_ = os.Remove(markerPath)
	control := exec.Command(probeCommand, probeArgs...)
	controlOut, controlErr := control.CombinedOutput()
	_, statErr := os.Stat(markerPath)
	c.check("A0 positive control: probe command writes its marker when actually executed",
		statErr == nil,
		"probe %q %v did not create %s (run err=%v out=%q); the negative assertion below would be vacuous",
		probeCommand, probeArgs, markerPath, controlErr, strings.TrimSpace(string(controlOut)))
	_ = os.Remove(markerPath)

	// --- local mock OpenAI-compatible provider ----------------------------
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		emit := func(payload string) {
			fmt.Fprintf(w, "data: %s\n\n", payload)
			if flusher != nil {
				flusher.Flush()
			}
		}
		emit(`{"id":"mock","object":"chat.completion.chunk","created":1,"model":"m1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
		emit(`{"id":"mock","object":"chat.completion.chunk","created":1,"model":"m1","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`)
		emit(`{"id":"mock","object":"chat.completion.chunk","created":1,"model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	cfg := fmt.Sprintf(`aicli:
  chat:
    default_provider: mock
    default_model: m1
    stream: true
    no_interactive: true
providers:
  default_provider: mock
  items:
    mock:
      enabled: true
      protocol: openai
      base_url: %s
      api_path: /v1/chat/completions
      api_key: mock-key
      default_model: m1
`, srv.URL)
	if err := os.WriteFile(filepath.Join(aicliDir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		fmt.Printf("FAIL write config: %v\n", err)
		os.Exit(1)
	}

	// --- start the real aicli ACP host ------------------------------------
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "agent", "stdio")
	// The process cwd is the untrusted workspace: folder trust resolves against
	// os.Getwd() for headless ACP (agent_stdio.go ensureProcessFolderTrust).
	cmd.Dir = workspace
	cmd.Env = append(os.Environ(),
		"USERPROFILE="+home,
		"HOME="+home,
		// Folder-trust feature ON: without it the gate is inert by design.
		"AICLI_FOLDER_TRUST=1",
	)
	stdin, _ := cmd.StdinPipe()
	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		fmt.Printf("FAIL start: %v\n", err)
		os.Exit(1)
	}
	stderrBuf := &captureStream{}
	stdoutBuf := &captureStream{}
	defer func() {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}()

	e := &e2e{pending: map[string]chan inbound{}, stdin: stdin}
	go func() {
		reader := bufio.NewReader(stdoutPipe)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			stdoutBuf.Write([]byte(line + "\n"))
			var msg Message
			if json.Unmarshal([]byte(line), &msg) != nil {
				continue
			}
			if msg.ID == nil {
				continue
			}
			e.mu.Lock()
			ch := e.pending[string(msg.ID)]
			e.mu.Unlock()
			if ch != nil {
				ch <- inbound{msg: msg, line: line}
			}
		}
	}()
	go func() {
		reader := bufio.NewReader(stderrPipe)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			stderrBuf.Write([]byte(line))
			fmt.Printf("  [agent stderr] %s", line)
		}
	}()

	mustCall := func(name, method string, params interface{}, timeout time.Duration) rpcCall {
		resp, err := e.call(method, params, timeout)
		if err != nil {
			c.fail(name, "transport error: %v", err)
			fmt.Printf("E2E FAIL (fatal): %v\n", err)
			fmt.Println(strings.Join([]string{"", "--- agent stderr tail ---", tailLines(stderrBuf.String(), 25)}, "\n"))
			os.Exit(c.summary())
		}
		return resp
	}

	// --- 1. initialize ----------------------------------------------------
	initResp := mustCall("A1 initialize", "initialize", map[string]interface{}{
		"protocolVersion":    1,
		"clientCapabilities": map[string]interface{}{"terminal": false},
	}, 30*time.Second)
	var initRes struct {
		AgentCapabilities struct {
			MCPCapabilities *struct {
				HTTP bool `json:"http"`
				SSE  bool `json:"sse"`
			} `json:"mcpCapabilities"`
		} `json:"agentCapabilities"`
	}
	_ = json.Unmarshal(initResp.Result, &initRes)
	capsHTTP, capsSSE := false, false
	if initRes.AgentCapabilities.MCPCapabilities != nil {
		capsHTTP = initRes.AgentCapabilities.MCPCapabilities.HTTP
		capsSSE = initRes.AgentCapabilities.MCPCapabilities.SSE
	}
	c.check("A1 initialize advertises agentCapabilities.mcpCapabilities.http=true",
		capsHTTP, "mcpCapabilities.http = %v (raw result: %s); P2 declares http support, so the refusal below cannot be blamed on a missing capability bit",
		capsHTTP, strings.TrimSpace(string(initResp.Result)))
	c.check("A1b initialize advertises agentCapabilities.mcpCapabilities.sse=true",
		capsSSE, "mcpCapabilities.sse = %v (raw result: %s)", capsSSE, strings.TrimSpace(string(initResp.Result)))

	// --- 2. session/new with an untrusted stdio entry + an unknown type ----
	unknownURL := "http://127.0.0.1:1/never-contacted"
	newResp := mustCall("A2 session/new (untrusted workspace)", "session/new", map[string]interface{}{
		"cwd": workspace,
		"mcpServers": []interface{}{
			map[string]interface{}{
				"name":    probeName,
				"command": probeCommand,
				"args":    probeArgs,
			},
			map[string]interface{}{
				"name": "unknown-transport-probe",
				"type": "websocket",
				"url":  unknownURL,
			},
		},
	}, 60*time.Second)

	sessionID := ""
	if newResp.Err == nil {
		var snew struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(newResp.Result, &snew)
		sessionID = strings.TrimSpace(snew.SessionID)
	}
	c.check("A2 session/new still returns a sessionId despite refused entries",
		newResp.Err == nil && sessionID != "",
		"result = %s error = %s, want a non-empty sessionId with no error",
		strings.TrimSpace(string(newResp.Result)), newResp.errorString())

	// Give any (buggy) async spawn a chance to happen before asserting absence.
	time.Sleep(3 * time.Second)

	// --- 3a. the stdio process was never spawned --------------------------
	_, markerStatErr := os.Stat(markerPath)
	c.check("A3 no stdio process started: marker file absent after session/new",
		markerStatErr != nil,
		"marker %s exists (stat err=%v): the untrusted stdio entry was actually spawned", markerPath, markerStatErr)

	// --- 3b/3c. diagnostics carry the refusal reason and the server name ---
	corpus := logCorpus(home, workspace, stderrBuf, stdoutBuf)
	stderrText := stderrBuf.String()

	c.check("A4 stderr diagnostic uses the stable acp-mcp prefix",
		strings.Contains(stderrText, "[acp-mcp]"),
		"stderr did not contain the [acp-mcp] diagnostic prefix; stderr tail:\n%s", tailLines(stderrText, 20))

	c.check("A5 refusal reason present in diagnostics (untrusted workspace / folder trust)",
		strings.Contains(corpus, "untrusted workspace") && strings.Contains(corpus, "folder trust"),
		"no refusal reason found; wanted substrings %q and %q. stderr tail:\n%s",
		"untrusted workspace", "folder trust", tailLines(stderrText, 20))

	c.check("A6 refused server name present in diagnostics",
		strings.Contains(corpus, probeName),
		"server name %q not found in stderr/log corpus. stderr tail:\n%s", probeName, tailLines(stderrText, 20))

	c.check("A7 refusal names the offending server explicitly (refused ... (<name>))",
		strings.Contains(corpus, "refused 1 client-provided MCP server(s) in an untrusted workspace ("+probeName+")"),
		"expected the exact refusal sentence with the server name; stderr tail:\n%s", tailLines(stderrText, 20))

	// --- 3d. unknown transport type is skipped per-entry with a diagnostic -
	c.check("A8 unknown transport type is skipped with a per-entry diagnostic",
		strings.Contains(corpus, `unsupported transport type "websocket"`) &&
			strings.Contains(corpus, "unknown-transport-probe"),
		"missing per-entry decode diagnostic for the unknown transport; stderr tail:\n%s", tailLines(stderrText, 20))

	// The refused session must still be usable/closeable: no half-built state.
	closeResp := mustCall("A9 session/close on the refused session", "session/close", map[string]interface{}{
		"sessionId": sessionID,
	}, 30*time.Second)
	c.check("A9 session/close of the refused session succeeds ({})",
		closeResp.Err == nil && strings.TrimSpace(string(closeResp.Result)) == "{}",
		"result = %s error = %s, want {}", strings.TrimSpace(string(closeResp.Result)), closeResp.errorString())

	// Nothing may have started after close either.
	time.Sleep(500 * time.Millisecond)
	_, markerStatErr = os.Stat(markerPath)
	c.check("A10 marker still absent after session/close",
		markerStatErr != nil,
		"marker %s exists after session/close (stat err=%v)", markerPath, markerStatErr)

	os.Exit(c.summary())
}

// tailLines returns the last n lines of s (diagnostic helper for failures).
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
