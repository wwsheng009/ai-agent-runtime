package cacheanalytics

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// projectorAggregates 会话级增量聚合（append/evict 各自维护，查询 O(1) 组装）。
type projectorAggregates struct {
	total            int
	withUsage        int
	cacheReported    int
	sumPrompt        int64
	sumCompletion    int64
	sumTotal         int64
	sumCacheRead     int64
	sumCacheCreation int64
	sumUncachedInput int64
	sumReasoning     int64
	// 命中/写入比率的分母各自只统计对应 reported 请求（§4.2：not_reported 不污染比率）。
	// 分母是完整输入总量 InputTotal（不含式口径下 prompt 只是新增输入）。
	sumInputTotalReadReported     int64
	sumCacheReadReported          int64
	sumInputTotalCreationReported int64
	sumCacheCreationReported      int64
	dist                          CacheStatusDistribution
}

func (a *projectorAggregates) add(record *CacheRequestRecord) {
	a.total++
	a.dist.add(record.CacheStatus)
	if record.Usage == nil {
		return
	}
	a.withUsage++
	usage := record.Usage
	a.sumPrompt += usage.PromptTokens
	a.sumCompletion += usage.CompletionTokens
	a.sumTotal += usage.TotalTokens
	a.sumCacheRead += usage.CacheReadTokens
	a.sumCacheCreation += usage.CacheCreationTokens
	a.sumUncachedInput += usage.UncachedInputTokens
	a.sumReasoning += usage.ReasoningTokens
	if usage.CacheReadReported {
		a.cacheReported++
		a.sumInputTotalReadReported += usage.InputTotal()
		a.sumCacheReadReported += usage.CacheReadTokens
	}
	if usage.CacheCreationReported {
		a.sumInputTotalCreationReported += usage.InputTotal()
		a.sumCacheCreationReported += usage.CacheCreationTokens
	}
}

func (a *projectorAggregates) remove(record *CacheRequestRecord) {
	if a.total > 0 {
		a.total--
	}
	a.dist.remove(record.CacheStatus)
	if record.Usage == nil {
		return
	}
	if a.withUsage > 0 {
		a.withUsage--
	}
	usage := record.Usage
	a.sumPrompt -= usage.PromptTokens
	a.sumCompletion -= usage.CompletionTokens
	a.sumTotal -= usage.TotalTokens
	a.sumCacheRead -= usage.CacheReadTokens
	a.sumCacheCreation -= usage.CacheCreationTokens
	a.sumUncachedInput -= usage.UncachedInputTokens
	a.sumReasoning -= usage.ReasoningTokens
	if usage.CacheReadReported {
		if a.cacheReported > 0 {
			a.cacheReported--
		}
		a.sumInputTotalReadReported -= usage.InputTotal()
		a.sumCacheReadReported -= usage.CacheReadTokens
	}
	if usage.CacheCreationReported {
		a.sumInputTotalCreationReported -= usage.InputTotal()
		a.sumCacheCreationReported -= usage.CacheCreationTokens
	}
}

// merge 把另一个聚合并入自身（父会话视图 = 自身 + 后代，schema v9）。
func (a *projectorAggregates) merge(other projectorAggregates) {
	a.total += other.total
	a.withUsage += other.withUsage
	a.cacheReported += other.cacheReported
	a.sumPrompt += other.sumPrompt
	a.sumCompletion += other.sumCompletion
	a.sumTotal += other.sumTotal
	a.sumCacheRead += other.sumCacheRead
	a.sumCacheCreation += other.sumCacheCreation
	a.sumUncachedInput += other.sumUncachedInput
	a.sumReasoning += other.sumReasoning
	a.sumInputTotalReadReported += other.sumInputTotalReadReported
	a.sumCacheReadReported += other.sumCacheReadReported
	a.sumInputTotalCreationReported += other.sumInputTotalCreationReported
	a.sumCacheCreationReported += other.sumCacheCreationReported
	a.dist.Hit += other.dist.Hit
	a.dist.Write += other.dist.Write
	a.dist.ReportedZero += other.dist.ReportedZero
	a.dist.NotReported += other.dist.NotReported
	a.dist.Error += other.dist.Error
}

func (d *CacheStatusDistribution) add(status string) {
	switch status {
	case CacheStatusHit:
		d.Hit++
	case CacheStatusWrite:
		d.Write++
	case CacheStatusReportedZero:
		d.ReportedZero++
	case CacheStatusNotReported:
		d.NotReported++
	case CacheStatusError:
		d.Error++
	}
}

func (d *CacheStatusDistribution) remove(status string) {
	switch status {
	case CacheStatusHit:
		if d.Hit > 0 {
			d.Hit--
		}
	case CacheStatusWrite:
		if d.Write > 0 {
			d.Write--
		}
	case CacheStatusReportedZero:
		if d.ReportedZero > 0 {
			d.ReportedZero--
		}
	case CacheStatusNotReported:
		if d.NotReported > 0 {
			d.NotReported--
		}
	case CacheStatusError:
		if d.Error > 0 {
			d.Error--
		}
	}
}

// sessionState 单会话投影状态：环形缓冲 + 增量聚合 + 溢出标记。
type sessionState struct {
	records        []*CacheRequestRecord // 按 started_at 升序（append 顺序）
	index          map[string]*CacheRequestRecord
	agg            projectorAggregates
	evicted        int
	partialReasons []string
}

// Projector 把终态 CacheRequestRecord 落入每 session 环形缓冲并维护聚合。
// 记录终态后不可变（message id 回填除外，且回填不影响聚合）。
type Projector struct {
	mu            sync.RWMutex
	maxPerSession int
	// persisted 表示已挂载持久化镜像表（Phase 3）：记录可随时从镜像表
	// 回放，内存投影不再淘汰，消除环形上限的 ring_overflow partial 降级
	//（方案 §710：Phase 3 sqlite 持久化后消除）。
	persisted bool
	sessions  map[string]*sessionState
	// children 是 root→子会话集合索引（schema v9）：父会话视图据此合并后代
	// 记录。淘汰只影响子会话自身状态，索引里的陈旧 id 查不到状态即忽略。
	children map[string]map[string]struct{}
}

func newProjector(maxPerSession int) *Projector {
	if maxPerSession <= 0 {
		maxPerSession = DefaultMaxRequestsPerSession
	}
	return &Projector{
		maxPerSession: maxPerSession,
		sessions:      make(map[string]*sessionState),
		children:      make(map[string]map[string]struct{}),
	}
}

func (p *Projector) state(sessionID string) *sessionState {
	state := p.sessions[sessionID]
	if state == nil {
		state = &sessionState{index: make(map[string]*CacheRequestRecord)}
		p.sessions[sessionID] = state
	}
	return state
}

// setPersisted 切换持久化模式（Attach 时按是否注入 RequestStore 设置）。
func (p *Projector) setPersisted(v bool) {
	p.mu.Lock()
	p.persisted = v
	p.mu.Unlock()
}

// append 追加终态记录；溢出时淘汰最旧并标记 partial（持久化模式下不淘汰）。
func (p *Projector) append(record *CacheRequestRecord) {
	if record == nil || record.LLMRequestID == "" || record.SessionID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.state(record.SessionID)
	if _, exists := state.index[record.LLMRequestID]; exists {
		// 幂等回放（同一请求重复终态事件）：不重复计数。
		return
	}
	state.records = append(state.records, record)
	state.index[record.LLMRequestID] = record
	state.agg.add(record)
	if root := strings.TrimSpace(record.RootSessionID); root != "" && root != record.SessionID {
		set := p.children[root]
		if set == nil {
			set = make(map[string]struct{})
			p.children[root] = set
		}
		set[record.SessionID] = struct{}{}
	}
	if !p.persisted {
		for len(state.records) > p.maxPerSession {
			oldest := state.records[0]
			state.records = state.records[1:]
			delete(state.index, oldest.LLMRequestID)
			state.agg.remove(oldest)
			state.evicted++
		}
		if state.evicted == 1 {
			// 原因字符串与方案 §4.2/§5.3 对齐：ring_overflow。
			state.partialReasons = append(state.partialReasons, "ring_overflow")
		}
	}
}

// backfillMessageID 查询期 message id 回填（不改聚合）。
func (p *Projector) backfillMessageID(sessionID, llmRequestID, userMessageID, assistantMessageID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.sessions[sessionID]
	if state == nil {
		return
	}
	record, ok := state.index[llmRequestID]
	if !ok {
		return
	}
	if userMessageID != "" && record.UserMessageID == "" {
		record.UserMessageID = userMessageID
	}
	if assistantMessageID != "" && record.AssistantMessageID == "" {
		record.AssistantMessageID = assistantMessageID
	}
}

// viewStatesLocked 返回会话视图的状态列表：自身 + 以该会话为 root 的子会话
// （schema v9）。调用方须持有 p.mu（读或写）。
func (p *Projector) viewStatesLocked(sessionID string) []*sessionState {
	states := make([]*sessionState, 0, 4)
	if state := p.sessions[sessionID]; state != nil {
		states = append(states, state)
	}
	for childID := range p.children[sessionID] {
		if state := p.sessions[childID]; state != nil {
			states = append(states, state)
		}
	}
	return states
}

// overview 组装会话总览（父会话视图 = 自身 + 全部后代记录合并）。
func (p *Projector) overview(sessionID string) CacheOverview {
	p.mu.RLock()
	defer p.mu.RUnlock()
	overview := CacheOverview{
		SchemaVersion: SchemaVersion,
		SessionID:     sessionID,
		GeneratedAt:   time.Now(),
	}
	states := p.viewStatesLocked(sessionID)
	if len(states) == 0 {
		// 空会话不是数据损伤：requests_total=0 已自明，不置 partial
		//（避免 UI 误显"仅保留最近 N 条"横幅，§4.2 partial 语义）。
		return overview
	}
	merged := projectorAggregates{}
	partialReasons := []string{}
	var records []*CacheRequestRecord
	for _, state := range states {
		merged.merge(state.agg)
		partialReasons = append(partialReasons, state.partialReasons...)
		records = append(records, state.records...)
	}
	agg := merged
	overview.RequestsTotal = agg.total
	overview.RequestsWithUsage = agg.withUsage
	overview.RequestsCacheReported = agg.cacheReported
	overview.Tokens = CacheOverviewTokens{
		PromptTokens:        agg.sumPrompt,
		CompletionTokens:    agg.sumCompletion,
		TotalTokens:         agg.sumTotal,
		CacheReadTokens:     agg.sumCacheRead,
		CacheCreationTokens: agg.sumCacheCreation,
		UncachedInputTokens: agg.sumUncachedInput,
		ReasoningTokens:     agg.sumReasoning,
	}
	if agg.sumInputTotalReadReported > 0 {
		ratio := float64(agg.sumCacheReadReported) / float64(agg.sumInputTotalReadReported)
		overview.CacheHitRatio = &ratio
	}
	if agg.sumInputTotalCreationReported > 0 {
		ratio := float64(agg.sumCacheCreationReported) / float64(agg.sumInputTotalCreationReported)
		overview.CacheWriteRatio = &ratio
	}
	overview.CacheStatusDistribution = agg.dist
	overview.Coverage = buildCoverage(agg, &sessionState{partialReasons: partialReasons})
	// 子会话事实（additive）：视图中的后代会话数与请求数。
	for childID := range p.children[sessionID] {
		if state := p.sessions[childID]; state != nil && len(state.records) > 0 {
			overview.ChildSessionCount++
			overview.ChildRequestsTotal += len(state.records)
		}
	}
	// 延迟均值：按记录线性扫描（与会话窗口 min/max 同量级），不新增增量状态。
	overview.DurationSamples, overview.AverageDurationMS = averageLatency(records, func(record *CacheRequestRecord) int64 {
		return record.DurationMS
	})
	overview.FirstTokenSamples, overview.AverageFirstTokenMS = averageLatency(records, func(record *CacheRequestRecord) int64 {
		return record.FirstTokenMS
	})
	if len(records) > 0 {
		// 窗口取 min/max 而非 records[0]/last：镜像回放与在线事件交错时
		// 追加序可能短暂非时间序，min/max 扫描使窗口与顺序无关（O(n)，
		// 与 requests 查询同量级）。
		from := records[0].StartedAt
		to := from
		for _, record := range records {
			if record.StartedAt.Before(from) {
				from = record.StartedAt
			}
			if record.StartedAt.After(to) {
				to = record.StartedAt
			}
		}
		overview.WindowFrom = &from
		overview.WindowTo = &to
	}
	return overview
}

// averageLatency 计算会话内延迟均值：0/负值视为"未采集"，不计入样本，
// 避免把缺省值摊平成一个看起来真实存在的 0ms。
func averageLatency(records []*CacheRequestRecord, pick func(*CacheRequestRecord) int64) (int, int64) {
	var samples int
	var sum int64
	for _, record := range records {
		if record == nil || pick == nil {
			continue
		}
		value := pick(record)
		if value <= 0 {
			continue
		}
		samples++
		sum += value
	}
	if samples == 0 {
		return 0, 0
	}
	return samples, sum / int64(samples)
}

func buildCoverage(agg projectorAggregates, state *sessionState) CoverageInfo {
	coverage := CoverageInfo{PartialReasons: []string{}}
	if agg.total > 0 {
		usageRate := float64(agg.withUsage) / float64(agg.total)
		coverage.UsageRequestRate = &usageRate
		if agg.withUsage > 0 {
			reportRate := float64(agg.cacheReported) / float64(agg.withUsage)
			coverage.CacheReportRate = &reportRate
		}
	}
	// partial 仅由数据完整性损伤触发（环形缓冲溢出等，§4.2/§5.3）。
	// provider 未上报 usage/缓存是正常业务状态，由 usage_request_rate /
	// cache_report_rate 自报比率表达，不置 partial（§4.2 示例：
	// not_reported>0 时 partial=false）。
	reasons := append([]string{}, state.partialReasons...)
	coverage.Partial = len(reasons) > 0
	coverage.PartialReasons = reasons
	return coverage
}

// requests 明细过滤 + 倒序分页。
func (p *Projector) requests(sessionID string, q RequestQuery) RequestListResponse {
	p.mu.RLock()
	defer p.mu.RUnlock()
	response := RequestListResponse{
		SchemaVersion: SchemaVersion,
		SessionID:     sessionID,
		Limit:         q.Limit,
		Offset:        q.Offset,
		Requests:      []CacheRequestRecord{},
	}
	states := p.viewStatesLocked(sessionID)
	if len(states) == 0 {
		return response
	}
	filtered := make([]*CacheRequestRecord, 0, 16)
	for _, state := range states {
		for _, record := range state.records {
			if !matchQuery(record, q) {
				continue
			}
			filtered = append(filtered, record)
		}
	}
	response.Total = len(filtered)
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].StartedAt.After(filtered[j].StartedAt)
	})
	if q.Offset > 0 {
		if q.Offset >= len(filtered) {
			return response
		}
		filtered = filtered[q.Offset:]
	}
	// limit 语义（§4.4）：默认 50，最大 200（超限 clamp 到 200 而非降回默认）。
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if limit < len(filtered) {
		filtered = filtered[:limit]
	}
	for _, record := range filtered {
		response.Requests = append(response.Requests, *record)
	}
	return response
}

func matchQuery(record *CacheRequestRecord, q RequestQuery) bool {
	if q.TraceID != "" && record.TraceID != q.TraceID {
		return false
	}
	if q.TurnID != "" && record.TurnID != q.TurnID {
		return false
	}
	if q.MessageID != "" && record.UserMessageID != q.MessageID && record.AssistantMessageID != q.MessageID {
		return false
	}
	if q.Status != "" && record.Status != q.Status {
		return false
	}
	if q.CacheStatus != "" && record.CacheStatus != q.CacheStatus {
		return false
	}
	if q.From != nil && record.StartedAt.Before(*q.From) {
		return false
	}
	if q.To != nil && record.StartedAt.After(*q.To) {
		return false
	}
	return true
}

// request 单请求详情。
func (p *Projector) request(sessionID, llmRequestID string) (CacheRequestRecord, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if state := p.sessions[sessionID]; state != nil {
		if record, ok := state.index[llmRequestID]; ok {
			return *record, true
		}
	}
	for childID := range p.children[sessionID] {
		state := p.sessions[childID]
		if state == nil {
			continue
		}
		if record, ok := state.index[llmRequestID]; ok {
			return *record, true
		}
	}
	return CacheRequestRecord{}, false
}

// turnSuccessfulRequests 返回 turn 内全部成功请求（登记顺序）。
func (p *Projector) turnSuccessfulRequests(sessionID, turnID string) []*CacheRequestRecord {
	p.mu.RLock()
	defer p.mu.RUnlock()
	state := p.sessions[sessionID]
	if state == nil {
		return nil
	}
	var out []*CacheRequestRecord
	for _, record := range state.records {
		if record.TurnID == turnID && record.Status == RequestStatusSuccess {
			out = append(out, record)
		}
	}
	return out
}

// requestsAfter 返回会话内 startedAt 之后开始的请求（至多 limit 条，升序）。
func (p *Projector) requestsAfter(sessionID string, startedAt time.Time, limit int) []*CacheRequestRecord {
	p.mu.RLock()
	defer p.mu.RUnlock()
	state := p.sessions[sessionID]
	if state == nil {
		return nil
	}
	var out []*CacheRequestRecord
	for _, record := range state.records {
		if record.StartedAt.After(startedAt) {
			out = append(out, record)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out
}

// sessionRecordCount 会话当前记录数（测试与诊断用）。
func (p *Projector) sessionRecordCount(sessionID string) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	count := 0
	if state := p.sessions[sessionID]; state != nil {
		count += len(state.records)
	}
	for childID := range p.children[sessionID] {
		if state := p.sessions[childID]; state != nil {
			count += len(state.records)
		}
	}
	return count
}

// sortSessionRecords 按 StartedAt 升序重排会话记录：镜像回放与在线事件
// 交错后恢复时间序（overview 窗口已改为 min/max 扫描，此排序主要保障
// requestsAfter/trace 的顺序语义；幂等，重复调用无副作用）。
func (p *Projector) sortSessionRecords(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.sessions[sessionID]
	if state == nil {
		return
	}
	sort.SliceStable(state.records, func(i, j int) bool {
		return state.records[i].StartedAt.Before(state.records[j].StartedAt)
	})
}
