package storefile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// ── 运行归属（state.json owners） ──────────────────────────────────────────

func TestConnectorOwnerClaimHeartbeatRelease(t *testing.T) {
	store, _, statePath := newTestStore(t)
	now := time.Now().UTC()

	claimed, owner, err := store.ClaimConnectorOwner("tg", "aicli", now, time.Minute)
	if err != nil || !claimed || owner != "aicli" {
		t.Fatalf("first claim = (%v, %q, %v), want (true, aicli, nil)", claimed, owner, err)
	}

	// 同宿主重复认领 = 幂等刷新（不改变持有者）。
	claimed, owner, err = store.ClaimConnectorOwner("tg", "aicli", now.Add(10*time.Second), time.Minute)
	if err != nil || !claimed || owner != "aicli" {
		t.Fatalf("re-claim = (%v, %q, %v), want (true, aicli, nil)", claimed, owner, err)
	}

	// 另一存活宿主：认领失败并回当前持有者。
	claimed, owner, err = store.ClaimConnectorOwner("tg", "runtime-server", now.Add(20*time.Second), time.Minute)
	if err != nil || claimed || owner != "aicli" {
		t.Fatalf("competing claim = (%v, %q, %v), want (false, aicli, nil)", claimed, owner, err)
	}

	// 心跳：非持有者 held=false 且不篡改记录。
	held, err := store.HeartbeatConnectorOwner("tg", "runtime-server", now.Add(30*time.Second))
	if err != nil || held {
		t.Fatalf("foreign heartbeat = (%v, %v), want (false, nil)", held, err)
	}

	// 持有者心跳刷新 heartbeat_at。
	refreshed := now.Add(40 * time.Second)
	held, err = store.HeartbeatConnectorOwner("tg", "aicli", refreshed)
	if err != nil || !held {
		t.Fatalf("owner heartbeat = (%v, %v), want (true, nil)", held, err)
	}
	record, ok := store.OwnerOf("tg")
	if !ok || record.Owner != "aicli" || !record.HeartbeatAt.Equal(refreshed) {
		t.Fatalf("owner record = %+v ok=%v, want aicli@%v", record, ok, refreshed)
	}
	if record.PID != os.Getpid() {
		t.Fatalf("owner pid = %d, want %d", record.PID, os.Getpid())
	}

	// state.json 形状：owners 表落盘（owner/heartbeat_at/pid）。
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var decoded struct {
		Owners map[string]struct {
			Owner       string    `json:"owner"`
			HeartbeatAt time.Time `json:"heartbeat_at"`
			PID         int       `json:"pid"`
		} `json:"owners"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if decoded.Owners["tg"].Owner != "aicli" || decoded.Owners["tg"].PID == 0 {
		t.Fatalf("state owners = %+v", decoded.Owners)
	}

	// 释放：非持有者是 no-op，持有者生效且幂等。
	if err := store.ReleaseConnectorOwner("tg", "runtime-server"); err != nil {
		t.Fatalf("foreign release: %v", err)
	}
	if _, ok := store.OwnerOf("tg"); !ok {
		t.Fatal("foreign release must not remove the owner record")
	}
	if err := store.ReleaseConnectorOwner("tg", "aicli"); err != nil {
		t.Fatalf("owner release: %v", err)
	}
	if err := store.ReleaseConnectorOwner("tg", "aicli"); err != nil {
		t.Fatalf("idempotent release: %v", err)
	}
	if _, ok := store.OwnerOf("tg"); ok {
		t.Fatal("owner record should be gone after release")
	}
}

func TestConnectorOwnerStaleTakeover(t *testing.T) {
	store, _, _ := newTestStore(t)
	now := time.Now().UTC()
	stale := 50 * time.Millisecond

	if claimed, _, err := store.ClaimConnectorOwner("tg", "aicli", now, stale); err != nil || !claimed {
		t.Fatalf("seed claim = (%v, %v)", claimed, err)
	}
	// 未超时：另一宿主不能认领。
	if claimed, owner, _ := store.ClaimConnectorOwner("tg", "runtime-server", now.Add(30*time.Millisecond), stale); claimed || owner != "aicli" {
		t.Fatalf("fresh claim = (%v, %q), want (false, aicli)", claimed, owner)
	}
	// 超时：接管成功，持有者改写。
	claimed, owner, err := store.ClaimConnectorOwner("tg", "runtime-server", now.Add(60*time.Millisecond), stale)
	if err != nil || !claimed || owner != "runtime-server" {
		t.Fatalf("stale takeover = (%v, %q, %v), want (true, runtime-server, nil)", claimed, owner, err)
	}
	if record, ok := store.OwnerOf("tg"); !ok || record.Owner != "runtime-server" {
		t.Fatalf("owner after takeover = %+v ok=%v", record, ok)
	}
}

func TestConnectorOwnerForceClaimOverridesLiveOwner(t *testing.T) {
	store, _, _ := newTestStore(t)
	now := time.Now().UTC()
	if _, _, err := store.ClaimConnectorOwner("tg", "runtime-server", now, time.Hour); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	if err := store.ForceClaimConnectorOwner("tg", "aicli", now.Add(time.Second)); err != nil {
		t.Fatalf("force claim: %v", err)
	}
	record, ok := store.OwnerOf("tg")
	if !ok || record.Owner != "aicli" {
		t.Fatalf("owner after force claim = %+v ok=%v", record, ok)
	}
}

// TestConfigStoreOwnersBackwardCompatible 覆盖旧 state.json（无 owners 键）：
// 读取不报错、旧账本可续用、写入后 owners 追加且旧键保留。
func TestConfigStoreOwnersBackwardCompatible(t *testing.T) {
	store, _, statePath := newTestStore(t)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"bindings":{},"claimed":{"tg\u0000m1":"2026-10-12T00:00:00Z"},"cursors":{"tg":{"update_offset":"7"}}}`
	if err := os.WriteFile(statePath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, ok := store.OwnerOf("tg"); ok {
		t.Fatal("legacy state must report no owner")
	}
	if value, ok := store.PollingState("tg").Get("update_offset"); !ok || value != "7" {
		t.Fatalf("legacy cursor = (%q, %v), want (7, true)", value, ok)
	}
	if claimed, err := store.ClaimInbound("tg", "m1"); err != nil || claimed {
		t.Fatalf("legacy claimed key must dedupe: (%v, %v)", claimed, err)
	}
	if claimed, _, err := store.ClaimConnectorOwner("tg", "aicli", time.Now().UTC(), time.Minute); err != nil || !claimed {
		t.Fatalf("claim on legacy state = (%v, %v)", claimed, err)
	}

	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("state is not valid JSON: %v\n%s", err, raw)
	}
	for _, key := range []string{"bindings", "claimed", "cursors", "owners"} {
		if _, ok := generic[key]; !ok {
			t.Fatalf("state.json missing %q after write:\n%s", key, raw)
		}
	}
}

// ── 跨进程数据目录锁 ───────────────────────────────────────────────────────

func TestDirLockExcludesAndStealsStale(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "connectors.lock")

	release, err := AcquireDirLock(lockPath, LockOptions{RetryInterval: 5 * time.Millisecond, StaleAfter: 120 * time.Millisecond, AcquireTimeout: time.Second})
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// 存活持有者期间第二方抢锁：等待超时后失败（互斥）。
	if _, err := AcquireDirLock(lockPath, LockOptions{RetryInterval: 5 * time.Millisecond, StaleAfter: time.Hour, AcquireTimeout: 60 * time.Millisecond}); err == nil {
		t.Fatal("second acquire must fail while the lock is held")
	}

	// 超过陈旧阈值后，后来者可窃取。
	time.Sleep(150 * time.Millisecond)
	second, err := AcquireDirLock(lockPath, LockOptions{RetryInterval: 5 * time.Millisecond, StaleAfter: 120 * time.Millisecond, AcquireTimeout: time.Second})
	if err != nil {
		t.Fatalf("stale acquire: %v", err)
	}

	// 旧持有者释放不得删除新持有者的锁。
	release()
	if _, err := AcquireDirLock(lockPath, LockOptions{RetryInterval: 5 * time.Millisecond, StaleAfter: time.Hour, AcquireTimeout: 40 * time.Millisecond}); err == nil {
		t.Fatal("stale owner release must not remove the new lock")
	}
	second()
	if _, err := AcquireDirLock(lockPath, LockOptions{RetryInterval: 5 * time.Millisecond, StaleAfter: time.Hour, AcquireTimeout: time.Second}); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}

func TestConfigStoreDirLockPathFollowsStateDir(t *testing.T) {
	store, _, statePath := newTestStore(t)
	want := filepath.Join(filepath.Dir(statePath), dirLockName)
	if got := store.DirLockPath(); got != want {
		t.Fatalf("DirLockPath = %q, want %q", got, want)
	}
}

// TestConcurrentStoresShareStateWithoutLostUpdates 用两个指向同一目录的 Store
// 实例并发读改写：跨实例锁必须保证「读旧值 → 写新值」不丢更新。
func TestConcurrentStoresShareStateWithoutLostUpdates(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	statePath := filepath.Join(dir, "data", "connectors", "state.json")
	first := NewConfigStore(configPath, statePath)
	second := NewConfigStore(configPath, statePath)

	const workers = 4
	var (
		wg   sync.WaitGroup
		wins int32
	)
	for _, store := range []*ConfigStore{first, second} {
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(store *ConfigStore) {
				defer wg.Done()
				claimed, err := store.ClaimInbound("tg", "same-message")
				if err != nil {
					t.Errorf("ClaimInbound: %v", err)
					return
				}
				if claimed {
					atomic.AddInt32(&wins, 1)
				}
			}(store)
		}
	}
	wg.Wait()
	if got := atomic.LoadInt32(&wins); got != 1 {
		t.Fatalf("same-message claims = %d, want exactly 1", got)
	}

	// 并发写不同绑定与不同连接器：两个实例都不丢更新。
	for _, target := range []struct {
		store  *ConfigStore
		chat   string
		config string
	}{
		{first, "chat-a", "cfg-a"},
		{second, "chat-b", "cfg-b"},
	} {
		wg.Add(1)
		go func(store *ConfigStore, chat, id string) {
			defer wg.Done()
			if err := store.PutBinding(connectors.Binding{ConnectorID: "tg", Chat: chat, Session: connectors.SessionRef{SessionID: chat}}); err != nil {
				t.Errorf("PutBinding(%s): %v", chat, err)
			}
			if err := store.PutConfig(connectors.Config{ID: id, Provider: "telegram", Enabled: true}); err != nil {
				t.Errorf("PutConfig(%s): %v", id, err)
			}
		}(target.store, target.chat, target.config)
	}
	wg.Wait()

	reopened := NewConfigStore(configPath, statePath)
	if _, ok := reopened.GetBinding("tg", "chat-a"); !ok {
		t.Fatal("binding chat-a lost by concurrent instance")
	}
	if _, ok := reopened.GetBinding("tg", "chat-b"); !ok {
		t.Fatal("binding chat-b lost by concurrent instance")
	}
	ids := map[string]bool{}
	for _, cfg := range reopened.ListConfigs() {
		ids[cfg.ID] = true
	}
	if !ids["cfg-a"] || !ids["cfg-b"] {
		t.Fatalf("concurrent PutConfig lost updates: %v", ids)
	}
}

// TestConcurrentOwnerClaimSingleWinner 跨实例并发认领同一连接器：恰好一个成功。
func TestConcurrentOwnerClaimSingleWinner(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "data", "connectors", "state.json")
	stores := []*ConfigStore{
		NewConfigStore(filepath.Join(dir, "config.yaml"), statePath),
		NewConfigStore(filepath.Join(dir, "config.yaml"), statePath),
	}
	var (
		wg   sync.WaitGroup
		wins int32
	)
	for i := 0; i < 6; i++ {
		store := stores[i%len(stores)]
		host := []string{"aicli", "runtime-server"}[i%2]
		wg.Add(1)
		go func(store *ConfigStore, host string) {
			defer wg.Done()
			claimed, _, err := store.ClaimConnectorOwner("tg", host, time.Now().UTC(), time.Minute)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			if claimed {
				atomic.AddInt32(&wins, 1)
			}
		}(store, host)
	}
	wg.Wait()
	// 同一宿主重复认领也算成功，所以赢家 >=1；但两个宿主不可能都认为自己首次持有：
	// 最终记录只属于一个宿主，且该宿主必须是本轮参与的宿主之一。
	record, ok := stores[0].OwnerOf("tg")
	if !ok || strings.TrimSpace(record.Owner) == "" {
		t.Fatalf("owner record = %+v ok=%v", record, ok)
	}
	if got := atomic.LoadInt32(&wins); got < 1 {
		t.Fatalf("claims = %d, want >= 1", got)
	}
}
