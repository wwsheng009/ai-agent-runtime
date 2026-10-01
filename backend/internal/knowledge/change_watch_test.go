package knowledge

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Phase 5 交付 1 第三类变更源（fsnotify 可选源）的测试：
// 空闲期（没有判定点、没有 edit hook）的外部写盘必须被即时发现并入队。

// watchLayerForTest 建一个 watch=on 的 owner Layer（mode=shadow），并做一次初始索引。
func watchLayerForTest(t *testing.T, root string) *Layer {
	t.Helper()
	ctx := context.Background()
	cfg := layerConfigForTest(root)
	cfg.Watch = CodeToolsOn
	layer, err := Open(ctx, cfg)
	require.NoError(t, err)
	require.True(t, layer.WatchStatus().Active, "watch=on 时必须真的在监听")
	t.Cleanup(func() { _ = layer.Close() })
	writeTree(t, root, "demo/a.go", demoGoSource)
	if _, err := RunIndex(ctx, layer.Store(), cfg); err != nil {
		t.Fatalf("RunIndex(initial): %v", err)
	}
	return layer
}

// waitForFileIndexed 轮询直到指定路径进入索引（有界窗口，避免依赖固定 sleep）。
func waitForFileIndexed(t *testing.T, layer *Layer, rel string, window time.Duration) bool {
	t.Helper()
	ctx := context.Background()
	wsID, err := layer.planWorkspaceID(ctx)
	require.NoError(t, err)
	deadline := time.Now().Add(window)
	for {
		records, err := layer.Store().ListActiveFiles(ctx, wsID)
		require.NoError(t, err)
		for _, record := range records {
			if normalizeRelPath(record.Path) == rel {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 空闲期的外部写盘（新建 + 修改）必须被 watcher 发现并索引。
func TestWatchSourceIndexesExternalWritesWhileIdle(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	layer := watchLayerForTest(t, root)

	// 1) 新建文件（不经过任何编辑工具/判定点）。
	writeTree(t, root, "demo/b.go", "package demo\n\nfunc Beta() int { return 2 }\n")
	require.True(t, waitForFileIndexed(t, layer, "demo/b.go", 5*time.Second),
		"watcher 必须发现空闲期新建的代码文件")

	// 2) 修改已索引文件：新符号必须进库（定向增量走同一队列）。
	writeTree(t, root, "demo/b.go", "package demo\n\nfunc Beta() int { return 2 }\n\nfunc Gamma() int { return 3 }\n")
	deadline := time.Now().Add(5 * time.Second)
	found := false
	for !found && time.Now().Before(deadline) {
		symbols, err := layer.Store().FindSymbols(ctx, SymbolQuery{Name: "Gamma", Limit: 5})
		require.NoError(t, err)
		for _, symbol := range symbols {
			if symbol.Name == "Gamma" {
				found = true
			}
		}
		if !found {
			time.Sleep(20 * time.Millisecond)
		}
	}
	require.True(t, found, "watcher 必须发现空闲期的文件修改（新符号 Gamma 进库）")
}

// 非索引口径的路径不得入队：内置忽略集（node_modules / .aicli）、非代码后缀、
// 隐藏目录——尤其是知识库自己的 .db/-wal/-shm，绝不能形成回环。
func TestWatchSourceIgnoresNonIndexablePaths(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	layer := watchLayerForTest(t, root)
	wsID, err := layer.planWorkspaceID(ctx)
	require.NoError(t, err)
	before, err := layer.Store().Stats(ctx, wsID)
	require.NoError(t, err)

	writeTree(t, root, "node_modules/pkg/x.go", "package pkg\n\nfunc X() int { return 1 }\n")
	writeTree(t, root, ".aicli/knowledge/state.go", "package state\n\nfunc S() int { return 1 }\n")
	writeTree(t, root, "demo/notes.txt", "not code\n")
	writeTree(t, root, "demo/knowledge.db-wal", "not code\n")

	// 负向断言只能靠时间窗口：给足 watcher 处理事件的时间后确认索引没变。
	time.Sleep(400 * time.Millisecond)
	after, err := layer.Store().Stats(ctx, wsID)
	require.NoError(t, err)
	require.Equal(t, before.Files, after.Files, "被忽略路径不得进入索引")
}

// 默认 off：不开 watch 时既没有监听，也不会把外部写盘即时入队。
func TestWatchSourceDisabledByDefault(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := layerConfigForTest(root) // 不设 Watch
	layer, err := Open(ctx, cfg)
	require.NoError(t, err)
	defer func() { _ = layer.Close() }()

	status := layer.WatchStatus()
	require.False(t, status.Active, "默认必须不监听")
	require.Empty(t, status.DegradedReason, "默认 off 不该往状态面写噪音")

	writeTree(t, root, "demo/a.go", demoGoSource)
	require.NoError(t, func() error { _, err := RunIndex(ctx, layer.Store(), cfg); return err }())
	writeTree(t, root, "demo/late.go", "package demo\n\nfunc Late() int { return 1 }\n")
	require.False(t, waitForFileIndexed(t, layer, "demo/late.go", 300*time.Millisecond),
		"watch=off 时不应有即时索引（由判定点校正负责）")
}

// 配额耗尽：显式降级并记录原因，而不是静默半监听。
func TestWatchSourceDegradesWhenDirCapReached(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"a", "b", "c", "d"} {
		writeTree(t, root, dir+"/x.go", demoGoSource)
	}
	cfg := layerConfigForTest(root)
	cfg.Watch = CodeToolsOn

	store, err := OpenStore(context.Background(), cfg.storePath(), false)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	queue := NewChangeQueue(cfg, store, ChangeQueueOptions{})
	defer queue.Close()

	src, err := newWatchSource(cfg, queue)
	require.NoError(t, err)
	src.maxDirs = 2 // 模拟 inotify 配额耗尽
	src.start()
	defer src.Close()

	require.LessOrEqual(t, src.Dirs(), 2, "不得超过配额继续监听")
	require.Contains(t, src.DegradedReason(), "cap reached", "必须显式记录降级原因")
}
