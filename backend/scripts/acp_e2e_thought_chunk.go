// Real end-to-end ACP verification of reasoning streaming (agent_thought_chunk)
// and messageId grouping against the compiled aicli binary. Runs aicli agent
// stdio with an isolated USERPROFILE and a local mock OpenAI-compatible
// streaming provider that emits reasoning_content deltas followed by content
// deltas, then:
//  1. initialize + session/new
//  2. session/prompt, asserting the stream produced agent_thought_chunk updates
//     carrying the reasoning text and a "<id>_thought" messageId
//  3. asserting all agent_message_chunk updates share one non-empty messageId,
//     that it differs from the thought id, and that no answer chunk leaks
//     reasoning text
//  4. session/load, asserting replayed user/assistant chunks carry distinct
//     non-empty messageIds and that stored reasoning is replayed as
//     agent_thought_chunk before the answer, with a "<id>_thought" messageId
//     (plan 4.6/4.7 replay closure)
//
// Run: go run scripts/acp_e2e_thought_chunk.go <aicli.exe>

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

func writeMsg(w io.Writer, msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
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

func mockProvider() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		emit := func(payload string) {
			fmt.Fprintf(w, "data: %s\n\n", payload)
			if flusher != nil {
				flusher.Flush()
			}
		}
		emit(`{"id":"mock","object":"chat.completion.chunk","created":1,"model":"m1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
		emit(`{"id":"mock","object":"chat.completion.chunk","created":1,"model":"m1","choices":[{"index":0,"delta":{"reasoning_content":"先确认"},"finish_reason":null}]}`)
		emit(`{"id":"mock","object":"chat.completion.chunk","created":1,"model":"m1","choices":[{"index":0,"delta":{"reasoning_content":"需求。"},"finish_reason":null}]}`)
		emit(`{"id":"mock","object":"chat.completion.chunk","created":1,"model":"m1","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`)
		emit(`{"id":"mock","object":"chat.completion.chunk","created":1,"model":"m1","choices":[{"index":0,"delta":{"content":" chunk"},"finish_reason":null}]}`)
		emit(`{"id":"mock","object":"chat.completion.chunk","created":1,"model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
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

func firstKindIndex(kinds []string, kind string) int {
	for i, k := range kinds {
		if k == kind {
			return i
		}
	}
	return -1
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: acp_e2e_thought_chunk <aicli.exe>")
		os.Exit(2)
	}
	bin := os.Args[1]

	srv := httptest.NewServer(mockProvider())
	defer srv.Close()

	home, err := os.MkdirTemp("", "acp-e2e-thought")
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

	// --- 3. prompt: reasoning -> agent_thought_chunk, content -> message chunk ---
	if _, err := call("session/prompt", map[string]interface{}{
		"sessionId": snew.SessionID,
		"prompt":    []map[string]interface{}{{"type": "text", "text": "explain"}},
	}, 60*time.Second); err != nil {
		fail("session/prompt: %v", err)
	}

	var thoughtText, answerText strings.Builder
	thoughtIDs := map[string]bool{}
	answerIDs := map[string]bool{}
	for _, params := range notifications.snapshot() {
		switch updateField(params, "sessionUpdate") {
		case "agent_thought_chunk":
			thoughtText.WriteString(updateText(params))
			thoughtIDs[updateField(params, "messageId")] = true
		case "agent_message_chunk":
			answerText.WriteString(updateText(params))
			answerIDs[updateField(params, "messageId")] = true
		}
	}
	if thoughtText.Len() == 0 {
		fail("no agent_thought_chunk received for reasoning stream")
	}
	if got := thoughtText.String(); got != "先确认需求。" {
		fail("thought text = %q, want 先确认需求。", got)
	}
	if len(thoughtIDs) != 1 {
		fail("thought chunks messageIds = %v, want one shared id", thoughtIDs)
	}
	var thoughtID string
	for id := range thoughtIDs {
		thoughtID = id
	}
	if thoughtID == "" || !strings.HasSuffix(thoughtID, "_thought") {
		fail("thought messageId = %q, want <turn>_thought", thoughtID)
	}
	if answerText.Len() == 0 {
		fail("no agent_message_chunk received for answer stream")
	}
	if got := answerText.String(); got != "hello chunk" {
		fail("answer text = %q, want hello chunk", got)
	}
	if strings.Contains(answerText.String(), "先确认") || strings.Contains(answerText.String(), "需求。") {
		fail("answer chunk leaked reasoning text: %q", answerText.String())
	}
	if len(answerIDs) != 1 {
		fail("answer chunks messageIds = %v, want one shared id", answerIDs)
	}
	var answerID string
	for id := range answerIDs {
		answerID = id
	}
	if answerID == "" {
		fail("answer chunks missing messageId")
	}
	if answerID == thoughtID {
		fail("answer and thought share messageId %q, want distinct ids", answerID)
	}
	fmt.Printf("OK prompt stream thought=%q(%s) answer=%q(%s)\n", thoughtText.String(), thoughtID, answerText.String(), answerID)

	// --- 4. session/load replay carries messageIds ---
	before := len(notifications.snapshot())
	if _, err := call("session/load", map[string]interface{}{
		"sessionId":  snew.SessionID,
		"cwd":        home,
		"mcpServers": []interface{}{},
	}, 30*time.Second); err != nil {
		fail("session/load: %v", err)
	}
	var replayUserIDs, replayAssistantIDs []string
	var replayKinds []string
	var replayThoughtText strings.Builder
	replayThoughtIDs := map[string]bool{}
	for _, params := range notifications.snapshot()[before:] {
		kind := updateField(params, "sessionUpdate")
		replayKinds = append(replayKinds, kind)
		switch kind {
		case "user_message_chunk":
			replayUserIDs = append(replayUserIDs, updateField(params, "messageId"))
		case "agent_message_chunk":
			replayAssistantIDs = append(replayAssistantIDs, updateField(params, "messageId"))
		case "agent_thought_chunk":
			replayThoughtText.WriteString(updateText(params))
			replayThoughtIDs[updateField(params, "messageId")] = true
		}
	}
	if len(replayUserIDs) == 0 {
		fail("session/load replay emitted no user_message_chunk")
	}
	if len(replayAssistantIDs) == 0 {
		fail("session/load replay emitted no agent_message_chunk")
	}
	for i, id := range append(append([]string{}, replayUserIDs...), replayAssistantIDs...) {
		if id == "" {
			fail("replayed chunk %d missing messageId (user=%v assistant=%v)", i, replayUserIDs, replayAssistantIDs)
		}
	}
	if replayUserIDs[0] == replayAssistantIDs[0] {
		fail("replay user/assistant share messageId %q", replayUserIDs[0])
	}
	if got := replayThoughtText.String(); !strings.Contains(got, "先确认需求。") {
		fail("replayed thought text = %q, want to contain 先确认需求。", got)
	}
	if strings.Contains(replayThoughtText.String(), "hello") {
		fail("replayed thought chunk leaked answer text: %q", replayThoughtText.String())
	}
	if len(replayThoughtIDs) != 1 {
		fail("replayed thought messageIds = %v, want one shared id", replayThoughtIDs)
	}
	for id := range replayThoughtIDs {
		if id == "" || !strings.HasSuffix(id, "_thought") {
			fail("replayed thought messageId = %q, want <turn>_thought", id)
		}
		if id == replayAssistantIDs[0] {
			fail("replayed thought/assistant share messageId %q", id)
		}
	}
	thoughtIdx := firstKindIndex(replayKinds, "agent_thought_chunk")
	answerIdx := firstKindIndex(replayKinds, "agent_message_chunk")
	if thoughtIdx < 0 || answerIdx < 0 || thoughtIdx > answerIdx {
		fail("replayed thought chunk must precede answer (thought=%d answer=%d, kinds=%v)", thoughtIdx, answerIdx, replayKinds)
	}
	fmt.Printf("OK session/load replay messageIds user=%v assistant=%v thought=%v\n", replayUserIDs, replayAssistantIDs, replayThoughtIDs)

	fmt.Println("E2E PASS")
}
