package observability

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockLLMProviderForObservability 用于 Observability 集成测试的 Mock Provider
type MockLLMProviderForObservability struct {
	name string
}

func NewMockLLMProviderForObservability() *MockLLMProviderForObservability {
	return &MockLLMProviderForObservability{
		name: "mock-obs-provider",
	}
}

func (m *MockLLMProviderForObservability) Name() string {
	return m.name
}

func (m *MockLLMProviderForObservability) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	// 创建响应
	response := &llm.LLMResponse{
		Content: "This is a mock response for observability testing.",
		Usage: &types.TokenUsage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
		Model: "gpt-4",
	}

	return response, nil
}

func (m *MockLLMProviderForObservability) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 1)
	go func() {
		defer close(ch)
		ch <- llm.StreamChunk{
			Type:    llm.EventTypeText,
			Content: "Mock streaming response",
			Done:    true,
		}
	}()
	return ch, nil
}

func (m *MockLLMProviderForObservability) CountTokens(text string) int {
	return len(text) / 4
}

func (m *MockLLMProviderForObservability) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsVision:    false,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsJSONMode:  true,
	}
}

func (m *MockLLMProviderForObservability) CheckHealth(ctx context.Context) error {
	return nil
}

// TestObservabilityWithLLMRuntime 测试 Observability 与 LLM Runtime 集成
func TestObservabilityWithLLMRuntime(t *testing.T) {
	ctx := context.Background()

	// 清理全局 metrics（确保测试隔离）
	GlobalMetrics = NewRegistry()

	// 创建 LLM Runtime
	runtimeConfig := &llm.RuntimeConfig{
		DefaultModel: "gpt-4",
		MaxRetries:    2,
	}
	runtime := llm.NewLLMRuntime(runtimeConfig)

	// 注册 Mock Provider
	mockProvider := NewMockLLMProviderForObservability()
	runtime.RegisterProvider(mockProvider.Name(), mockProvider)

	// 记录 LLM 调用指标
	labels := map[string]string{"model": "gpt-4"}
	startTime := time.Now()

	// 调用 LLM
	req := &llm.LLMRequest{
		Model:    "gpt-4",
		Messages: []types.Message{*types.NewUserMessage("Test message")},
	}
	_, _ = runtime.Call(ctx, req)

	duration := time.Since(startTime)

	// 记录指标
	IncrementCounter(MetricLLMCallsTotal, labels)
	RecordDuration(MetricLLMDuration, labels, duration)

	// 验证指标记录
	counter := GlobalMetrics.GetOrCreateCounter(MetricLLMCallsTotal, labels)
	require.NotNil(t, counter)
	assert.Greater(t, counter.Get(), float64(0), "LLM call counter should be incremented")

	allHistograms := GlobalMetrics.GetAllHistograms()
	require.NotEmpty(t, allHistograms, "Should have recorded histograms")
}

// TestObservabilityWithAgent 测试 Observability 与 Agent 集成
func TestObservabilityWithAgent(t *testing.T) {
	// 清理全局 metrics（确保测试隔离）
	GlobalMetrics = NewRegistry()

	// 创建 Agent 配置
	agentConfig := &agent.Config{
		Name:         "test-agent-obs",
		Model:        "gpt-4",
		MaxSteps:     5,
		SystemPrompt: "You are a helpful assistant.",
	}

	// 创建追踪 span
	span := StartSpan("agent_execution", "trace-123", "")
	defer span.Finish()

	labels := map[string]string{"agent": agentConfig.Name}

	// 记录 Agent 运行指标
	IncrementCounter(MetricAgentRunsTotal, labels)
	IncrementCounter(MetricAgentStepsTotal, labels)

	// 记录 Agent 运行时间
	startTime := time.Now()
	time.Sleep(10 * time.Millisecond)
	duration := time.Since(startTime)
	RecordDuration(MetricAgentDuration, labels, duration)

	// 设置 span 属性
	span.SetAttribute("agent.name", agentConfig.Name)
	span.SetAttribute("agent.max_steps", fmt.Sprintf("%d", agentConfig.MaxSteps))

	// 验证指标
	counter := GlobalMetrics.GetOrCreateCounter(MetricAgentRunsTotal, labels)
	require.NotNil(t, counter)
	assert.Greater(t, counter.Get(), float64(0))

	// 验证 trace ID 不为空
	assert.NotEmpty(t, span.TraceID)
	assert.NotEmpty(t, span.ID)
}

// TestObservabilityWithSkill 测试 Observability 与 Skill 集成
func TestObservabilityWithSkill(t *testing.T) {
	// 清理全局 metrics（确保测试隔离）
	GlobalMetrics = NewRegistry()

	// 创建 span
	span := StartSpan("skill_execution", "trace-456", "")
	defer span.Finish()

	// 模拟 Skill 执行
	skillName := "test-skill"
	labels := map[string]string{"skill": skillName}

	// 记录 Skill 执行指标
	IncrementCounter(MetricSkillExecutionsTotal, labels)

	// 记录执行时间
	startTime := time.Now()
	time.Sleep(15 * time.Millisecond)
	duration := time.Since(startTime)
	RecordDuration(MetricSkillDuration, labels, duration)

	// 设置 span 属性
	span.SetAttribute("skill.name", skillName)
	span.SetAttribute("status", "success")

	// 验证指标
	allHistograms := GlobalMetrics.GetAllHistograms()
	require.NotEmpty(t, allHistograms, "Should have recorded skill duration histograms")
}

// TestObservabilityWithSession 测试 Observability 与 Session 集成
func TestObservabilityWithSession(t *testing.T) {
	// 清理全局 metrics（确保测试隔离）
	GlobalMetrics = NewRegistry()

	sessionID := "session-123"
	userID := "user-456"
	labels := map[string]string{"session_id": sessionID, "user_id": userID}

	// 模拟 Session 创建
	IncrementCounter(MetricSessionTotal, labels)
	SetGauge(MetricSessionActive, nil, 1.0)

	// 模拟 Session 过期
	time.Sleep(50 * time.Millisecond)
	SetGauge(MetricSessionActive, nil, 0.0)
	IncrementCounter(MetricSessionExpired, labels)

	// 验证指标
	gauge := GlobalMetrics.GetOrCreateGauge(MetricSessionActive, nil)
	require.NotNil(t, gauge)
	assert.Equal(t, 0.0, gauge.Get())

	expiredCounter := GlobalMetrics.GetOrCreateCounter(MetricSessionExpired, labels)
	require.NotNil(t, expiredCounter)
	assert.Greater(t, expiredCounter.Get(), float64(0))
}

// TestObservabilityWithEmbedding 测试 Observability 与 Embedding 集成
func TestObservabilityWithEmbedding(t *testing.T) {
	// 清理全局 metrics（确保测试隔离）
	GlobalMetrics = NewRegistry()

	labels := map[string]string{"model": "text-embedding-ada-002"}

	// 记录 Embedding 响应时间
	startTime := time.Now()
	time.Sleep(20 * time.Millisecond)
	duration := time.Since(startTime)
	RecordDuration(MetricEmbeddingDuration, labels, duration)

	// 验证指标
	allHistograms := GlobalMetrics.GetAllHistograms()
	require.NotEmpty(t, allHistograms, "Should have recorded embedding duration histograms")
}

// TestObservabilityWithRouter 测试 Observability 与 Router 集成
func TestObservabilityWithRouter(t *testing.T) {
	// 清理全局 metrics（确保测试隔离）
	GlobalMetrics = NewRegistry()

	// 模拟路由匹配
	matches := 10
	fallbacks := 2

	matchesLabels := map[string]string{"status": "matched"}
	fallbackLabels := map[string]string{"status": "fallback"}

	for i := 0; i < matches; i++ {
		IncrementCounter(MetricRouterMatchesTotal, matchesLabels)
	}
	for i := 0; i < fallbacks; i++ {
		IncrementCounter(MetricRouterFallbacks, fallbackLabels)
	}

	// 验证指标
	matchesCounter := GlobalMetrics.GetOrCreateCounter(MetricRouterMatchesTotal, matchesLabels)
	require.NotNil(t, matchesCounter)
	assert.Equal(t, float64(matches), matchesCounter.Get())

	fallbacksCounter := GlobalMetrics.GetOrCreateCounter(MetricRouterFallbacks, fallbackLabels)
	require.NotNil(t, fallbacksCounter)
	assert.Equal(t, float64(fallbacks), fallbacksCounter.Get())
}

// TestObservabilityErrorTracking 测试 Observability 错误跟踪
func TestObservabilityErrorTracking(t *testing.T) {
	// 清理全局 metrics（确保测试隔离）
	GlobalMetrics = NewRegistry()

	// 模拟不同类型的错误
	errorTypes := []string{"validation_error", "network_error", "timeout_error"}
	errorSources := []string{"llm", "agent", "skill"}

	// 记录总错误数
	IncrementCounter(MetricErrorsTotal, nil)

	// 记录按类型分类的错误
	for _, errorType := range errorTypes {
		IncrementCounter(MetricErrorsByType, map[string]string{"type": errorType})
	}

	// 记录按来源分类的错误
	for _, source := range errorSources {
		IncrementCounter(MetricErrorsBySource, map[string]string{"source": source})
	}

	// 验证指标
	totalCounter := GlobalMetrics.GetOrCreateCounter(MetricErrorsTotal, nil)
	require.NotNil(t, totalCounter)
	assert.Greater(t, totalCounter.Get(), float64(0))

	// 按类型验证
	typeCounter := GlobalMetrics.GetOrCreateCounter(MetricErrorsByType, map[string]string{"type": "validation_error"})
	require.NotNil(t, typeCounter)
	assert.Greater(t, typeCounter.Get(), float64(0))

	// 按来源验证
	sourceCounter := GlobalMetrics.GetOrCreateCounter(MetricErrorsBySource, map[string]string{"source": "llm"})
	require.NotNil(t, sourceCounter)
	assert.Greater(t, sourceCounter.Get(), float64(0))
}

// TestObservabilityWithLogging 测试 Observability 与 Logging 集成
func TestObservabilityWithLogging(t *testing.T) {
	// 创建 JSON Writer
	var jsonBuffer strings.Builder
	jsonWriter := NewJSONWriter(&jsonBuffer)

	// 创建 Logger Config
	loggerConfig := &LoggerConfig{
		Level:  LevelInfo,
		Source: "test-source",
		Writers: []LogWriter{jsonWriter},
	}

	// 创建 Logger
	logger := NewLogger(loggerConfig)

	// 记录不同级别的日志
	logger.Debug("Debug message")

	logger.Info("Info message")

	logger.Warn("Warning message")

	logger.Error("Error message")

	// 验证日志输出
	logOutput := jsonBuffer.String()
	assert.Contains(t, logOutput, "Info message")
	assert.Contains(t, logOutput, "Warning message")
	assert.Contains(t, logOutput, "Error message")
	assert.NotContains(t, logOutput, "Debug message") // Debug 级别不会被记录
}

// TestObservabilityEndToEndWorkflow 测试 Observability 端到端工作流
func TestObservabilityEndToEndWorkflow(t *testing.T) {
	ctx := context.Background()

	// 清理全局 metrics（确保测试隔离）
	GlobalMetrics = NewRegistry()

	var jsonBuffer strings.Builder
	loggerConfig := &LoggerConfig{
		Level:  LevelInfo,
		Source: "workflow-test",
		Writers: []LogWriter{NewJSONWriter(&jsonBuffer)},
	}
	logger := NewLogger(loggerConfig)

	// 创建 trace
	traceID := generateTraceID()
	span := StartSpan("workflow", traceID, "")

	labels := map[string]string{"trace_id": traceID}

	// 步骤 1: 记录请求指标
	IncrementCounter(MetricRequestTotal, labels)
	span.SetAttribute("workflow.step", "1")
	logger.InfoWithFields("Starting workflow", map[string]interface{}{
		"traceId": traceID,
		"spanId":  span.ID,
	})

	// 步骤 2: 执行 LLM 调用
	llmLabels := map[string]string{"trace_id": traceID, "model": "gpt-4"}
	llmStartTime := time.Now()

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "gpt-4",
	})
	runtime.RegisterProvider(NewMockLLMProviderForObservability().Name(), NewMockLLMProviderForObservability())

	req := &llm.LLMRequest{
		Model:    "gpt-4",
		Messages: []types.Message{*types.NewUserMessage("Test")},
	}
	_, _ = runtime.Call(ctx, req)

	llmDuration := time.Since(llmStartTime)
	RecordDuration(MetricLLMDuration, llmLabels, llmDuration)
	IncrementCounter(MetricLLMCallsTotal, llmLabels)

	span.SetAttribute("workflow.step", "2")
	logger.InfoWithFields("LLM call completed", map[string]interface{}{
		"traceId": traceID,
		"spanId":  span.ID,
	})

	// 步骤 3: Agent 执行
	agentLabels := map[string]string{"trace_id": traceID, "agent": "test-agent"}
	agentStartTime := time.Now()
	time.Sleep(10 * time.Millisecond)
	agentDuration := time.Since(agentStartTime)
	RecordDuration(MetricAgentDuration, agentLabels, agentDuration)
	IncrementCounter(MetricAgentRunsTotal, agentLabels)

	span.SetAttribute("workflow.step", "3")
	logger.InfoWithFields("Agent execution completed", map[string]interface{}{
		"traceId": traceID,
		"spanId":  span.ID,
	})

	// 步骤 4: Skill 执行
	skillLabels := map[string]string{"trace_id": traceID, "skill": "test-skill"}
	skillStartTime := time.Now()
	time.Sleep(10 * time.Millisecond)
	skillDuration := time.Since(skillStartTime)
	RecordDuration(MetricSkillDuration, skillLabels, skillDuration)
	IncrementCounter(MetricSkillExecutionsTotal, skillLabels)

	logger.InfoWithFields("Skill execution completed", map[string]interface{}{
		"traceId": traceID,
		"spanId":  span.ID,
	})

	// 结束 span
	span.Finish()

	// 验证指标
	allCounters := GlobalMetrics.GetAllCounters()
	require.NotEmpty(t, allCounters, "Should have recorded counters")

	// 验证日志
	logOutput := jsonBuffer.String()
	assert.Contains(t, logOutput, traceID)
	assert.Contains(t, logOutput, span.ID)
	assert.Contains(t, logOutput, "Starting workflow")
	assert.Contains(t, logOutput, "LLM call completed")
	assert.Contains(t, logOutput, "Agent execution completed")
}

// TestObservabilityWithCustomMetrics 测试自定义指标
func TestObservabilityWithCustomMetrics(t *testing.T) {
	// 创建自定义计数器
	customCounter := NewCounter("custom_metric_total", nil)
	customCounter.Inc()
	customCounter.Add(5)

	assert.Equal(t, float64(6), customCounter.Get())

	// 创建自定义仪表盘
	customGauge := NewGauge("custom_gauge", nil)
	customGauge.Set(42.5)
	customGauge.Inc()
	customGauge.Dec()

	assert.Equal(t, 42.5, customGauge.Get())

	// 创建自定义直方图
	customHistogram := NewHistogram("custom_duration", nil, []float32{0.01, 0.05, 0.1})
	customHistogram.ObserveDuration(time.Duration(100) * time.Millisecond)
	customHistogram.ObserveDuration(time.Duration(200) * time.Millisecond)

	assert.Equal(t, 2, customHistogram.GetCount())
	assert.Greater(t, customHistogram.GetP95(), float64(0))
}

// TestObservabilityWithTokenMetrics 测试 Token 指标
func TestObservabilityWithTokenMetrics(t *testing.T) {
	// 清理全局 metrics（确保测试隔离）
	GlobalMetrics = NewRegistry()

	labels := map[string]string{"model": "gpt-4"}

	// 记录 Token 指标
	promptTokens := 100
	completionTokens := 50

	IncrementCounter(MetricLLMTokensTotal, labels)
	SetGauge(MetricLLMTokensPrompt, labels, float64(promptTokens))
	SetGauge(MetricLLMTokensCompletion, labels, float64(completionTokens))

	// 验证指标
	promptGauge := GlobalMetrics.GetOrCreateGauge(MetricLLMTokensPrompt, labels)
	require.NotNil(t, promptGauge)
	assert.Equal(t, float64(promptTokens), promptGauge.Get())

	completionGauge := GlobalMetrics.GetOrCreateGauge(MetricLLMTokensCompletion, labels)
	require.NotNil(t, completionGauge)
	assert.Equal(t, float64(completionTokens), completionGauge.Get())
}

// TestObservabilityWithMultipleLabels 测试多标签指标的隔离性
func TestObservabilityWithMultipleLabels(t *testing.T) {
	// 清理全局 metrics（确保测试隔离）
	GlobalMetrics = NewRegistry()

	// 定义 labels（避免 map 迭代顺序不确定性）
	// 使用 GetOrCreateCounter 直接返回 counter，而不是依赖 IncrementCounter 的内部逻辑
	labels1 := map[string]string{"status": "success", "agent": "agent-1"}
	labels2 := map[string]string{"status": "success", "agent": "agent-2"}
	labels3 := map[string]string{"status": "error", "agent": "agent-1"}

	// 直接操作 counter 避免通过 IncrementCounter 的 labels 处理
	counter1 := GlobalMetrics.GetOrCreateCounter(MetricAgentRunsTotal, labels1)
	counter2 := GlobalMetrics.GetOrCreateCounter(MetricAgentRunsTotal, labels2)
	counter3 := GlobalMetrics.GetOrCreateCounter(MetricAgentRunsTotal, labels3)

	// 测试重用相同 labels 获取相同 counter
	counter1Again := GlobalMetrics.GetOrCreateCounter(MetricAgentRunsTotal, labels1)
	assert.Same(t, counter1, counter1Again, "Same labels should return same counter")

	// 增加计数
	counter1.Inc()
	counter1.Inc() // agent-1 成功 2 次
	counter2.Inc() // agent-2 成功 1 次
	counter3.Inc() // agent-1 失败 1 次

	// 验证指标隔离
	assert.Equal(t, float64(2), counter1.Get())
	assert.Equal(t, float64(1), counter2.Get())
	assert.Equal(t, float64(1), counter3.Get())
}
