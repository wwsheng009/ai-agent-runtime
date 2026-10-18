package qqbot

// 指令面板自动同步：按 remark 幂等维护、内容未变不写、变更才 PUT。

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

func TestSyncCommandPanelCreatesWhenMissing(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	fake.panelRecords = nil // 无面板
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	changed, err := provider.SyncCommandPanel(context.Background(), cfg)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !changed {
		t.Fatal("missing panel should be created")
	}
	if len(fake.panelCreates) != 1 {
		t.Fatalf("creates = %d, want 1", len(fake.panelCreates))
	}
	created := fake.panelCreates[0]
	if created["scope"] != "c2c" || created["target_type"] != "all" {
		t.Fatalf("create payload = %+v", created)
	}
}

func TestSyncCommandPanelIdempotent(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	fake.panelRecords = nil
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	if _, err := provider.SyncCommandPanel(context.Background(), cfg); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	creates := len(fake.panelCreates)
	updates := len(fake.panelUpdates)

	// 第二次：假平台已保存面板（remark 命中且内容一致）→ 不应再写。
	changed, err := provider.SyncCommandPanel(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if changed {
		t.Fatal("unchanged panel must not be rewritten")
	}
	if len(fake.panelCreates) != creates || len(fake.panelUpdates) != updates {
		t.Fatalf("unexpected writes: creates=%d updates=%d", len(fake.panelCreates), len(fake.panelUpdates))
	}
}

func TestSyncCommandPanelUpdatesWhenCommandsChange(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	fake.panelRecords = []map[string]any{{
		"panel_id": "p-old", "scope": "c2c", "target_type": "all",
		"panel": map[string]any{
			"remark": panelRemark,
			"items":  []map[string]any{{"name": "旧命令", "type": "command"}},
		},
	}}
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	changed, err := provider.SyncCommandPanel(context.Background(), cfg)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !changed || len(fake.panelUpdates) != 1 {
		t.Fatalf("changed=%v updates=%d", changed, len(fake.panelUpdates))
	}
	if fake.panelUpdates[0]["panel_id"] != "p-old" {
		t.Fatalf("update target = %+v", fake.panelUpdates[0])
	}
}

func TestCommandPanelSyncDisabled(t *testing.T) {
	cfg := testConfig("qq-1", "app-1", "secret-1")
	cfg.Settings["command_panel_sync"] = "false"
	provider := New()
	if provider.commandPanelSyncEnabled(cfg) {
		t.Fatal("explicit false must disable sync")
	}
	cfg.Settings["command_panel_sync"] = ""
	if !provider.commandPanelSyncEnabled(cfg) {
		t.Fatal("empty setting must default to enabled")
	}
}

func TestPanelItemsEqual(t *testing.T) {
	a := []QQPanelItem{{Name: "帮助", Desc: "查看可用命令", Type: "command"}}
	b := []QQPanelItem{{Name: "帮助", Desc: "查看可用命令", Type: "command"}}
	if !panelItemsEqual(a, b) {
		t.Fatal("identical items must be equal")
	}
	b[0].Desc = "changed"
	if panelItemsEqual(a, b) {
		t.Fatal("changed description must differ")
	}
}

// 核心命令表驱动面板内容（防止命令与面板漂移）。
func TestPanelUsesAgentCommandCatalog(t *testing.T) {
	commands := connectors.AgentCommands()
	if len(commands) == 0 {
		t.Fatal("catalog must not be empty")
	}
	for _, command := range commands {
		if command.Name == "" || command.Label == "" {
			t.Fatalf("incomplete command: %+v", command)
		}
	}
}
