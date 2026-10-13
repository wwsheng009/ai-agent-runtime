package storejson

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

func testConfig(id string) connectors.Config {
	return connectors.Config{
		ID:       id,
		Provider: "telegram",
		Name:     "测试通道",
		Enabled:  true,
		Mode:     "polling",
		Settings: map[string]string{"node_id": "node-1", "chat_scope": "all"},
		Secrets:  map[string]string{"bot_token": "123456:secret"},
		Policy: connectors.Policy{
			AllowedUsers: []string{"u1", "u2"},
			ReplyMode:    "final",
			RuntimeOwner: "gateway",
		},
	}
}

func TestConfigRoundTripAndPersistence(t *testing.T) {
	dir := t.TempDir()
	store := New(dir)
	cfg := testConfig("tg-main")
	if err := store.PutConfig(cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	got, ok := store.GetConfig("tg-main")
	if !ok {
		t.Fatal("GetConfig: not found")
	}
	if got.Provider != cfg.Provider || got.Name != cfg.Name || !got.Enabled || got.Mode != cfg.Mode {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.Settings["node_id"] != "node-1" || got.Secret("bot_token") != "123456:secret" {
		t.Fatalf("settings/secrets lost: %+v", got)
	}
	if len(got.Policy.AllowedUsers) != 2 || got.Policy.ReplyMode != "final" || got.Policy.RuntimeOwner != "gateway" {
		t.Fatalf("policy lost: %+v", got.Policy)
	}

	// 覆盖同一 id 不产生重复项。
	cfg.Name = "改名"
	if err := store.PutConfig(cfg); err != nil {
		t.Fatalf("PutConfig overwrite: %v", err)
	}
	if list := store.ListConfigs(); len(list) != 1 || list[0].Name != "改名" {
		t.Fatalf("overwrite mismatch: %+v", list)
	}

	// 另一实例（模拟重启）从同一 data-dir 读到相同配置。
	reopened := New(dir)
	list := reopened.ListConfigs()
	if len(list) != 1 || list[0].ID != "tg-main" || list[0].Secret("bot_token") != "123456:secret" {
		t.Fatalf("persisted config mismatch: %+v", list)
	}

	// 配置与密钥在同一文件（文档 03 §7 口径），文件含 secrets 键。
	raw, err := os.ReadFile(filepath.Join(dir, DefaultConfigFileName))
	if err != nil {
		t.Fatalf("read %s: %v", DefaultConfigFileName, err)
	}
	var decoded struct {
		Configs map[string]struct {
			Secrets map[string]string `json:"secrets"`
		} `json:"configs"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode config file: %v", err)
	}
	if decoded.Configs["tg-main"].Secrets["bot_token"] == "" {
		t.Fatalf("secrets not stored in %s: %s", DefaultConfigFileName, raw)
	}
}

func TestConfigFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows does not report POSIX permission bits")
	}
	dir := t.TempDir()
	store := New(dir)
	if err := store.PutConfig(testConfig("tg-perm")); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, DefaultConfigFileName))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config mode = %o, want 600", perm)
	}
	if err := store.PutBinding(connectors.Binding{ConnectorID: "tg-perm", Chat: "c1"}); err != nil {
		t.Fatalf("PutBinding: %v", err)
	}
	stateInfo, err := os.Stat(filepath.Join(dir, DefaultStateFileName))
	if err != nil {
		t.Fatalf("stat state: %v", err)
	}
	if perm := stateInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("state mode = %o, want 600", perm)
	}
}

func TestDeleteConfigCascades(t *testing.T) {
	dir := t.TempDir()
	store := New(dir)
	if err := store.PutConfig(testConfig("tg-del")); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	if err := store.PutBinding(connectors.Binding{
		ConnectorID: "tg-del",
		Chat:        "chat-1",
		Session:     connectors.SessionRef{NodeID: "node-1", SessionID: "sess-1"},
	}); err != nil {
		t.Fatalf("PutBinding: %v", err)
	}
	if err := store.PollingState("tg-del").Set("offset", "42"); err != nil {
		t.Fatalf("cursor set: %v", err)
	}
	if claimed, err := store.ClaimInbound("tg-del", "chat-1\x00msg-1"); err != nil || !claimed {
		t.Fatalf("ClaimInbound first = %v, %v", claimed, err)
	}
	if err := store.ForceClaimConnectorOwner("tg-del", "gateway", time.Now()); err != nil {
		t.Fatalf("ForceClaim: %v", err)
	}
	if err := store.DeleteConfig("tg-del"); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
	if _, ok := store.GetConfig("tg-del"); ok {
		t.Fatal("config still present after delete")
	}
	if _, ok := store.GetBinding("tg-del", "chat-1"); ok {
		t.Fatal("binding not cascaded")
	}
	if _, ok := store.PollingState("tg-del").Get("offset"); ok {
		t.Fatal("cursor not cascaded")
	}
	if _, ok := store.OwnerOf("tg-del"); ok {
		t.Fatal("owner not cascaded")
	}
	if claimed, err := store.ClaimInbound("tg-del", "chat-1\x00msg-1"); err != nil || !claimed {
		t.Fatalf("claimed ledger not cascaded: %v, %v", claimed, err)
	}
}

func TestBindingRoundTrip(t *testing.T) {
	store := New(t.TempDir())
	binding := connectors.Binding{
		ConnectorID: "tg-1",
		Chat:        "chat-9",
		Session:     connectors.SessionRef{NodeID: "node-a", SessionID: "sess-a"},
		UpdatedAt:   time.Now().UTC().Truncate(time.Second),
	}
	if err := store.PutBinding(binding); err != nil {
		t.Fatalf("PutBinding: %v", err)
	}
	got, ok := store.GetBinding("tg-1", "chat-9")
	if !ok || got.Session.NodeID != "node-a" || got.Session.SessionID != "sess-a" {
		t.Fatalf("binding mismatch: %+v ok=%v", got, ok)
	}
	// 空白键拒绝。
	if err := store.PutBinding(connectors.Binding{ConnectorID: "tg-1"}); err == nil {
		t.Fatal("expected error for empty binding chat")
	}
}

func TestClaimInboundDedupes(t *testing.T) {
	store := New(t.TempDir())
	key := "tg-1\x00chat-1\x00msg-7"
	if claimed, err := store.ClaimInbound("tg-1", key); err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	if claimed, err := store.ClaimInbound("tg-1", key); err != nil || claimed {
		t.Fatalf("second claim = %v, %v (want false)", claimed, err)
	}
}

func TestOwnerClaimStaleTakeoverAndHeartbeat(t *testing.T) {
	store := New(t.TempDir())
	id := "tg-owner"
	now := time.Now().UTC()

	claimed, owner, err := store.ClaimConnectorOwner(id, "gateway", now, connectors.DefaultOwnerStaleAfter)
	if err != nil || !claimed || owner != "gateway" {
		t.Fatalf("first claim = %v, %q, %v", claimed, owner, err)
	}
	// 他人持有时不抢占（心跳新鲜）。
	claimed, owner, err = store.ClaimConnectorOwner(id, "other", now, connectors.DefaultOwnerStaleAfter)
	if err != nil || claimed || owner != "gateway" {
		t.Fatalf("foreign claim = %v, %q, %v (want false/gateway)", claimed, owner, err)
	}
	// 续约：仅持有者可刷心跳。
	if held, err := store.HeartbeatConnectorOwner(id, "other", now); err != nil || held {
		t.Fatalf("foreign heartbeat = %v, %v (want false)", held, err)
	}
	if held, err := store.HeartbeatConnectorOwner(id, "gateway", now.Add(time.Minute)); err != nil || !held {
		t.Fatalf("owner heartbeat = %v, %v (want true)", held, err)
	}
	// 陈旧接管：伪造一条 2 小时前的心跳后，later 宿主可认领。
	if _, _, err := store.ClaimConnectorOwner(id, "gateway", now.Add(-2*time.Hour), connectors.DefaultOwnerStaleAfter); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	claimed, owner, err = store.ClaimConnectorOwner(id, "later", now, connectors.DefaultOwnerStaleAfter)
	if err != nil || !claimed || owner != "later" {
		t.Fatalf("stale takeover = %v, %q, %v", claimed, owner, err)
	}
	// 非持有者释放是 no-op；持有者释放生效。
	if err := store.ReleaseConnectorOwner(id, "gateway"); err != nil {
		t.Fatalf("foreign release: %v", err)
	}
	if record, ok := store.OwnerOf(id); !ok || record.Owner != "later" {
		t.Fatalf("foreign release removed owner: %+v ok=%v", record, ok)
	}
	if err := store.ReleaseConnectorOwner(id, "later"); err != nil {
		t.Fatalf("owner release: %v", err)
	}
	if _, ok := store.OwnerOf(id); ok {
		t.Fatal("owner still present after release")
	}
}

// TestTwoInstancesSameDirNoLostUpdate 验证两个 Store 实例指向同一 data-dir 时：
// 配置写入不互相覆盖（读-改-写在数据目录锁内），入站认领恰好一次。
func TestTwoInstancesSameDirNoLostUpdate(t *testing.T) {
	dir := t.TempDir()
	a := New(dir)
	b := New(dir)

	if err := a.PutConfig(testConfig("cfg-a")); err != nil {
		t.Fatalf("a.PutConfig: %v", err)
	}
	if err := b.PutConfig(testConfig("cfg-b")); err != nil {
		t.Fatalf("b.PutConfig: %v", err)
	}
	for name, store := range map[string]*Store{"a": a, "b": b} {
		list := store.ListConfigs()
		if len(list) != 2 {
			t.Fatalf("%s.ListConfigs = %d configs, want 2", name, len(list))
		}
	}

	// 跨实例可见性：a 认领过的键，b 必须看到「已认领」（锁内重读磁盘账本，
	// 而不是吃进程内缓存）。
	if claimed, err := a.ClaimInbound("tg-multi", "msg-visible"); err != nil || !claimed {
		t.Fatalf("a claim = %v, %v (want true)", claimed, err)
	}
	if claimed, err := b.ClaimInbound("tg-multi", "msg-visible"); err != nil || claimed {
		t.Fatalf("b claim = %v, %v (want false)", claimed, err)
	}

	// 并发抢同一条消息：恰好一个实例赢（8 个 worker，与 storefile owner_test
	// 同口径；更大并发会把不公平的重试循环推到 acquire timeout，属于锁实现的
	// 已知边界，不作为本包的语义要求）。
	const workers = 4
	var (
		wg   sync.WaitGroup
		wins atomic.Int32
	)
	for _, store := range []*Store{a, b} {
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(s *Store) {
				defer wg.Done()
				claimed, err := s.ClaimInbound("tg-multi", "same-message")
				if err != nil {
					t.Errorf("ClaimInbound: %v", err)
					return
				}
				if claimed {
					wins.Add(1)
				}
			}(store)
		}
	}
	wg.Wait()
	if got := wins.Load(); got != 1 {
		t.Fatalf("same-message claims = %d, want exactly 1", got)
	}

	// 并发写不同绑定：两个实例都不丢更新（锁内「重读 → 改 → 原子写」）。
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := a.PutBinding(connectors.Binding{ConnectorID: "tg-multi", Chat: "chat-a", Session: connectors.SessionRef{NodeID: "node-a", SessionID: "s-a"}}); err != nil {
			t.Errorf("a.PutBinding: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := b.PutBinding(connectors.Binding{ConnectorID: "tg-multi", Chat: "chat-b", Session: connectors.SessionRef{NodeID: "node-b", SessionID: "s-b"}}); err != nil {
			t.Errorf("b.PutBinding: %v", err)
		}
	}()
	wg.Wait()
	reopened := New(dir)
	if _, ok := reopened.GetBinding("tg-multi", "chat-a"); !ok {
		t.Fatal("binding chat-a lost by concurrent instance")
	}
	if _, ok := reopened.GetBinding("tg-multi", "chat-b"); !ok {
		t.Fatal("binding chat-b lost by concurrent instance")
	}
}

func TestListBindingsFiltersAndPersists(t *testing.T) {
	dir := t.TempDir()
	store := New(dir)
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
	// 输出按 connector+chat 排序（state 文件 map 无序，排序保证确定性）。
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

	// 重建实例：绑定从 connectors-state.json 读回。
	reopened := New(dir)
	again := reopened.ListBindings("tg-1")
	if len(again) != 2 {
		t.Fatalf("重启后 ListBindings(tg-1) = %d 条, want 2", len(again))
	}
	if again[1].Session.NodeID != "n1" || again[1].Session.SessionID != "s1" {
		t.Fatalf("重启后 session = %+v, want n1/s1", again[1].Session)
	}
}
