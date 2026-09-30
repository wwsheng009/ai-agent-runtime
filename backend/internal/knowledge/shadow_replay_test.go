package knowledge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageledger"
)

// replayCall 是提取脚本（backend/scripts/extract-shadow-calls.mjs）产出的
// 一次真实工具调用：args + output 都来自会话日志，保证 baseline 与模型当时
// 看到的输出一致（ADR-0003 §4.3）。
type replayCall struct {
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args"`
	Output    string         `json:"output"`
	SessionID string         `json:"session_id"`
}

// TestPhase1ShadowReplay 是 Phase1-shadow 的实测量入口（手动运行，CI 跳过）。
//
// 它把真实会话里的 grep/view 调用重放给生产 ShadowObserver：索引侧候选走
// 真实 knowledge.db，baseline 走日志里的原始输出，落库走真实 usageledger，
// 最后用 SummarizeAttribution 复算 M1–M4 并给出 α 校准建议。
//
// 环境变量：
//
//	KNOWLEDGE_SHADOW_REPO      被索引的工作区根目录（必填）
//	KNOWLEDGE_SHADOW_CALLS     调用集 JSONL 路径（必填，由提取脚本生成）
//	KNOWLEDGE_SHADOW_ALPHA     本次复算使用的 α（缺省 DefaultShadowAlpha）
//	KNOWLEDGE_SHADOW_DB        索引库路径（缺省临时文件；配合 SKIP_INDEX 可复用）
//	KNOWLEDGE_SHADOW_SKIP_INDEX=1  跳过重建索引，复用既有 KNOWLEDGE_SHADOW_DB
//	KNOWLEDGE_SHADOW_LEDGER_DB 归因落库的账本路径（缺省临时文件）
//
// 运行示例：
//
//	cd backend
//	$env:KNOWLEDGE_SHADOW_REPO=(Resolve-Path ..).Path
//	$env:KNOWLEDGE_SHADOW_CALLS='../docs/knowledge_Layer/reports/phase1_shadow_calls.jsonl'
//	go test ./internal/knowledge/ -run TestPhase1ShadowReplay -v -count=1 -timeout 30m
func TestPhase1ShadowReplay(t *testing.T) {
	repo := strings.TrimSpace(os.Getenv("KNOWLEDGE_SHADOW_REPO"))
	callsPath := strings.TrimSpace(os.Getenv("KNOWLEDGE_SHADOW_CALLS"))
	if repo == "" || callsPath == "" {
		t.Skip("set KNOWLEDGE_SHADOW_REPO and KNOWLEDGE_SHADOW_CALLS to run the Phase1-shadow replay measurement")
	}
	ctx := context.Background()
	calls := loadReplayCalls(t, callsPath)
	if len(calls) == 0 {
		t.Fatalf("replay call set %s is empty", callsPath)
	}

	alpha := DefaultShadowAlpha
	if raw := strings.TrimSpace(os.Getenv("KNOWLEDGE_SHADOW_ALPHA")); raw != "" {
		if _, err := fmt.Sscanf(raw, "%f", &alpha); err != nil || alpha <= 0 || alpha > 1 {
			t.Fatalf("invalid KNOWLEDGE_SHADOW_ALPHA=%q", raw)
		}
	}

	storeDSN := strings.TrimSpace(os.Getenv("KNOWLEDGE_SHADOW_DB"))
	if storeDSN == "" {
		storeDSN = filepath.Join(t.TempDir(), "knowledge.db")
	}
	store, err := OpenStore(ctx, storeDSN, false)
	if err != nil {
		t.Fatalf("OpenStore(%s): %v", storeDSN, err)
	}
	defer store.Close()

	cfg := DefaultConfig().WithWorkspace(repo)
	cfg.Mode = ModeShadow
	cfg.Alpha = alpha
	if os.Getenv("KNOWLEDGE_SHADOW_SKIP_INDEX") == "1" {
		if _, err := store.EnsureWorkspace(ctx, Workspace{RootPath: repo}); err != nil {
			t.Fatalf("EnsureWorkspace: %v", err)
		}
		fmt.Printf("=== index (reused %s) ===\n", storeDSN)
	} else {
		started := time.Now()
		res, err := RunIndex(ctx, store, cfg)
		if err != nil {
			t.Fatalf("RunIndex: %v", err)
		}
		fmt.Printf("=== index ===\nscanned=%d indexed=%d skipped=%d errors=%d deleted=%d symbols=%d refs=%d duration=%s\n",
			res.Scanned, res.Indexed, res.Skipped, res.Errors, res.Deleted, res.Symbols, res.Refs, time.Since(started).Round(time.Millisecond))
	}
	if info, err := os.Stat(storeDSN); err == nil {
		fmt.Printf("index_db_bytes=%d\n", info.Size())
	}

	ledgerDSN := strings.TrimSpace(os.Getenv("KNOWLEDGE_SHADOW_LEDGER_DB"))
	if ledgerDSN == "" {
		ledgerDSN = filepath.Join(t.TempDir(), "ledger.db")
	}
	ledger, err := usageledger.NewSQLiteStore(&usageledger.Config{Driver: "sqlite", DSN: ledgerDSN})
	if err != nil {
		t.Fatalf("open ledger(%s): %v", ledgerDSN, err)
	}
	defer ledger.Close()

	observer := NewShadowObserver(ShadowConfig{
		Alpha:     alpha,
		Mode:      ModeShadow,
		Workspace: repo,
		ProjectID: ProjectIDForWorkspace(repo),
		Index:     store,
		Sink:      ledger,
	})

	since := time.Now().UTC().Add(-time.Second)
	skipped := 0
	// file-level 覆盖（口径裁决数据）：与行级口径同时给出"文件集合重合"的数字。
	fileLevel := replayFileLevel{}
	for i, call := range calls {
		if err := ctx.Err(); err != nil {
			t.Fatalf("cancelled after %d/%d calls: %v", i, len(calls), err)
		}
		rec, detail, err := observer.observeDetailed(ctx, ObservedCall{
			SessionID: call.SessionID,
			TurnID:    fmt.Sprintf("replay-%d", i+1),
			Tool:      call.Tool,
			Args:      call.Args,
			Output:    call.Output,
		})
		if err != nil {
			t.Fatalf("observe call %d (%s): %v", i+1, call.Tool, err)
		}
		if rec == nil {
			skipped++
			continue
		}
		if call.Tool == "grep" {
			fileLevel.add(rec, detail)
		}
	}

	records, err := ledger.ListExplorationAttribution(ctx, since, len(calls)+10)
	if err != nil {
		t.Fatalf("ListExplorationAttribution: %v", err)
	}
	report := SummarizeAttribution(records, alpha)
	if again := SummarizeAttribution(records, alpha); !reflect.DeepEqual(report, again) {
		t.Fatalf("recompute mismatch: %+v vs %+v", report, again)
	}

	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	fmt.Printf("=== replay ===\ntotal_calls=%d observed_rows=%d skipped=%d\n", len(calls), len(records), skipped)
	if fileLevel.denominator > 0 {
		mean := 0.0
		for _, coverage := range fileLevel.coverages {
			mean += coverage
		}
		mean /= float64(len(fileLevel.coverages))
		usable := 0
		for _, coverage := range fileLevel.coverages {
			if coverage >= alpha {
				usable++
			}
		}
		fmt.Printf("=== file-level coverage (grep, 口径裁决用) ===\ncalls=%d denominator=%d mean=%.4f p50=%.4f p90=%.4f usable@alpha=%.4f file_precision=%.4f answerable_rate=%.4f\n",
			fileLevel.calls, fileLevel.denominator, mean,
			percentileValue(fileLevel.coverages, 0.5), percentileValue(fileLevel.coverages, 0.9),
			float64(usable)/float64(fileLevel.denominator),
			float64(fileLevel.overlapFiles)/float64(fileLevel.baselineFiles),
			float64(fileLevel.answerable)/float64(fileLevel.calls))
	}
	if os.Getenv("KNOWLEDGE_SHADOW_DEBUG") == "1" {
		limit := len(records)
		if limit > 12 {
			limit = 12
		}
		for _, rec := range records[:limit] {
			fmt.Printf("sample tool=%s baseline_n=%d candidate_n=%d overlap_n=%d coverage=%v economy=%v\n",
				rec.Tool, rec.BaselineN, rec.CandidateN, rec.OverlapN, rec.Coverage, rec.Economy)
		}
	}
	fmt.Printf("=== M1-M4 (alpha=%.2f) ===\n%s\n", alpha, payload)
	fmt.Printf("=== alpha calibration ===\ncalibrated_alpha=%.2f\n", CalibrateShadowAlpha(records))
}

// loadReplayCalls 读取调用集 JSONL；坏行显式失败，避免静默丢样本。
func loadReplayCalls(t *testing.T, path string) []replayCall {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open replay calls: %v", err)
	}
	defer file.Close()

	var out []replayCall
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 8<<20)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var call replayCall
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatalf("parse replay call line %d: %v", lineNo, err)
		}
		if call.Tool != "grep" && call.Tool != "view" {
			t.Fatalf("replay call line %d: unsupported tool %q", lineNo, call.Tool)
		}
		out = append(out, call)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan replay calls: %v", err)
	}
	return out
}

// replayFileLevel 汇总 grep 调用的 file-level 覆盖（口径裁决数据）：
// 覆盖率 := |G_files ∩ K_files| / |G_files|，只在 baseline 文件集非空时进分母；
// file_precision := Σ|G_files ∩ K_files| / Σ|G_files|；answerable_rate :=
// candidate_n > 0 的调用占比。
type replayFileLevel struct {
	calls         int
	denominator   int
	answerable    int
	overlapFiles  int
	baselineFiles int
	coverages     []float64
}

func (f *replayFileLevel) add(rec *entity.ExplorationAttribution, detail ObservationDetail) {
	f.calls++
	if rec.CandidateN > 0 {
		f.answerable++
	}
	if len(detail.BaselineFiles) == 0 {
		return
	}
	f.denominator++
	overlap := 0
	for file := range detail.BaselineFiles {
		if _, ok := detail.CandidateFiles[file]; ok {
			overlap++
		}
	}
	f.overlapFiles += overlap
	f.baselineFiles += len(detail.BaselineFiles)
	f.coverages = append(f.coverages, float64(overlap)/float64(len(detail.BaselineFiles)))
}
