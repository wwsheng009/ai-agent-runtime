package knowledge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestActivateModeOffIsZeroSideEffect 验证 Phase 1 接入的硬不变量：
// mode=off 时 Activate 不建库、不建锁文件、不起后台任务，句柄为 nil 且所有方法安全。
func TestActivateModeOffIsZeroSideEffect(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/demo.go", demoGoSource)

	act, err := Activate(ctx, DefaultConfig(), root, ActivationOptions{})
	if err != nil {
		t.Fatalf("Activate(off): %v", err)
	}
	if act != nil {
		t.Fatalf("Activate(off) = %#v, want nil（off 必须返回 nil 句柄）", act)
	}

	// nil 句柄契约：接入方不需要任何分支。
	if act.Mode() != ModeOff || act.Enabled() {
		t.Fatalf("nil Activation Mode=%q Enabled=%v, want off/false", act.Mode(), act.Enabled())
	}
	if act.Role() != RoleNone || act.Workspace() != "" || act.DBPath() != "" {
		t.Fatalf("nil Activation role=%q workspace=%q db=%q", act.Role(), act.Workspace(), act.DBPath())
	}
	if stats, err := act.Stats(ctx); err != nil || stats.Files != 0 {
		t.Fatalf("nil Activation Stats=%+v err=%v", stats, err)
	}
	if result, err := act.WaitIndex(ctx); err != nil || result.Indexed != 0 {
		t.Fatalf("nil Activation WaitIndex=%+v err=%v", result, err)
	}
	if _, _, ok := act.LastIndex(); ok {
		t.Fatal("nil Activation LastIndex ok=true, want false")
	}
	if err := act.Close(); err != nil {
		t.Fatalf("nil Activation Close: %v", err)
	}

	// 零副作用：workspace 下不得出现 .aicli/knowledge 目录或锁文件。
	if _, err := os.Stat(filepath.Join(root, ".aicli")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mode=off 后 .aicli 目录存在（err=%v），off 路径必须零副作用", err)
	}
}

func TestActivateRequiresWorkspaceWhenEnabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = ModeShadow
	if _, err := Activate(context.Background(), cfg, "   ", ActivationOptions{}); err == nil {
		t.Fatal("Activate(shadow, 空 workspace) 必须报错，不得静默降级")
	}
}

// TestActivateShadowRunsInitialIndex：owner 角色下后台索引必须真的跑完并可读回结果。
func TestActivateShadowRunsInitialIndex(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/demo.go", demoGoSource)

	cfg := DefaultConfig()
	cfg.Mode = ModeShadow

	done := make(chan IndexResult, 1)
	act, err := Activate(ctx, cfg, root, ActivationOptions{
		OnIndexDone: func(result IndexResult, err error) {
			if err != nil {
				t.Errorf("OnIndexDone err=%v", err)
			}
			done <- result
		},
	})
	if err != nil {
		t.Fatalf("Activate(shadow): %v", err)
	}
	if act == nil {
		t.Fatal("Activate(shadow) 返回 nil 句柄")
	}
	defer act.Close()

	if act.Mode() != ModeShadow || !act.Enabled() {
		t.Fatalf("Mode=%q Enabled=%v, want shadow/true", act.Mode(), act.Enabled())
	}
	if act.Role() != RoleOwner {
		t.Fatalf("Role=%q, want owner（本进程先到）", act.Role())
	}
	if act.DBPath() == "" {
		t.Fatal("DBPath 为空，shadow 必须给出落点")
	}

	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := act.WaitIndex(waitCtx)
	if err != nil {
		t.Fatalf("WaitIndex: %v", err)
	}
	if result.Indexed < 1 {
		t.Fatalf("Indexed=%d, want >=1（demo.go 必须被索引）", result.Indexed)
	}
	select {
	case got := <-done:
		if got.Indexed != result.Indexed {
			t.Fatalf("OnIndexDone Indexed=%d, WaitIndex=%d", got.Indexed, result.Indexed)
		}
	default:
		t.Fatal("OnIndexDone 未被调用")
	}
	if _, _, ok := act.LastIndex(); !ok {
		t.Fatal("LastIndex ok=false，索引已结束")
	}

	stats, err := act.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Files < 1 {
		t.Fatalf("Stats.Files=%d, want >=1", stats.Files)
	}
	if _, err := os.Stat(act.DBPath()); err != nil {
		t.Fatalf("db 文件不存在：%v", err)
	}
}

// TestActivateShadowSkipInitialIndex：短任务/探测路径不建索引，但层仍然是打开的。
func TestActivateShadowSkipInitialIndex(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/demo.go", demoGoSource)

	cfg := DefaultConfig()
	cfg.Mode = ModeShadow

	act, err := Activate(ctx, cfg, root, ActivationOptions{SkipInitialIndex: true})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	defer act.Close()

	// done 已关闭：WaitIndex 立即返回零值而不是阻塞。
	start := time.Now()
	result, err := act.WaitIndex(ctx)
	if err != nil || result.Indexed != 0 {
		t.Fatalf("WaitIndex=%+v err=%v, want 零值", result, err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("WaitIndex 阻塞了 %v，SkipInitialIndex 必须立即返回", elapsed)
	}
	if _, _, ok := act.LastIndex(); ok {
		t.Fatal("SkipInitialIndex 下 LastIndex ok=true")
	}
	stats, err := act.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Files != 0 {
		t.Fatalf("Stats.Files=%d, want 0（未跑索引）", stats.Files)
	}
}

// TestActivateReaderDoesNotIndex：锁被同进程的另一个 owner 持有 → reader，
// 且 reader 不启动索引（避免"读者写库"破坏单写者不变量）。
func TestActivateReaderDoesNotIndex(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/demo.go", demoGoSource)

	cfg := DefaultConfig()
	cfg.Mode = ModeShadow

	owner, err := Activate(ctx, cfg, root, ActivationOptions{SkipInitialIndex: true})
	if err != nil {
		t.Fatalf("Activate(owner): %v", err)
	}
	defer owner.Close()
	if owner.Role() != RoleOwner {
		t.Fatalf("first Role=%q, want owner", owner.Role())
	}

	reader, err := Activate(ctx, cfg, root, ActivationOptions{})
	if err != nil {
		t.Fatalf("Activate(reader): %v", err)
	}
	defer reader.Close()
	if reader.Role() != RoleReader {
		t.Fatalf("second Role=%q, want reader（锁被存活进程持有）", reader.Role())
	}
	if reader.DBPath() != owner.DBPath() {
		t.Fatalf("reader DBPath=%q, owner DBPath=%q", reader.DBPath(), owner.DBPath())
	}
	// reader 的完成通道应立即关闭，且不产生索引结果。
	if result, err := reader.WaitIndex(ctx); err != nil || result.Indexed != 0 {
		t.Fatalf("reader WaitIndex=%+v err=%v, want 零值", result, err)
	}
	if _, _, ok := reader.LastIndex(); ok {
		t.Fatal("reader 不应有后台索引结果")
	}
	stats, err := reader.Stats(ctx)
	if err != nil {
		t.Fatalf("reader Stats: %v", err)
	}
	if stats.Files != 0 {
		t.Fatalf("reader Stats.Files=%d, want 0（owner 未索引）", stats.Files)
	}
}

// TestActivationCloseIsIdempotent：重复 Close 不得 panic 或重复释放。
func TestActivationCloseIsIdempotent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/demo.go", demoGoSource)

	cfg := DefaultConfig()
	cfg.Mode = ModeShadow

	act, err := Activate(ctx, cfg, root, ActivationOptions{SkipInitialIndex: true})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := act.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := act.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
