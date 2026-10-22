package ui

import (
	"context"
	"fmt"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// transcript_lazy_source.go —— 冷区回填与预取（任务 T5 / Spike-3）。
//
// 设计文档：§14.5（ensureResident / 预取 lookbehind 2 + forward 1 / in-flight
// 去重 / 失败退避 / fail-closed）。
//
// 语义边界：回填产物进入 **页面缓存**（pager/回看消费），不改变 L1 窗口的连续性；
// 窗口内 cell 的"回到可视区"重物化由 T6 的装配路径负责（复用同一 store/校验）。
// 状态机约定：回填中 = Loading（pin，禁逐出）；成功后回到 Cold（body 在缓存）；
// 连续失败达到上限后 = Unrebuildable（永久 pin，不再重试，渲染占位）。

// TranscriptPageCache 是回填 body 的有界页面缓存（按 cell 计容量，FIFO 逐出）。
type TranscriptPageCache struct {
	capacity int
	order    []scene.CellID
	bodies   map[scene.CellID]scene.TranscriptCell
	bytes    int64
}

// NewTranscriptPageCache 构造页面缓存；capacity <=0 时取 256。
func NewTranscriptPageCache(capacity int) *TranscriptPageCache {
	if capacity <= 0 {
		capacity = 256
	}
	return &TranscriptPageCache{capacity: capacity, bodies: make(map[scene.CellID]scene.TranscriptCell, capacity)}
}

// Put 写入（同一 cell 覆盖不重复占位）。
func (c *TranscriptPageCache) Put(cell scene.TranscriptCell) {
	if c.bodies == nil {
		c.bodies = make(map[scene.CellID]scene.TranscriptCell, c.capacity)
	}
	if _, exists := c.bodies[cell.ID]; !exists {
		c.order = append(c.order, cell.ID)
		c.bytes += int64(len(cell.Source)) + 64
	}
	c.bodies[cell.ID] = cell
	for len(c.order) > c.capacity {
		oldest := c.order[0]
		c.order = c.order[1:]
		if old, ok := c.bodies[oldest]; ok {
			c.bytes -= int64(len(old.Source)) + 64
			delete(c.bodies, oldest)
		}
	}
}

// Get 读取缓存。
func (c *TranscriptPageCache) Get(id scene.CellID) (scene.TranscriptCell, bool) {
	if c == nil || c.bodies == nil {
		return scene.TranscriptCell{}, false
	}
	cell, ok := c.bodies[id]
	return cell, ok
}

// Len 返回缓存条目数。
func (c *TranscriptPageCache) Len() int {
	if c == nil {
		return 0
	}
	return len(c.bodies)
}

// Bytes 返回缓存字节估算。
func (c *TranscriptPageCache) Bytes() int64 {
	if c == nil {
		return 0
	}
	return c.bytes
}

// BackfillStats 是回填器计数（映射到 residency 观测口径）。
type BackfillStats struct {
	Requests      int
	Loaded        int
	Failed        int
	Unrebuildable int
	Bytes         int64
	MaxLatencyMs  float64
}

// TranscriptBackfiller 负责冷区回填与预取。
type TranscriptBackfiller struct {
	Residency          *TranscriptResidency
	Store              CellBodyStore
	Cache              *TranscriptPageCache
	PageCells          int
	PrefetchLookbehind int
	PrefetchForward    int
	MaxAttempts        int

	pending map[scene.CellID]int
	stats   BackfillStats
}

// NewTranscriptBackfiller 构造回填器（默认页 256、lookbehind 2、forward 1、重试 3）。
func NewTranscriptBackfiller(residency *TranscriptResidency, store CellBodyStore) *TranscriptBackfiller {
	return &TranscriptBackfiller{
		Residency:          residency,
		Store:              store,
		Cache:              NewTranscriptPageCache(512),
		PageCells:          256,
		PrefetchLookbehind: 2,
		PrefetchForward:    1,
		MaxAttempts:        3,
		pending:            make(map[scene.CellID]int),
	}
}

// EnsureResident 把冷区 cell 排入回填队列（去重；返回新入队数量）。
func (b *TranscriptBackfiller) EnsureResident(ids ...scene.CellID) int {
	if b == nil || b.Residency == nil {
		return 0
	}
	enqueued := 0
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if b.Residency.StateOf(id) == Unrebuildable {
			continue // 永久 pin：不再重试
		}
		if _, exists := b.pending[id]; exists {
			continue
		}
		if _, ok := b.Cache.Get(id); ok {
			continue
		}
		b.pending[id] = 0
		b.Residency.MarkState(id, Loading)
		b.stats.Requests++
		enqueued++
	}
	return enqueued
}

// PrefetchVisible 依据可见区间规划冷区预取（lookbehind 2 页 + forward 1 页，
// 仅限冷区 [0, coldLen)）；返回新入队数量。
func (b *TranscriptBackfiller) PrefetchVisible(startSeq, endSeq int) int {
	if b == nil || b.Residency == nil {
		return 0
	}
	coldLen := b.Residency.Cold.Len()
	if coldLen == 0 {
		return 0
	}
	ranges := SeqRangesForPrefetch(startSeq, endSeq, coldLen, b.PageCells, b.PrefetchLookbehind, b.PrefetchForward)
	var ids []scene.CellID
	for _, window := range ranges {
		for seq := window.From; seq < window.To; seq++ {
			if id, ok := b.Residency.CellIDAtSeq(seq); ok {
				ids = append(ids, id)
			}
		}
	}
	return b.EnsureResident(ids...)
}

// BackfillOnce 处理一轮待回填请求（同步执行；生产路径由后台 worker 调用，
// 结果通过 actor 动作安装）。返回本轮处理数量。
func (b *TranscriptBackfiller) BackfillOnce(ctx context.Context) (int, error) {
	if b == nil || b.Residency == nil {
		return 0, fmt.Errorf("backfiller: not initialized")
	}
	if len(b.pending) == 0 {
		return 0, nil
	}
	processed := 0
	for id := range b.pending {
		attempts := b.pending[id]
		if attempts >= b.MaxAttempts {
			b.Residency.MarkState(id, Unrebuildable)
			b.stats.Unrebuildable++
			delete(b.pending, id)
			noteTranscriptResidencyRematerialize(true)
			continue
		}
		ref, ok := b.Residency.ColdRefOf(id)
		if !ok {
			// 已不在冷区（窗口内或不存在）：不再需要回填。
			b.Residency.MarkState(id, Hot)
			delete(b.pending, id)
			continue
		}
		if b.Store == nil {
			b.Residency.MarkState(id, Cold)
			b.stats.Loaded++
			delete(b.pending, id)
			continue
		}
		body, found, err := b.Store.Get(ctx, b.Residency.SessionID, id, ref.Revision)
		if err != nil || !found {
			b.pending[id] = attempts + 1
			b.stats.Failed++
			if err == nil {
				err = fmt.Errorf("backfill cell %d revision %d: miss", id, ref.Revision)
			}
			if b.pending[id] >= b.MaxAttempts {
				b.Residency.MarkState(id, Unrebuildable)
				b.stats.Unrebuildable++
				delete(b.pending, id)
				noteTranscriptResidencyRematerialize(true)
			}
			continue
		}
		decoded, err := ValidateTranscriptCellBody(body)
		if err == nil && !cellBodyMatchesColdRef(decoded, ref) {
			err = fmt.Errorf("backfill cell %d revision %d: body does not match ledger ref", id, ref.Revision)
		}
		if err != nil {
			b.pending[id] = attempts + 1
			b.stats.Failed++
			if b.pending[id] >= b.MaxAttempts {
				b.Residency.MarkState(id, Unrebuildable)
				b.stats.Unrebuildable++
				delete(b.pending, id)
				noteTranscriptResidencyRematerialize(true)
			}
			continue
		}
		b.Cache.Put(decoded)
		b.stats.Loaded++
		b.stats.Bytes += int64(len(body.Data))
		b.Residency.MarkState(id, Cold)
		delete(b.pending, id)
		noteTranscriptResidencyRematerialize(false)
		processed++
	}
	return processed, nil
}

// Stats 返回回填统计快照。
func (b *TranscriptBackfiller) Stats() BackfillStats {
	if b == nil {
		return BackfillStats{}
	}
	return b.stats
}

// cellBodyMatchesColdRef 校验解码后的 body 与冷区账本引用一致（身份 + 语义指纹）：
// 写穿内容被替换/错配时必须 fail-closed（设计 §14.5）。
func cellBodyMatchesColdRef(cell scene.TranscriptCell, ref ColdCellRef) bool {
	if cell.ID != ref.ID || cell.Revision != ref.Revision {
		return false
	}
	return CellBodySourceFingerprint(cell) == ref.SourceFP
}

// Pending 返回待回填数量（诊断用）。
func (b *TranscriptBackfiller) Pending() int {
	if b == nil {
		return 0
	}
	return len(b.pending)
}

// Reset 取消全部 in-flight（会话切换/SceneID 变化时调用）。
func (b *TranscriptBackfiller) Reset() {
	if b == nil {
		return
	}
	for id := range b.pending {
		b.Residency.MarkState(id, Cold)
	}
	b.pending = make(map[scene.CellID]int)
}

// RecordLatency 记录一次回填耗时（毫秒），用于 p95 观测。
func (b *TranscriptBackfiller) RecordLatency(ms float64) {
	if b == nil {
		return
	}
	if ms > b.stats.MaxLatencyMs {
		b.stats.MaxLatencyMs = ms
	}
}
