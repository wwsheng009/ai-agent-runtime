// Real end-to-end ACP verification of multimodal (image / embedded resource)
// prompts against the compiled aicli binary. Runs aicli agent stdio with an
// isolated USERPROFILE and a local mock OpenAI-compatible streaming provider,
// then:
//  1. initialize, asserting promptCapabilities advertises image + embeddedContext
//     (and never audio)
//  2. session/prompt carrying text + an embedded text resource (the shape Zed
//     uses for editor selection / branch diff) + an image block, asserting the
//     upstream provider request carries both the inlined resource text and the
//     image as a data URL
//  3. an image-only prompt (no text), asserting the carrier prompt still reaches
//     the provider with the image attached
//  4. an audio block, asserting the turn is rejected instead of silently dropped
//
// Run: go run scripts/acp_e2e_image.go <aicli.exe>

//go:build ignore

package main

import (
	"bufio"
	"context"
	"encoding/base64"
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
		emit(`{"id":"mock","object":"chat.completion.chunk","created":1,"model":"m1","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`)
		emit(`{"id":"mock","object":"chat.completion.chunk","created":1,"model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
}

// lastUserMessage returns the final user message of the captured request body.
func lastUserMessage(body map[string]interface{}) (map[string]interface{}, bool) {
	messages, _ := body["messages"].([]interface{})
	for index := len(messages) - 1; index >= 0; index-- {
		message, _ := messages[index].(map[string]interface{})
		if message == nil {
			continue
		}
		if role, _ := message["role"].(string); strings.EqualFold(role, "user") {
			return message, true
		}
	}
	return nil, false
}

// messageText joins every text part of a user message (string content included).
func messageText(message map[string]interface{}) string {
	switch content := message["content"].(type) {
	case string:
		return content
	case []interface{}:
		var builder strings.Builder
		for _, rawPart := range content {
			part, _ := rawPart.(map[string]interface{})
			if part == nil {
				continue
			}
			if text, _ := part["text"].(string); text != "" {
				builder.WriteString(text)
			}
		}
		return builder.String()
	}
	return ""
}

// messageImageURLs collects image_url data URLs from a user message.
func messageImageURLs(message map[string]interface{}) []string {
	content, _ := message["content"].([]interface{})
	urls := make([]string, 0, len(content))
	for _, rawPart := range content {
		part, _ := rawPart.(map[string]interface{})
		if part == nil {
			continue
		}
		if partType, _ := part["type"].(string); partType != "image_url" {
			continue
		}
		switch imageURL := part["image_url"].(type) {
		case string:
			if imageURL != "" {
				urls = append(urls, imageURL)
			}
		case map[string]interface{}:
			if url, _ := imageURL["url"].(string); url != "" {
				urls = append(urls, url)
			}
		}
	}
	return urls
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: acp_e2e_image <aicli.exe>")
		os.Exit(2)
	}
	bin := os.Args[1]

	// Minimal PNG signature + payload; the runtime sniffs the staged file, so
	// the bytes (not the declared mime) decide whether the image is accepted.
	pngBytes := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, []byte("acp-e2e-image")...)
	pngBase64 := base64.StdEncoding.EncodeToString(pngBytes)

	captured := &capturedBodies{}
	srv := httptest.NewServer(mockProvider(captured))
	defer srv.Close()

	home, err := os.MkdirTemp("", "acp-e2e-image-home")
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
          input_modalities:
            - text
            - image
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

	// --- 1. initialize advertises the multimodal prompt capabilities ---
	initRes, err := call("initialize", map[string]interface{}{
		"protocolVersion":    1,
		"clientCapabilities": map[string]interface{}{"terminal": false},
	}, 20*time.Second)
	if err != nil {
		fail("initialize: %v", err)
	}
	var initResp struct {
		AgentCapabilities struct {
			PromptCapabilities struct {
				Image           bool `json:"image"`
				Audio           bool `json:"audio"`
				EmbeddedContext bool `json:"embeddedContext"`
			} `json:"promptCapabilities"`
		} `json:"agentCapabilities"`
	}
	if err := json.Unmarshal(initRes, &initResp); err != nil {
		fail("initialize result: %v", err)
	}
	caps := initResp.AgentCapabilities.PromptCapabilities
	if !caps.Image || !caps.EmbeddedContext {
		fail("promptCapabilities = %+v, want image and embeddedContext on", caps)
	}
	if caps.Audio {
		fail("promptCapabilities = %+v, audio must stay off", caps)
	}
	fmt.Println("OK initialize advertises image + embeddedContext prompt capabilities")

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
	if err := json.Unmarshal(res, &snew); err != nil {
		fail("session/new result: %v", err)
	}
	fmt.Printf("OK session/new sessionId=%s\n", snew.SessionID)

	// --- 3. text + embedded resource (selection / branch diff) + image ---
	selectionText := "fn main() { println!(\"selection\"); }"
	if _, err := call("session/prompt", map[string]interface{}{
		"sessionId": snew.SessionID,
		"prompt": []map[string]interface{}{
			{"type": "text", "text": "review this change"},
			{"type": "resource", "resource": map[string]interface{}{
				"uri":      "file:///repo/src/main.rs",
				"mimeType": "text/x-rust",
				"text":     selectionText,
			}},
			{"type": "image", "data": pngBase64, "mimeType": "image/png"},
		},
	}, 60*time.Second); err != nil {
		fail("prompt with image + resource: %v", err)
	}
	body, ok := captured.latest()
	if !ok {
		fail("provider received no request")
	}
	userMessage, ok := lastUserMessage(body)
	if !ok {
		fail("upstream request has no user message: %#v", body["messages"])
	}
	text := messageText(userMessage)
	if !strings.Contains(text, "review this change") || !strings.Contains(text, selectionText) {
		fail("upstream user text = %q, want prompt text and inlined resource text", text)
	}
	urls := messageImageURLs(userMessage)
	if len(urls) != 1 {
		fail("upstream image parts = %v, want exactly one", urls)
	}
	if wantPrefix := "data:image/png;base64,"; !strings.HasPrefix(urls[0], wantPrefix) || !strings.Contains(urls[0], pngBase64) {
		fail("upstream image data URL = %q, want prefix %q carrying the sent payload", urls[0], wantPrefix)
	}
	fmt.Println("OK upstream request carries inlined resource text + image data URL")

	// --- 4. image-only prompt still reaches the provider ---
	if _, err := call("session/prompt", map[string]interface{}{
		"sessionId": snew.SessionID,
		"prompt": []map[string]interface{}{
			{"type": "image", "data": pngBase64, "mimeType": "image/png"},
		},
	}, 60*time.Second); err != nil {
		fail("image-only prompt: %v", err)
	}
	body, ok = captured.latest()
	if !ok {
		fail("provider received no request for the image-only prompt")
	}
	userMessage, ok = lastUserMessage(body)
	if !ok {
		fail("image-only request has no user message")
	}
	if urls := messageImageURLs(userMessage); len(urls) != 1 {
		fail("image-only upstream image parts = %v, want exactly one", urls)
	}
	if text := messageText(userMessage); strings.TrimSpace(text) == "" {
		fail("image-only upstream user text is empty, want the carrier prompt")
	}
	fmt.Println("OK image-only prompt reaches the provider with the carrier prompt")

	// --- 5. audio blocks are rejected, never silently dropped ---
	_, err = call("session/prompt", map[string]interface{}{
		"sessionId": snew.SessionID,
		"prompt": []map[string]interface{}{
			{"type": "audio", "data": pngBase64, "mimeType": "audio/wav"},
		},
	}, 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "audio prompts are not supported") {
		fail("audio prompt error = %v, want an explicit rejection", err)
	}
	fmt.Println("OK audio prompt rejected with an explicit error")

	fmt.Println("E2E PASS")
}
