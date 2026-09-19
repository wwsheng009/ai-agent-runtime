// Live ACP verification against a real provider (no mock).
// Usage: go run scripts/acp_live_prompt.go <aicli.exe> <provider> <model> [prompt]
// Performs initialize -> session/new -> session/prompt over real stdio,
// prints session/update events, and asserts stopReason == "end_turn".
//
// Note: `go run` this file individually (not `go run ./scripts/`) — the
// scripts directory holds several standalone main programs that would
// collide on package-level names. Alternatively use `go run scripts/acp_live_prompt.go`.

//go:build ignore

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
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
	mu          sync.Mutex
	nextID      int
	pending     map[string]chan Message
	updatelines []string
}

func main() {
	if len(os.Args) < 4 {
		fmt.Println("usage: acp_live_prompt <aicli.exe> <provider> <model> [prompt]")
		os.Exit(2)
	}
	bin, provider, model := os.Args[1], os.Args[2], os.Args[3]
	prompt := "Reply with exactly one line: ACP-LIVE-OK and nothing else."
	if len(os.Args) >= 5 {
		prompt = os.Args[4]
	}

	cwd, _ := os.Getwd()
	cmd := exec.Command(bin, "agent", "stdio",
		"-P", provider, "-m", model,
		"--reasoning-effort", "medium",
		"--ephemeral", "--yolo",
		"--log-dir", filepath.Join(os.TempDir(), "acp-live-logs"),
	)
	cmd.Dir = cwd
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		fmt.Printf("FAIL start: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	e := &e2e{pending: map[string]chan Message{}}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		r := bufio.NewReader(stdout)
		for {
			line, err := r.ReadString('\n')
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
			if msg.ID != nil && len(msg.Method) == 0 {
				e.mu.Lock()
				ch := e.pending[string(msg.ID)]
				e.mu.Unlock()
				if ch != nil {
					ch <- msg
				}
				continue
			}
			if msg.Method == "session/update" {
				e.mu.Lock()
				// Keep the raw line; the agent_message_chunk assertion and
				// display both work off verbatim protocol evidence.
				e.updatelines = append(e.updatelines, line)
				e.mu.Unlock()
			}
		}
	}()
	go io.Copy(io.Discard, stderr)

	call := func(method string, params interface{}, timeout time.Duration) (json.RawMessage, error) {
		raw, _ := json.Marshal(params)
		ch := make(chan Message, 1)
		e.mu.Lock()
		e.nextID++
		id := e.nextID
		e.pending[fmt.Sprintf("%d", id)] = ch
		e.mu.Unlock()
		if err := writeMsg(stdin, Message{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprintf("%d", id)), Method: method, Params: raw}); err != nil {
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

	res, err := call("session/new", map[string]interface{}{"cwd": cwd}, 30*time.Second)
	if err != nil {
		fmt.Printf("FAIL session/new: %v\n", err)
		os.Exit(1)
	}
	var snew struct {
		SessionID string `json:"sessionId"`
	}
	json.Unmarshal(res, &snew)
	fmt.Printf("OK session/new sessionId=%s\n", snew.SessionID)

	start := time.Now()
	res, err = call("session/prompt", map[string]interface{}{
		"sessionId": snew.SessionID,
		"prompt":    []map[string]interface{}{{"type": "text", "text": prompt}},
	}, 180*time.Second)
	elapsed := time.Since(start)

	e.mu.Lock()
	updates := append([]string(nil), e.updatelines...)
	e.mu.Unlock()
	for _, u := range updates {
		fmt.Printf("  update: %s\n", truncate(u, 220))
	}
	if err != nil {
		fmt.Printf("FAIL session/prompt: %v\n", err)
		os.Exit(1)
	}
	var pr struct {
		StopReason string `json:"stopReason"`
	}
	json.Unmarshal(res, &pr)
	fmt.Printf("RESULT prompt stopReason=%q elapsed=%s\n", pr.StopReason, elapsed.Round(time.Millisecond))
	if pr.StopReason != "end_turn" {
		fmt.Println("E2E FAIL: unexpected stopReason")
		os.Exit(1)
	}
	// Assert assistant text was streamed via session/update.
	joined := strings.Join(updates, "\n")
	if !strings.Contains(joined, "agent_message_chunk") {
		fmt.Println("E2E FAIL: no agent_message_chunk streamed")
		os.Exit(1)
	}
	fmt.Println("E2E PASS")
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func writeMsg(w io.Writer, msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}
