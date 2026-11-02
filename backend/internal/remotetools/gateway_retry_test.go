package remotetools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
)

// TestStartGatewayAgentRetriesInitialConnect pins the fix for the "mount shows
// offline, process is up, nothing retries" incident: a first dial rejected by a
// gateway that is not ready yet must be retried with the backoff ladder instead
// of giving up until the next process restart.
func TestStartGatewayAgentRetriesInitialConnect(t *testing.T) {
	registry := tunnel.NewRegistry()
	tunnelServer := tunnel.NewServer(tunnel.Config{ServerVersion: "test", Registry: registry})

	var ready atomic.Bool
	firstAttempt := make(chan struct{}, 1)
	front := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() {
			select {
			case firstAttempt <- struct{}{}:
			default:
			}
			http.Error(w, "gateway starting", http.StatusServiceUnavailable)
			return
		}
		tunnelServer.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(front)
	t.Cleanup(srv.Close)

	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.HTTP.Path = "/mcp"
	cfg.HTTP.TokenEnv = "AICLI_RETRY_TEST_TOKEN"

	server, err := NewServer(cfg, "gateway-retry")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	logs := &retryLogCollector{}
	server.gatewayIdentity = "identity-retry-test"
	server.gatewayCfg = GatewayConfig{URL: srv.URL, AutoConnect: boolPtr(true)}
	server.gatewayToken = "agw_x"
	server.gatewayOptions = []gatewayagent.Option{
		gatewayagent.WithReconnectDelays([]time.Duration{20 * time.Millisecond, 40 * time.Millisecond}),
		gatewayagent.WithLogger(logs.logf),
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := server.startGatewayAgent(ctx, "127.0.0.1:18765"); err != nil {
		t.Fatalf("startGatewayAgent: %v", err)
	}

	select {
	case <-firstAttempt:
	case <-time.After(5 * time.Second):
		t.Fatal("first dial never reached the warming-up gateway")
	}
	if live := registry.CountLive(); live != 0 {
		t.Fatalf("live nodes = %d, want 0 while the gateway answers 503", live)
	}

	ready.Store(true)
	waitUntil(t, "node registered after the gateway became ready", func() bool {
		return registry.CountLive() == 1
	})
	nodes := registry.List()
	if len(nodes) != 1 {
		t.Fatalf("registered nodes = %d, want 1", len(nodes))
	}
	node := nodes[0]
	if node.Kind != "remote-tools" || node.Stale {
		t.Fatalf("node = %+v, want a live remote-tools registration", node)
	}
	if len(node.Mounts) == 0 {
		t.Fatal("registration must carry the self mount")
	}
	if !strings.Contains(logs.joined(), "retrying in") {
		t.Fatalf("initial-connect retry diagnostics missing:\n%s", logs.joined())
	}
}

type retryLogCollector struct {
	mu    sync.Mutex
	lines []string
}

func (l *retryLogCollector) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *retryLogCollector) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within deadline: %s", what)
}
