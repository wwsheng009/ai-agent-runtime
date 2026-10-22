package ui

import (
	"sync"
	"sync/atomic"
	"time"
)

// transcript_residency.go —— C 档「transcript 驻留降级」的开关与观测骨架（任务 T0）。
//
// 设计文档：docs/plan/aicli-transcript-residency-and-lazy-history-design-20261020.md
// 权威规范：§14（四层驻留/双代账本/写穿物化/流式装载）、§15（配置与观测）
//
// 本文件在 T0 阶段只提供：
//  1. 启动期选项（默认全部关闭 → 任何行为路径都不读取它，故与引入前逐字节一致）；
//  2. 计数器与统计快照（/debug/chat/status#app_state.residency 的数据源）。
//
// T3–T5 会在此之上填充真实的驻留/索引/逐出/回填计数；在此之前快照返回零值，
// 由 Note* 入口保留计数能力（当前无调用点）。

// TranscriptResidencyOptions 是驻留降级的启动期配置（§15.1）。
type TranscriptResidencyOptions struct {
	Enabled            bool    `json:"enabled"`
	EvictPrefix        bool    `json:"evict_prefix"`
	MaxResidentCells   int     `json:"max_resident_cells"`
	MaxResidentRows    int     `json:"max_resident_rows"`
	MaxResidentMiB     int     `json:"max_resident_mib"`
	ViewportMarginRows int     `json:"viewport_margin_rows"`
	HysteresisCells    int     `json:"hysteresis_cells"`
	EvictHighWater     float64 `json:"evict_high_water"`
	PageCells          int     `json:"page_cells"`
	PageMiB            int     `json:"page_mib"`
	PrefetchPages      int     `json:"prefetch_pages"`
	BodyStore          string  `json:"body_store"`
}

// 默认值来自设计文档 §15.1（由实测推导：21.8 行/cell、≈8.9 KB/cell 渲染）。
const (
	residencyDefaultMaxResidentCells   = 512
	residencyDefaultMaxResidentRows    = 4096
	residencyDefaultMaxResidentMiB     = 24
	residencyDefaultViewportMarginRows = 2
	residencyDefaultHysteresisCells    = 128
	residencyDefaultEvictHighWater     = 1.25
	residencyDefaultPageCells          = 256
	residencyDefaultPageMiB            = 2
	residencyDefaultPrefetchPages      = 2
	residencyDefaultBodyStore          = "sqlite"

	residencyStatsNote = "T8b-5：计数来自真实路径（bridge 写穿裁剪 / 回填 / sqlite 写穿 store）；" +
		"resident/cold/memory 分区由端点现场补充；当前不可归因的子系统列在 unavailable。"
)

// DefaultTranscriptResidencyOptions 返回默认选项（T10-4 起：总开关默认开启）。
//
// 分层默认（§15.1）：
//   - Enabled=true：窗口化布局/帧（始终生效）、§15.1 缓存预算（16/8/8 MiB）、
//     调试端点缺省窗口都属于"零风险"档，默认打开；
//   - EvictPrefix=false：**逐出仍是独立灰度开关**。逐出会真正把数据移出驻留
//     窗口（虽已写穿到 sqlite 且可读回），它需要 canary 放量，不应随总开关
//     一起静默生效（否则"关掉逐出"这个保守档就不存在了）。
//
// 已验证的默认开启收益：帧合成分配 -99.4%（5000 cells）、调试端点只算窗口、
// 三类布局缓存不再无界增长；这些都不改变 transcript 的输出字节。
func DefaultTranscriptResidencyOptions() TranscriptResidencyOptions {
	return TranscriptResidencyOptions{
		Enabled:            true,
		EvictPrefix:        false,
		MaxResidentCells:   residencyDefaultMaxResidentCells,
		MaxResidentRows:    residencyDefaultMaxResidentRows,
		MaxResidentMiB:     residencyDefaultMaxResidentMiB,
		ViewportMarginRows: residencyDefaultViewportMarginRows,
		HysteresisCells:    residencyDefaultHysteresisCells,
		EvictHighWater:     residencyDefaultEvictHighWater,
		PageCells:          residencyDefaultPageCells,
		PageMiB:            residencyDefaultPageMiB,
		PrefetchPages:      residencyDefaultPrefetchPages,
		BodyStore:          residencyDefaultBodyStore,
	}
}

// DisabledTranscriptResidencyOptions 返回**显式关闭**的选项（回退/测试用）。
// 与 DefaultTranscriptResidencyOptions 区分开，避免"默认开启"之后测试清理
// 或回退路径误把总开关又打开（那会让后续用例继承非默认行为）。
func DisabledTranscriptResidencyOptions() TranscriptResidencyOptions {
	opts := DefaultTranscriptResidencyOptions()
	opts.Enabled = false
	opts.EvictPrefix = false
	return opts
}

// clampTranscriptResidencyOptions 把零值/非法值收敛到默认值：配置层零值表示
// 「未配置」，负值视为配置错误（与 SessionsConfig 的宽容口径一致：不报错、取默认）。
func clampTranscriptResidencyOptions(opts TranscriptResidencyOptions) TranscriptResidencyOptions {
	defaults := DefaultTranscriptResidencyOptions()
	if opts.MaxResidentCells <= 0 {
		opts.MaxResidentCells = defaults.MaxResidentCells
	}
	if opts.MaxResidentRows <= 0 {
		opts.MaxResidentRows = defaults.MaxResidentRows
	}
	if opts.MaxResidentMiB <= 0 {
		opts.MaxResidentMiB = defaults.MaxResidentMiB
	}
	if opts.ViewportMarginRows <= 0 {
		opts.ViewportMarginRows = defaults.ViewportMarginRows
	}
	if opts.HysteresisCells <= 0 {
		opts.HysteresisCells = defaults.HysteresisCells
	}
	if opts.EvictHighWater < 1 {
		opts.EvictHighWater = defaults.EvictHighWater
	}
	if opts.PageCells <= 0 {
		opts.PageCells = defaults.PageCells
	}
	if opts.PageMiB <= 0 {
		opts.PageMiB = defaults.PageMiB
	}
	if opts.PrefetchPages <= 0 {
		opts.PrefetchPages = defaults.PrefetchPages
	}
	if opts.BodyStore == "" {
		opts.BodyStore = defaults.BodyStore
	}
	return opts
}

var (
	residencyOptionsMu sync.RWMutex
	residencyOptions   = DefaultTranscriptResidencyOptions()
)

// ConfigureTranscriptResidency 应用启动期选项（幂等；进程内全局唯一）。
func ConfigureTranscriptResidency(opts TranscriptResidencyOptions) {
	normalized := clampTranscriptResidencyOptions(opts)
	residencyOptionsMu.Lock()
	residencyOptions = normalized
	residencyOptionsMu.Unlock()
}

// TranscriptResidencyOptionsSnapshot 返回当前生效的选项（值拷贝）。
func TranscriptResidencyOptionsSnapshot() TranscriptResidencyOptions {
	residencyOptionsMu.RLock()
	defer residencyOptionsMu.RUnlock()
	return residencyOptions
}

// TranscriptResidencyEnabled 报告总开关状态（后续任务的行为路径以此为准）。
func TranscriptResidencyEnabled() bool {
	residencyOptionsMu.RLock()
	defer residencyOptionsMu.RUnlock()
	return residencyOptions.Enabled
}

// TranscriptResidencyCounters 是 T3–T5 将填充的计数器；T0 全部为零。
type TranscriptResidencyCounters struct {
	EvictedCells          uint64 `json:"evicted_cells"`
	EvictionsPinned       uint64 `json:"evictions_pinned"`
	RematerializedCells   uint64 `json:"rematerialized_cells"`
	RematerializeFailures uint64 `json:"rematerialize_failures"`
	RematerializeBytes    int64  `json:"rematerialize_bytes"`
	RematerializeMaxMs    uint64 `json:"rematerialize_max_ms"`
	RematerializeInflight int64  `json:"rematerialize_inflight"`
	BodyStorePuts         uint64 `json:"body_store_puts"`
	BodyStorePutErrors    uint64 `json:"body_store_put_errors"`
	BodyStoreGets         uint64 `json:"body_store_gets"`
	BodyStoreGetMisses    uint64 `json:"body_store_get_misses"`
}

var (
	residencyEvictedCells          atomic.Uint64
	residencyEvictionsPinned       atomic.Uint64
	residencyRematerializedCells   atomic.Uint64
	residencyRematerializeFailures atomic.Uint64
	residencyRematerializeBytes    atomic.Int64
	residencyRematerializeMaxMs    atomic.Uint64
	residencyRematerializeInflight atomic.Int64
	residencyBodyStorePuts         atomic.Uint64
	residencyBodyStorePutErrors    atomic.Uint64
	residencyBodyStoreGets         atomic.Uint64
	residencyBodyStoreGetMisses    atomic.Uint64
)

// TranscriptResidencyCountersSnapshot 返回计数器快照。
func TranscriptResidencyCountersSnapshot() TranscriptResidencyCounters {
	return TranscriptResidencyCounters{
		EvictedCells:          residencyEvictedCells.Load(),
		EvictionsPinned:       residencyEvictionsPinned.Load(),
		RematerializedCells:   residencyRematerializedCells.Load(),
		RematerializeFailures: residencyRematerializeFailures.Load(),
		RematerializeBytes:    residencyRematerializeBytes.Load(),
		RematerializeMaxMs:    residencyRematerializeMaxMs.Load(),
		RematerializeInflight: residencyRematerializeInflight.Load(),
		BodyStorePuts:         residencyBodyStorePuts.Load(),
		BodyStorePutErrors:    residencyBodyStorePutErrors.Load(),
		BodyStoreGets:         residencyBodyStoreGets.Load(),
		BodyStoreGetMisses:    residencyBodyStoreGetMisses.Load(),
	}
}

// 以下记录入口由 T3–T5 的逐出/回填路径调用；T0 无调用点，接口先固定，
// 便于观测口径在实现期不再变化（设计文档 §15.2）。
func noteTranscriptResidencyEviction(pinned bool) {
	if pinned {
		residencyEvictionsPinned.Add(1)
		return
	}
	residencyEvictedCells.Add(1)
}

func noteTranscriptResidencyRematerialize(failed bool) {
	if failed {
		residencyRematerializeFailures.Add(1)
		return
	}
	residencyRematerializedCells.Add(1)
}

func noteTranscriptResidencyBodyStorePut(failed bool) {
	residencyBodyStorePuts.Add(1)
	if failed {
		residencyBodyStorePutErrors.Add(1)
	}
}

func noteTranscriptResidencyBodyStoreGet(missed bool) {
	residencyBodyStoreGets.Add(1)
	if missed {
		residencyBodyStoreGetMisses.Add(1)
	}
}

// ---------------------------------------------------------------------------
// T8b-5：导出记录入口（commands 的 bridge 写穿裁剪路径调用）+ 真实快照分区。

// NoteTranscriptResidencyEviction 记录 cells 个 cell 的逐出（批量口径）。
func NoteTranscriptResidencyEviction(cells int) {
	if cells > 0 {
		residencyEvictedCells.Add(uint64(cells))
	}
}

// NoteTranscriptResidencyEvictionPinned 记录被 pin 拒绝的候选逐出。
func NoteTranscriptResidencyEvictionPinned(cells int) {
	if cells > 0 {
		residencyEvictionsPinned.Add(uint64(cells))
	}
}

// NoteTranscriptResidencyWriteThrough 记录写穿尝试与失败（attempts 含失败次数）。
func NoteTranscriptResidencyWriteThrough(attempts, errors int) {
	if attempts <= 0 && errors <= 0 {
		return
	}
	if attempts > 0 {
		residencyBodyStorePuts.Add(uint64(attempts))
	}
	if errors > 0 {
		residencyBodyStorePutErrors.Add(uint64(errors))
	}
}

// NoteTranscriptResidencyRematerialize 记录重物化结果（cells 成功、failures 失败、
// bytes 回读字节、elapsed 耗时；max_ms 取历史最大值）。
func NoteTranscriptResidencyRematerialize(cells, failures int, bytes int64, elapsed time.Duration) {
	if cells > 0 {
		residencyRematerializedCells.Add(uint64(cells))
	}
	if failures > 0 {
		residencyRematerializeFailures.Add(uint64(failures))
	}
	if bytes > 0 {
		residencyRematerializeBytes.Add(bytes)
	}
	if elapsed > 0 {
		ms := uint64(elapsed.Milliseconds())
		for {
			prev := residencyRematerializeMaxMs.Load()
			if ms <= prev || residencyRematerializeMaxMs.CompareAndSwap(prev, ms) {
				break
			}
		}
	}
}

// NoteTranscriptResidencyRematerializeStart / Done 维护在飞计数（inflight），
// 供 §15.2 的 rematerialize.inflight 报告（恒 >= 0）。
func NoteTranscriptResidencyRematerializeStart() {
	residencyRematerializeInflight.Add(1)
}

// NoteTranscriptResidencyRematerializeDone 与 Start 配对。
func NoteTranscriptResidencyRematerializeDone() {
	if residencyRematerializeInflight.Add(-1) < 0 {
		residencyRematerializeInflight.Store(0)
	}
}

// TranscriptResidencyStats 是 /debug/chat/status#app_state.residency 的投影
// （§15.2）。counters 派生的分区（evictions/rematerialize/body_store）在本包
// 填充；resident/cold/index/memory 由端点现场补充（bridge/store/session 口径）。
type TranscriptResidencyStats struct {
	Enabled     bool                        `json:"enabled"`
	EvictPrefix bool                        `json:"evict_prefix"`
	Options     TranscriptResidencyOptions  `json:"options"`
	Counters    TranscriptResidencyCounters `json:"counters"`
	// LayoutCache 是当前生效的布局缓存预算覆盖（T8 §15.1；零值 = 未覆盖）。
	LayoutCache LayoutCacheBudgets `json:"layout_cache_budgets"`
	// §15.2 分区（T8b-5）：缺席 = 该分区当前不可归因（见 Unavailable）。
	Evictions     *TranscriptResidencyEvictionStats      `json:"evictions,omitempty"`
	Rematerialize *TranscriptResidencyRematerializeStats `json:"rematerialize,omitempty"`
	BodyStore     *TranscriptResidencyBodyStoreStats     `json:"body_store,omitempty"`
	Resident      *TranscriptResidencyResidentStats      `json:"resident,omitempty"`
	Cold          *TranscriptResidencyColdStats          `json:"cold,omitempty"`
	Index         *TranscriptResidencyIndexStats         `json:"index,omitempty"`
	Memory        *TranscriptResidencyMemoryStats        `json:"memory,omitempty"`
	// Sources 记录本次快照的数据来源（消费方据此判断分区可信度）。
	Sources []string `json:"sources,omitempty"`
	// Unavailable 记录当前不可归因的分区与原因（绝不静默省略）。
	Unavailable []string `json:"unavailable,omitempty"`
	Note        string   `json:"note,omitempty"`
}

// TranscriptResidencyStatsSnapshot 采集驻留降级统计快照。
func TranscriptResidencyStatsSnapshot() TranscriptResidencyStats {
	opts := TranscriptResidencyOptionsSnapshot()
	counters := TranscriptResidencyCountersSnapshot()
	stats := TranscriptResidencyStats{
		Enabled:     opts.Enabled,
		EvictPrefix: opts.EvictPrefix && opts.Enabled,
		Options:     opts,
		Counters:    counters,
		LayoutCache: LayoutCacheBudgetsSnapshot(),
		Evictions: &TranscriptResidencyEvictionStats{
			Total: counters.EvictedCells,
		},
		Rematerialize: &TranscriptResidencyRematerializeStats{
			Total:      counters.RematerializedCells,
			MaxMs:      counters.RematerializeMaxMs,
			Failures:   counters.RematerializeFailures,
			BytesTotal: counters.RematerializeBytes,
			Inflight:   counters.RematerializeInflight,
		},
		BodyStore: &TranscriptResidencyBodyStoreStats{
			Puts:      counters.BodyStorePuts,
			Gets:      counters.BodyStoreGets,
			PutErrors: counters.BodyStorePutErrors,
			GetMisses: counters.BodyStoreGetMisses,
		},
		Sources: []string{"ui.counters"},
		Note:    residencyStatsNote,
	}
	if counters.EvictionsPinned > 0 {
		stats.Evictions.PinnedByReason = map[string]uint64{"policy_pinned": counters.EvictionsPinned}
	}
	return stats
}
