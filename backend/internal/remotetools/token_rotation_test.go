package remotetools_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// startTokenServer binds an HTTP listener for one bearer token and returns its
// address. The token is captured by NewServer (it is read from the environment
// once per process), so rotating a token means constructing a new server — which
// is exactly the property this file pins.
func startTokenServer(t *testing.T, tokenEnvValue string) string {
	t.Helper()
	root := newTestWorkspace(t)
	accessPath := filepath.Join(t.TempDir(), "access.ndjson")
	t.Setenv("AICLI_TEST_ROTATION_TOKEN", tokenEnvValue)

	cfg := newOpsConfig(t, root, []string{"echo"})
	cfg.HTTP.Listen = "127.0.0.1:0"
	cfg.HTTP.TokenEnv = "AICLI_TEST_ROTATION_TOKEN"
	cfg.HTTP.AccessLog = accessPath

	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	addrCh := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.RunHTTPWithListener(ctx, func(addr string) { addrCh <- addr })
	}()
	select {
	case addr := <-addrCh:
		t.Cleanup(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("http server did not stop")
			}
		})
		return addr
	case <-time.After(5 * time.Second):
		t.Fatal("http server did not start")
		return ""
	}
}

// metricsStatus probes /metrics with an optional bearer token.
func metricsStatus(t *testing.T, addr, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/metrics", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain so the connection is released for reuse by the next probe.
	_, _ = io.ReadAll(resp.Body)
	return resp.StatusCode
}

// TestBearerTokenRotationStopsAcceptingTheOldToken covers the T12 rotation
// property: a bearer credential is captured per process, so a restarted target
// must reject the previous token outright instead of honouring a replay window.
func TestBearerTokenRotationStopsAcceptingTheOldToken(t *testing.T) {
	const (
		oldToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		newToken = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)

	// Phase 1: the old token is the live credential.
	first := startTokenServer(t, oldToken)
	if got := metricsStatus(t, first, oldToken); got != http.StatusOK {
		t.Fatalf("old token before rotation: status = %d, want 200", got)
	}
	if got := metricsStatus(t, first, newToken); got != http.StatusUnauthorized {
		t.Fatalf("unknown token: status = %d, want 401", got)
	}
	if got := metricsStatus(t, first, ""); got != http.StatusUnauthorized {
		t.Fatalf("missing token: status = %d, want 401", got)
	}

	// Phase 2: rotate. The previous credential must stop working with the new
	// process; the rotation window is exactly the restart.
	second := startTokenServer(t, newToken)
	if got := metricsStatus(t, second, newToken); got != http.StatusOK {
		t.Fatalf("new token after rotation: status = %d, want 200", got)
	}
	if got := metricsStatus(t, second, oldToken); got != http.StatusUnauthorized {
		t.Fatalf("rotated-out token still authenticates after restart: status = %d", got)
	}
}

// TestAccessLogRecordsRefusalsWithoutBearerToken is the T6/T12 cross-check: a 401
// is recorded (so refusals are still auditable) while neither credential is.
func TestAccessLogRecordsRefusalsWithoutBearerToken(t *testing.T) {
	const token = "cccccccccccccccccccccccccccccccccccccccccccccccc"

	root := newTestWorkspace(t)
	accessPath := filepath.Join(t.TempDir(), "access.ndjson")
	t.Setenv("AICLI_TEST_LOG_TOKEN", token)

	cfg := newOpsConfig(t, root, []string{"echo"})
	cfg.HTTP.Listen = "127.0.0.1:0"
	cfg.HTTP.TokenEnv = "AICLI_TEST_LOG_TOKEN"
	cfg.HTTP.AccessLog = accessPath

	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
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

	if got := metricsStatus(t, addr, ""); got != http.StatusUnauthorized {
		t.Fatalf("refused status = %d, want 401", got)
	}
	if got := metricsStatus(t, addr, token); got != http.StatusOK {
		t.Fatalf("accepted status = %d, want 200", got)
	}

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
	log := string(raw)
	if strings.Contains(log, token) {
		t.Fatalf("access log leaked the bearer token: %s", log)
	}
	if !strings.Contains(log, "401") || !strings.Contains(log, "200") {
		t.Fatalf("access log must record both the refusal and the success: %s", log)
	}
}
