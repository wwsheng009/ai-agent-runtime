package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBaselineFullIndex 是 Phase 0 基线的一次性测量入口（默认跳过）。
//
// Phase 1 的验收门槛（04 §5：“首次全量索引 ≤ 实测基线，先测后定，初值 ≤ 120s；
// 单文件增量 < 50ms；DB ≤ 200MB”）必须先有实测数字，本测试就是产出它的工具，
// 因此只输出可复算的观测值，不做断言。
//
// 用法（PowerShell）：
//
//	cd backend
//	$env:KNOWLEDGE_BASELINE_REPO = (Resolve-Path ..).Path
//	go test ./internal/knowledge/ -run TestBaselineFullIndex -v -count=1
//
// 输出字段：Scanned / Indexed / Skipped / Symbols / Refs / 首次全量耗时 /
// 二次增量耗时 / DB 字节数。同一份工作区重复运行必须得到相同的 Scanned 与
// Indexed（内容哈希判增量，见 indexer.go）。
func TestBaselineFullIndex(t *testing.T) {
	workspace := strings.TrimSpace(os.Getenv("KNOWLEDGE_BASELINE_REPO"))
	if workspace == "" {
		t.Skip("set KNOWLEDGE_BASELINE_REPO=<path> to measure the full-index baseline")
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		t.Fatalf("KNOWLEDGE_BASELINE_REPO=%q is not a directory", workspace)
	}

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "knowledge.db")

	store, err := OpenStore(ctx, dbPath, false)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := DefaultConfig().WithWorkspace(workspace)
	cfg.Mode = ModeShadow

	before := snapshotFileMeta(t, cfg)

	started := time.Now()
	first, err := RunIndex(ctx, store, cfg)
	if err != nil {
		t.Fatalf("first RunIndex: %v", err)
	}
	firstDuration := time.Since(started)

	started = time.Now()
	second, err := RunIndex(ctx, store, cfg)
	if err != nil {
		t.Fatalf("second RunIndex: %v", err)
	}
	secondDuration := time.Since(started)
	after := snapshotFileMeta(t, cfg)

	workspaceID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: workspace})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	stats, err := store.Stats(ctx, workspaceID)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	var dbBytes int64
	if info, err := os.Stat(dbPath); err == nil {
		dbBytes = info.Size()
	}

	t.Logf("workspace=%s", workspace)
	t.Logf("first:  scanned=%d indexed=%d skipped=%d symbols=%d refs=%d errors=%d duration=%s",
		first.Scanned, first.Indexed, first.Skipped, first.Symbols, first.Refs, first.Errors, firstDuration)
	t.Logf("second: scanned=%d indexed=%d skipped=%d duration=%s",
		second.Scanned, second.Indexed, second.Skipped, secondDuration)
	t.Logf("stats:  files=%d symbols=%d refs=%d", stats.Files, stats.Symbols, stats.Refs)
	t.Logf("db:     bytes=%d (%.1f MiB)", dbBytes, float64(dbBytes)/(1024*1024))

	// 04 §4.4 的歧义规则：stable_key 冲突必须留下 adapter_conflict 事件。
	// result.Symbols 与 stats.Symbols 的差额就是被合并的重复身份数。
	sqlite, ok := store.(*sqliteStore)
	if ok {
		var conflicts int
		if err := sqlite.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM invalidation_events WHERE workspace_id = ? AND reason = ?`,
			workspaceID, InvalidationReasonAdapterConflict).Scan(&conflicts); err != nil {
			t.Fatalf("count adapter_conflict: %v", err)
		}
		t.Logf("conflict: adapter_conflict_events=%d merged_identities=%d",
			conflicts, int64(first.Symbols)-stats.Symbols)
	}

	if second.Indexed != 0 {
		for path := range diffFileMeta(before, after) {
			t.Logf("运行期间被改动的文件（解释 indexed>0）：%s", path)
		}
		t.Errorf("第二次索引必须全部跳过（content_hash 未变），实际 indexed=%d", second.Indexed)
	}
	if second.Scanned != first.Scanned {
		t.Errorf("两次遍历的候选文件数必须一致：first=%d second=%d", first.Scanned, second.Scanned)
	}
}

// fileMeta 是判增量用的最小元数据（与 inspectFile 的廉价预筛一致）。
type fileMeta struct {
	size    int64
	mtimeNS int64
}

// snapshotFileMeta 记录候选文件的 size+mtime，用于解释"两次索引之间文件被改动"。
func snapshotFileMeta(t *testing.T, cfg Config) map[string]fileMeta {
	t.Helper()
	files, _, err := collectIndexableFiles(cfg)
	if err != nil {
		t.Fatalf("collectIndexableFiles: %v", err)
	}
	out := make(map[string]fileMeta, len(files))
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		out[path] = fileMeta{size: info.Size(), mtimeNS: info.ModTime().UnixNano()}
	}
	return out
}

// diffFileMeta 返回两次快照之间有差异的路径。
func diffFileMeta(before, after map[string]fileMeta) map[string]struct{} {
	changed := map[string]struct{}{}
	for path, meta := range after {
		if old, ok := before[path]; !ok || old != meta {
			changed[path] = struct{}{}
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			changed[path] = struct{}{}
		}
	}
	return changed
}
