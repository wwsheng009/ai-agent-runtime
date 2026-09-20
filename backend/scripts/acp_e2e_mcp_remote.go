// Real end-to-end ACP verification of the P2 remote MCP transports (plan §6.3).
//
// The script runs the real aicli binary as `aicli agent stdio` with an isolated
// USERPROFILE/HOME and a local mock OpenAI-compatible provider, then drives the
// real NDJSON JSON-RPC traffic against two LOCAL mock MCP servers built with the
// same MCP Go SDK the runtime uses (so the wire protocol is correct by
// construction and the test stays fully offline):
//
//   - a Streamable HTTP endpoint (mcp.NewStreamableHTTPHandler)
//   - an SSE endpoint (mcp.NewSSEHandler)
//
// The client-supplied mcpServers entries are:
//
//	{"name":"remote-http-probe","type":"http","url":<streamable>,"headers":[Authorization, X-Api-Key]}
//	{"name":"remote-sse-probe", "type":"sse", "url":<sse>,       "headers":[X-Sse-Token]}
//	{"name":"unknown-transport-probe","type":"websocket","url":...}   // per-entry skip
//
// Assertions:
//
//  1. initialize advertises agentCapabilities.mcpCapabilities.http/sse = true
//     ("capability set = promise": the entries below must not be gated away)
//  2. session/new returns a sessionId even though one entry has an unknown type
//  3. connection success: both endpoints complete the MCP handshake and serve
//     tools/list, and the connected tools really reach the model: the mock
//     provider's recorded request body for the session's first prompt lists
//     both mock tools. A baseline session created beforehand (no client-provided
//     server) must NOT list them, so the names cannot come from anywhere else.
//  4. the http entry really is mapped onto the runtime's *streamable* transport:
//     its endpoint receives JSON-RPC POSTs and never an SSE-style GET
//  5. header injection: the mock servers observe the downloaded headers
//     verbatim — the streamable endpoint sees Authorization + X-Api-Key, the
//     SSE endpoint sees X-Sse-Token on both the stream GET and the message POST
//  6. credentials never reach the logs: the acp.mcp.audit line carries the
//     header KEY NAMES only (header_keys=...), and the secret VALUES appear
//     nowhere in stderr, stdout or any file the agent wrote under HOME
//  7. unknown transport type is skipped per-entry with a visible diagnostic
//
// Run (from backend/):
//
//	go run ./scripts/acp_e2e_mcp_remote.go [aicli.exe]
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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
// mock MCP servers (real SDK server side) + request recorder
// ---------------------------------------------------------------------------

type hit struct {
	method string
	path   string
	header http.Header
	body   []byte
}

type recorder struct {
	mu   sync.Mutex
	hits []hit
}

func (r *recorder) add(h hit) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hits = append(r.hits, h)
}

func (r *recorder) snapshot() []hit {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]hit, len(r.hits))
	copy(out, r.hits)
	return out
}

// headerValueSeen reports whether any recorded request carried key: value.
func (r *recorder) headerValueSeen(key, value string) bool {
	for _, h := range r.snapshot() {
		if h.header.Get(key) == value {
			return true
		}
	}
	return false
}

// headerValueSeenForMethod narrows the previous check to one HTTP verb.
func (r *recorder) headerValueSeenForMethod(method, key, value string) bool {
	for _, h := range r.snapshot() {
		if h.method == method && h.header.Get(key) == value {
			return true
		}
	}
	return false
}

// sawJSONRPCMethod reports whether any recorded request body was a JSON-RPC
// call for the given MCP method (initialize / tools/list / ...).
func (r *recorder) sawJSONRPCMethod(method string) bool {
	for _, h := range r.snapshot() {
		if len(h.body) == 0 {
			continue
		}
		var probe struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(h.body, &probe) == nil && probe.Method == method {
			return true
		}
	}
	return false
}

func (r *recorder) countMethod(method string) int {
	n := 0
	for _, h := range r.snapshot() {
		if h.method == method {
			n++
		}
	}
	return n
}

func (r *recorder) count() int {
	return len(r.snapshot())
}

// bodiesContainingAll counts recorded request bodies that contain every needle.
// It is used against the mock provider to inspect the model-facing tool list.
func (r *recorder) bodiesContainingAll(needles ...string) int {
	n := 0
	for _, h := range r.snapshot() {
		if len(h.body) == 0 {
			continue
		}
		ok := true
		for _, needle := range needles {
			if !strings.Contains(string(h.body), needle) {
				ok = false
				break
			}
		}
		if ok {
			n++
		}
	}
	return n
}

// describeBodies summarises each recorded provider request: how many tool
// definitions the model was offered and whether the mock MCP tools were among
// them. This makes a failing model-facing tool-list assertion diagnosable.
func (r *recorder) describeBodies() string {
	var sb strings.Builder
	for i, h := range r.snapshot() {
		body := string(h.body)
		fmt.Fprintf(&sb, "\n    req[%d] bytes=%d toolEntries=%d hasMockHTTP=%v hasMockSSE=%v",
			i, len(body), strings.Count(body, `"type":"function"`),
			strings.Contains(body, "mock_echo_http"), strings.Contains(body, "mock_echo_sse"))
	}
	if sb.Len() == 0 {
		return " (no provider requests recorded)"
	}
	return sb.String()
}

// toolsLoadedLines extracts the "AICLI tools loaded:" lines (one per session).
// They enumerate the session tool surface that was assembled at setup time, so
// they show whether the client-provided MCP tools made it into the session
// catalog.
func toolsLoadedLines(corpus string) []string {
	var out []string
	for _, line := range strings.Split(corpus, "\n") {
		if idx := strings.Index(line, "AICLI tools loaded:"); idx >= 0 {
			out = append(out, strings.TrimSpace(line[idx:]))
		}
	}
	return out
}

// describe renders a compact transcript for failure messages.
func (r *recorder) describe() string {
	var sb strings.Builder
	for _, h := range r.snapshot() {
		creds := make([]string, 0, 3)
		for _, key := range []string{"Authorization", "X-Api-Key", "X-Sse-Token"} {
			if v := h.header.Get(key); v != "" {
				creds = append(creds, key+"="+v)
			}
		}
		body := strings.TrimSpace(string(h.body))
		if len(body) > 160 {
			body = body[:160] + "..."
		}
		fmt.Fprintf(&sb, "\n    %s %s [%s] body=%s", h.method, h.path, strings.Join(creds, ","), body)
	}
	if sb.Len() == 0 {
		return " (no requests recorded)"
	}
	return sb.String()
}

// recordHits wraps an MCP handler so every request (method, path, headers,
// body) is captured before the SDK sees it. The body is replayed to the inner
// handler.
func recordHits(rec *recorder, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil && r.Method != http.MethodGet {
			body, _ = io.ReadAll(r.Body)
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		rec.add(hit{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: body})
		next.ServeHTTP(w, r)
	})
}

// newMockMCPServer builds a real MCP server exposing one trivial tool. The same
// instance can back both transports (the SDK server supports concurrent
// sessions).
func newMockMCPServer(serverName, toolName string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: "1.0.0"}, nil)
	type echoIn struct {
		Text string `json:"text" jsonschema:"text to echo back"`
	}
	type echoOut struct {
		Echo string `json:"echo"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        toolName,
		Description: "Echo the provided text back (e2e mock tool)",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
		return nil, echoOut{Echo: in.Text}, nil
	})
	return server
}

func newMockStreamableServer(serverName, toolName string) *httptest.Server {
	server := newMockMCPServer(serverName, toolName)
	return httptest.NewServer(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	))
}

func newMockSSEServer(serverName, toolName string) *httptest.Server {
	server := newMockMCPServer(serverName, toolName)
	return httptest.NewServer(mcp.NewSSEHandler(
		func(*http.Request) *mcp.Server { return server },
		nil,
	))
}

// registeredCountFor extracts N from the session-scoped log line
// "<prefix> session=<id> registered N MCP tool(s) after connect". The runtime
// only emits it when tools arrive AFTER session setup (they are pre-registered
// during setup otherwise), so it is informational, not a hard assertion.
func registeredCountFor(corpus, sessionID string) int {
	re := regexp.MustCompile(`session=` + regexp.QuoteMeta(sessionID) + ` registered (\d+) MCP tool\(s\) after connect`)
	match := re.FindStringSubmatch(corpus)
	if len(match) != 2 {
		return 0
	}
	n, err := strconv.Atoi(match[1])
	if err != nil {
		return 0
	}
	return n
}

// ---------------------------------------------------------------------------
// positive controls: prove the mock endpoints themselves speak MCP, so that a
// "the endpoint saw nothing" failure really points at the runtime.
// ---------------------------------------------------------------------------

func probeStreamable(url string) (string, error) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
		`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"1"}}}`
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("status=%d content-type=%s body=%s",
		resp.StatusCode, resp.Header.Get("Content-Type"), strings.TrimSpace(string(data))), nil
}

// probeSSE opens the SSE stream and waits for the endpoint event that carries
// the message URL.
func probeSSE(url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status=%d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	lines := make([]string, 0, 6)
	for scanner.Scan() {
		line := scanner.Text()
		lines = append(lines, line)
		if strings.HasPrefix(line, "event: endpoint") {
			// the data line follows immediately
			if scanner.Scan() {
				lines = append(lines, scanner.Text())
			}
			return fmt.Sprintf("status=%d content-type=%s stream=%q",
				resp.StatusCode, resp.Header.Get("Content-Type"), strings.Join(lines, "|")), nil
		}
		if len(lines) > 20 {
			break
		}
	}
	return "", fmt.Errorf("no endpoint event on the SSE stream; status=%d content-type=%s saw=%q",
		resp.StatusCode, resp.Header.Get("Content-Type"), strings.Join(lines, "|"))
}

// ---------------------------------------------------------------------------
// process / environment helpers
// ---------------------------------------------------------------------------

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

// logCorpus concatenates every artifact the agent produced: captured
// stderr/stdout plus all files under the isolated HOME and the workspace.
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

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func main() {
	os.Exit(run())
}

func run() int {
	bin := ""
	if len(os.Args) >= 2 {
		bin = strings.TrimSpace(os.Args[1])
	} else {
		built, cleanup, err := buildAICLI()
		if err != nil {
			fmt.Printf("FAIL build aicli: %v\n", err)
			return 2
		}
		defer cleanup()
		bin = built
		fmt.Printf("built aicli: %s\n", bin)
	}

	c := &checker{}

	// --- R0 positive controls: separate mock instances, never recorded -----
	ctlStreamable := newMockStreamableServer("aicli-e2e-control-streamable", "control_echo")
	ctlSSE := newMockSSEServer("aicli-e2e-control-sse", "control_echo")
	streamableProbe, streamableProbeErr := probeStreamable(ctlStreamable.URL)
	sseProbe, sseProbeErr := probeSSE(ctlSSE.URL)
	ctlStreamable.Close()
	ctlSSE.Close()
	c.check("R0 control: mock streamable endpoint answers an MCP initialize POST",
		streamableProbeErr == nil &&
			strings.Contains(streamableProbe, `"result"`) &&
			strings.Contains(streamableProbe, "aicli-e2e-control-streamable"),
		"probe=%q err=%v", streamableProbe, streamableProbeErr)
	c.check("R0b control: mock SSE endpoint emits the endpoint event",
		sseProbeErr == nil && strings.Contains(sseProbe, "event: endpoint"),
		"probe=%q err=%v", sseProbe, sseProbeErr)

	// --- mock MCP servers under test --------------------------------------
	// Distinct tool names per server so the model-facing tool list below is
	// unambiguous even if the runtime collapses same-named tools.
	const (
		mockToolHTTP = "mock_echo_http"
		mockToolSSE  = "mock_echo_sse"
	)
	httpRec := &recorder{}
	sseRec := &recorder{}
	httpMCP := newMockMCPServer("aicli-e2e-mock-http", mockToolHTTP)
	sseMCP := newMockMCPServer("aicli-e2e-mock-sse", mockToolSSE)

	streamableMock := httptest.NewServer(recordHits(httpRec, mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return httpMCP },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	)))
	defer streamableMock.Close()

	sseMock := httptest.NewServer(recordHits(sseRec, mcp.NewSSEHandler(
		func(*http.Request) *mcp.Server { return sseMCP },
		nil,
	)))
	defer sseMock.Close()

	// Secrets are unique per run so "value absent from logs" cannot be
	// satisfied by accident (e.g. by matching an unrelated string).
	runTag := fmt.Sprintf("%d", time.Now().UnixNano())
	secretAuth := "Bearer mcp-e2e-auth-" + runTag
	secretAPIKey := "mcp-e2e-apikey-" + runTag
	secretSSE := "mcp-e2e-sse-" + runTag
	secrets := []string{secretAuth, secretAPIKey, secretSSE}

	// --- isolated environment ---------------------------------------------
	// Client-provided remote servers are gated by folder trust (P1); the
	// workspace must therefore be TRUSTED here. Folder trust defaults to OFF,
	// which is the trusted state; the env var is set explicitly so a stray
	// parent value cannot flip the gate.
	workspace, err := os.MkdirTemp("", "acp-e2e-mcp-remote")
	if err != nil {
		fmt.Printf("FAIL temp workspace: %v\n", err)
		return 1
	}
	defer os.RemoveAll(workspace)

	home, err := os.MkdirTemp("", "acp-e2e-mcp-remote-home")
	if err != nil {
		fmt.Printf("FAIL temp home: %v\n", err)
		return 1
	}
	defer os.RemoveAll(home)
	aicliDir := filepath.Join(home, ".aicli")
	if err := os.MkdirAll(aicliDir, 0o755); err != nil {
		fmt.Printf("FAIL mkdir .aicli: %v\n", err)
		return 1
	}

	// The mock provider also records every upstream request body, which is the
	// authoritative view of the tool list the model was offered.
	providerRec := &recorder{}
	provider := httptest.NewServer(recordHits(providerRec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	})))
	defer provider.Close()

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
`, provider.URL)
	if err := os.WriteFile(filepath.Join(aicliDir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		fmt.Printf("FAIL write config: %v\n", err)
		return 1
	}

	// --- start the real aicli ACP host ------------------------------------
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "agent", "stdio")
	cmd.Dir = workspace
	cmd.Env = append(os.Environ(),
		"USERPROFILE="+home,
		"HOME="+home,
		"AICLI_FOLDER_TRUST=0",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fmt.Printf("FAIL stdin pipe: %v\n", err)
		return 1
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Printf("FAIL stdout pipe: %v\n", err)
		return 1
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		fmt.Printf("FAIL stderr pipe: %v\n", err)
		return 1
	}
	if err := cmd.Start(); err != nil {
		fmt.Printf("FAIL start: %v\n", err)
		return 1
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

	fatal := func(name string, err error) int {
		c.fail(name, "transport error: %v", err)
		fmt.Println(strings.Join([]string{"", "--- agent stderr tail ---", tailLines(stderrBuf.String(), 25)}, "\n"))
		return c.summary()
	}
	mustCall := func(name, method string, params interface{}, timeout time.Duration) (rpcCall, int, bool) {
		resp, err := e.call(method, params, timeout)
		if err != nil {
			return rpcCall{}, fatal(name, err), false
		}
		return resp, 0, true
	}

	// --- R1: initialize ---------------------------------------------------
	initResp, code, ok := mustCall("R1 initialize", "initialize", map[string]interface{}{
		"protocolVersion":    1,
		"clientCapabilities": map[string]interface{}{"terminal": false},
	}, 30*time.Second)
	if !ok {
		return code
	}
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
	c.check("R1 initialize advertises agentCapabilities.mcpCapabilities.http=true",
		capsHTTP, "mcpCapabilities.http = %v (raw: %s)", capsHTTP, strings.TrimSpace(string(initResp.Result)))
	c.check("R1b initialize advertises agentCapabilities.mcpCapabilities.sse=true",
		capsSSE, "mcpCapabilities.sse = %v (raw: %s)", capsSSE, strings.TrimSpace(string(initResp.Result)))

	// --- R9 baseline: a session with NO client-provided servers ------------
	// The delta against this session isolates the tools that the client-provided
	// remote servers contributed to the session tool surface. It must be created
	// first, while no client server has been connected yet.
	baselineResp, code, ok := mustCall("R9 session/new (baseline, no mcpServers)", "session/new", map[string]interface{}{
		"cwd": workspace,
	}, 60*time.Second)
	if !ok {
		return code
	}
	baselineID := ""
	if baselineResp.Err == nil {
		var snew struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(baselineResp.Result, &snew)
		baselineID = strings.TrimSpace(snew.SessionID)
	}
	if baselineID == "" {
		c.fail("R9 session/new (baseline, no mcpServers)", "no sessionId: result = %s error = %s",
			strings.TrimSpace(string(baselineResp.Result)), baselineResp.errorString())
		return c.summary()
	}
	if _, code, ok := mustCall("R9 session/prompt (baseline)", "session/prompt", map[string]interface{}{
		"sessionId": baselineID,
		"prompt":    []map[string]interface{}{{"type": "text", "text": "reply with ok"}},
	}, 120*time.Second); !ok {
		return code
	}
	// Negative control: before any client-provided server exists, the model must
	// not be offered the mock tools. This makes the positive check below
	// meaningful (the names cannot come from anywhere else).
	baselineRequests := providerRec.count()
	baselineAdvertised := providerRec.bodiesContainingAll(mockToolHTTP) + providerRec.bodiesContainingAll(mockToolSSE)
	baselineToolLog := registeredCountFor(logCorpus(home, workspace, stderrBuf, stdoutBuf), baselineID)
	fmt.Printf("  baseline session %s: %d model request(s), %d advertised a mock MCP tool (late-arrival registration line = %d); model-facing bodies:%s\n",
		baselineID, baselineRequests, baselineAdvertised, baselineToolLog, providerRec.describeBodies())
	c.check("R9 baseline: the baseline session's model request carries no client-provided MCP tool",
		baselineRequests > 0 && baselineAdvertised == 0,
		"provider saw %d request(s), %d advertised %s/%s; want >0 requests and 0 advertised",
		baselineRequests, baselineAdvertised, mockToolHTTP, mockToolSSE)

	// --- R2: session/new with http + sse + unknown transport --------------
	const (
		httpName    = "remote-http-probe"
		sseName     = "remote-sse-probe"
		unknownName = "unknown-transport-probe"
	)
	newResp, code, ok := mustCall("R2 session/new (remote MCP entries)", "session/new", map[string]interface{}{
		"cwd": workspace,
		"mcpServers": []interface{}{
			map[string]interface{}{
				"name": httpName,
				"type": "http",
				"url":  streamableMock.URL,
				"headers": []interface{}{
					map[string]interface{}{"name": "Authorization", "value": secretAuth},
					map[string]interface{}{"name": "X-Api-Key", "value": secretAPIKey},
				},
			},
			map[string]interface{}{
				"name": sseName,
				"type": "sse",
				"url":  sseMock.URL,
				"headers": []interface{}{
					map[string]interface{}{"name": "X-Sse-Token", "value": secretSSE},
				},
			},
			map[string]interface{}{
				"name": unknownName,
				"type": "websocket",
				"url":  "http://127.0.0.1:1/never-contacted",
			},
		},
	}, 90*time.Second)
	if !ok {
		return code
	}
	sessionID := ""
	if newResp.Err == nil {
		var snew struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(newResp.Result, &snew)
		sessionID = strings.TrimSpace(snew.SessionID)
	}
	c.check("R2 session/new returns a sessionId with http+sse+unknown entries",
		newResp.Err == nil && sessionID != "",
		"result = %s error = %s, want a non-empty sessionId with no error",
		strings.TrimSpace(string(newResp.Result)), newResp.errorString())
	if sessionID == "" {
		fmt.Println(strings.Join([]string{"", "--- agent stderr tail ---", tailLines(stderrBuf.String(), 25)}, "\n"))
		return c.summary()
	}

	// --- R3/R4/R5: wait for the handshake on both endpoints ---------------
	deadline := time.Now().Add(20 * time.Second)
	httpReady, sseReady := false, false
	for time.Now().Before(deadline) {
		httpReady = httpRec.sawJSONRPCMethod("initialize") && httpRec.sawJSONRPCMethod("tools/list")
		sseReady = sseRec.sawJSONRPCMethod("initialize") && sseRec.sawJSONRPCMethod("tools/list")
		if httpReady && sseReady {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	c.check("R3 streamable endpoint completed the MCP handshake (initialize + tools/list POSTs)",
		httpReady, "streamable mock transcript:%s", httpRec.describe())
	c.check("R3b SSE endpoint completed the MCP handshake (initialize + tools/list over the SSE session)",
		sseReady, "SSE mock transcript:%s", sseRec.describe())

	c.check("R4 streamable endpoint received the downloaded Authorization header verbatim",
		httpRec.headerValueSeen("Authorization", secretAuth),
		"no request carried Authorization=<run secret>; transcript:%s", httpRec.describe())
	c.check("R4b streamable endpoint received the downloaded X-Api-Key header verbatim",
		httpRec.headerValueSeen("X-Api-Key", secretAPIKey),
		"no request carried X-Api-Key=<run secret>; transcript:%s", httpRec.describe())
	c.check("R5 SSE stream GET carried the downloaded X-Sse-Token header verbatim",
		sseRec.headerValueSeenForMethod(http.MethodGet, "X-Sse-Token", secretSSE),
		"no GET carried X-Sse-Token=<run secret>; transcript:%s", sseRec.describe())
	c.check("R5b SSE message POST carried the downloaded X-Sse-Token header verbatim",
		sseRec.headerValueSeenForMethod(http.MethodPost, "X-Sse-Token", secretSSE),
		"no POST carried X-Sse-Token=<run secret>; transcript:%s", sseRec.describe())

	// --- R6: type "http" is really driven over the streamable transport ---
	// Streamable HTTP = JSON-RPC over POST (Accept: application/json,
	// text/event-stream). The legacy SSE transport = one long-lived GET stream
	// plus message POSTs. Seeing POSTs and zero GETs proves the http entry was
	// mapped onto the streamable client, not onto SSE.
	streamablePOSTs := httpRec.countMethod(http.MethodPost)
	streamableGETs := httpRec.countMethod(http.MethodGet)
	streamableAcceptOK := false
	for _, h := range httpRec.snapshot() {
		if h.method == http.MethodPost && strings.Contains(h.header.Get("Accept"), "text/event-stream") {
			streamableAcceptOK = true
			break
		}
	}
	c.check("R6 http entry is mapped onto the streamable transport (JSON-RPC POSTs, no SSE GET)",
		streamablePOSTs > 0 && streamableGETs == 0,
		"streamable endpoint saw %d POST(s) and %d GET(s), want POSTs>0 and GETs==0; transcript:%s",
		streamablePOSTs, streamableGETs, httpRec.describe())
	c.check("R6b streamable POSTs negotiate the Streamable HTTP content type (Accept includes text/event-stream)",
		streamableAcceptOK, "no POST carried Accept: text/event-stream; transcript:%s", httpRec.describe())

	// --- R7: audit log carries the credential KEY NAMES only --------------
	corpus := logCorpus(home, workspace, stderrBuf, stdoutBuf)
	auditHTTP := fmt.Sprintf("server=%s transport=http url=%s header_keys=Authorization,X-Api-Key", httpName, streamableMock.URL)
	auditSSE := fmt.Sprintf("server=%s transport=sse url=%s header_keys=X-Sse-Token", sseName, sseMock.URL)
	c.check("R7 acp.mcp.audit records transport=http + url + sorted header key names for the http entry",
		strings.Contains(corpus, auditHTTP),
		"log corpus is missing %q", auditHTTP)
	c.check("R7b acp.mcp.audit records transport=sse + url + header key names for the sse entry",
		strings.Contains(corpus, auditSSE),
		"log corpus is missing %q", auditSSE)

	// --- R8: unknown transport skipped per-entry --------------------------
	unknownRe := regexp.MustCompile(`skipped client MCP entry: mcpServers\[\d+\] \(name="` + regexp.QuoteMeta(unknownName) + `"\): unsupported transport type "websocket"`)
	c.check("R8 unknown transport type is skipped per-entry with a diagnostic naming the entry",
		unknownRe.MatchString(corpus),
		"log corpus is missing the per-entry skip diagnostic for %q; tail:\n%s",
		unknownName, tailLines(stderrBuf.String(), 15))

	// --- R9: the connected servers reach the session tool surface ---------
	promptResp, code, ok := mustCall("R9 session/prompt (main session, first turn)", "session/prompt", map[string]interface{}{
		"sessionId": sessionID,
		"prompt":    []map[string]interface{}{{"type": "text", "text": "reply with ok"}},
	}, 120*time.Second)
	if !ok {
		return code
	}
	corpus = logCorpus(home, workspace, stderrBuf, stdoutBuf)
	loaded := toolsLoadedLines(corpus)
	fmt.Printf("  AICLI tools loaded lines: %d (informational: late-arrival registration line = %d)\n",
		len(loaded), registeredCountFor(corpus, sessionID))
	for i, line := range loaded {
		short := line
		if len(short) > 260 {
			short = short[:260] + "..."
		}
		fmt.Printf("    [%d] has %s=%v has %s=%v: %s\n",
			i, mockToolHTTP, strings.Contains(line, mockToolHTTP),
			mockToolSSE, strings.Contains(line, mockToolSSE), short)
	}
	c.check("R9b first prompt offers the streamable (http) server's tool to the model",
		promptResp.Err == nil && providerRec.bodiesContainingAll(mockToolHTTP) >= 1,
		"prompt error=%s; %d of %d model request(s) advertised %s (want >=1); model-facing bodies:%s",
		promptResp.errorString(), providerRec.bodiesContainingAll(mockToolHTTP), providerRec.count(),
		mockToolHTTP, providerRec.describeBodies())

	// --- R9c: the SSE tool arrives late; the next turn must pick it up ------
	// The first-prompt readiness wait is bounded (acpMCPFirstPromptReadyTimeout)
	// and tools that arrive afterwards are supposed to be registered at the next
	// turn boundary by acpSessionMCP.prepareForPrompt -> refreshTools.
	secondResp, code, ok := mustCall("R9c session/prompt (main session, second turn)", "session/prompt", map[string]interface{}{
		"sessionId": sessionID,
		"prompt":    []map[string]interface{}{{"type": "text", "text": "reply with ok again"}},
	}, 120*time.Second)
	if !ok {
		return code
	}
	corpus = logCorpus(home, workspace, stderrBuf, stdoutBuf)
	sseAdvertised := providerRec.bodiesContainingAll(mockToolSSE)
	lateRegistered := registeredCountFor(corpus, sessionID)
	fmt.Printf("  after the second prompt: model request(s)=%d, containing %s=%d, late-arrival registration line=%d; model-facing bodies:%s\n",
		providerRec.count(), mockToolSSE, sseAdvertised, lateRegistered, providerRec.describeBodies())
	c.check("R9c second turn offers the late-arriving SSE server's tool to the model",
		secondResp.Err == nil && sseAdvertised >= 1,
		"prompt error=%s; %d of %d model request(s) advertised %s (want >=1); late-arrival registration line=%d; model-facing bodies:%s",
		secondResp.errorString(), sseAdvertised, providerRec.count(), mockToolSSE, lateRegistered,
		providerRec.describeBodies())

	// --- R10: credentials never reach the logs ----------------------------
	for _, secret := range secrets {
		c.check("R10 credential value is absent from every log artifact ("+secretLabel(secret, secretAuth, secretAPIKey, secretSSE)+")",
			!strings.Contains(corpus, secret),
			"a downloaded credential VALUE leaked into the logs; search string present: %s", secretLabel(secret, secretAuth, secretAPIKey, secretSSE))
	}

	// --- R11: session/close ------------------------------------------------
	closeResp, code, ok := mustCall("R11 session/close", "session/close", map[string]interface{}{
		"sessionId": sessionID,
	}, 30*time.Second)
	if !ok {
		return code
	}
	c.check("R11 session/close succeeds after remote MCP servers were connected",
		closeResp.Err == nil, "result = %s error = %s, want {} with no error",
		strings.TrimSpace(string(closeResp.Result)), closeResp.errorString())

	// close the baseline session too (keeps the host tidy before shutdown)
	if _, _, ok := mustCall("R11b session/close (baseline)", "session/close", map[string]interface{}{
		"sessionId": baselineID,
	}, 30*time.Second); !ok {
		return code
	}

	// final corpus sweep, now that close has flushed its logs too
	corpus = logCorpus(home, workspace, stderrBuf, stdoutBuf)
	for _, secret := range secrets {
		c.check("R10b credential value still absent after session/close ("+secretLabel(secret, secretAuth, secretAPIKey, secretSSE)+")",
			!strings.Contains(corpus, secret),
			"a downloaded credential VALUE leaked into the logs; search string present: %s", secretLabel(secret, secretAuth, secretAPIKey, secretSSE))
	}

	return c.summary()
}

// secretLabel names a credential without printing its value.
func secretLabel(secret, auth, apiKey, sse string) string {
	switch secret {
	case auth:
		return "Authorization"
	case apiKey:
		return "X-Api-Key"
	case sse:
		return "X-Sse-Token"
	default:
		return "unknown"
	}
}
