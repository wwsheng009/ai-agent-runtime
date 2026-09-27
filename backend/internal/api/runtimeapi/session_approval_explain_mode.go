package runtimeapi

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/approvalexplain"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// §4.13 解释模式与结果缓存。
//
// 三个模式：
//   - off：只用规则摘要，永不调用模型（适合对成本敏感或不需要解释的部署）；
//   - on_demand（默认）：用户点「解释」才调用一次模型；
//   - pre_generate：审批一在 Web 端露面（读 /runtime 快照时）就后台预生成，
//     用户点「解释」直接命中缓存。
//
// 无论哪种模式，模型结果都会按「会话 + 审批 ID」缓存一小段时间：
// 同一审批重复点击只计费一次；审批被裁决后换上下一条时，ID 不匹配即视为未命中。
// 失败结果**不缓存**，避免把一次网络抖动钉死成永远降级。

// ApprovalExplainMode 是审批解释的生成策略。取值与解析语义由共享包
// internal/approvalexplain 定义（本地模式与 runtime-server 必须一致）；
// 这里的别名只保持 runtimeapi 既有 API 面不变。
type ApprovalExplainMode = approvalexplain.Mode

const (
	ApprovalExplainModeOff         = approvalexplain.ModeOff
	ApprovalExplainModeOnDemand    = approvalexplain.ModeOnDemand
	ApprovalExplainModePreGenerate = approvalexplain.ModePreGenerate

	// 缓存寿命：审批本身是短生命周期对象；过期即失效，长会话不会无限增长。
	approvalExplainCacheTTL = 10 * time.Minute
	// 缓存条目上限（按会话计）：超出时先清过期项，再丢弃最早过期的一条。
	approvalExplainCacheLimit = 256
)

// ParseApprovalExplainMode 解析模式取值；空串表示默认（on_demand）。
// 未知取值返回 ok=false，由调用方决定是报错还是保持默认。实现委托共享包。
func ParseApprovalExplainMode(raw string) (ApprovalExplainMode, bool) {
	return approvalexplain.ParseMode(raw)
}

// SetApprovalExplainMode 设置解释模式；空值回到默认（on_demand）。
// 非法取值返回错误且**不改变**当前状态（避免把错配置静默当成默认）。
func (h *Handler) SetApprovalExplainMode(raw string) error {
	if h == nil {
		return fmt.Errorf("handler is nil")
	}
	mode, ok := ParseApprovalExplainMode(raw)
	if !ok {
		return fmt.Errorf("unsupported approval explain mode %q (off|on_demand|pre_generate)", strings.TrimSpace(raw))
	}
	h.approvalExplainModeMu.Lock()
	h.approvalExplainMode = mode
	h.approvalExplainModeMu.Unlock()
	return nil
}

// ApprovalExplainMode 返回当前模式；未显式设置时是 on_demand。
func (h *Handler) ApprovalExplainMode() ApprovalExplainMode {
	if h == nil {
		return ApprovalExplainModeOnDemand
	}
	h.approvalExplainModeMu.RLock()
	defer h.approvalExplainModeMu.RUnlock()
	if h.approvalExplainMode == "" {
		return ApprovalExplainModeOnDemand
	}
	return h.approvalExplainMode
}

func (h *Handler) approvalExplainCacheStore() *approvalExplainCache {
	if h == nil {
		return nil
	}
	h.approvalExplainCacheOnce.Do(func() {
		h.approvalExplainCache = newApprovalExplainCache(approvalExplainCacheTTL, approvalExplainCacheLimit)
	})
	return h.approvalExplainCache
}

// approvalExplanationForRequest 取一条审批的解释：缓存 → 单飞模型调用 → 规则降级。
// 无论走哪条分支都返回 200 语义的 payload（端点对模型故障永不 5xx）。
func (h *Handler) approvalExplanationForRequest(ctx context.Context, sessionID string, pending *chat.ApprovalRequest) sessionApprovalExplanationPayload {
	mode := h.ApprovalExplainMode()
	fallback := sessionApprovalExplanationPayload{
		Explanation: ruleBasedApprovalExplanation(pending),
		Source:      approvalExplainSourceRules,
		Mode:        string(mode),
	}
	if mode == ApprovalExplainModeOff || pending == nil {
		return fallback
	}

	requestID := strings.TrimSpace(pending.ID)
	cache := h.approvalExplainCacheStore()
	if payload, ok := cache.get(sessionID, requestID); ok {
		return markApprovalExplanationCached(payload, mode)
	}

	key := approvalExplainCacheKey(sessionID, requestID)
	leader, wait := cache.acquire(key)
	if !leader {
		// 并发重复点击：等第一次调用出结果，绝不重复计费；等不到就退回规则摘要。
		if !waitForApprovalExplanation(ctx, wait) {
			return fallback
		}
		if payload, ok := cache.get(sessionID, requestID); ok {
			return markApprovalExplanationCached(payload, mode)
		}
		return fallback
	}
	defer cache.release(key)
	// 等锁期间可能已有结果（前一个调用刚写完缓存）。
	if payload, ok := cache.get(sessionID, requestID); ok {
		return markApprovalExplanationCached(payload, mode)
	}

	explanation, model, err := h.summarizeApproval(ctx, sessionID, pending)
	if err != nil || strings.TrimSpace(explanation) == "" {
		return fallback
	}
	payload := sessionApprovalExplanationPayload{
		Explanation: strings.TrimSpace(explanation),
		Source:      approvalExplainSourceModel,
		Model:       model,
		Mode:        string(mode),
	}
	cache.put(sessionID, requestID, payload)
	return payload
}

// MaybeWarmApprovalExplanation 在 pre_generate 模式下为刚出现的 pending 审批
// 预生成解释：由读路径（/runtime 快照）在观察到审批时调用。
//
// 非阻塞：真正的工作在后台 goroutine 里，失败静默（失败不缓存，下次点击仍会
// 走按需路径）；重复调用由缓存与单飞去重。
func (h *Handler) MaybeWarmApprovalExplanation(state *chat.RuntimeState) {
	if h == nil || state == nil || state.PendingApproval == nil {
		return
	}
	if h.ApprovalExplainMode() != ApprovalExplainModePreGenerate {
		return
	}
	// 复制一份再进 goroutine：state 可能被 store 侧继续改写。
	pending := *state.PendingApproval
	if strings.TrimSpace(pending.ID) == "" {
		return
	}
	sessionID := strings.TrimSpace(pending.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(state.SessionID)
	}
	if sessionID == "" {
		return
	}
	if cache := h.approvalExplainCacheStore(); cache != nil && cache.has(sessionID, pending.ID) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), approvalExplainTimeout)
		defer cancel()
		h.warmApprovalExplanation(ctx, sessionID, &pending)
	}()
}

// warmApprovalExplanation 是预生成的实际执行体：与端点共用单飞与缓存，
// 因此「预生成未完成时用户点了解释」只会等待同一次调用，不会变成两笔费用。
func (h *Handler) warmApprovalExplanation(ctx context.Context, sessionID string, pending *chat.ApprovalRequest) {
	if h == nil || pending == nil {
		return
	}
	cache := h.approvalExplainCacheStore()
	if cache == nil {
		return
	}
	requestID := strings.TrimSpace(pending.ID)
	if requestID == "" {
		return
	}
	key := approvalExplainCacheKey(sessionID, requestID)
	leader, _ := cache.acquire(key)
	if !leader {
		return
	}
	defer cache.release(key)
	if cache.has(sessionID, requestID) {
		return
	}
	explanation, model, err := h.summarizeApproval(ctx, sessionID, pending)
	if err != nil || strings.TrimSpace(explanation) == "" {
		return
	}
	cache.put(sessionID, requestID, sessionApprovalExplanationPayload{
		Explanation: strings.TrimSpace(explanation),
		Source:      approvalExplainSourceModel,
		Model:       model,
		Mode:        string(ApprovalExplainModePreGenerate),
	})
}

func markApprovalExplanationCached(payload sessionApprovalExplanationPayload, mode ApprovalExplainMode) sessionApprovalExplanationPayload {
	payload.Mode = string(mode)
	payload.Cached = true
	return payload
}

func waitForApprovalExplanation(ctx context.Context, wait <-chan struct{}) bool {
	if wait == nil {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-wait:
		return true
	case <-ctx.Done():
		return false
	}
}

func approvalExplainCacheKey(sessionID, requestID string) string {
	return strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(requestID)
}

type approvalExplainCacheEntry struct {
	RequestID string
	Payload   sessionApprovalExplanationPayload
	ExpiresAt time.Time
}

// approvalExplainCache 是进程内的审批解释缓存 + 单飞表。
//
// 只存「成功的模型结果」；条目按会话键存放，读时校验 request_id 一致，
// 因此同一会话换了下一条审批不会命中上一条的内容。
type approvalExplainCache struct {
	mu       sync.Mutex
	entries  map[string]approvalExplainCacheEntry
	inflight map[string]chan struct{}
	now      func() time.Time
	ttl      time.Duration
	limit    int
}

func newApprovalExplainCache(ttl time.Duration, limit int) *approvalExplainCache {
	if ttl <= 0 {
		ttl = approvalExplainCacheTTL
	}
	if limit <= 0 {
		limit = approvalExplainCacheLimit
	}
	return &approvalExplainCache{
		entries:  make(map[string]approvalExplainCacheEntry),
		inflight: make(map[string]chan struct{}),
		now:      time.Now,
		ttl:      ttl,
		limit:    limit,
	}
}

func (c *approvalExplainCache) get(sessionID, requestID string) (sessionApprovalExplanationPayload, bool) {
	if c == nil {
		return sessionApprovalExplanationPayload{}, false
	}
	key := approvalExplainCacheKey(sessionID, requestID)
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return sessionApprovalExplanationPayload{}, false
	}
	if entry.RequestID != strings.TrimSpace(requestID) || !c.now().Before(entry.ExpiresAt) {
		delete(c.entries, key)
		return sessionApprovalExplanationPayload{}, false
	}
	return entry.Payload, true
}

func (c *approvalExplainCache) has(sessionID, requestID string) bool {
	_, ok := c.get(sessionID, requestID)
	return ok
}

func (c *approvalExplainCache) put(sessionID, requestID string, payload sessionApprovalExplanationPayload) {
	if c == nil {
		return
	}
	key := approvalExplainCacheKey(sessionID, requestID)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictExpiredLocked()
	for c.limit > 0 && len(c.entries) >= c.limit {
		oldestKey := ""
		var oldest time.Time
		for candidate, entry := range c.entries {
			if oldestKey == "" || entry.ExpiresAt.Before(oldest) {
				oldestKey, oldest = candidate, entry.ExpiresAt
			}
		}
		if oldestKey == "" {
			break
		}
		delete(c.entries, oldestKey)
	}
	c.entries[key] = approvalExplainCacheEntry{
		RequestID: strings.TrimSpace(requestID),
		Payload:   payload,
		ExpiresAt: c.now().Add(c.ttl),
	}
}

func (c *approvalExplainCache) evictExpiredLocked() {
	now := c.now()
	for key, entry := range c.entries {
		if !now.Before(entry.ExpiresAt) {
			delete(c.entries, key)
		}
	}
}

// acquire 返回 (是否为本次调用的发起者, 等待通道)。非发起者应等待通道关闭后
// 重新读缓存；发起者负责在结束时 release（关闭通道并清空在途标记）。
func (c *approvalExplainCache) acquire(key string) (bool, <-chan struct{}) {
	if c == nil {
		return true, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if wait, ok := c.inflight[key]; ok {
		return false, wait
	}
	wait := make(chan struct{})
	c.inflight[key] = wait
	return true, wait
}

func (c *approvalExplainCache) release(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if wait, ok := c.inflight[key]; ok {
		delete(c.inflight, key)
		close(wait)
	}
}
