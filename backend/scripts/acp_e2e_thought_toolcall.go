// Real end-to-end ACP verification that reasoning which shares ONE SSE delta
// with tool_calls still reaches the client as agent_thought_chunk.
//
// Hypothesis under test: the converter drops thinking content when the provider
// packs reasoning_content and tool_calls into the same OpenAI stream delta, so
// the thought never becomes its own ACP event. The mock provider below emits
// exactly that shape against the compiled aicli binary:
//
//  1. reasoning_content deltas ("先想")
//  2. one delta carrying BOTH reasoning_content ("混合块里的思考") and tool_calls
//     (a real tool name taken from the request's own tools array, so the call
//     resolves instead of failing the lookup)
//  3. finish_reason=tool_calls; after the tool result the agent issues a second
//     request and the provider answers with plain content
//
// Assertions:
//   - agent_thought_chunk carries BOTH reasoning fragments (the mixed delta is
//     not swallowed by its sibling tool_calls)
//   - tool_call / tool_call_update updates are emitted for the tool call
//   - agent_message_chunk never leaks reasoning text
//
// Run: go run scripts/acp_e2e_thought_toolcall.go <aicli.exe>

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
	writeMu sync.Mutex
	nextID  int
	pending map[string]chan Message
}

func (e *e2e) send(w io.Writer, msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	_, err = w.Write(append(data, '\n'))
	return err
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

// mockProviderState is stateful: request 1 answers with reasoning + a mixed
// reasoning/tool_calls delta, request 2 (after the tool result) answers with the
// final assistant text.
type mockProviderState struct {
	mu        sync.Mutex
	requests  int
	toolNames []string
}

func sseChunk(delta map[string]interface{}, finish interface{}) string {
	choice := map[string]interface{}{"index": 0, "delta": delta, "finish_reason": finish}
	payload := map[string]interface{}{
		"id":      "mock",
		"object":  "chat.completion.chunk",
		"created": 1,
		"model":   "m1",
		"choices": []interface{}{choice},
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

// pickToolName prefers a read-only file tool so the executed call cannot mutate
// the workspace; falls back to the first advertised function.
func pickToolName(names []string) string {
	for _, name := range names {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "read") || strings.Contains(lower, "view") || strings.Contains(lower, "cat") {
			return name
		}
	}
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

func (s *mockProviderState) handler(targetPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.Unmarshal(body, &request)
		names := make([]string, 0, len(request.Tools))
		for _, tool := range request.Tools {
			if name := strings.TrimSpace(tool.Function.Name); name != "" {
				names = append(names, name)
			}
		}

		s.mu.Lock()
		s.requests++
		attempt := s.requests
		if len(names) > 0 {
			s.toolNames = names
		}
		s.mu.Unlock()
		toolName := pickToolName(names)

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		emit := func(payload string) {
			fmt.Fprintf(w, "data: %s\n\n", payload)
			if flusher != nil {
				flusher.Flush()
			}
		}

		if attempt == 1 && toolName != "" {
			args, _ := json.Marshal(map[string]interface{}{"path": targetPath, "file_path": targetPath})
			emit(sseChunk(map[string]interface{}{"role": "assistant"}, nil))
			emit(sseChunk(map[string]interface{}{"reasoning_content": "先想"}, nil))
			// The hypothesis case: reasoning and tool_calls in the SAME delta.
			emit(sseChunk(map[string]interface{}{
				"reasoning_content": "混合块里的思考",
				"tool_calls": []interface{}{
					map[string]interface{}{
						"index": 0,
						"id":    "call_e2e_1",
						"type":  "function",
						"function": map[string]interface{}{
							"name":      toolName,
							"arguments": string(args),
						},
					},
				},
			}, nil))
			emit(sseChunk(map[string]interface{}{}, "tool_calls"))
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}

		emit(sseChunk(map[string]interface{}{"role": "assistant"}, nil))
		emit(sseChunk(map[string]interface{}{"content": "tool 之后的答案"}, nil))
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

func updateText(params map[string]interface{}) string {
	update, _ := params["update"].(map[string]interface{})
	if update == nil {
		return ""
	}
	content, _ := update["content"].(map[string]interface{})
	if content == nil {
		return ""
	}
	text, _ := content["text"].(string)
	return text
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: acp_e2e_thought_toolcall <aicli.exe>")
		os.Exit(2)
	}
	bin := os.Args[1]

	home, err := os.MkdirTemp("", "acp-e2e-thought-tool")
	if err != nil {
		fmt.Printf("FAIL temp home: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(home)
	target := filepath.Join(home, "target.txt")
	if err := os.WriteFile(target, []byte("e2e target\n"), 0o644); err != nil {
		fmt.Printf("FAIL write target: %v\n", err)
		os.Exit(1)
	}

	state := &mockProviderState{}
	srv := httptest.NewServer(state.handler(target))
	defer srv.Close()

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
      model_capabilities:
        m1:
          reasoning_model: true
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
			// Agent -> client requests: auto-approve permissions so a tool call
			// can never stall the turn waiting for a UI that is not there.
			if msg.ID != nil && strings.TrimSpace(msg.Method) != "" {
				switch msg.Method {
				case "session/request_permission":
					var params struct {
						Options []struct {
							OptionID string `json:"optionId"`
						} `json:"options"`
					}
					_ = json.Unmarshal(msg.Params, &params)
					optionID := ""
					for _, option := range params.Options {
						if strings.Contains(strings.ToLower(option.OptionID), "allow") {
							optionID = option.OptionID
							break
						}
					}
					if optionID == "" && len(params.Options) > 0 {
						optionID = params.Options[0].OptionID
					}
					var result map[string]interface{}
					if optionID == "" {
						result = map[string]interface{}{"outcome": map[string]interface{}{"outcome": "cancelled"}}
					} else {
						result = map[string]interface{}{"outcome": map[string]interface{}{"outcome": "selected", "optionId": optionID}}
					}
					raw, _ := json.Marshal(result)
					_ = e.send(stdin, Message{JSONRPC: "2.0", ID: msg.ID, Result: raw})
				default:
					_ = e.send(stdin, Message{
						JSONRPC: "2.0",
						ID:      msg.ID,
						Error:   json.RawMessage(`{"code":-32601,"message":"e2e harness has no handler for ` + msg.Method + `"}`),
					})
				}
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
		e.mu.Lock()
		e.pending[fmt.Sprintf("%d", e.nextID)] = ch
		e.mu.Unlock()
		if err := e.send(stdin, Message{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprintf("%d", e.nextID)), Method: method, Params: raw}); err != nil {
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

	if _, err := call("initialize", map[string]interface{}{
		"protocolVersion":    1,
		"clientCapabilities": map[string]interface{}{"terminal": false},
	}, 20*time.Second); err != nil {
		fail("initialize: %v", err)
	}
	fmt.Println("OK initialize")

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
	if err := json.Unmarshal(res, &snew); err != nil || strings.TrimSpace(snew.SessionID) == "" {
		fail("session/new returned no sessionId: %s", string(res))
	}
	fmt.Printf("OK session/new %s\n", snew.SessionID)

	before := len(notifications.snapshot())
	if _, err := call("session/prompt", map[string]interface{}{
		"sessionId": snew.SessionID,
		"prompt": []interface{}{
			map[string]interface{}{"type": "text", "text": "读取目标文件并总结"},
		},
	}, 90*time.Second); err != nil {
		fail("session/prompt: %v", err)
	}

	var thoughtText strings.Builder
	thoughtIDs := map[string]bool{}
	var answerText strings.Builder
	answerIDs := map[string]bool{}
	toolUpdates := 0
	toolNames := map[string]bool{}
	for _, params := range notifications.snapshot()[before:] {
		switch updateField(params, "sessionUpdate") {
		case "agent_thought_chunk":
			thoughtText.WriteString(updateText(params))
			thoughtIDs[updateField(params, "messageId")] = true
		case "agent_message_chunk":
			answerText.WriteString(updateText(params))
			answerIDs[updateField(params, "messageId")] = true
		case "tool_call", "tool_call_update":
			toolUpdates++
			if name := updateField(params, "name"); name != "" {
				toolNames[name] = true
			}
		}
	}

	state.mu.Lock()
	requests := state.requests
	advertised := append([]string{}, state.toolNames...)
	state.mu.Unlock()

	if requests < 2 {
		fmt.Printf("NOTE provider saw %d request(s); advertised tools=%v\n", requests, advertised)
	}

	// 1. The mixed reasoning+tool_calls delta must surface as a thought chunk.
	if got := thoughtText.String(); !strings.Contains(got, "先想") {
		fail("thought text = %q, want to contain 先想", got)
	}
	if got := thoughtText.String(); !strings.Contains(got, "混合块里的思考") {
		fail("thought text = %q: reasoning co-located with tool_calls was dropped", got)
	}
	if len(thoughtIDs) != 1 {
		fail("thought messageIds = %v, want one shared id", thoughtIDs)
	}
	for id := range thoughtIDs {
		if id == "" || !strings.HasSuffix(id, "_thought") {
			fail("thought messageId = %q, want <turn>_thought", id)
		}
	}

	// 2. Reasoning must never leak into the answer stream.
	if strings.Contains(answerText.String(), "先想") || strings.Contains(answerText.String(), "混合块里的思考") {
		fail("answer chunk leaked reasoning text: %q", answerText.String())
	}
	if got := answerText.String(); got != "tool 之后的答案" {
		fail("answer text = %q, want tool 之后的答案", got)
	}
	if len(answerIDs) != 1 {
		fail("answer messageIds = %v, want one shared id", answerIDs)
	}

	// 3. The tool call itself must be visible to the client.
	if len(advertised) == 0 {
		fmt.Println("SKIP tool_call assertion: provider request advertised no tools")
	} else if toolUpdates == 0 {
		fail("no tool_call/tool_call_update updates for advertised tools %v", advertised)
	}

	fmt.Printf("OK prompt stream thought=%q(%v) answer=%q tools=%d(%v) requests=%d\n",
		thoughtText.String(), thoughtIDs, answerText.String(), toolUpdates, toolNames, requests)
	fmt.Println("E2E PASS: reasoning co-located with tool_calls reaches agent_thought_chunk")
}
