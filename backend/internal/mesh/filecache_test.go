package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// resetMeshFileCaches 清空解析缓存，用于「冷读」对照与测试隔离。
func resetMeshFileCaches() {
	nodeLivenessCache.Range(func(key, _ interface{}) bool {
		nodeLivenessCache.Delete(key)
		return true
	})
	nodeFileCache.Range(func(key, _ interface{}) bool {
		nodeFileCache.Delete(key)
		return true
	})
	bindingFileCache.Range(func(key, _ interface{}) bool {
		bindingFileCache.Delete(key)
		return true
	})
	bindingViewCache.Range(func(key, _ interface{}) bool {
		bindingViewCache.Delete(key)
		return true
	})
}

// TestReadNodeFileCacheInvalidatesOnRewrite 锁定缓存失效语义：
// 同长度改写只能靠 mtime 区分，必须重新读盘（不能返回旧内容）。
func TestReadNodeFileCacheInvalidatesOnRewrite(t *testing.T) {
	resetMeshFileCaches()
	dir := t.TempDir()
	path := filepath.Join(dir, "node.json")

	write := func(nodeID string) {
		record := NodeRecord{SchemaVersion: SchemaVersion, NodeID: nodeID, PID: 42}
		data, err := json.Marshal(record)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, data, 0o644))
	}

	write("node-a")
	first := ReadNodeFile(path)
	require.NoError(t, first.Err)
	require.Equal(t, "node-a", first.Record.NodeID)

	// 同长度改写：size 不变，必须依赖 mtime。显式推进 mtime 以规避
	// 不同文件系统的粒度差异，让断言确定。
	write("node-b")
	future := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(path, future, future))

	second := ReadNodeFile(path)
	require.NoError(t, second.Err)
	require.Equal(t, "node-b", second.Record.NodeID, "改写后必须重新读盘，不能返回缓存")
}

// TestReadNodeFileCacheMissOnDelete 锁定删除语义：stat 失败时不走缓存，
// 必须保留原有的错误结果。
func TestReadNodeFileCacheMissOnDelete(t *testing.T) {
	resetMeshFileCaches()
	dir := t.TempDir()
	path := filepath.Join(dir, "node.json")
	record := NodeRecord{SchemaVersion: SchemaVersion, NodeID: "node-a", PID: 42}
	data, err := json.Marshal(record)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))

	require.NoError(t, ReadNodeFile(path).Err)
	require.NoError(t, os.Remove(path))

	after := ReadNodeFile(path)
	require.Error(t, after.Err, "文件删除后必须返回错误，不能返回缓存内容")
	require.Nil(t, after.Record)
}

// TestLoadBindingCacheInvalidatesOnRewrite 与节点文件同构。
func TestLoadBindingCacheInvalidatesOnRewrite(t *testing.T) {
	resetMeshFileCaches()
	root := t.TempDir()
	bindings := filepath.Join(root, "bindings")
	require.NoError(t, os.MkdirAll(bindings, 0o755))
	paths := Paths{Root: root, Nodes: filepath.Join(root, "nodes"), Bindings: bindings, Source: "test"}

	write := func(port int) {
		binding := SessionBinding{
			SchemaVersion: SchemaVersion,
			SessionID:     "session-1",
			Preferred:     &BindingAddr{Host: "127.0.0.1", Port: port},
		}
		data, err := json.Marshal(binding)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(paths.BindingPath("session-1"), data, 0o644))
	}

	write(60001)
	first, ok := LoadBinding(paths, "session-1")
	require.True(t, ok)
	require.Equal(t, 60001, first.Preferred.Port)

	write(60002)
	future := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(paths.BindingPath("session-1"), future, future))

	second, ok := LoadBinding(paths, "session-1")
	require.True(t, ok)
	require.Equal(t, 60002, second.Preferred.Port, "改写后必须重新读盘")
}

func TestNodeProcessAliveCachesDeadResultAndInvalidates(t *testing.T) {
	resetMeshFileCaches()
	previous := processAliveProbe
	t.Cleanup(func() { processAliveProbe = previous })
	calls := 0
	processAliveProbe = func(int) bool {
		calls++
		return false
	}

	if nodeProcessAlive("node-dead", 4242) {
		t.Fatal("fake dead pid reported alive")
	}
	if nodeProcessAlive("node-dead", 4242) {
		t.Fatal("fake dead pid reported alive")
	}
	if calls != 1 {
		t.Fatalf("dead liveness probe calls=%d, want 1 (cached)", calls)
	}
	invalidateNodeProcessLiveness("node-dead")
	if nodeProcessAlive("node-dead", 4242) {
		t.Fatal("fake dead pid reported alive")
	}
	if calls != 2 {
		t.Fatalf("invalidated liveness must re-probe: calls=%d, want 2", calls)
	}
}

func TestListNodeFilesSkipsStatForKnownDeadNode(t *testing.T) {
	resetMeshFileCaches()
	previousProbe := processAliveProbe
	previousStat := fileStat
	t.Cleanup(func() {
		processAliveProbe = previousProbe
		fileStat = previousStat
	})
	processAliveProbe = func(int) bool { return false }

	paths := testPaths(t)
	now := NowUTC()
	writeViewNode(t, paths, viewNodeRecord("node-dead", 4242, now, "", ""))
	// 第一次 BuildView 读取并缓存节点文件，同时把 PID 判定为 dead。
	view := BuildView(paths, ViewOptions{Now: now})
	if len(view.Nodes) != 1 || view.Nodes[0].State != NodeStateStale {
		t.Fatalf("first view = %+v, want one stale node", view.Nodes)
	}

	statCalls := 0
	fileStat = func(path string) (os.FileInfo, error) {
		statCalls++
		return os.Stat(path)
	}
	files := ListNodeFiles(paths)
	if len(files) != 1 || files[0].Record == nil || files[0].Record.NodeID != "node-dead" {
		t.Fatalf("fast list = %+v, want cached node-dead", files)
	}
	if statCalls != 0 {
		t.Fatalf("known-dead node must skip Stat, got %d calls", statCalls)
	}
}

// seedMeshBenchDir 构造 nodes×1 的 mesh 目录（每个节点带 binding）。
func seedMeshBenchDir(tb testing.TB, nodeCount int) Paths {
	tb.Helper()
	root := tb.TempDir()
	nodesDir := filepath.Join(root, "nodes")
	bindingsDir := filepath.Join(root, "bindings")
	require.NoError(tb, os.MkdirAll(nodesDir, 0o755))
	require.NoError(tb, os.MkdirAll(bindingsDir, 0o755))

	paths := Paths{Root: root, Nodes: nodesDir, Bindings: bindingsDir, Source: "bench"}
	for index := 0; index < nodeCount; index++ {
		sessionID := fmt.Sprintf("session-%03d", index)
		record := NodeRecord{
			SchemaVersion: SchemaVersion,
			NodeID:        fmt.Sprintf("node-%03d", index),
			PID:           1000 + index,
			Kind:          "chat",
			Session:       &SessionInfo{ID: sessionID, Title: "bench"},
			Liveness:      LivenessInfo{HeartbeatAt: NowUTC()},
		}
		data, err := json.Marshal(record)
		require.NoError(tb, err)
		require.NoError(tb, os.WriteFile(filepath.Join(nodesDir, fmt.Sprintf("node-%03d.json", index)), data, 0o644))

		binding := SessionBinding{
			SchemaVersion: SchemaVersion,
			SessionID:     sessionID,
			Preferred:     &BindingAddr{Host: "127.0.0.1", Port: 60000 + index},
		}
		bindingData, err := json.Marshal(binding)
		require.NoError(tb, err)
		require.NoError(tb, os.WriteFile(filepath.Join(bindingsDir, sessionID+".json"), bindingData, 0o644))
	}
	return paths
}

// BenchmarkBuildViewColdCache 近似修复前的行为：每次调用都重新读盘 + 反序列化。
func BenchmarkBuildViewColdCache(b *testing.B) {
	paths := seedMeshBenchDir(b, 32)
	opts := ViewOptions{SelfNodeID: "node-000"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resetMeshFileCaches()
		_ = BuildView(paths, opts)
	}
}

// BenchmarkBuildViewWarmCache 是修复后的稳态：文件未变化时只做 stat。
func BenchmarkBuildViewWarmCache(b *testing.B) {
	paths := seedMeshBenchDir(b, 32)
	opts := ViewOptions{SelfNodeID: "node-000"}
	_ = BuildView(paths, opts) // 预热
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildView(paths, opts)
	}
}

// BenchmarkBuildViewWarmCacheProductionScale 按生产规模（约 900 节点）度量
// 稳态开销。仓库原有的 BenchmarkBuildViewWarmCache 固定 32 节点，无法反映
// 真实目录：实测 ~/.aicli/mesh/nodes 下有 912 个节点文件、bindings 下有
// 1009 个文件，且心跳每几秒重写一次。
//
// 这条 benchmark 回答的是「N 个节点 × 每秒若干次 BuildView」的系数问题：
// 缓存命中后单次调用仍需对每个节点文件执行一次 os.Stat（Windows 上是
// GetFileAttributesEx 系统调用），因此稳态开销随节点数线性增长。
func BenchmarkBuildViewWarmCacheProductionScale(b *testing.B) {
	paths := seedMeshBenchDir(b, 900)
	opts := ViewOptions{SelfNodeID: "node-000"}
	_ = BuildView(paths, opts) // 预热
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildView(paths, opts)
	}
}

// TestListNodeFilesUsesDirEntryMetadata 锁定本次优化的核心契约：
// 节点侧读取必须复用 os.ReadDir 的 DirEntry，而不是对每个文件再调一次
// os.Stat。
//
// 依据：Windows 上 os.Stat 落到 GetFileAttributesEx 系统调用，900 节点目录
// 实测 118.3 ms/次；DirEntry.Info() 复用目录枚举时内核已返回的元数据，仅
// 1.5 ms/次。CPU profile 显示稳态下 GetFileAttributesEx 占单进程 CPU 的
// 50.9%，是每个心跳周期都要付的固定成本。
//
// 注意：这里拦截 fileStat（os.Stat 的内部接缝）而非 DirEntry.Info()，正是
// 因为优化后存活节点不应再走 os.Stat。
func TestListNodeFilesUsesDirEntryMetadata(t *testing.T) {
	resetMeshFileCaches()
	previousProbe := processAliveProbe
	previousStat := fileStat
	t.Cleanup(func() {
		processAliveProbe = previousProbe
		fileStat = previousStat
	})
	// 让节点被判定为存活（否则会命中 dead 快速路径，走不到 Stat 分支）。
	processAliveProbe = func(int) bool { return true }

	paths := testPaths(t)
	now := NowUTC()
	for _, id := range []string{"node-live-a", "node-live-b", "node-live-c"} {
		writeViewNode(t, paths, viewNodeRecord(id, 1001, now, "", ""))
	}
	// 预热：建立节点缓存。
	_ = BuildView(paths, ViewOptions{Now: now})

	statCalls := 0
	fileStat = func(path string) (os.FileInfo, error) {
		statCalls++
		return os.Stat(path)
	}
	files := ListNodeFiles(paths)
	if len(files) != 3 {
		t.Fatalf("ListNodeFiles = %d files, want 3", len(files))
	}
	for _, f := range files {
		if f.Record == nil {
			t.Fatalf("node %s lost its record", f.Path)
		}
	}
	if statCalls != 0 {
		t.Fatalf("ListNodeFiles must reuse DirEntry metadata, got %d os.Stat calls", statCalls)
	}
}
