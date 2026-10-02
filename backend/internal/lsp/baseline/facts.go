package baseline

import (
	"sort"
	"strings"
	"time"
)

// 本文件把"基线怎么算"与"事实从哪来"彻底分开。
//
// 拆分动机（架构修复）：过去 Analyze 一边回扫 runtime-events.jsonl 一边算指标，
// 事实源与口径焊死在一起。日志有保留策略、会被清理（仓库分析文档记录 184 个
// 采样会话里 146 个文件已被清掉），所以焊死的代价不是慢，而是**数字会随轮转
// 无声退化**，且 UI 不会提示覆盖率不足。
//
// 现在：
//   - Facts 只描述"算之前需要哪些事实"，不含任何口径；
//   - Aggregate 是唯一的口径实现，日志源与 SQL 源**共用同一段代码**。
//
// 共用而非各写一份，是等价性的来源：换源不可能只换对一半——数字要么完全一致，
// 要么测试立刻红。

// FactInput 是跨包数据源构造一条请求事实的入口。
//
// 字段与 usage_lsp_requests 的列一一对应（见 usageanalytics/ingest_lsp.go），
// 也与日志事件的载荷字段同源，故两条数据源能喂出同构的事实。
type FactInput struct {
	SessionID           string
	Day                 string
	Trigger             string
	Outcome             string
	DurationMS          int
	DiagCount           int
	AppendedBytes       int
	AppendedDiagBytes   int
	AppendedNoteBytes   int
	AppendedEmptyBytes  int
	TotalDiagCount      int
	NewDiagCount        int
	OmittedItems        int
	OmittedByChars      int
	Server              string
	TS                  time.Time
	PathFingerprint     string
	DiagFingerprint     string
	ReasonCategory      string
	ColdFastFail        bool
	ColdProbeClassified bool
	AttemptedMembers    int
}

// Facts 是聚合之前所需的全部事实，由具体数据源产出。
//
// 编辑类工具调用也在这里：覆盖率的**分母**来自 tool.completed，而 tool.completed
// 早已入库（usage_tool_calls.record_json 带 logical_tool 与
// output_model_visible_bytes），所以分母与追加比分子都能走 SQL，无需回扫日志。
type Facts struct {
	requests            []requestFact
	timestamps          []time.Time
	sessionRequests     map[string]int
	editCallsBySession  map[string]int
	editOutputBySession map[string]int
	coldFirst           map[string]coldFact
	editCalls           int
	editOutput          int
	editOutputEvents    int
	// scan 是**日志扫描自身**的健康度，不是数据事实。
	//
	// 它回答"这轮扫了多少文件、多少行、坏了多少行"，只有回扫日志才知道，SQL
	// 永远给不出。按"日志只作调试依据"的原则，这些量已移出数据面（Rows() 不再
	// 渲染）；保留字段仅为调试工具与日志路径自身可见，SQL 路径恒为 0。
	scan ScanStats
}

// NewFacts 返回一个各 map 已初始化的空事实集。
func NewFacts() Facts {
	return Facts{
		sessionRequests:     map[string]int{},
		editCallsBySession:  map[string]int{},
		editOutputBySession: map[string]int{},
		coldFirst:           map[string]coldFact{},
	}
}

// EditingTools 是覆盖率分母认定的编辑类工具（与 §3.1 mutated_paths 同口径）。
//
// 导出它是为了让 SQL 源与日志源共用**同一份清单**。两处各写一份的话，日后新增
// 工具只改一处就会让两条路径的分母悄悄不同 —— 而覆盖率恰恰是最容易被这种漂移
// 悄悄改掉的读数。
func EditingTools() []string {
	tools := make([]string, 0, len(editingTools))
	for tool := range editingTools {
		tools = append(tools, tool)
	}
	sort.Strings(tools)
	return tools
}

// DayOf 是按日分桶的键，与日志路径的 day 取值口径一致。
func DayOf(ts time.Time) string {
	if ts.IsZero() {
		return "unknown"
	}
	return ts.UTC().Format("2006-01-02")
}

// AddFact 追加一条请求事实，并维护按会话的计数与时间戳。
func (f *Facts) AddFact(in FactInput) {
	if f.sessionRequests == nil {
		f.sessionRequests = map[string]int{}
	}
	f.requests = append(f.requests, requestFact{
		sessionID:           in.SessionID,
		day:                 in.Day,
		trigger:             in.Trigger,
		outcome:             in.Outcome,
		durationMS:          in.DurationMS,
		diagCount:           in.DiagCount,
		appendedBytes:       in.AppendedBytes,
		appendedDiagBytes:   in.AppendedDiagBytes,
		appendedNoteBytes:   in.AppendedNoteBytes,
		appendedEmptyBytes:  in.AppendedEmptyBytes,
		totalDiagCount:      in.TotalDiagCount,
		newDiagCount:        in.NewDiagCount,
		omittedItems:        in.OmittedItems,
		omittedByChars:      in.OmittedByChars,
		server:              in.Server,
		ts:                  in.TS,
		pathFingerprint:     in.PathFingerprint,
		diagFingerprint:     in.DiagFingerprint,
		reasonCategory:      in.ReasonCategory,
		coldFastFail:        in.ColdFastFail,
		coldProbeClassified: in.ColdProbeClassified,
		attemptedMembers:    in.AttemptedMembers,
	})
	if !in.TS.IsZero() {
		f.timestamps = append(f.timestamps, in.TS)
	}
	if in.SessionID != "" {
		f.sessionRequests[in.SessionID]++
	}
}

// AddEditStats 累加一份来源的编辑类工具调用统计（覆盖率分母 + 追加比分子）。
//
// 总量与按会话分布同时传入：Aggregate 需要总量算 EditCalls/EditOutput，需要分布
// 算 ActiveEdit/ActiveOutput（只统计 LSP 活跃会话内的编辑调用 —— §4.3 反模式：
// 不得把未启用 LSP 的会话算进分母）。
func (f *Facts) AddEditStats(totalCalls, totalOutput, totalOutputEvents int, callsBySession, outputBySession map[string]int) {
	if f.editCallsBySession == nil {
		f.editCallsBySession = map[string]int{}
	}
	if f.editOutputBySession == nil {
		f.editOutputBySession = map[string]int{}
	}
	f.editCalls += totalCalls
	f.editOutput += totalOutput
	f.editOutputEvents += totalOutputEvents
	for sessionID, count := range callsBySession {
		f.editCallsBySession[sessionID] += count
	}
	for sessionID, total := range outputBySession {
		f.editOutputBySession[sessionID] += total
	}
}

// PutColdFirst 记录 (session, server) 的首发布延迟观测，保留**最早**那条。
//
// 严格早于才覆盖，使并列时保留先出现的那条 —— 与日志路径逐行喂入得到的
// 运行最小值等价。
func (f *Facts) PutColdFirst(key string, ts time.Time, ms int) {
	if f.coldFirst == nil {
		f.coldFirst = map[string]coldFact{}
	}
	if prev, ok := f.coldFirst[key]; !ok || ts.Before(prev.ts) {
		f.coldFirst[key] = coldFact{ts: ts, ms: ms}
	}
}

// SetScan 附上日志扫描健康度（仅日志路径调用；SQL 路径不调，恒为零值）。
func (f *Facts) SetScan(scan ScanStats) { f.scan = scan }

// Aggregate 是基线的**唯一口径实现**：把事实聚合成 Stats。
//
// 这段代码原先内联在 Analyze 末尾，拆分时逐字搬运，未改任何分支、阈值或取整。
// 两条数据源都走这里，所以"换源"不可能只换对一半。
func Aggregate(facts Facts, now time.Time) Stats {
	stats := Stats{
		Triggers: map[string]int{},
		Outcomes: map[string]int{},
		Servers:  map[string]int{},
		ByDay:    map[string]*DayBucket{},
		Scan:     facts.scan,
	}
	requests := facts.requests
	sessionRequests := facts.sessionRequests
	editCallsBySession := facts.editCallsBySession
	editOutputBySession := facts.editOutputBySession
	coldFirst := facts.coldFirst
	timestamps := facts.timestamps

	stats.EditCalls = facts.editCalls
	stats.EditOutput = facts.editOutput
	stats.EditOutputEvs = facts.editOutputEvents

	stats.Requests = len(requests)
	stats.Sessions = len(sessionRequests)
	for _, fact := range requests {
		stats.Triggers[fact.trigger]++
		stats.Outcomes[fact.outcome]++
		if fact.server != "" {
			stats.Servers[fact.server]++
		}
		stats.AppendedBytes += fact.appendedBytes
		stats.AppendedDiagBytes += fact.appendedDiagBytes
		stats.AppendedNoteBytes += fact.appendedNoteBytes
		stats.AppendedEmptyBytes += fact.appendedEmptyBytes
		stats.TotalDiagCount += fact.totalDiagCount
		stats.NewDiagCount += fact.newDiagCount
		stats.DiagCount += fact.diagCount
		// truncated_requests 不是日志健康度：它由 LSP 事件载荷的
		// omitted_items / omitted_by_chars 决定（usage_lsp_requests 同名列），
		// 与"日志行被截断"无关，所以保留在数据面。
		if fact.omittedItems > 0 || fact.omittedByChars > 0 {
			stats.Truncated++
		}
		stats.OmittedItems += fact.omittedItems
		stats.OmittedByChars += fact.omittedByChars
		stats.durations = append(stats.durations, fact.durationMS)
		if fact.outcome == "injected" {
			stats.Injected++
			if fact.diagCount > 0 {
				stats.DiagHit++
			}
		}
		if fact.outcome == "clean" {
			stats.Clean++
		}
		if fact.outcome == "no_server" {
			stats.NoServer++
		}
		if strings.HasPrefix(fact.outcome, "degraded") {
			stats.Degraded++
		}
		// 只有携带 cold_fast_fail 字段的事件才参与拆分：旧构建缺字段时
		// "缺失"不等于"首探针"，否则会把历史 no_fresh 全部误计入分子。
		if fact.outcome == "degraded_no_fresh" && fact.coldProbeClassified {
			if fact.coldFastFail {
				stats.ColdRepeat++
			} else {
				stats.ColdFirstProbe++
			}
		}
		if fact.attemptedMembers > 1 {
			stats.MultiMemberRequests++
		}
		if fact.attemptedMembers > stats.AttemptedMembersMax {
			stats.AttemptedMembersMax = fact.attemptedMembers
		}
		if fact.reasonCategory != "" {
			if stats.DegradeReasons == nil {
				stats.DegradeReasons = map[string]int{}
			}
			stats.DegradeReasons[fact.reasonCategory]++
		}
		bucket := stats.ByDay[fact.day]
		if bucket == nil {
			bucket = &DayBucket{}
			stats.ByDay[fact.day] = bucket
		}
		bucket.Requests++
		bucket.Durations = append(bucket.Durations, fact.durationMS)
		if fact.outcome == "injected" {
			bucket.Injected++
		}
		if strings.HasPrefix(fact.outcome, "degraded") {
			bucket.Degraded++
		}
	}
	sort.Ints(stats.durations)
	stats.LatencyP50MS = Percentile(stats.durations, 0.50)
	stats.LatencyP95MS = Percentile(stats.durations, 0.95)
	if len(timestamps) > 0 {
		sort.Slice(timestamps, func(i, j int) bool { return timestamps[i].Before(timestamps[j]) })
		stats.FirstAt = timestamps[0].UTC().Format(time.RFC3339)
		stats.LastAt = timestamps[len(timestamps)-1].UTC().Format(time.RFC3339)
	}
	for sessionID, count := range editCallsBySession {
		if _, active := sessionRequests[sessionID]; active {
			stats.ActiveEdit += count
		}
	}
	for sessionID, total := range editOutputBySession {
		if _, active := sessionRequests[sessionID]; active {
			stats.ActiveOutput += total
		}
	}
	for _, fact := range coldFirst {
		stats.coldStarts = append(stats.coldStarts, fact.ms)
	}
	sort.Ints(stats.coldStarts)
	stats.ColdFirstPublishP50MS = Percentile(stats.coldStarts, 0.50)
	stats.ColdFirstPublishP95MS = Percentile(stats.coldStarts, 0.95)
	// lsp_closure_ratio（plan §3.3）：同一会话内，被注入的诊断集合是否在下一次
	// 同文件编辑后消失（下一次请求 outcome=clean）。只统计带 fingerprint 的
	// injected 请求；无 eligible 样本输出 n/a（不把未采集渲染成 0）。
	bySessionPath := map[string][]requestFact{}
	for _, fact := range requests {
		if fact.sessionID == "" || fact.pathFingerprint == "" {
			continue
		}
		key := fact.sessionID + "\x00" + fact.pathFingerprint
		bySessionPath[key] = append(bySessionPath[key], fact)
	}
	for _, group := range bySessionPath {
		sort.SliceStable(group, func(i, j int) bool { return group[i].ts.Before(group[j].ts) })
		for index, fact := range group {
			if fact.outcome != "injected" || fact.diagFingerprint == "" {
				continue
			}
			stats.ClosureEligible++
			if index+1 < len(group) && group[index+1].outcome == "clean" {
				stats.ClosureClosed++
			}
		}
	}
	for _, bucket := range stats.ByDay {
		sort.Ints(bucket.Durations)
		bucket.P95MS = Percentile(bucket.Durations, 0.95)
	}
	stats.GeneratedAt = now.UTC().Format(time.RFC3339)
	return stats
}
