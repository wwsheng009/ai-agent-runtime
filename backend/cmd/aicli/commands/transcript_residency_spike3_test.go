package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// transcript_residency_spike3_test.go —— Spike-3（T5）端到端测量：
// 已写穿冷区（5,632 cells）上的页面回填延迟、虚拟列表定位性能与锚点往返率。
//
// 证据：设 AICLI_SPIKE3_OUT=<dir> 时写 report.json。

const (
	spike3TotalCells = 44 * 128
	spike3PageCells  = 128
	spike3Pages      = 10
	spike3Probes     = 10000
)

func TestSpike3ColdBackfillAndVirtualPagerMeasurement(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 跳过 Spike-3 测量")
	}
	ctx := context.Background()
	store, err := openSQLiteCellBodyStore(filepath.Join(t.TempDir(), "spike3-cells.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	// 1) 构造全部冷区：安装 5,632 cells 窗口并全部逐出（写穿）。
	policy := ui.ResidencyPolicyFromOptions(ui.TranscriptResidencyOptions{Enabled: true, EvictPrefix: true})
	residency := ui.NewTranscriptResidency(policy, store, "spike3")
	corpus := spike1Corpus(spike3TotalCells)
	rows := make([]int32, len(corpus))
	gaps := make([]int8, len(corpus))
	for i := range corpus {
		rows[i] = 2
		if i == 0 {
			gaps[i] = 1
		}
	}
	if err := residency.InstallWindow(corpus, rows, gaps, 1, 0); err != nil {
		t.Fatalf("install window: %v", err)
	}
	evictFacts := func(int) ui.ResidencyGuardFacts {
		return ui.ResidencyGuardFacts{Finalized: true, AckedAll: true, TombstonePermanent: true}
	}
	evicted := 0
	for len(residency.Window.Cells) > 0 {
		outcome, err := residency.SlidePrefix(ctx, residency.Window.EndSeq, evictFacts)
		if err != nil {
			t.Fatalf("slide: %v", err)
		}
		if outcome.Cells == 0 {
			t.Fatalf("无法继续逐出：%+v", outcome)
		}
		evicted += outcome.Cells
	}
	if evicted != spike3TotalCells || residency.Cold.Len() != spike3TotalCells {
		t.Fatalf("冷区规模不符：evicted=%d cold=%d", evicted, residency.Cold.Len())
	}

	// 2) 页面回填延迟（写穿读回 + 校验 + 入缓存）。
	backfiller := ui.NewTranscriptBackfiller(residency, store)
	backfiller.Cache = ui.NewTranscriptPageCache(4096)
	pageLatency := make([]time.Duration, 0, spike3Pages)
	loadedTotal := 0
	for probe := 0; probe < spike3Pages; probe++ {
		startSeq := (probe * 517) % (spike3TotalCells - spike3PageCells)
		ids := make([]scene.CellID, 0, spike3PageCells)
		for seq := startSeq; seq < startSeq+spike3PageCells; seq++ {
			id, ok := residency.CellIDAtSeq(seq)
			if !ok {
				t.Fatalf("seq %d 无 cell", seq)
			}
			ids = append(ids, id)
		}
		if enqueued := backfiller.EnsureResident(ids...); enqueued != spike3PageCells {
			t.Fatalf("page %d 入队=%d", probe, enqueued)
		}
		start := time.Now()
		processed, err := backfiller.BackfillOnce(ctx)
		pageLatency = append(pageLatency, time.Since(start))
		if err != nil {
			t.Fatalf("page %d backfill: %v", probe, err)
		}
		loadedTotal += processed
	}
	if loadedTotal != spike3Pages*spike3PageCells {
		t.Fatalf("回填数量=%d，期望 %d", loadedTotal, spike3Pages*spike3PageCells)
	}
	p95Page := spike1Percentile(pageLatency, 95)

	// 3) 虚拟列表定位 + 锚点往返。
	pager := ui.NewTranscriptVirtualPager(residency)
	total := pager.TotalRows()
	if total != int64(spike3TotalCells*2) {
		t.Fatalf("总高度=%d，期望 %d", total, spike3TotalCells*2)
	}
	probes := make([]time.Duration, 0, spike3Probes)
	roundTrips := 0
	var missing []string
	for probe := 0; probe < spike3Probes; probe++ {
		row := int64((probe * 7919) % int(total))
		anchor, ok := pager.AnchorAtRow(row)
		if !ok {
			missing = append(missing, "anchor")
			continue
		}
		start := time.Now()
		startSeq, endSeq, ok := pager.VisibleRange(anchor, 40)
		probes = append(probes, time.Since(start))
		if !ok || endSeq <= startSeq {
			missing = append(missing, "range")
			continue
		}
		seq, _ := residency.SeqOf(anchor.CellID)
		base, _ := residency.GlobalRowStart(seq)
		if seq >= startSeq && seq < endSeq && base+int64(anchor.Row) == row {
			roundTrips++
		} else {
			missing = append(missing, "roundtrip")
		}
	}
	p95Probe := spike1Percentile(probes, 95)
	roundTripRate := float64(roundTrips) / float64(spike3Probes)

	stats := backfiller.Stats()
	evidence := map[string]any{
		"tool":                   "spike3-cold-backfill-virtual-pager",
		"generated_at":           time.Now().Format(time.RFC3339),
		"cold_cells":             residency.Cold.Len(),
		"pages_measured":         spike3Pages,
		"page_cells":             spike3PageCells,
		"backfill_loaded":        loadedTotal,
		"backfill_failures":      stats.Failed,
		"backfill_unrebuildable": stats.Unrebuildable,
		"backfill_p95_ms":        float64(p95Page.Microseconds()) / 1000.0,
		"probes":                 spike3Probes,
		"probe_p95_us":           float64(p95Probe.Nanoseconds()) / 1000.0,
		"anchor_round_trip_rate": roundTripRate,
		"cache_cells":            backfiller.Cache.Len(),
		"cache_bytes":            backfiller.Cache.Bytes(),
		"thresholds": map[string]any{
			"backfill_p95_ms": 50.0,
			"probe_p95_us":    100.0,
			"round_trip_rate": 1.0,
		},
	}
	if len(missing) > 0 {
		limit := len(missing)
		if limit > 5 {
			limit = 5
		}
		evidence["missing"] = missing[:limit]
	}
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatalf("marshal evidence: %v", err)
	}
	if outDir := strings.TrimSpace(os.Getenv("AICLI_SPIKE3_OUT")); outDir != "" {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			t.Fatalf("mkdir evidence: %v", err)
		}
		path := filepath.Join(outDir, "report.json")
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatalf("write evidence: %v", err)
		}
		t.Logf("spike-3 evidence: %s", path)
	}
	t.Logf("spike-3: cold=%d pages=%d loaded=%d p95Page=%.2fms probes=%d p95Probe=%.3fµs roundTrip=%.4f cache=%d/%dB",
		residency.Cold.Len(), spike3Pages, loadedTotal, float64(p95Page.Microseconds())/1000.0,
		spike3Probes, float64(p95Probe.Nanoseconds())/1000.0, roundTripRate,
		backfiller.Cache.Len(), backfiller.Cache.Bytes())

	// —— 门禁 ——
	if stats.Failed != 0 || stats.Unrebuildable != 0 {
		t.Fatalf("回填失败/不可重建必须为 0：%+v", stats)
	}
	if p95Page > 50*time.Millisecond {
		t.Fatalf("冷页回填 p95=%v，超过 50ms 门禁", p95Page)
	}
	if p95Probe > 100*time.Microsecond {
		t.Fatalf("定位 p95=%v，超过 100µs 门禁", p95Probe)
	}
	if roundTripRate != 1.0 {
		t.Fatalf("锚点往返率=%.4f，必须 100%%", roundTripRate)
	}
}
