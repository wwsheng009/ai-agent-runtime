package agent

// subagent_report_test.go — submit_report 结构化回执契约测试（P1-2' 收尾）。
//
// 覆盖：
//   - 工具定义的 schema 直通与 type 补齐；
//   - 契约校验（required 缺失 / additionalProperties=false 未声明键 /
//     非对象 schema / nil 参数）；
//   - handleSubmitReport 的捕获、拒绝重复、unavailable 错误；
//   - loop 端到端：模型调用 submit_report → 回执覆盖最终 Output；
//     校验失败 → 模型可修正重交。

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func reportSchemaFixture() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"summary": map[string]interface{}{"type": "string"},
			"files":   map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
		},
		"required":             []string{"summary"},
		"additionalProperties": false,
	}
}

func TestSubmitReportToolDefinitionUsesSchema(t *testing.T) {
	schema := reportSchemaFixture()
	def := submitReportToolDefinition(schema)
	require.Equal(t, SubmitReportToolName, def.Name)
	require.Equal(t, "object", def.Parameters["type"])
	// 参数即 schema：required/properties 原样承载。
	require.Equal(t, []string{"summary"}, def.Parameters["required"])
	props, ok := def.Parameters["properties"].(map[string]interface{})
	require.True(t, ok)
	require.Contains(t, props, "summary")

	// nil schema → 最小对象契约（type 补齐）。
	minimal := submitReportToolDefinition(nil)
	require.Equal(t, "object", minimal.Parameters["type"])
}

func TestValidateStructuredReport(t *testing.T) {
	schema := reportSchemaFixture()

	require.NoError(t, validateStructuredReport(schema, map[string]interface{}{"summary": "done"}))
	require.NoError(t, validateStructuredReport(schema, map[string]interface{}{
		"summary": "done",
		"files":   []interface{}{"a.go", "b.go"},
	}))

	// required 缺失。
	err := validateStructuredReport(schema, map[string]interface{}{"files": []interface{}{}})
	require.Error(t, err)
	require.Contains(t, err.Error(), `missing required field "summary"`)

	// additionalProperties=false 拒绝未声明键。
	err = validateStructuredReport(schema, map[string]interface{}{"summary": "x", "extra": 1})
	require.Error(t, err)
	require.Contains(t, err.Error(), `undeclared field "extra"`)

	// nil 参数。
	require.Error(t, validateStructuredReport(schema, nil))

	// 非对象 schema 视为部署错误。
	err = validateStructuredReport(map[string]interface{}{"type": "array"}, map[string]interface{}{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "object-rooted")
}

func TestHandleSubmitReportCapturesOnce(t *testing.T) {
	loop := &ReActLoop{reportSchema: reportSchemaFixture()}

	output, err := loop.handleSubmitReport(map[string]interface{}{"summary": "first"})
	require.NoError(t, err)
	require.Contains(t, output, "accepted")
	require.Equal(t, "first", loop.reportSubmitted["summary"])

	// 重复提交拒绝：已接受的回执是终态。
	_, err = loop.handleSubmitReport(map[string]interface{}{"summary": "second"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "already accepted")
	require.Equal(t, "first", loop.reportSubmitted["summary"], "the first accepted report must win")

	// 校验失败不污染捕获。
	loop2 := &ReActLoop{reportSchema: reportSchemaFixture()}
	_, err = loop2.handleSubmitReport(map[string]interface{}{"nope": true})
	require.Error(t, err)
	require.Nil(t, loop2.reportSubmitted)

	// 未启用契约时 unavailable。
	loop3 := &ReActLoop{}
	_, err = loop3.handleSubmitReport(map[string]interface{}{"summary": "x"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unavailable")
}

// ---- loop 端到端 ----------------------------------------------------------

func TestReActLoop_SubmitReportContractEndToEnd(t *testing.T) {
	provider := &SequenceLLMProvider{
		name: "report-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{ID: "call-report", Name: SubmitReportToolName, Args: map[string]interface{}{
						"summary": "implemented and verified",
						"files":   []interface{}{"a.go"},
					}},
				},
				Usage: &types.TokenUsage{},
			},
			{Content: "report submitted", Model: "test-model", Usage: &types.TokenUsage{}},
		},
	}
	llmRuntime := llm.NewLLMRuntime(nil)
	require.NoError(t, llmRuntime.RegisterProvider("report-provider", provider))

	agent := NewAgentWithLLM(&Config{
		Name:         "report-agent",
		Provider:     "report-provider",
		Model:        "test-model",
		SystemPrompt: "You are a worker.",
	}, nil, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        4,
		EnableThought:   true,
		EnableToolCalls: true,
	})

	result, err := loop.run(context.Background(), "do the work", loopRunOptions{
		TraceID:       "trace-report",
		IncludePrompt: true,
		ReportSchema:  reportSchemaFixture(),
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	// 已接受的回执是最终 Output（JSON），而不是后续文本。
	require.Contains(t, result.Output, `"summary":"implemented and verified"`)
	require.True(t, result.Success)

	// 请求列表中应包含 submit_report 工具定义（注入生效）。
	require.NotEmpty(t, provider.requests)
	found := false
	for _, def := range provider.requests[0].Tools {
		if def.Name == SubmitReportToolName {
			found = true
			break
		}
	}
	require.True(t, found, "submit_report must be injected into the model-facing tool surface")
}

func TestReActLoop_SubmitReportValidationFailureAllowsCorrection(t *testing.T) {
	provider := &SequenceLLMProvider{
		name: "report-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					// 缺少 required 字段 summary → 校验失败，模型应修正重交。
					{ID: "call-bad", Name: SubmitReportToolName, Args: map[string]interface{}{"files": []interface{}{}}},
				},
				Usage: &types.TokenUsage{},
			},
			{
				Content: "",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{ID: "call-good", Name: SubmitReportToolName, Args: map[string]interface{}{"summary": "fixed"}},
				},
				Usage: &types.TokenUsage{},
			},
			{Content: "done", Model: "test-model", Usage: &types.TokenUsage{}},
		},
	}
	llmRuntime := llm.NewLLMRuntime(nil)
	require.NoError(t, llmRuntime.RegisterProvider("report-provider", provider))

	agent := NewAgentWithLLM(&Config{
		Name:         "report-agent",
		Provider:     "report-provider",
		Model:        "test-model",
		SystemPrompt: "You are a worker.",
	}, nil, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        6,
		EnableThought:   true,
		EnableToolCalls: true,
	})

	result, err := loop.run(context.Background(), "do the work", loopRunOptions{
		TraceID:       "trace-report-fix",
		IncludePrompt: true,
		ReportSchema:  reportSchemaFixture(),
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, result.Output, `"summary":"fixed"`)
	require.False(t, strings.Contains(result.Output, `"files":[]`),
		"the corrected report must replace the rejected submission")
}