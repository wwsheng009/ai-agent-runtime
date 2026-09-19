// Minimal raw ACP trace: initialize -> session/new -> session/prompt,
// dumping every stdout line verbatim.

//go:build ignore

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
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

func main() {
	bin := os.Args[1]
	cwd, _ := os.Getwd()
	cmd := exec.Command(bin, "agent", "stdio",
		"-P", "unsee", "-m", "glm-5.3-flash",
		"--reasoning-effort", "medium",
		"--ephemeral", "--yolo",
	)
	cmd.Dir = cwd
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(1)
	}
	defer func() {
		stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	lines := make(chan string, 256)
	go func() {
		r := bufio.NewReader(stdout)
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				close(lines)
				return
			}
			lines <- strings.TrimSpace(l)
		}
	}()
	go io.Copy(io.Discard, stderr)

	call := func(method string, params interface{}, timeout time.Duration) json.RawMessage {
		raw, _ := json.Marshal(params)
		var id json.RawMessage
		// use incrementing numeric ids
		callSeq++
		id = json.RawMessage(fmt.Sprintf("%d", callSeq))
		fmt.Fprintf(stdin, "%s\n", mustJSON(Message{JSONRPC: "2.0", ID: id, Method: method, Params: raw}))
		deadline := time.After(timeout)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					fmt.Println("STDOUT CLOSED")
					os.Exit(1)
				}
				var msg Message
				if json.Unmarshal([]byte(l), &msg) == nil && msg.ID != nil && string(msg.ID) == string(id) {
					return msg.Result
				}
				// print non-matching lines (updates etc.) compactly
				fmt.Println("LINE:", truncate(l, 400))
			case <-deadline:
				fmt.Println("TIMEOUT waiting", method)
				os.Exit(1)
			}
		}
	}
	callSeq = 0
	_ = call("initialize", map[string]interface{}{"protocolVersion": 1, "clientCapabilities": map[string]interface{}{"fs": map[string]interface{}{"readTextFile": false, "writeTextFile": false}, "terminal": false}}, 20*time.Second)
	fmt.Println("OK initialize")
	res := call("session/new", map[string]interface{}{"cwd": cwd}, 30*time.Second)
	fmt.Println("OK session/new:", string(res))
	var snew struct {
		SessionID string `json:"sessionId"`
	}
	json.Unmarshal(res, &snew)
	start := time.Now()
	res = call("session/prompt", map[string]interface{}{
		"sessionId": snew.SessionID,
		"prompt":    []map[string]interface{}{{"type": "text", "text": "Reply with exactly one line: ACP-LIVE-OK and nothing else."}},
	}, 180*time.Second)
	fmt.Printf("RESULT (%s): %s\n", time.Since(start).Round(time.Millisecond), string(res))
}

var callSeq int

func mustJSON(m Message) string {
	b, _ := json.Marshal(m)
	return string(b)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
