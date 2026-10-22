package remotetools_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// TestHTTPAccessLogRecordsStatuses proves the access log records both rejected
// and successful requests, with the peer address, without any Authorization
// header.
func TestHTTPAccessLogRecordsStatuses(t *testing.T) {
	root := newTestWorkspace(t)
	accessPath := filepath.Join(t.TempDir(), "access.ndjson")
	token := strings.Repeat("a", 48)
	t.Setenv("AICLI_TEST_TOKEN", token)

	cfg := newOpsConfig(t, root, []string{"echo"})
	cfg.HTTP.Listen = "127.0.0.1:0"
	cfg.HTTP.AccessLog = accessPath
	cfg.HTTP.TokenEnv = "AICLI_TEST_TOKEN"

	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
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
		cancel()
		t.Fatal("http server did not start")
	}

	resp, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		cancel()
		t.Fatalf("unauthorized request: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without token: status = %d, want 401", resp.StatusCode)
	}
	_ = resp.Body.Close()

	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/metrics", nil)
	if err != nil {
		cancel()
		t.Fatalf("build authorized request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("authorized request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("with token: status = %d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("http server did not stop")
	}
	if err := server.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := os.ReadFile(accessPath)
	if err != nil {
		t.Fatalf("read access log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 access records, got %d: %s", len(lines), raw)
	}
	statuses := map[int]bool{}
	for _, line := range lines {
		var record map[string]interface{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode access record: %v", err)
		}
		if record["path"] != "/metrics" {
			t.Fatalf("path = %v", record["path"])
		}
		if record["remote_addr"] == nil || record["remote_addr"] == "" {
			t.Fatal("remote_addr is missing")
		}
		if status, ok := record["status"].(float64); ok {
			statuses[int(status)] = true
		}
		if strings.Contains(line, token) {
			t.Fatal("access log leaked the bearer token")
		}
	}
	if !statuses[http.StatusUnauthorized] || !statuses[http.StatusOK] {
		t.Fatalf("statuses = %v, want 401 and 200", statuses)
	}
}
