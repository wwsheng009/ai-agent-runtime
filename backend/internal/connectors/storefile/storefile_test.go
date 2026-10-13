package storefile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

func newTestStore(t *testing.T) (*ConfigStore, string, string) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	statePath := filepath.Join(dir, "data", "connectors", "state.json")
	return NewConfigStore(configPath, statePath), configPath, statePath
}

func sampleConfig() connectors.Config {
	return connectors.Config{
		ID:       "telegram-main",
		Provider: "telegram",
		Name:     "主账号",
		Enabled:  true,
		Mode:     "polling",
		Settings: map[string]string{"api_base": "https://api.telegram.org"},
		Secrets:  map[string]string{"bot_token": "123:abc"},
		Policy: connectors.Policy{
			CommandPrefix: "/",
			AllowedUsers:  []string{"42", "77"},
			ReplyMode:     "stream",
			RuntimeOwner:  "runtime-server",
		},
	}
}

func TestConfigStoreRoundTripPreservesOtherKeys(t *testing.T) {
	store, configPath, _ := newTestStore(t)
	if err := os.WriteFile(configPath, []byte("# keep me\nmodel: demo\n"), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if err := store.PutConfig(sampleConfig()); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	for _, want := range []string{
		"# keep me",
		"model: demo",
		"connectors:",
		"id: telegram-main",
		"provider: telegram",
		"reply_mode: stream",
		"runtime_owner: runtime-server",
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("config.yaml missing %q:\n%s", want, raw)
		}
	}
	// 密钥不进 config.yaml：单独写 connectors.secrets.yaml（0600）。
	if strings.Contains(string(raw), "bot_token") {
		t.Fatalf("config.yaml must not contain secrets:\n%s", raw)
	}
	secretsPath := filepath.Join(filepath.Dir(configPath), defaultSecretsFileName)
	secretsRaw, err := os.ReadFile(secretsPath)
	if err != nil {
		t.Fatalf("read secrets: %v", err)
	}
	if !strings.Contains(string(secretsRaw), "bot_token: 123:abc") {
		t.Fatalf("secrets file missing bot token:\n%s", secretsRaw)
	}

	// 新实例读取（模拟进程重启）。
	reopened := NewConfigStore(configPath, filepath.Join(filepath.Dir(configPath), "data", "connectors", "state.json"))
	got, ok := reopened.GetConfig("telegram-main")
	if !ok {
		t.Fatal("config not found after reopen")
	}
	if got.Provider != "telegram" || !got.Enabled || got.Mode != "polling" {
		t.Fatalf("unexpected config: %+v", got)
	}
	if got.Secrets["bot_token"] != "123:abc" {
		t.Fatalf("secret round trip failed: %+v", got.Secrets)
	}
	if len(got.Policy.AllowedUsers) != 2 || got.Policy.AllowedUsers[0] != "42" {
		t.Fatalf("policy round trip failed: %+v", got.Policy)
	}

	// 覆盖写不追加重复条目。
	update := got
	update.Enabled = false
	if err := reopened.PutConfig(update); err != nil {
		t.Fatalf("PutConfig update: %v", err)
	}
	if configs := reopened.ListConfigs(); len(configs) != 1 {
		t.Fatalf("expected 1 config after update, got %d", len(configs))
	}
	if got, _ := reopened.GetConfig("telegram-main"); got.Enabled {
		t.Fatal("update should disable connector")
	}
}

func TestConfigStoreStatePersistsAndCascades(t *testing.T) {
	store, configPath, statePath := newTestStore(t)
	if err := store.PutConfig(sampleConfig()); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	if claimed, err := store.ClaimInbound("telegram-main", "100"); err != nil || !claimed {
		t.Fatalf("first claim = (%v, %v), want (true, nil)", claimed, err)
	}
	if claimed, err := store.ClaimInbound("telegram-main", "100"); err != nil || claimed {
		t.Fatalf("duplicate claim = (%v, %v), want (false, nil)", claimed, err)
	}
	if err := store.PutBinding(connectors.Binding{
		ConnectorID: "telegram-main",
		Chat:        "telegram:1",
		Session:     connectors.SessionRef{SessionID: "sess-1"},
	}); err != nil {
		t.Fatalf("PutBinding: %v", err)
	}
	if err := store.PollingState("telegram-main").Set("update_offset", "120"); err != nil {
		t.Fatalf("polling Set: %v", err)
	}

	reopened := NewConfigStore(configPath, statePath)
	binding, ok := reopened.GetBinding("telegram-main", "telegram:1")
	if !ok || binding.Session.SessionID != "sess-1" {
		t.Fatalf("binding not persisted: %+v (ok=%v)", binding, ok)
	}
	if value, ok := reopened.PollingState("telegram-main").Get("update_offset"); !ok || value != "120" {
		t.Fatalf("cursor not persisted: %q (ok=%v)", value, ok)
	}
	if claimed, err := reopened.ClaimInbound("telegram-main", "100"); err != nil || claimed {
		t.Fatalf("claim should persist across reopen: (%v, %v)", claimed, err)
	}

	if err := reopened.DeleteConfig("telegram-main"); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
	if _, ok := reopened.GetConfig("telegram-main"); ok {
		t.Fatal("config should be deleted")
	}
	if _, ok := reopened.GetBinding("telegram-main", "telegram:1"); ok {
		t.Fatal("binding should be cascaded on delete")
	}
	if _, ok := reopened.PollingState("telegram-main").Get("update_offset"); ok {
		t.Fatal("cursor should be cascaded on delete")
	}
	if data, err := os.ReadFile(filepath.Join(filepath.Dir(configPath), defaultSecretsFileName)); err == nil && strings.Contains(string(data), "telegram-main") {
		t.Fatalf("secrets entry should be cascaded on delete:\n%s", data)
	}
}

func TestConfigStoreEmptyAndMissingFiles(t *testing.T) {
	store, _, _ := newTestStore(t)
	if configs := store.ListConfigs(); len(configs) != 0 {
		t.Fatalf("missing config should list nothing, got %d", len(configs))
	}
	cfg := sampleConfig()
	if err := store.PutConfig(cfg); err != nil {
		t.Fatalf("PutConfig into missing file: %v", err)
	}
	if _, ok := store.GetConfig(cfg.ID); !ok {
		t.Fatal("config should exist after PutConfig")
	}
}

func TestConfigStoreListBindingsFiltersAndPersists(t *testing.T) {
	store, configPath, statePath := newTestStore(t)
	seed := []connectors.Binding{
		{ConnectorID: "tg-2", Chat: "chat-b", Session: connectors.SessionRef{SessionID: "s2"}},
		{ConnectorID: "tg-1", Chat: "chat-b", Session: connectors.SessionRef{NodeID: "n1", SessionID: "s1"}},
		{ConnectorID: "tg-1", Chat: "chat-a", Session: connectors.SessionRef{SessionID: "s1"}},
	}
	for _, binding := range seed {
		if err := store.PutBinding(binding); err != nil {
			t.Fatalf("PutBinding: %v", err)
		}
	}

	all := store.ListBindings("")
	if len(all) != 3 {
		t.Fatalf("ListBindings(全部) = %d 条, want 3", len(all))
	}
	// 输出按 connector+chat 排序（state.json 的 map 无序，排序保证确定性）。
	want := []struct{ connector, chat string }{{"tg-1", "chat-a"}, {"tg-1", "chat-b"}, {"tg-2", "chat-b"}}
	for i, expect := range want {
		if all[i].ConnectorID != expect.connector || all[i].Chat != expect.chat {
			t.Fatalf("all[%d] = %s/%s, want %s/%s", i, all[i].ConnectorID, all[i].Chat, expect.connector, expect.chat)
		}
	}
	if all[1].Session != (connectors.SessionRef{NodeID: "n1", SessionID: "s1"}) {
		t.Fatalf("session = %+v, want n1/s1", all[1].Session)
	}
	if filtered := store.ListBindings("tg-1"); len(filtered) != 2 {
		t.Fatalf("ListBindings(tg-1) = %d 条, want 2", len(filtered))
	}
	if missing := store.ListBindings("nope"); len(missing) != 0 {
		t.Fatalf("未知连接器应返回空列表: %+v", missing)
	}

	// 重建实例 = 进程内重启：绑定从 state.json 读回。
	reopened := NewConfigStore(configPath, statePath)
	again := reopened.ListBindings("tg-1")
	if len(again) != 2 {
		t.Fatalf("重启后 ListBindings(tg-1) = %d 条, want 2", len(again))
	}
	if again[1].Session.NodeID != "n1" || again[1].Session.SessionID != "s1" {
		t.Fatalf("重启后 session = %+v, want n1/s1", again[1].Session)
	}
}
