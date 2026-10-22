package ui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// transcript_load_pager_test.go —— T2（Spike-2）不变量测试：
// 页序（INV-A）、exactly-once（INV-B）、token 连续（INV-C）、窗口上界（INV-D），
// 以及写穿失败时的 fail-closed 语义（设计 §14.3/§14.6）。

func loadPagerCorpus(pages, cellsPerPage int) []TranscriptLoadPage {
	out := make([]TranscriptLoadPage, 0, pages)
	id := scene.CellID(0)
	for p := 0; p < pages; p++ {
		page := TranscriptLoadPage{Index: p, Cells: make([]scene.TranscriptCell, 0, cellsPerPage)}
		for c := 0; c < cellsPerPage; c++ {
			id++
			cell := scene.TranscriptCell{
				ID:       id,
				Sequence: uint64(id),
				Kind:     scene.KindAssistant,
				Source:   fmt.Sprintf("page %d cell %d 内容\n", p, c),
				Revision: uint64(1 + c%5),
				Phase:    scene.CellCommitted,
			}
			cell.Presentation = scene.TranscriptPresentation{Kind: scene.PresentationPlain}
			page.Cells = append(page.Cells, cell)
		}
		out = append(out, page)
	}
	return out
}

func expectedLoadOrder(pages []TranscriptLoadPage) []scene.CellID {
	var order []scene.CellID
	for _, page := range pages {
		for _, cell := range page.Cells {
			order = append(order, cell.ID)
		}
	}
	return order
}

func TestTranscriptLoadPagerOrderTokensWindowAndRematerialize(t *testing.T) {
	ctx := context.Background()
	const pages, per = 20, 64
	corpus := loadPagerCorpus(pages, per)
	store := NewMemoryCellBodyStore()
	pager := NewTranscriptLoadPager("spike2", corpus, store, TranscriptLoadPagerOptions{
		MaxResidentPages: 2,
		RowsPerCell:      3,
	})

	// 先逐步推进若干页，检查窗口稳态上界。
	for i := 0; i < 5; i++ {
		more, err := pager.Step(ctx)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if !more {
			t.Fatalf("step %d 提前结束", i)
		}
		if resident := len(pager.resident); resident > 2 {
			t.Fatalf("稳态驻留窗口超界：%d 页", resident)
		}
	}

	report, err := pager.Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if report.OrderViolations != 0 || report.Duplicates != 0 || report.TokenGaps != 0 {
		t.Fatalf("不变量违规：order=%d dup=%d gaps=%d", report.OrderViolations, report.Duplicates, report.TokenGaps)
	}
	if report.Pages != pages || report.Cells != pages*per || report.Rows != pages*per*3 {
		t.Fatalf("规模不符：pages=%d cells=%d rows=%d", report.Pages, report.Cells, report.Rows)
	}
	if report.MaxResidentPages > 2 || report.MaxResidentPagesTransient > 3 {
		t.Fatalf("窗口上界违规：steady=%d transient=%d", report.MaxResidentPages, report.MaxResidentPagesTransient)
	}
	if !reflect.DeepEqual(pager.DeliveredOrder(), expectedLoadOrder(corpus)) {
		t.Fatal("交付顺序与 canonical 顺序不一致（INV-A）")
	}
	if report.EvictedCells != pages*per || report.PutErrors != 0 {
		t.Fatalf("逐出/写穿计数不符：evicted=%d putErrors=%d", report.EvictedCells, report.PutErrors)
	}
	if report.Rematerialized != report.EvictedCells || report.RematFailures != 0 {
		t.Fatalf("回读校验不符：remat=%d failures=%d evicted=%d",
			report.Rematerialized, report.RematFailures, report.EvictedCells)
	}
}

func TestTranscriptLoadPagerRejectsPageOrderViolation(t *testing.T) {
	ctx := context.Background()
	corpus := loadPagerCorpus(3, 4)
	corpus[2].Index = 1 // 制造页序倒退
	pager := NewTranscriptLoadPager("s", corpus, NewMemoryCellBodyStore(), TranscriptLoadPagerOptions{MaxResidentPages: 2})
	if _, err := pager.Step(ctx); err != nil {
		t.Fatalf("step0: %v", err)
	}
	if _, err := pager.Step(ctx); err != nil {
		t.Fatalf("step1: %v", err)
	}
	if _, err := pager.Step(ctx); err == nil {
		t.Fatal("页序违规必须报错")
	}
	if report := pager.Report(); report.OrderViolations != 1 {
		t.Fatalf("OrderViolations=%d，期望 1", report.OrderViolations)
	}
}

func TestTranscriptLoadPagerRejectsDuplicateDelivery(t *testing.T) {
	ctx := context.Background()
	corpus := loadPagerCorpus(2, 3)
	corpus[1].Cells[0].ID = corpus[0].Cells[0].ID // 跨页重复身份
	corpus[1].Cells[0].Revision = corpus[0].Cells[0].Revision
	pager := NewTranscriptLoadPager("s", corpus, NewMemoryCellBodyStore(), TranscriptLoadPagerOptions{MaxResidentPages: 2})
	if _, err := pager.Step(ctx); err != nil {
		t.Fatalf("step0: %v", err)
	}
	if _, err := pager.Step(ctx); err == nil {
		t.Fatal("重复交付必须报错")
	}
	if report := pager.Report(); report.Duplicates != 1 {
		t.Fatalf("Duplicates=%d，期望 1", report.Duplicates)
	}
}

type failingPutStore struct{ err error }

func (f failingPutStore) Put(context.Context, string, CellBodyPayload) error { return f.err }
func (f failingPutStore) Get(context.Context, string, scene.CellID, uint64) (CellBodyPayload, bool, error) {
	return CellBodyPayload{}, false, nil
}

func TestTranscriptLoadPagerWriteThroughFailureKeepsResident(t *testing.T) {
	ctx := context.Background()
	corpus := loadPagerCorpus(3, 4)
	store := failingPutStore{err: errors.New("disk full")}
	pager := NewTranscriptLoadPager("s", corpus, store, TranscriptLoadPagerOptions{MaxResidentPages: 1})
	if _, err := pager.Step(ctx); err != nil {
		t.Fatalf("step0: %v", err)
	}
	// 第二页会把第一页挤出窗口 → 触发写穿失败 → 必须报错且不移出驻留。
	if _, err := pager.Step(ctx); err == nil {
		t.Fatal("写穿失败必须报错（fail-closed）")
	}
	if resident := len(pager.resident); resident != 2 {
		t.Fatalf("fail-closed 时不应移出驻留：resident=%d", resident)
	}
	if report := pager.Report(); report.PutErrors != 1 {
		t.Fatalf("PutErrors=%d，期望 1", report.PutErrors)
	}
}
