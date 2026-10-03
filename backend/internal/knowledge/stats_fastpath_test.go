package knowledge

import (
	"context"
	"testing"
	"time"
)

// indexedAtFastPath 暴露 store 的可选快路径（与 internal/tools 的结构化接口同形）。
//
// 断言用类型断言而不是直接调 *sqliteStore 的方法：快路径本来就是"实现有能力就
// 走、没有就回退"的可选能力，测试也必须按能力探测的口径来验。
type indexedAtFastPath interface {
	IndexedAt(ctx context.Context, workspaceID string) (int64, error)
}

func requireIndexedAtFastPath(t *testing.T, store Store) indexedAtFastPath {
	t.Helper()
	fast, ok := store.(indexedAtFastPath)
	if !ok {
		t.Fatal("sqliteStore 必须实现 IndexedAt 快路径：工具面档位判定只缺这一个标量")
	}
	return fast
}

// TestIndexedAtMatchesStats 钉住「快路径与状态面只有一份口径」：两者共用
// statsIndexedAtSQL，必须在各种 index_jobs 状态下逐值一致，否则档位判定与
// 状态面会各说各话（一个说新鲜、一个说陈旧）。
func TestIndexedAtMatchesStats(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	fast := requireIndexedAtFastPath(t, store)
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}

	assertSame := func(stage string) {
		t.Helper()
		stats, err := store.Stats(ctx, wsID)
		if err != nil {
			t.Fatalf("Stats(%s): %v", stage, err)
		}
		got, err := fast.IndexedAt(ctx, wsID)
		if err != nil {
			t.Fatalf("IndexedAt(%s): %v", stage, err)
		}
		if got != stats.IndexedAt {
			t.Fatalf("IndexedAt(%s) = %d, Stats.IndexedAt = %d；两条出口必须同源",
				stage, got, stats.IndexedAt)
		}
	}

	// 空工作区：两者都取 0（"尚未索引"）。索引器进程的 files.deleted_at 归零场景。
	assertSame("empty workspace")

	if _, err := store.UpsertFile(ctx, FileRecord{
		WorkspaceID: wsID, Path: "a.go", Language: "go", ContentHash: "a",
	}); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	assertSame("after single-file write (legacy MAX fallback)")

	lightID, err := store.StartIndexJob(ctx, IndexJob{WorkspaceID: wsID, Kind: IndexJobKindLight})
	if err != nil {
		t.Fatalf("StartIndexJob(light): %v", err)
	}
	if err := store.FinishIndexJob(ctx, lightID, IndexJobStatusDone, 1, 1, ""); err != nil {
		t.Fatalf("FinishIndexJob(light): %v", err)
	}
	assertSame("after successful light run")

	time.Sleep(5 * time.Millisecond)
	failID, err := store.StartIndexJob(ctx, IndexJob{WorkspaceID: wsID, Kind: IndexJobKindLight})
	if err != nil {
		t.Fatalf("StartIndexJob(failed light): %v", err)
	}
	if err := store.FinishIndexJob(ctx, failID, IndexJobStatusFailed, 1, 0, "boom"); err != nil {
		t.Fatalf("FinishIndexJob(failed light): %v", err)
	}
	assertSame("after failed light run")

	time.Sleep(5 * time.Millisecond)
	truncID, err := store.StartIndexJob(ctx, IndexJob{WorkspaceID: wsID, Kind: IndexJobKindLight})
	if err != nil {
		t.Fatalf("StartIndexJob(truncated light): %v", err)
	}
	if err := store.FinishIndexJob(ctx, truncID, IndexJobStatusDone, maxIndexFiles, maxIndexFiles, ""); err != nil {
		t.Fatalf("FinishIndexJob(truncated light): %v", err)
	}
	assertSame("after budget-truncated light run")
}

// TestStatsRefsIgnoreSoftDeletedFiles 钉住 refs 计数的语义：只统计"文件仍未被
// 软删"的引用行。
//
// statsRefsSQL 把 join 顺序从 SQLite 自选的 refs → files 改写成强制的
// files → refs（CROSS JOIN）以换取约 3 倍速度，这条测试锁住改写前后逐值等价，
// 特别是"软删文件上的引用不计入"这一条——那正是最容易在改 join 顺序时写丢的语义。
func TestStatsRefsIgnoreSoftDeletedFiles(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	liveID, err := store.UpsertFile(ctx, FileRecord{
		WorkspaceID: wsID, Path: "live.go", Language: "go", ContentHash: "live",
	})
	if err != nil {
		t.Fatalf("UpsertFile(live): %v", err)
	}
	goneID, err := store.UpsertFile(ctx, FileRecord{
		WorkspaceID: wsID, Path: "gone.go", Language: "go", ContentHash: "gone",
	})
	if err != nil {
		t.Fatalf("UpsertFile(gone): %v", err)
	}

	// RefID 由 (workspace, file, line, col, kind) 派生：同一文件内多条引用必须
	// 落在不同的 line 上，否则主键相撞。
	refOf := func(fileID string, line int, name string) Reference {
		return Reference{
			WorkspaceID: wsID, FileID: fileID, ToSymbolName: name, Kind: RefImport,
			Line: line, Col: 1, Snippet: `import "` + name + `"`, Source: SourceBuiltin,
		}
	}
	if err := store.ReplaceRefs(ctx, liveID, []Reference{refOf(liveID, 1, "A"), refOf(liveID, 2, "B")}); err != nil {
		t.Fatalf("ReplaceRefs(live): %v", err)
	}
	if err := store.ReplaceRefs(ctx, goneID, []Reference{
		refOf(goneID, 1, "C"), refOf(goneID, 2, "D"), refOf(goneID, 3, "E"),
	}); err != nil {
		t.Fatalf("ReplaceRefs(gone): %v", err)
	}

	before, err := store.Stats(ctx, wsID)
	if err != nil {
		t.Fatalf("Stats(before): %v", err)
	}
	if before.Files != 2 || before.Refs != 5 {
		t.Fatalf("Stats(before) = files %d refs %d, want 2/5", before.Files, before.Refs)
	}

	// 软删一个文件：它保留在 files/symbols/refs 表里，但读路径必须看不见。
	if _, err := store.MarkFilesDeleted(ctx, wsID, []string{"gone.go"}, time.Now()); err != nil {
		t.Fatalf("MarkFilesDeleted: %v", err)
	}
	after, err := store.Stats(ctx, wsID)
	if err != nil {
		t.Fatalf("Stats(after): %v", err)
	}
	if after.Files != 1 {
		t.Fatalf("Stats(after).Files = %d, want 1（软删文件不再计入）", after.Files)
	}
	if after.Refs != 2 {
		t.Fatalf("Stats(after).Refs = %d, want 2（软删文件上的 3 条引用必须被排除）；"+
			"join 顺序改写不得改变这一口径", after.Refs)
	}
}
