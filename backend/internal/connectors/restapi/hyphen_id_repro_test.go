package restapi

import (
	"net/http"
	"strings"
	"testing"
)

// 复现测试:含连字符的连接器 id 经宿主顶层 mux(UseEncodedPath) + StripPrefix +
// restapi 子路由后,单查是否还能命中。真实故障:runtime-server 上
// GET /api/connectors/wechat-mv7lod8ozf9 → 404"未知连接器",但列表可见。
func TestGetConnectorWithHyphenatedID(t *testing.T) {
	fx := newFixture(t, allowAll, baseConfig("c1-bot", "fakeweb"))
	recorder := do(t, fx.router, http.MethodGet, "/connectors/c1-bot", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET hyphenated id = %d %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"id":"c1-bot"`) {
		t.Fatalf("body missing id: %s", recorder.Body.String())
	}
}
