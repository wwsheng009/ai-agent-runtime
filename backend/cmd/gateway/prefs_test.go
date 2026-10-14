package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/notify"
)

// TestLoadNotifyPreferences 锁定 gateway 的偏好文件装载（06 §1 D6）：
// 缺失文件 = 无偏好；合法文件三级生效；非法 scope/min_priority 报错。
func TestLoadNotifyPreferences(t *testing.T) {
	if store, err := loadNotifyPreferences(filepath.Join(t.TempDir(), "absent.json")); err != nil || store != nil {
		t.Fatalf("absent = %v / %v", store, err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "notify-prefs.json")
	content := `{"scopes":[
	  {"scope":"global","muted":false},
	  {"scope":"channel:ntfy","muted":true},
	  {"scope":"session:s-1","min_priority":"high"}
	]}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := loadNotifyPreferences(path)
	if err != nil || store == nil {
		t.Fatalf("load = %v / %v", store, err)
	}
	if eff := store.Resolve("s-1", "ntfy"); !eff.Muted {
		t.Fatalf("channel:ntfy 静音 = %+v", eff)
	}
	if eff := store.Resolve("s-1", "webpush"); eff.Allows(notify.PriorityNormal) {
		t.Fatalf("session 门槛 high = %+v", eff)
	}
	if eff := store.Resolve("s-2", "webpush"); !eff.Allows(notify.PriorityNormal) {
		t.Fatalf("其它会话不受影响 = %+v", eff)
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"scopes":[{"scope":"bogus"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadNotifyPreferences(bad); err == nil {
		t.Fatal("非法 scope 应报错")
	}
	badPrio := filepath.Join(dir, "badprio.json")
	if err := os.WriteFile(badPrio, []byte(`{"scopes":[{"scope":"global","min_priority":"urgent"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadNotifyPreferences(badPrio); err == nil {
		t.Fatal("非法 min_priority 应报错")
	}
}
