package commands

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestChatWebPage_ServesParkedTurnModule 确认托管挂起/恢复横幅随 go:embed 发布、
// 且装配链完整：index.html 有横幅元素、sse.js 导入 parked.js 并接入事件分发、
// style.css 定义了横幅样式。新增 js 文件最容易漏的回归点是 embed 收录与装配断链
// ——两者都不在编译期报错，浏览器里表现为模块 404 或横幅静默不显示（用户只能
// 看到「卡死」，看不到「等待子任务」）。
func TestChatWebPage_ServesParkedTurnModule(t *testing.T) {
	module := readParkedTurnAssetOverHTTP(t, "/web/js/parked.js")
	if !strings.Contains(module, "handleParkedTurnSSEEvent") {
		t.Fatal("parked.js 缺少事件入口 handleParkedTurnSSEEvent")
	}
	for _, want := range []string{"turn.suspended", "turn.resumed", "hideParkedTurnBanner"} {
		if !strings.Contains(module, want) {
			t.Fatalf("parked.js 缺少 %q（事件分支必须齐全）", want)
		}
	}

	sse := readParkedTurnAssetOverHTTP(t, "/web/js/sse.js")
	for _, want := range []string{`from "./parked.js"`, "handleParkedTurnSSEEvent(eventName, data)"} {
		if !strings.Contains(sse, want) {
			t.Fatalf("sse.js 缺少 %q（横幅装配断链：事件到了也不会显示）", want)
		}
	}

	index := readParkedTurnEmbeddedAsset(t, "web/index.html")
	if !strings.Contains(index, `id="parked-banner"`) {
		t.Fatal("index.html 缺少 #parked-banner（模块会静默降级，横幅永远不显示）")
	}
	style := readParkedTurnEmbeddedAsset(t, "web/style.css")
	for _, want := range []string{".parked-banner", "parked-suspended", "parked-resumed"} {
		if !strings.Contains(style, want) {
			t.Fatalf("style.css 缺少 %q", want)
		}
	}
}

func readParkedTurnAssetOverHTTP(t *testing.T, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	recorder := httptest.NewRecorder()
	HandleChatWebPage(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d", path, recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "javascript") {
		t.Fatalf("%s content-type = %q", path, contentType)
	}
	return recorder.Body.String()
}

func readParkedTurnEmbeddedAsset(t *testing.T, path string) string {
	t.Helper()
	data, err := webFS.ReadFile(path)
	if err != nil {
		t.Fatalf("embed 未收录 %s: %v", path, err)
	}
	return string(data)
}
