package remotetools_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// TestMetricsCountCallsAndDenials locks the counter contract.
//
// Denied calls are counted separately from failed calls: a spike in denials means
// the allowlist/profile is wrong, while a spike in failures means the target is
// broken. Conflating them hides the difference from the operator.
func TestMetricsCountCallsAndDenials(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newOpsConfig(t, root, []string{"echo"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	metrics := server.Metrics()
	if metrics == nil {
		t.Fatal("expected a metrics counter")
	}
	session, ctx := startTestSession(t, server)

	// One allowed call.
	callTool(t, ctx, session, "remote_dev__view", map[string]interface{}{"file_path": "README.md"})
	// One denied call (command outside the allowlist).
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remote_dev__shell",
		Arguments: map[string]interface{}{"command": "whoami"},
	}); err != nil {
		t.Fatalf("call: %v", err)
	}

	snapshot := metrics.Snapshot()
	if snapshot.CallsTotal < 2 {
		t.Fatalf("expected at least 2 calls, got %d", snapshot.CallsTotal)
	}
	if snapshot.CallsDenied < 1 {
		t.Fatalf("expected at least 1 denied call, got %d", snapshot.CallsDenied)
	}
	view := snapshot.PerTool["remote_dev__view"]
	if view.Calls < 1 {
		t.Fatalf("expected per-tool counters for view, got %+v", snapshot.PerTool)
	}
}

// TestMetricsHTTPEndpoint covers the /metrics surface and its auth.
func TestMetricsHTTPEndpoint(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newTestConfig(t, root)
	cfg.HTTP.Listen = "127.0.0.1:0"
	// A token is optional on loopback, but set one so the auth path is exercised.
	t.Setenv("AICLI_REMOTE_TEST_TOKEN", "0123456789abcdef0123456789abcdef")
	cfg.HTTP.TokenEnv = "AICLI_REMOTE_TEST_TOKEN"

	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addrCh := make(chan string, 1)
	go func() { _ = server.RunHTTPWithListener(ctx, func(addr string) { addrCh <- addr }) }()

	var addr string
	select {
	case addr = <-addrCh:
	case <-time.After(5 * time.Second):
		t.Fatal("http server did not start")
	}

	// Without a token the endpoint must refuse.
	response, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", response.StatusCode)
	}

	// With the token it returns the snapshot.
	request, err := http.NewRequest(http.MethodGet, "http://"+addr+"/metrics", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer 0123456789abcdef0123456789abcdef")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("get metrics with token: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with token, got %d", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "\"target\"") {
		t.Fatalf("unexpected metrics payload: %s", body)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode metrics: %v", err)
	}
	if payload["target"] != "dev" {
		t.Fatalf("unexpected target: %v", payload["target"])
	}
}

// TestMetricsObserveIsBounded locks the per-tool map bound: an unexpected tool
// name must not grow the map without limit.
func TestMetricsObserveIsBounded(t *testing.T) {
	metrics := remotetools.NewMetrics()
	for index := 0; index < 500; index++ {
		name := "tool_" + strings.Repeat("x", index%20) + string(rune('a'+index%26))
		metrics.Observe(name, time.Millisecond, 0, false, false)
	}
	snapshot := metrics.Snapshot()
	if len(snapshot.PerTool) > 64 {
		t.Fatalf("per-tool map exceeded its bound: %d entries", len(snapshot.PerTool))
	}
	if snapshot.CallsTotal != 500 {
		t.Fatalf("total must still count every call, got %d", snapshot.CallsTotal)
	}
}