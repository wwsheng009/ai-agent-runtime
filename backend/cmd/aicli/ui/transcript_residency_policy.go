package ui

import (
	"context"
	"fmt"
	"math"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// transcript_residency_policy.go —— C 档窗口滑动与逐出守卫（任务 T4）。
//
// 设计文档：§14.3（Hot→Cold 守卫）、§14.4（mustKeep/滞回/原子写穿）。
//
// 分层：
//   - EvaluateEvictionGuards：纯函数守卫（决策矩阵穷举的测试对象）；
//   - ResidencyPolicy.PlanSlide：滞回 + 高水位 + 单次步长上限的滑动规划；
//   - TranscriptResidency：窗口/冷区/状态机与**原子** slidePrefix
//     （阶段一全部写穿成功，阶段二才提交账本与状态）。

// PinReason 是"禁止逐出"的原因位（可组合）。
type PinReason uint16

const (
	PinActiveCell       PinReason = 1 << iota // mutable/finalizing、frontier barrier
	PinUnackedRows                            // 存在未 Ack/Unknown/partial 行
	PinQueuedTokens                           // 队列中 token 未结算
	PinPagerTarget                            // pager 可见页/锚点所在
	PinViewportNeighbor                       // 主界面可视区 ± margin
	PinRecentlyRead                           // 最近被 /debug、/web 读取（TTL）
)

func (p PinReason) Has(flag PinReason) bool { return p&flag != 0 }

// Residency 是单 cell 的驻留状态（§14.2 状态机）。
type Residency uint8

const (
	Hot           Residency = iota // 完整 body 在窗口
	Cold                           // 仅 ColdCellRef
	Loading                        // 回填中（pin）
	Unrebuildable                  // 校验失败：永久 pin
)

// ResidencyGuardFacts 是逐出判定所需的全部事实（由调用方从账本/队列计算）。
type ResidencyGuardFacts struct {
	State              Residency
	Finalized          bool
	AckedAll           bool
	TombstonePermanent bool
	EpochOpen          bool
	Pins               PinReason
}

// EvaluateEvictionGuards 是纯函数的逐出守卫（设计 §14.3）。
// 返回是否可逐出与稳定原因码（原因码用于观测与决策矩阵断言）。
func EvaluateEvictionGuards(facts ResidencyGuardFacts) (bool, string) {
	if facts.State != Hot {
		return false, "state_not_hot"
	}
	if !facts.Finalized {
		return false, "not_finalized"
	}
	if !facts.AckedAll {
		return false, "unacked_rows"
	}
	if !facts.TombstonePermanent {
		return false, "tombstone_not_permanent"
	}
	if facts.EpochOpen {
		return false, "epoch_open"
	}
	if facts.Pins != 0 {
		return false, "pinned"
	}
	return true, "evictable"
}

// ResidencyPolicy 是滑动/逐出的策略参数（由 T0 选项推导）。
type ResidencyPolicy struct {
	Enabled          bool
	EvictPrefix      bool
	MaxResidentCells int
	MaxResidentRows  int
	HysteresisCells  int
	EvictHighWater   float64
}

// ResidencyPolicyFromOptions 从启动期选项构造策略（零值取默认）。
func ResidencyPolicyFromOptions(opts TranscriptResidencyOptions) ResidencyPolicy {
	opts = clampTranscriptResidencyOptions(opts)
	return ResidencyPolicy{
		Enabled:          opts.Enabled,
		EvictPrefix:      opts.EvictPrefix,
		MaxResidentCells: opts.MaxResidentCells,
		MaxResidentRows:  opts.MaxResidentRows,
		HysteresisCells:  opts.HysteresisCells,
		EvictHighWater:   opts.EvictHighWater,
	}
}

// MaxStepCells 返回单次滑动允许移交的最大 cell 数（≤ maxCells/4）。
func (p ResidencyPolicy) MaxStepCells() int {
	step := p.MaxResidentCells / 4
	if step < 1 {
		step = 1
	}
	return step
}

// PlanSlide 决定是否滑动以及滑到哪个下标（设计 §14.4，滞回防抖）。
//
//	desiredStart：mustKeep / pager / viewport 三者取最小（调用方算好）。
//	高水位（cells/rows 超过 max×1.25）时忽略滞回（强制回收）。
//	非高水位时，收益 ≤ HysteresisCells 则不动（防抖）。
func (p ResidencyPolicy) PlanSlide(window WindowLedger, desiredStart int) (target int, shouldSlide bool) {
	windowCells := len(window.Cells)
	if windowCells == 0 || desiredStart <= window.StartSeq {
		return 0, false
	}
	lag := desiredStart - window.StartSeq
	highWaterCells := float64(windowCells) > float64(p.MaxResidentCells)*p.EvictHighWater
	highWaterRows := float64(window.TotalRows()) > float64(p.MaxResidentRows)*p.EvictHighWater
	if !highWaterCells && !highWaterRows && lag <= p.HysteresisCells {
		return 0, false
	}
	target = desiredStart
	if step := p.MaxStepCells(); lag > step {
		target = window.StartSeq + step
	}
	if target > window.EndSeq {
		target = window.EndSeq
	}
	if target <= window.StartSeq {
		return 0, false
	}
	return target, true
}

// SlideOutcome 描述一次 slidePrefix 的结果。
type SlideOutcome struct {
	Cells   int    // 实际移交（逐出）的 cell 数
	Pinned  bool   // 因守卫/状态停在原地（前缀首个不可逐出）
	Aborted bool   // 写穿失败，原子中止（窗口与状态保持不变）
	Reason  string // 稳定原因码
}

// TranscriptResidency 组合窗口/冷区/状态机（T3 数据结构 + T4 滑动）。
type TranscriptResidency struct {
	Policy    ResidencyPolicy
	Store     CellBodyStore // nil = 纯算法模式（跳过写穿，仅测试/对拍用）
	SessionID string

	Cold   ColdLedger
	Window WindowLedger

	states          map[scene.CellID]Residency
	nextDeliveredAt int64
	// indexCache / windowStartsCache 是 T5 的派生索引缓存（自失效）。
	indexCache        *residencyIndexCache
	windowStartsCache []int64
}

// NewTranscriptResidency 构造驻留状态（状态默认 Hot）。
func NewTranscriptResidency(policy ResidencyPolicy, store CellBodyStore, sessionID string) *TranscriptResidency {
	return &TranscriptResidency{
		Policy:    policy,
		Store:     store,
		SessionID: sessionID,
		states:    make(map[scene.CellID]Residency),
	}
}

// InstallWindow 安装一个窗口（cells 必须连续且已按序）。
func (r *TranscriptResidency) InstallWindow(cells []scene.TranscriptCell, rows []int32, leadingGaps []int8, gen uint64, startSeq int) error {
	if len(cells) != len(rows) {
		return fmt.Errorf("residency: cells=%d rows=%d 数量不一致", len(cells), len(rows))
	}
	if leadingGaps != nil && len(leadingGaps) != len(cells) {
		return fmt.Errorf("residency: leadingGaps=%d cells=%d 数量不一致", len(leadingGaps), len(cells))
	}
	gaps := make([]int8, len(cells))
	if leadingGaps != nil {
		copy(gaps, leadingGaps)
	}
	r.Window = WindowLedger{
		StartSeq:    startSeq,
		EndSeq:      startSeq + len(cells),
		Cells:       append([]scene.TranscriptCell(nil), cells...),
		LocalRows:   append([]int32(nil), rows...),
		LeadingGaps: gaps,
		Generation:  gen,
	}
	for _, cell := range cells {
		r.states[cell.ID] = Hot
	}
	r.indexCache = nil
	r.windowStartsCache = nil
	return nil
}

// StateOf 返回驻留状态（未知视为 Hot：未登记即未逐出）。
func (r *TranscriptResidency) StateOf(id scene.CellID) Residency {
	if r == nil || r.states == nil {
		return Hot
	}
	if state, ok := r.states[id]; ok {
		return state
	}
	return Hot
}

// MarkState 设置驻留状态（Loading/Unrebuildable 由回填路径使用）。
func (r *TranscriptResidency) MarkState(id scene.CellID, state Residency) {
	if r.states == nil {
		r.states = make(map[scene.CellID]Residency)
	}
	r.states[id] = state
}

// SlidePrefix 原子地把窗口前 target 个 cell 移入冷区。
//
// facts(i) 返回窗口内第 i 个 cell 的守卫事实（由生产接线从 ledger/队列计算；
// 单测直接构造）。语义：
//  1. evictPrefix 关闭 → 不动（返回 reason=evict_disabled）；
//  2. 只裁**连续可逐出前缀**（遇到第一个不可逐出即停止，Pinned=true）；
//  3. 阶段一：全部 Marshal + Put 成功；
//     任一失败 → Aborted=true，窗口/冷区/状态均不变（fail-closed）；
//  4. 阶段二：按序 AppendCell 到冷区账本 + 从窗口移除 + 状态置 Cold。
func (r *TranscriptResidency) SlidePrefix(ctx context.Context, target int, facts func(index int) ResidencyGuardFacts) (SlideOutcome, error) {
	if r == nil {
		return SlideOutcome{}, fmt.Errorf("residency: nil receiver")
	}
	if !r.Policy.EvictPrefix {
		return SlideOutcome{Reason: "evict_disabled"}, nil
	}
	if facts == nil {
		return SlideOutcome{}, fmt.Errorf("residency: facts callback required")
	}
	if target > r.Window.EndSeq {
		target = r.Window.EndSeq
	}
	if target <= r.Window.StartSeq {
		return SlideOutcome{Reason: "nothing_to_slide"}, nil
	}
	if step := r.Policy.MaxStepCells(); target-r.Window.StartSeq > step {
		target = r.Window.StartSeq + step
	}

	// 收敛连续可逐出前缀。
	count := 0
	for index := 0; index < len(r.Window.Cells) && r.Window.StartSeq+index < target; index++ {
		cell := r.Window.Cells[index]
		guardFacts := facts(index)
		// 状态轴由驻留状态机权威决定：callback 只需给出账本/队列事实
		// （finalized/acked/tombstone/epoch/pins），避免两处状态漂移。
		guardFacts.State = r.StateOf(cell.ID)
		ok, reason := EvaluateEvictionGuards(guardFacts)
		if !ok {
			if count == 0 {
				return SlideOutcome{Pinned: true, Reason: reason}, nil
			}
			break
		}
		count++
	}
	if count == 0 {
		return SlideOutcome{Reason: "empty_prefix"}, nil
	}
	cutByGuard := r.Window.StartSeq+count < target

	// 阶段一：写穿（全部成功才提交）。
	deliveredAt := r.nextDeliveredAt
	for index := 0; index < count; index++ {
		cell := r.Window.Cells[index]
		deliveredAt++
		if r.Store == nil {
			continue
		}
		body, err := MarshalTranscriptCellBody(cell, r.Window.LocalRows[index], r.Window.Generation)
		if err != nil {
			return SlideOutcome{Aborted: true, Reason: "marshal_failed"}, err
		}
		if err := r.Store.Put(ctx, r.SessionID, body); err != nil {
			noteTranscriptResidencyBodyStorePut(true)
			return SlideOutcome{Aborted: true, Reason: "write_through_failed"}, err
		}
		noteTranscriptResidencyBodyStorePut(false)
	}

	// 阶段二：提交（账本 + 窗口 + 状态）。
	for index := 0; index < count; index++ {
		cell := r.Window.Cells[index]
		r.nextDeliveredAt++
		gap := int8(0)
		if r.Window.LeadingGaps != nil {
			gap = r.Window.LeadingGaps[index]
		}
		if _, err := r.Cold.AppendCell(cell, r.Window.LocalRows[index], gap, r.Window.Generation, r.nextDeliveredAt, true); err != nil {
			return SlideOutcome{Aborted: true, Reason: "cold_append_failed"}, err
		}
	}
	detached, _, err := r.Window.DetachPrefix(count)
	if err != nil {
		return SlideOutcome{Aborted: true, Reason: "detach_failed"}, err
	}
	for _, cell := range detached {
		r.MarkState(cell.ID, Cold)
	}
	r.indexCache = nil
	r.windowStartsCache = nil
	noteTranscriptResidencyEviction(false)
	return SlideOutcome{Cells: count, Pinned: cutByGuard, Reason: "evicted"}, nil
}

// AppendEvicted 把一批**已经写穿成功**的 cell 记入冷区账本（生产 bridge 的
// 逐出路径使用）。
//
// 与 SlidePrefix 的分工：SlidePrefix 是"住留状态机内部"的原子滑动（窗口 →
// 冷区）；bridge 的写穿裁剪已经在 scene 层完成写穿与裁剪，只需把同样的
// 事实补记到账本，供 pager 冷区读回与 status 的 index 分区使用。
//
// 约束：cells 必须按交付顺序（append-only）；rows 与 cells 等长；gen 为该批
// 的布局代。写穿失败的回滚由调用方保证（本方法只记账，不再触碰 store）。
func (r *TranscriptResidency) AppendEvicted(cells []scene.TranscriptCell, rows []int32, gaps []int8, gen uint64) error {
	if r == nil {
		return fmt.Errorf("residency: nil receiver")
	}
	if len(cells) != len(rows) {
		return fmt.Errorf("residency: cells=%d rows=%d 数量不一致", len(cells), len(rows))
	}
	if gaps != nil && len(gaps) != len(cells) {
		return fmt.Errorf("residency: gaps=%d cells=%d 数量不一致", len(gaps), len(cells))
	}
	for index := range cells {
		gap := int8(0)
		if gaps != nil {
			gap = gaps[index]
		}
		r.nextDeliveredAt++
		if _, err := r.Cold.AppendCell(cells[index], rows[index], gap, gen, r.nextDeliveredAt, true); err != nil {
			return err
		}
		r.MarkState(cells[index].ID, Cold)
	}
	r.indexCache = nil
	r.windowStartsCache = nil
	return nil
}

// DesiredStart 计算 §14.4 的 desired = min(mustKeep, pagerStart, viewportStart-margin)。
// 负值/未知项用 -1 表示"无约束"。
func DesiredStart(mustKeep, pagerStart, viewportStart, margin int) int {
	desired := math.MaxInt32
	if mustKeep >= 0 && mustKeep < desired {
		desired = mustKeep
	}
	if pagerStart >= 0 && pagerStart < desired {
		desired = pagerStart
	}
	if viewportStart >= 0 {
		viewport := viewportStart - margin
		if viewport < 0 {
			viewport = 0
		}
		if viewport < desired {
			desired = viewport
		}
	}
	if desired == math.MaxInt32 {
		return 0
	}
	return desired
}
