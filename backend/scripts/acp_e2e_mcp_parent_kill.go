// Real end-to-end ACP verification of plan §7.4 item 5 / R7: when the ACP client
// hard-kills the agent process (no graceful shutdown, no process-tree flag), the
// stdio MCP child must not be left orphaned.
//
// Mechanism under test: StdioTransport binds every stdio MCP command to a
// ProcessGuard. On Windows that guard is a Job Object created with
// KILL_ON_JOB_CLOSE (internal/executor/process_guard_windows.go), so killing the
// agent closes the job handle and the OS tears the child down. On Unix the guard
// is only a process group killed on the graceful path, so an orphan there is the
// expected platform limitation (reported as DIAG, not as a failure).
//
// Flow:
//  1. build aicli + the stdio MCP helper
//  2. isolated HOME (mock provider, folder trust ON, workspace trusted)
//  3. initialize + session/new with a client-supplied stdio server; the helper
//     publishes its pid through -pidfile
//  4. session/prompt -> the tool must be callable (the child is really in use)
//  5. hard-kill ONLY the agent process (taskkill /F /PID, or kill -9): nothing
//     runs the graceful Terminate() path
//  6. the MCP child must be gone within the reap window (Windows contract)
//
// Run (from backend/):
//
//	go run ./scripts/acp_e2e_mcp_parent_kill.go [aicli.exe]
//
// Fully offline: the MCP server is stdio and the provider is a local httptest
// mock, so no LIVE_MCP_TEST-style gate is required.

//go:build ignore

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// killTool is the MCP tool exposed by the helper for this scenario. The name
	// is unique on purpose: a name shared with the built-in toolkit is shadowed.
	killTool = "acp_e2e_kill_echo"
	// killServerName is the key of the client-supplied server on session/new.
	killServerName = "client-kill-e2e"
	// probeSentinel marks the user turn that must be answered with a tool call.
	probeSentinel = "RUN_MCP_TOOL_PROBE"
	// maxPromptAttempts bounds the wait for a late (async-connected) MCP tool.
	maxPromptAttempts = 3
)

// Message is one NDJSON JSON-RPC frame.
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

func (e rpcError) String() string {
	return fmt.Sprintf("code=%d message=%q", e.Code, e.Message)
}

type inbound struct {
	msg  Message
	line string
}

// logTail keeps the last lines of a stream for failure diagnostics.
type logTail struct {
	mu    sync.Mutex
	items []string
}

func (l *logTail) add(line string) {
	if l == nil || strings.TrimSpace(line) == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, strings.TrimSpace(line))
	if len(l.items) > 400 {
		l.items = l.items[len(l.items)-400:]
	}
}

func (l *logTail) dump(limit int) string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit <= 0 || limit > len(l.items) {
		limit = len(l.items)
	}
	return strings.Join(l.items[len(l.items)-limit:], "\n")
}

// capture records upstream provider request bodies in arrival order.
type capture struct {
	mu    sync.Mutex
	items []string
}

func (c *capture) add(raw string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = append(c.items, raw)
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// since returns the bodies captured after index n.
func (c *capture) since(n int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n < 0 || n > len(c.items) {
		n = len(c.items)
	}
	out := make([]string, len(c.items)-n)
	copy(out, c.items[n:])
	return out
}

// agent drives one `aicli agent stdio` process over NDJSON JSON-RPC.
type agent struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	frames  *logTail
	stderr  *logTail
	done    chan struct{}
	waitErr error

	mu      sync.Mutex
	nextID  int
	pending map[string]chan inbound
	updates []map[string]interface{}
}

func startAgent(bin string, env []string, dir string, extraArgs ...string) (*agent, error) {
	args := append([]string{"agent", "stdio"}, extraArgs...)
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Dir = dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	a := &agent{
		cmd:     cmd,
		stdin:   stdin,
		frames:  &logTail{},
		stderr:  &logTail{},
		done:    make(chan struct{}),
		pending: map[string]chan inbound{},
	}
	go a.readLoop(stdout)
	go func() {
		reader := bufio.NewReader(stderr)
		for {
			line, err := reader.ReadString('\n')
			if strings.TrimSpace(line) != "" {
				a.stderr.add(line)
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		a.waitErr = cmd.Wait()
		close(a.done)
	}()
	return a, nil
}

func (a *agent) readLoop(stdout io.Reader) {
	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadString('\n')
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			a.frames.add(trimmed)
			a.dispatch(trimmed)
		}
		if err != nil {
			return
		}
	}
}

func (a *agent) dispatch(line string) {
	var msg Message
	if json.Unmarshal([]byte(line), &msg) != nil {
		return
	}
	switch {
	case msg.ID == nil && msg.Method == "session/update":
		var params map[string]interface{}
		if json.Unmarshal(msg.Params, &params) == nil {
			a.mu.Lock()
			a.updates = append(a.updates, params)
			a.mu.Unlock()
		}
	case msg.ID != nil && msg.Method == "session/request_permission":
		a.answerPermission(msg)
	case msg.ID != nil:
		key := string(msg.ID)
		a.mu.Lock()
		ch := a.pending[key]
		delete(a.pending, key)
		a.mu.Unlock()
		if ch != nil {
			ch <- inbound{msg: msg, line: line}
		}
	}
}

// answerPermission auto-approves tool execution so the probe turn can complete
// without a human in the loop.
func (a *agent) answerPermission(msg Message) {
	var params struct {
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	_ = json.Unmarshal(msg.Params, &params)
	chosen := ""
	for _, kind := range []string{"allow_always", "allow_once"} {
		for _, opt := range params.Options {
			if opt.Kind == kind {
				chosen = opt.OptionID
				break
			}
		}
		if chosen != "" {
			break
		}
	}
	if chosen == "" && len(params.Options) > 0 {
		chosen = params.Options[0].OptionID
	}
	result, _ := json.Marshal(map[string]interface{}{
		"outcome": map[string]interface{}{"outcome": "selected", "optionId": chosen},
	})
	_ = a.send(Message{JSONRPC: "2.0", ID: msg.ID, Result: result})
}

func (a *agent) send(msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, err = a.stdin.Write(append(data, '\n'))
	return err
}

func (a *agent) call(method string, params interface{}, timeout time.Duration) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.nextID++
	id := strconv.Itoa(a.nextID)
	ch := make(chan inbound, 1)
	a.pending[id] = ch
	a.mu.Unlock()

	if err := a.send(Message{JSONRPC: "2.0", ID: json.RawMessage(id), Method: method, Params: raw}); err != nil {
		return nil, err
	}
	select {
	case in := <-ch:
		if in.msg.Error != nil {
			var rpcErr rpcError
			_ = json.Unmarshal(in.msg.Error, &rpcErr)
			return nil, fmt.Errorf("%s: %s", method, rpcErr.String())
		}
		return in.msg.Result, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout after %s waiting for %s\nframes:\n%s\nstderr:\n%s",
			timeout, method, a.frames.dump(8), a.stderr.dump(12))
	}
}

func (a *agent) stop() {
	if a == nil {
		return
	}
	_ = a.stdin.Close()
	_ = a.cmd.Process.Kill()
	select {
	case <-a.done:
	case <-time.After(10 * time.Second):
	}
}

// --- mock provider -----------------------------------------------------------

func sseChunk(delta map[string]interface{}, finish interface{}) string {
	payload := map[string]interface{}{
		"id":      "mock",
		"object":  "chat.completion.chunk",
		"created": 1,
		"model":   "m1",
		"choices": []map[string]interface{}{{
			"index":         0,
			"delta":         delta,
			"finish_reason": finish,
		}},
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

// mockProvider answers the probe turn with a tool call when (and only when) the
// helper tool is present in the request tool surface; every other request gets
// plain text. That makes "tool visible" observable from the upstream payload.
type mockProvider struct {
	captured *capture
	sentinel string
	tool     string
}

func (m *mockProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	m.captured.add(string(raw))
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	emit := func(payload string) {
		fmt.Fprintf(w, "data: %s\n\n", payload)
		if flusher != nil {
			flusher.Flush()
		}
	}
	emit(sseChunk(map[string]interface{}{"role": "assistant"}, nil))

	if !strings.Contains(string(raw), m.sentinel) || hasToolRoleMessage(string(raw)) {
		emit(sseChunk(map[string]interface{}{"content": "plain-answer"}, nil))
		emit(sseChunk(map[string]interface{}{}, "stop"))
		fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	name, ok := pickToolForCall(string(raw), m.tool)
	if !ok {
		emit(sseChunk(map[string]interface{}{"content": "mcp-tool-not-in-surface"}, nil))
		emit(sseChunk(map[string]interface{}{}, "stop"))
		fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	args, _ := json.Marshal(map[string]interface{}{"text": m.sentinel})
	emit(sseChunk(map[string]interface{}{"tool_calls": []map[string]interface{}{{
		"index": 0,
		"id":    "call_mcp_1",
		"type":  "function",
		"function": map[string]interface{}{
			"name":      name,
			"arguments": string(args),
		},
	}}}, nil))
	emit(sseChunk(map[string]interface{}{}, "tool_calls"))
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// hasToolRoleMessage reports whether the request already carries a tool result
// (i.e. it is the follow-up turn after tool execution).
func hasToolRoleMessage(raw string) bool {
	var payload struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return false
	}
	for _, msg := range payload.Messages {
		if strings.EqualFold(strings.TrimSpace(msg.Role), "tool") {
			return true
		}
	}
	return false
}

// requestToolNames lists the tool names advertised in the upstream request.
func requestToolNames(raw string) []string {
	var payload struct {
		Tools []map[string]interface{} `json:"tools"`
	}
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return nil
	}
	names := make([]string, 0, len(payload.Tools))
	for _, tool := range payload.Tools {
		name := ""
		if fn, ok := tool["function"].(map[string]interface{}); ok {
			name, _ = fn["name"].(string)
		}
		if strings.TrimSpace(name) == "" {
			name, _ = tool["name"].(string)
		}
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			names = append(names, trimmed)
		}
	}
	sort.Strings(names)
	return names
}

// matchesToolName accepts both the raw MCP tool name and the canonical
// mcp__<server>__<tool> form used on name conflicts.
func matchesToolName(name, want string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	target := strings.ToLower(strings.TrimSpace(want))
	if trimmed == "" || target == "" {
		return false
	}
	return trimmed == target || strings.HasSuffix(trimmed, "__"+target)
}

// pickToolForCall returns the name the model should call this turn.
func pickToolForCall(raw, want string) (string, bool) {
	for _, name := range requestToolNames(raw) {
		if matchesToolName(name, want) {
			return name, true
		}
	}
	return "", false
}

// --- process / file helpers --------------------------------------------------

func exeName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func goBuild(dir, out string, pkg string) error {
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = dir
	combined, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build %s: %v\n%s", pkg, err, string(combined))
	}
	return nil
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func yamlQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// findProbeRequest returns the first captured body belonging to the probe turn.
func findProbeRequest(bodies []string) string {
	for _, body := range bodies {
		if strings.Contains(body, probeSentinel) {
			return body
		}
	}
	return ""
}

// fileState reports whether a probe file exists (and its trimmed content).
func fileState(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "absent"
	}
	return "present(" + strings.TrimSpace(string(data)) + ")"
}

// logDigest prints the agent log lines that matter for MCP/tool-surface
// diagnosis (the process logs to a file, not to stderr).
func logDigest(path string, limit int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("<no agent log: %v>", err)
	}
	keywords := []string{"acp.mcp", "mcp", "MCP", "tools loaded", "tool surface", "tool_surface", "surface"}
	var hits []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		for _, keyword := range keywords {
			if strings.Contains(trimmed, keyword) {
				hits = append(hits, trimmed)
				break
			}
		}
	}
	if len(hits) > limit {
		hits = hits[len(hits)-limit:]
	}
	return strings.Join(hits, "\n")
}

// waitForPID polls pidPath until the helper published its pid.
func waitForPID(path string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 {
				return pid, nil
			} else {
				lastErr = fmt.Errorf("unreadable pid file %s: %v (%q)", path, convErr, strings.TrimSpace(string(data)))
			}
		} else {
			lastErr = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return 0, fmt.Errorf("no pid file after %s: %v", timeout, lastErr)
}

// processAlive reports whether pid is still running (Windows: tasklist probe).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH").Output()
		if err != nil {
			return false
		}
		return strings.Contains(string(out), fmt.Sprintf("\"%d\"", pid))
	}
	return exec.Command("kill", "-0", strconv.Itoa(pid)).Run() == nil
}

// hardKill terminates exactly one process, deliberately WITHOUT any process-tree
// flag: the point of this scenario is that no descendant cleanup runs.
func hardKill(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("hardKill: invalid pid %d", pid)
	}
	if runtime.GOOS == "windows" {
		out, err := exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).CombinedOutput()
		if err != nil {
			return fmt.Errorf("taskkill /F /PID %d: %v (%s)", pid, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	out, err := exec.Command("kill", "-9", strconv.Itoa(pid)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("kill -9 %d: %v (%s)", pid, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func run() error {
	backendDir, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(backendDir, "cmd", "aicli")); err != nil {
		return fmt.Errorf("run this script from backend/ (cmd/aicli not found in %s)", backendDir)
	}

	tmpRoot, err := os.MkdirTemp("", "acp-e2e-mcp-kill")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpRoot)

	bin := ""
	if len(os.Args) > 1 && strings.TrimSpace(os.Args[1]) != "" {
		bin, err = filepath.Abs(os.Args[1])
		if err != nil {
			return err
		}
	} else {
		bin = filepath.Join(tmpRoot, exeName("aicli"))
		if err := goBuild(backendDir, bin, "./cmd/aicli"); err != nil {
			return err
		}
	}
	helper := filepath.Join(tmpRoot, exeName("acp_e2e_mcp_helper_kill"))
	if err := goBuild(backendDir, helper, "./scripts/acp_e2e_mcp_stdio_helper.go"); err != nil {
		return err
	}
	fmt.Printf("OK built aicli=%s helper=%s\n", bin, helper)

	home := filepath.Join(tmpRoot, "home")
	workspace := filepath.Join(tmpRoot, "workspace")
	for _, dir := range []string{filepath.Join(home, ".aicli"), workspace} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	markerPath := filepath.Join(tmpRoot, "kill-server-started.marker")
	pidPath := filepath.Join(tmpRoot, "kill-server.pid")
	agentLog := filepath.Join(tmpRoot, "agent.log")

	trustYAML := fmt.Sprintf("version: 1\nfolders:\n  %s:\n    trusted: true\n", yamlQuote(workspace))
	if err := writeFile(filepath.Join(home, ".aicli", "trusted_folders.yaml"), trustYAML); err != nil {
		return err
	}

	captured := &capture{}
	srv := httptest.NewServer(&mockProvider{captured: captured, sentinel: probeSentinel, tool: killTool})
	defer srv.Close()

	configYAML := fmt.Sprintf(`log:
  level: debug
  file_path: %s
  enabled: true
aicli:
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
`, yamlQuote(agentLog), srv.URL)
	if err := writeFile(filepath.Join(home, ".aicli", "config.yaml"), configYAML); err != nil {
		return err
	}

	env := append(os.Environ(),
		"USERPROFILE="+home,
		"HOME="+home,
		"AICLI_HOME="+filepath.Join(home, ".aicli"),
		"AICLI_FOLDER_TRUST=1",
	)
	fmt.Printf("OK isolated HOME=%s workspace=%s (trusted via trusted_folders.yaml)\n", home, workspace)

	a, err := startAgent(bin, env, workspace, "--logfile", agentLog)
	if err != nil {
		return fmt.Errorf("start aicli agent stdio: %w", err)
	}
	defer a.stop()

	agentPID := a.cmd.Process.Pid

	if _, err := a.call("initialize", map[string]interface{}{
		"protocolVersion":    1,
		"clientCapabilities": map[string]interface{}{"terminal": false},
	}, 30*time.Second); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	fmt.Println("OK initialize")

	servers := []interface{}{map[string]interface{}{
		"name":    killServerName,
		"type":    "stdio",
		"command": helper,
		"args": []string{
			"-tool", killTool,
			"-marker", markerPath,
			"-pidfile", pidPath,
		},
	}}
	res, err := a.call("session/new", map[string]interface{}{
		"cwd":        workspace,
		"mcpServers": servers,
	}, 90*time.Second)
	if err != nil {
		return fmt.Errorf("session/new with a client-supplied stdio server: %w\nagent stderr:\n%s", err, a.stderr.dump(25))
	}
	var snew struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &snew); err != nil || strings.TrimSpace(snew.SessionID) == "" {
		return fmt.Errorf("session/new result: %v %s", err, res)
	}
	sessionID := strings.TrimSpace(snew.SessionID)
	fmt.Printf("OK session/new sessionId=%s\n", sessionID)

	pid, pidErr := waitForPID(pidPath, 20*time.Second)
	if pidErr != nil {
		return fmt.Errorf("client-supplied stdio server was not started: %v\nmarker=%s pid=%s\nagent stderr:\n%s\nagent log (mcp/tool lines):\n%s",
			pidErr, fileState(markerPath), fileState(pidPath), a.stderr.dump(25), logDigest(agentLog, 40))
	}
	fmt.Printf("OK client-supplied stdio server started (pid=%d marker=%s)\n", pid, fileState(markerPath))

	// One probe turn so the server is not merely spawned but really in use.
	probeUsed := false
	for attempt := 1; attempt <= maxPromptAttempts && !probeUsed; attempt++ {
		before := captured.count()
		if _, err := a.call("session/prompt", map[string]interface{}{
			"sessionId": sessionID,
			"prompt": []map[string]interface{}{
				{"type": "text", "text": fmt.Sprintf("%s attempt=%d", probeSentinel, attempt)},
			},
		}, 120*time.Second); err != nil {
			return fmt.Errorf("session/prompt #%d: %w", attempt, err)
		}
		requests := captured.since(before)
		probe := findProbeRequest(requests)
		if probe == "" {
			return fmt.Errorf("prompt #%d produced no upstream request carrying %q (upstream bodies=%d)\nagent stderr:\n%s",
				attempt, probeSentinel, len(requests), a.stderr.dump(20))
		}
		for _, body := range requests {
			if strings.Contains(body, "echo:"+probeSentinel) {
				probeUsed = true
				break
			}
		}
		fmt.Printf("OK prompt #%d upstream_tools=%d callable=%v\n", attempt, len(requestToolNames(probe)), probeUsed)
	}
	if !probeUsed {
		return fmt.Errorf("client-supplied MCP tool %q never became callable within %d prompts; the kill scenario would be vacuous\nagent stderr:\n%s",
			killTool, maxPromptAttempts, a.stderr.dump(25))
	}

	// Preconditions: both the agent and the MCP child are alive.
	if !processAlive(agentPID) {
		return fmt.Errorf("agent pid=%d is not alive before the kill", agentPID)
	}
	if !processAlive(pid) {
		return fmt.Errorf("MCP child pid=%d is not alive before the kill; the scenario cannot prove anything", pid)
	}
	fmt.Printf("DIAG before kill: agent-alive=%v mcp-child-alive=%v\n", true, true)

	// Hard-kill ONLY the agent process (no /T): this is what a client crash or
	// Zed's Drop -> child.kill() looks like. Nothing runs the graceful
	// Terminate() path, so the MCP child survives only if the OS-level guard
	// (Windows Job Object with KILL_ON_JOB_CLOSE) fails to tear it down.
	killStart := time.Now()
	if err := hardKill(agentPID); err != nil {
		return err
	}
	select {
	case <-a.done:
	case <-time.After(20 * time.Second):
		return fmt.Errorf("agent pid=%d did not exit after the hard kill", agentPID)
	}
	fmt.Printf("OK agent hard-killed (pid=%d, no process-tree flag)\n", agentPID)

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && processAlive(pid) {
		time.Sleep(100 * time.Millisecond)
	}
	alive := processAlive(pid)
	elapsed := time.Since(killStart).Round(100 * time.Millisecond)

	if runtime.GOOS == "windows" {
		if alive {
			// Best-effort cleanup so the machine is not left with an orphan.
			_ = hardKill(pid)
			return fmt.Errorf("MCP child pid=%d survived the agent hard kill (Windows Job Object with KILL_ON_JOB_CLOSE must tear the tree down)\nagent log (mcp/tool lines):\n%s",
				pid, logDigest(agentLog, 40))
		}
		fmt.Printf("OK MCP child pid=%d reaped by the Job Object after the agent hard kill (elapsed=%s)\n", pid, elapsed)
		fmt.Println("E2E PASS acp_e2e_mcp_parent_kill (windows: job_object KILL_ON_JOB_CLOSE)")
		return nil
	}

	// Unix: the guard is a process group killed at Terminate() time; there is no
	// parent-death signal, so an orphan here is an expected platform limitation
	// rather than a regression. Report it and clean up.
	if alive {
		_ = hardKill(pid)
		fmt.Printf("DIAG MCP child pid=%d outlived the agent hard kill (elapsed=%s) and was reaped by the probe: Unix has no parent-death guarantee (process-group kill only runs on the graceful path)\n", pid, elapsed)
		fmt.Println("E2E PASS acp_e2e_mcp_parent_kill (unix: orphan expected, documented limitation)")
		return nil
	}
	fmt.Printf("OK MCP child pid=%d died with the agent (elapsed=%s)\n", pid, elapsed)
	fmt.Println("E2E PASS acp_e2e_mcp_parent_kill (unix: child did not outlive the agent)")
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "\nFAIL acp_e2e_mcp_parent_kill: %v\n", err)
		os.Exit(1)
	}
}
