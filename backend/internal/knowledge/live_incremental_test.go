package knowledge

import (
	"context"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLiveSingleFileIncremental 是 04 §7.4「单文件增量索引 p95 < 50ms」的 live 实测入口
// （默认跳过，env 门控）。
//
// 方法：对 KNOWLEDGE_INCR_WORKSPACE 指向的**可写语料副本**先做一次全量索引，然后执行
// N 次「向 1 个文件追加一行注释 → RunIndex」，记录每次 job 的墙钟耗时与 Indexed 计数
// （每次必须恰好 1）；末尾补一次无改动的 RunIndex 作为"全量跳过"基线，用于把
// 遍历+预筛开销与单文件处理开销分开看（marginal = 探测耗时 - 跳过基线）。
//
// 用法（PowerShell）：
//
//	cd backend
//	$env:KNOWLEDGE_INCR_WORKSPACE = "$env:TEMP\knowledge_calib\repo4"
//	$env:KNOWLEDGE_INCR_PROBES = "20"
//	go test ./internal/knowledge/ -run TestLiveSingleFileIncremental -v -count=1
func TestLiveSingleFileIncremental(t *testing.T) {
	workspace := strings.TrimSpace(os.Getenv("KNOWLEDGE_INCR_WORKSPACE"))
	if workspace == "" {
		t.Skip("set KNOWLEDGE_INCR_WORKSPACE=<writable corpus copy> to measure single-file incremental latency")
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		t.Fatalf("KNOWLEDGE_INCR_WORKSPACE=%q is not a directory", workspace)
	}
	probes := 20
	if raw := strings.TrimSpace(os.Getenv("KNOWLEDGE_INCR_PROBES")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			probes = v
		}
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

	started := time.Now()
	first, err := RunIndex(ctx, store, cfg)
	if err != nil {
		t.Fatalf("full RunIndex: %v", err)
	}
	t.Logf("full:  scanned=%d indexed=%d duration=%s", first.Scanned, first.Indexed, time.Since(started))

	targets := incrementalProbeTargets(t, workspace, probes)
	durations := make([]time.Duration, 0, probes)
	for i, rel := range targets {
		path := filepath.Join(workspace, filepath.FromSlash(rel))
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatalf("append probe %d (%s): %v", i, rel, err)
		}
		if _, err := f.WriteString("\n// incremental-probe " + strconv.Itoa(i) + "\n"); err != nil {
			_ = f.Close()
			t.Fatalf("write probe %d: %v", i, err)
		}
		_ = f.Close()

		started = time.Now()
		res, err := RunIndex(ctx, store, cfg)
		elapsed := time.Since(started)
		if err != nil {
			t.Fatalf("probe %d RunIndex: %v", i, err)
		}
		if res.Indexed != 1 {
			t.Fatalf("probe %d: indexed=%d, want 1 (file=%s)", i, res.Indexed, rel)
		}
		durations = append(durations, elapsed)
		t.Logf("probe %2d file=%-58s indexed=%d duration=%s", i, rel, res.Indexed, elapsed)
	}

	started = time.Now()
	skipped, err := RunIndex(ctx, store, cfg)
	if err != nil {
		t.Fatalf("skip RunIndex: %v", err)
	}
	skipDuration := time.Since(started)

	sort.Slice(durations, func(a, b int) bool { return durations[a] < durations[b] })
	quantile := func(q float64) time.Duration {
		if len(durations) == 0 {
			return 0
		}
		idx := int(math.Ceil(q*float64(len(durations)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(durations) {
			idx = len(durations) - 1
		}
		return durations[idx]
	}
	t.Logf("skip:  scanned=%d indexed=%d duration=%s", skipped.Scanned, skipped.Indexed, skipDuration)
	t.Logf("incr:  probes=%d min=%s p50=%s p95=%s max=%s skip_baseline=%s marginal_p50=%s marginal_p95=%s",
		len(durations), durations[0], quantile(0.50), quantile(0.95), durations[len(durations)-1], skipDuration,
		quantile(0.50)-skipDuration, quantile(0.95)-skipDuration)
}

// incrementalProbeTargets 选 probes 个可追加注释的源码文件（按路径升序等距采样，
// 结果确定、可复算）。
func incrementalProbeTargets(t *testing.T, workspace string, probes int) []string {
	t.Helper()
	var all []string
	err := filepath.WalkDir(workspace, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".go", ".ts", ".js", ".mjs", ".tsx", ".jsx":
		default:
			return nil
		}
		rel, relErr := filepath.Rel(workspace, path)
		if relErr != nil {
			return nil
		}
		all = append(all, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk workspace: %v", err)
	}
	sort.Strings(all)
	if len(all) < probes {
		t.Fatalf("workspace has %d probe-able files, need %d", len(all), probes)
	}
	step := len(all) / probes
	if step < 1 {
		step = 1
	}
	picked := make([]string, 0, probes)
	for i := 0; i < probes; i++ {
		picked = append(picked, all[i*step])
	}
	return picked
}
