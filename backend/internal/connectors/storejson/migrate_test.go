package storejson

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/statepath"
)

// writeFile 是测试助手：创建父目录后写文件。
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// bindingKeyJSON 构造 JSON 键：connectorID + \u0000 + chat（NUL 按 JSON 转义，
// 与 Go json.Marshal 写 state.json 的形状一致；原始 NUL 是非法 JSON）。
func bindingKeyJSON(connectorID, chat string) string {
	return connectorID + `\u0000` + chat
}

// TestMigratesLegacyStateOnFirstOpen 覆盖 D1 迁移：规范 state 缺失时，旧位置
// （cwd .aicli / configPath 同级）的绑定/游标/归属按 09 §4.2 规则并集迁移，
// 旧文件改名 .bak；再次打开不重复迁移（幂等）。
func TestMigratesLegacyStateOnFirstOpen(t *testing.T) {
	canonicalDir := t.TempDir()
	t.Setenv(statepath.EnvOverride, filepath.Join(canonicalDir, "state.json"))

	legacyDir := t.TempDir()
	older := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	newer := time.Now().UTC().Truncate(time.Second)

	// 候选 1：cwd .aicli（旧 runtime-server 的实际落点），持有较新的绑定 + claimed。
	cwdLegacy := filepath.Join(legacyDir, ".aicli", "data", "connectors", "state.json")
	writeFile(t, cwdLegacy, `{
		"bindings": {"`+bindingKeyJSON("tg-1", "chat-1")+`": {"connector_id":"tg-1","chat":"chat-1","session_id":"s-new","updated_at":"`+newer.Format(time.RFC3339Nano)+`"}},
		"claimed": {"tg-1\u0000chat-1\u0000m-1": "`+newer.Format(time.RFC3339Nano)+`"}
	}`)

	// 候选 2：configPath 同级 data 目录（旧 aicli 落点），持有较旧的绑定 + 游标 + 归属。
	siblingLegacy := filepath.Join(legacyDir, "data", "connectors", "state.json")
	writeFile(t, siblingLegacy, `{
		"bindings": {"`+bindingKeyJSON("tg-1", "chat-1")+`": {"connector_id":"tg-1","chat":"chat-1","session_id":"s-old","updated_at":"`+older.Format(time.RFC3339Nano)+`"}},
		"cursors": {"tg-1": {"sync_buf":"cursor-legacy"}},
		"owners": {"tg-1": {"owner":"aicli","heartbeat_at":"`+older.Format(time.RFC3339Nano)+`","pid":111}}
	}`)

	// 让 cwd = legacyDir：LegacyCandidates 的 "cwd .aicli" 候选命中候选 1；
	// configPath 同级候选命中候选 2。
	t.Chdir(legacyDir)
	configPath := filepath.Join(legacyDir, "config.yaml")
	writeFile(t, configPath, "connectors: []\n")

	statePath, err := statepath.CanonicalStatePath()
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	// 注入历史 configPath 提示：让"configPath 同级"候选命中候选 2。
	store := NewWithStatePath(legacyDir, statePath).WithLegacyConfigHints(configPath)

	// 首次打开（读路径）触发迁移。
	binding, ok := store.GetBinding("tg-1", "chat-1")
	if !ok {
		t.Fatal("migrated binding not found")
	}
	// updated_at 新者胜：cwd 候选的 s-new 胜出。
	if binding.Session.SessionID != "s-new" {
		t.Fatalf("binding winner = %q, want s-new", binding.Session.SessionID)
	}
	// 幂等账本并入：同一 key 已 claimed（迁移来的时间戳），重复 claim 应为 false。
	if claimed, err := store.ClaimInbound("tg-1", "chat-1\x00m-1"); err != nil || claimed {
		t.Fatalf("claimed ledger not migrated: claimed=%v err=%v", claimed, err)
	}
	// 游标并入（经 reopen 验证持久化）。
	reopened := NewWithStatePath(legacyDir, statePath).WithLegacyConfigHints(configPath)
	if _, ok := reopened.GetBinding("tg-1", "chat-1"); !ok {
		t.Fatal("migrated binding lost on reopen")
	}
	if strings.TrimSpace(statePath) == "" {
		t.Fatal("empty canonical state path")
	}
	// 旧文件已改名 .bak-*（best-effort，但本平台 rename 应成功）。
	for _, legacy := range []string{cwdLegacy, siblingLegacy} {
		if _, err := os.Stat(legacy); !os.IsNotExist(err) {
			t.Fatalf("legacy state not archived (%s): %v", legacy, err)
		}
	}
}
