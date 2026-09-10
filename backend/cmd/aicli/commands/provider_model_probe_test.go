package commands

import (
	"strings"
	"testing"
)

// 错误分类学单测：负向结论（unsupported）只认“网关明确拒绝该模型/格式”
// 的措辞；鉴权/配额/区域/参数错误一律 unknown，避免把 gpt-5.6-luna 这类
// 区域受限模型永久误判成“不支持该协议”。

func TestClassifyProviderModelProbeMessage(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    providerModelProbeVerdict
	}{
		{"空消息", "", probeVerdictUnknown},
		{"明确格式拒绝", "Model gpt-5.6 is not supported for format openai", probeVerdictUnsupported},
		{"模型不存在", "model deepseek-chat does not exist", probeVerdictUnsupported},
		{"unknown model", "unknown model foo-bar", probeVerdictUnsupported},
		{"no endpoints found", "no endpoints found for model", probeVerdictUnsupported},
		// 参数错误即使带 not supported 字样也必须 unknown：
		// “max_tokens is not supported for this model” 是请求问题不是协议问题。
		{"参数错误带 not supported", "max_tokens is not supported for this model", probeVerdictUnknown},
		{"鉴权失败", "invalid api key", probeVerdictUnknown},
		{"配额", "insufficient quota", probeVerdictUnknown},
		{"区域限制（绝不能判负向）", "this model is not available in your region", probeVerdictUnknown},
		{"限流", "rate limit exceeded", probeVerdictUnknown},
		{"过载", "the server is overloaded", probeVerdictUnknown},
		{"无法识别的措辞", "something went wrong entirely differently", probeVerdictUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyProviderModelProbeMessage(tc.message); got != tc.want {
				t.Fatalf("classifyProviderModelProbeMessage(%q) = %q, want %q", tc.message, got, tc.want)
			}
		})
	}
}

func TestClassifyProviderModelProbeResponse(t *testing.T) {
	openAISuccess := []byte(`{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
	anthropicSuccess := []byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn"}`)
	codexSuccess := []byte(`{"id":"resp_1","object":"response","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]}`)

	t.Run("200+choices 判 supported", func(t *testing.T) {
		verdict, _ := classifyProviderModelProbeResponse(200, openAISuccess, "openai")
		if verdict != probeVerdictSupported {
			t.Fatalf("verdict = %q, want supported", verdict)
		}
	})
	t.Run("200+anthropic 结构判 supported", func(t *testing.T) {
		verdict, _ := classifyProviderModelProbeResponse(200, anthropicSuccess, "anthropic")
		if verdict != probeVerdictSupported {
			t.Fatalf("verdict = %q, want supported", verdict)
		}
	})
	t.Run("200+codex output 判 supported", func(t *testing.T) {
		verdict, _ := classifyProviderModelProbeResponse(200, codexSuccess, "codex")
		if verdict != probeVerdictSupported {
			t.Fatalf("verdict = %q, want supported", verdict)
		}
	})
	t.Run("200 内嵌明确拒绝判 unsupported", func(t *testing.T) {
		body := []byte(`{"error":{"message":"Model x is not supported for format openai"}}`)
		verdict, _ := classifyProviderModelProbeResponse(200, body, "openai")
		if verdict != probeVerdictUnsupported {
			t.Fatalf("verdict = %q, want unsupported", verdict)
		}
	})
	t.Run("200 内嵌鉴权错误判 unknown", func(t *testing.T) {
		body := []byte(`{"error":{"message":"invalid api key"}}`)
		verdict, _ := classifyProviderModelProbeResponse(200, body, "openai")
		if verdict != probeVerdictUnknown {
			t.Fatalf("verdict = %q, want unknown", verdict)
		}
	})
	t.Run("401 格式拒绝判 unsupported（opencode.ai 行为）", func(t *testing.T) {
		body := []byte(`{"error":{"message":"Model gpt-5.6 is not supported for format anthropic"}}`)
		verdict, _ := classifyProviderModelProbeResponse(401, body, "anthropic")
		if verdict != probeVerdictUnsupported {
			t.Fatalf("verdict = %q, want unsupported", verdict)
		}
	})
	t.Run("401 普通鉴权失败判 unknown", func(t *testing.T) {
		body := []byte(`{"error":{"message":"unauthorized"}}`)
		verdict, _ := classifyProviderModelProbeResponse(401, body, "openai")
		if verdict != probeVerdictUnknown {
			t.Fatalf("verdict = %q, want unknown", verdict)
		}
	})
	t.Run("404 模型不存在判 unsupported", func(t *testing.T) {
		body := []byte(`{"error":{"message":"model not found"}}`)
		verdict, _ := classifyProviderModelProbeResponse(404, body, "openai")
		if verdict != probeVerdictUnsupported {
			t.Fatalf("verdict = %q, want unsupported", verdict)
		}
	})
	t.Run("404 路由 HTML 判 unknown", func(t *testing.T) {
		verdict, _ := classifyProviderModelProbeResponse(404, []byte("<html>404 page</html>"), "openai")
		if verdict != probeVerdictUnknown {
			t.Fatalf("verdict = %q, want unknown", verdict)
		}
	})
	t.Run("429 限流判 unknown", func(t *testing.T) {
		verdict, _ := classifyProviderModelProbeResponse(429, []byte(`{"error":{"message":"too many requests"}}`), "openai")
		if verdict != probeVerdictUnknown {
			t.Fatalf("verdict = %q, want unknown", verdict)
		}
	})
	t.Run("500 判 unknown", func(t *testing.T) {
		verdict, _ := classifyProviderModelProbeResponse(500, []byte("internal error"), "openai")
		if verdict != probeVerdictUnknown {
			t.Fatalf("verdict = %q, want unknown", verdict)
		}
	})
}

func TestProbeResponseLooksSuccessful(t *testing.T) {
	if !probeResponseLooksSuccessful("openai", map[string]interface{}{"choices": []interface{}{map[string]interface{}{}}}) {
		t.Fatal("openai choices 应判成功")
	}
	if probeResponseLooksSuccessful("openai", map[string]interface{}{"choices": []interface{}{}}) {
		t.Fatal("空 choices 不应判成功")
	}
	if !probeResponseLooksSuccessful("anthropic", map[string]interface{}{"id": "msg_1", "type": "message"}) {
		t.Fatal("anthropic id+type=message 应判成功")
	}
	if !probeResponseLooksSuccessful("codex", map[string]interface{}{"output": []interface{}{}}) {
		t.Fatal("codex output 数组应判成功")
	}
	if !probeResponseLooksSuccessful("gemini", map[string]interface{}{"candidates": []interface{}{}}) {
		t.Fatal("gemini candidates 应判成功")
	}
	if probeResponseLooksSuccessful("openai", map[string]interface{}{"foo": "bar"}) {
		t.Fatal("无法识别结构不应判成功")
	}
}

func TestProviderProbeCardID(t *testing.T) {
	cases := []struct {
		provider, host, model, protocol, wantPrefix string
	}{
		{"opencode_ai", "opencode.ai", "gpt-5.6", "codex", "probe.opencode-ai.gpt-5-6.codex"},
		{"", "deepseek.com", "deepseek-chat", "anthropic", "probe.deepseek-com.deepseek-chat.anthropic"},
		{"", "", "m", "openai", "probe.unknown.m.openai"},
	}
	for _, tc := range cases {
		got := providerProbeCardID(tc.provider, tc.host, tc.model, tc.protocol)
		if got != tc.wantPrefix {
			t.Fatalf("providerProbeCardID(%q,%q,%q,%q) = %q, want %q", tc.provider, tc.host, tc.model, tc.protocol, got, tc.wantPrefix)
		}
		if !strings.HasPrefix(got, "probe.") {
			t.Fatalf("card id %q 应以 probe. 开头", got)
		}
	}
}

func TestProviderProbeBaseURLHost(t *testing.T) {
	cases := []struct {
		baseURL, want string
	}{
		{"https://opencode.ai/zen/go/v1", "opencode.ai"},
		{"http://localhost:8317/v1", "localhost:8317"},
		{"", ""},
		{"not a url", ""},
	}
	for _, tc := range cases {
		if got := providerProbeBaseURLHost(tc.baseURL); got != tc.want {
			t.Fatalf("providerProbeBaseURLHost(%q) = %q, want %q", tc.baseURL, got, tc.want)
		}
	}
}
