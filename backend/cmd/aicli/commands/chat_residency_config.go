package commands

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// applyTranscriptResidencyConfig 把 runtime 配置映射到 ui 驻留选项（任务 T0/T8b）。
//
// 零值语义（与 internal/config 的既有约定一致）：配置零值 = 未配置 → 保留 ui 默认；
// 非法值不在配置层报错，统一由 ui.ConfigureTranscriptResidency 夹紧（配置层
// 不复制第二套默认值，避免两处默认漂移）。
//
// T8b：
//   - residency 启用且未显式配置布局缓存预算时，套用 §15.1 的窗口化后默认（16/8/8 MiB）；
//   - 按选项安装进程级 residency 运行时（写穿 store；失败 fail-closed）。
//
// 设计文档：docs/plan/aicli-transcript-residency-and-lazy-history-design-20261020.md §15.1
func applyTranscriptResidencyConfig(cfg *runtimecfg.RuntimeConfig, sessionDir string) {
	opts := ui.DefaultTranscriptResidencyOptions()
	budgets := ui.LayoutCacheBudgets{}
	if cfg != nil {
		r := cfg.UI.Residency
		// 指针语义：nil = 未配置 → 保留默认（T10-4 起 Enabled 默认 true）；
		// 显式 false 必须能关掉（回退开关），显式 true 才打开逐出。
		if r.Enabled != nil {
			opts.Enabled = *r.Enabled
		}
		if r.EvictPrefix != nil {
			opts.EvictPrefix = *r.EvictPrefix
		}
		if r.MaxResidentCells > 0 {
			opts.MaxResidentCells = r.MaxResidentCells
		}
		if r.MaxResidentRows > 0 {
			opts.MaxResidentRows = r.MaxResidentRows
		}
		if r.MaxResidentMiB > 0 {
			opts.MaxResidentMiB = r.MaxResidentMiB
		}
		if r.ViewportMarginRows > 0 {
			opts.ViewportMarginRows = r.ViewportMarginRows
		}
		if r.HysteresisCells > 0 {
			opts.HysteresisCells = r.HysteresisCells
		}
		if r.EvictHighWater >= 1 {
			opts.EvictHighWater = r.EvictHighWater
		}
		if r.PageCells > 0 {
			opts.PageCells = r.PageCells
		}
		if r.PageMiB > 0 {
			opts.PageMiB = r.PageMiB
		}
		if r.PrefetchPages > 0 {
			opts.PrefetchPages = r.PrefetchPages
		}
		if store := strings.TrimSpace(r.BodyStore); store != "" {
			opts.BodyStore = store
		}
		budgets = ui.LayoutCacheBudgets{
			CellRowsMiB:     cfg.UI.LayoutCache.CellRowsMiB,
			HistoryPlanMiB:  cfg.UI.LayoutCache.HistoryPlanMiB,
			FoldOmissionMiB: cfg.UI.LayoutCache.FoldOmissionMiB,
		}
	}
	ui.ConfigureTranscriptResidency(opts)

	// 布局缓存预算（§15.1）：
	//   - 显式配置（>0）→ 覆盖；
	//   - residency 启用且未配置 → 套用窗口化后的收敛默认（16/8/8 MiB）；
	//   - 其余情况 → 保持现状（默认行为不变）。
	if budgets != (ui.LayoutCacheBudgets{}) {
		ui.ApplyLayoutCacheBudgets(budgets)
	} else if opts.Enabled {
		ui.ApplyLayoutCacheBudgets(ui.LayoutCacheBudgets{
			CellRowsMiB:     16,
			HistoryPlanMiB:  8,
			FoldOmissionMiB: 8,
		})
	} else {
		// 回退（enabled=false 且未显式配置预算）：必须清掉此前套用的 §15.1
		// 默认，否则"关掉开关"只停用逐出、仍保留 16/8/8 的收紧预算——
		// 回退不彻底（T10 回退演练实测）。
		ui.ResetLayoutCacheBudgets()
	}

	installTranscriptResidencyRuntime(opts, sessionDir)
}
