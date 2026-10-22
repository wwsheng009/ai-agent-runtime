package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// transcript_body_store_spike_test.go —— Spike-1（T1）抽样测量：
// 1,000 个混合 cell 经 sqlite 写穿 → 读回 → 校验，输出一致率/延迟/页字节，
// 供 M2 前置门禁与后续 A/B 对照（设计文档 §14.2/§14.5；任务清单 §T1）。
//
// 证据：设 AICLI_SPIKE1_OUT=<dir> 时写 report.json；否则只写 t.Log。

const (
	spike1SessionID     = "spike1-residency"
	spike1Cells         = 1000
	spike1PageCells     = 256
	spike1GetP95Budget  = 50 * time.Millisecond
	spike1PageByteLimit = 2 * 1024 * 1024
)

func spike1Corpus(n int) []scene.TranscriptCell {
	out := make([]scene.TranscriptCell, 0, n)
	for i := 0; i < n; i++ {
		cell := scene.TranscriptCell{
			ID:       scene.CellID(i + 1),
			Sequence: uint64(i + 1),
			Revision: uint64(i%7 + 1),
			Phase:    scene.CellCommitted,
		}
		switch i % 5 {
		case 0: // 用户纯文本：CJK + 内部空行 + 尾随空行（INV-9 边界）
			cell.Kind = scene.KindUser
			cell.Source = fmt.Sprintf("用户消息 %d\n第二行内容\n\n", i)
			cell.Presentation = scene.TranscriptPresentation{Kind: scene.PresentationPlain}
		case 1: // assistant markdown：结构化 Document
			cell.Kind = scene.KindAssistant
			cell.Source = fmt.Sprintf("# 标题 %d\n\n- 项目一\n- 项目二\n\n```go\nfmt.Println(%d)\n```\n", i, i)
			cell.Presentation = scene.TranscriptPresentation{
				Kind: scene.PresentationDocument,
				Document: render.Document{Blocks: []render.Block{
					{Kind: render.BlockParagraph, Lines: []render.Line{
						{Spans: []render.Span{{Text: fmt.Sprintf("段落 %d", i), Style: render.Style{Bold: true}}}},
					}},
					{Kind: render.BlockCode, Lines: []render.Line{
						{Spans: []render.Span{{Text: fmt.Sprintf("fmt.Println(%d)", i), Style: render.Style{Role: "code"}}}},
					}},
				}},
			}
		case 2: // diff supplement：DiffLabel 参与身份
			cell.Kind = scene.KindSupplement
			cell.Source = fmt.Sprintf("diff --git a/f%d.go b/f%d.go\n@@ -1,3 +1,3 @@\n-old %d\n+new %d\n", i, i, i, i)
			cell.Presentation = scene.TranscriptPresentation{Kind: scene.PresentationDiffSupplement, DiffLabel: "Edited"}
		case 3: // reasoning：长正文
			cell.Kind = scene.KindReasoning
			var builder strings.Builder
			for k := 0; k < 40; k++ {
				fmt.Fprintf(&builder, "reasoning %d 中文推演第 %d 行\n", i, k)
			}
			cell.Source = builder.String()
			cell.Presentation = scene.TranscriptPresentation{Kind: scene.PresentationPlain}
		case 4: // tool chain：分组键
			cell.Kind = scene.KindToolChain
			cell.Source = fmt.Sprintf("tool output %d\n", i)
			cell.Presentation = scene.TranscriptPresentation{Kind: scene.PresentationPlain}
			cell.ChainKey = fmt.Sprintf("chain-%d", i/10)
			cell.BoundaryGroupKey = fmt.Sprintf("grp-%d", i/50)
		}
		out = append(out, cell)
	}
	return out
}

func spike1Percentile(values []time.Duration, percentile int) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := (percentile*len(sorted) + 99) / 100
	if index < 1 {
		index = 1
	}
	if index > len(sorted) {
		index = len(sorted)
	}
	return sorted[index-1]
}

func TestSpike1CellBodyStoreMeasurement(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 跳过 Spike-1 测量")
	}
	ctx := context.Background()
	store, err := openSQLiteCellBodyStore(filepath.Join(t.TempDir(), "spike1-cells.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	corpus := spike1Corpus(spike1Cells)

	putLatency := make([]time.Duration, 0, len(corpus))
	payloadBytes := 0
	for i, cell := range corpus {
		rowsAtGen := int32(1 + (i % 200))
		gen := uint64(1 + i%3)
		payload, err := ui.MarshalTranscriptCellBody(cell, rowsAtGen, gen)
		if err != nil {
			t.Fatalf("cell %d marshal: %v", cell.ID, err)
		}
		payloadBytes += len(payload.Data)
		start := time.Now()
		if err := store.Put(ctx, spike1SessionID, payload); err != nil {
			t.Fatalf("cell %d put: %v", cell.ID, err)
		}
		putLatency = append(putLatency, time.Since(start))
	}

	getLatency := make([]time.Duration, 0, len(corpus))
	consistent := 0
	for _, cell := range corpus {
		start := time.Now()
		payload, ok, err := store.Get(ctx, spike1SessionID, cell.ID, cell.Revision)
		getLatency = append(getLatency, time.Since(start))
		if err != nil {
			t.Fatalf("cell %d get: %v", cell.ID, err)
		}
		if !ok {
			t.Fatalf("cell %d revision %d get miss（写穿后必须可读）", cell.ID, cell.Revision)
		}
		decoded, err := ui.ValidateTranscriptCellBody(payload)
		if err != nil {
			t.Fatalf("cell %d validate: %v", cell.ID, err)
		}
		if decoded.Source == cell.Source && decoded.Revision == cell.Revision &&
			reflect.DeepEqual(decoded.Presentation, cell.Presentation) {
			consistent++
		}
	}

	p95Put := spike1Percentile(putLatency, 95)
	p95Get := spike1Percentile(getLatency, 95)
	avgPayload := payloadBytes / len(corpus)
	pageBytes := avgPayload * spike1PageCells
	stats := store.Stats()
	consistency := float64(consistent) / float64(len(corpus))

	report := map[string]any{
		"tool":              "spike1-cell-body-store",
		"generated_at":      time.Now().Format(time.RFC3339),
		"cells":             len(corpus),
		"consistent_cells":  consistent,
		"consistency_rate":  consistency,
		"put_p95_ms":        float64(p95Put.Microseconds()) / 1000.0,
		"get_p95_ms":        float64(p95Get.Microseconds()) / 1000.0,
		"avg_payload_bytes": avgPayload,
		"page_cells":        spike1PageCells,
		"page_bytes":        pageBytes,
		"store_stats": map[string]any{
			"puts": stats.Puts, "gets": stats.Gets, "misses": stats.Misses, "errors": stats.Errors,
		},
		"thresholds": map[string]any{
			"consistency_rate": 1.0,
			"get_p95_ms":       spike1GetP95Budget.Milliseconds(),
			"page_bytes":       spike1PageByteLimit,
		},
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if outDir := strings.TrimSpace(os.Getenv("AICLI_SPIKE1_OUT")); outDir != "" {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			t.Fatalf("mkdir evidence dir: %v", err)
		}
		path := filepath.Join(outDir, "report.json")
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatalf("write evidence: %v", err)
		}
		t.Logf("spike-1 evidence: %s", path)
	}
	t.Logf("spike-1: cells=%d consistent=%d put_p95=%.2fms get_p95=%.2fms avg_payload=%dB page_bytes=%dB stats=%+v",
		len(corpus), consistent, float64(p95Put.Microseconds())/1000.0, float64(p95Get.Microseconds())/1000.0,
		avgPayload, pageBytes, stats)

	// 硬门禁（任务清单 §T1 通过标准）。
	if consistent != len(corpus) {
		t.Fatalf("一致率必须 100%%：%d/%d", consistent, len(corpus))
	}
	if stats.Errors != 0 {
		t.Fatalf("写穿错误必须为 0：%+v", stats)
	}
	if p95Get > spike1GetP95Budget {
		t.Fatalf("读回 p95 必须 ≤%v，实际 %v", spike1GetP95Budget, p95Get)
	}
	if pageBytes > spike1PageByteLimit {
		t.Fatalf("页字节（%d cells）必须 ≤%d，实际 %d", spike1PageCells, spike1PageByteLimit, pageBytes)
	}
}
