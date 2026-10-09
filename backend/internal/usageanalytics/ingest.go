package usageanalytics

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// 事件类型：实际发布为点号格式（internal/agent/loop.go）；
// 下划线常量（internal/chat/events.go）为消费端兼容别名，一并订阅。
const (
	EventLLMRequestStarted       = "llm.request.started"
	EventLLMRequestFinished      = "llm.request.finished"
	EventLLMRequestStartedAlias  = "llm_request_started"
	EventLLMRequestFinishedAlias = "llm_request_finished"
	EventAssistantMessage        = "assistant_message"
	EventSessionStart            = "session_start"
	EventSessionEnd              = "session_end"
	EventSessionInterrupted      = "session_interrupted"
	// schema v2 输入（方案 §5.3）：工具生命周期与子代理完成。
	EventToolRequested     = "tool.requested"
	EventToolCompleted     = "tool.completed"
	EventSubagentCompleted = "subagent.completed"
	// EventSubagentStarted 是子代理开工事件（scheduler.go 发射，载荷含
	// parent_session_id/subagent_id）：collector 据此学习 child→parent 映射，
	// 作为 llm.request.* 载荷与宿主 lookup 都缺失时的父链兜底。
	EventSubagentStarted = "subagent.started"
)

// maxLineageDepth 父链向上回溯的深度上限（防环/防异常链）。
const maxLineageDepth = 16

// SessionStatusCompleted / SessionStatusInterrupted 会话终态。
const (
	SessionStatusCompleted   = "completed"
	SessionStatusInterrupted = "interrupted"
)

// SessionMeta 是补齐 usage_sessions 元数据所需的最小字段集
// （best-effort：查不到就保留已有值）。
type SessionMeta struct {
	Title            string
	ProjectPath      string
	WorkingDirectory string
	Provider         string
	Model            string
	Protocol         string
	Status           string
	// ParentSessionID / SubagentID 是父链维度（schema v9）：子代理会话归属的
	// 直接父会话与子代理任务 id；顶层会话为空。空值不覆盖已有值（与其余字段
	// 的合并语义一致）。
	ParentSessionID string
	SubagentID      string
}

// SessionMetaLookup 由调用方注入的会话元数据来源
// （runtime server: chat.SessionManager；aicli 本地: host.SessionStore）。
type SessionMetaLookup interface {
	// SessionMeta 返回会话元数据；ok=false 表示未知（保留库中已有值）。
	SessionMeta(sessionID string) (SessionMeta, bool)
}

// SessionLineage 描述会话的父链归属（schema v9）。
type SessionLineage struct {
	// ParentSessionID 直接父会话 id；顶层会话为空。
	ParentSessionID string
	// RootSessionID 根会话 id；顶层会话等于自身。父会话视图按 root 展开全部后代。
	RootSessionID string
	// SubagentID 子会话对应的子代理任务 id（调度器 task.ID / 控制面 agent_id）。
	SubagentID string
}

// SessionLineageLookup 由宿主注入的父链来源（best-effort，可为 nil）：
// runtime server / aicli 本地均从会话 context（agent_parent_session_id /
// agent_root_session_id / agent_id）读取。
type SessionLineageLookup interface {
	SessionLineage(sessionID string) (SessionLineage, bool)
}

// inflightRequest 已开始未终态的请求登记（会话终止时兜底为 interrupted）。
type inflightRequest struct {
	sessionID         string
	parentSessionID   string
	rootSessionID     string
	subagentID        string
	traceID           string
	turnID            string
	step              int
	provider          string
	model             string
	stream            bool
	startedAt         time.Time
	cacheEpoch        int
	promptCacheKey    string
	promptFingerprint string
}

// collector 订阅 runtime EventBus 并实时 UPSERT 分析库。
type collector struct {
	store  *Store
	lookup SessionMetaLookup
	// lineage 宿主父链来源（可为 nil）；learned 是 subagent.started/completed
	// 事件学习到的 child→父链映射（mu 保护），作为第三优先级兜底。
	lineage SessionLineageLookup
	learned map[string]SessionLineage
	now    func() time.Time

	mu       sync.Mutex
	inflight map[string]*inflightRequest
	unsubs   []func()
	// turnToolStats：回合内工具失败/恢复计数（会话终止时落 usage_turns）。
	turnToolStats map[string]*turnToolStats

	// writeFailureCount：分析库写入失败此前被完全静默（行丢失且无痕迹）。
	// 首次失败提示一次，之后每 100 次再提示一次，既留痕又不刷屏。
	writeFailureCount atomic.Int64
}

func newCollector(store *Store, lookup SessionMetaLookup, now func() time.Time, lineage ...SessionLineageLookup) *collector {
	if now == nil {
		now = time.Now
	}
	collector := &collector{
		store:    store,
		lookup:   lookup,
		now:      now,
		inflight: make(map[string]*inflightRequest),
	}
	if len(lineage) > 0 {
		collector.lineage = lineage[0]
	}
	return collector
}

// subscribe 订阅全部输入事件（幂等：重复调用只订阅一次由调用方保证）。
func (c *collector) subscribe(bus *runtimeevents.Bus) {
	if c == nil || bus == nil {
		return
	}
	for _, eventType := range []string{
		EventLLMRequestStarted, EventLLMRequestFinished,
		EventLLMRequestStartedAlias, EventLLMRequestFinishedAlias,
		EventAssistantMessage, EventSessionStart, EventSessionEnd, EventSessionInterrupted,
		EventToolRequested, EventToolCompleted, EventSubagentCompleted,
		EventSubagentStarted,
		// 路由切换观测（主 Agent / 子 Agent）：常量取 internal/events 的权威定义，
		// 避免此处再抄一份字面量。
		runtimeevents.EventSubagentRouteResolved,
		runtimeevents.EventMainAgentRouteApplied,
		runtimeevents.EventMainAgentRouteCleared,
		runtimeevents.EventMainAgentRoutePredictionInvalid,
		runtimeevents.EventMainAgentRoutePredictionUnresolvable,
		runtimeevents.EventMainAgentRouteDisabledForTurn,
		runtimeevents.EventMainAgentRouteCostGuardTripped,
		// 渲染围栏丢弃诊断（P1-1b）：CLI 在 EndRun 上报增量计数。
		runtimeevents.EventRenderFenceDropped,
		// LSP 观测（§4.3 基线的事实源）。刻意包含 lsp.server.state —— 它在
		// contract.go 里是 ChannelLiveOnly（不落盘），但基线需要它的首发布延迟，
		// 而"不落盘"是**每会话 JSONL 体积**的约束，与"进分析库"无关：collector
		// 订阅的是实时总线。落盘面保持不变，事实却因此可复算。
		runtimeevents.EventLSPRequestFinished,
		runtimeevents.EventLSPServerState,
	} {
		c.unsubs = append(c.unsubs, bus.SubscribeCancelable(eventType, c.handleEvent))
	}
}

// close 取消订阅并清空 in-flight（幂等）。
func (c *collector) close() {
	if c == nil {
		return
	}
	for _, unsub := range c.unsubs {
		if unsub != nil {
			unsub()
		}
	}
	c.unsubs = nil
	c.mu.Lock()
	c.inflight = make(map[string]*inflightRequest)
	c.turnToolStats = make(map[string]*turnToolStats)
	c.mu.Unlock()
}

func (c *collector) handleEvent(event runtimeevents.Event) {
	switch event.Type {
	case EventLLMRequestStarted, EventLLMRequestStartedAlias:
		c.onRequestStarted(event)
	case EventLLMRequestFinished, EventLLMRequestFinishedAlias:
		c.onRequestFinished(event)
	case EventAssistantMessage:
		c.onAssistantMessage(event)
	case EventSessionStart:
		c.onSessionStart(event)
	case EventSessionEnd, EventSessionInterrupted:
		c.onSessionTerminal(event)
	case EventToolRequested:
		c.onToolRequested(event)
	case EventToolCompleted:
		c.onToolCompleted(event)
	case EventSubagentCompleted:
		c.onSubagentCompleted(event)
		c.learnSubagentLineage(event)
	case EventSubagentStarted:
		c.learnSubagentLineage(event)
	case runtimeevents.EventSubagentRouteResolved:
		c.onSubagentRouteResolved(event)
	case runtimeevents.EventMainAgentRouteApplied:
		c.onMainAgentRouteApplied(event)
	case runtimeevents.EventMainAgentRouteCleared:
		c.onMainAgentRouteCleared(event)
	case runtimeevents.EventMainAgentRoutePredictionInvalid,
		runtimeevents.EventMainAgentRoutePredictionUnresolvable,
		runtimeevents.EventMainAgentRouteDisabledForTurn,
		runtimeevents.EventMainAgentRouteCostGuardTripped:
		if reason := mainAgentRouteWarningReason(event.Type); reason != "" {
			c.onMainAgentRouteWarning(event, reason)
		}
	case runtimeevents.EventRenderFenceDropped:
		c.onRenderFenceDropped(event)
	case runtimeevents.EventLSPRequestFinished:
		c.onLSPRequestFinished(event)
	case runtimeevents.EventLSPServerState:
		c.onLSPServerState(event)
	}
}

func (c *collector) onRequestStarted(event runtimeevents.Event) {
	payload := event.Payload
	llmRequestID := payloadString(payload, "llm_request_id")
	if llmRequestID == "" {
		return
	}
	sessionID := event.SessionID
	if sessionID == "" {
		sessionID = payloadString(payload, "session_id")
	}
	if sessionID == "" {
		return
	}
	traceID := event.TraceID
	if traceID == "" {
		traceID = payloadString(payload, "trace_id")
	}
	lineage := c.resolveLineage(sessionID, payload)
	startedAt := event.Timestamp
	if startedAt.IsZero() {
		startedAt = c.now()
	}
	inflight := &inflightRequest{
		sessionID:         sessionID,
		parentSessionID:   lineage.ParentSessionID,
		rootSessionID:     lineage.RootSessionID,
		subagentID:        lineage.SubagentID,
		traceID:           traceID,
		turnID:            payloadString(payload, "logical_turn_id", "turn_id"),
		step:              payloadInt(payload, "step"),
		provider:          payloadString(payload, "provider"),
		model:             payloadString(payload, "model"),
		stream:            payloadString(payload, "stream_id") != "",
		startedAt:         startedAt,
		cacheEpoch:        payloadInt(payload, "prompt_cache_epoch"),
		promptCacheKey:    payloadString(payload, "prompt_cache_key"),
		promptFingerprint: payloadString(payload, "prompt_fingerprint"),
	}
	c.mu.Lock()
	c.inflight[llmRequestID] = inflight
	c.mu.Unlock()

	// 会话行用会话自身归属；请求行沿用 lineage（请求级归因）。
	c.upsertSession(sessionID, c.sessionLineageMeta(sessionID, SessionMeta{
		Provider: inflight.provider,
		Model:    inflight.model,
	}, lineage), startedAt, time.Time{})
}

func (c *collector) onRequestFinished(event runtimeevents.Event) {
	payload := event.Payload
	llmRequestID := payloadString(payload, "llm_request_id")
	if llmRequestID == "" {
		return
	}
	c.mu.Lock()
	inflight := c.inflight[llmRequestID]
	delete(c.inflight, llmRequestID)
	now := c.now()
	c.mu.Unlock()

	sessionID := event.SessionID
	if sessionID == "" {
		sessionID = payloadString(payload, "session_id")
	}
	if inflight == nil {
		inflight = &inflightRequest{}
	}
	if inflight.sessionID == "" {
		inflight.sessionID = sessionID
	}
	if inflight.traceID == "" {
		inflight.traceID = event.TraceID
		if inflight.traceID == "" {
			inflight.traceID = payloadString(payload, "trace_id")
		}
	}
	if inflight.turnID == "" {
		inflight.turnID = payloadString(payload, "logical_turn_id", "turn_id")
	}
	if inflight.provider == "" {
		inflight.provider = payloadString(payload, "provider")
	}
	if inflight.model == "" {
		inflight.model = payloadString(payload, "model")
	}
	if inflight.startedAt.IsZero() {
		inflight.startedAt = now
	}
	// 父链兜底：started 事件缺失（进程重启/丢帧）时按 finished 载荷重新解析。
	if inflight.parentSessionID == "" && inflight.rootSessionID == "" {
		lineage := c.resolveLineage(inflight.sessionID, payload)
		inflight.parentSessionID = lineage.ParentSessionID
		inflight.rootSessionID = lineage.RootSessionID
		if inflight.subagentID == "" {
			inflight.subagentID = lineage.SubagentID
		}
	}
	if inflight.sessionID == "" {
		// 无会话归属的请求不进入分析库（无法归组）。
		return
	}

	finishedAt := now
	record := cacheanalytics.BuildTerminalRecord(cacheanalytics.TerminalRecordInput{
		LLMRequestID:      llmRequestID,
		SessionID:         inflight.sessionID,
		ParentSessionID:   inflight.parentSessionID,
		RootSessionID:     inflight.rootSessionID,
		SubagentID:        inflight.subagentID,
		TraceID:           inflight.traceID,
		TurnID:            inflight.turnID,
		Step:              inflight.step,
		Provider:          inflight.provider,
		Model:             inflight.model,
		Stream:            inflight.stream,
		Attempt:           1,
		StartedAt:         inflight.startedAt,
		FinishedAt:        finishedAt,
		CacheEpoch:        inflight.cacheEpoch,
		PromptCacheKey:    inflight.promptCacheKey,
		PromptFingerprint: inflight.promptFingerprint,
		Payload:           payload,
	})
	c.persistRequestTerminal(record, c.sessionLineageMeta(record.SessionID, SessionMeta{
		Provider: record.Provider,
		Model:    record.Model,
	}, SessionLineage{
		ParentSessionID: inflight.parentSessionID,
		RootSessionID:   inflight.rootSessionID,
		SubagentID:      inflight.subagentID,
	}), record.StartedAt, finishedAt)
}

func (c *collector) onAssistantMessage(event runtimeevents.Event) {
	sessionID := event.SessionID
	if sessionID == "" {
		sessionID = payloadString(event.Payload, "session_id")
	}
	if sessionID == "" {
		return
	}
	c.upsertSession(sessionID, SessionMeta{}, time.Time{}, c.now())
}

// onSessionTerminal 终结该会话全部悬挂 in-flight（interrupted），
// 并把会话状态推进到终态（幂等：同 id 重复事件覆盖为同一状态）。
func (c *collector) onSessionTerminal(event runtimeevents.Event) {
	sessionID := event.SessionID
	if sessionID == "" {
		sessionID = payloadString(event.Payload, "session_id")
	}
	if sessionID == "" {
		return
	}
	now := c.now()
	c.mu.Lock()
	type orphanPair struct {
		id       string
		inflight *inflightRequest
	}
	var orphans []orphanPair
	for id, inflight := range c.inflight {
		if inflight.sessionID == sessionID {
			orphans = append(orphans, orphanPair{id: id, inflight: inflight})
		}
	}
	for _, orphan := range orphans {
		delete(c.inflight, orphan.id)
	}
	c.mu.Unlock()

	for _, orphan := range orphans {
		inflight := orphan.inflight
		finishedAt := now
		record := cacheanalytics.BuildTerminalRecord(cacheanalytics.TerminalRecordInput{
			LLMRequestID:      orphan.id,
			SessionID:         inflight.sessionID,
			ParentSessionID:   inflight.parentSessionID,
			RootSessionID:     inflight.rootSessionID,
			SubagentID:        inflight.subagentID,
			TraceID:           inflight.traceID,
			TurnID:            inflight.turnID,
			Step:              inflight.step,
			Provider:          inflight.provider,
			Model:             inflight.model,
			Stream:            inflight.stream,
			StartedAt:         inflight.startedAt,
			FinishedAt:        finishedAt,
			CacheEpoch:        inflight.cacheEpoch,
			PromptCacheKey:    inflight.promptCacheKey,
			PromptFingerprint: inflight.promptFingerprint,
			Interrupted:       true,
		})
		c.persistRequestTerminal(record, c.sessionLineageMeta(record.SessionID, SessionMeta{}, SessionLineage{
			ParentSessionID: inflight.parentSessionID,
			RootSessionID:   inflight.rootSessionID,
			SubagentID:      inflight.subagentID,
		}), record.StartedAt, finishedAt)
	}

	status := SessionStatusCompleted
	if event.Type == EventSessionInterrupted {
		status = SessionStatusInterrupted
	}
	c.upsertSession(sessionID, SessionMeta{Status: status}, time.Time{}, now)
	// schema v2：回合级终值（含工具失败/恢复计数），session_end 为权威。
	c.upsertTurnTerminal(sessionID, event)
}

// ---------------------------------------------------------------------------
// 父链解析（schema v9）：优先级 事件载荷 > 宿主 lookup > subagent.* 学习映射。
// 解析结果随请求行与会话行落库；root 保证非空（顶层 = 自身）。
// ---------------------------------------------------------------------------

// learnSubagentLineage 从 subagent.started/completed 事件学习 child→父链映射。
// 事件载荷（scheduler.go 发射点）含 parent_session_id/subagent_id，事件 SessionID
// 即子会话 id；缺失标识的载荷直接忽略（不写猜测值）。
func (c *collector) learnSubagentLineage(event runtimeevents.Event) {
	if c == nil {
		return
	}
	payload := event.Payload
	child := strings.TrimSpace(event.SessionID)
	if child == "" {
		child = payloadString(payload, "child_session_id", "session_id")
	}
	parent := payloadString(payload, "parent_session_id")
	if child == "" || parent == "" {
		return
	}
	if parent == child {
		// 自指父值非法（schema 语义：顶层会话 parent 为空）：subagent.* 事件由
		// 父会话发射时 event.SessionID 可能就是父会话本身，学到自指映射会把
		// 父会话误当子会话（实测父行 parent_session_id=自身）。
		return
	}
	c.mu.Lock()
	if c.learned == nil {
		c.learned = make(map[string]SessionLineage)
	}
	c.learned[child] = SessionLineage{
		ParentSessionID: parent,
		SubagentID:      payloadString(payload, "subagent_id", "agent_id"),
	}
	c.mu.Unlock()
}

// learnedParent 读取学习映射（mu 保护）。
func (c *collector) learnedParent(sessionID string) (SessionLineage, bool) {
	if c == nil || sessionID == "" {
		return SessionLineage{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	lineage, ok := c.learned[sessionID]
	return lineage, ok
}

// parentOf 返回会话的直接父会话（学习映射优先于宿主 lookup）。
func (c *collector) parentOf(sessionID string) string {
	if c == nil || sessionID == "" {
		return ""
	}
	if learned, ok := c.learnedParent(sessionID); ok && learned.ParentSessionID != "" {
		return learned.ParentSessionID
	}
	if c.lineage != nil {
		if looked, ok := c.lineage.SessionLineage(sessionID); ok {
			return looked.ParentSessionID
		}
	}
	return ""
}

// resolveRoot 从直接父会话向上回溯到根（深度上限 + 环保护）；无父链时 root=自身。
func (c *collector) resolveRoot(sessionID, parentSessionID string) string {
	if parentSessionID == "" {
		return sessionID
	}
	current := parentSessionID
	seen := map[string]bool{sessionID: true}
	for depth := 0; depth < maxLineageDepth; depth++ {
		if seen[current] {
			return current
		}
		seen[current] = true
		next := c.parentOf(current)
		if next == "" {
			return current
		}
		current = next
	}
	return current
}

// resolveLineage 解析会话的父链归属；RootSessionID 保证非空。
func (c *collector) resolveLineage(sessionID string, payload map[string]interface{}) SessionLineage {
	lineage := SessionLineage{
		ParentSessionID: payloadString(payload, "parent_session_id"),
		RootSessionID:   payloadString(payload, "root_session_id"),
		SubagentID:      payloadString(payload, "subagent_id"),
	}
	if c != nil && c.lineage != nil && (lineage.ParentSessionID == "" || lineage.SubagentID == "") {
		if looked, ok := c.lineage.SessionLineage(sessionID); ok {
			if lineage.ParentSessionID == "" {
				lineage.ParentSessionID = looked.ParentSessionID
			}
			if lineage.SubagentID == "" {
				lineage.SubagentID = looked.SubagentID
			}
			if lineage.RootSessionID == "" {
				lineage.RootSessionID = looked.RootSessionID
			}
		}
	}
	if lineage.ParentSessionID == "" {
		if learned, ok := c.learnedParent(sessionID); ok {
			lineage.ParentSessionID = learned.ParentSessionID
			if lineage.SubagentID == "" {
				lineage.SubagentID = learned.SubagentID
			}
		}
	}
	if lineage.RootSessionID == "" {
		lineage.RootSessionID = c.resolveRoot(sessionID, lineage.ParentSessionID)
	}
	// 记住本次解析结果：重复终态事件 / 后续无载荷事件（载荷丢帧、跨进程回放）
	// 仍能解析出同一父链，避免把子代理请求从根会话回退掉。
	if lineage.ParentSessionID != "" {
		c.rememberLineage(sessionID, lineage)
	}
	return lineage
}

// rememberLineage 把已解析的父链写入学习映射（幂等；只补不覆盖已知父链，
// 仅补齐缺失的 subagent id）。
func (c *collector) rememberLineage(sessionID string, lineage SessionLineage) {
	if c == nil || strings.TrimSpace(sessionID) == "" || lineage.ParentSessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.learned == nil {
		c.learned = make(map[string]SessionLineage)
	}
	existing, ok := c.learned[sessionID]
	if !ok || existing.ParentSessionID == "" {
		c.learned[sessionID] = lineage
		return
	}
	if existing.SubagentID == "" && lineage.SubagentID != "" {
		existing.SubagentID = lineage.SubagentID
		c.learned[sessionID] = existing
	}
}

// sessionLineageMeta 返回 usage_sessions 行应写入的父链维度。会话行归属以会话
// 自身为准（宿主 lookup > subagent.* 学习映射），最后才退回请求级归因；自指父值
// （parent=自身）按顶层处理——父会话在子代理在途期间替子代理代跑的请求带
// parent_session_id=自身/subagent_id=子会话，这些是请求级标注，绝不能写进父会话
// 自己的行：否则根会话会被列表过滤（s.parent_session_id = ''）漏掉，会话视图的
// rollup 口径也会退化为"仅自身"（2026-10-09 实测父行 parent=自身）。
func (c *collector) sessionLineageMeta(sessionID string, meta SessionMeta, requestLineage SessionLineage) SessionMeta {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return meta
	}
	candidate := SessionLineage{}
	found := false
	if c != nil && c.lineage != nil {
		if looked, ok := c.lineage.SessionLineage(sessionID); ok {
			candidate, found = looked, true
		}
	}
	if !found {
		if learned, ok := c.learnedParent(sessionID); ok {
			candidate, found = learned, true
		}
	}
	if !found && strings.TrimSpace(requestLineage.ParentSessionID) != "" {
		candidate, found = requestLineage, true
	}
	if !found || strings.TrimSpace(candidate.ParentSessionID) == sessionID {
		return meta
	}
	meta.ParentSessionID = strings.TrimSpace(candidate.ParentSessionID)
	meta.SubagentID = strings.TrimSpace(candidate.SubagentID)
	return meta
}

// upsertRequest 幂等写入一条请求终态行（同 llm_request_id 覆盖）。
func (c *collector) upsertRequest(record cacheanalytics.CacheRequestRecord) {
	if c == nil || c.store == nil {
		return
	}
	statement, args := c.requestUpsertStatement(record)
	if err := c.store.execWithLockRetry(statement, args...); err != nil {
		c.reportWriteFailure("usage_requests", err)
	}
}

// requestUpsertStatement 构造 usage_requests 幂等 UPSERT（ON CONFLICT 语义不变）。
func (c *collector) requestUpsertStatement(record cacheanalytics.CacheRequestRecord) (string, []interface{}) {
	startedAt := record.StartedAt
	if startedAt.IsZero() && record.FinishedAt != nil {
		startedAt = *record.FinishedAt
	}
	payload, err := json.Marshal(record)
	if err != nil {
		payload = []byte{}
	}
	var promptTokens, completionTokens, totalTokens, cacheRead, cacheCreation, uncachedInput, inputTotal, reasoning int64
	if record.Usage != nil {
		promptTokens = record.Usage.PromptTokens
		completionTokens = record.Usage.CompletionTokens
		totalTokens = record.Usage.TotalTokens
		cacheRead = record.Usage.CacheReadTokens
		cacheCreation = record.Usage.CacheCreationTokens
		uncachedInput = record.Usage.UncachedInputTokens
		inputTotal = record.Usage.InputTotal()
		reasoning = record.Usage.ReasoningTokens
	}
	success := 0
	if record.Status == cacheanalytics.RequestStatusSuccess {
		success = 1
	}
	usageAvailable := 0
	if record.Usage != nil {
		usageAvailable = 1
	}
	const statement = `
INSERT INTO usage_requests (
  llm_request_id, session_id, parent_session_id, root_session_id, subagent_id, trace_id, turn_id, step, provider, model, status, cache_status,
  success, error_category, started_at_unix_nano, duration_ms, first_token_ms, prompt_tokens, completion_tokens,
  cache_read_tokens, cache_creation_tokens, uncached_input_tokens, input_total_tokens, reasoning_tokens, total_tokens, usage_available, record_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(llm_request_id) DO UPDATE SET
  session_id = excluded.session_id,
  parent_session_id = CASE
    WHEN excluded.parent_session_id <> '' THEN excluded.parent_session_id
    WHEN usage_requests.session_id = excluded.session_id THEN usage_requests.parent_session_id
    ELSE '' END,
  root_session_id = CASE
    -- 同一会话且新值退化为自身、旧值有真实根：保留旧根（重复终态/丢帧回放）。
    WHEN usage_requests.session_id = excluded.session_id
      AND excluded.root_session_id = excluded.session_id
      AND usage_requests.root_session_id <> ''
      AND usage_requests.root_session_id <> excluded.session_id
      THEN usage_requests.root_session_id
    WHEN excluded.root_session_id <> '' THEN excluded.root_session_id
    ELSE excluded.session_id END,
  subagent_id = CASE
    WHEN excluded.subagent_id <> '' THEN excluded.subagent_id
    WHEN usage_requests.session_id = excluded.session_id THEN usage_requests.subagent_id
    ELSE '' END,
  trace_id = excluded.trace_id,
  turn_id = excluded.turn_id,
  step = excluded.step,
  provider = CASE WHEN excluded.provider <> '' THEN excluded.provider ELSE usage_requests.provider END,
  model = CASE WHEN excluded.model <> '' THEN excluded.model ELSE usage_requests.model END,
  status = excluded.status,
  cache_status = excluded.cache_status,
  success = excluded.success,
  error_category = excluded.error_category,
  started_at_unix_nano = CASE WHEN usage_requests.started_at_unix_nano = 0 THEN excluded.started_at_unix_nano ELSE usage_requests.started_at_unix_nano END,
  duration_ms = excluded.duration_ms,
  first_token_ms = CASE WHEN excluded.first_token_ms <> 0 THEN excluded.first_token_ms ELSE usage_requests.first_token_ms END,
  prompt_tokens = excluded.prompt_tokens,
  completion_tokens = excluded.completion_tokens,
  cache_read_tokens = excluded.cache_read_tokens,
  cache_creation_tokens = excluded.cache_creation_tokens,
  uncached_input_tokens = excluded.uncached_input_tokens,
  input_total_tokens = excluded.input_total_tokens,
  reasoning_tokens = excluded.reasoning_tokens,
  total_tokens = excluded.total_tokens,
  usage_available = excluded.usage_available,
  record_json = excluded.record_json`
	rootSessionID := record.RootSessionID
	if rootSessionID == "" {
		rootSessionID = record.SessionID
	}
	return statement, []interface{}{
		record.LLMRequestID,
		record.SessionID,
		record.ParentSessionID,
		rootSessionID,
		record.SubagentID,
		record.TraceID,
		record.TurnID,
		record.Step,
		record.Provider,
		record.Model,
		record.Status,
		record.CacheStatus,
		success,
		record.ErrorCategory,
		startedAt.UnixNano(),
		record.DurationMS,
		record.FirstTokenMS,
		promptTokens,
		completionTokens,
		cacheRead,
		cacheCreation,
		uncachedInput,
		inputTotal,
		reasoning,
		totalTokens,
		usageAvailable,
		string(payload),
	}
}

// reportWriteFailure 记录一次分析库写入失败（此前完全静默：行丢失且无痕迹）。
func (c *collector) reportWriteFailure(table string, err error) {
	if c == nil || err == nil {
		return
	}
	failures := c.writeFailureCount.Add(1)
	if failures == 1 || failures%100 == 0 {
		degradeWarn("写入 %s 失败（累计 %d 次，本行已丢失）：%v", table, failures, err)
	}
}

// upsertSession 幂等合并会话元数据：空值不覆盖已有值；查找成功时补齐
// title/project_path/working_directory/provider/model/protocol/status。
func (c *collector) upsertSession(sessionID string, meta SessionMeta, startedAt, endedAt time.Time) {
	if c == nil || c.store == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	statement, args := c.sessionUpsertStatement(sessionID, meta, startedAt, endedAt)
	if err := c.store.execWithLockRetry(statement, args...); err != nil {
		c.reportWriteFailure("usage_sessions", err)
	}
}

// sessionUpsertStatement 构造 usage_sessions 元数据合并 UPSERT（空值不覆盖）。
func (c *collector) sessionUpsertStatement(sessionID string, meta SessionMeta, startedAt, endedAt time.Time) (string, []interface{}) {
	now := c.now()
	if c.lookup != nil {
		if looked, ok := c.lookup.SessionMeta(sessionID); ok {
			meta = mergeSessionMeta(meta, looked)
		}
	}
	var started, ended int64
	if !startedAt.IsZero() {
		started = startedAt.UnixNano()
	}
	if !endedAt.IsZero() {
		ended = endedAt.UnixNano()
	}
	const statement = `
INSERT INTO usage_sessions (
  session_id, parent_session_id, subagent_id, title, project_path, working_directory, provider, model, protocol, status,
  started_at_unix_nano, ended_at_unix_nano, updated_at_unix_nano, meta_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,NULL)
ON CONFLICT(session_id) DO UPDATE SET
  parent_session_id = CASE WHEN excluded.parent_session_id <> '' THEN excluded.parent_session_id ELSE usage_sessions.parent_session_id END,
  subagent_id = CASE WHEN excluded.subagent_id <> '' THEN excluded.subagent_id ELSE usage_sessions.subagent_id END,
  title = CASE WHEN excluded.title <> '' THEN excluded.title ELSE usage_sessions.title END,
  project_path = CASE WHEN excluded.project_path <> '' THEN excluded.project_path ELSE usage_sessions.project_path END,
  working_directory = CASE WHEN excluded.working_directory <> '' THEN excluded.working_directory ELSE usage_sessions.working_directory END,
  provider = CASE WHEN excluded.provider <> '' THEN excluded.provider ELSE usage_sessions.provider END,
  model = CASE WHEN excluded.model <> '' THEN excluded.model ELSE usage_sessions.model END,
  protocol = CASE WHEN excluded.protocol <> '' THEN excluded.protocol ELSE usage_sessions.protocol END,
  status = CASE WHEN excluded.status <> '' THEN excluded.status ELSE usage_sessions.status END,
  started_at_unix_nano = CASE
    WHEN excluded.started_at_unix_nano = 0 THEN usage_sessions.started_at_unix_nano
    WHEN usage_sessions.started_at_unix_nano = 0 THEN excluded.started_at_unix_nano
    ELSE MIN(usage_sessions.started_at_unix_nano, excluded.started_at_unix_nano) END,
  ended_at_unix_nano = MAX(usage_sessions.ended_at_unix_nano, excluded.ended_at_unix_nano),
  updated_at_unix_nano = MAX(usage_sessions.updated_at_unix_nano, excluded.updated_at_unix_nano)`
	return statement, []interface{}{
		sessionID,
		meta.ParentSessionID,
		meta.SubagentID,
		meta.Title,
		meta.ProjectPath,
		meta.WorkingDirectory,
		meta.Provider,
		meta.Model,
		meta.Protocol,
		meta.Status,
		started,
		ended,
		now.UnixNano(),
	}
}

// mergeSessionMeta 事件元数据 + 查找元数据（查找值补齐事件缺失字段）。
func mergeSessionMeta(primary, fallback SessionMeta) SessionMeta {
	merged := primary
	if merged.Title == "" {
		merged.Title = fallback.Title
	}
	if merged.ProjectPath == "" {
		merged.ProjectPath = fallback.ProjectPath
	}
	if merged.WorkingDirectory == "" {
		merged.WorkingDirectory = fallback.WorkingDirectory
	}
	if merged.Provider == "" {
		merged.Provider = fallback.Provider
	}
	if merged.Model == "" {
		merged.Model = fallback.Model
	}
	if merged.Protocol == "" {
		merged.Protocol = fallback.Protocol
	}
	if merged.Status == "" {
		merged.Status = fallback.Status
	}
	return merged
}

// ---------------------------------------------------------------------------
// 载荷读取辅助（与 cacheanalytics 同语义：缺失/类型不符返回零值）。
// ---------------------------------------------------------------------------

func payloadString(payload map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if payload == nil {
			return ""
		}
		raw, ok := payload[key]
		if !ok || raw == nil {
			continue
		}
		if value, ok := raw.(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
			continue
		}
		return strings.TrimSpace(fmt.Sprintf("%v", raw))
	}
	return ""
}

func payloadInt(payload map[string]interface{}, key string) int {
	if payload == nil {
		return 0
	}
	switch value := payload[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0
		}
		return int(parsed)
	default:
		return 0
	}
}
