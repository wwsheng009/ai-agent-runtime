package commands

// 「机器人」页签（IM 连接器）的前端资产与装配回归测试。
//
// 覆盖三个最容易漏的回归点：
//   1. 页面骨架（页签按钮/面板/移动端菜单项/弹层容器）随 go:embed 发布；
//   2. js/connectors.js 可被页面服务器伺服，且关键端点字符串在位；
//   3. app.js / ui.js 引用新模块（页签懒加载装配链完整）。
// 数据面（REST）契约由 internal/connectors/restapi 的测试覆盖，这里不重复。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatWebPage_ServesConnectorsTab(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/web/", nil)
	recorder := httptest.NewRecorder()
	HandleChatWebPage(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /web/ status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		`id="tab-connectors-btn"`,
		`id="tab-connectors"`,
		`id="connectors-list"`,
		`id="connectors-overlay"`,
		`data-menu-action="tab-connectors"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("page body missing %q", want)
		}
	}
}

func TestChatWebPage_ServesConnectorsAssets(t *testing.T) {
	script := httptest.NewRequest(http.MethodGet, "/web/js/connectors.js", nil)
	scriptRecorder := httptest.NewRecorder()
	HandleChatWebPage(scriptRecorder, script)
	if scriptRecorder.Code != http.StatusOK {
		t.Fatalf("GET /web/js/connectors.js status = %d", scriptRecorder.Code)
	}
	if contentType := scriptRecorder.Header().Get("Content-Type"); !strings.Contains(contentType, "javascript") {
		t.Fatalf("connectors.js content-type = %q", contentType)
	}
	scriptBody := scriptRecorder.Body.String()
	for _, want := range []string{
		"export function initConnectors",
		"export function loadConnectors",
		"/api/connectors/schema",
		"/api/connectors/bindings",
		"connector-bindings",
		"login_status",
	} {
		if !strings.Contains(scriptBody, want) {
			t.Fatalf("connectors.js missing %q", want)
		}
	}
}

func TestChatWebPage_ConnectorsModuleWired(t *testing.T) {
	for _, tc := range []struct {
		path string
		want string
	}{
		{path: "/web/app.js", want: `initConnectors()`},
		{path: "/web/js/ui.js", want: `loadConnectors()`},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		recorder := httptest.NewRecorder()
		HandleChatWebPage(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d", tc.path, recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), tc.want) {
			t.Fatalf("%s missing %q", tc.path, tc.want)
		}
	}
}
