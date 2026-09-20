// Real end-to-end ACP verification of session management (list / delete /
// close / load) against the compiled aicli binary. Runs `aicli agent stdio`
// with an isolated USERPROFILE/HOME and a local mock OpenAI-compatible
// streaming provider, then asserts on the real NDJSON JSON-RPC traffic:
//
//  1. initialize advertises agentCapabilities.sessionCapabilities
//     list/delete/close as capability OBJECTS ({} = available; ACP v1 does not
//     accept booleans here)
//  2. session/new with an absolute cwd pointing at an existing temp workspace
//     returns a non-empty sessionId
//  3. session/list (cwd filter) returns `sessions` as a JSON array (never
//     null) containing the new sessionId; a cwd that matches nothing returns
//     `sessions: []` and no nextCursor
//  4. cursor handling: cursor "1" is a valid response without error,
//     cursor "abc" is a JSON-RPC error with code -32602
//  5. session/delete of a never-existing sessionId succeeds ({}), and a repeat
//     session/delete of an already-deleted sessionId also succeeds (idempotent)
//  6. after delete, session/list no longer contains that sessionId
//  7. session/load of the deleted sessionId is a JSON-RPC error -32002
//     (resource not found)
//  8. close semantics: session/new -> slow in-flight prompt -> session/close
//     settles the prompt as stopReason "cancelled"; session/prompt on the
//     closed session is -32002 (resource not found: the session is gone, the
//     request itself is well-formed — server.go sessionLookupRPCError);
//     session/load on the same session still succeeds (close keeps history)
//
// Run (from backend/):
//
//	go run ./scripts/acp_e2e_session_mgmt.go [aicli.exe]
//
// With no argument the real aicli binary is built into a temp dir with
// `go build -o <tmp>/aicli.exe ./cmd/aicli`.

//go:build ignore

package main

import (
	"bufio"
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

type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// rpcError mirrors the JSON-RPC error object so assertions can pin exact codes.
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

func writeMsg(w io.Writer, msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

// frameLog keeps the raw NDJSON frames read from the agent so a failing
// assertion can print the offending frame(s).
type frameLog struct {
	mu    sync.Mutex
	items []string
}

func (f *frameLog) append(line string) {
	if f == nil || strings.TrimSpace(line) == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items = append(f.items, line)
	if len(f.items) > 200 {
		f.items = f.items[len(f.items)-200:]
	}
}

func (f *frameLog) tail(limit int) []string {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 || limit > len(f.items) {
		limit = len(f.items)
	}
	out := make([]string, limit)
	copy(out, f.items[len(f.items)-limit:])
	return out
}

// inbound carries one decoded response together with its original NDJSON line.
type inbound struct {
	msg  Message
	line string
}

func rawFrame(msg Message) string {
	data, err := json.Marshal(msg)
	if err != nil {
		return "<unmarshalable frame>"
	}
	return string(data)
}

// capturedBodies records upstream request bodies in arrival order so the test
// can assert on what the provider actually received.
type capturedBodies struct {
	mu    sync.Mutex
	items []map[string]interface{}
}

func (c *capturedBodies) record(body map[string]interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = append(c.items, body)
}

func (c *capturedBodies) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// slowProvider is the local mock OpenAI-compatible streaming endpoint. It
// records every upstream request body, emits the assistant role chunk, then
// holds the stream open until the client aborts (prompt cancel / session close)
// or a hard timeout, mirroring the slow provider used by acp_e2e_cancel.go.
func slowProvider(captured *capturedBodies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var parsed map[string]interface{}
		if err := json.Unmarshal(raw, &parsed); err == nil {
			captured.record(parsed)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		emit := func(payload string) {
			fmt.Fprintf(w, "data: %s\n\n", payload)
			if flusher != nil {
				flusher.Flush()
			}
		}
		roleChunk := `{"id":"mock","object":"chat.completion.chunk","created":1,"model":"m1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`
		contentChunk := `{"id":"mock","object":"chat.completion.chunk","created":1,"model":"m1","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`
		finalChunk := `{"id":"mock","object":"chat.completion.chunk","created":1,"model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`

		emit(roleChunk)
		// Hold the stream: the e2e client closes/cancels the session while we
		// are waiting here. Stop as soon as the upstream context dies.
		select {
		case <-r.Context().Done():
			return
		case <-time.After(15 * time.Second):
		}
		for i := 0; i < 5; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
			emit(contentChunk)
		}
		emit(finalChunk)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
}

type e2e struct {
	mu      sync.Mutex
	nextID  int
	pending map[string]chan inbound
	stdin   io.Writer
	frames  *frameLog
}

// begin registers a pending response slot and writes the request frame.
func (e *e2e) begin(method string, params interface{}) (int, chan inbound, error) {
	raw, _ := json.Marshal(params)
	ch := make(chan inbound, 1)
	e.mu.Lock()
	e.nextID++
	id := e.nextID
	e.pending[fmt.Sprintf("%d", id)] = ch
	e.mu.Unlock()
	return id, ch, writeMsg(e.stdin, Message{
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

// call writes one JSON-RPC request and waits for its response frame. The
// returned error is transport-level only (timeout / write failure); JSON-RPC
// errors stay in rpcCall.Err so assertions can pin their codes.
func (e *e2e) call(method string, params interface{}, timeout time.Duration) (rpcCall, error) {
	_, ch, err := e.begin(method, params)
	if err != nil {
		return rpcCall{Method: method}, err
	}
	return e.await(method, ch, timeout)
}

func (e *e2e) await(method string, ch chan inbound, timeout time.Duration) (rpcCall, error) {
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

// checker accumulates PASS/FAIL lines and exits non-zero when anything failed.
type checker struct {
	mu     sync.Mutex
	passed int
	failed int
	names  []string
	frames *frameLog
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
	if c.frames != nil {
		for _, frame := range c.frames.tail(6) {
			fmt.Printf("  raw frame: %s\n", frame)
		}
	}
}

// check is the common shape: report ok/reason for one named assertion.
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

// idInArray reports whether decodes to a JSON array and contains want.
func decodeSessionIDs(raw json.RawMessage) ([]string, bool, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, false, fmt.Errorf("missing sessions field")
	}
	if trimmed[0] != '[' {
		return nil, false, fmt.Errorf("sessions is not a JSON array: %s", trimmed)
	}
	var rows []struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, false, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.SessionID)
	}
	return ids, true, nil
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
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

	// --- isolated environment: temp USERPROFILE + local mock provider ---
	captured := &capturedBodies{}
	srv := httptest.NewServer(slowProvider(captured))
	defer srv.Close()

	workspace, err := os.MkdirTemp("", "acp-e2e-workspace")
	if err != nil {
		fmt.Printf("FAIL temp workspace: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(workspace)

	home, err := os.MkdirTemp("", "acp-e2e-home")
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "agent", "stdio")
	cmd.Env = append(os.Environ(), "USERPROFILE="+home, "HOME="+home)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		fmt.Printf("FAIL start: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}()
	go io.Copy(io.Discard, stderr)

	frames := &frameLog{}
	e := &e2e{pending: map[string]chan inbound{}, stdin: stdin, frames: frames}
	c := &checker{frames: frames}
	go func() {
		reader := bufio.NewReader(stdout)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			frames.append(line)
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

	// checkFrame reports one named assertion, printing the offending raw NDJSON
	// frame when it fails.
	checkFrame := func(name string, ok bool, frame string, reason string, args ...interface{}) {
		if ok {
			c.pass(name, "")
			return
		}
		if strings.TrimSpace(frame) != "" {
			c.fail(name, reason+"\n  offending frame: %s", append(args, frame)...)
			return
		}
		c.fail(name, reason, args...)
	}
	mustCall := func(name, method string, params interface{}, timeout time.Duration) rpcCall {
		resp, err := e.call(method, params, timeout)
		if err != nil {
			fmt.Printf("FAIL %s (fatal): %v\n", name, err)
			os.Exit(1)
		}
		return resp
	}

	// --- assertion 1: initialize advertises session capabilities ---
	initResp := mustCall("A1 initialize", "initialize", map[string]interface{}{
		"protocolVersion": 1,
		"clientCapabilities": map[string]interface{}{
			"fs":       map[string]interface{}{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
	}, 20*time.Second)
	var initRes struct {
		AgentCapabilities struct {
			LoadSession bool `json:"loadSession"`
			// ACP v1 spells every session capability as an OBJECT (`{}`), so
			// decoding into *struct{} both asserts presence and rejects the
			// boolean form a non-conforming agent would emit.
			SessionCapabilities *struct {
				List   *struct{} `json:"list"`
				Delete *struct{} `json:"delete"`
				Close  *struct{} `json:"close"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}
	if initResp.Err != nil {
		fmt.Printf("FAIL A1 initialize (fatal): %s\n", initResp.errorString())
		os.Exit(1)
	}
	if err := json.Unmarshal(initResp.Result, &initRes); err != nil {
		fmt.Printf("FAIL A1 initialize (fatal): decode result: %v\n", err)
		os.Exit(1)
	}
	caps := initRes.AgentCapabilities.SessionCapabilities
	if caps == nil {
		c.fail("A1 initialize: sessionCapabilities advertised", "agentCapabilities.sessionCapabilities missing in %s", initResp.Result)
	} else {
		c.check("A1 initialize: sessionCapabilities list={}", caps.List != nil, "list absent in %s", initResp.Result)
		c.check("A1 initialize: sessionCapabilities delete={}", caps.Delete != nil, "delete absent in %s", initResp.Result)
		c.check("A1 initialize: sessionCapabilities close={}", caps.Close != nil, "close absent in %s", initResp.Result)
	}
	c.check("A1 initialize: loadSession=true (needed by A7)", initRes.AgentCapabilities.LoadSession, "loadSession=false in %s", initResp.Result)

	// --- assertion 2: session/new with an existing absolute cwd ---
	newResp := mustCall("A2 session/new", "session/new", map[string]interface{}{
		"cwd":        workspace,
		"mcpServers": []interface{}{},
	}, 30*time.Second)
	var snew struct {
		SessionID string `json:"sessionId"`
	}
	if newResp.Err != nil {
		fmt.Printf("FAIL A2 session/new (fatal): %s\n  offending frame: %s\n", newResp.errorString(), newResp.Frame)
		os.Exit(1)
	}
	if err := json.Unmarshal(newResp.Result, &snew); err != nil {
		fmt.Printf("FAIL A2 session/new (fatal): decode result: %v\n", err)
		os.Exit(1)
	}
	if !c.check("A2 session/new: non-empty sessionId", strings.TrimSpace(snew.SessionID) != "", "sessionId=%q in %s", snew.SessionID, newResp.Result) {
		os.Exit(1)
	}
	fmt.Printf("     sessionId=%s workspace=%s\n", snew.SessionID, workspace)

	// --- assertion 3a: session/list with cwd filter contains the new session ---
	listResp := mustCall("A3a session/list", "session/list", map[string]interface{}{"cwd": workspace}, 20*time.Second)
	checkFrame("A3a session/list: no JSON-RPC error", listResp.Err == nil, listResp.Frame, "error %s", listResp.errorString())
	var listed struct {
		Sessions   json.RawMessage `json:"sessions"`
		NextCursor string          `json:"nextCursor"`
	}
	if err := json.Unmarshal(listResp.Result, &listed); err != nil {
		c.fail("A3a session/list: sessions is a JSON array containing the new sessionId", "decode result: %v", err)
	} else {
		ids, isArray, err := decodeSessionIDs(listed.Sessions)
		reason := "sessions field = %s"
		ok := err == nil && isArray && containsString(ids, snew.SessionID)
		if err != nil {
			reason = "sessions field invalid: %v (raw result %s)"
			c.fail("A3a session/list: sessions is a JSON array containing the new sessionId", reason, err, listResp.Result)
		} else {
			checkFrame("A3a session/list: sessions is a JSON array containing the new sessionId", ok, listResp.Frame, reason, listed.Sessions)
		}
	}

	// --- assertion 3b: a cwd that matches nothing returns sessions:[] and no nextCursor ---
	missCwd := filepath.Join(home, "workspace-that-does-not-exist")
	missResp := mustCall("A3b session/list (unknown cwd)", "session/list", map[string]interface{}{"cwd": missCwd}, 20*time.Second)
	missRaw := strings.TrimSpace(string(missResp.Result))
	checkFrame("A3b session/list unknown cwd: no JSON-RPC error", missResp.Err == nil, missResp.Frame, "error %s", missResp.errorString())
	var missListed struct {
		Sessions   json.RawMessage `json:"sessions"`
		NextCursor string          `json:"nextCursor"`
	}
	missDecodeErr := json.Unmarshal(missResp.Result, &missListed)
	isEmptyArray := missDecodeErr == nil && strings.HasPrefix(strings.TrimSpace(string(missListed.Sessions)), "[") && strings.Contains(missRaw, `"sessions":[]`)
	checkFrame("A3b session/list unknown cwd: sessions:[] (empty array, not null)", isEmptyArray, missResp.Frame, "result = %s, want an empty sessions array", missRaw)
	noCursor := missDecodeErr == nil && !strings.Contains(missRaw, "nextCursor")
	checkFrame("A3b session/list unknown cwd: no nextCursor", noCursor, missResp.Frame, "result = %s, want no nextCursor", missRaw)

	// --- assertion 4: cursor handling ---
	cursorResp := mustCall("A4a session/list cursor=1", "session/list", map[string]interface{}{
		"cwd":    workspace,
		"cursor": "1",
	}, 20*time.Second)
	ok4a := cursorResp.Err == nil
	if ok4a {
		var cursorListed struct {
			Sessions json.RawMessage `json:"sessions"`
		}
		if err := json.Unmarshal(cursorResp.Result, &cursorListed); err != nil {
			ok4a = false
		} else if _, isArray, err := decodeSessionIDs(cursorListed.Sessions); err != nil || !isArray {
			ok4a = false
		}
	}
	checkFrame("A4a session/list cursor=1: valid response without error", ok4a, cursorResp.Frame,
		"result = %s error = %s, want a valid session/list response", cursorResp.Result, cursorResp.errorString())

	badCursorResp := mustCall("A4b session/list cursor=abc", "session/list", map[string]interface{}{
		"cwd":    workspace,
		"cursor": "abc",
	}, 20*time.Second)
	checkFrame("A4b session/list cursor=abc: JSON-RPC error -32602", badCursorResp.Err != nil && badCursorResp.Err.Code == -32602, badCursorResp.Frame,
		"error = %s, want code -32602", badCursorResp.errorString())

	// --- assertion 5: session/delete is idempotent and tolerates missing ids ---
	neverID := fmt.Sprintf("sess_never_%d", time.Now().UnixNano())
	delNeverResp := mustCall("A5a session/delete (never existed)", "session/delete", map[string]interface{}{
		"sessionId": neverID,
	}, 20*time.Second)
	delNeverRaw := strings.TrimSpace(string(delNeverResp.Result))
	checkFrame("A5a session/delete of a never-existing sessionId succeeds ({})",
		delNeverResp.Err == nil && delNeverRaw == "{}", delNeverResp.Frame,
		"result = %s error = %s, want {} with no error", delNeverRaw, delNeverResp.errorString())

	delLiveResp := mustCall("A5b session/delete (live session)", "session/delete", map[string]interface{}{
		"sessionId": snew.SessionID,
	}, 20*time.Second)
	delLiveRaw := strings.TrimSpace(string(delLiveResp.Result))
	checkFrame("A5b session/delete of the live session succeeds ({})",
		delLiveResp.Err == nil && delLiveRaw == "{}", delLiveResp.Frame,
		"result = %s error = %s, want {} with no error", delLiveRaw, delLiveResp.errorString())

	delAgainResp := mustCall("A5c session/delete (repeat)", "session/delete", map[string]interface{}{
		"sessionId": snew.SessionID,
	}, 20*time.Second)
	delAgainRaw := strings.TrimSpace(string(delAgainResp.Result))
	checkFrame("A5c repeat session/delete of an already-deleted sessionId succeeds (idempotent)",
		delAgainResp.Err == nil && delAgainRaw == "{}", delAgainResp.Frame,
		"result = %s error = %s, want {} with no error", delAgainRaw, delAgainResp.errorString())

	// --- assertion 6: the deleted session disappears from session/list ---
	afterDelResp := mustCall("A6 session/list after delete", "session/list", map[string]interface{}{
		"cwd": workspace,
	}, 20*time.Second)
	ok6 := afterDelResp.Err == nil
	var afterDelListed struct {
		Sessions json.RawMessage `json:"sessions"`
	}
	var afterDelIDs []string
	if ok6 {
		if err := json.Unmarshal(afterDelResp.Result, &afterDelListed); err != nil {
			ok6 = false
		} else if ids, isArray, err := decodeSessionIDs(afterDelListed.Sessions); err != nil || !isArray {
			ok6 = false
		} else {
			afterDelIDs = ids
			ok6 = !containsString(ids, snew.SessionID)
		}
	}
	checkFrame("A6 session/list no longer contains the deleted sessionId", ok6, afterDelResp.Frame,
		"sessions = %v (raw %s) error = %s, want the deleted sessionId absent", afterDelIDs, afterDelResp.Result, afterDelResp.errorString())

	// --- assertion 7: session/load of the deleted session is -32002 ---
	loadDeletedResp := mustCall("A7 session/load (deleted)", "session/load", map[string]interface{}{
		"sessionId": snew.SessionID,
		"cwd":       workspace,
	}, 30*time.Second)
	checkFrame("A7 session/load of the deleted sessionId: JSON-RPC error -32002 (resource not found)",
		loadDeletedResp.Err != nil && loadDeletedResp.Err.Code == -32002, loadDeletedResp.Frame,
		"error = %s, want code -32002", loadDeletedResp.errorString())

	// --- assertion 8: close semantics ---
	closeNewResp := mustCall("A8 session/new (close target)", "session/new", map[string]interface{}{
		"cwd":        workspace,
		"mcpServers": []interface{}{},
	}, 30*time.Second)
	var closeSess struct {
		SessionID string `json:"sessionId"`
	}
	if closeNewResp.Err != nil || json.Unmarshal(closeNewResp.Result, &closeSess) != nil || strings.TrimSpace(closeSess.SessionID) == "" {
		c.fail("A8 session/new for close semantics", "result = %s error = %s", closeNewResp.Result, closeNewResp.errorString())
		os.Exit(c.summary())
	}
	before := captured.count()

	// Start a long-running prompt and wait until the mock provider has actually
	// received the upstream request before closing the session.
	_, promptCh, err := e.begin("session/prompt", map[string]interface{}{
		"sessionId": closeSess.SessionID,
		"prompt":    []map[string]interface{}{{"type": "text", "text": "hello slowly"}},
	})
	if err != nil {
		fmt.Printf("FAIL A8 start slow prompt (fatal): %v\n", err)
		os.Exit(1)
	}
	inFlight := false
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if captured.count() > before {
			inFlight = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	c.check("A8 slow prompt reached the mock provider (in flight)", inFlight,
		"provider saw %d upstream requests, want at least %d", captured.count(), before+1)

	closeResp := mustCall("A8 session/close", "session/close", map[string]interface{}{
		"sessionId": closeSess.SessionID,
	}, 30*time.Second)
	closeRaw := strings.TrimSpace(string(closeResp.Result))
	checkFrame("A8a session/close while a prompt is in flight succeeds ({})",
		closeResp.Err == nil && closeRaw == "{}", closeResp.Frame,
		"result = %s error = %s, want {} with no error", closeRaw, closeResp.errorString())

	promptSettled, err := e.await("session/prompt", promptCh, 30*time.Second)
	if err != nil {
		c.fail("A8b in-flight prompt settles as cancelled", "no prompt response: %v", err)
	} else if promptSettled.Err != nil {
		checkFrame("A8b in-flight prompt settles as cancelled", false, promptSettled.Frame,
			"prompt failed with %s, want stopReason \"cancelled\"", promptSettled.errorString())
	} else {
		var pr struct {
			StopReason string `json:"stopReason"`
		}
		_ = json.Unmarshal(promptSettled.Result, &pr)
		checkFrame("A8b in-flight prompt settles as cancelled", pr.StopReason == "cancelled", promptSettled.Frame,
			"stopReason = %q, want \"cancelled\"", pr.StopReason)
	}

	promptClosedResp := mustCall("A8 session/prompt on closed session", "session/prompt", map[string]interface{}{
		"sessionId": closeSess.SessionID,
		"prompt":    []map[string]interface{}{{"type": "text", "text": "still there?"}},
	}, 30*time.Second)
	checkFrame("A8c session/prompt on a closed session: JSON-RPC error -32002",
		promptClosedResp.Err != nil && promptClosedResp.Err.Code == -32002, promptClosedResp.Frame,
		"error = %s, want code -32002 (resource not found)", promptClosedResp.errorString())

	loadClosedResp := mustCall("A8 session/load after close", "session/load", map[string]interface{}{
		"sessionId": closeSess.SessionID,
		"cwd":       workspace,
	}, 30*time.Second)
	loadClosedRaw := strings.TrimSpace(string(loadClosedResp.Result))
	checkFrame("A8d session/load after close still succeeds (close keeps history)",
		loadClosedResp.Err == nil && strings.HasPrefix(loadClosedRaw, "{"), loadClosedResp.Frame,
		"result = %s error = %s, want an object result", loadClosedRaw, loadClosedResp.errorString())

	os.Exit(c.summary())
}
