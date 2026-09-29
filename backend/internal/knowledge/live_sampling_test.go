package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLiveContentHashSampling 是 §5.5「content_hash 一致率（抽样 ≥200）」的 live 测量入口
// （默认跳过，env 门控）。
//
// 口径：对既有 index DB（如 Phase 1 实测产出的 4989 文件库）按 path 升序等距抽样
// ≥200 个**未软删**文件，用生产同一算法（sha256(文件全字节)，见 indexer.go inspectFile）
// 重算并与库内 content_hash 比对；missing 表示抽样时磁盘已缺失。
//
// 用法（PowerShell）：
//
//	cd backend
//	$env:KNOWLEDGE_SAMPLE_DB = "$env:TEMP\knowledge_shadow_phase1\knowledge.db"
//	$env:KNOWLEDGE_SAMPLE_WORKSPACE = (Resolve-Path ..).Path
//	go test ./internal/knowledge/ -run TestLiveContentHashSampling -v -count=1
func TestLiveContentHashSampling(t *testing.T) {
	dbPath := strings.TrimSpace(os.Getenv("KNOWLEDGE_SAMPLE_DB"))
	root := strings.TrimSpace(os.Getenv("KNOWLEDGE_SAMPLE_WORKSPACE"))
	if dbPath == "" || root == "" {
		t.Skip("set KNOWLEDGE_SAMPLE_DB and KNOWLEDGE_SAMPLE_WORKSPACE to sample content_hash consistency")
	}
	if info, err := os.Stat(dbPath); err != nil || info.IsDir() {
		t.Fatalf("KNOWLEDGE_SAMPLE_DB=%q is not a file", dbPath)
	}

	ctx := context.Background()
	store, err := OpenStore(ctx, dbPath, true)
	if err != nil {
		t.Fatalf("OpenStore(readOnly): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	wsID, ok, err := store.FindWorkspace(ctx, root)
	if err != nil {
		t.Fatalf("FindWorkspace: %v", err)
	}
	if !ok {
		t.Fatalf("workspace not registered in %s: %s", dbPath, root)
	}
	files, err := store.ListActiveFiles(ctx, wsID)
	if err != nil {
		t.Fatalf("ListActiveFiles: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("no active files recorded for %s", root)
	}

	// 等距抽样：目标 250，至少覆盖 200；路径已按升序返回，抽样结果可复算。
	const target = 250
	step := 1
	if len(files) > target {
		step = len(files) / target
		if step < 1 {
			step = 1
		}
	}

	sampled, missing, readErr, emptyHash := 0, 0, 0, 0
	// 两个队列：unchanged（DB 元数据与磁盘一致 → 哈希必须相等）与
	// changedAfterIndex（索引后又被修改 → 哈希不等是预期，不构成不一致）。
	unchanged, unchangedMatch, unchangedMismatch := 0, 0, 0
	changedAfterIndex, staleHashDiff := 0, 0
	firstUnchangedMismatch, firstStale, firstMissing := "", "", ""
	stateCounts := map[IndexState]int{}
	for i := 0; i < len(files); i += step {
		rec := files[i]
		sampled++
		stateCounts[rec.IndexState]++
		if strings.TrimSpace(rec.ContentHash) == "" {
			emptyHash++
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(rec.Path))
		content, err := os.ReadFile(abs)
		if err != nil {
			if os.IsNotExist(err) {
				missing++
				if firstMissing == "" {
					firstMissing = rec.Path
				}
			} else {
				readErr++
			}
			continue
		}
		hashEqual := ""
		sum := sha256.Sum256(content)
		hashEqual = hex.EncodeToString(sum[:])
		info, statErr := os.Stat(abs)
		metaSame := statErr == nil && info.Size() == rec.Size && info.ModTime().UnixNano() == rec.MTimeNS
		if !metaSame {
			changedAfterIndex++
			if hashEqual != rec.ContentHash {
				staleHashDiff++
				if firstStale == "" {
					firstStale = rec.Path
				}
			}
			continue
		}
		unchanged++
		if hashEqual == rec.ContentHash {
			unchangedMatch++
		} else {
			unchangedMismatch++
			if firstUnchangedMismatch == "" {
				firstUnchangedMismatch = rec.Path
			}
		}
	}

	unchangedRate := 0.0
	if unchanged > 0 {
		unchangedRate = float64(unchangedMatch) / float64(unchanged)
	}
	t.Logf("content_hash sampling: files=%d step=%d sampled=%d missing=%d read_error=%d empty_hash=%d",
		len(files), step, sampled, missing, readErr, emptyHash)
	t.Logf("consistency cohort (disk meta == db meta): n=%d match=%d mismatch=%d rate=%.4f",
		unchanged, unchangedMatch, unchangedMismatch, unchangedRate)
	t.Logf("changed after index (expected asymmetry): n=%d hash_diff=%d",
		changedAfterIndex, staleHashDiff)
	t.Logf("index_state distribution: %v", stateCounts)
	if firstUnchangedMismatch != "" {
		t.Logf("first unchanged-cohort mismatch: %s", firstUnchangedMismatch)
	}
	if firstStale != "" {
		t.Logf("first changed-after-index file: %s", firstStale)
	}
	if firstMissing != "" {
		t.Logf("first missing: %s", firstMissing)
	}
	if sampled < 200 {
		t.Fatalf("sample too small for §5.5 (≥200 required): %d", sampled)
	}
}

// TestLiveLockWaitSampling 是 §5.5「锁等待 p95」的 live 测量入口（默认跳过，env 门控）。
//
// 口径：锁等待样本来自**写路径**（execWrite → RetryLockedCtxObserved）的进程内 128 环
// （lockwait.go）。本测试在同一进程内并发运行 N 个 RunIndex 写者（共享一个 sqliteStore），
// 制造真实写竞争后读取该进程的采样摘要——这是与 knowledge.status 的 lock_wait 字段
// 完全同源的观测面。跨进程竞争时每个进程各自记录，不在本实验范围内。
//
// 用法（PowerShell）：
//
//	cd backend
//	$env:KNOWLEDGE_LOCKWAIT_LIVE = "1"
//	go test ./internal/knowledge/ -run TestLiveLockWaitSampling -v -count=1
func TestLiveLockWaitSampling(t *testing.T) {
	if strings.TrimSpace(os.Getenv("KNOWLEDGE_LOCKWAIT_LIVE")) != "1" {
		t.Skip("set KNOWLEDGE_LOCKWAIT_LIVE=1 to run the lock-wait sampling experiment")
	}

	ctx := context.Background()
	wsRoot := t.TempDir()
	const (
		fileCount = 240
		lineCount = 40
		writers   = 4
	)
	for i := 0; i < fileCount; i++ {
		path := filepath.Join(wsRoot, fmt.Sprintf("pkg%02d", i%12), fmt.Sprintf("file_%03d.go", i))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		var b strings.Builder
		b.WriteString("package sample\n\n")
		for l := 0; l < lineCount; l++ {
			fmt.Fprintf(&b, "func Fn%03d_%02d() int { return %d }\n", i, l, l)
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	dbPath := filepath.Join(t.TempDir(), "knowledge.db")
	store, err := OpenStore(ctx, dbPath, false)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := DefaultConfig().WithWorkspace(wsRoot)
	cfg.Mode = ModeShadow

	var wg sync.WaitGroup
	results := make([]IndexResult, writers)
	errs := make([]error, writers)
	started := time.Now()
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = RunIndex(ctx, store, cfg)
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(started)

	sqlite, ok := store.(*sqliteStore)
	if !ok {
		t.Fatalf("store is %T, want *sqliteStore", store)
	}
	stats := sqlite.lockWait.snapshot()
	payload, _ := json.Marshal(stats)
	t.Logf("lock_wait (same source as knowledge.status): %s", payload)
	t.Logf("concurrency: writers=%d files=%d elapsed_ms=%d", writers, fileCount, elapsed.Milliseconds())
	for w := range results {
		t.Logf("writer %d: scanned=%d indexed=%d skipped=%d deleted=%d errors=%d symbols=%d refs=%d",
			w, results[w].Scanned, results[w].Indexed, results[w].Skipped, results[w].Deleted,
			results[w].Errors, results[w].Symbols, results[w].Refs)
		if errs[w] != nil {
			t.Fatalf("writer %d failed: %v", w, errs[w])
		}
	}
	if stats.RetryFailures != 0 {
		t.Fatalf("retry failures must stay zero under bounded in-process contention: %+v", stats)
	}
}
