// Real end-to-end ACP verification of reasoning-effort switching against the
// compiled aicli binary. Runs aicli agent stdio with an isolated USERPROFILE and
// a local mock OpenAI-compatible streaming provider, then:
//  1. initialize + session/new, asserting the thought_level option is advertised
//     with the synthetic "default" currentValue
//  2. session/set_config_option thought_level=high, then a prompt, asserting the
//     upstream request body carries reasoning_effort="high"
//  3. session/set_config_option thought_level=default, then a prompt, asserting
//     the upstream request body no longer carries reasoning_effort
//
// Run: go run scripts/acp_e2e_thought_level.go <aicli.exe>
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

func writeMsg(w io.Writer, msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

// capturedBodies records the upstream request bodies in arrival order so the
// test can assert on what the provider actually received.
type capturedBodies struct {
	mu    sync.Mutex
	items []map[string]interface{}
}

func (c *capturedBodies) record(body map[string]interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = append(c.items, body)
}

func (c *capturedBodies) latest() (map[string]interface{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) == 0 {
		return nil, false
	}
	return c.items[len(c.items)-1], true
}

func mockProvider(captured *capturedBodies) http.HandlerFunc {
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
		emit(`{"id":"mock","object":"chat.completion.chunk","created":1,"model":"m1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
		emit(`{"id":"mock","object":"chat.completion.chunk","created":1,"model":"m1","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`)
		emit(`{"id":"mock","object":"chat.completion.chunk","created":1,"model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
}

type configOption struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Category     string `json:"category"`
	Type         string `json:"type"`
	CurrentValue string `json:"currentValue"`
	Options      []struct {
		Value string `json:"value"`
		Name  string `json:"name"`
	} `json:"options"`
}

func findOption(options []configOption, id string) (configOption, bool) {
	for _, option := range options {
		if option.ID == id {
			return option, true
		}
	}
	return configOption{}, false
}

func optionValues(option configOption) []string {
	values := make([]string, 0, len(option.Options))
	for _, choice := range option.Options {
		values = append(values, choice.Value)
	}
	return values
}

func hasValue(option configOption, want string) bool {
	for _, choice := range option.Options {
		if strings.EqualFold(choice.Value, want) {
			return true
		}
	}
	return false
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: acp_e2e_thought_level <aicli.exe>")
		os.Exit(2)
	}
	bin := os.Args[1]

	captured := &capturedBodies{}
	srv := httptest.NewServer(mockProvider(captured))
	defer srv.Close()

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
      model_capabilities:
        m1:
          reasoning_model: true
          reasoning_efforts:
            - low
            - medium
            - high
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
		if err := writeMsg(stdin, Message{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprintf("%d", e.nextID)), Method: method, Params: raw}); err != nil {
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

	// --- 2. session/new advertises thought_level ---
	res, err := call("session/new", map[string]interface{}{
		"cwd":        home,
		"mcpServers": []interface{}{},
	}, 30*time.Second)
	if err != nil {
		fail("session/new: %v", err)
	}
	var snew struct {
		SessionID     string         `json:"sessionId"`
		ConfigOptions []configOption `json:"configOptions"`
	}
	if err := json.Unmarshal(res, &snew); err != nil {
		fail("session/new result: %v", err)
	}
	option, ok := findOption(snew.ConfigOptions, "thought_level")
	if !ok {
		fail("thought_level option missing from session/new: %s", res)
	}
	if option.Category != "thought_level" || option.Type != "select" {
		fail("thought_level shape = category %q type %q", option.Category, option.Type)
	}
	if option.CurrentValue != "default" {
		fail("thought_level currentValue = %q, want default", option.CurrentValue)
	}
	for _, want := range []string{"default", "low", "medium", "high"} {
		if !hasValue(option, want) {
			fail("thought_level values = %v, missing %q", optionValues(option), want)
		}
	}
	fmt.Printf("OK session/new sessionId=%s thought_level=%v\n", snew.SessionID, optionValues(option))

	// --- 3. switch to high, prompt, assert upstream body ---
	res, err = call("session/set_config_option", map[string]interface{}{
		"sessionId": snew.SessionID,
		"configId":  "thought_level",
		"value":     "high",
	}, 20*time.Second)
	if err != nil {
		fail("set thought_level=high: %v", err)
	}
	var setResp struct {
		ConfigOptions []configOption `json:"configOptions"`
	}
	if err := json.Unmarshal(res, &setResp); err != nil {
		fail("set_config_option result: %v", err)
	}
	option, ok = findOption(setResp.ConfigOptions, "thought_level")
	if !ok {
		fail("thought_level option missing from set_config_option response: %s", res)
	}
	if option.CurrentValue != "high" {
		fail("thought_level currentValue after set = %q, want high", option.CurrentValue)
	}
	fmt.Println("OK session/set_config_option thought_level=high")

	if _, err := call("session/prompt", map[string]interface{}{
		"sessionId": snew.SessionID,
		"prompt":    []map[string]interface{}{{"type": "text", "text": "hello high"}},
	}, 60*time.Second); err != nil {
		fail("prompt after high: %v", err)
	}
	body, ok := captured.latest()
	if !ok {
		fail("provider received no request")
	}
	if got, _ := body["reasoning_effort"].(string); got != "high" {
		fail("upstream reasoning_effort = %#v, want high", body["reasoning_effort"])
	}
	fmt.Println("OK upstream request carries reasoning_effort=high")

	// --- 4. clear the override, prompt, assert the field disappears ---
	res, err = call("session/set_config_option", map[string]interface{}{
		"sessionId": snew.SessionID,
		"configId":  "thought_level",
		"value":     "default",
	}, 20*time.Second)
	if err != nil {
		fail("set thought_level=default: %v", err)
	}
	if err := json.Unmarshal(res, &setResp); err != nil {
		fail("set_config_option result: %v", err)
	}
	option, ok = findOption(setResp.ConfigOptions, "thought_level")
	if !ok {
		fail("thought_level option missing after clearing: %s", res)
	}
	if option.CurrentValue != "default" {
		fail("thought_level currentValue after clearing = %q, want default", option.CurrentValue)
	}
	fmt.Println("OK session/set_config_option thought_level=default")

	if _, err := call("session/prompt", map[string]interface{}{
		"sessionId": snew.SessionID,
		"prompt":    []map[string]interface{}{{"type": "text", "text": "hello default"}},
	}, 60*time.Second); err != nil {
		fail("prompt after default: %v", err)
	}
	body, ok = captured.latest()
	if !ok {
		fail("provider received no request after clearing")
	}
	if _, exists := body["reasoning_effort"]; exists {
		fail("upstream reasoning_effort = %#v after clearing, want absent", body["reasoning_effort"])
	}
	fmt.Println("OK upstream request omits reasoning_effort after clearing")

	// --- 5. unknown value is rejected ---
	if _, err := call("session/set_config_option", map[string]interface{}{
		"sessionId": snew.SessionID,
		"configId":  "thought_level",
		"value":     "ultra",
	}, 20*time.Second); err == nil {
		fail("thought_level=ultra was accepted, want invalid params")
	}
	fmt.Println("OK session/set_config_option rejects unknown effort")

	fmt.Println("E2E PASS")
}
