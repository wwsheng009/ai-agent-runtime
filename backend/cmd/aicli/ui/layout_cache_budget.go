package ui

import "sync"

// layout_cache_budget.go —— T8：三类布局缓存预算的可配置化（设计文档 §15.1/§4.6）。
//
// 语义：
//   - 零值/负数 = 不覆盖（保持当前预算；负数按 0 处理）；
//   - 正数（MiB）= 覆盖对应缓存的**字节预算**（条目上限沿用既有常量）；
//   - 应用 = 锁内就地 setBudget + 立即逐出（无指针替换，无数据竞争）；
//   - ResetLayoutCacheBudgets 恢复出厂默认（现状 64/64/64 MiB），供测试与回退。
//
// 旧默认（64/64/64 MiB）是"缓存必须覆盖全工作集"时代的产物；C 档窗口化后
// 工作集=窗口，配置层可降至 16/8/8 MiB（§15.1），本文件只提供机制。

// LayoutCacheBudgets 是三类布局缓存的字节预算（MiB；0 = 不覆盖）。
type LayoutCacheBudgets struct {
	CellRowsMiB     int `json:"cell_rows_mib"`
	HistoryPlanMiB  int `json:"history_plan_mib"`
	FoldOmissionMiB int `json:"fold_omission_mib"`
}

const (
	defaultCellRowsMiB     = cellRowsCacheMaxBytes / (1 << 20)
	defaultHistoryPlanMiB  = historyPlanCacheMaxBytes / (1 << 20)
	defaultFoldOmissionMiB = foldOmissionMemoMaxBytes / (1 << 20)
)

var (
	layoutCacheBudgetsMu sync.RWMutex
	layoutCacheBudgets   LayoutCacheBudgets
)

func normalizeLayoutCacheBudgets(b LayoutCacheBudgets) LayoutCacheBudgets {
	if b.CellRowsMiB < 0 {
		b.CellRowsMiB = 0
	}
	if b.HistoryPlanMiB < 0 {
		b.HistoryPlanMiB = 0
	}
	if b.FoldOmissionMiB < 0 {
		b.FoldOmissionMiB = 0
	}
	return b
}

// ApplyLayoutCacheBudgets 应用覆盖并返回回收的估算字节合计。
func ApplyLayoutCacheBudgets(b LayoutCacheBudgets) int64 {
	normalized := normalizeLayoutCacheBudgets(b)
	layoutCacheBudgetsMu.Lock()
	layoutCacheBudgets = normalized
	layoutCacheBudgetsMu.Unlock()
	return applyLayoutCacheBudgetsToCaches(normalized)
}

// ResetLayoutCacheBudgets 恢复默认预算，返回回收的估算字节合计。
func ResetLayoutCacheBudgets() int64 {
	layoutCacheBudgetsMu.Lock()
	layoutCacheBudgets = LayoutCacheBudgets{}
	layoutCacheBudgetsMu.Unlock()
	return applyLayoutCacheBudgetsToCaches(LayoutCacheBudgets{
		CellRowsMiB:     defaultCellRowsMiB,
		HistoryPlanMiB:  defaultHistoryPlanMiB,
		FoldOmissionMiB: defaultFoldOmissionMiB,
	})
}

func applyLayoutCacheBudgetsToCaches(b LayoutCacheBudgets) int64 {
	var reclaimed int64
	if b.CellRowsMiB > 0 {
		reclaimed += int64(sharedCellRows.setBudgetBytes(b.CellRowsMiB << 20))
	}
	if b.HistoryPlanMiB > 0 {
		reclaimed += int64(sharedHistoryPlan.setBudgetBytes(b.HistoryPlanMiB << 20))
	}
	if b.FoldOmissionMiB > 0 {
		reclaimed += int64(sharedFoldOmissions.setBudgetBytes(b.FoldOmissionMiB << 20))
	}
	return reclaimed
}

// LayoutCacheBudgetsSnapshot 返回当前覆盖值（零值 = 未覆盖）。
func LayoutCacheBudgetsSnapshot() LayoutCacheBudgets {
	layoutCacheBudgetsMu.RLock()
	defer layoutCacheBudgetsMu.RUnlock()
	return layoutCacheBudgets
}

// DefaultLayoutCacheBudgets 返回出厂默认预算（MiB）。
func DefaultLayoutCacheBudgets() LayoutCacheBudgets {
	return LayoutCacheBudgets{
		CellRowsMiB:     defaultCellRowsMiB,
		HistoryPlanMiB:  defaultHistoryPlanMiB,
		FoldOmissionMiB: defaultFoldOmissionMiB,
	}
}
