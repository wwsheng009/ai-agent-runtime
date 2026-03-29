package observability

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// MetricsKey 指标键常量
const (
	// Request 相关指标
	MetricRequestTotal    = "request_total"
	MetricRequestDuration = "request_duration_seconds"
	MetricRequestActive   = "request_active"

	// LLM 相关指标
	MetricLLMCallsTotal       = "llm_calls_total"
	MetricLLMCallsFailed      = "llm_calls_failed"
	MetricLLMTokensTotal      = "llm_tokens_total"
	MetricLLMTokensPrompt     = "llm_tokens_prompt"
	MetricLLMTokensCompletion = "llm_tokens_completion"
	MetricLLMDuration         = "llm_duration_seconds"

	// Agent 相关指标
	MetricAgentRunsTotal  = "agent_runs_total"
	MetricAgentStepsTotal = "agent_steps_total"
	MetricAgentReActLoops = "agent_react_loops"
	MetricAgentDuration   = "agent_duration_seconds"

	// Skill 相关指标
	MetricSkillExecutionsTotal  = "skill_executions_total"
	MetricSkillExecutionsFailed = "skill_executions_failed"
	MetricSkillDuration         = "skill_duration_seconds"

	// Router 相关指标
	MetricRouterMatchesTotal   = "router_matches_total"
	MetricRouterFallbacks      = "router_fallbacks"
	MetricSearchAdminActions   = "search_admin_actions_total"
	MetricSearchReindexRuns    = "search_reindex_runs_total"
	MetricSkillMutationActions = "skill_mutation_actions_total"
	MetricSkillUsageRequests   = "skill_usage_requests_total"
	MetricSkillUsageTokens     = "skill_usage_tokens_total"
	MetricSkillQuotaDenials    = "skill_quota_denials_total"

	// Embedding 相关指标
	MetricEmbeddingCallsTotal = "embedding_calls_total"
	MetricEmbeddingDuration   = "embedding_duration_seconds"

	// Session 相关指标
	MetricSessionTotal   = "session_total"
	MetricSessionActive  = "session_active"
	MetricSessionExpired = "session_expired"

	// 错误相关指标
	MetricErrorsTotal    = "errors_total"
	MetricErrorsByType   = "errors_by_type"
	MetricErrorsBySource = "errors_by_source"
)

// MetricLabel 标签常量
const (
	LabelModel      = "model"
	LabelProvider   = "provider"
	LabelSkill      = "skill"
	LabelAgent      = "agent"
	LabelSuccess    = "success"
	LabelError      = "error"
	LabelMethod     = "method"
	LabelStatus     = "status"
	LabelSource     = "source"
	LabelErrorType  = "error_type"
	LabelMatchType  = "match_type"
	LabelLanguage   = "language"
	LabelAction     = "action"
	LabelOutcome    = "outcome"
	LabelAccessMode = "access_mode"
	LabelEntrypoint = "entrypoint"
	LabelQuotaType  = "quota_type"
	LabelTokenType  = "token_type"
)

// MetricValue 指标值类型
type MetricValue struct {
	Value     float64
	Labels    map[string]string
	Timestamp time.Time
}

// Counter 计数器
type Counter struct {
	name   string
	labels map[string]string
	mu     sync.RWMutex
	value  float64
}

// NewCounter 创建计数器
func NewCounter(name string, labels map[string]string) *Counter {
	return &Counter{
		name:   name,
		labels: labels,
		value:  0,
	}
}

// Inc 增加计数
func (c *Counter) Inc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value++
}

// IncBy 增加指定值
func (c *Counter) IncBy(delta float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value += delta
}

// Add 添加值
func (c *Counter) Add(other float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value += other
}

// Get 获取当前值
func (c *Counter) Get() float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.value
}

// ToMetricValue 转换为 MetricValue
func (c *Counter) ToMetricValue() *MetricValue {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return &MetricValue{
		Value:     c.value,
		Labels:    cloneLabels(c.labels),
		Timestamp: time.Now(),
	}
}

// Reset 重置计数器
func (c *Counter) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value = 0
}

// Histogram 直方图（用于记录耗时分布）
type Histogram struct {
	name     string
	labels   map[string]string
	mu       sync.RWMutex
	buckets  []float64
	observes map[float64]int
	sum      float64
	count    int
}

// NewHistogram 创建直方图
func NewHistogram(name string, labels map[string]string, buckets []float32) *Histogram {
	var float64Buckets []float64

	if len(buckets) == 0 {
		// 默认 buckets
		defaultFloat32 := []float32{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
		float64Buckets = make([]float64, len(defaultFloat32))
		for i, b := range defaultFloat32 {
			float64Buckets[i] = float64(b)
		}
	} else {
		float64Buckets = make([]float64, len(buckets))
		for i, b := range buckets {
			float64Buckets[i] = float64(b)
		}
	}

	return &Histogram{
		name:     name,
		labels:   labels,
		buckets:  float64Buckets,
		observes: make(map[float64]int),
	}
}

// Observe 记录一个值
func (h *Histogram) Observe(value float32) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.count++
	h.sum += float64(value)

	for _, bucket := range h.buckets {
		if float64(value) <= bucket {
			h.observes[bucket]++
		}
	}
}

// ObserveDuration 记录耗时
func (h *Histogram) ObserveDuration(duration time.Duration) {
	h.Observe(float32(duration.Seconds()))
}

// GetCount 获取记录总数
func (h *Histogram) GetCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.count
}

// GetSum 获取总和
func (h *Histogram) GetSum() float64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sum
}

// GetAverage 获取平均值
func (h *Histogram) GetAverage() float64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.count == 0 {
		return 0
	}
	return h.sum / float64(h.count)
}

// GetPercentile 获取百分位数
func (h *Histogram) GetPercentile(percentile float32) float64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.count == 0 {
		return 0
	}

	target := int(float64(h.count) * float64(percentile) / 100.0)
	accumulated := 0

	// Sort buckets
	sortedBuckets := make([]float64, len(h.buckets))
	copy(sortedBuckets, h.buckets)

	for i := 0; i < len(sortedBuckets); i++ {
		for j := i + 1; j < len(sortedBuckets); j++ {
			if sortedBuckets[i] > sortedBuckets[j] {
				sortedBuckets[i], sortedBuckets[j] = sortedBuckets[j], sortedBuckets[i]
			}
		}
	}

	for _, bucket := range sortedBuckets {
		accumulated += h.observes[bucket]
		if accumulated >= target {
			return bucket
		}
	}

	return sortedBuckets[len(sortedBuckets)-1]
}

// GetP50 获取中位数
func (h *Histogram) GetP50() float64 {
	return h.GetPercentile(50)
}

// GetP95 获取 P95
func (h *Histogram) GetP95() float64 {
	return h.GetPercentile(95)
}

// GetP99 获取 P99
func (h *Histogram) GetP99() float64 {
	return h.GetPercentile(99)
}

// Gauge 仪表盘（用于记录瞬时值）
type Gauge struct {
	name   string
	labels map[string]string
	mu     sync.RWMutex
	value  float64
}

// NewGauge 创建仪表盘
func NewGauge(name string, labels map[string]string) *Gauge {
	return &Gauge{
		name:   name,
		labels: labels,
		value:  0,
	}
}

// Set 设置值
func (g *Gauge) Set(value float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.value = value
}

// Inc 增加计数
func (g *Gauge) Inc() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.value++
}

// Dec 减少计数
func (g *Gauge) Dec() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.value--
}

// Add 添加值
func (g *Gauge) Add(delta float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.value += delta
}

// Sub 减少值
func (g *Gauge) Sub(delta float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.value -= delta
}

// Get 获取当前值
func (g *Gauge) Get() float64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.value
}

// ToMetricValue 转换为 MetricValue
func (g *Gauge) ToMetricValue() *MetricValue {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return &MetricValue{
		Value:     g.value,
		Labels:    cloneLabels(g.labels),
		Timestamp: time.Now(),
	}
}

// Registry 指标注册表
type Registry struct {
	mu         sync.RWMutex
	counters   map[string]*Counter
	gauges     map[string]*Gauge
	histograms map[string]*Histogram
}

// NewRegistry 创建指标注册表
func NewRegistry() *Registry {
	return &Registry{
		counters:   make(map[string]*Counter),
		gauges:     make(map[string]*Gauge),
		histograms: make(map[string]*Histogram),
	}
}

// GetOrCreateCounter 获取或创建计数器
func (r *Registry) GetOrCreateCounter(name string, labels map[string]string) *Counter {
	key := r.buildKey(name, labels)

	r.mu.Lock()
	defer r.mu.Unlock()

	if counter, exists := r.counters[key]; exists {
		return counter
	}

	counter := NewCounter(name, cloneLabels(labels))
	r.counters[key] = counter
	return counter
}

// GetOrCreateGauge 获取或创建仪表盘
func (r *Registry) GetOrCreateGauge(name string, labels map[string]string) *Gauge {
	key := r.buildKey(name, labels)

	r.mu.Lock()
	defer r.mu.Unlock()

	if gauge, exists := r.gauges[key]; exists {
		return gauge
	}

	gauge := NewGauge(name, cloneLabels(labels))
	r.gauges[key] = gauge
	return gauge
}

// GetOrCreateHistogram 获取或创建直方图
func (r *Registry) GetOrCreateHistogram(name string, labels map[string]string, buckets []float32) *Histogram {
	key := r.buildKey(name, labels)

	r.mu.Lock()
	defer r.mu.Unlock()

	if histogram, exists := r.histograms[key]; exists {
		return histogram
	}

	histogram := NewHistogram(name, cloneLabels(labels), buckets)
	r.histograms[key] = histogram
	return histogram
}

// GetAllCounters 获取所有计数器
func (r *Registry) GetAllCounters() []*MetricValue {
	r.mu.RLock()
	defer r.mu.RUnlock()
	values := make([]*MetricValue, 0, len(r.counters))
	for _, counter := range r.counters {
		values = append(values, counter.ToMetricValue())
	}
	return values
}

// SnapshotCounters 获取按指标名分组的计数器快照
func (r *Registry) SnapshotCounters() map[string][]MetricValue {
	r.mu.RLock()
	defer r.mu.RUnlock()

	values := make(map[string][]MetricValue)
	for _, counter := range r.counters {
		metricValue := counter.ToMetricValue()
		values[counter.name] = append(values[counter.name], *metricValue)
	}
	return values
}

// GetAllGauges 获取所有仪表盘
func (r *Registry) GetAllGauges() []*MetricValue {
	r.mu.RLock()
	defer r.mu.RUnlock()
	values := make([]*MetricValue, 0, len(r.gauges))
	for _, gauge := range r.gauges {
		values = append(values, gauge.ToMetricValue())
	}
	return values
}

// SnapshotGauges 获取按指标名分组的仪表盘快照
func (r *Registry) SnapshotGauges() map[string][]MetricValue {
	r.mu.RLock()
	defer r.mu.RUnlock()

	values := make(map[string][]MetricValue)
	for _, gauge := range r.gauges {
		metricValue := gauge.ToMetricValue()
		values[gauge.name] = append(values[gauge.name], *metricValue)
	}
	return values
}

// GetAllHistograms 获取所有直方图
func (r *Registry) GetAllHistograms() map[string]*Histogram {
	r.mu.RLock()
	defer r.mu.RUnlock()

	histograms := make(map[string]*Histogram, len(r.histograms))
	for key, histogram := range r.histograms {
		histograms[key] = histogram
	}
	return histograms
}

// buildKey 构建键
func (r *Registry) buildKey(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name
	}

	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var builder strings.Builder
	builder.Grow(len(name) + len(keys)*8)
	builder.WriteString(name)
	for _, key := range keys {
		builder.WriteString(":")
		builder.WriteString(key)
		builder.WriteString("=")
		builder.WriteString(labels[key])
	}

	return builder.String()
}

// Reset 重置所有指标
func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.counters = make(map[string]*Counter)
	r.gauges = make(map[string]*Gauge)
	r.histograms = make(map[string]*Histogram)
}

func cloneLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}

	cloned := make(map[string]string, len(labels))
	for key, value := range labels {
		cloned[key] = value
	}
	return cloned
}

// GlobalMetrics 全局指标注册表
var GlobalMetrics = NewRegistry()

// Helper 函数

// IncrementCounter 增加计数器
func IncrementCounter(name string, labels map[string]string) {
	counter := GlobalMetrics.GetOrCreateCounter(name, labels)
	counter.Inc()
}

// RecordDuration 记录耗时到直方图
func RecordDuration(name string, labels map[string]string, duration time.Duration) {
	histogram := GlobalMetrics.GetOrCreateHistogram(name+"_seconds", labels, nil)
	histogram.ObserveDuration(duration)
}

// SetGauge 设置仪表盘值
func SetGauge(name string, labels map[string]string, value float64) {
	gauge := GlobalMetrics.GetOrCreateGauge(name, labels)
	gauge.Set(value)
}

// IncGauge 增加仪表盘值
func IncGauge(name string, labels map[string]string) {
	gauge := GlobalMetrics.GetOrCreateGauge(name, labels)
	gauge.Inc()
}

// DecGauge 减少仪表盘值
func DecGauge(name string, labels map[string]string) {
	gauge := GlobalMetrics.GetOrCreateGauge(name, labels)
	gauge.Dec()
}
