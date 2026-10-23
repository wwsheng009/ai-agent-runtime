package remotetools_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// mcpHTTPClient drives one MCP tool call over the streamable HTTP endpoint.
//
// It exists because the bearer principal only materializes on the HTTP path: the
// in-memory stdio session used by the other audit tests has no token to hash, so
// without this the "no secret in the log" assertion could pass merely because
// nothing ever attributed the call.
type mcpHTTPClient struct {
	t         *testing.T
	url       string
	token     string
	sessionID string
}

func newMCPHTTPClient(t *testing.T, addr, token string) *mcpHTTPClient {
	t.Helper()
	return &mcpHTTPClient{t: t, url: "http://" + addr + "/mcp", token: token}
}

// post sends one JSON-RPC frame and returns the unwrapped payload plus the
// Mcp-Session-Id header, which the SDK reports out-of-band from the body.
func (c *mcpHTTPClient) post(payload map[string]interface{}) (string, string) {
	c.t.Helper()
	isNotification := payload["method"] == "notifications/initialized"
	body, err := json.Marshal(payload)
	if err != nil {
		c.t.Fatalf("encode %v: %v", payload["method"], err)
	}
	req, err := http.NewRequest(http.MethodPost, c.url, strings.NewReader(string(body)))
	if err != nil {
		c.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.t.Fatalf("post %v: %v", payload["method"], err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Notifications are acknowledged with 202 Accepted and no body; requests
	// expect 200 with a JSON/SSE payload.
	if isNotification {
		if resp.StatusCode != http.StatusAccepted {
			c.t.Fatalf("post %v: status = %d, want 202", payload["method"], resp.StatusCode)
		}
		return "", c.sessionID
	}
	if resp.StatusCode != http.StatusOK {
		c.t.Fatalf("post %v: status = %d", payload["method"], resp.StatusCode)
	}
	session := resp.Header.Get("Mcp-Session-Id")
	if session != "" {
		c.sessionID = session
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("read body: %v", err)
	}
	// Streamable HTTP answers with SSE frames; tolerate a plain JSON body too.
	for _, line := range strings.Split(string(raw), "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "data:") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")), session
		}
	}
	return strings.TrimSpace(string(raw)), session
}

// callShell initializes a session and runs one remote shell command.
func (c *mcpHTTPClient) callShell(command string) {
	c.t.Helper()
	initResponse, _ := c.post(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]interface{}{},
			"clientInfo":      map[string]interface{}{"name": "audit-hygiene", "version": "1"},
		},
	})
	if !strings.Contains(initResponse, "result") {
		c.t.Fatalf("initialize returned no result: %s", initResponse)
	}
	if c.sessionID == "" {
		c.t.Fatal("server returned no Mcp-Session-Id")
	}
	c.post(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
		"params":  map[string]interface{}{},
	})

	callResponse, _ := c.post(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      "remote_dev__shell",
			"arguments": map[string]interface{}{"command": command},
		},
	})
	if strings.Contains(callResponse, `"error"`) {
		c.t.Fatalf("tools/call failed: %s", callResponse)
	}
}

// TestAuditLogsCarryNoSecret closes the T6 gap in the threat model: the audit and
// access sinks must store hashes, labels and byte counts — never the bearer
// credential, and never the command output that may itself contain a secret.
func TestAuditLogsCarryNoSecret(t *testing.T) {
	root := newTestWorkspace(t)
	auditPath := filepath.Join(t.TempDir(), "audit.ndjson")
	accessPath := filepath.Join(t.TempDir(), "access.ndjson")

	token := strings.Repeat("k", 48)
	t.Setenv("AICLI_TEST_SECRET_TOKEN", token)
	// A distinctive value the command prints, standing in for a credential that
	// shows up in a tool result.
	const outputSecret = "sk-live-0123456789abcdef"

	cfg := newOpsConfig(t, root, []string{"echo"})
	cfg.Audit.Path = auditPath
	// The default privacy posture: preview stays off unless explicitly raised.
	cfg.Audit.CommandPreviewBytes = 0
	cfg.HTTP.Listen = "127.0.0.1:0"
	cfg.HTTP.TokenEnv = "AICLI_TEST_SECRET_TOKEN"
	cfg.HTTP.AccessLog = accessPath

	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	// Safety net for early Fatalf paths: on Windows an open audit file makes
	// TempDir cleanup fail, which would mask the real assertion.
	t.Cleanup(func() { _ = server.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addrCh := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.RunHTTPWithListener(ctx, func(addr string) { addrCh <- addr })
	}()

	var addr string
	select {
	case addr = <-addrCh:
	case <-time.After(5 * time.Second):
		t.Fatal("http server did not start")
	}

	newMCPHTTPClient(t, addr, token).callShell("echo " + outputSecret)

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("http server did not stop")
	}
	if err := server.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	audit := readFileString(t, auditPath)
	access := readFileString(t, accessPath)

	if !strings.Contains(audit, "shell") {
		t.Fatalf("expected a shell audit record, got: %s", audit)
	}
	// Negative assertions first: neither sink may contain the credential or the
	// echoed secret.
	for _, sink := range []struct {
		name  string
		value string
	}{
		{"audit", audit},
		{"access", access},
	} {
		if strings.Contains(sink.value, token) {
			t.Fatalf("%s log leaked the bearer token: %s", sink.name, sink.value)
		}
		if strings.Contains(sink.value, outputSecret) {
			t.Fatalf("%s log leaked command output: %s", sink.name, sink.value)
		}
	}

	// Positive guard: the call really was attributed, so the checks above are not
	// passing because the request was dropped or never recorded.
	if !strings.Contains(audit, `"principal":"bearer:`) {
		t.Fatalf("audit record is missing the hashed bearer principal: %s", audit)
	}
	if !strings.Contains(audit, `"command_hash"`) {
		t.Fatalf("audit record is missing the command hash: %s", audit)
	}
	// The command must have actually run and produced output, otherwise "the
	// output is not in the log" would be vacuously true.
	if !strings.Contains(audit, `"decision":"allow"`) {
		t.Fatalf("expected an allowed shell decision: %s", audit)
	}
	if !strings.Contains(audit, `"bytes_out":`) {
		t.Fatalf("expected recorded output bytes (the command echoed a secret): %s", audit)
	}
	if strings.Contains(audit, `"command_preview"`) {
		t.Fatalf("command preview must stay off at the default (previewBytes=0): %s", audit)
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
