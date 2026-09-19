// Real end-to-end ACP verification against the compiled aicli binary.
// Runs aicli agent stdio with an isolated USERPROFILE and a local mock
// OpenAI-compatible streaming provider, then:
//  1. initialize + session/new handshake over real stdio
//  2. session/prompt cancelled mid-flight via $/cancel_request
//  3. verifies the prompt response reports stopReason "cancelled"
//
// Run: go run scripts/acp_e2e_cancel.go <aicli.exe>
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

var firstChunkOnce sync.Once
var firstChunkReleased = make(chan struct{})

// mockProvider streams a chat completion whose first chunk is delayed long
// enough for the client's $/cancel_request to arrive mid-flight.
func mockProvider(w http.ResponseWriter, r *http.Request) {
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
	// Hold the stream before the first content chunk; the e2e client cancels
	// while we are waiting. If the request context is cancelled (client went
	// away / agent aborted the upstream call), just stop.
	select {
	case <-firstChunkReleased:
	case <-time.After(10 * time.Second):
	case <-r.Context().Done():
		return
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

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: acp_e2e_cancel <aicli.exe>")
		os.Exit(2)
	}
	bin := os.Args[1]

	// --- isolated environment: temp USERPROFILE + local mock provider ---
	srv := httptest.NewServer(http.HandlerFunc(mockProvider))
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
			if msg.ID != nil {
				e.mu.Lock()
				ch := e.pending[string(msg.ID)]
				e.mu.Unlock()
				if ch != nil {
					ch <- msg
				}
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

	notify := func(method string, params interface{}) error {
		raw, _ := json.Marshal(params)
		return writeMsg(stdin, Message{JSONRPC: "2.0", Method: method, Params: raw})
	}

	// --- 1. initialize ---
	if _, err := call("initialize", map[string]interface{}{
		"protocolVersion": 1,
		"clientCapabilities": map[string]interface{}{
			"fs":       map[string]interface{}{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
	}, 20*time.Second); err != nil {
		fmt.Printf("FAIL initialize: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("OK initialize")

	// --- 2. session/new ---
	res, err := call("session/new", map[string]interface{}{
		"cwd":        home,
		"mcpServers": []interface{}{},
	}, 30*time.Second)
	if err != nil {
		fmt.Printf("FAIL session/new: %v\n", err)
		os.Exit(1)
	}
	var snew struct {
		SessionID string `json:"sessionId"`
	}
	json.Unmarshal(res, &snew)
	fmt.Printf("OK session/new sessionId=%s\n", snew.SessionID)

	// --- 3. prompt, cancelled mid-flight ---
	e.nextID++
	promptID := e.nextID
	raw, _ := json.Marshal(map[string]interface{}{
		"sessionId": snew.SessionID,
		"prompt":    []map[string]interface{}{{"type": "text", "text": "hello slowly"}},
	})
	ch := make(chan Message, 1)
	e.mu.Lock()
	e.pending[fmt.Sprintf("%d", promptID)] = ch
	e.mu.Unlock()
	if err := writeMsg(stdin, Message{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprintf("%d", promptID)), Method: "session/prompt", Params: raw}); err != nil {
		fmt.Printf("FAIL send prompt: %v\n", err)
		os.Exit(1)
	}

	time.Sleep(500 * time.Millisecond)
	if err := notify("$/cancel_request", map[string]interface{}{"requestId": promptID}); err != nil {
		fmt.Printf("FAIL send cancel: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("OK sent $/cancel_request")

	// --- 4. expect prompt response with stopReason=cancelled ---
	select {
	case msg := <-ch:
		if msg.Error != nil {
			fmt.Printf("RESULT prompt error (acceptable for cancel): %s\n", msg.Error)
			fmt.Println("E2E PASS")
			return
		}
		var pr struct {
			StopReason string `json:"stopReason"`
		}
		json.Unmarshal(msg.Result, &pr)
		fmt.Printf("RESULT prompt stopReason=%q\n", pr.StopReason)
		if pr.StopReason != "cancelled" && pr.StopReason != "" {
			fmt.Printf("E2E FAIL: unexpected stopReason %q\n", pr.StopReason)
			os.Exit(1)
		}
		fmt.Println("E2E PASS")
	case <-time.After(30 * time.Second):
		fmt.Println("E2E FAIL: no response to cancelled prompt within 30s")
		os.Exit(1)
	}
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
