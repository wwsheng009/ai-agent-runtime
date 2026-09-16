package runtimeserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func usageLedgerConfig(enabled bool, driver, dsn string) *config.Config {
	return &config.Config{
		Database: config.DatabaseConfig{Driver: driver, DSN: dsn},
		SkillsRuntime: &config.SkillsRuntimeConfig{
			UsageLedgerEnabled: enabled,
		},
	}
}

// 未启用账本：正常的关闭态，既没有 store 也不该有 reason（接口返回「未配置」）。
func TestResolveUsageLedgerStore_DisabledReturnsNilWithoutReason(t *testing.T) {
	store, reason := ResolveUsageLedgerStore(usageLedgerConfig(false, "sqlite", ""))

	if store != nil {
		t.Fatalf("expected nil store when ledger disabled, got %#v", store)
	}
	if reason != "" {
		t.Fatalf("expected empty reason when ledger disabled, got %q", reason)
	}
}

func TestResolveUsageLedgerStore_NilConfigReturnsNilWithoutReason(t *testing.T) {
	store, reason := ResolveUsageLedgerStore(nil)

	if store != nil || reason != "" {
		t.Fatalf("expected nil store and empty reason, got store=%#v reason=%q", store, reason)
	}
}

// 已启用但 dsn 为空：降级为 reason，而不是让调用方启动失败。
func TestResolveUsageLedgerStore_EmptyDSNReturnsReason(t *testing.T) {
	store, reason := ResolveUsageLedgerStore(usageLedgerConfig(true, "sqlite", "   "))

	if store != nil {
		t.Fatalf("expected nil store when dsn empty, got %#v", store)
	}
	if !strings.Contains(reason, "database.dsn is empty") {
		t.Fatalf("expected actionable dsn reason, got %q", reason)
	}
}

// 已启用但驱动不是 sqlite：目前只支持 sqlite，这类环境必须能启动并把原因透出。
func TestResolveUsageLedgerStore_UnsupportedDriverReturnsReason(t *testing.T) {
	store, reason := ResolveUsageLedgerStore(usageLedgerConfig(true, "postgres", "postgres://user@host/db"))

	if store != nil {
		t.Fatalf("expected nil store for unsupported driver, got %#v", store)
	}
	if !strings.Contains(reason, "unsupported usage ledger driver") {
		t.Fatalf("expected driver reason, got %q", reason)
	}
}

// 建表/建目录失败（这里用 dsn 指向一个已存在的文件作为父目录）也要降级为 reason。
func TestResolveUsageLedgerStore_UnwritablePathReturnsReason(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("prepare blocker file: %v", err)
	}

	store, reason := ResolveUsageLedgerStore(usageLedgerConfig(true, "sqlite", filepath.Join(blocker, "ledger.db")))

	if store != nil {
		_ = store.Close()
		t.Fatalf("expected nil store for unwritable path, got %#v", store)
	}
	if strings.TrimSpace(reason) == "" {
		t.Fatal("expected non-empty reason for unwritable path")
	}
}

// 正常路径：sqlite + 可写目录应构建成功，且 reason 为空（避免把成功误判成降级）。
func TestResolveUsageLedgerStore_SQLiteHappyPath(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "nested", "ledger.db")

	store, reason := ResolveUsageLedgerStore(usageLedgerConfig(true, "sqlite", dsn))
	if reason != "" {
		t.Fatalf("expected empty reason on happy path, got %q", reason)
	}
	if store == nil {
		t.Fatal("expected non-nil store on happy path")
	}
	t.Cleanup(func() { _ = store.Close() })
}
