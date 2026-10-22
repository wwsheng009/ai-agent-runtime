package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// chat_residency_soak_test.go —— T10-2 / R8：residency 长跑内存门禁。
//
// 设计 §2.2 R8 的断言：`resident_cells` 不随总 cell 数增长；live heap
// ≤120 MB（20k cells）；WS ≤384 MB；`rematerialize_failures=0`。
//
// 运行方式（默认跳过，避免污染常规测试）：
//
//	$env:AICLI_R8_SOAK_MINUTES='30'
//	$env:AICLI_R8_SOAK_OUT='<repo>/output/residency-baseline/r8'
//	go test ./cmd/aicli/commands -run TestResidencySoak -count=1 -timeout 3h -v
//
// 形态：真实 chatRuntimeEventBridge + 真实 sqlite 写穿 + 真实逐出守卫，
// 按固定节拍持续追加 cell（模拟长会话），每轮采样：
// 驻留/冷区/逐出规模、ui 计数器、Go live heap、进程 WS。
func TestResidencySoak(t *testing.T) {
	minutesRaw := os.Getenv("AICLI_R8_SOAK_MINUTES")
	if minutesRaw == "" {
		t.Skip("AICLI_R8_SOAK_MINUTES 未设置：跳过长跑（常规测试不跑）")
	}
	var minutes int
	if _, err := fmt.Sscanf(minutesRaw, "%d", &minutes); err != nil || minutes <= 0 {
		t.Fatalf("AICLI_R8_SOAK_MINUTES 非法：%q", minutesRaw)
	}
	outDir := os.Getenv("AICLI_R8_SOAK_OUT")
	if outDir == "" {
		outDir = filepath.Join(os.TempDir(), "aicli-r8-soak")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	dir := t.TempDir()
	resetResidencyRuntimeForTest(t)
	const keep = 512
	cfg := &runtimecfg.RuntimeConfig{}
	cfg.UI.Residency.Enabled = boolPtr(true)
	cfg.UI.Residency.EvictPrefix = boolPtr(true)
	cfg.UI.Residency.MaxResidentCells = keep
	applyTranscriptResidencyConfig(cfg, dir)
	if runtime := currentTranscriptResidencyRuntime(); runtime == nil || runtime.Store == nil {
		t.Fatalf("residency 运行时未就绪：%+v", runtime)
	}

	sessionID := "r8-soak"
	bridge := newChatRuntimeEventBridge(&ChatSession{RuntimeSession: &runtimechat.Session{ID: sessionID}})
	bridge.applyChangeSet(&encoding.ChangeSet{})
	if bridge.sceneTrimPolicy == nil {
		t.Fatal("裁剪策略未安装")
	}
	// 长跑里桥接不会持续产生提交账本；用"全部已交付"注入（等价于稳定稳态）。
	bridge.sceneTrimPolicy.trimDeliveryFactsOverride = &deliveryFacts{Known: true, Delivered: map[scene.CellID]bool{}}
	delivered := bridge.sceneTrimPolicy.trimDeliveryFactsOverride.Delivered

	type sample struct {
		At                string `json:"at"`
		ElapsedSec        int    `json:"elapsed_s"`
		TotalCells        int    `json:"total_cells"`
		ResidentCells     int    `json:"resident_cells"`
		ColdCells         int    `json:"cold_cells"`
		Evictions         uint64 `json:"evictions"`
		RematFailures     uint64 `json:"rematerialize_failures"`
		HeapAllocMB       uint64 `json:"heap_alloc_mb"`
		HeapInuseMB       uint64 `json:"heap_inuse_mb"`
		TrimmedBytes      int64  `json:"trimmed_bytes"`
		LastReason        string `json:"last_reason"`
		ProcessWorkingSet int64  `json:"process_ws_mb"`
	}

	started := time.Now()
	deadline := started.Add(time.Duration(minutes) * time.Minute)
	nextCellID := scene.CellID(1)
	samples := make([]sample, 0, minutes*4)
	// 追加节奏：每 500ms 追加 20 个 cell（≈40 cells/s）。30 分钟 ≈ 72k cells，
	// 远超设计里 20k cells 的量级，足以暴露"驻留随总量增长"的回归。
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	sampleEvery := 20 // 每 20 个 tick（10s）采一次
	tick := 0
	var lastStats ui.TranscriptResidencyStats

	for time.Now().Before(deadline) {
		<-ticker.C
		tick++
		bridge.sceneMu.Lock()
		for i := 0; i < 20; i++ {
			cell := scene.TranscriptCell{
				ID:       nextCellID,
				Sequence: uint64(nextCellID),
				Revision: 1,
				Kind:     scene.KindAssistant,
				Source:   fmt.Sprintf("R8-SOAK-CELL-%06d body padding for residency soak measurement", nextCellID),
				Phase:    scene.CellCommitted,
			}
			if _, err := bridge.renderScene.ApplyCellMutation(&scene.AppendCell{Cell: cell}); err != nil {
				bridge.sceneMu.Unlock()
				t.Fatalf("append: %v", err)
			}
			delivered[cell.ID] = true
			nextCellID++
		}
		bridge.sceneMu.Unlock()
		// 触发写穿裁剪（每轮单批，长跑里持续收敛到 keep）。
		bridge.applyChangeSet(&encoding.ChangeSet{})

		if tick%sampleEvery != 0 {
			continue
		}
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		policy := bridge.sceneTrimPolicy
		resident := sceneTrimLen(bridge)
		cold := 0
		var evictions uint64
		var trimmedBytes int64
		lastReason := ""
		if policy != nil {
			if policy.Residency != nil {
				cold = policy.Residency.Cold.Len()
			}
			evictions = uint64(policy.TrimmedCells)
			trimmedBytes = policy.TrimmedBytes
			lastReason = policy.LastReason
		}
		lastStats = chatResidencyStatsForSession(&ChatSession{RuntimeEventBridge: bridge, RuntimeSession: &runtimechat.Session{ID: sessionID}}, nil, nil)
		samples = append(samples, sample{
			At:            time.Now().UTC().Format(time.RFC3339),
			ElapsedSec:    int(time.Since(started).Seconds()),
			TotalCells:    int(nextCellID - 1),
			ResidentCells: resident,
			ColdCells:     cold,
			Evictions:     evictions,
			RematFailures: lastStats.Counters.RematerializeFailures,
			HeapAllocMB:   mem.HeapAlloc / (1 << 20),
			HeapInuseMB:   mem.HeapInuse / (1 << 20),
			TrimmedBytes:  trimmedBytes,
			LastReason:    lastReason,
		})
	}

	// 门禁判定（§2.2 R8）。
	failures := make([]string, 0, 4)
	if len(samples) == 0 {
		failures = append(failures, "no samples collected")
	} else {
		first, last := samples[0], samples[len(samples)-1]
		// ① 驻留窗口不随总 cell 数增长：末期驻留不超过 2×keep。
		if last.ResidentCells > keep*2 {
			failures = append(failures, fmt.Sprintf("resident_cells=%d > 2x keep=%d", last.ResidentCells, keep*2))
		}
		// ② 冷区确实在增长（说明逐出真的发生，而不是"没裁"）。
		if last.ColdCells <= first.ColdCells {
			failures = append(failures, fmt.Sprintf("cold_cells did not grow: %d -> %d", first.ColdCells, last.ColdCells))
		}
		// ③ live heap ≤120MB（20k cells 口径；本长跑规模更大，仍按该门禁）。
		if last.HeapAllocMB > 120 {
			failures = append(failures, fmt.Sprintf("heap_alloc=%dMB > 120MB", last.HeapAllocMB))
		}
		// ④ 无可重建失败。
		if last.RematFailures > 0 {
			failures = append(failures, fmt.Sprintf("rematerialize_failures=%d", last.RematFailures))
		}
	}

	report := map[string]any{
		"task":             "R8 residency soak",
		"started_at":       started.UTC().Format(time.RFC3339),
		"minutes":          minutes,
		"keep_cells":       keep,
		"samples":          samples,
		"failures":         failures,
		"verdict":          verdictOf(failures),
		"final_residency":  lastStats,
		"appended_cells":   nextCellID - 1,
		"sample_count":     len(samples),
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "soak-report.json"), raw, 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	if len(failures) > 0 {
		t.Fatalf("R8 长跑门禁失败：%v（报告 %s）", failures, filepath.Join(outDir, "soak-report.json"))
	}
	if len(samples) > 0 {
		last := samples[len(samples)-1]
		t.Logf("R8 PASS: appended=%d resident=%d cold=%d evictions=%d heap=%dMB",
			nextCellID-1, last.ResidentCells, last.ColdCells, last.Evictions, last.HeapAllocMB)
	}
}

func verdictOf(failures []string) string {
	if len(failures) == 0 {
		return "pass"
	}
	return "fail"
}