package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// transcript_load_pager_spike_test.go —— Spike-2（T2）端到端测量：
// 24 页 × 128 cells 经"页序 mint → deliver → ack → evict（sqlite 写穿）"装载，
// 断言顺序/exactly-once/token 连续/窗口上界，并对全部逐出 cell 做冷区回读校验。
//
// 证据：设 AICLI_SPIKE2_OUT=<dir> 时写 report.json。

const (
	// 44 × 128 = 5,632 cells：对齐 T2 验收口径的"合成 5.5k cells 会话"。
	spike2Pages   = 44
	spike2PerPage = 128
)

func spike2PagesFromCorpus(corpus []scene.TranscriptCell, perPage int) []ui.TranscriptLoadPage {
	pages := make([]ui.TranscriptLoadPage, 0, len(corpus)/perPage)
	for i := 0; i*perPage < len(corpus); i++ {
		end := (i + 1) * perPage
		if end > len(corpus) {
			end = len(corpus)
		}
		pages = append(pages, ui.TranscriptLoadPage{Index: i, Cells: corpus[i*perPage : end]})
	}
	return pages
}

func TestSpike2StreamingLoadPagerMeasurement(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 跳过 Spike-2 测量")
	}
	ctx := context.Background()
	total := spike2Pages * spike2PerPage
	corpus := spike1Corpus(total)
	pages := spike2PagesFromCorpus(corpus, spike2PerPage)

	store, err := openSQLiteCellBodyStore(filepath.Join(t.TempDir(), "spike2-cells.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	pager := ui.NewTranscriptLoadPager(spike1SessionID+"-load", pages, store, ui.TranscriptLoadPagerOptions{
		MaxResidentPages: 2,
		RowsPerCell:      3,
	})
	wallStart := time.Now()
	report, err := pager.Run(ctx)
	wall := time.Since(wallStart)
	if err != nil {
		t.Fatalf("load pager run: %v", err)
	}

	// —— 不变量断言（T2 通过标准）——
	if report.OrderViolations != 0 || report.Duplicates != 0 || report.TokenGaps != 0 {
		t.Fatalf("顺序/exactly-once/token 违规：order=%d dup=%d gaps=%d",
			report.OrderViolations, report.Duplicates, report.TokenGaps)
	}
	expected := make([]scene.CellID, 0, total)
	for _, cell := range corpus {
		expected = append(expected, cell.ID)
	}
	if !reflect.DeepEqual(pager.DeliveredOrder(), expected) {
		t.Fatal("交付顺序必须等于 canonical 顺序")
	}
	if report.Pages != spike2Pages || report.Cells != total || report.Rows != total*3 {
		t.Fatalf("规模不符：pages=%d cells=%d rows=%d", report.Pages, report.Cells, report.Rows)
	}
	if report.MaxResidentPages > 2 || report.MaxResidentPagesTransient > 3 {
		t.Fatalf("窗口上界违规：steady=%d transient=%d", report.MaxResidentPages, report.MaxResidentPagesTransient)
	}
	if report.EvictedCells != total || report.PutErrors != 0 {
		t.Fatalf("逐出/写穿计数不符：evicted=%d putErrors=%d", report.EvictedCells, report.PutErrors)
	}
	if report.Rematerialized != total || report.RematFailures != 0 {
		t.Fatalf("冷区回读校验不符：remat=%d failures=%d", report.Rematerialized, report.RematFailures)
	}
	if report.MintMillisMaxMs >= 50 {
		t.Fatalf("单页 mint 超出锁内预算（≥50ms）：%.2fms", report.MintMillisMaxMs)
	}

	stats := store.Stats()
	evidence := map[string]any{
		"tool":                         "spike2-streaming-load-pager",
		"generated_at":                 time.Now().Format(time.RFC3339),
		"pages":                        report.Pages,
		"page_cells":                   spike2PerPage,
		"cells":                        report.Cells,
		"rows":                         report.Rows,
		"order_violations":             report.OrderViolations,
		"duplicates":                   report.Duplicates,
		"token_gaps":                   report.TokenGaps,
		"max_resident_pages":           report.MaxResidentPages,
		"max_resident_pages_transient": report.MaxResidentPagesTransient,
		"max_resident_cells":           report.MaxResidentCells,
		"evicted_cells":                report.EvictedCells,
		"rematerialized":               report.Rematerialized,
		"remat_failures":               report.RematFailures,
		"put_errors":                   report.PutErrors,
		"mint_ms_max":                  report.MintMillisMaxMs,
		"pager_total_ms":               report.TotalMillisMs,
		"wall_ms":                      float64(wall.Microseconds()) / 1000.0,
		"store_stats": map[string]any{
			"puts": stats.Puts, "gets": stats.Gets, "misses": stats.Misses, "errors": stats.Errors,
		},
		"thresholds": map[string]any{
			"order_violations": 0, "duplicates": 0, "token_gaps": 0,
			"max_resident_pages": 2, "max_resident_pages_transient": 3,
			"remat_failures": 0, "put_errors": 0, "mint_ms_max": 50,
		},
	}
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatalf("marshal evidence: %v", err)
	}
	if outDir := strings.TrimSpace(os.Getenv("AICLI_SPIKE2_OUT")); outDir != "" {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			t.Fatalf("mkdir evidence dir: %v", err)
		}
		path := filepath.Join(outDir, "report.json")
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatalf("write evidence: %v", err)
		}
		t.Logf("spike-2 evidence: %s", path)
	}
	t.Logf("spike-2: pages=%d cells=%d rows=%d steady=%d transient=%d evicted=%d remat=%d mintMax=%.2fms total=%.2fms wall=%.2fms store=%+v",
		report.Pages, report.Cells, report.Rows, report.MaxResidentPages, report.MaxResidentPagesTransient,
		report.EvictedCells, report.Rematerialized, report.MintMillisMaxMs, report.TotalMillisMs,
		float64(wall.Microseconds())/1000.0, stats)
}
