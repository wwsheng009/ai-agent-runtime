// Package baselinesql 是 LSP 基线的**分析库事实源**。
//
// 存在的意义：internal/lsp/baseline 过去把"回扫 runtime-events.jsonl"和"算指标"
// 焊在同一个函数里，于是事实源是日志。日志有保留策略（仓库分析文档记录 184 个
// 采样会话里 146 个文件已被清掉），所以代价不是慢，而是**数字会随轮转无声退化**。
//
// 本包只做一件事：把分析库里的行翻译成 baseline.Facts，然后交给
// baseline.Aggregate —— 与日志路径**同一段**聚合代码。
//
// 于是"切库"这件事的验收标准不是"数字差不多"，而是"逐字段完全相同"：两条路径
// 共用聚合，只有事实来源不同。equivalence_test.go 钉死这一点。
//
// 分层：baseline 只认 stdlib，usageanalytics 只认数据库，两者互不知晓。
package baselinesql

import (
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/lsp/baseline"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// Options 控制查询窗口。
type Options struct {
	// Since 只统计该时刻之后的事件；零值 = 全窗口。
	Since time.Time
	// Now 供测试注入时钟；零值取 time.Now。
	Now time.Time
}

// Analyze 从分析库产出 LSP 基线 Stats。
//
// 不读任何日志文件。这是与 baseline.Analyze（回扫路径）的唯一区别；口径由两者
// 共用的 baseline.Aggregate 保证一致。
func Analyze(store *usageanalytics.Store, opts Options) (baseline.Stats, error) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	if store == nil {
		return baseline.Aggregate(baseline.NewFacts(), now), nil
	}

	requests, err := store.LSPBaselineRequests(opts.Since)
	if err != nil {
		return baseline.Stats{}, err
	}
	firstPublish, err := store.LSPBaselineFirstPublish(opts.Since)
	if err != nil {
		return baseline.Stats{}, err
	}
	editCalls, err := store.LSPBaselineEditCalls(opts.Since, baseline.EditingTools())
	if err != nil {
		return baseline.Stats{}, err
	}

	facts := baseline.NewFacts()
	for _, row := range requests {
		facts.AddFact(baseline.FactInput{
			SessionID:           row.SessionID,
			Day:                 baseline.DayOf(row.StartedAt),
			Trigger:             row.Trigger,
			Outcome:             row.Outcome,
			DurationMS:          row.DurationMS,
			DiagCount:           row.DiagCount,
			AppendedBytes:       row.AppendedBytes,
			AppendedDiagBytes:   row.AppendedDiagBytes,
			AppendedNoteBytes:   row.AppendedNoteBytes,
			AppendedEmptyBytes:  row.AppendedEmptyBytes,
			TotalDiagCount:      row.TotalDiagCount,
			NewDiagCount:        row.NewDiagCount,
			OmittedItems:        row.OmittedItems,
			OmittedByChars:      row.OmittedByChars,
			Server:              row.Server,
			TS:                  row.StartedAt,
			PathFingerprint:     row.PathFingerprint,
			DiagFingerprint:     row.DiagFingerprint,
			ReasonCategory:      row.ReasonCategory,
			ColdFastFail:        row.ColdFastFail,
			ColdProbeClassified: row.ColdProbeClassified,
			AttemptedMembers:    row.AttemptedMembers,
		})
	}
	// 编辑调用逐条喂入：AddEditStats 的总量与分布由这一份唯一来源决定，
	// 不会与请求事实重复计数。
	perSessionCalls := map[string]int{}
	perSessionOutput := map[string]int{}
	totalCalls, totalOutput, totalOutputEvents := 0, 0, 0
	for _, call := range editCalls {
		totalCalls++
		perSessionCalls[call.SessionID]++
		if call.OutputBytes > 0 {
			totalOutput += call.OutputBytes
			totalOutputEvents++
			perSessionOutput[call.SessionID] += call.OutputBytes
		}
	}
	facts.AddEditStats(totalCalls, totalOutput, totalOutputEvents, perSessionCalls, perSessionOutput)

	for _, row := range firstPublish {
		// 键的构造与日志路径逐字一致（同为 session + "\x00" + server）。
		facts.PutColdFirst(row.SessionID+"\x00"+row.Server, row.ObservedAt, row.FirstPublishMS)
	}

	// Scan 保持零值：文件数/行数/损坏行数只有回扫日志才知道，SQL 给不出。
	// 不编造，也不从数据面读取 —— 这正是"日志只作调试依据"的落点。
	return baseline.Aggregate(facts, now), nil
}
