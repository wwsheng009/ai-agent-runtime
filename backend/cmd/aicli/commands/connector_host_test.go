// aicli 连接器宿主接线的单测：路由挂载/503 语义、session id 派生、
// Backend 未就绪时的失败语义。真正的轮询/去重 e2e 在
// internal/connectors/e2e_restart_test.go（假 Bot API + storefile）。
package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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
