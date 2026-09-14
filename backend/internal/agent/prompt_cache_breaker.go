package agent

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// PR-4 §6.4 第 5 条：prompt cache 熔断与 UPSTREAM_INVALID_RESPONSE 聚合。
//
// 现场问题有两个，根因同源——重试是**每次尝试**都对外发一条 llm.retry 事件，
// 而 llm.retry 在 TUI 里是动态状态行（composer 上方），于是同一个
// UPSTREAM_INVALID_RESPONSE 在一次 run 内被刷 6 次，状态行反复翻转；同时
// 模型对**完全相同的 prompt**（同一 prompt_fingerprint）反复失败时，循环仍按
// 原节奏重发，没有任何短期抑制。
//
// 本模块只做两件事，都不改变既有重试语义（bounded backoff 仍由 llm 包决定）：
//
//  1. 同一 prompt_fingerprint **连续失败**达到阈值 → 打开短期熔断窗口；
//     窗口内该指纹的下一次请求必须先退避（指数、有上限）再发。
//  2. UPSTREAM_INVALID_RESPONSE 的重试事件**聚合计数**：首次照常上报（状态行
//     仍能看到"正在重试"），其后同类事件不再逐条外发，run 结束时一次性
//     WARN 计数上报（现场 6 次 → 1 条首次 + 1 条汇总）。
//
// 全部状态按 run 隔离：熔断器经 run ctx 下发，不落在 ReActLoop 结构体上，
// 因此同 loop 上的并发/串行 run 不会互相污染计数器。

// 熔断缺省值。阈值取 3：两次连续失败仍可能是上游抖动，第三次同指纹失败说明
// 请求本身（同 prompt + 同 cache key）在浪费配额。
const (
	DefaultPromptCacheBreakerFailureThreshold = 3
	DefaultPromptCacheBreakerCooldown         = 2 * time.Minute
	DefaultPromptCacheBreakerBaseBackoff      = 2 * time.Second
	DefaultPromptCacheBreakerMaxBackoff       = 30 * time.Second
)

// UpstreamInvalidResponseCode 是 llm.retry 事件里需要聚合的错误码。
const UpstreamInvalidResponseCode = "UPSTREAM_INVALID_RESPONSE"

// PromptCacheBreakerSpec 描述熔断策略。零值经 normalized() 回落到缺省值，
// 因此宿主不配置也能工作；测试可注入确定性参数。
type PromptCacheBreakerSpec struct {
	FailureThreshold int
	Cooldown         time.Duration
	BaseBackoff      time.Duration
	MaxBackoff       time.Duration
}

func (spec PromptCacheBreakerSpec) normalized() PromptCacheBreakerSpec {
	if spec.FailureThreshold <= 0 {
		spec.FailureThreshold = DefaultPromptCacheBreakerFailureThreshold
	}
	if spec.Cooldown <= 0 {
		spec.Cooldown = DefaultPromptCacheBreakerCooldown
	}
	if spec.BaseBackoff <= 0 {
		spec.BaseBackoff = DefaultPromptCacheBreakerBaseBackoff
	}
	if spec.MaxBackoff <= 0 {
		spec.MaxBackoff = DefaultPromptCacheBreakerMaxBackoff
	}
	if spec.MaxBackoff < spec.BaseBackoff {
		spec.MaxBackoff = spec.BaseBackoff
	}
	return spec
}

type promptBreakerEntry struct {
	consecutive int
	trips       int
	openedAt    time.Time
	backoff     time.Duration
	waitedAt    time.Time
	lastCode    string
}

// PromptCacheBreakerDecision 是一次失败观测的判决结果。
type PromptCacheBreakerDecision struct {
	Fingerprint string
	Consecutive int
	// Tripped 只在跨过阈值的那一次为 true（边沿），避免重复通告。
	Tripped bool
	// Open 表示该指纹当前处于熔断窗口内。
	Open bool
	// Backoff 是窗口内下一次同指纹请求应等待的时长。
	Backoff time.Duration
	// Trips 是该指纹累计触发熔断的次数。
	Trips     int
	ErrorCode string
}

// PromptCacheBreakerAggregate 是 UPSTREAM_INVALID_RESPONSE 的一次性聚合快照。
type PromptCacheBreakerAggregate struct {
	ErrorCode    string   `json:"error_code"`
	Count        int      `json:"count"`
	Fingerprints []string `json:"prompt_fingerprints,omitempty"`
}

// PromptCacheBreakerSnapshot 是 /debug 与宿主可读的运行态投影。
type PromptCacheBreakerSnapshot struct {
	TrackedFingerprints int
	OpenFingerprints    int
	Trips               int
	AggregatedEvents    int
	AggregatedCodes     int
}

// PromptCacheBreaker 是 per-run 的 prompt 指纹失败跟踪器。所有方法对 nil
// 接收者安全：未接线（例如子代理 loop 未注入）时退化为观测空操作。
type PromptCacheBreaker struct {
	mu sync.Mutex

	spec    PromptCacheBreakerSpec
	entries map[string]*promptBreakerEntry

	aggregated           map[string]int
	aggregatedTotal      int
	aggregatedFingerprnt map[string]struct{}
}

// NewPromptCacheBreaker 创建熔断器；spec 零值使用缺省策略。
func NewPromptCacheBreaker(spec PromptCacheBreakerSpec) *PromptCacheBreaker {
	return &PromptCacheBreaker{
		spec:                 spec.normalized(),
		entries:              make(map[string]*promptBreakerEntry),
		aggregated:           make(map[string]int),
		aggregatedFingerprnt: make(map[string]struct{}),
	}
}

// ObserveFailure 记录一次以 errorCode 失败的调用，并返回熔断判决。
func (b *PromptCacheBreaker) ObserveFailure(fingerprint, errorCode string, now time.Time) PromptCacheBreakerDecision {
	decision := PromptCacheBreakerDecision{
		Fingerprint: strings.TrimSpace(fingerprint),
		ErrorCode:   strings.TrimSpace(errorCode),
	}
	if b == nil {
		return decision
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if decision.Fingerprint == "" {
		return decision
	}
	if now.IsZero() {
		now = time.Now()
	}
	entry := b.entries[decision.Fingerprint]
	if entry == nil {
		entry = &promptBreakerEntry{}
		b.entries[decision.Fingerprint] = entry
	}
	entry.consecutive++
	entry.lastCode = decision.ErrorCode
	decision.Consecutive = entry.consecutive

	// 窗口到期即视为冷却完成：连续计数从本次起重新累积，但退避基数保留，
	// 让反复触发的指纹下一次更快进入熔断（指数退避语义）。
	if entry.trips > 0 && !entry.openedAt.IsZero() && now.Sub(entry.openedAt) >= b.spec.Cooldown {
		entry.consecutive = 1
		decision.Consecutive = 1
		entry.openedAt = time.Time{}
	}
	if entry.consecutive >= b.spec.FailureThreshold {
		entry.trips++
		entry.openedAt = now
		entry.backoff = b.nextBackoff(entry.trips)
		entry.waitedAt = time.Time{}
		// Tripped 是**边沿**信号：只有恰好跨过阈值（含冷却后重新累积再跨过）
		// 才通告一次；窗口内继续失败只升级退避，不重复刷事件。
		decision.Tripped = entry.consecutive == b.spec.FailureThreshold
		decision.Trips = entry.trips
	}
	decision.Open = entry.trips > 0 && !entry.openedAt.IsZero()
	decision.Backoff = entry.backoff
	if decision.Open && !entry.openedAt.IsZero() && now.Sub(entry.openedAt) >= b.spec.Cooldown {
		decision.Open = false
	}
	return decision
}

// ObserveSuccess 清除该指纹的连续失败计数（成功即恢复）。
func (b *PromptCacheBreaker) ObserveSuccess(fingerprint string) {
	if b == nil {
		return
	}
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.entries, fingerprint)
}

// PendingBackoff 报告该指纹在熔断窗口内、且本轮窗口尚未退避过时应等待的时长。
// 每个窗口只退避一次：退避后仍失败会再次计入连续失败并在下一次窗口再退避，
// 避免同一个窗口里叠加重试（熔断退避与 llm 包的有界重试是两层，不互相替代）。
func (b *PromptCacheBreaker) PendingBackoff(fingerprint string, now time.Time) (time.Duration, bool) {
	if b == nil {
		return 0, false
	}
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return 0, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.entries[fingerprint]
	if entry == nil || entry.openedAt.IsZero() || entry.backoff <= 0 {
		return 0, false
	}
	if now.IsZero() {
		now = time.Now()
	}
	if now.Sub(entry.openedAt) >= b.spec.Cooldown {
		return 0, false
	}
	if !entry.waitedAt.IsZero() {
		return 0, false
	}
	entry.waitedAt = now
	return entry.backoff, true
}

// AggregateRetryError 处理一条 llm.retry 事件的错误码。返回 suppress=true 时
// 调用方不应再逐条外发该事件（已被计数）。首次出现返回 count=1、suppress=false，
// 保证状态行至少能看到一次"正在重试"，其后同类只累计。
func (b *PromptCacheBreaker) AggregateRetryError(fingerprint, errorCode string) (bool, int) {
	if b == nil {
		return false, 0
	}
	errorCode = strings.TrimSpace(errorCode)
	if errorCode != UpstreamInvalidResponseCode {
		return false, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.aggregated[errorCode]++
	b.aggregatedTotal++
	if fingerprint = strings.TrimSpace(fingerprint); fingerprint != "" {
		b.aggregatedFingerprnt[fingerprint] = struct{}{}
	}
	count := b.aggregated[errorCode]
	return count > 1, count
}

// FlushAggregated 取走聚合计数并重置；ok=false 表示本段没有可上报的内容。
func (b *PromptCacheBreaker) FlushAggregated() (PromptCacheBreakerAggregate, bool) {
	if b == nil {
		return PromptCacheBreakerAggregate{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.aggregated) == 0 {
		return PromptCacheBreakerAggregate{}, false
	}
	errorCodes := make([]string, 0, len(b.aggregated))
	for code := range b.aggregated {
		errorCodes = append(errorCodes, code)
	}
	// 稳定输出：同一批上报的顺序不随 map 迭代变化。
	sort.Strings(errorCodes)
	primary := errorCodes[0]
	aggregate := PromptCacheBreakerAggregate{
		ErrorCode:    primary,
		Count:        b.aggregated[primary],
		Fingerprints: sortedPromptBreakerKeys(b.aggregatedFingerprnt),
	}
	b.aggregated = make(map[string]int)
	b.aggregatedFingerprnt = make(map[string]struct{})
	return aggregate, true
}

// AggregatedTotal 是本 run 累计被聚合（含首次上报）的同类事件数，供 Result 回填。
func (b *PromptCacheBreaker) AggregatedTotal() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.aggregatedTotal
}

// Trips 是该 run 累计触发熔断的指纹次数，供 Result 回填。
func (b *PromptCacheBreaker) Trips() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	total := 0
	for _, entry := range b.entries {
		total += entry.trips
	}
	return total
}

// Snapshot 投影运行态；/debug 与 observe 可据此渲染，不需要二次推导。
func (b *PromptCacheBreaker) Snapshot(now time.Time) PromptCacheBreakerSnapshot {
	if b == nil {
		return PromptCacheBreakerSnapshot{}
	}
	if now.IsZero() {
		now = time.Now()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	snapshot := PromptCacheBreakerSnapshot{
		TrackedFingerprints: len(b.entries),
		AggregatedEvents:    b.aggregatedTotal,
		AggregatedCodes:     len(b.aggregated),
	}
	for _, entry := range b.entries {
		snapshot.Trips += entry.trips
		if !entry.openedAt.IsZero() && now.Sub(entry.openedAt) < b.spec.Cooldown {
			snapshot.OpenFingerprints++
		}
	}
	return snapshot
}

func (b *PromptCacheBreaker) nextBackoff(trips int) time.Duration {
	backoff := b.spec.BaseBackoff
	for i := 1; i < trips; i++ {
		backoff *= 2
		if backoff >= b.spec.MaxBackoff {
			return b.spec.MaxBackoff
		}
	}
	if backoff > b.spec.MaxBackoff {
		return b.spec.MaxBackoff
	}
	return backoff
}

type promptCacheBreakerContextKey struct{}

func sortedPromptBreakerKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// WithPromptCacheBreaker 把 run 级熔断器下发到 ctx；run() 注入一次，think()
// 与重试事件上报器读取同一实例。
func WithPromptCacheBreaker(ctx context.Context, breaker *PromptCacheBreaker) context.Context {
	if breaker == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, promptCacheBreakerContextKey{}, breaker)
}

func promptCacheBreakerFromContext(ctx context.Context) *PromptCacheBreaker {
	if ctx == nil {
		return nil
	}
	breaker, _ := ctx.Value(promptCacheBreakerContextKey{}).(*PromptCacheBreaker)
	return breaker
}

// promptFingerprintFromRequest 读取 think() 在发起请求前盖在 Metadata 上的
// prompt_fingerprint。上报器闭包持有 req 指针，因此事件触发时读到的是本次
// 请求的指纹（盖章早于 Call，见 loop.go 的 llm.request.started 之前）。
func promptFingerprintFromRequest(req *llm.LLMRequest) string {
	if req == nil || req.Metadata == nil {
		return ""
	}
	return strings.TrimSpace(stringValue(req.Metadata["prompt_fingerprint"]))
}

// waitPromptCacheBackoff 在 ctx 约束下等待退避；ctx 取消时立即返回，不伪等待。
func waitPromptCacheBackoff(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	if ctx == nil {
		<-timer.C
		return nil
	}
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
