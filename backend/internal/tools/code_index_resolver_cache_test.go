package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	kitools "github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
)

// 背景（2026-10-03 实测）：工具面每轮会对每个工具问一次来源，解析器每次解析
// 都要重跑知识层取数。原实现走 Stats（四个聚合，355 MiB 库单次 ~2.5s）并且
// 每次都重新物化全部文件映射。本文件的测试锁住两条修复不变量：
//
//  1. 热路径只走 IndexedAt 快路径，Stats 一次都不许被调用（缺失该能力时才回退）；
//  2. 快照视图（filePaths/FileStamps/IndexedAt）在 TTL 内按库文件指纹复用。

// snapshotFakeStore 是只读 store 的确定性替身：内嵌 Store 接口让未覆写的方法
// 保持未实现（一旦被调用就 panic，这正是我们要的强断言）。
type snapshotFakeStore struct {
	knowledge.Store

	mu             sync.Mutex
	wsID           string
	indexedAt      int64
	files          []knowledge.FileRecord
	findCalls      int
	listCalls      int
	statsCalls     int
	indexedAtCalls int
}

func (s *snapshotFakeStore) Close() error { return nil }

func (s *snapshotFakeStore) FindWorkspace(context.Context, string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.findCalls++
	return s.wsID, true, nil
}

func (s *snapshotFakeStore) ListActiveFiles(context.Context, string) ([]knowledge.FileRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCalls++
	return append([]knowledge.FileRecord(nil), s.files...), nil
}

func (s *snapshotFakeStore) Stats(context.Context, string) (knowledge.Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statsCalls++
	return knowledge.Stats{}, errors.New("Stats 不得出现在工具面热路径上")
}

func (s *snapshotFakeStore) IndexedAt(context.Context, string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.indexedAtCalls++
	return s.indexedAt, nil
}

func (s *snapshotFakeStore) counts() (find, list, stats, indexedAt int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findCalls, s.listCalls, s.statsCalls, s.indexedAtCalls
}

func (s *snapshotFakeStore) setIndexedAt(value int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.indexedAt = value
}

// statsOnlyFakeStore 刻意**不实现** IndexedAt：用来钉住"能力缺失时回退 Stats"。
type statsOnlyFakeStore struct {
	knowledge.Store

	mu         sync.Mutex
	wsID       string
	indexedAt  int64
	statsCalls int
}

func (s *statsOnlyFakeStore) Close() error { return nil }

func (s *statsOnlyFakeStore) FindWorkspace(context.Context, string) (string, bool, error) {
	return s.wsID, true, nil
}

func (s *statsOnlyFakeStore) ListActiveFiles(context.Context, string) ([]knowledge.FileRecord, error) {
	return nil, nil
}

func (s *statsOnlyFakeStore) Stats(context.Context, string) (knowledge.Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statsCalls++
	return knowledge.Stats{IndexedAt: s.indexedAt}, nil
}

// injectCodeIndexStore 把替身塞进进程级只读句柄缓存，并返回解析器与库路径。
//
// 走缓存注入而不是构造新解析器，是因为解析器自己按 StorePathFor 打开库；
// 这里用一个内容无关的占位文件提供 size/mtime 指纹，让 open 命中注入项。
func injectCodeIndexStore(t *testing.T, store knowledge.Store) (context.Context, func(context.Context) (*kitools.CodeIndexHandle, bool), string) {
	t.Helper()
	root := t.TempDir()
	cfg := knowledge.DefaultConfig().WithWorkspace(root)
	cfg.Mode = knowledge.ModeOn
	path := knowledge.StorePathFor(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir store dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("stub-knowledge-db"), 0o644); err != nil {
		t.Fatalf("write stub db: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat stub db: %v", err)
	}

	codeIndexCacheGlobal.mu.Lock()
	codeIndexCacheGlobal.entries[path] = &codeIndexEntry{
		store:   store,
		size:    info.Size(),
		modTime: info.ModTime(),
	}
	codeIndexCacheGlobal.mu.Unlock()
	codeIndexSnapshotsGlobal.reset()
	t.Cleanup(func() {
		codeIndexCacheGlobal.closeAll()
		codeIndexSnapshotsGlobal.reset()
	})

	return context.Background(), newCodeIndexResolver(cfg, root), path
}

// 回归（性能）：解析一次只许走 IndexedAt 快路径，Stats 一次都不许被调用。
//
// 原实现在这里调 Stats：355 MiB 库单次 ~2.5s，而解析器在一次 think 里会被调用
// 几十次（每个工具一次），于是"网络很快但 TUI 迟迟没输出"。
func TestCodeIndexResolverUsesIndexedAtFastPath(t *testing.T) {
	indexedAt := time.Now().Add(-90 * time.Second).UnixMilli()
	fake := &snapshotFakeStore{
		wsID:      "w1",
		indexedAt: indexedAt,
		files: []knowledge.FileRecord{
			{ID: "f1", Path: "a.go", Size: 11, MTimeNS: 22, ContentHash: "h1"},
		},
	}
	ctx, resolver, _ := injectCodeIndexStore(t, fake)

	handle, ok := resolver(ctx)
	if !ok || handle == nil {
		t.Fatalf("handle = %+v ok=%v, want available", handle, ok)
	}
	if handle.WorkspaceID != "w1" {
		t.Fatalf("workspace = %q, want w1", handle.WorkspaceID)
	}
	if handle.SnapshotTS != indexedAt/1000 {
		t.Fatalf("SnapshotTS = %d, want %d（必须来自快路径的 IndexedAt）", handle.SnapshotTS, indexedAt/1000)
	}
	if handle.StalenessSeconds < 89 || handle.StalenessSeconds > 91 {
		t.Fatalf("StalenessSeconds = %d, want ~90（按调用时刻从 IndexedAt 推算）", handle.StalenessSeconds)
	}
	if handle.FilePaths["f1"] != "a.go" || handle.FileStamps["f1"].ContentHash != "h1" {
		t.Fatalf("文件视图未装配：paths=%v stamps=%v", handle.FilePaths, handle.FileStamps)
	}

	find, list, stats, indexedAtCalls := fake.counts()
	if stats != 0 {
		t.Fatalf("Stats 被调用了 %d 次：工具面热路径只缺 IndexedAt，跑行数聚合就是 2.5s 级卡顿", stats)
	}
	if indexedAtCalls != 1 || find != 1 || list != 1 {
		t.Fatalf("首次解析计数 = find %d / list %d / IndexedAt %d, want 1/1/1",
			find, list, indexedAtCalls)
	}
}

// 回归（性能）：TTL 内的重复解析必须复用快照视图，不得重复查库/物化映射。
func TestCodeIndexResolverCoalescesSnapshotAcrossResolutions(t *testing.T) {
	fake := &snapshotFakeStore{
		wsID:      "w1",
		indexedAt: time.Now().UnixMilli(),
		files: []knowledge.FileRecord{
			{ID: "f1", Path: "a.go", Size: 1, ContentHash: "h1"},
			{ID: "f2", Path: "b.go", Size: 2, ContentHash: "h2"},
		},
	}
	ctx, resolver, _ := injectCodeIndexStore(t, fake)

	for i := 0; i < 5; i++ {
		if _, ok := resolver(ctx); !ok {
			t.Fatalf("第 %d 次解析不可用", i+1)
		}
	}

	find, list, _, indexedAtCalls := fake.counts()
	if find != 1 || list != 1 || indexedAtCalls != 1 {
		t.Fatalf("5 次解析的取数计数 = find %d / list %d / IndexedAt %d, want 1/1/1（快照应被复用）",
			find, list, indexedAtCalls)
	}
}

// 能力缺失时必须原样回退到 Stats（口径不变，只是更贵），不能变成不可用。
func TestCodeIndexResolverFallsBackToStatsWithoutFastPath(t *testing.T) {
	indexedAt := time.Now().Add(-30 * time.Second).UnixMilli()
	fake := &statsOnlyFakeStore{wsID: "w1", indexedAt: indexedAt}
	ctx, resolver, _ := injectCodeIndexStore(t, fake)

	handle, ok := resolver(ctx)
	if !ok || handle == nil {
		t.Fatalf("handle = %+v ok=%v, want available（缺 IndexedAt 时应回退 Stats）", handle, ok)
	}
	if handle.SnapshotTS != indexedAt/1000 {
		t.Fatalf("SnapshotTS = %d, want %d", handle.SnapshotTS, indexedAt/1000)
	}
	fake.mu.Lock()
	statsCalls := fake.statsCalls
	fake.mu.Unlock()
	if statsCalls != 1 {
		t.Fatalf("Stats 调用次数 = %d, want 1（回退路径）", statsCalls)
	}
}

// 快照缓存的两个失效条件：库文件指纹变化、TTL 到期。
func TestCodeIndexSnapshotCacheRefreshesOnTTLAndFileChange(t *testing.T) {
	codeIndexSnapshotsGlobal.reset()
	t.Cleanup(codeIndexSnapshotsGlobal.reset)

	dbPath := filepath.Join(t.TempDir(), "knowledge.db")
	if err := os.WriteFile(dbPath, []byte("v1"), 0o644); err != nil {
		t.Fatalf("write db: %v", err)
	}

	builds := 0
	build := func(context.Context) (*codeIndexSnapshot, error) {
		builds++
		return &codeIndexSnapshot{workspaceID: "w1", indexedAt: int64(builds)}, nil
	}
	ctx := context.Background()

	first, err := codeIndexSnapshotsGlobal.load(ctx, dbPath, "/ws", build)
	if err != nil {
		t.Fatalf("load(first): %v", err)
	}
	if builds != 1 {
		t.Fatalf("builds = %d, want 1", builds)
	}
	again, err := codeIndexSnapshotsGlobal.load(ctx, dbPath, "/ws", build)
	if err != nil {
		t.Fatalf("load(again): %v", err)
	}
	if builds != 1 || again != first {
		t.Fatalf("TTL 内且文件未变时必须命中同一快照：builds=%d same=%v", builds, again == first)
	}

	// 库文件被重建/替换（size 或 mtime 变化）→ 立即失效，不等 TTL。
	if err := os.WriteFile(dbPath, []byte("v2-longer-content"), 0o644); err != nil {
		t.Fatalf("rewrite db: %v", err)
	}
	if _, err := codeIndexSnapshotsGlobal.load(ctx, dbPath, "/ws", build); err != nil {
		t.Fatalf("load(after file change): %v", err)
	}
	if builds != 2 {
		t.Fatalf("库文件变化后 builds = %d, want 2", builds)
	}

	// TTL 到期 → 重新构建。
	previousTTL := codeIndexSnapshotTTL
	codeIndexSnapshotTTL = 0
	t.Cleanup(func() { codeIndexSnapshotTTL = previousTTL })
	if _, err := codeIndexSnapshotsGlobal.load(ctx, dbPath, "/ws", build); err != nil {
		t.Fatalf("load(after ttl expiry): %v", err)
	}
	if builds != 3 {
		t.Fatalf("TTL 到期后 builds = %d, want 3", builds)
	}
}
