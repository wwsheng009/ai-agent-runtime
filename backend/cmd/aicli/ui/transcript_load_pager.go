package ui

import (
	"context"
	"fmt"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// transcript_load_pager.go —— C 档「流式装载铸提交状态机」的算法原型（任务 T2 / Spike-2）。
//
// 设计文档：§14.6（页序 mint → deliver → ack → evict；峰值 ≤2 页窗口）。
//
// 本原型刻意与生产装载路径解耦（生产接线在 T3/T6/T8 的 M3 阶段）：
//   - 输入 = 按 canonical 顺序排列的页（每页一段 cell）；
//   - mint：按行分配 token（本 spike 用固定 RowsPerCell 合成；T3 换成真实布局行数）；
//   - deliver：按 canonical 顺序记录身份（禁止重复、禁止倒退）；
//   - ack：spike 采用确定性立即 Ack（真实路径由执行器回 Ack，语义一致）；
//   - evict：写穿 CellBodyStore 后才把页移出驻留窗口（失败即放弃逐出）。
//
// 不变量（每次 Step 内检查，两处违规都进 report 并返回错误）：
//   INV-A 交付顺序 == canonical 顺序（页下标严格递增，页内按序）；
//   INV-B 每个 cell 恰好交付一次；
//   INV-C token 跨页连续（nextToken == 已交付总行数）；
//   INV-D 驻留窗口 ≤ MaxResidentPages（默认 2）。

// TranscriptLoadPage 是一页 canonical cell（Index 必须从 0 递增）。
type TranscriptLoadPage struct {
	Index int
	Cells []scene.TranscriptCell
}

// TranscriptLoadPagerOptions 控制窗口与合成行数。
type TranscriptLoadPagerOptions struct {
	MaxResidentPages int // <=0 → 2（当前页 + 预取页）
	RowsPerCell      int // <=0 → 1（合成行数；T3 用真实布局结果替代）
}

// TranscriptLoadReport 是一次流式装载的可断言结果（Spike-2 证据口径）。
type TranscriptLoadReport struct {
	Pages            int
	Cells            int
	Rows             int
	MaxResidentPages int
	MaxResidentCells int
	// MaxResidentPagesTransient 是"先 append 后 trim"过程中的瞬时窗口页数
	// （诚实口径：真实实现同样存在该瞬时点；MaxResidentPages 为稳态值）。
	MaxResidentPagesTransient int
	OrderViolations           int
	Duplicates                int
	TokenGaps                 int
	EvictedCells              int
	Rematerialized            int
	RematFailures             int
	PutErrors                 int
	MintMillisMaxMs           float64
	TotalMillisMs             float64
}

// TranscriptLoadPager 是装载状态机。
type TranscriptLoadPager struct {
	opts      TranscriptLoadPagerOptions
	store     CellBodyStore
	sessionID string
	pages     []TranscriptLoadPage

	nextPage  int
	nextToken uint64
	delivered map[scene.CellID]struct{}
	order     []scene.CellID

	resident      [][]scene.TranscriptCell
	evictedCells  []scene.TranscriptCell
	startedAt     time.Time
	totalMintCost time.Duration
	report        TranscriptLoadReport
}

// NewTranscriptLoadPager 构造状态机；store 为 nil 时 evict 阶段会跳过写穿
// （仅用于纯算法测试；端到端测量必须传真实 store）。
func NewTranscriptLoadPager(sessionID string, pages []TranscriptLoadPage, store CellBodyStore, opts TranscriptLoadPagerOptions) *TranscriptLoadPager {
	if opts.MaxResidentPages <= 0 {
		opts.MaxResidentPages = 2
	}
	if opts.RowsPerCell <= 0 {
		opts.RowsPerCell = 1
	}
	return &TranscriptLoadPager{
		opts:      opts,
		store:     store,
		sessionID: sessionID,
		pages:     pages,
		delivered: make(map[scene.CellID]struct{}),
		startedAt: time.Now(),
	}
}

// Step 处理下一页；返回是否还有后续工作（false = 已完成收尾）。
func (p *TranscriptLoadPager) Step(ctx context.Context) (bool, error) {
	if p == nil {
		return false, fmt.Errorf("transcript load pager: nil receiver")
	}
	if p.nextPage >= len(p.pages) {
		if len(p.resident) > 0 {
			if err := p.finish(ctx); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	page := p.pages[p.nextPage]

	// INV-A：页序必须与 canonical 顺序一致。
	if page.Index != p.report.Pages {
		p.report.OrderViolations++
		return false, fmt.Errorf("load pager: page order violation: got index %d want %d", page.Index, p.report.Pages)
	}

	// mint：按行分配 token，并校验跨页连续性（INV-C）。
	mintStart := time.Now()
	rowsBefore := p.report.Rows
	for _, cell := range page.Cells {
		if cell.ID == 0 {
			return false, fmt.Errorf("load pager: page %d contains cell with zero id", page.Index)
		}
		p.report.Cells++
		p.report.Rows += p.opts.RowsPerCell
		p.nextToken += uint64(p.opts.RowsPerCell)
	}
	if p.nextToken != uint64(p.report.Rows) {
		p.report.TokenGaps++
		return false, fmt.Errorf("load pager: token gap: nextToken=%d rows=%d", p.nextToken, p.report.Rows)
	}
	if p.report.Rows-rowsBefore != len(page.Cells)*p.opts.RowsPerCell {
		return false, fmt.Errorf("load pager: mint row bookkeeping mismatch on page %d", page.Index)
	}
	mintCost := time.Since(mintStart)
	p.totalMintCost += mintCost
	if mintCost > time.Duration(p.report.MintMillisMaxMs*float64(time.Millisecond)) {
		p.report.MintMillisMaxMs = float64(mintCost.Microseconds()) / 1000.0
	}

	// deliver：记录身份顺序，禁止重复（INV-B）。
	for _, cell := range page.Cells {
		if _, dup := p.delivered[cell.ID]; dup {
			p.report.Duplicates++
			return false, fmt.Errorf("load pager: duplicate delivery of cell %d", cell.ID)
		}
		p.delivered[cell.ID] = struct{}{}
		p.order = append(p.order, cell.ID)
	}

	// 驻留窗口：加入当前页；超出预算则逐出最老页（先写穿，后移出）。
	p.resident = append(p.resident, page.Cells)
	if len(p.resident) > p.report.MaxResidentPagesTransient {
		p.report.MaxResidentPagesTransient = len(p.resident)
	}
	if len(p.resident) > p.opts.MaxResidentPages {
		if err := p.evictOldest(ctx); err != nil {
			return false, err
		}
	}
	if len(p.resident) > p.report.MaxResidentPages {
		p.report.MaxResidentPages = len(p.resident)
	}
	if cells := p.residentCells(); cells > p.report.MaxResidentCells {
		p.report.MaxResidentCells = cells
	}

	p.report.Pages++
	p.nextPage++
	return true, nil
}

// Run 驱动到完成，并做收尾（逐出剩余页 + 冷区回读校验）。
func (p *TranscriptLoadPager) Run(ctx context.Context) (TranscriptLoadReport, error) {
	for {
		more, err := p.Step(ctx)
		if err != nil {
			p.report.TotalMillisMs = float64(time.Since(p.startedAt).Microseconds()) / 1000.0
			return p.report, err
		}
		if !more {
			break
		}
	}
	if err := p.verifyRematerialization(ctx, len(p.evictedCells)); err != nil {
		p.report.TotalMillisMs = float64(time.Since(p.startedAt).Microseconds()) / 1000.0
		return p.report, err
	}
	p.report.TotalMillisMs = float64(time.Since(p.startedAt).Microseconds()) / 1000.0
	return p.report, nil
}

// Report 返回只读报告快照（可中途调用）。
func (p *TranscriptLoadPager) Report() TranscriptLoadReport {
	if p == nil {
		return TranscriptLoadReport{}
	}
	return p.report
}

// DeliveredOrder 返回当前交付身份顺序（测试断言 INV-A/INV-B）。
func (p *TranscriptLoadPager) DeliveredOrder() []scene.CellID {
	if p == nil {
		return nil
	}
	return append([]scene.CellID(nil), p.order...)
}

func (p *TranscriptLoadPager) residentCells() int {
	total := 0
	for _, page := range p.resident {
		total += len(page)
	}
	return total
}

// evictOldest 写穿最老页后将其移出窗口（设计 §14.3：先写穿、后切换）。
func (p *TranscriptLoadPager) evictOldest(ctx context.Context) error {
	if len(p.resident) == 0 {
		return nil
	}
	page := p.resident[0]
	if p.store != nil {
		for _, cell := range page {
			body, err := MarshalTranscriptCellBody(cell, int32(p.opts.RowsPerCell), 1)
			if err != nil {
				return fmt.Errorf("load pager: marshal cell %d: %w", cell.ID, err)
			}
			if err := p.store.Put(ctx, p.sessionID, body); err != nil {
				p.report.PutErrors++
				noteTranscriptResidencyBodyStorePut(true)
				// fail-closed：写穿失败则放弃本次逐出（页保持驻留）。
				return fmt.Errorf("load pager: write-through cell %d: %w", cell.ID, err)
			}
			noteTranscriptResidencyBodyStorePut(false)
		}
	}
	p.resident = p.resident[1:]
	p.evictedCells = append(p.evictedCells, page...)
	p.report.EvictedCells += len(page)
	noteTranscriptResidencyEviction(false)
	return nil
}

// finish 逐出剩余驻留页。
func (p *TranscriptLoadPager) finish(ctx context.Context) error {
	for len(p.resident) > 0 {
		if err := p.evictOldest(ctx); err != nil {
			return err
		}
	}
	return nil
}

// verifyRematerialization 对已逐出 cell 做冷区回读校验（读回 + 指纹比对）。
// limit <=0 表示全量校验。
func (p *TranscriptLoadPager) verifyRematerialization(ctx context.Context, limit int) error {
	if p.store == nil {
		return nil
	}
	candidates := p.evictedCells
	if limit > 0 && limit < len(candidates) {
		candidates = candidates[:limit]
	}
	for _, cell := range candidates {
		body, ok, err := p.store.Get(ctx, p.sessionID, cell.ID, cell.Revision)
		if err != nil {
			p.report.Rematerialized++
			p.report.RematFailures++
			noteTranscriptResidencyRematerialize(true)
			return fmt.Errorf("load pager: rematerialize cell %d: %w", cell.ID, err)
		}
		if !ok {
			p.report.Rematerialized++
			p.report.RematFailures++
			noteTranscriptResidencyRematerialize(true)
			return fmt.Errorf("load pager: rematerialize cell %d revision %d: miss", cell.ID, cell.Revision)
		}
		decoded, err := ValidateTranscriptCellBody(body)
		if err != nil {
			p.report.Rematerialized++
			p.report.RematFailures++
			noteTranscriptResidencyRematerialize(true)
			return fmt.Errorf("load pager: rematerialize validate cell %d: %w", cell.ID, err)
		}
		if decoded.ID != cell.ID || decoded.Revision != cell.Revision {
			p.report.Rematerialized++
			p.report.RematFailures++
			noteTranscriptResidencyRematerialize(true)
			return fmt.Errorf("load pager: rematerialize identity mismatch cell %d", cell.ID)
		}
		p.report.Rematerialized++
		noteTranscriptResidencyRematerialize(false)
	}
	return nil
}
