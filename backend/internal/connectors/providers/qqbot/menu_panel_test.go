package qqbot

// 自定义菜单 / 指令面板 / 分享链接（官方 2026-08/09）契约回归。

import (
	"context"
	"strings"
	"testing"
)

func TestMenuGetAndSet(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	state, err := provider.GetMenu(context.Background(), cfg)
	if err != nil {
		t.Fatalf("get menu: %v", err)
	}
	if state.Version != 1 || state.Menu == nil || len(state.Menu.Items) != 1 ||
		state.Menu.Items[0].SendMessage != "/help" {
		t.Fatalf("state = %+v", state)
	}

	version, err := provider.SetMenu(context.Background(), cfg, QQMenu{Items: []QQMenuItem{
		{Name: "官网", Type: "link", Link: "https://example.com"},
	}})
	if err != nil {
		t.Fatalf("set menu: %v", err)
	}
	if version != 0 { // 假平台默认返回空 body → 0，仅验证请求成功
		t.Logf("version = %d", version)
	}
	sent := fake.sentMessages()
	var putPath string
	for _, item := range sent {
		if item.path == "/v2/menu" {
			putPath = item.path
		}
	}
	if putPath == "" {
		t.Fatalf("menu PUT not recorded: %+v", sent)
	}
}

func TestMenuValidation(t *testing.T) {
	provider := New()
	cfg := testConfig("qq-1", "app-1", "secret-1")
	cases := []struct {
		name string
		menu QQMenu
	}{
		{"超过 10 项", QQMenu{Items: make([]QQMenuItem, 11)}},
		{"非法类型", QQMenu{Items: []QQMenuItem{{Name: "x", Type: "bad"}}}},
		{"link 非 https", QQMenu{Items: []QQMenuItem{{Name: "x", Type: "link", Link: "http://a"}}}},
		{"switch 缺 id", QQMenu{Items: []QQMenuItem{{Name: "x", Type: "switch"}}}},
		{"子菜单超 5", QQMenu{Items: []QQMenuItem{{Name: "x", Type: "menu",
			SubMenuItems: make([]QQSubMenuItem, 6)}}}},
		{"子菜单非法类型", QQMenu{Items: []QQMenuItem{{Name: "x", Type: "menu",
			SubMenuItems: []QQSubMenuItem{{Name: "s", Type: "menu"}}}}}},
	}
	for _, tc := range cases {
		if _, err := provider.SetMenu(context.Background(), cfg, tc.menu); err == nil {
			t.Fatalf("%s must be rejected", tc.name)
		}
	}
}

func TestPanelCRUD(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	fake.panelRecords = []map[string]any{{
		"panel_id": "p1", "scope": "c2c", "target_type": "all",
		"panel": map[string]any{"items": []map[string]any{{
			"name": "查询天气", "type": "command",
		}}},
	}}
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	page, err := provider.ListPanels(context.Background(), cfg, "c2c", "", 0)
	if err != nil {
		t.Fatalf("list panels: %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].PanelID != "p1" {
		t.Fatalf("page = %+v", page)
	}
	record, err := provider.GetPanel(context.Background(), cfg, "p1")
	if err != nil {
		t.Fatalf("get panel: %v", err)
	}
	if record.PanelID != "p1" {
		t.Fatalf("record = %+v", record)
	}
	if _, err := provider.CreatePanel(context.Background(), cfg, QQPanelCreateRequest{
		Scope: "c2c", TargetType: "all",
		Panel: QQPanel{Items: []QQPanelItem{{Name: "查询天气", Type: "command"}}},
	}); err != nil {
		t.Fatalf("create panel: %v", err)
	}
	if err := provider.DeletePanel(context.Background(), cfg, "p1"); err != nil {
		t.Fatalf("delete panel: %v", err)
	}
	if err := provider.UpdatePanelTargets(context.Background(), cfg, "p1", "add", []string{"u1"}, nil); err != nil {
		t.Fatalf("update targets: %v", err)
	}
}

func TestPanelValidation(t *testing.T) {
	provider := New()
	cfg := testConfig("qq-1", "app-1", "secret-1")
	if _, err := provider.ListPanels(context.Background(), cfg, "bad", "", 0); err == nil {
		t.Fatal("invalid scope must be rejected")
	}
	if _, err := provider.CreatePanel(context.Background(), cfg, QQPanelCreateRequest{
		Scope: "channel", TargetType: "specific",
	}); err == nil {
		t.Fatal("channel + specific must be rejected")
	}
	if _, err := provider.CreatePanel(context.Background(), cfg, QQPanelCreateRequest{
		Scope: "c2c", TargetType: "specific",
	}); err == nil {
		t.Fatal("c2c specific without user_openids must be rejected")
	}
	if _, err := provider.CreatePanel(context.Background(), cfg, QQPanelCreateRequest{
		Scope: "c2c",
		Panel: QQPanel{Items: make([]QQPanelItem, 21)},
	}); err == nil {
		t.Fatal("panel items over 20 must be rejected")
	}
	if _, err := provider.CreatePanel(context.Background(), cfg, QQPanelCreateRequest{
		Scope: "c2c",
		Panel: QQPanel{Items: []QQPanelItem{{Name: "x", Type: "link", Link: "http://a"}}},
	}); err == nil {
		t.Fatal("non-https link must be rejected")
	}
	if err := provider.UpdatePanelTargets(context.Background(), cfg, "p1", "add", nil, nil); err == nil {
		t.Fatal("empty targets must be rejected")
	}
}

func TestGenerateShareLink(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	// 假平台对未知 /v2/ POST 返回 {"id":"msg-out-1"}；分享链接需自定义响应。
	fake.mu.Lock()
	fake.shareURL = "https://qun.qq.com/qunpro/robot/qunshare?robot_appid=1&data=abc"
	fake.mu.Unlock()
	link, err := provider.GenerateShareLink(context.Background(), cfg, "abc")
	if err != nil {
		t.Fatalf("share link: %v", err)
	}
	if !strings.Contains(link, "qun.qq.com") {
		t.Fatalf("link = %q", link)
	}
	if _, err := provider.GenerateShareLink(context.Background(), cfg,
		strings.Repeat("x", 33)); err == nil {
		t.Fatal("callback_data over 32 chars must be rejected")
	}
}
