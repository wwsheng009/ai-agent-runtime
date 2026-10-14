package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/restapi"
)

// 复现真实故障（2026-10-14）：runtime-server 顶层 router 带 UseEncodedPath()，
// 经 PathPrefix("/api/connectors") + StripPrefix 挂载 restapi 后，含连字符的
// {id} 单查 404"未知连接器"（列表正常）——本测试在宿主同构挂载形态下锁定该
// 行为，修复后必须双向（raw + escaped）命中。
func hostStyleRouter(deps restapi.Deps) *mux.Router {
	router := mux.NewRouter()
	router.UseEncodedPath() // 与 cmd/runtime-server/main.go 一致
	router.PathPrefix("/api/connectors").Handler(http.StripPrefix("/api", restapi.NewRouter(deps)))
	return router
}

func TestHostStyleRouterHyphenatedID(t *testing.T) {
	store := connectors.NewMemoryStore(connectors.Config{ID: "wechat-mv7lod8ozf9", Provider: "fake", Enabled: true})
	service := connectors.New(connectors.Deps{Store: store, Backend: stubConnectorBackend{}, Logf: func(string, ...any) {}})
	deps := restapi.Deps{Service: service, Store: store, Logf: func(string, ...any) {}, Authorize: func(*http.Request) error { return nil }}
	router := hostStyleRouter(deps)

	for _, path := range []string{
		"/api/connectors/wechat-mv7lod8ozf9",   // raw（浏览器/curl 常态）
		"/api/connectors/wechat%2Dmv7lod8ozf9", // escaped
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"id":"wechat-mv7lod8ozf9"`) {
			t.Fatalf("GET %s body missing id: %s", path, rec.Body.String())
		}
	}
}
