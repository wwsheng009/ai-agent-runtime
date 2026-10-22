//go:build ignore

// Command remotetools_probe talks to a live aicli-remote-tools endpoint with the
// raw Streamable HTTP protocol, so a scenario can be validated without the model
// in the loop. It exists to answer "does this exact command survive the target's
// policy?" before blaming the model for a refusal.
//
// Usage:
//
//	go run ./scripts/remotetools_probe.go -url ... -token ... -tool shell -arg command="curl -s ..."
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:18766/mcp", "MCP endpoint")
	token := flag.String("token", "", "bearer token")
	tool := flag.String("tool", "shell", "tool name")
	argJSON := flag.String("args", `{}`, "tool arguments as JSON")
	list := flag.Bool("list", false, "only list tools")
	flag.Parse()

	client := &http.Client{Timeout: 300 * time.Second}
	session := ""
	call := func(method string, params interface{}) json.RawMessage {
		body := map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": method}
		if params != nil {
			body["params"] = params
		}
		raw, _ := json.Marshal(body)
		req, err := http.NewRequest(http.MethodPost, *url, bytes.NewReader(raw))
		if err != nil {
			fail("build request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if *token != "" {
			req.Header.Set("Authorization", "Bearer "+*token)
		}
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		resp, err := client.Do(req)
		if err != nil {
			fail("request: %v", err)
		}
		defer resp.Body.Close()
		if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
			session = id
		}
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			fail("HTTP %d: %s", resp.StatusCode, string(raw))
		}
		return decodeSSE(resp.Body)
	}

	call("initialize", map[string]interface{}{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "probe", "version": "1"},
	})

	if *list {
		out := call("tools/list", map[string]interface{}{})
		fmt.Println(string(out))
		return
	}

	var args map[string]interface{}
	if err := json.Unmarshal([]byte(*argJSON), &args); err != nil {
		fail("bad -args: %v", err)
	}
	out := call("tools/call", map[string]interface{}{
		"name":      *tool,
		"arguments": args,
	})
	fmt.Println(string(out))
	if strings.Contains(string(out), `"isError":true`) || strings.Contains(string(out), `"success":false`) {
		os.Exit(1)
	}
}

// decodeSSE reads the first `data:` payload of a text/event-stream response.
func decodeSSE(body io.Reader) json.RawMessage {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var payload strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			if payload.Len() > 0 {
				payload.WriteByte('\n')
			}
			payload.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			continue
		}
		if line == "" && payload.Len() > 0 {
			return json.RawMessage(payload.String())
		}
	}
	return json.RawMessage(payload.String())
}

func fail(format string, args ...interface{}) {
	fmt.Printf("FAIL: "+format+"\n", args...)
	os.Exit(2)
}
