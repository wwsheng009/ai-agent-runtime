package ui

import (
	"fmt"
	"strings"
	"testing"
)

// layout_cache_budget_test.go —— T8 契约：预算可下调并立即逐出、可复位、
// 负数按未覆盖处理、状态投影可见。

func TestLayoutCacheBudgetsApplyTrimAndReset(t *testing.T) {
	t.Cleanup(func() { ResetLayoutCacheBudgets() })
	ResetLayoutCacheBudgets()

	putEntries := func(prefix string, count, bytesEach int) {
		for i := 0; i < count; i++ {
			key := cellLayoutKey{source: fmt.Sprintf("%s-%d", prefix, i), width: 80}
			rows := []AppScreenRow{{Text: strings.Repeat("x", 16)}}
			sharedCellRows.mu.Lock()
			sharedCellRows.lru.put(key, rows, bytesEach)
			sharedCellRows.mu.Unlock()
		}
	}

	// 默认（出厂 64MiB）预算下，2MiB 驻留不应被逐出。
	putEntries("budget-default", 4, 512*1024)
	if _, _, _, entries, bytes := sharedCellRows.stats(); entries < 4 || bytes < 2<<20 {
		t.Fatalf("默认预算下不应逐出：entries=%d bytes=%d", entries, bytes)
	}

	// 下调到 1MiB：立即逐出到预算内，并返回回收字节。
	reclaimed := ApplyLayoutCacheBudgets(LayoutCacheBudgets{CellRowsMiB: 1})
	if reclaimed <= 0 {
		t.Fatalf("下调预算应收割已驻留条目：%d", reclaimed)
	}
	if _, _, _, entries, bytes := sharedCellRows.stats(); bytes > 1<<20 || entries > 2 {
		t.Fatalf("下调后未收敛到 1MiB：entries=%d bytes=%d", entries, bytes)
	}
	if got := LayoutCacheBudgetsSnapshot(); got.CellRowsMiB != 1 || got.HistoryPlanMiB != 0 || got.FoldOmissionMiB != 0 {
		t.Fatalf("预算覆盖快照不符：%+v", got)
	}
	if stats := TranscriptResidencyStatsSnapshot(); stats.LayoutCache.CellRowsMiB != 1 {
		t.Fatalf("residency 状态未反映缓存预算：%+v", stats.LayoutCache)
	}

	// 负数视为"不覆盖"（不回退也不放宽）。
	if reclaimed := ApplyLayoutCacheBudgets(LayoutCacheBudgets{CellRowsMiB: -5}); reclaimed != 0 {
		t.Fatalf("负数预算不应触发逐出：%d", reclaimed)
	}
	if got := LayoutCacheBudgetsSnapshot(); got != (LayoutCacheBudgets{}) {
		t.Fatalf("负数应归一为未覆盖：%+v", got)
	}

	// 复位：覆盖清零、默认预算恢复（2MiB 驻留不再被逐出）。
	ResetLayoutCacheBudgets()
	if got := LayoutCacheBudgetsSnapshot(); got != (LayoutCacheBudgets{}) {
		t.Fatalf("Reset 后覆盖值应为零：%+v", got)
	}
	if defaults := DefaultLayoutCacheBudgets(); defaults.CellRowsMiB != 64 ||
		defaults.HistoryPlanMiB != 64 || defaults.FoldOmissionMiB != 64 {
		t.Fatalf("出厂默认预算不符（应 64/64/64 MiB）：%+v", defaults)
	}
	putEntries("budget-reset", 4, 512*1024)
	if _, _, _, entries, _ := sharedCellRows.stats(); entries < 4 {
		t.Fatalf("Reset 后应恢复默认预算：entries=%d", entries)
	}
}
