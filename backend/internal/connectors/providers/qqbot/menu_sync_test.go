// 自定义菜单自动同步（2026-10-17 接线）：激活时把核心命令幂等写入 /v2/menu。
//
// 语义钉住三点：空菜单创建（send_message 直发 "/命令"）、内容一致不写、
// 已有他人菜单不覆盖。

package qqbot

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// menuPutCount 统计 PUT /v2/menu 次数（GET 记录带 "?" 尾缀，PUT 为裸路径）。
func menuPutCount(fake *fakeQQ) int {
	n := 0
	for _, item := range fake.sentMessages() {
		if item.path == "/v2/menu" && item.payload != nil {
			n++
		}
	}
	return n
}

func TestSyncCommandMenuCreatesWhenMissing(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	fake.menuBody = map[string]any{"version": 0} // 无菜单
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	changed, err := provider.SyncCommandMenu(context.Background(), cfg)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !changed {
		t.Fatal("missing menu must be created")
	}
	if puts := menuPutCount(fake); puts != 1 {
		t.Fatalf("menu PUTs = %d, want 1", puts)
	}
	// 写回内容 = 核心命令目录（全量对齐；首项为 /help 的 send_message 项）。
	state, err := provider.GetMenu(context.Background(), cfg)
	if err != nil {
		t.Fatalf("get menu: %v", err)
	}
	if state.Menu == nil {
		t.Fatal("menu must be persisted by fake after PUT")
	}
	want := commandMenuItems()
	if len(want) != len(connectors.AgentCommands()) {
		t.Fatalf("menu items = %d, want catalog size %d", len(want), len(connectors.AgentCommands()))
	}
	if !menuItemsEqual(state.Menu.Items, want) {
		t.Fatalf("persisted menu = %+v, want %+v", state.Menu.Items, want)
	}
	first := state.Menu.Items[0]
	if first.Name != "帮助" || first.Type != "send_message" || first.SendMessage != "/help" {
		t.Fatalf("first item = %+v, want 帮助/send_message//help", first)
	}
}

func TestSyncCommandMenuIdempotent(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	fake.menuBody = map[string]any{"version": 0}
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	if _, err := provider.SyncCommandMenu(context.Background(), cfg); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	puts := menuPutCount(fake)

	// 第二次：假平台已保存同一菜单 → 不应再写。
	changed, err := provider.SyncCommandMenu(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if changed {
		t.Fatal("unchanged menu must not be rewritten")
	}
	if now := menuPutCount(fake); now != puts {
		t.Fatalf("menu PUTs changed: before=%d after=%d", puts, now)
	}
}

func TestSyncCommandMenuSkipsForeignMenu(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	// 运营手工配置的菜单（与内置不一致）：不得覆盖。
	fake.menuBody = map[string]any{
		"version": 3,
		"menu": map[string]any{"items": []map[string]any{
			{"name": "官网", "type": "link", "link": "https://example.com"},
		}},
	}
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	changed, err := provider.SyncCommandMenu(context.Background(), cfg)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if changed {
		t.Fatal("foreign menu must not be overwritten")
	}
	if puts := menuPutCount(fake); puts != 0 {
		t.Fatalf("foreign menu must not be written, PUTs = %d", puts)
	}
	// 原菜单保持原样。
	state, err := provider.GetMenu(context.Background(), cfg)
	if err != nil {
		t.Fatalf("get menu: %v", err)
	}
	if state.Menu == nil || len(state.Menu.Items) != 1 || state.Menu.Items[0].Name != "官网" {
		t.Fatalf("foreign menu mutated: %+v", state.Menu)
	}
}

func TestCommandMenuSyncDisabled(t *testing.T) {
	cfg := testConfig("qq-1", "app-1", "secret-1")
	cfg.Settings["command_menu_sync"] = "false"
	provider := New()
	if provider.commandMenuSyncEnabled(cfg) {
		t.Fatal("explicit false must disable menu sync")
	}
	cfg.Settings["command_menu_sync"] = ""
	if !provider.commandMenuSyncEnabled(cfg) {
		t.Fatal("empty setting must default to enabled")
	}
}

// TestSyncCommandMenuUpgradesManagedMenu：识别为本程序旧版菜单（子集，逐项
// name+send_message 与核心命令目录配对）→ 覆盖更新为最新内置（命令目录演进
// 后旧菜单不滞留）。
func TestSyncCommandMenuUpgradesManagedMenu(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	fake.menuBody = map[string]any{
		"version": 2,
		"menu": map[string]any{"items": []map[string]any{
			{"name": "帮助", "type": "send_message", "send_message": "/help"},
			{"name": "新会话", "type": "send_message", "send_message": "/new"},
		}},
	}
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	changed, err := provider.SyncCommandMenu(context.Background(), cfg)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !changed {
		t.Fatal("managed older menu must be upgraded")
	}
	state, err := provider.GetMenu(context.Background(), cfg)
	if err != nil {
		t.Fatalf("get menu: %v", err)
	}
	if state.Menu == nil || !menuItemsEqual(state.Menu.Items, commandMenuItems()) {
		t.Fatalf("menu after upgrade = %+v", state.Menu)
	}
}

// TestActivateSyncsCommandPanelAndMenu：钉住激活接线（2026-10-17）——默认开关
// 下 Activate 同时同步「指令面板」与「自定义菜单」（两者都是体验增强，失败只
// 记日志，不阻断激活）。
func TestActivateSyncsCommandPanelAndMenu(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	fake.panelRecords = nil
	fake.menuBody = map[string]any{"version": 0}
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	if _, err := provider.Activate(context.Background(), cfg); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if creates := len(fake.panelCreates); creates != 1 {
		t.Fatalf("panel creates = %d, want 1", creates)
	}
	if puts := menuPutCount(fake); puts != 1 {
		t.Fatalf("menu PUTs = %d, want 1", puts)
	}
}
