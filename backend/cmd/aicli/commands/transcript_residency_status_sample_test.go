package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// transcript_residency_status_sample_test.go —— T8b-5 证据采样（只在
// AICLI_T8B5_OUT 设置时落盘，普通测试运行零副作用）。
//
// 场景：4,096 cells 的 bridge scene + 真实 sqlite 写穿 store + §15.1 默认
// keepLast（512）与批量（256），反复触发裁剪后采集 §15.2/§15.3 快照。
func TestResidencyStatusSampleEvidence(t *testing.T) {
	out := os.Getenv("AICLI_T8B5_OUT")
	if out == "" {
		t.Skip("AICLI_T8B5_OUT 未设置：跳过证据采样")
	}
	dir := t.TempDir()
	resetResidencyRuntimeForTest(t)
	cfg := &runtimecfg.RuntimeConfig{}
	cfg.UI.Residency.Enabled = boolPtr(true)
	applyTranscriptResidencyConfig(cfg, dir)

	runtime := currentTranscriptResidencyRuntime()
	if runtime == nil || runtime.Store == nil {
		t.Fatalf("residency 运行时/store 未就绪：%+v", runtime)
	}
	keep := runtime.Options.MaxResidentCells
	if keep <= 0 {
		keep = 512
	}

	const total = 4096
	bridge := newSceneTrimTestBridge(t, total)
	bridge.SetSceneTrimPolicy(&bridgeSceneTrimPolicy{
		Enabled: true, KeepLastCells: keep, TrimBatch: 256,
		RowsPerGen: 1, Generation: 7, SessionID: "t8b5-sample", Store: runtime.Store,
	})

	start := time.Now()
	for rounds := 0; rounds < 64 && sceneTrimLen(bridge) > keep; rounds++ {
		bridge.applyChangeSet(&encoding.ChangeSet{})
	}
	elapsed := time.Since(start)
	if size := sceneTrimLen(bridge); size != keep {
		t.Fatalf("裁剪未收敛：size=%d keep=%d", size, keep)
	}

	session := &ChatSession{
		RuntimeEventBridge: bridge,
		RuntimeSession:     &runtimechat.Session{ID: "t8b5-sample"},
	}
	stats := chatResidencyStatsForSession(session, nil, nil)
	if stats.Cold == nil || stats.Cold.Cells != total-keep {
		t.Fatalf("cold 分区不符：%+v", stats.Cold)
	}
	if stats.BodyStore == nil || stats.BodyStore.Puts != uint64(total-keep) {
		t.Fatalf("store 自报 puts 不符：%+v", stats.BodyStore)
	}

	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	payload := map[string]any{
		"task":            "T8b-5 residency status payload sample",
		"date":            time.Now().Format("2006-01-02"),
		"total_cells":     total,
		"keep_last_cells": keep,
		"trim_rounds_ms":  elapsed.Milliseconds(),
		"residency":       stats,
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(out, "status-sample.json"), raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("T8b-5 evidence: cold.cells=%d cold.rows=%d cold.bytes=%d resident.cells=%d resident.rows=%d resident.bytes=%d "+
		"store.puts=%d evictions.total=%d trim_ms=%d",
		stats.Cold.Cells, stats.Cold.DeliveredRows, stats.Cold.Bytes,
		stats.Resident.Cells, stats.Resident.Rows, stats.Resident.Bytes,
		stats.BodyStore.Puts, stats.Evictions.Total, elapsed.Milliseconds())
}