package main

// 三宿主挂载矩阵的 runtime-server 断言（docs/bots/06-target-architecture.md §4.2
// N6「同一 golden ×3 宿主」）：与 aicli（commands/connector_host_test.go）、
// gateway（cmd/gateway）一样，runtime-server 也必须把共享 restapi 挂到同一形状。
// 本测试用裸 mux + 内存 Store + 假 Backend/Provider 直接调用宿主真实的
// mountConnectorRoutes，断言三条入口可达且走的是 restapi 业务载荷：
//
//   - GET  /api/connectors        → 200，JSON {items:[...]}（管理面，StripPrefix）；
//   - GET  /api/connectors/schema → 200（三端渲染器的 Schema 入口）；
//   - POST /hooks/connectors/{id} → 未知 id 必须落到业务 404（JSON {error}）或
//     405，而不是 mux 未匹配的 text/plain 404（挂载缺失的回归信号）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/restapi"
)

type mountFakeProvider struct{}

func (mountFakeProvider) Name() string { return "mountfake" }

func (mountFakeProvider) Schema() api.ProviderSchema {
	return api.ProviderSchema{
		Provider: "mountfake",
		Label:    "Mount Fake",
		Modes:    []string{"polling"},
		Fields:   []api.FieldSchema{{Key: "token", Label: "Token", Type: api.FieldSecret, Required: true}},
	}
}

func (mountFakeProvider) Activate(context.Context, connectors.Config) (connectors.Activation, error) {
	return connectors.Activation{}, nil
}

func (mountFakeProvider) Deactivate(context.Context, connectors.Config) error { return nil }

func (mountFakeProvider) Send(context.Context, connectors.Config, string, []connectors.OutboundMessage) error {
	return nil
}

type mountFakeBackend struct{}

func (mountFakeBackend) Name() string { return "mount-fake-backend" }

func (mountFakeBackend) Handle(context.Context, connectors.BackendRequest, connectors.ReplySink) (connectors.BackendResult, error) {
	return connectors.BackendResult{}, nil
}

func TestMountConnectorRoutesSharedContract(t *testing.T) {
	store := connectors.NewMemoryStore(connectors.Config{
		ID: "c1", Provider: "mountfake", Name: "c1", Enabled: true, Mode: "polling",
		Secrets: map[string]string{"token": "tok"},
	})
	service := connectors.New(connectors.Deps{
		Store:   store,
		Backend: mountFakeBackend{},
		Logf:    func(string, ...any) {},
		HostID:  "runtime-server",
	})
	if err := service.Register(mountFakeProvider{}); err != nil {
		t.Fatal(err)
	}
	deps := restapi.Deps{
		Service:   service,
		Store:     store,
		Authorize: func(*http.Request) error { return nil },
		Logf:      func(string, ...any) {},
		HostID:    "runtime-server",
	}
	router := mux.NewRouter()
	mountConnectorRoutes(router, deps)

	// 管理面列表：200 + JSON items。
	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/connectors", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("GET /api/connectors 应 200，got %d %s", list.Code, list.Body.String())
	}
	var payload struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &payload); err != nil {
		t.Fatalf("列表载荷不是 JSON items：%v（%s）", err, list.Body.String())
	}
	if len(payload.Items) != 1 {
		t.Fatalf("items = %d，want 1（%s）", len(payload.Items), list.Body.String())
	}

	// Schema 入口：200（与 restapi golden 同一载荷）。
	schema := httptest.NewRecorder()
	router.ServeHTTP(schema, httptest.NewRequest(http.MethodGet, "/api/connectors/schema", nil))
	if schema.Code != http.StatusOK {
		t.Fatalf("GET /api/connectors/schema 应 200，got %d %s", schema.Code, schema.Body.String())
	}
	if !strings.Contains(schema.Body.String(), `"providers"`) {
		t.Fatalf("schema 载荷缺 providers：%s", schema.Body.String())
	}

	// webhook 未知 id：必须可达（业务 404 JSON 或 405），不能是 mux 未匹配。
	hook := httptest.NewRecorder()
	router.ServeHTTP(hook, httptest.NewRequest(http.MethodPost, "/hooks/connectors/unknown", strings.NewReader("{}")))
	switch hook.Code {
	case http.StatusNotFound:
		contentType := hook.Header().Get("Content-Type")
		if !strings.Contains(contentType, "application/json") || !strings.Contains(hook.Body.String(), `"error"`) {
			t.Fatalf("POST /hooks/connectors/unknown 落到 mux 未匹配（%d %s）：%s",
				hook.Code, contentType, hook.Body.String())
		}
	case http.StatusMethodNotAllowed:
		// 路径已匹配、仅方法不符：仍证明挂载存在。
	default:
		t.Fatalf("POST /hooks/connectors/unknown = %d，want 业务 404 或 405（%s）", hook.Code, hook.Body.String())
	}
}
