// Real end-to-end ACP verification of the plan session update, driven by the
// todos tool against the compiled aicli binary. Runs `aicli agent stdio` with an
// isolated USERPROFILE and a local mock OpenAI-compatible streaming provider
// that answers the first model request with a streamed `todos` tool call and the
// follow-up request (the one carrying the tool result) with plain text, then:
//  1. initialize + session/new
//  2. session/prompt, asserting the todos tool terminal is followed by exactly
//     one `plan` update whose entries mirror the tool snapshot (content, status,
//     priority=medium) and whose `entries` field is a JSON array
//  3. session/load, asserting the replayed transcript rebuilds the same plan
//     from the persisted tool-result metadata
//
// Run: go run scripts/acp_e2e_plan.go <aicli.exe>
//
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

type e2e struct {
	mu      sync.Mutex
	nextID  int
	pending map[string]chan Message
}

// notificationLog records session/update notifications in arrival order.
type notificationLog struct {
	mu    sync.Mutex
	items []map[string]interface{}
}

func (n *notificationLog) append(params map[string]interface{}) {
	if n == nil || params == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.items = append(n.items, params)
}

func (n *notificationLog) snapshot() []map[string]interface{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]map[string]interface{}, len(n.items))
	copy(out, n.items)
	return out
}

// todoArguments is the streamed tool-call payload. It is split across two
// argument deltas on purpose so the runtime's accumulation path is exercised.
const todoArguments = `{"todos":[{"content":"分析需求","status":"in_progress","active_form":"正在分析需求"},{"content":"实施适配","status":"pending","active_form":""}]}`

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

// mockProvider answers the tool-call turn first; any request that already
// carries a tool result (the follow-up turn, whatever order the title generator
// runs in) gets the plain final answer.
func mockProvider() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		emit := func(payload string) {
			fmt.Fprintf(w, "data: %s\n\n", payload)
			if flusher != nil {
				flusher.Flush()
			}
		}

		hasToolResult := false
		var parsed struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if json.Unmarshal(body, &parsed) == nil {
			for _, msg := range parsed.Messages {
				if msg.Role == "tool" {
					hasToolResult = true
					break
				}
			}
		}

		if !hasToolResult {
			// Streamed OpenAI tool call: identity first, arguments in two parts.
			emit(sseChunk(map[string]interface{}{"role": "assistant"}, nil))
			emit(sseChunk(map[string]interface{}{"tool_calls": []map[string]interface{}{{
				"index":    0,
				"id":       "call_todos_1",
				"type":     "function",
				"function": map[string]interface{}{"name": "todos", "arguments": ""},
			}}}, nil))
			half := len(todoArguments) / 2
			emit(sseChunk(map[string]interface{}{"tool_calls": []map[string]interface{}{{
				"index":    0,
				"function": map[string]interface{}{"arguments": todoArguments[:half]},
			}}}, nil))
			emit(sseChunk(map[string]interface{}{"tool_calls": []map[string]interface{}{{
				"index":    0,
				"function": map[string]interface{}{"arguments": todoArguments[half:]},
			}}}, nil))
			emit(sseChunk(map[string]interface{}{}, "tool_calls"))
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}

		emit(sseChunk(map[string]interface{}{"role": "assistant"}, nil))
		emit(sseChunk(map[string]interface{}{"content": "任务已记录。"}, nil))
		emit(sseChunk(map[string]interface{}{}, "stop"))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
}

func updateField(params map[string]interface{}, key string) string {
	update, _ := params["update"].(map[string]interface{})
	if update == nil {
		return ""
	}
	value, _ := update[key].(string)
	return strings.TrimSpace(value)
}

func updateEntries(params map[string]interface{}) []interface{} {
	update, _ := params["update"].(map[string]interface{})
	if update == nil {
		return nil
	}
	entries, _ := update["entries"].([]interface{})
	return entries
}

// planEntries returns the plan notifications of the log with their entries
// decoded as maps.
func planEntries(items []map[string]interface{}) [][]map[string]interface{} {
	var plans [][]map[string]interface{}
	for _, params := range items {
		if updateField(params, "sessionUpdate") != "plan" {
			continue
		}
		raw := updateEntries(params)
		entries := make([]map[string]interface{}, 0, len(raw))
		for _, item := range raw {
			entry, _ := item.(map[string]interface{})
			entries = append(entries, entry)
		}
		plans = append(plans, entries)
	}
	return plans
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: acp_e2e_plan <aicli.exe>")
		os.Exit(2)
	}
	bin := os.Args[1]

	srv := httptest.NewServer(mockProvider())
	defer srv.Close()

	home, err := os.MkdirTemp("", "acp-e2e-plan")
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

	var stdinMu sync.Mutex
	send := func(msg Message) error {
		stdinMu.Lock()
		defer stdinMu.Unlock()
		data, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		_, err = stdin.Write(append(data, '\n'))
		return err
	}

	e := &e2e{pending: map[string]chan Message{}}
	notifications := &notificationLog{}
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
			var msg Message
			if json.Unmarshal([]byte(line), &msg) != nil {
				continue
			}
			if msg.ID == nil {
				if msg.Method == "session/update" {
					var params map[string]interface{}
					if json.Unmarshal(msg.Params, &params) == nil {
						notifications.append(params)
					}
				}
				continue
			}
			// Agent -> client request: the todos tool asks for approval before
			// running. Answer with the first allow option so the turn proceeds.
			if msg.Method == "session/request_permission" {
				var params struct {
					Options []struct {
						OptionID string `json:"optionId"`
						Kind     string `json:"kind"`
					} `json:"options"`
				}
				_ = json.Unmarshal(msg.Params, &params)
				chosen := ""
				for _, opt := range params.Options {
					if opt.Kind == "allow_always" {
						chosen = opt.OptionID
					}
				}
				if chosen == "" {
					for _, opt := range params.Options {
						if opt.Kind == "allow_once" {
							chosen = opt.OptionID
						}
					}
				}
				if chosen == "" && len(params.Options) > 0 {
					chosen = params.Options[0].OptionID
				}
				result, _ := json.Marshal(map[string]interface{}{
					"outcome": map[string]interface{}{"outcome": "selected", "optionId": chosen},
				})
				_ = send(Message{JSONRPC: "2.0", ID: msg.ID, Result: result})
				continue
			}
			e.mu.Lock()
			ch := e.pending[string(msg.ID)]
			e.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
		}
	}()
	go io.Copy(io.Discard, stderr)

	call := func(method string, params interface{}, timeout time.Duration) (json.RawMessage, error) {
		raw, _ := json.Marshal(params)
		ch := make(chan Message, 1)
		e.nextID++
		id := fmt.Sprintf("%d", e.nextID)
		e.mu.Lock()
		e.pending[id] = ch
		e.mu.Unlock()
		if err := send(Message{JSONRPC: "2.0", ID: json.RawMessage(id), Method: method, Params: raw}); err != nil {
			return nil, err
		}
		select {
		case msg := <-ch:
			if msg.Error != nil {
				return nil, fmt.Errorf("rpc error: %s", msg.Error)
			}
			return msg.Result, nil
		case <-time.After(timeout):
			return nil, fmt.Errorf("timeout waiting for %s", method)
		}
	}

	fail := func(format string, args ...interface{}) {
		fmt.Printf("E2E FAIL: "+format+"\n", args...)
		os.Exit(1)
	}

	// --- 1. initialize ---
	if _, err := call("initialize", map[string]interface{}{
		"protocolVersion":    1,
		"clientCapabilities": map[string]interface{}{"terminal": false},
	}, 20*time.Second); err != nil {
		fail("initialize: %v", err)
	}
	fmt.Println("OK initialize")

	// --- 2. session/new ---
	res, err := call("session/new", map[string]interface{}{
		"cwd":        home,
		"mcpServers": []interface{}{},
	}, 30*time.Second)
	if err != nil {
		fail("session/new: %v", err)
	}
	var snew struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &snew); err != nil || snew.SessionID == "" {
		fail("session/new result: %v %s", err, res)
	}
	fmt.Printf("OK session/new sessionId=%s\n", snew.SessionID)

	// --- 3. prompt: todos tool call -> plan update ---
	if _, err := call("session/prompt", map[string]interface{}{
		"sessionId": snew.SessionID,
		"prompt":    []map[string]interface{}{{"type": "text", "text": "记录任务"}},
	}, 90*time.Second); err != nil {
		fail("session/prompt: %v", err)
	}

	live := notifications.snapshot()
	plans := planEntries(live)
	if len(plans) != 1 {
		fail("live plans = %d, want exactly 1: %+v", len(plans), live)
	}
	if len(plans[0]) != 2 {
		fail("live plan entries = %+v, want 2 entries", plans[0])
	}
	want := []map[string]string{
		{"content": "分析需求", "status": "in_progress", "priority": "medium"},
		{"content": "实施适配", "status": "pending", "priority": "medium"},
	}
	for i, entry := range plans[0] {
		for key, value := range want[i] {
			if got, _ := entry[key].(string); got != value {
				fail("live plan entry %d %s = %q, want %q (%+v)", i, key, got, value, entry)
			}
		}
	}
	// The plan must arrive after the todos tool terminal, not before it.
	planIndex, toolTerminalIndex := -1, -1
	for i, params := range live {
		if updateField(params, "sessionUpdate") == "plan" && planIndex < 0 {
			planIndex = i
		}
		if updateField(params, "sessionUpdate") != "tool_call_update" {
			continue
		}
		update, _ := params["update"].(map[string]interface{})
		if status, _ := update["status"].(string); status == "completed" && toolTerminalIndex < 0 {
			toolTerminalIndex = i
		}
	}
	if toolTerminalIndex < 0 {
		fail("no completed tool_call_update in live stream: %+v", live)
	}
	if planIndex < toolTerminalIndex {
		fail("plan index %d precedes tool terminal %d", planIndex, toolTerminalIndex)
	}
	fmt.Printf("OK prompt plan entries=%+v\n", plans[0])

	// --- 4. session/load replay rebuilds the plan from history ---
	before := len(notifications.snapshot())
	if _, err := call("session/load", map[string]interface{}{
		"sessionId":  snew.SessionID,
		"cwd":        home,
		"mcpServers": []interface{}{},
	}, 30*time.Second); err != nil {
		fail("session/load: %v", err)
	}
	replayed := notifications.snapshot()[before:]
	replayPlans := planEntries(replayed)
	if len(replayPlans) != 1 {
		fail("replay plans = %d, want exactly 1: %+v", len(replayPlans), replayed)
	}
	if len(replayPlans[0]) != 2 {
		fail("replay plan entries = %+v, want 2 entries", replayPlans[0])
	}
	for i, entry := range replayPlans[0] {
		for key, value := range want[i] {
			if got, _ := entry[key].(string); got != value {
				fail("replay plan entry %d %s = %q, want %q (%+v)", i, key, got, value, entry)
			}
		}
	}
	fmt.Printf("OK session/load replay plan entries=%+v\n", replayPlans[0])

	fmt.Println("E2E PASS acp plan")
}
