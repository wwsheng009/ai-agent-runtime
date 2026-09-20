// Real end-to-end ACP verification of the P3 concurrency item / risk R2: one
// `aicli agent stdio` process must serve several ACP sessions that each own a
// client-supplied stdio MCP server, without leaking state across sessions.
//
// Flow:
//  1. build aicli + the stdio MCP helper
//  2. isolated HOME (mock provider, folder trust ON, workspace trusted)
//  3. initialize + 4x session/new CONCURRENTLY, each with its own stdio server
//     (distinct name, distinct tool, distinct pid file) -> 4 live sessions
//  4. 4x session/prompt CONCURRENTLY; every session must see its OWN tool as
//     visible + callable and must NOT see any other session's tool
//  5. session/close #0 -> only its own MCP child is reaped, the other 3 stay
//  6. close the remaining sessions -> every child reaped, no orphan left
//
// Run (from backend/):
//
//	go run ./scripts/acp_e2e_mcp_multi_session.go [aicli.exe]
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
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// multiToolPrefix is the shared prefix of the per-session MCP tools
	// (<prefix>_<sessionIndex>); the names are unique on purpose so no built-in
	// toolkit entry can shadow them and cross-session leaks are detectable.
	multiToolPrefix = "acp_e2e_multi_echo"
	// multiServerPrefix is the shared prefix of the per-session server names.
	multiServerPrefix = "client-multi-"
	// probeSentinel marks the user turn that must be answered with a tool call.
	probeSentinel = "RUN_MCP_TOOL_PROBE"
	// maxPromptAttempts bounds the wait for a late (async-connected) MCP tool.
	maxPromptAttempts = 3
)

// sessionTagRe matches the "session=<n>" tag carried by every probe prompt.
var sessionTagRe = regexp.MustCompile(`session=(\d+)`)

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
// session's own helper tool is present in the request tool surface; every other
// request gets plain text. In this scenario m.tool is a PREFIX: the tool picked
// for a request is <prefix>_<sessionTag>, which is what makes a cross-session
// leak observable (a leaked tool would satisfy the wrong tag or none at all).
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

	tag := sessionTagOf(string(raw))
	if !strings.Contains(string(raw), m.sentinel) || tag == "" || hasToolRoleMessage(string(raw)) {
		emit(sseChunk(map[string]interface{}{"content": "plain-answer"}, nil))
		emit(sseChunk(map[string]interface{}{}, "stop"))
		fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	name, ok := pickToolForCall(string(raw), m.tool+"_"+tag)
	if !ok {
		emit(sseChunk(map[string]interface{}{"content": "mcp-tool-not-in-surface"}, nil))
		emit(sseChunk(map[string]interface{}{}, "stop"))
		fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	args, _ := json.Marshal(map[string]interface{}{"text": fmt.Sprintf("%s session=%s", m.sentinel, tag)})
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

// sessionTagOf extracts the "session=<n>" tag that every probe prompt carries.
// It is what attributes a captured upstream body to one concurrent session.
func sessionTagOf(raw string) string {
	m := sessionTagRe.FindStringSubmatch(raw)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// helperProcessCount counts live processes by image name (Windows: tasklist,
// Unix: pgrep -f). A negative result means "could not probe".
func helperProcessCount(image string) int {
	if runtime.GOOS == "windows" {
		out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq "+image, "/FO", "CSV", "/NH").Output()
		if err != nil {
			return -1
		}
		count := 0
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "\""+image+"\"") {
				count++
			}
		}
		return count
	}
	out, err := exec.Command("pgrep", "-f", image).Output()
	if err != nil {
		// pgrep exits 1 when nothing matches.
		return 0
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func run() error {
	backendDir, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(backendDir, "cmd", "aicli")); err != nil {
		return fmt.Errorf("run this script from backend/ (cmd/aicli not found in %s)", backendDir)
	}

	tmpRoot, err := os.MkdirTemp("", "acp-e2e-mcp-multi")
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
	helperImage := exeName("acp_e2e_mcp_helper_multi")
	helper := filepath.Join(tmpRoot, helperImage)
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
	agentLog := filepath.Join(tmpRoot, "agent.log")

	trustYAML := fmt.Sprintf("version: 1\nfolders:\n  %s:\n    trusted: true\n", yamlQuote(workspace))
	if err := writeFile(filepath.Join(home, ".aicli", "trusted_folders.yaml"), trustYAML); err != nil {
		return err
	}

	captured := &capture{}
	srv := httptest.NewServer(&mockProvider{captured: captured, sentinel: probeSentinel, tool: multiToolPrefix})
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

	if _, err := a.call("initialize", map[string]interface{}{
		"protocolVersion":    1,
		"clientCapabilities": map[string]interface{}{"terminal": false},
	}, 30*time.Second); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	fmt.Println("OK initialize")

	// --- phase 1: N concurrent sessions, each with its own stdio server ---
	const sessionCount = 4
	type sess struct {
		index      int
		id         string
		server     string
		tool       string
		pidPath    string
		markerPath string
		pid        int
	}
	states := make([]*sess, sessionCount)
	for i := 0; i < sessionCount; i++ {
		states[i] = &sess{
			index:      i,
			server:     fmt.Sprintf("%s%d", multiServerPrefix, i),
			tool:       fmt.Sprintf("%s_%d", multiToolPrefix, i),
			pidPath:    filepath.Join(tmpRoot, fmt.Sprintf("multi-%d.pid", i)),
			markerPath: filepath.Join(tmpRoot, fmt.Sprintf("multi-%d.marker", i)),
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, sessionCount)
	newStart := time.Now()
	for i := 0; i < sessionCount; i++ {
		wg.Add(1)
		go func(s *sess) {
			defer wg.Done()
			res, err := a.call("session/new", map[string]interface{}{
				"cwd": workspace,
				"mcpServers": []interface{}{map[string]interface{}{
					"name":    s.server,
					"type":    "stdio",
					"command": helper,
					"args": []string{
						"-tool", s.tool,
						"-marker", s.markerPath,
						"-pidfile", s.pidPath,
					},
				}},
			}, 90*time.Second)
			if err != nil {
				errs <- fmt.Errorf("session/new #%d: %w", s.index, err)
				return
			}
			var out struct {
				SessionID string `json:"sessionId"`
			}
			if err := json.Unmarshal(res, &out); err != nil || strings.TrimSpace(out.SessionID) == "" {
				errs <- fmt.Errorf("session/new #%d result: %v %s", s.index, err, res)
				return
			}
			s.id = strings.TrimSpace(out.SessionID)
		}(states[i])
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return fmt.Errorf("%w\nagent stderr:\n%s", err, a.stderr.dump(25))
		}
	}
	fmt.Printf("OK session/new x%d concurrently (elapsed=%s)\n", sessionCount, time.Since(newStart).Round(time.Millisecond))

	// Every session must own a distinct, live MCP child.
	seen := map[int]int{}
	var pids []int
	for _, s := range states {
		pid, pidErr := waitForPID(s.pidPath, 20*time.Second)
		if pidErr != nil {
			return fmt.Errorf("session #%d did not spawn its client-supplied server: %v\nmarker=%s pid=%s\nagent stderr:\n%s\nagent log (mcp/tool lines):\n%s",
				s.index, pidErr, fileState(s.markerPath), fileState(s.pidPath), a.stderr.dump(25), logDigest(agentLog, 40))
		}
		if prev, dup := seen[pid]; dup {
			return fmt.Errorf("session #%d and session #%d share the MCP child pid %d; sessions are not isolated", prev, s.index, pid)
		}
		seen[pid] = s.index
		s.pid = pid
		if !processAlive(pid) {
			return fmt.Errorf("MCP child pid=%d of session #%d is not alive", pid, s.index)
		}
		pids = append(pids, pid)
	}
	fmt.Printf("OK %d distinct MCP child processes alive: %v\n", len(pids), pids)

	// --- phase 2: concurrent probe prompts, strict per-session isolation ---
	type probeResult struct {
		visible  bool
		callable bool
		foreign  []string
		tools    int
	}
	results := make([]probeResult, sessionCount)
	promptStart := time.Now()
	errs = make(chan error, sessionCount)
	for i := 0; i < sessionCount; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := states[i]
			tag := fmt.Sprintf("session=%d", s.index)
			for attempt := 1; attempt <= maxPromptAttempts; attempt++ {
				text := fmt.Sprintf("%s %s attempt=%d", probeSentinel, tag, attempt)
				if _, err := a.call("session/prompt", map[string]interface{}{
					"sessionId": s.id,
					"prompt":    []map[string]interface{}{{"type": "text", "text": text}},
				}, 120*time.Second); err != nil {
					errs <- fmt.Errorf("session/prompt #%d (session #%d): %w", attempt, s.index, err)
					return
				}
				bodies := captured.since(0)
				probe := ""
				callable := false
				// The mock calls the tool with the argument "<sentinel> <tag>", so the
				// echoed tool result is "echo:<sentinel> <tag>" (no attempt suffix).
				echoed := "echo:" + probeSentinel + " " + tag
				for _, body := range bodies {
					if strings.Contains(body, echoed) {
						callable = true
					}
					// The first body carrying this session's tag is the probe turn; later
					// bodies repeat the tag inside the echoed tool result.
					if probe == "" && strings.Contains(body, probeSentinel) && strings.Contains(body, tag) {
						probe = body
					}
				}
				if probe == "" {
					errs <- fmt.Errorf("session #%d produced no probe upstream request carrying %q", s.index, tag)
					return
				}
				tools := requestToolNames(probe)
				visible := false
				var foreign []string
				for _, name := range tools {
					if matchesToolName(name, s.tool) {
						visible = true
					}
					for j := 0; j < sessionCount; j++ {
						if j == i {
							continue
						}
						if matchesToolName(name, fmt.Sprintf("%s_%d", multiToolPrefix, j)) {
							foreign = append(foreign, name)
						}
					}
				}
				results[i] = probeResult{visible: visible, callable: callable, foreign: foreign, tools: len(tools)}
				if visible && callable {
					return
				}
			}
			errs <- fmt.Errorf("session #%d never saw its own MCP tool %q as visible+callable within %d prompts\nagent stderr:\n%s\nagent log (mcp/tool lines):\n%s",
				s.index, s.tool, maxPromptAttempts, a.stderr.dump(25), logDigest(agentLog, 40))
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	fmt.Printf("OK prompt x%d concurrently (elapsed=%s)\n", sessionCount, time.Since(promptStart).Round(time.Millisecond))

	for i, r := range results {
		if !r.visible || !r.callable {
			return fmt.Errorf("session #%d: own_tool_visible=%v callable=%v", i, r.visible, r.callable)
		}
		if len(r.foreign) > 0 {
			return fmt.Errorf("session #%d saw another session's MCP tools %v; session isolation is broken", i, r.foreign)
		}
		fmt.Printf("OK session #%d upstream_tools=%d own_tool_visible=true foreign_tools=0 callable=true\n", i, r.tools)
	}

	// --- phase 3: closing one session must reap only its own child ---
	if _, err := a.call("session/close", map[string]interface{}{"sessionId": states[0].id}, 60*time.Second); err != nil {
		return fmt.Errorf("session/close #0: %w\nagent stderr:\n%s", err, a.stderr.dump(25))
	}
	reapDeadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(reapDeadline) && processAlive(states[0].pid) {
		time.Sleep(200 * time.Millisecond)
	}
	if processAlive(states[0].pid) {
		return fmt.Errorf("session #0 close did not reap its MCP child pid=%d", states[0].pid)
	}
	for i := 1; i < sessionCount; i++ {
		if !processAlive(states[i].pid) {
			return fmt.Errorf("closing session #0 also killed session #%d's MCP child pid=%d; lifecycle is not per-session",
				i, states[i].pid)
		}
	}
	fmt.Printf("OK closing session #0 reaped only pid=%d; the other %d children stayed alive\n", states[0].pid, sessionCount-1)

	// --- phase 4: close the rest and prove no orphan is left ---
	closeStart := time.Now()
	errs = make(chan error, sessionCount)
	for i := 1; i < sessionCount; i++ {
		wg.Add(1)
		go func(s *sess) {
			defer wg.Done()
			if _, err := a.call("session/close", map[string]interface{}{"sessionId": s.id}, 60*time.Second); err != nil {
				errs <- fmt.Errorf("session/close #%d: %w", s.index, err)
			}
		}(states[i])
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		alive := 0
		for i := 1; i < sessionCount; i++ {
			if processAlive(states[i].pid) {
				alive++
			}
		}
		if alive == 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	for i := 1; i < sessionCount; i++ {
		if processAlive(states[i].pid) {
			return fmt.Errorf("session #%d MCP child pid=%d survived session/close", i, states[i].pid)
		}
	}
	fmt.Printf("OK all %d MCP children reaped after session/close (elapsed=%s)\n", sessionCount, time.Since(closeStart).Round(time.Millisecond))

	if count := helperProcessCount(helperImage); count != 0 {
		return fmt.Errorf("orphan MCP helper processes remain after closing every session: image=%s count=%d", helperImage, count)
	}
	fmt.Printf("OK no orphan helper processes remain (image=%s)\n", helperImage)

	fmt.Printf("E2E PASS acp_e2e_mcp_multi_session (sessions=%d isolation=strict reaped=%d)\n", sessionCount, sessionCount)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "\nFAIL acp_e2e_mcp_multi_session: %v\n", err)
		os.Exit(1)
	}
}
