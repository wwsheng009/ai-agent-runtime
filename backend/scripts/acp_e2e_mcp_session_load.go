// Real end-to-end ACP verification of plan §7.4 item 4: a client-supplied stdio
// MCP server must be assembled the same way on session/load as on session/new.
// Both entry points funnel through withACPMCPClientPlan(ctx, plan) ->
// bootstrapSessionWithIDLocked (agent_stdio.go), so the observable contract is:
// server started, tool visible at the FIRST prompt after load, tool callable.
//
// Flow:
//  1. build aicli + the stdio MCP helper
//  2. isolated HOME (mock provider, folder trust ON, workspace trusted)
//  3. initialize + session/new WITHOUT mcpServers -> session S (nothing spawned)
//  4. session/prompt #1 (plain answer), then session/close: close keeps history
//     but detaches the live session, so session/load takes the durable path
//     (agent_stdio.go LoadSession: existing == nil -> bootstrap from store)
//  5. session/load { sessionId: S, cwd, mcpServers: [stdio helper] }
//     - the helper must really be spawned (marker + pid file)
//  6. session/prompt -> the tool must be in the FIRST prompt tool surface and
//     its echo result must reach the provider (callable)
//  7. session/close -> the helper process must be reaped
//
// Run (from backend/):
//
//	go run ./scripts/acp_e2e_mcp_session_load.go [aicli.exe]
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
	// loadTool is the MCP tool exposed by the helper for this scenario. The name
	// is unique on purpose: a name shared with the built-in toolkit is shadowed.
	loadTool = "acp_e2e_load_echo"
	// loadServerName is the key of the client-supplied server on session/load.
	loadServerName = "client-load-e2e"
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

func run() error {
	backendDir, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(backendDir, "cmd", "aicli")); err != nil {
		return fmt.Errorf("run this script from backend/ (cmd/aicli not found in %s)", backendDir)
	}

	tmpRoot, err := os.MkdirTemp("", "acp-e2e-mcp-load")
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
	helper := filepath.Join(tmpRoot, exeName("acp_e2e_mcp_helper_load"))
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
	markerPath := filepath.Join(tmpRoot, "load-server-started.marker")
	pidPath := filepath.Join(tmpRoot, "load-server.pid")
	agentLog := filepath.Join(tmpRoot, "agent.log")

	// Trusted workspace: folder trust is ON and the isolated HOME carries an
	// explicit trust record, so the client-supplied server passes the gate.
	trustYAML := fmt.Sprintf("version: 1\nfolders:\n  %s:\n    trusted: true\n", yamlQuote(workspace))
	if err := writeFile(filepath.Join(home, ".aicli", "trusted_folders.yaml"), trustYAML); err != nil {
		return err
	}

	captured := &capture{}
	srv := httptest.NewServer(&mockProvider{captured: captured, sentinel: probeSentinel, tool: loadTool})
	defer srv.Close()

	// No local mcp.yaml on purpose: this script isolates the client-supplied path.
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

	if _, err := a.call("initialize", map[string]interface{}{
		"protocolVersion":    1,
		"clientCapabilities": map[string]interface{}{"terminal": false},
	}, 30*time.Second); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	fmt.Println("OK initialize")

	// --- session/new WITHOUT mcpServers: nothing may be spawned yet ---
	res, err := a.call("session/new", map[string]interface{}{"cwd": workspace}, 90*time.Second)
	if err != nil {
		return fmt.Errorf("session/new (no mcpServers): %w\nagent stderr:\n%s", err, a.stderr.dump(25))
	}
	var snew struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &snew); err != nil || strings.TrimSpace(snew.SessionID) == "" {
		return fmt.Errorf("session/new result: %v %s", err, res)
	}
	sessionID := strings.TrimSpace(snew.SessionID)
	fmt.Printf("OK session/new (no mcpServers) sessionId=%s\n", sessionID)

	// One warm-up turn so the durable session has history before close.
	if _, err := a.call("session/prompt", map[string]interface{}{
		"sessionId": sessionID,
		"prompt":    []map[string]interface{}{{"type": "text", "text": "warm-up before load"}},
	}, 120*time.Second); err != nil {
		return fmt.Errorf("session/prompt (warm-up): %w\nagent stderr:\n%s", err, a.stderr.dump(25))
	}

	if _, err := a.call("session/close", map[string]interface{}{"sessionId": sessionID}, 60*time.Second); err != nil {
		return fmt.Errorf("session/close: %w\nagent stderr:\n%s", err, a.stderr.dump(25))
	}
	if state := fileState(pidPath); state != "absent" {
		return fmt.Errorf("session/new without mcpServers must not spawn a server, but pid file is %s", state)
	}
	fmt.Println("OK session/close returned; no server was spawned by session/new")

	// --- session/load WITH the client-supplied stdio server ---
	servers := []interface{}{map[string]interface{}{
		"name":    loadServerName,
		"type":    "stdio",
		"command": helper,
		"args": []string{
			"-tool", loadTool,
			"-marker", markerPath,
			"-pidfile", pidPath,
		},
	}}
	if _, err := a.call("session/load", map[string]interface{}{
		"sessionId":  sessionID,
		"cwd":        workspace,
		"mcpServers": servers,
	}, 120*time.Second); err != nil {
		return fmt.Errorf("session/load with a client-supplied stdio server: %w\nagent stderr:\n%s\nagent log (mcp/tool lines):\n%s",
			err, a.stderr.dump(25), logDigest(agentLog, 40))
	}
	fmt.Println("OK session/load returned")

	pid, pidErr := waitForPID(pidPath, 20*time.Second)
	if pidErr != nil {
		return fmt.Errorf("session/load did NOT assemble the client-supplied server: %v\nmarker=%s pid=%s\nagent stderr:\n%s\nagent log (mcp/tool lines):\n%s",
			pidErr, fileState(markerPath), fileState(pidPath), a.stderr.dump(25), logDigest(agentLog, 40))
	}
	fmt.Printf("OK session/load assembled the client-supplied server (pid=%d marker=%s)\n", pid, fileState(markerPath))

	firstVisible := 0
	callable := false
	var lastTools []string
	for attempt := 1; attempt <= maxPromptAttempts; attempt++ {
		before := captured.count()
		if _, err := a.call("session/prompt", map[string]interface{}{
			"sessionId": sessionID,
			"prompt": []map[string]interface{}{
				{"type": "text", "text": fmt.Sprintf("%s attempt=%d", probeSentinel, attempt)},
			},
		}, 120*time.Second); err != nil {
			return fmt.Errorf("session/prompt #%d after load: %w", attempt, err)
		}
		requests := captured.since(before)
		probe := findProbeRequest(requests)
		if probe == "" {
			return fmt.Errorf("prompt #%d after load produced no upstream request carrying %q (upstream bodies=%d)\nagent stderr:\n%s",
				attempt, probeSentinel, len(requests), a.stderr.dump(20))
		}
		lastTools = requestToolNames(probe)
		visible := false
		for _, name := range lastTools {
			if matchesToolName(name, loadTool) {
				visible = true
				break
			}
		}
		if visible && firstVisible == 0 {
			firstVisible = attempt
		}
		if visible {
			for _, body := range requests {
				if strings.Contains(body, "echo:"+probeSentinel) {
					callable = true
					break
				}
			}
		}
		fmt.Printf("OK prompt #%d upstream_tools=%d load_tool_visible=%v callable=%v\n",
			attempt, len(lastTools), visible, callable)
		if visible && callable {
			break
		}
	}

	if firstVisible == 0 {
		return fmt.Errorf("client-supplied MCP tool %q never appeared in the model tool surface within %d prompts after session/load\nHINT: session/load must consume the request-scoped plan (agent_stdio.go LoadSession -> withACPMCPClientPlan) and, like session/new, re-sync the surface-derived tool policy allowlist at the next turn boundary (cmd/aicli/commands/chat_tool_policy_sync.go).\nlast upstream tools=%v\nagent stderr:\n%s\nagent log (mcp/tool lines):\n%s",
			loadTool, maxPromptAttempts, lastTools, a.stderr.dump(25), logDigest(agentLog, 40))
	}
	if firstVisible != 1 {
		return fmt.Errorf("client-supplied MCP tool %q was NOT in the FIRST prompt tool surface after load (first visible at prompt #%d) - §7.4 item 4 consistency\nagent log (mcp/tool lines):\n%s",
			loadTool, firstVisible, logDigest(agentLog, 40))
	}
	if !callable {
		return fmt.Errorf("client-supplied MCP tool %q was visible at the first prompt after load but its echo result never reached the provider (tool not executable)\nagent stderr:\n%s",
			loadTool, a.stderr.dump(25))
	}
	fmt.Printf("OK session/load tool usable at the FIRST prompt: tool=%s server=%s\n", loadTool, loadServerName)

	// session/close must reap the stdio child assembled by session/load.
	if !processAlive(pid) {
		return fmt.Errorf("client-supplied stdio server pid=%d is not alive before session/close; reaping cannot be proven", pid)
	}
	if _, err := a.call("session/close", map[string]interface{}{"sessionId": sessionID}, 60*time.Second); err != nil {
		return fmt.Errorf("session/close after load: %w\nagent stderr:\n%s", err, a.stderr.dump(25))
	}
	reapDeadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(reapDeadline) && processAlive(pid) {
		time.Sleep(250 * time.Millisecond)
	}
	if processAlive(pid) {
		return fmt.Errorf("client-supplied stdio server pid=%d survived session/close (session/load path)\nagent log (mcp/tool lines):\n%s",
			pid, logDigest(agentLog, 40))
	}
	fmt.Printf("OK session/load server pid=%d reaped after session/close (tasklist/proc probe)\n", pid)

	fmt.Println("E2E PASS acp_e2e_mcp_session_load")
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "\nFAIL acp_e2e_mcp_session_load: %v\n", err)
		os.Exit(1)
	}
}
