package observability

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Span 追踪跨度
type Span struct {
	ID          string            // Span ID
	ParentID    string            // 父 Span ID
	TraceID     string            // 追踪 ID
	Name        string            // Span 名称
	StartTime   time.Time         // 开始时间
	EndTime     time.Time         // 结束时间
	Attributes  map[string]string // 属性
	Events      []SpanEvent       // 事件
	Status      SpanStatus        // 状态
}

// SpanEvent Span 事件
type SpanEvent struct {
	Timestamp time.Time
	Name      string
	Attributes map[string]string
}

// SpanStatus Span 状态
type SpanStatus struct {
	Code    int16
	Message string
}

// Span 常量
const (
	SpanStatusOK    = 1
	SpanStatusError = 2
)

// NewSpan 创建新的 Span
func NewSpan(name, traceID, parentID string) *Span {
	spanID := generateSpanID()

	return &Span{
		ID:         spanID,
		ParentID:   parentID,
		TraceID:    traceID,
		Name:       name,
		StartTime:  time.Now(),
		Attributes: make(map[string]string),
		Events:     make([]SpanEvent, 0),
		Status:     SpanStatus{Code: SpanStatusOK},
	}
}

// Trace 追踪器
type Trace struct {
	TraceID string
	Root    *Span
	Spans   []*Span
	mu      sync.RWMutex
}

// NewTrace 创建新的追踪
func NewTrace(rootName string) *Trace {
	traceID := generateTraceID()
	
	root := NewSpan(rootName, traceID, "")
	
	return &Trace{
		TraceID: traceID,
		Root:    root,
		Spans:   []*Span{root},
	}
}

// StartSpan 开始一个新的 Span
func (t *Trace) StartSpan(name string) *Span {
	t.mu.Lock()
	defer t.mu.Unlock()

	parentID := ""
	if len(t.Spans) > 0 {
		// 使用最后一个 Span 作为父
		parentID = t.Spans[len(t.Spans)-1].ID
	}

	span := NewSpan(name, t.TraceID, parentID)
	t.Spans = append(t.Spans, span)

	return span
}

// NewChildSpan 创建子 Span
func (s *Span) NewChild(name string) *Span {
	child := NewSpan(name, s.TraceID, s.ID)
	return child
}

// Finish 完成 Span
func (s *Span) Finish() {
	s.EndTime = time.Now()
}

// SetAttribute 设置属性
func (s *Span) SetAttribute(key, value string) {
	s.Attributes[key] = value
}

// SetAttributes 批量设置属性
func (s *Span) SetAttributes(attributes map[string]string) {
	for k, v := range attributes {
		s.Attributes[k] = v
	}
}

// AddEvent 添加事件
func (s *Span) AddEvent(name string) {
	s.AddEventWithAttributes(name, nil)
}

// AddEventWithAttributes 添加带属性的事件
func (s *Span) AddEventWithAttributes(name string, attributes map[string]string) {
	event := SpanEvent{
		Timestamp: time.Now(),
		Name:      name,
		Attributes: attributes,
	}
	s.Events = append(s.Events, event)
}

// SetError 设置错误状态
func (s *Span) SetError(message string) {
	s.Status.Code = SpanStatusError
	s.Status.Message = message
	s.SetAttribute("error", message)
}

// Duration 获取耗时
func (s *Span) Duration() time.Duration {
	if s.EndTime.IsZero() {
		return time.Since(s.StartTime)
	}
	return s.EndTime.Sub(s.StartTime)
}

// AddSpan 添加 Span 到追踪
func (t *Trace) AddSpan(span *Span) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Spans = append(t.Spans, span)
}

// GetDuration 获取追踪总耗时
func (t *Trace) GetDuration() time.Duration {
	if len(t.Spans) == 0 {
		return 0
	}
	
	return t.Root.Duration()
}

// GetSpanByID 根据 ID 获取 Span
func (t *Trace) GetSpanByID(spanID string) *Span {
	t.mu.RLock()
	defer t.mu.RUnlock()

	for _, span := range t.Spans {
		if span.ID == spanID {
			return span
		}
	}
	return nil
}

// GetSpansByName 根据名称获取 Spans
func (t *Trace) GetSpansByName(name string) []*Span {
	t.mu.RLock()
	defer t.mu.RUnlock()

	results := make([]*Span, 0)
	for _, span := range t.Spans {
		if span.Name == name {
			results = append(results, span)
		}
	}
	return results
}

// GetErrorSpans 获取有错误的 Spans
func (t *Trace) GetErrorSpans() []*Span {
	t.mu.RLock()
	defer t.mu.RUnlock()

	results := make([]*Span, 0)
	for _, span := range t.Spans {
		if span.Status.Code == SpanStatusError {
			results = append(results, span)
		}
	}
	return results
}

// Tracer 追踪器接口
type Tracer interface {
	StartTrace(name string) *Trace
	GetTrace(traceID string) *Trace
}

// DefaultTracer 默认追踪器
type DefaultTracer struct {
	traces map[string]*Trace
	mu     sync.RWMutex
}

// NewDefaultTracer 创建默认追踪器
func NewDefaultTracer() *DefaultTracer {
	return &DefaultTracer{
		traces: make(map[string]*Trace),
	}
}

// StartTrace 开始新的追踪
func (t *DefaultTracer) StartTrace(name string) *Trace {
	trace := NewTrace(name)
	
	t.mu.Lock()
	defer t.mu.Unlock()
	
	t.traces[trace.TraceID] = trace
	return trace
}

// GetTrace 获取追踪
func (t *DefaultTracer) GetTrace(traceID string) *Trace {
	t.mu.RLock()
	defer t.mu.RUnlock()
	
	return t.traces[traceID]
}

// RemoveTrace 移除追踪
func (t *DefaultTracer) RemoveTrace(traceID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	
	delete(t.traces, traceID)
}

// GetAllTraces 获取所有追踪
func (t *DefaultTracer) GetAllTraces() []*Trace {
	t.mu.RLock()
	defer t.mu.RUnlock()
	
	traces := make([]*Trace, 0, len(t.traces))
	for _, trace := range t.traces {
		traces = append(traces, trace)
	}
	return traces
}

// GlobalTracer 全局追踪器
var GlobalTracer Tracer = NewDefaultTracer()

// 辅助函数

// StartSpan 开始一个新的 Span（使用全局追踪器）
func StartSpan(name, traceID, parentID string) *Span {
	return NewSpan(name, traceID, parentID)
}

// createSpanID 生成 Span ID
func generateSpanID() string {
	return fmt.Sprintf("span_%d", time.Now().UnixNano())
}

// generateTraceID 生成追踪 ID
func generateTraceID() string {
	return fmt.Sprintf("trace_%d", time.Now().UnixNano())
}

// Timing 计时辅助结构
type Timing struct {
	start time.Time
}

// StartTiming 开始计时
func StartTiming() *Timing {
	return &Timing{
		start: time.Now(),
	}
}

// Stop 停止计时并返回持续时间
func (t *Timing) Stop() time.Duration {
	return time.Since(t.start)
}

// Elapsed 获取已用时间（不停止）
func (t *Timing) Elapsed() time.Duration {
	return time.Since(t.start)
}

// TraceContext 追踪上下文
type TraceContext struct {
	TraceID string
	SpanID  string
}

// NewTraceContext 创建追踪上下文
func NewTraceContext(traceID, spanID string) *TraceContext {
	return &TraceContext{
		TraceID: traceID,
		SpanID:  spanID,
	}
}

// FromContext 从 context 中提取追踪上下文
func FromContext(ctx context.Context) *TraceContext {
	if traceCtx, ok := ctx.Value(traceContextKey{}).(*TraceContext); ok {
		return traceCtx
	}
	return nil
}

// ToContext 将追踪上下文存入 context
func ToContext(ctx context.Context, traceCtx *TraceContext) context.Context {
	return context.WithValue(ctx, traceContextKey{}, traceCtx)
}

type traceContextKey struct{}

// WithTrace 在 context 中创建新的追踪
func WithTrace(ctx context.Context, traceID, spanID string) context.Context {
	traceCtx := NewTraceContext(traceID, spanID)
	return ToContext(ctx, traceCtx)
}

// InstrumentedSpan 可追踪的 Span
type InstrumentedSpan struct {
	*Span
}

// NewInstrumentedSpan 创建可追踪的 Span
func NewInstrumentedSpan(name string) *InstrumentedSpan {
	return &InstrumentedSpan{
		Span: NewSpan(name, generateTraceID(), ""),
	}
}

// Start 开始追踪并运行函数
func (is *InstrumentedSpan) Start(fn func() error) error {
	is.StartTime = time.Now()
	defer is.Finish()
	
	err := fn()
	if err != nil {
		is.SetError(err.Error())
	}
	
	return err
}

// StartWithResult 开始追踪并返回结果
func (is *InstrumentedSpan) StartWithResult(fn func() (interface{}, error)) (interface{}, error) {
	is.StartTime = time.Now()
	defer is.Finish()
	
	result, err := fn()
	if err != nil {
		is.SetError(err.Error())
	}
	
	is.SetAttribute("success", fmt.Sprintf("%t", err == nil))
	
	return result, err
}
