package storefile

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/statepath"
)

// TestConfigStoreMigratesLegacyStateOnFirstOpen 覆盖 runtime-server / aicli 宿主
// 的 D1 迁移：storefile 规范 state 缺失时，旧位置（cwd .aicli / configPath 同级）
// 的绑定/游标/归属并集迁移，旧文件改名 .bak，重开幂等。
func TestConfigStoreMigratesLegacyStateOnFirstOpen(t *testing.T) {
	canonicalDir := t.TempDir()
	t.Setenv(statepath.EnvOverride, filepath.Join(canonicalDir, "state.json"))

	legacyDir := t.TempDir()
	older := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	newer := time.Now().UTC().Truncate(time.Second)

	// 候选 1：cwd .aicli（旧 runtime-server 落点）——较新的绑定 + 归属。
	// JSON 键的 NUL 用 \u0000 转义（与 Go json.Marshal 写 state.json 的形状一致；
	// 原始 NUL 是非法 JSON，Unmarshal 会静默跳过——这正是本用例防的回归点）。
	cwdLegacy := filepath.Join(legacyDir, ".aicli", "data", "connectors", "state.json")
	if err := os.MkdirAll(filepath.Dir(cwdLegacy), 0o755); err != nil {
		t.Fatalf("mkdir cwd legacy: %v", err)
	}
	cwdJSON := `{
		"bindings": {"wx-1\u0000chat-1": {"connector_id":"wx-1","chat":"chat-1","session_id":"s-new","updated_at":"` + newer.Format(time.RFC3339Nano) + `"}},
		"owners": {"wx-1": {"owner":"runtime-server","heartbeat_at":"` + newer.Format(time.RFC3339Nano) + `","pid":222}}
	}`
	if err := os.WriteFile(cwdLegacy, []byte(cwdJSON), 0o600); err != nil {
		t.Fatalf("write cwd legacy: %v", err)
	}

	// 候选 2：configPath 同级 data 目录（旧 aicli 落点）——较旧的绑定 + 游标。
	configPath := filepath.Join(legacyDir, "config.yaml")
	siblingLegacy := filepath.Join(legacyDir, "data", "connectors", "state.json")
	if err := os.MkdirAll(filepath.Dir(siblingLegacy), 0o755); err != nil {
		t.Fatalf("mkdir sibling legacy: %v", err)
	}
	siblingJSON := `{
		"bindings": {"wx-1\u0000chat-1": {"connector_id":"wx-1","chat":"chat-1","session_id":"s-old","updated_at":"` + older.Format(time.RFC3339Nano) + `"}},
		"cursors": {"wx-1": {"sync_buf":"cursor-legacy"}}
	}`
	if err := os.WriteFile(siblingLegacy, []byte(siblingJSON), 0o600); err != nil {
		t.Fatalf("write sibling legacy: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("connectors: []\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Chdir(legacyDir) // 让 "cwd .aicli" 候选命中候选 1

	statePath, err := statepath.CanonicalStatePath()
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	store := NewConfigStore(configPath, statePath)

	// 首次打开（读路径）触发迁移；updated_at 新者胜。
	binding, ok := store.GetBinding("wx-1", "chat-1")
	if !ok {
		t.Fatal("migrated binding not found")
	}
	if binding.Session.SessionID != "s-new" {
		t.Fatalf("binding winner = %q, want s-new", binding.Session.SessionID)
	}
	// 归属并入。
	if _, claimed := store.OwnerOf("wx-1"); !claimed {
		t.Fatal("owner ledger not migrated")
	}
	// 游标并入 + 幂等（重开不丢）。
	reopened := NewConfigStore(configPath, statePath)
	cursors := reopened.PollingState("wx-1")
	if got, _ := cursors.Get("sync_buf"); got != "cursor-legacy" {
		t.Fatalf("cursor not migrated: sync_buf=%q", got)
	}
	if _, ok := reopened.GetBinding("wx-1", "chat-1"); !ok {
		t.Fatal("migrated binding lost on reopen")
	}
	// 旧文件已归档。
	for _, legacy := range []string{cwdLegacy, siblingLegacy} {
		if _, err := os.Stat(legacy); !os.IsNotExist(err) {
			t.Fatalf("legacy state not archived (%s): %v", legacy, err)
		}
	}
	_ = connectors.DefaultOwnerStaleAfter // 保持 import（测试可能按平台裁剪）
}
