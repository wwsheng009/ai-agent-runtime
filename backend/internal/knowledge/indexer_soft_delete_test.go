package knowledge

import (
	"context"
	"os"
	"testing"
)

// TestRunIndexSoftDeletesAndResurrects 钉住 04 §5 Phase 1 交付 3 的软删除契约：
//   - 磁盘删除 → files.deleted_at 标记（index_state=stale）、其 symbols 同步标记，
//     FindSymbols / Stats / ListActiveFiles 立即不可见；
//   - 重复对账幂等（Deleated 不再增加）；
//   - 文件重新出现 → UpsertFile 复活（清空 deleted_at）、ReplaceSymbols 重建符号。
func TestRunIndexSoftDeletesAndResurrects(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/keep.go", demoGoSource)
	gonePath := writeTree(t, root, "demo/gone.go", "package demo\n\nfunc Gone() {}\n")

	store := newTestStore(t)
	cfg := DefaultConfig().WithWorkspace(root)

	first, err := RunIndex(ctx, store, cfg)
	if err != nil {
		t.Fatalf("RunIndex(first): %v", err)
	}
	if first.Deleted != 0 {
		t.Fatalf("first.Deleted = %d, want 0", first.Deleted)
	}

	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: root})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}

	if err := os.Remove(gonePath); err != nil {
		t.Fatalf("remove gone.go: %v", err)
	}
	second, err := RunIndex(ctx, store, cfg)
	if err != nil {
		t.Fatalf("RunIndex(second): %v", err)
	}
	if second.Deleted != 1 {
		t.Fatalf("second.Deleted = %d, want 1 (%+v)", second.Deleted, second)
	}

	rec, ok, err := store.FileByPath(ctx, wsID, "demo/gone.go")
	if err != nil || !ok {
		t.Fatalf("FileByPath(gone.go): ok=%v err=%v（软删除必须保留行）", ok, err)
	}
	if rec.DeletedAt == 0 {
		t.Fatalf("deleted_at 未标记: %+v", rec)
	}
	if rec.IndexState != IndexStale {
		t.Fatalf("index_state = %q, want %q", rec.IndexState, IndexStale)
	}

	gone, err := store.FindSymbols(ctx, SymbolQuery{Name: "Gone", Exact: true})
	if err != nil {
		t.Fatalf("FindSymbols(Gone): %v", err)
	}
	if len(gone) != 0 {
		t.Fatalf("已删除文件的符号必须不可见: %+v", gone)
	}

	active, err := store.ListActiveFiles(ctx, wsID)
	if err != nil {
		t.Fatalf("ListActiveFiles: %v", err)
	}
	if len(active) != 1 || active[0].Path != "demo/keep.go" {
		t.Fatalf("active = %+v, want 仅 demo/keep.go", active)
	}

	stats, err := store.Stats(ctx, wsID)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Files != 1 {
		t.Fatalf("stats.Files = %d, want 1（软删除不进统计）", stats.Files)
	}

	// 幂等：同一删除状态再次对账不得重复计数。
	third, err := RunIndex(ctx, store, cfg)
	if err != nil {
		t.Fatalf("RunIndex(third): %v", err)
	}
	if third.Deleted != 0 {
		t.Fatalf("third.Deleted = %d, want 0", third.Deleted)
	}

	// 文件恢复：同样的路径/内容必须复活。
	writeTree(t, root, "demo/gone.go", "package demo\n\nfunc Gone() {}\n")
	fourth, err := RunIndex(ctx, store, cfg)
	if err != nil {
		t.Fatalf("RunIndex(fourth): %v", err)
	}
	if fourth.Deleted != 0 || fourth.Indexed != 1 {
		t.Fatalf("fourth = %+v, want deleted=0 indexed=1（复活要重新解析）", fourth)
	}
	rec, ok, err = store.FileByPath(ctx, wsID, "demo/gone.go")
	if err != nil || !ok {
		t.Fatalf("FileByPath after restore: ok=%v err=%v", ok, err)
	}
	if rec.DeletedAt != 0 {
		t.Fatalf("复活后 deleted_at 必须清空: %+v", rec)
	}
	gone, err = store.FindSymbols(ctx, SymbolQuery{Name: "Gone", Exact: true})
	if err != nil || len(gone) != 1 {
		t.Fatalf("复活后符号必须回来: %+v (err=%v)", gone, err)
	}
}
