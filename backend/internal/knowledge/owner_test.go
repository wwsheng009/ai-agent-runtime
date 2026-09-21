package knowledge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOwnershipArbitration(t *testing.T) {
	root := t.TempDir()
	cfg := DefaultConfig().WithWorkspace(root)
	cfg.Mode = ModeShadow

	owner, err := acquireOwnership(cfg)
	if err != nil {
		t.Fatalf("acquireOwnership(first): %v", err)
	}
	if owner.role() != RoleOwner || owner.readOnly() {
		t.Fatalf("first acquirer role = %q readOnly = %v, want owner", owner.role(), owner.readOnly())
	}

	// 同一进程再次获取：锁已被活着的进程（自己）持有 → reader。
	reader, err := acquireOwnership(cfg)
	if err != nil {
		t.Fatalf("acquireOwnership(second): %v", err)
	}
	if reader.role() != RoleReader || !reader.readOnly() {
		t.Fatalf("second acquirer role = %q, want reader", reader.role())
	}
	if reader.holder != os.Getpid() {
		t.Fatalf("reader.holder = %d, want %d", reader.holder, os.Getpid())
	}
	if err := reader.release(); err != nil {
		t.Fatalf("reader.release: %v", err)
	}
	if _, err := os.Stat(cfg.storePath() + ".lock"); err != nil {
		t.Fatalf("reader.release 不得删除 owner 的锁: %v", err)
	}

	// owner 释放后，新的进程（此处仍是本进程）应重新成为 owner。
	if err := owner.release(); err != nil {
		t.Fatalf("owner.release: %v", err)
	}
	if _, err := os.Stat(cfg.storePath() + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("lock 文件应被删除, err=%v", err)
	}
	again, err := acquireOwnership(cfg)
	if err != nil {
		t.Fatalf("acquireOwnership(third): %v", err)
	}
	if again.role() != RoleOwner {
		t.Fatalf("third acquirer role = %q, want owner", again.role())
	}
	_ = again.release()
}

func TestOwnershipStealsStaleLock(t *testing.T) {
	root := t.TempDir()
	cfg := DefaultConfig().WithWorkspace(root)
	cfg.Mode = ModeShadow
	if err := cfg.ensureDir(); err != nil {
		t.Fatalf("ensureDir: %v", err)
	}

	lockPath := cfg.storePath() + ".lock"
	// 写入一个不可能存活的 pid（超过 Windows/Unix 的 pid 上限）。
	payload, err := json.Marshal(lockPayload{PID: 1 << 30, StartedAt: time.Now().UnixMilli()})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(lockPath, payload, 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	own, err := acquireOwnership(cfg)
	if err != nil {
		t.Fatalf("acquireOwnership: %v", err)
	}
	if own.role() != RoleOwner {
		t.Fatalf("role = %q, want owner（陈旧锁必须被抢占）", own.role())
	}
	_ = own.release()
}

func TestOwnershipTreatsCorruptRecentLockAsAlive(t *testing.T) {
	root := t.TempDir()
	cfg := DefaultConfig().WithWorkspace(root)
	cfg.Mode = ModeShadow
	if err := cfg.ensureDir(); err != nil {
		t.Fatalf("ensureDir: %v", err)
	}
	lockPath := cfg.storePath() + ".lock"
	if err := os.WriteFile(lockPath, []byte("not-json"), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	own, err := acquireOwnership(cfg)
	if err != nil {
		t.Fatalf("acquireOwnership: %v", err)
	}
	if own.role() != RoleReader {
		t.Fatalf("role = %q, want reader（损坏但新鲜的锁不得被抢占）", own.role())
	}
}

func TestLayerLifecycleAndMode(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/demo.go", demoGoSource)

	// off：返回 nil layer，且所有方法对 nil 安全。
	layer, err := Open(ctx, DefaultConfig().WithWorkspace(root))
	if err != nil {
		t.Fatalf("Open(off): %v", err)
	}
	if layer != nil {
		t.Fatalf("Open(off) = %+v, want nil", layer)
	}
	if layer.Enabled() || layer.Injects() || layer.Role() != RoleNone {
		t.Fatal("nil layer 必须报告为已禁用")
	}
	if _, err := layer.Index(ctx); err != nil {
		t.Fatalf("nil layer Index: %v", err)
	}
	if _, err := layer.Stats(ctx); err != nil {
		t.Fatalf("nil layer Stats: %v", err)
	}
	if err := layer.Close(); err != nil {
		t.Fatalf("nil layer Close: %v", err)
	}

	// shadow：建索引但不注入。
	shadow, err := Open(ctx, DefaultConfig().WithWorkspace(root))
	if err != nil {
		t.Fatalf("Open(off, again): %v", err)
	}
	_ = shadow
	cfg := DefaultConfig().WithWorkspace(root)
	cfg.Mode = ModeShadow
	layer, err = Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open(shadow): %v", err)
	}
	defer layer.Close()
	if !layer.Enabled() || layer.Injects() {
		t.Fatalf("shadow: enabled=%v injects=%v, want true/false", layer.Enabled(), layer.Injects())
	}
	if layer.Role() != RoleOwner {
		t.Fatalf("role = %q, want owner", layer.Role())
	}

	result, err := layer.Index(ctx)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if result.Indexed != 1 || result.Symbols == 0 {
		t.Fatalf("index result = %+v", result)
	}

	stats, err := layer.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Files != 1 || stats.Symbols == 0 {
		t.Fatalf("stats = %+v", stats)
	}

	syms, err := layer.FindSymbols(ctx, SymbolQuery{Name: "OpenFile", Exact: true})
	if err != nil || len(syms) != 1 {
		t.Fatalf("FindSymbols = %+v (err=%v)", syms, err)
	}
	hits, err := layer.Search(ctx, SearchQuery{Text: "OpenFile"})
	if err != nil || len(hits) != 1 {
		t.Fatalf("Search = %+v (err=%v)", hits, err)
	}
	if err := layer.RecordInvalidation(ctx, "manual", ""); err != nil {
		t.Fatalf("RecordInvalidation: %v", err)
	}

	// on：注入面打开。
	cfg.Mode = ModeOn
	onLayer, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open(on): %v", err)
	}
	defer onLayer.Close()
	if onLayer.Role() != RoleReader {
		t.Fatalf("第二个 Layer 应为 reader, got %q", onLayer.Role())
	}
	if !onLayer.Injects() {
		t.Fatal("mode=on 且 store 可用时必须注入")
	}
	// reader 写库必须显式失败（而不是静默丢弃）。
	if err := onLayer.RecordInvalidation(ctx, "manual", ""); err == nil {
		t.Fatal("reader 的写操作必须失败")
	}
}

func TestOpenRejectsInvalidConfig(t *testing.T) {
	ctx := context.Background()
	cfg := DefaultConfig()
	cfg.Mode = ModeShadow // 未设置 workspace
	if _, err := Open(ctx, cfg); err == nil {
		t.Fatal("Open 必须拒绝缺少 workspace 的启用配置")
	}

	cfg = DefaultConfig()
	cfg.Mode = Mode("bogus")
	cfg.Workspace = t.TempDir()
	if _, err := Open(ctx, cfg); err == nil {
		t.Fatal("Open 必须拒绝非法 mode")
	}
}

func TestConfigStorePath(t *testing.T) {
	root := t.TempDir()
	cfg := DefaultConfig().WithWorkspace(root)
	want := filepath.Join(root, filepath.FromSlash(DefaultDBRelativePath))
	if got := cfg.storePath(); got != want {
		t.Fatalf("storePath = %q, want %q", got, want)
	}

	cfg.DBPath = "custom/kb.db"
	if got := cfg.storePath(); got != filepath.Join(root, "custom", "kb.db") {
		t.Fatalf("storePath(relative db_path) = %q", got)
	}

	abs := filepath.Join(t.TempDir(), "abs.db")
	cfg.DBPath = abs
	if got := cfg.storePath(); got != abs {
		t.Fatalf("storePath(absolute db_path) = %q, want %q", got, abs)
	}
}

func TestParseModeAndNormalize(t *testing.T) {
	cases := map[string]Mode{
		"":          ModeOff,
		"off":       ModeOff,
		"OFF":       ModeOff,
		"disabled":  ModeOff,
		"shadow":    ModeShadow,
		"Shadow":    ModeShadow,
		"on":        ModeOn,
		"1":         ModeOn,
		"  enabled": ModeOn,
	}
	for input, want := range cases {
		got, err := ParseMode(input)
		if err != nil {
			t.Fatalf("ParseMode(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("ParseMode(%q) = %q, want %q", input, got, want)
		}
	}
	if _, err := ParseMode("sometimes"); err == nil {
		t.Fatal("ParseMode 必须拒绝未知值")
	}

	// Normalize 补齐限额，但不改变 mode 语义。
	cfg := Config{Mode: ModeShadow}
	normalized := cfg.Normalize()
	if normalized.MaxFileBytes != DefaultMaxFileBytes || normalized.MaxDBSizeMB != DefaultMaxDBSizeMB {
		t.Fatalf("Normalize 未补齐限额: %+v", normalized)
	}
	if DefaultConfig().Enabled() {
		t.Fatal("默认配置必须是关闭的")
	}
}
