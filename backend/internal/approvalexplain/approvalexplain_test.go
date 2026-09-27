package approvalexplain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// §4.13 共享真源契约：prompt / 摘要 / 规则降级 / 预算，runtime-server 与本地
// 模式都必须得到同样的结果。

func TestSystemPromptContract(t *testing.T) {
	require.Contains(t, SystemPrompt, "审批解释器")
	require.Contains(t, SystemPrompt, "不要建议批准或拒绝")
	require.Contains(t, SystemPrompt, "不要输出 JSON 或 Markdown 标题")
}

func TestUserPromptIncludesStructuredFactsAndFlattensArrays(t *testing.T) {
	pending := &chat.ApprovalRequest{
		ToolName:        "shell_exec",
		Reason:          "writes outside workspace",
		RiskLevel:       "high",
		RememberPattern: "cmd:rm:*",
		ArgsJSON:        json.RawMessage(`{"command":"rm -rf build/","paths":["build","dist"]}`),
	}
	prompt := UserPrompt(pending)
	require.True(t, strings.HasPrefix(prompt, "工具：shell_exec\n"), "prompt 必须从工具名开始: %q", prompt)
	require.Contains(t, prompt, "触发原因：writes outside workspace")
	require.Contains(t, prompt, "风险级别：high")
	require.Contains(t, prompt, "可记忆为：cmd:rm:*")
	require.Contains(t, prompt, "- command: rm -rf build/")
	// 数组参数必须展平成单行（否则会被拼成多行 JSON）。
	require.Contains(t, prompt, "- paths: build, dist")
	// 缺字段不产生空行/占位。
	require.NotContains(t, prompt, "触发原因：\n")

	empty := UserPrompt(&chat.ApprovalRequest{})
	require.Equal(t, "工具：unknown", empty)
	require.Equal(t, "", UserPrompt(nil))
}

func TestUserPromptTruncatesLongArguments(t *testing.T) {
	long := strings.Repeat("x", ArgumentLimit+500)
	prompt := UserPrompt(&chat.ApprovalRequest{
		ToolName: "shell_exec",
		ArgsJSON: json.RawMessage(`{"command":"` + long + `"}`),
	})
	require.Contains(t, prompt, "…", "超长参数必须按共享预算截断")
	require.NotContains(t, prompt, long)
}

func TestArgumentDigestContract(t *testing.T) {
	// 优先字段命中：只输出命中的行。
	digest := ArgumentDigest(json.RawMessage(`{"command":"echo hi","other":"x"}`))
	require.Equal(t, "- command: echo hi", digest)

	// 零命中：回退原始 JSON，而不是空摘要。
	digest = ArgumentDigest(json.RawMessage(`{"unknown_key":"value"}`))
	require.Contains(t, digest, "unknown_key")

	// 空参数不产生摘要行。
	require.Equal(t, "", ArgumentDigest(json.RawMessage(`{}`)))
	require.Equal(t, "", ArgumentDigest(json.RawMessage(`null`)))
	require.Equal(t, "", ArgumentDigest(nil))

	// 坏 JSON：回退原文（截断）。
	require.Equal(t, "{not json", ArgumentDigest(json.RawMessage(`{not json`)))
}

func TestArgumentDigestRespectsLineLimit(t *testing.T) {
	digest := ArgumentDigest(json.RawMessage(`{
		"command":"c","file_path":"f","url":"u","query":"q",
		"pattern":"p","glob":"g","patch":"x","diff":"y"
	}`))
	lines := strings.Split(digest, "\n")
	require.Len(t, lines, DigestLineLimit, "摘要行数必须受共享上限约束: %q", digest)
	require.True(t, strings.HasPrefix(lines[0], "- command: "))
	require.True(t, strings.HasPrefix(lines[5], "- glob: "))
}

func TestArgumentDigestFlattensNestedValues(t *testing.T) {
	digest := ArgumentDigest(json.RawMessage(`{"paths":["a","b"],"url":"u"}`))
	require.Equal(t, "- paths: a, b\n- url: u", digest)
}

func TestRuleBasedContract(t *testing.T) {
	require.Equal(t, "", RuleBased(nil))

	plain := RuleBased(&chat.ApprovalRequest{
		ToolName: "shell_exec",
		ArgsJSON: json.RawMessage(`{"command":"echo hi"}`),
	})
	require.Contains(t, plain, "工具：shell_exec")
	require.Contains(t, plain, "参数摘要：")
	require.Contains(t, plain, "- command: echo hi")
	require.Contains(t, plain, "不可记忆：该审批每次都需要人工确认")
	require.NotContains(t, plain, "（批准并勾选「记住」后生效）")

	rememberable := RuleBased(&chat.ApprovalRequest{ToolName: "shell_exec", RememberPattern: "cmd:echo:*"})
	require.Contains(t, rememberable, "可记忆为：cmd:echo:*（批准并勾选「记住」后生效）")
	require.NotContains(t, rememberable, "不可记忆")
}

func TestTruncateIsRuneSafe(t *testing.T) {
	require.Equal(t, "中文", Truncate("中文", 4), "按 rune 判断长度，不截断完整字符")
	require.Equal(t, "汉汉汉…", Truncate(strings.Repeat("汉", 10), 3))
	require.Equal(t, "abc", Truncate("  abc  ", 0), "limit<=0 不截断，只去空白")
}

func TestParseModeAliases(t *testing.T) {
	for raw, want := range map[string]Mode{
		"":             ModeOnDemand,
		"on_demand":    ModeOnDemand,
		"on-demand":    ModeOnDemand,
		"on":           ModeOnDemand,
		"OFF":          ModeOff,
		"none":         ModeOff,
		"disabled":     ModeOff,
		"pre_generate": ModePreGenerate,
		"pre":          ModePreGenerate,
	} {
		got, ok := ParseMode(raw)
		require.Truef(t, ok, "raw=%q", raw)
		require.Equalf(t, want, got, "raw=%q", raw)
	}
	_, ok := ParseMode("sometimes")
	require.False(t, ok)
}

func TestUsagePayloadShape(t *testing.T) {
	resp := &llm.LLMResponse{Usage: &runtimetypes.TokenUsage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
	}}
	payload := UsagePayload("req-1", "provider-a", "model-b", resp, nil)
	require.Equal(t, "req-1", payload["llm_request_id"])
	require.Equal(t, true, payload["success"])
	require.Equal(t, "approval_explain", payload["source"])
	require.Equal(t, "approval_explain", payload["origin"])
	require.Equal(t, "provider-a", payload["provider"])
	require.Equal(t, "model-b", payload["model"])
	require.Equal(t, 100, payload["usage_prompt_tokens"])
	require.Equal(t, 50, payload["usage_completion_tokens"])
	require.Equal(t, 150, payload["usage_total_tokens"])
	require.NotContains(t, payload, "error")

	failed := UsagePayload("req-2", "provider-a", "model-b", nil, fmt.Errorf("upstream 503"))
	require.Equal(t, false, failed["success"])
	require.Equal(t, "upstream 503", failed["error"])
	require.NotContains(t, failed, "usage_prompt_tokens")

	// 失败且有响应体（例如解析失败）也保持 success=false。
	partial := UsagePayload("req-3", "provider-a", "model-b", &llm.LLMResponse{}, fmt.Errorf("decode failed"))
	require.Equal(t, false, partial["success"])
	require.Equal(t, "decode failed", partial["error"])
}
