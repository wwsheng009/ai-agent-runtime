package commands

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// chat_runtime_scene_trim.go —— T6：bridge scene 层窗口化策略。
//
// 设计文档：§14.1(L0)「renderScene 不得无限累积」；写穿在裁剪之前完成，
// 失败即 fail-closed（把尚未写穿的 cell 恢复回场景，绝不丢数据）。
//
// 默认策略为 nil（关闭）：`applyChangeSet` 中的调用变成空操作，行为与引入前
// 逐字节一致。开启与阈值由 T8 的配置接线注入；本层只负责语义与不变量。

// bridgeSceneTrimPolicy 是 scene 层裁剪策略与计数。
type bridgeSceneTrimPolicy struct {
	Enabled bool
	// KeepLastCells 是场景保留的尾部 cell 数（工作集窗口）。
	KeepLastCells int
	// TrimBatch 是单次裁剪上限（避免长临界区）。
	TrimBatch int
	// RowsPerGen 是写穿快照的行数口径；scene 层不知道真实布局行数，
	// 由 T8 接线时按当前 layout generation 传入（默认 0 = 未知）。
	RowsPerGen int32
	// Generation 是写穿快照的 layout generation（默认 1）。
	Generation uint64
	// SessionID/Store 是写穿目标（Store 为 nil 时策略整体不生效）。
	SessionID string
	Store     ui.CellBodyStore

	TrimmedCells  int
	PutErrors     int
	RestoreErrors int
	LastError     string
	// T8b-5 会计口径（§15.2/§15.3）：写穿字节/行数、批次、恢复与最后原因。
	TrimBatches   int
	TrimmedBytes  int64
	TrimmedRows   int64
	RestoredCells int
	LastReason    string
	GenerationMin uint64
	GenerationMax uint64
	// trimDeliveryFactsOverride 仅供测试注入交付事实（生产由 UI 账本提供）。
	trimDeliveryFactsOverride *deliveryFacts
	// Residency 是冷区账本（T10：pager 冷区读回 + status index 分区）。
	// nil = 该 bridge 未启用账本（保持既有行为）。
	Residency *ui.TranscriptResidency
}

// sceneTrimAccounting 是裁剪路径的会计快照（T8b-5）。ok=false 表示策略未安装。
func (b *chatRuntimeEventBridge) sceneTrimAccounting() (sceneTrimAccounting, bool) {
	if b == nil || b.sceneTrimPolicy == nil {
		return sceneTrimAccounting{}, false
	}
	policy := b.sceneTrimPolicy
	return sceneTrimAccounting{
		Batches:       policy.TrimBatches,
		TrimmedCells:  policy.TrimmedCells,
		TrimmedBytes:  policy.TrimmedBytes,
		TrimmedRows:   policy.TrimmedRows,
		RestoredCells: policy.RestoredCells,
		PutErrors:     policy.PutErrors,
		RestoreErrors: policy.RestoreErrors,
		LastReason:    policy.LastReason,
		LastError:     policy.LastError,
		GenerationMin: policy.GenerationMin,
		GenerationMax: policy.GenerationMax,
	}, true
}

// sceneTrimAccounting 的进程内可见形态（诊断/状态投影用）。
type sceneTrimAccounting struct {
	Batches       int
	TrimmedCells  int
	TrimmedBytes  int64
	TrimmedRows   int64
	RestoredCells int
	PutErrors     int
	RestoreErrors int
	LastReason    string
	LastError     string
	GenerationMin uint64
	GenerationMax uint64
}

// deliveryFacts 是逐出守卫所需的交付事实（§14.3 H3）。
//
// 三态语义（重要）：
//   - Known=false：没有 UI 账本可用（无 uiActor / 未启用统一渲染器 / 测试直连
//     bridge）。此时**无法证明** cell 未交付，守卫不介入——保持既有裁剪行为
//     （否则所有非 UI 装配路径都会被误判为"未交付"而永不裁剪）。
//   - Known=true：按 CellID 判定；账本里没有该 cell（尚未铸提交）即为未交付。
type deliveryFacts struct {
	Known     bool
	Delivered map[scene.CellID]bool
}

func (d deliveryFacts) delivered(id scene.CellID) bool {
	if !d.Known {
		return true // 不可判定 → 不阻断（fail-open 仅限"无账本"这一装配形态）
	}
	return d.Delivered[id]
}

// deliveryFacts 采集交付事实。账本不可用时 Known=false。
func (b *chatRuntimeEventBridge) deliveryFacts() deliveryFacts {
	if b != nil && b.sceneTrimPolicy != nil && b.sceneTrimPolicy.trimDeliveryFactsOverride != nil {
		return *b.sceneTrimPolicy.trimDeliveryFactsOverride
	}
	if b == nil || b.session == nil || b.session.Interaction == nil {
		return deliveryFacts{}
	}
	actor := b.session.Interaction.currentUIActor()
	if actor == nil {
		return deliveryFacts{}
	}
	// R5：存在"可能已部分写入 / 交付不可证明 / 失效中"的条目时，整轮禁止逐出。
	// 这些行可能已经出现在屏幕上、也可能没有；此时逐出会让恢复路径无法判断
	// 该补写还是跳过（partial-zero-write 互锁）。
	if blocked, reason := actor.DeliveryBlockers(); blocked {
		if b.sceneTrimPolicy != nil && reason != "" {
			b.sceneTrimPolicy.LastReason = reason
		}
		return deliveryFacts{Known: true, Delivered: map[scene.CellID]bool{}}
	}
	// 零 payload 克隆的账本投影（Entries() 会逐条复制 Commit.Lines，交付期
	// 账本可达 8 万条 —— 每次裁剪都克隆一遍会把 UI 锁按秒级占用）。
	ids, known := actor.DeliveredCellIDs()
	if !known {
		// 账本存在但为空：可能是首帧尚未提交，也可能是该装配路径不产提交。
		// 无任何交付记录时无法区分，按"不可判定"处理（不阻断既有行为）。
		return deliveryFacts{}
	}
	delivered := make(map[scene.CellID]bool, len(ids))
	for id := range ids {
		delivered[id] = true
	}
	return deliveryFacts{Known: true, Delivered: delivered}
}

// SetSceneTrimPolicy 安装/替换策略（nil 关闭）。
func (b *chatRuntimeEventBridge) SetSceneTrimPolicy(policy *bridgeSceneTrimPolicy) {
	if b == nil {
		return
	}
	b.sceneTrimPolicy = policy
}

// SceneTrimStats 返回裁剪计数（诊断/测试用）。
func (b *chatRuntimeEventBridge) SceneTrimStats() (trimmed, putErrors, restoreErrors int, lastError string) {
	if b == nil || b.sceneTrimPolicy == nil {
		return 0, 0, 0, ""
	}
	policy := b.sceneTrimPolicy
	return policy.TrimmedCells, policy.PutErrors, policy.RestoreErrors, policy.LastError
}

// applySceneTrimPolicyLocked 执行一轮裁剪；调用方必须持 sceneMu。
// 返回本轮实际裁剪的 cell 数。
func (b *chatRuntimeEventBridge) applySceneTrimPolicyLocked() int {
	if b == nil || b.renderScene == nil {
		return 0
	}
	b.ensureSceneTrimPolicy()
	policy := b.sceneTrimPolicy
	if policy == nil || !policy.Enabled || policy.Store == nil {
		return 0
	}
	keep := policy.KeepLastCells
	if keep <= 0 {
		keep = 2048
	}
	batch := policy.TrimBatch
	if batch <= 0 {
		batch = 256
	}
	generation := policy.Generation
	if generation == 0 {
		generation = 1
	}
	total := b.renderScene.Len()
	if total <= keep {
		return 0
	}
	count := total - keep
	if count > batch {
		count = batch
	}

	// §14.3 逐出守卫（H3）：只有「已结算且已交付到原生 scrollback」的 cell 才能
	// 离开驻留窗口。此前这里只判断 total > keep —— 当 residency 在**装载前**
	// 启用且窗口很小时（实测 keep=1），尚未交付的 cell 会被写穿并裁掉，终端里
	// 一行都留不下（数据在 store 里，但用户看到"历史消失"）。
	//
	// 交付事实来自 UI 侧账本：按 CellID 汇总 Delivered 计数；未在账本中出现过的
	// cell（尚未铸提交）一律不可逐出。取连续可逐出前缀，遇到第一个不可逐出 cell
	// 立即停止（前缀语义，INV-6）。
	if facts := b.deliveryFacts(); facts.Known {
		cells := b.renderScene.Cells()
		evictable := 0
		for evictable < count {
			if evictable >= len(cells) || cells[evictable] == nil || !facts.delivered(cells[evictable].ID) {
				break
			}
			evictable++
		}
		if evictable == 0 {
			// 前缀仍有未交付 cell：本轮不裁剪（等交付推进后下一轮再试）。
			policy.LastReason = "unacked_rows"
			return 0
		}
		count = evictable
	}
	trimmed, err := b.renderScene.TrimPrefix(count)
	if err != nil {
		policy.LastError = err.Error()
		return 0
	}
	persisted := 0
	persistedBytes := int64(0)
	putAttempts := 0
	for _, cell := range trimmed {
		body, err := ui.MarshalTranscriptCellBody(*cell, policy.RowsPerGen, generation)
		if err != nil {
			policy.LastError = err.Error()
			break
		}
		putAttempts++
		if err := policy.Store.Put(context.Background(), policy.SessionID, body); err != nil {
			policy.PutErrors++
			policy.LastError = err.Error()
			break
		}
		persisted++
		persistedBytes += int64(len(body.Data))
	}
	ui.NoteTranscriptResidencyWriteThrough(putAttempts, putAttempts-persisted)
	if persisted < len(trimmed) {
		// fail-closed：未写穿的后缀恢复回场景头部（顺序不变），已写穿部分保持裁剪。
		restored := trimmed[persisted:]
		if _, err := b.renderScene.RestorePrefix(restored); err != nil {
			policy.RestoreErrors++
			if policy.LastError == "" {
				policy.LastError = err.Error()
			}
		}
		policy.RestoredCells += len(restored)
		policy.LastReason = "write_through_failed_restored"
	}
	if persisted > 0 {
		// 逐出路径固化真实行数（§14.2 RowsAtGen）：只统计已写穿的前缀。
		batch := trimmed[:persisted]
		// 逐 cell 行数（含 leading gap 归属后继 cell 的规则）：账本按 cell 记账，
		// 不能拿整批行数均摊。
		rowsPerCell := make([]int32, len(batch))
		gapsPerCell := make([]int8, len(batch))
		persistedRows := 0
		var previous *scene.TranscriptCell
		for index, cell := range batch {
			if cell == nil {
				continue
			}
			gap := 0
			if previous != nil && previous.ID != cell.ID {
				gap = int(boundary.ResolveGap(previous.BoundaryMeta(), cell.BoundaryMeta()))
			}
			content := len(scene.LayoutTranscript([]*scene.TranscriptCell{cell}, generation))
			rowsPerCell[index] = int32(content + gap)
			gapsPerCell[index] = int8(gap)
			persistedRows += content + gap
			previous = cell
		}
		policy.TrimBatches++
		policy.TrimmedCells += persisted
		policy.TrimmedBytes += persistedBytes
		policy.TrimmedRows += int64(persistedRows)
		if policy.GenerationMin == 0 || generation < policy.GenerationMin {
			policy.GenerationMin = generation
		}
		if generation > policy.GenerationMax {
			policy.GenerationMax = generation
		}
		if persisted == len(trimmed) {
			policy.LastReason = "prefix_window_exceeded"
		}
		ui.NoteTranscriptResidencyEviction(persisted)

		// T10（A）：把同一批写穿成功的 cell 记入冷区账本，供 pager 冷区读回
		// 与 status 的 index 分区（prefix_fence / fold_target）使用。
		// 记账失败不影响已完成的裁剪（正文已安全落盘），只记录错误。
		if policy.Residency != nil {
			cells := make([]scene.TranscriptCell, 0, len(batch))
			for _, cell := range batch {
				if cell != nil {
					cells = append(cells, *cell)
				}
			}
			if err := policy.Residency.AppendEvicted(cells, rowsPerCell[:len(cells)], gapsPerCell[:len(cells)], generation); err != nil {
				policy.LastError = err.Error()
			}
		}
	}
	return persisted
}

// ensureSceneTrimPolicy 在 residency 运行时启用时安装默认裁剪策略（T8b-1）。
// 仅在策略为空、运行时启用且写穿 store 可用、会话 ID 可解析时安装一次；
// 否则保持不裁剪（fail-closed）。
func (b *chatRuntimeEventBridge) ensureSceneTrimPolicy() {
	if b == nil || b.sceneTrimPolicy != nil || b.session == nil || b.session.RuntimeSession == nil {
		return
	}
	runtime := currentTranscriptResidencyRuntime()
	if runtime == nil || !runtime.Options.Enabled || runtime.Store == nil {
		return
	}
	// §15.1 保守档：`evictPrefix=false`（默认）表示"只窗口化、不逐出"。
	// 若在这里也装上裁剪策略，enabled=true 就会静默打开逐出——与配置语义不符，
	// 也让灰度开关失效（T10 的 canary 正是靠它分批放量）。
	if !runtime.Options.EvictPrefix {
		return
	}
	sessionID := strings.TrimSpace(b.session.RuntimeSession.ID)
	if sessionID == "" {
		return
	}
	keep := runtime.Options.MaxResidentCells
	if keep <= 0 {
		keep = 512
	}
	batch := keep / 2
	if batch < 32 {
		batch = 32
	}
	b.sceneTrimPolicy = &bridgeSceneTrimPolicy{
		Enabled:       true,
		KeepLastCells: keep,
		TrimBatch:     batch,
		RowsPerGen:    0, // 真实行数由 residency 账本在逐出路径固化
		Generation:    1,
		SessionID:     sessionID,
		Store:         runtime.Store,
		// 冷区账本与裁剪策略同源（同一 store/会话）：pager 冷区读回与 status
		// 的 index 分区都以它为准。
		Residency: ui.NewTranscriptResidency(runtime.Policy, runtime.Store, sessionID),
	}
}

// sceneTrimRestore 暴露给恢复路径（reconcile/回填需要完整视图时调用）：
// 把此前写穿并裁剪的 cell 依序放回场景头部。
func (b *chatRuntimeEventBridge) sceneTrimRestore(cells []*scene.TranscriptCell) (int, error) {
	if b == nil || b.renderScene == nil {
		return 0, nil
	}
	b.sceneMu.Lock()
	defer b.sceneMu.Unlock()
	return b.renderScene.RestorePrefix(cells)
}
