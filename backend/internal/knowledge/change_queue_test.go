package knowledge

import (
	"context"
	"testing"
	"time"
)

// Phase 5 交付 1/2 的队列语义测试：去重、debounce 后台执行、并发标记、
// 关闭幂等、nil-safe，以及 Activation.MarkChanged（edit hook 入口）的门控。

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func TestChangeQueueFlushIndexesMarkedFiles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/a.go", indexPathsSourceA)

	store := newTestStore(t)
	cfg := indexPathsConfig(root)
	if _, err := RunIndex(ctx, store, cfg); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}

	queue := NewChangeQueue(cfg, store, ChangeQueueOptions{Debounce: 30 * time.Millisecond})
	t.Cleanup(queue.Close)
	writeTree(t, root, "demo/a.go", indexPathsSourceAV2)
	queue.Mark("demo/a.go")

	result, err := queue.Flush(ctx)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if result.Scanned != 1 || result.Indexed != 1 {
		t.Fatalf("Flush 结果不符: %+v", result)
	}
	if queue.Pending() != 0 {
		t.Fatalf("Flush 后待办应为空，got %d", queue.Pending())
	}
	if last := queue.LastResult(); last.Indexed != 1 {
		t.Fatalf("LastResult 未记录: %+v", last)
	}
	if err := queue.LastError(); err != nil {
		t.Fatalf("LastError = %v, want nil", err)
	}
	if syms, err := store.FindSymbols(ctx, SymbolQuery{Name: "TargetRenamed", Exact: true, Limit: 50}); err != nil || len(syms) == 0 {
		t.Fatalf("标记文件必须已增量入库: %+v err=%v", syms, err)
	}
}

func TestChangeQueueCoalescesAndDedupes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/a.go", indexPathsSourceA)
	writeTree(t, root, "demo/b.go", indexPathsSourceB)

	store := newTestStore(t)
	cfg := indexPathsConfig(root)
	if _, err := RunIndex(ctx, store, cfg); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}

	queue := NewChangeQueue(cfg, store, ChangeQueueOptions{Debounce: 30 * time.Millisecond})
	t.Cleanup(queue.Close)
	// 两个文件都真的改了内容（队列只处理"标记"，是否重解析由 content_hash 决定）。
	writeTree(t, root, "demo/a.go", indexPathsSourceAV2)
	writeTree(t, root, "demo/b.go", indexPathsSourceBV2)
	queue.Mark("demo/a.go", "demo/a.go", "demo/a.go")
	queue.Mark("demo/b.go")

	result, err := queue.Flush(ctx)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if result.Scanned != 2 || result.Indexed != 2 {
		t.Fatalf("去重后应处理 2 个文件: %+v", result)
	}
}

// debounce 之后由后台 worker 自动执行——编辑路径不需要（也不应该）显式 Flush。
func TestChangeQueueDebounceRunsInBackground(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/a.go", indexPathsSourceA)

	store := newTestStore(t)
	cfg := indexPathsConfig(root)
	if _, err := RunIndex(ctx, store, cfg); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}

	queue := NewChangeQueue(cfg, store, ChangeQueueOptions{Debounce: 20 * time.Millisecond})
	t.Cleanup(queue.Close)
	writeTree(t, root, "demo/a.go", indexPathsSourceAV2)
	queue.Mark("demo/a.go")

	if !waitForCondition(t, 5*time.Second, func() bool {
		return queue.Pending() == 0 && queue.LastResult().Indexed == 1
	}) {
		t.Fatalf("后台 worker 未在窗口内完成: pending=%d last=%+v err=%v",
			queue.Pending(), queue.LastResult(), queue.LastError())
	}
	if syms, err := store.FindSymbols(ctx, SymbolQuery{Name: "TargetRenamed", Exact: true, Limit: 50}); err != nil || len(syms) == 0 {
		t.Fatalf("后台增量未入库: %+v err=%v", syms, err)
	}
}

// 并发标记（多个编辑源同时写）不丢标记、不并发写库（-race 下也验证）。
func TestChangeQueueConcurrentMarks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/a.go", indexPathsSourceA)
	writeTree(t, root, "demo/b.go", indexPathsSourceB)
	writeTree(t, root, "demo/c.go", indexPathsSourceC)

	store := newTestStore(t)
	cfg := indexPathsConfig(root)
	if _, err := RunIndex(ctx, store, cfg); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}

	queue := NewChangeQueue(cfg, store, ChangeQueueOptions{Debounce: 10 * time.Millisecond})
	t.Cleanup(queue.Close)

	writeTree(t, root, "demo/a.go", indexPathsSourceAV2)
	writeTree(t, root, "demo/b.go", indexPathsSourceBV2)
	writeTree(t, root, "demo/c.go", indexPathsSourceD)

	paths := []string{"demo/a.go", "demo/b.go", "demo/c.go"}
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(worker int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 5; j++ {
				queue.Mark(paths[(worker+j)%len(paths)])
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}

	result, err := queue.Flush(ctx)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if result.Scanned != 3 || result.Indexed != 3 {
		t.Fatalf("并发标记后应处理全部 3 个文件: %+v", result)
	}
	if queue.Pending() != 0 {
		t.Fatalf("待办应为空，got %d", queue.Pending())
	}
}

func TestChangeQueueCloseIsIdempotentAndStopsMarking(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, "demo/a.go", indexPathsSourceA)

	store := newTestStore(t)
	queue := NewChangeQueue(indexPathsConfig(root), store, ChangeQueueOptions{Debounce: 10 * time.Millisecond})
	queue.Close()
	queue.Close() // 幂等

	queue.Mark("demo/a.go")
	if queue.Pending() != 0 {
		t.Fatalf("关闭后 Mark 必须是 no-op，pending=%d", queue.Pending())
	}
}

func TestChangeQueueNilStoreIsNoop(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	queue := NewChangeQueue(DefaultConfig(), nil, ChangeQueueOptions{Debounce: 10 * time.Millisecond})
	t.Cleanup(queue.Close)

	queue.Mark("demo/a.go")
	if _, err := queue.Flush(ctx); err != nil {
		t.Fatalf("nil store 时 Flush 不应报错: %v", err)
	}
	if queue.LastResult().Indexed != 0 {
		t.Fatalf("nil store 不应产生索引结果: %+v", queue.LastResult())
	}
}

// edit hook 入口：owner + shadow 的 Activation 会把标记送进队列并完成增量。
// （reader / off 的 no-op 与 Recorder 同一条门控代码路径，见 activation 既有测试。）
func TestActivationMarkChangedFeedsQueue(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/a.go", indexPathsSourceA)

	cfg := DefaultConfig().WithWorkspace(root)
	cfg.Mode = ModeShadow
	act, err := Activate(ctx, cfg, root, ActivationOptions{SkipInitialIndex: true})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	t.Cleanup(func() { _ = act.Close() })

	if act.ChangeQueue() == nil {
		t.Fatal("owner + shadow 的 Activation 必须提供变更队列")
	}
	if _, err := RunIndex(ctx, act.Layer().Store(), cfg); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}

	writeTree(t, root, "demo/a.go", indexPathsSourceAV2)
	act.MarkChanged("demo/a.go")

	if !waitForCondition(t, 5*time.Second, func() bool {
		return act.ChangeQueue().LastResult().Indexed == 1
	}) {
		t.Fatalf("MarkChanged 未触发增量: last=%+v err=%v",
			act.ChangeQueue().LastResult(), act.ChangeQueue().LastError())
	}

	// nil-safe：off 会话（Activation 为 nil）调用它是 no-op。
	var off *Activation
	off.MarkChanged("demo/a.go")
	if off.ChangeQueue() != nil {
		t.Fatal("nil Activation 的 ChangeQueue 必须是 nil")
	}
}
