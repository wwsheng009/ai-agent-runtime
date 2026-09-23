package ui

import (
	"strconv"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// 这组测试把「布局缓存容量必须覆盖 transcript 的 cell 工作集」这条不变量固定
// 在生产常量与生产规模上。
//
// 故障形状（生产 pprof 实测）：巨型恢复会话有 3951 个 cell / 146535 个布局行，
// 而 cellRowsCacheMax 只有 1024 且逐出是 FIFO。LayoutAppScreen（每帧）与
// planEligibleHistoryCommits（每次完整规划）都按稳定的 transcript 顺序遍历全部
// cell，于是每个条目都在被再次复用之前就遭逐出 —— 稳态命中率退化为 0，每次布局
// 都对每个 diff/代码单元格重跑一遍 markdown/chroma 渲染（chroma 的 regexp2 回溯
// 在 profile 里是秒级），UI 锁因此被按秒级持有，FramePump 停止出帧。

// resumedSessionWorkingSet 是生产实测的 cell 工作集量级（3951，取整到 4000）。
const resumedSessionWorkingSet = 4000

func TestCellRowsCacheMaxCoversResumedSessionWorkingSet(t *testing.T) {
	if cellRowsCacheMax < resumedSessionWorkingSet {
		t.Fatalf("cellRowsCacheMax = %d，必须覆盖恢复会话的 cell 工作集 %d；"+
			"否则命中率会退化为 0，布局回退为每帧全量重算", cellRowsCacheMax, resumedSessionWorkingSet)
	}
	if historyPlanCacheMax < resumedSessionWorkingSet {
		t.Fatalf("historyPlanCacheMax = %d，必须覆盖恢复会话的 cell 工作集 %d",
			historyPlanCacheMax, resumedSessionWorkingSet)
	}
}

// TestSharedCellRowsCacheFitsResumedSessionWorkingSet 用生产容量与生产规模做
// 端到端回归：工作集装得下时，预热后的第二轮全量扫描必须 100% 命中、零逐出。
// 旧容量 1024 在此测试下会得到 0 命中 / 4000 未命中。
func TestSharedCellRowsCacheFitsResumedSessionWorkingSet(t *testing.T) {
	c := &cellRowsCache{lru: newCellLayoutLRU[[]AppScreenRow](cellRowsCacheMax, cellRowsCacheMaxBytes)}
	fp := "dark|1|github|{0}|false"
	keys := make([]cellLayoutKey, 0, resumedSessionWorkingSet)
	for i := 0; i < resumedSessionWorkingSet; i++ {
		source := "cell-" + strconv.Itoa(i)
		key := cellLayoutKeyFor(testCell(source, scene.PresentationPlain), 100, fp)
		keys = append(keys, key)
		c.put(key, []AppScreenRow{{Owner: 1, Text: source}})
	}
	if _, _, evictions, entries, _ := c.stats(); evictions != 0 || entries != resumedSessionWorkingSet {
		t.Fatalf("预热阶段 entries=%d evictions=%d，want %d/0", entries, evictions, resumedSessionWorkingSet)
	}
	for index, key := range keys {
		if c.get(key) == nil {
			t.Fatalf("第二轮扫描在第 %d 个 cell 上未命中：工作集没有被容量覆盖", index)
		}
	}
	hits, misses, evictions, _, _ := c.stats()
	if misses != 0 || hits != resumedSessionWorkingSet || evictions != 0 {
		t.Fatalf("hits=%d misses=%d evictions=%d，want %d/0/0", hits, misses, evictions, resumedSessionWorkingSet)
	}
}

// TestSharedHistoryPlanCacheFitsResumedSessionWorkingSet 是同一不变量的
// history-plan 侧：规划阶段同样按 cell 顺序遍历全部历史，容量不足会让每次完整
// 规划都重新 wrap 并物化全部历史行。
func TestSharedHistoryPlanCacheFitsResumedSessionWorkingSet(t *testing.T) {
	c := &historyPlanCache{lru: newCellLayoutLRU[[]planPhysicalRow](historyPlanCacheMax, historyPlanCacheMaxBytes)}
	fp := "dark|1|github|{0}|false"
	keys := make([]cellLayoutKey, 0, resumedSessionWorkingSet)
	for i := 0; i < resumedSessionWorkingSet; i++ {
		source := "cell-" + strconv.Itoa(i)
		key := planCacheKeyFor(testCell(source, scene.PresentationPlain), 100, fp)
		keys = append(keys, key)
		c.put(key, []planPhysicalRow{{text: source, line: appPlainRenderLine(source)}})
	}
	for index, key := range keys {
		if c.get(key) == nil {
			t.Fatalf("第二轮扫描在第 %d 个 cell 上未命中", index)
		}
	}
	hits, misses, evictions, _, _ := c.stats()
	if misses != 0 || hits != resumedSessionWorkingSet || evictions != 0 {
		t.Fatalf("hits=%d misses=%d evictions=%d，want %d/0/0", hits, misses, evictions, resumedSessionWorkingSet)
	}
}

// TestLayoutTranscriptScreenRowsWarmPassIsCacheOnly 验证修复后的稳态性质：第二次
// 布局同一份 transcript 时，全部 cell 都走缓存命中路径，不再触发任何渲染。
func TestLayoutTranscriptScreenRowsWarmPassIsCacheOnly(t *testing.T) {
	snapshot := benchResumedSnapshot(600)
	transcript := NewTranscriptState(snapshot)
	rows := transcript.LayoutRows(1)
	byID := transcriptCellsByID(transcript)
	width := 100

	first, complete := layoutTranscriptScreenRowsWithin(rows, byID, nil, width, time.Time{}, style.ThemeContext{})
	if !complete {
		t.Fatal("无预算布局不应被截断")
	}
	_, missesBefore, evictionsBefore, _, _ := sharedCellRows.stats()
	second, complete := layoutTranscriptScreenRowsWithin(rows, byID, nil, width, time.Time{}, style.ThemeContext{})
	if !complete {
		t.Fatal("无预算布局不应被截断")
	}
	hitsAfter, missesAfter, evictionsAfter, _, _ := sharedCellRows.stats()

	if len(first) != len(second) {
		t.Fatalf("两次布局行数不一致：%d vs %d", len(first), len(second))
	}
	for index := range first {
		if first[index].Text != second[index].Text || first[index].CellID != second[index].CellID {
			t.Fatalf("第 %d 行不一致：%q/%d vs %q/%d",
				index, first[index].Text, first[index].CellID, second[index].Text, second[index].CellID)
		}
	}
	// 第二次布局对已经驻留的 cell 不应产生任何未命中或逐出。
	if missesAfter != missesBefore {
		t.Fatalf("热缓存布局产生了 %d 次新未命中，want 0", missesAfter-missesBefore)
	}
	if evictionsAfter != evictionsBefore {
		t.Fatalf("热缓存布局产生了 %d 次逐出，want 0", evictionsAfter-evictionsBefore)
	}
	if hitsAfter == 0 {
		t.Fatal("第二次布局没有发生任何缓存命中")
	}
}

// TestLayoutTranscriptScreenRowsWithinHonoursBudget 验证布局本身真的受预算约束。
// 旧实现把 historyCommitPlanningBudget 的 deadline 建在 commit 循环内部，昂贵的
// 布局在预算建立之前就已经跑完 —— 预算是装饰性的。现在 deadline 由调用方在布局
// 之前建立，并且布局会在采样点检查它。
func TestLayoutTranscriptScreenRowsWithinHonoursBudget(t *testing.T) {
	snapshot := benchResumedSnapshot(400)
	transcript := NewTranscriptState(snapshot)
	rows := transcript.LayoutRows(1)
	byID := transcriptCellsByID(transcript)

	// 已经过期的 deadline 必须在第一个采样点就截断，并报告 complete=false。
	prefix, complete := layoutTranscriptScreenRowsWithin(rows, byID, nil, 100, time.Now().Add(-time.Second), style.ThemeContext{})
	if complete {
		t.Fatal("过期的 deadline 必须让布局报告 complete=false（否则调用方会把截断结果记入 memo，被截断的 cell 将永远不再被规划）")
	}
	if len(prefix) != 0 {
		t.Fatalf("过期 deadline 下应返回空前缀，got %d 行", len(prefix))
	}

	// 无预算形式必须完整且非空，并且截断形式返回的必然是它的真前缀。
	full, fullComplete := layoutTranscriptScreenRowsWithin(rows, byID, nil, 100, time.Time{}, style.ThemeContext{})
	if !fullComplete {
		t.Fatal("零值 deadline 表示无预算，不应报告截断")
	}
	if len(full) == 0 {
		t.Fatal("完整布局不应为空")
	}
}
