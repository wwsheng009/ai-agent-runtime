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
	nodeFileCache.Range(func(key, _ interface{}) bool {
		nodeFileCache.Delete(key)
		return true
	})
	bindingFileCache.Range(func(key, _ interface{}) bool {
		bindingFileCache.Delete(key)
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
