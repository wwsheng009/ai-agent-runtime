// Package providerhealth 维护 provider/model 粒度的动态健康状态。
//
// 子 Agent 路由（internal/modelrouting）此前只能依据人工写的 availability 标注
// 判断某个后端是否可用。标注是静态的：provider 凌晨挂掉时路由不会知道，除非
// 有人手动改配置。本包补上这一环——记录每次 LLM 调用的真实成败，聚合成熔断
// 状态，供路由在**构造子 Agent 时**读取一次。
//
// 四条刻意的设计约束：
//
//  1. 只有 provider 归因的失败才计入（provider_error / rate_limited / timeout /
//     interrupted）。tool_error、budget_exceeded、context_overflow、cancelled
//     是我们自己的问题或用户行为，记进去会在自身 bug 上误熔断，反而在最需要
//     路由的时候把健康的后端摘掉。
//  2. 读取（Lookup）是纯读：不消费探测令牌、不产生副作用。路由在 Resolve 时读
//     一次，子 Agent 的整个生命周期沿用该判定，不引入请求级重解析。
//  3. 时间由调用方注入，包内不调用 time.Now()，保证测试确定性。
//  4. 已打开的电路不因持续失败而重新计时，否则 provider 永远进不了半开，也就
//     永远没有恢复的机会。
package providerhealth

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// 健康状态三值。语义与 modelrouting 的 availability 标注对齐：
// healthy ≈ available（照常路由），degraded ≈ degraded（可用但标注），
// unhealthy ≈ unavailable（按 availability_policy 决定是否跳过）。
const (
	StateHealthy   = "healthy"
	StateDegraded  = "degraded"
	StateUnhealthy = "unhealthy"
)

// 熔断缺省值。与 backend/configs/config.yaml 的 circuit_breaker 块保持同一组
// 语义：配置缺省或不可解析时回落到这里。
const (
	DefaultFailureThreshold = 3
	DefaultFailureRate      = 0.5
	DefaultSampleThreshold  = 10
	DefaultWindowDuration   = 10 * time.Second
	DefaultOpenTimeout      = 60 * time.Second
	DefaultHalfOpenMaxCalls = 1
)

// maxSamplesPerTarget 给每个目标的滑动窗口设硬上限。窗口本身会裁剪，但若调用方
// 注入的时间不单调（或窗口被配得极大），无上限的切片会随调用量线性增长。
const maxSamplesPerTarget = 512

// Health 是一个 provider/model 目标的观测健康快照。
type Health struct {
	Provider            string
	Model               string
	State               string
	Reason              string
	ConsecutiveFailures int
	Samples             int
	Failures            int
	FailureRate         float64
	OpenedAt            time.Time
	LastFailureAt       time.Time
	LastCategory        string
}

// Healthy 是"可照常路由"的便捷判断。
func (h Health) Healthy() bool { return h.State == StateHealthy }

// Spec 描述熔断策略。零值经 Normalized 后回落到 DefaultSpec。
type Spec struct {
	FailureThreshold int           // 连续失败达到该值即熔断
	FailureRate      float64       // 窗口内失败率达到该值即熔断
	SampleThreshold  int           // 窗口内样本数达到该值后，失败率规则才生效
	WindowDuration   time.Duration // 失败率滑动窗口长度
	OpenTimeout      time.Duration // 熔断打开后多久进入半开
	HalfOpenMaxCalls int           // 半开期需要连续成功多少次才闭合
}

func DefaultSpec() Spec {
	return Spec{
		FailureThreshold: DefaultFailureThreshold,
		FailureRate:      DefaultFailureRate,
		SampleThreshold:  DefaultSampleThreshold,
		WindowDuration:   DefaultWindowDuration,
		OpenTimeout:      DefaultOpenTimeout,
		HalfOpenMaxCalls: DefaultHalfOpenMaxCalls,
	}
}

// Normalized 把零值/非法值回落到缺省值。宿主不配置也能工作。
func (s Spec) Normalized() Spec {
	if s.FailureThreshold <= 0 {
		s.FailureThreshold = DefaultFailureThreshold
	}
	if s.FailureRate <= 0 {
		s.FailureRate = DefaultFailureRate
	}
	if s.FailureRate > 1 {
		s.FailureRate = 1
	}
	if s.SampleThreshold <= 0 {
		s.SampleThreshold = DefaultSampleThreshold
	}
	if s.WindowDuration <= 0 {
		s.WindowDuration = DefaultWindowDuration
	}
	if s.OpenTimeout <= 0 {
		s.OpenTimeout = DefaultOpenTimeout
	}
	if s.HalfOpenMaxCalls <= 0 {
		s.HalfOpenMaxCalls = DefaultHalfOpenMaxCalls
	}
	return s
}

// ObserveResult 描述一次观测对状态机的影响，供调用方决定是否对外发事件。
type ObserveResult struct {
	Health Health
	// Tripped 只在「闭合 → 打开」跨过的那一次为 true（边沿触发），
	// 与 PromptCacheBreaker 的通告约定一致，避免每步重复上报。
	Tripped bool
	// Opened 表示观测后电路处于打开状态。
	Opened bool
}

// ClassifyFailure 把调用方给的错误码或分类名统一成 D5 分类枚举。
//
// 输入可能是已归一化的分类（provider_error），也可能是 llm 的稳定错误码
// （UPSTREAM_UNAVAILABLE），两条路径都要能认出来。认不出时返回 unknown——
// 绝不猜成具体类别，否则会在自身 bug 上误熔断。
func ClassifyFailure(raw string) string {
	if category := llm.NormalizeFailureCategory(raw); category != "" && category != llm.FailureCategoryUnknown {
		return category
	}
	return llm.FailureCategoryFromErrorCode(raw)
}

// IsProviderFault 报告某个失败是否应归因于 provider。
//
// 只认四类：provider_error、rate_limited、timeout、interrupted。这四类都能由
// 后端自身的不健康解释。context_overflow 是请求构造问题，budget_exceeded 是我们
// 自己的预算策略，tool_error 是工具链问题，cancelled 是用户行为——把它们算成
// provider 故障会让熔断器在无关场景下打开。
func IsProviderFault(raw string) bool {
	switch ClassifyFailure(raw) {
	case llm.FailureCategoryProviderError,
		llm.FailureCategoryRateLimited,
		llm.FailureCategoryTimeout,
		llm.FailureCategoryInterrupted:
		return true
	default:
		return false
	}
}

// targetKey 归一化 provider/model 作为索引键。provider 名大小写不敏感，
// 且 "\x00" 不会出现在配置名里，用它分隔可避免前缀歧义。
func targetKey(provider, model string) string {
	return strings.ToLower(strings.TrimSpace(provider)) + "\x00" + strings.ToLower(strings.TrimSpace(model))
}

// observation 是滑动窗口里的一次调用样本。成功样本必须保留，否则失败率没有分母。
type observation struct {
	at      time.Time
	failure bool
}

type entry struct {
	provider string
	model    string

	consecutiveFailures int
	openedAt            time.Time
	halfOpenSuccesses   int
	lastFailureAt       time.Time
	lastCategory        string
	samples             []observation
}

// Registry 是一个并发安全的 provider 健康注册表。零值不可用，用 NewRegistry。
type Registry struct {
	spec Spec

	mu      sync.RWMutex
	entries map[string]*entry
}

func NewRegistry(spec Spec) *Registry {
	return &Registry{
		spec:    spec.Normalized(),
		entries: make(map[string]*entry),
	}
}

// Spec 返回归一化后的生效策略。
func (r *Registry) Spec() Spec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.spec
}

// setSpec 就地替换策略，保留既有观测。
//
// 与包级 Configure 的区别：Configure 重建注册表（丢弃观测），语义是"重新开始"，
// 只适合启动/热重载；setSpec 保留观测，因此可以在策略跟随配置对齐时安全调用——
// 否则每次对齐都会把刚学到的故障状态清空，熔断永远来不及生效。
func (r *Registry) setSpec(spec Spec) {
	spec = spec.Normalized()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spec = spec
}

// Observe 记录一次调用失败。非 provider 归因的失败不改变任何计数。
func (r *Registry) Observe(provider, model, category string, now time.Time) ObserveResult {
	key := targetKey(provider, model)
	classified := ClassifyFailure(category)

	r.mu.Lock()
	defer r.mu.Unlock()

	e := r.ensureLocked(key, provider, model)
	if !IsProviderFault(classified) {
		// 不改动任何计数：自身 bug 或用户取消不该把 provider 摘掉。
		return ObserveResult{Health: r.healthLocked(e, now)}
	}

	state := r.stateOf(e, now)
	e.consecutiveFailures++
	e.lastFailureAt = now
	e.lastCategory = classified
	e.samples = appendSample(e.samples, observation{at: now, failure: true}, now, r.spec.WindowDuration)

	switch state {
	case StateUnhealthy:
		// 已打开：只累积样本，不重新计时。否则持续失败会一直推后 openedAt，
		// provider 永远进不了半开，也就永远没有恢复的机会。
		return ObserveResult{Health: r.healthLocked(e, now), Opened: true}
	case StateDegraded:
		// 半开期探测失败：重新打开并重新计时。
		e.openedAt = now
		e.halfOpenSuccesses = 0
		return ObserveResult{Health: r.healthLocked(e, now), Opened: true}
	}

	if e.consecutiveFailures >= r.spec.FailureThreshold || r.rateTrippedLocked(e, now) {
		e.openedAt = now
		e.halfOpenSuccesses = 0
		return ObserveResult{Health: r.healthLocked(e, now), Tripped: true, Opened: true}
	}
	return ObserveResult{Health: r.healthLocked(e, now)}
}

// ObserveSuccess 记录一次调用成功。
func (r *Registry) ObserveSuccess(provider, model string, now time.Time) ObserveResult {
	key := targetKey(provider, model)

	r.mu.Lock()
	defer r.mu.Unlock()

	e := r.ensureLocked(key, provider, model)
	e.samples = appendSample(e.samples, observation{at: now, failure: false}, now, r.spec.WindowDuration)

	switch r.stateOf(e, now) {
	case StateUnhealthy:
		// 打开期内的成功不计入恢复：否则一次偶发成功就会把刚熔断的 provider
		// 立刻放回来。恢复只能经半开探测这条唯一的窄路。
		return ObserveResult{Health: r.healthLocked(e, now), Opened: true}
	case StateDegraded:
		e.halfOpenSuccesses++
		if e.halfOpenSuccesses >= r.spec.HalfOpenMaxCalls {
			e.openedAt = time.Time{}
			e.consecutiveFailures = 0
			e.halfOpenSuccesses = 0
		}
		return ObserveResult{Health: r.healthLocked(e, now)}
	}

	// 闭合态：历史抖动不累积成熔断。
	e.consecutiveFailures = 0
	e.halfOpenSuccesses = 0
	return ObserveResult{Health: r.healthLocked(e, now)}
}

// stateOf 由 openedAt 与当前时间推导状态：未打开为闭合；打开未超时为打开；
// 打开已超时进入半开。
func (r *Registry) stateOf(e *entry, now time.Time) string {
	if e.openedAt.IsZero() {
		return StateHealthy
	}
	if now.Sub(e.openedAt) >= r.spec.OpenTimeout {
		return StateDegraded
	}
	return StateUnhealthy
}

// rateTrippedLocked 判断窗口失败率规则是否触发。样本不足 SampleThreshold 时
// 不触发——一两次失败不足以说明后端在系统性变坏。
func (r *Registry) rateTrippedLocked(e *entry, now time.Time) bool {
	samples, failures := windowCounts(e.samples, now, r.spec.WindowDuration)
	if samples < r.spec.SampleThreshold {
		return false
	}
	return float64(failures)/float64(samples) >= r.spec.FailureRate
}

func (r *Registry) ensureLocked(key, provider, model string) *entry {
	e, ok := r.entries[key]
	if !ok {
		e = &entry{provider: strings.TrimSpace(provider), model: strings.TrimSpace(model)}
		r.entries[key] = e
	}
	return e
}

// healthLocked 组装快照。Reason 是稳定短码（非自然语言），便于路由与 doctor
// 复用同一套判定词汇。
func (r *Registry) healthLocked(e *entry, now time.Time) Health {
	samples, failures := windowCounts(e.samples, now, r.spec.WindowDuration)
	state := r.stateOf(e, now)
	health := Health{
		Provider:            e.provider,
		Model:               e.model,
		State:               state,
		ConsecutiveFailures: e.consecutiveFailures,
		Samples:             samples,
		Failures:            failures,
		OpenedAt:            e.openedAt,
		LastFailureAt:       e.lastFailureAt,
		LastCategory:        e.lastCategory,
	}
	if samples > 0 {
		health.FailureRate = float64(failures) / float64(samples)
	}
	switch state {
	case StateUnhealthy:
		health.Reason = "circuit_open"
	case StateDegraded:
		health.Reason = "half_open_probe"
	default:
		if health.FailureRate > 0 {
			health.Reason = "observed_failures"
		} else {
			health.Reason = "observed_healthy"
		}
	}
	return health
}

// windowCounts 裁剪出窗口内的样本数与该窗口内的失败数。
func windowCounts(samples []observation, now time.Time, window time.Duration) (int, int) {
	cutoff := now.Add(-window)
	total, failures := 0, 0
	for _, sample := range samples {
		if sample.at.Before(cutoff) || sample.at.After(now) {
			continue
		}
		total++
		if sample.failure {
			failures++
		}
	}
	return total, failures
}

// appendSample 追加样本并裁剪窗口外的旧样本。不假设调用方注入的时间单调，
// 因此按时间过滤而不是从头部切片；同时用 maxSamplesPerTarget 兜住极端情况。
func appendSample(samples []observation, next observation, now time.Time, window time.Duration) []observation {
	samples = append(samples, next)
	cutoff := now.Add(-window)
	kept := samples[:0]
	for _, sample := range samples {
		if sample.at.Before(cutoff) || sample.at.After(now) {
			continue
		}
		kept = append(kept, sample)
	}
	if len(kept) > maxSamplesPerTarget {
		kept = kept[len(kept)-maxSamplesPerTarget:]
	}
	return kept
}

// Lookup 读取某目标的当前健康状态。known=false 表示从无观测记录，调用方应当
// 视为健康——没有证据不等于有罪，否则冷启动会凭空摘掉所有后端。
//
// 纯读：不消费探测令牌、不推进状态机。
func (r *Registry) Lookup(provider, model string, now time.Time) (Health, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	e, ok := r.entries[targetKey(provider, model)]
	if !ok {
		return Health{}, false
	}
	return r.healthLocked(e, now), true
}

// Snapshots 返回全部已记录目标的快照，按 provider、model 排序，供诊断输出。
func (r *Registry) Snapshots(now time.Time) []Health {
	r.mu.RLock()
	defer r.mu.RUnlock()

	snapshots := make([]Health, 0, len(r.entries))
	for _, e := range r.entries {
		snapshots = append(snapshots, r.healthLocked(e, now))
	}
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].Provider != snapshots[j].Provider {
			return snapshots[i].Provider < snapshots[j].Provider
		}
		return snapshots[i].Model < snapshots[j].Model
	})
	return snapshots
}

// Reset 清空全部状态。
func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = make(map[string]*entry)
}

var (
	defaultMu       sync.Mutex
	defaultRegistry = NewRegistry(DefaultSpec())
	// defaultSyncedConfig 记录上一次对齐所用的全局配置指针，用于把"是否需要对
	// 齐"降到一次指针比较，而不是每次调用都重新解析六个标量。
	defaultSyncedConfig *agentconfig.Config
)

// Default 返回进程级共享注册表。
//
// 之所以是进程级：健康状态描述的是"本进程刚刚经历过的上游表现"，它天然跨
// session、跨子 Agent。让每个调用点各持一份会让观测被切碎，谁都不足以熔断。
//
// 策略在取用时按全局配置对齐。放在这里而不是各个配置装载点：SetGlobalConfig
// 的调用点散布在 runtimeserver、aicli image 等多处，逐个挂钩必漏；而所有消费方
// （agent.loop 记录、modelrouting 读取）都会经过这里。
//
// 两条保守约束：
//   - 只有宿主真的声明了 circuit_breaker 块才采纳。未声明时保留当前策略，
//     显式 Configure 过的宿主不会被默认值悄悄覆盖。
//   - 采纳走 setSpec 而非 Configure：策略变化不该清空既有观测。
func Default() *Registry {
	cfg := agentconfig.GetGlobalConfig()
	defaultMu.Lock()
	defer defaultMu.Unlock()
	registry := defaultRegistry
	if cfg != defaultSyncedConfig {
		defaultSyncedConfig = cfg
		if cfg != nil && cfg.CircuitBreaker != nil {
			registry.setSpec(SpecFromConfig(cfg.CircuitBreaker))
		}
	}
	return registry
}

// Configure 用给定策略重建进程级共享注册表。宿主加载配置后调用一次即可；
// 重建会丢弃既有观测，因此只在启动/热重载时使用。
func Configure(spec Spec) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultRegistry = NewRegistry(spec)
	// 显式配置优先：记下当时的全局配置，避免紧接着的 Default() 又按配置文件
	// 把策略对齐回去，让这次调用看起来没生效。
	defaultSyncedConfig = agentconfig.GetGlobalConfig()
}
