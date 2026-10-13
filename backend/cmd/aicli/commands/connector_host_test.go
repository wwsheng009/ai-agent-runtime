// aicli 连接器宿主接线的单测：路由挂载/503 语义、session id 派生、
// Backend 未就绪时的失败语义。真正的轮询/去重 e2e 在
// internal/connectors/e2e_restart_test.go（假 Bot API + storefile）。
package commands

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/restapi"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/storefile"
)

// ── 测试替身 ───────────────────────────────────────────────────────────────

type stubConnectorBackend struct{}

func (stubConnectorBackend) Name() string { return "stub" }

func (stubConnectorBackend) Handle(context.Context, connectors.BackendRequest, connectors.ReplySink) (connectors.BackendResult, error) {
	return connectors.BackendResult{}, nil
}

func newTestConnectorService(t *testing.T) (*connectors.Service, *storefile.ConfigStore) {
	t.Helper()
	dir := t.TempDir()
	store := storefile.NewConfigStore(filepath.Join(dir, "config.yaml"), filepath.Join(dir, "data", "connectors", "state.json"))
	service := connectors.New(connectors.Deps{Store: store, Backend: stubConnectorBackend{}, Logf: func(string, ...any) {}})
	return service, store
}

// ── 用例 ───────────────────────────────────────────────────────────────────

func TestLocalConnectorSessionIDDerivesStableID(t *testing.T) {
	require.Equal(t, "conn-tg-main-200", localConnectorSessionID("tg-main", "200"))
	require.Equal(t, "conn-tg-main-a-b", localConnectorSessionID("tg main", "a/b"))
	require.Equal(t, "conn-default", localConnectorSessionID("", ""))
	require.Equal(t, "conn-default", localConnectorSessionID("-", "-"))

	long := localConnectorSessionID("tg", strings.Repeat("9", 200))
	require.LessOrEqual(t, len(long), 80)
	require.True(t, strings.HasPrefix(long, "conn-tg-"))
}

func TestMountLocalConnectorRoutesWithoutServiceReturns503(t *testing.T) {
	setLocalConnectorHandlers(nil, nil)
	mux := http.NewServeMux()
	MountLocalConnectorRoutes(mux)

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/connectors", nil),
		httptest.NewRequest(http.MethodPost, "/api/connectors", strings.NewReader("{}")),
		httptest.NewRequest(http.MethodPost, "/hooks/connectors/tg-main", strings.NewReader("{}")),
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code, request.URL.Path)
		require.Contains(t, recorder.Body.String(), "连接器未在本会话装配")
	}
}

func TestMountLocalConnectorRoutesForwardsToService(t *testing.T) {
	service, store := newTestConnectorService(t)
	deps := restapi.Deps{
		Service:   service,
		Store:     store,
		Authorize: func(*http.Request) error { return nil },
		Logf:      func(string, ...any) {},
	}
	setLocalConnectorHandlers(restapi.NewRouter(deps), restapi.NewHooksRouter(deps))
	t.Cleanup(func() { setLocalConnectorHandlers(nil, nil) })

	mux := http.NewServeMux()
	MountLocalConnectorRoutes(mux)

	// 精确路径 /api/connectors（不带尾斜杠）必须直接命中，而不是 301。
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/connectors", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"items"`)

	// schema 路由同样可达（前缀剥离正确）。
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/connectors/schema", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"providers"`)

	// hooks 入口可达：未知连接器 404 由 handlers 返回，而不是 503/404 兜底。
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/hooks/connectors/unknown", strings.NewReader("{}")))
	require.NotEqual(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestLocalConnectorBackendWithoutHostFailsSoftly(t *testing.T) {
	backend := localConnectorBackend{}
	_, err := backend.Handle(context.Background(), connectors.BackendRequest{Text: "你好"}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "本机 chat 运行时未就绪")
}

func TestLocalConnectorHostCloseIsIdempotent(t *testing.T) {
	service, _ := newTestConnectorService(t)
	host := &LocalConnectorHost{service: service}
	host.Close()
	host.Close()
}

// TestStartLocalConnectorsEnforcesAllowedUsersGate 覆盖 Fix-S2 的 aicli 接线：
// StartLocalConnectors 必须设置 RequireAllowedUsers=true——启用中的连接器若
// policy.allowed_users 为空，则不得 Activate（不向平台发 getMe/轮询）；同一份
// 配置里白名单非空的连接器照常 Activate，既证明配置被正确解析，也证明闸只拦
// 空白名单（避免 YAML 解析失败导致的假阳性）。
func TestStartLocalConnectorsEnforcesAllowedUsersGate(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"username":"gate-test"}}`))
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := "connectors:\n" +
		"  - id: tg-gate\n" +
		"    provider: telegram\n" +
		"    enabled: true\n" +
		"    mode: polling\n" +
		"    settings:\n" +
		"      api_base: " + server.URL + "\n" +
		"    secrets:\n" +
		"      bot_token: \"111:gate\"\n" +
		"  - id: tg-open\n" +
		"    provider: telegram\n" +
		"    enabled: true\n" +
		"    mode: webhook\n" +
		"    settings:\n" +
		"      api_base: " + server.URL + "\n" +
		"    secrets:\n" +
		"      bot_token: \"222:open\"\n" +
		"    policy:\n" +
		"      allowed_users: [\"42\"]\n"
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	var logs []string
	host, err := StartLocalConnectors(context.Background(), &localChatRuntimeHost{}, configPath, func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	require.NoError(t, err)
	require.NotNil(t, host)
	t.Cleanup(host.Close)

	// tg-open 的 Activate 在 service.Start 内同步完成；轮询等 100ms 兜底。
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, path := range paths {
			if strings.Contains(path, "222:open") {
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond, "白名单非空连接器应照常 Activate")

	time.Sleep(100 * time.Millisecond) // 留出闸失效时空白名单连接器发请求的窗口
	mu.Lock()
	joined := strings.Join(paths, "\n")
	mu.Unlock()
	require.NotContains(t, joined, "111:gate", "空白名单连接器不得 Activate/轮询")
	require.Contains(t, strings.Join(logs, "\n"), "aicli connectors: started")
}

// TestStartLocalConnectorsHonorsRuntimeOwnerGate 覆盖归属闸接线（07 §5.6）：
// aicli 宿主身份为 "aicli"——policy.runtime_owner 指定 runtime-server 的连接器
// 不得 Activate/轮询（只读配置），指定 aicli 的照常启动。
func TestStartLocalConnectorsHonorsRuntimeOwnerGate(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"username":"owner-test"}}`))
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := "connectors:\n" +
		"  - id: tg-owned\n" +
		"    provider: telegram\n" +
		"    enabled: true\n" +
		"    mode: polling\n" +
		"    settings:\n" +
		"      api_base: " + server.URL + "\n" +
		"    secrets:\n" +
		"      bot_token: \"333:owned\"\n" +
		"    policy:\n" +
		"      allowed_users: [\"42\"]\n" +
		"      runtime_owner: runtime-server\n" +
		"  - id: tg-aicli\n" +
		"    provider: telegram\n" +
		"    enabled: true\n" +
		"    mode: polling\n" +
		"    settings:\n" +
		"      api_base: " + server.URL + "\n" +
		"    secrets:\n" +
		"      bot_token: \"444:aicli\"\n" +
		"    policy:\n" +
		"      allowed_users: [\"42\"]\n" +
		"      runtime_owner: aicli\n"
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	var logsMu sync.Mutex
	var logs []string
	host, err := StartLocalConnectors(context.Background(), &localChatRuntimeHost{}, configPath, func(format string, args ...any) {
		logsMu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		logsMu.Unlock()
	})
	require.NoError(t, err)
	require.NotNil(t, host)
	t.Cleanup(host.Close)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, path := range paths {
			if strings.Contains(path, "444:aicli") {
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond, "runtime_owner=aicli 的连接器应照常 Activate")

	time.Sleep(100 * time.Millisecond) // 留出闸失效时误启动的窗口
	mu.Lock()
	joined := strings.Join(paths, "\n")
	mu.Unlock()
	require.NotContains(t, joined, "333:owned", "runtime_owner=runtime-server 的连接器不得由 aicli Activate/轮询")
	logsMu.Lock()
	joinedLogs := strings.Join(logs, "\n")
	logsMu.Unlock()
	require.Contains(t, joinedLogs, "未运行（由 runtime-server 持有")
}
