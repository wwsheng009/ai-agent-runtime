package manager

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/client"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

// stderrAwareClient 在 fakeClient（manager_health_test.go）之上实现可选的
// stdio 诊断能力，用于验证 StderrDiagnosticsProvider 的读取与留存逻辑。
type stderrAwareClient struct {
	*fakeClient
	stderr string
}

func (c *stderrAwareClient) StderrDiagnostics() string { return c.stderr }

func newStderrTestManager(cfg *config.Config, newClient func(string, *config.MCPConfig) (client.Client, error)) *manager {
	return &manager{
		cfg:        cfg,
		registry:   registry.NewRegistry(),
		clients:    make(map[string]client.Client),
		pending:    make(map[string]client.Client),
		lastStderr: make(map[string]string),
		status:     make(map[string]*config.MCPStatus),
		connecting: make(map[string]struct{}),
		started:    true,
		newClient:  newClient,
	}
}

func stderrTestConfig() *config.Config {
	return &config.Config{
		MCPServers: map[string]config.MCPConfig{
			"svc": {Name: "svc", Type: "stdio", Enabled: true},
		},
	}
}

func TestManagerStderrDiagnosticsKeepsFailedAttempt(t *testing.T) {
	connectErr := errors.New("calling \"initialize\": EOF")
	mgr := newStderrTestManager(stderrTestConfig(), func(name string, _ *config.MCPConfig) (client.Client, error) {
		return &stderrAwareClient{
			fakeClient: &fakeClient{name: name, connectErr: connectErr},
			stderr:     "[stdio 子进程诊断]\nstderr 尾部（共 68 字节）：\n'C:\\Program' is not recognized as an internal or external command",
		}, nil
	})

	mgr.connectMCP(context.Background(), 0, "svc", &config.MCPConfig{Name: "svc", Type: "stdio", Enabled: true})

	if cli := mgr.getClient("svc"); cli != nil {
		t.Fatalf("建连失败的客户端不应留在 clients 中，实际 %T", cli)
	}
	got := mgr.StderrDiagnostics("svc")
	if !strings.Contains(got, "is not recognized") {
		t.Fatalf("失败诊断未留存: %q", got)
	}
}

func TestManagerStderrDiagnosticsPrefersLiveClient(t *testing.T) {
	mgr := newStderrTestManager(&config.Config{}, nil)
	mgr.clients["svc"] = &stderrAwareClient{fakeClient: &fakeClient{name: "svc", connected: true}, stderr: "live tail"}
	mgr.lastStderr["svc"] = "stale tail"

	if got := mgr.StderrDiagnostics("svc"); got != "live tail" {
		t.Fatalf("应优先返回在线客户端诊断，实际 %q", got)
	}
}

func TestManagerStderrDiagnosticsClearedOnReconnect(t *testing.T) {
	mgr := newStderrTestManager(stderrTestConfig(), func(name string, _ *config.MCPConfig) (client.Client, error) {
		return &stderrAwareClient{fakeClient: &fakeClient{name: name}}, nil
	})
	mgr.lastStderr["svc"] = "stale tail"

	mgr.connectMCP(context.Background(), 0, "svc", &config.MCPConfig{Name: "svc", Type: "stdio", Enabled: true})

	if cli := mgr.getClient("svc"); cli == nil {
		t.Fatal("建连成功后客户端应登记在 clients 中")
	}
	if got := mgr.StderrDiagnostics("svc"); got != "" {
		t.Fatalf("重连成功后不应返回历史诊断，实际 %q", got)
	}
}

func TestManagerStderrDiagnosticsUnknownServerAndClient(t *testing.T) {
	mgr := newStderrTestManager(&config.Config{}, nil)
	if got := mgr.StderrDiagnostics("missing"); got != "" {
		t.Fatalf("未知 MCP 应返回空串，实际 %q", got)
	}
	// 不实现可选诊断能力的客户端必须安全降级为空串。
	mgr.clients["plain"] = &fakeClient{name: "plain", connected: true}
	if got := mgr.StderrDiagnostics("plain"); got != "" {
		t.Fatalf("无诊断能力的客户端应返回空串，实际 %q", got)
	}
}
