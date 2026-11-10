package llm

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func commonPrefixLen(a, b []byte) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// 复现 session_20261028063206_ZW5KbyLq seq4→7 / 7→8 抓包：encoding/json 对
// map 按字典序输出，max_tokens 排在 messages 之前，重试把 8000 加宽到 16000
// 时会改写请求体第 14 字节起的内容，provider 侧前缀缓存整段失配。修复后
// 内容主体（messages/tools）必须先于易变参数序列化，重试只允许在其后分叉。
func TestMarshalProviderRequestBody_PrefixSurvivesMaxTokensChange(t *testing.T) {
	messages := make([]map[string]interface{}, 0, 30)
	for i := 0; i < 30; i++ {
		messages = append(messages, map[string]interface{}{
			"role":    "user",
			"content": strings.Repeat("上下文消息-", 40) + string(rune('a'+i%26)),
		})
	}
	tools := []map[string]interface{}{
		{"type": "function", "function": map[string]interface{}{"name": "shell", "description": "run"}},
	}
	base := map[string]interface{}{
		"messages":         messages,
		"tools":            tools,
		"max_tokens":       8000,
		"model":            "cn:hy3",
		"stream":           true,
		"temperature":      0.7,
		"prompt_cache_key": "session_test#prompt-cache-epoch-2",
		"reasoning_effort": "max",
	}
	widened := make(map[string]interface{}, len(base))
	for key, value := range base {
		widened[key] = value
	}
	widened["max_tokens"] = 16000

	first, err := MarshalProviderRequestBody(base)
	require.NoError(t, err)
	second, err := MarshalProviderRequestBody(widened)
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(string(first), `{"messages":`),
		"内容主体必须是请求体的首字段: %s", string(first[:min(80, len(first))]))
	require.NotEqual(t, string(first), string(second))

	// 内容主体（messages + tools）必须逐字节保留：分叉只允许发生在
	// max_tokens（第一个易变参数）之后。
	cut := strings.Index(string(first), `,"max_tokens":`)
	require.Greater(t, cut, 0)
	require.True(t, strings.HasPrefix(string(second), string(first)[:cut]),
		"重试加宽 max_tokens 不得改写内容主体前缀")
	require.Greater(t, cut, len(`{"messages":[{"content":`))

	// 语义等价：除 max_tokens 外逐字段一致。
	var decodedFirst, decodedSecond map[string]interface{}
	require.NoError(t, json.Unmarshal(first, &decodedFirst))
	require.NoError(t, json.Unmarshal(second, &decodedSecond))
	require.Equal(t, decodedFirst["messages"], decodedSecond["messages"])
	require.Equal(t, decodedFirst["tools"], decodedSecond["tools"])
	require.NotEqual(t, decodedFirst["max_tokens"], decodedSecond["max_tokens"])

	// 与前缀缓存修复前的行为对比：map 字典序序列化的公共前缀只有
	// `{"max_tokens":`（8000 与 16000 在第一个数字处分叉）。
	legacyFirst, err := json.Marshal(base)
	require.NoError(t, err)
	legacySecond, err := json.Marshal(widened)
	require.NoError(t, err)
	legacyCommon := commonPrefixLen(legacyFirst, legacySecond)
	require.Equal(t, len(`{"max_tokens":`), legacyCommon)
	require.Greater(t, cut, legacyCommon,
		"修复后的可复用前缀必须显著长于修复前")
}

// 字段序必须确定（多次序列化字节一致），且除顺序外与 json.Marshal 语义一致
// （同样的 HTML 转义与数值格式）。
func TestMarshalProviderRequestBody_DeterministicAndSemanticallyEquivalent(t *testing.T) {
	body := map[string]interface{}{
		"messages":    []interface{}{map[string]interface{}{"role": "user", "content": "1 < 2 & 3"}},
		"max_tokens":  1234,
		"temperature": 0.7,
		"model":       "m",
		"stream":      true,
	}

	first, err := MarshalProviderRequestBody(body)
	require.NoError(t, err)
	second, err := MarshalProviderRequestBody(body)
	require.NoError(t, err)
	require.Equal(t, string(first), string(second), "字段序必须确定")

	reference, err := json.Marshal(body)
	require.NoError(t, err)
	var fromWrapped, fromReference map[string]interface{}
	require.NoError(t, json.Unmarshal(first, &fromWrapped))
	require.NoError(t, json.Unmarshal(reference, &fromReference))
	require.Equal(t, fromReference, fromWrapped)

	// 与 json.Marshal 相同的 HTML 转义语义。
	require.Contains(t, string(first), `1 \u003c 2 \u0026 3`)

	// 其余字段保持字典序，且排在内容主体之后。
	modelIdx := strings.Index(string(first), `"model"`)
	maxTokensIdx := strings.Index(string(first), `"max_tokens"`)
	require.Greater(t, modelIdx, strings.Index(string(first), `"messages"`))
	require.Less(t, maxTokensIdx, modelIdx, "易变参数之间保持既有字典序")
}

// TestMarshalProviderRequestBody_ValueBytesMatchJSONMarshalExactly 锁定
// 直接写入最终 buffer 的编码路径与 json.Marshal 逐字节一致：字段值不再
// 先分配一份完整 []byte 再复制，但 HTML 转义、Unicode、嵌套 map 与
// 数值格式必须完全不变。
func TestMarshalProviderRequestBody_ValueBytesMatchJSONMarshalExactly(t *testing.T) {
	values := map[string]interface{}{
		"html":     "1 < 2 & 3 > 0",
		"unicode":  "中文/emoji: \U0001F600",
		"nested":   map[string]interface{}{"b": []interface{}{1, 2.5, true, nil}, "a": "x"},
		"raw":      json.RawMessage(`{"k":"v"}`),
		"nil":      nil,
		"number":   1.23456789012345,
		"largeint": int64(1 << 53),
	}
	for name, value := range values {
		t.Run(name, func(t *testing.T) {
			body := map[string]interface{}{"messages": value}
			got, err := MarshalProviderRequestBody(body)
			require.NoError(t, err)
			want, err := json.Marshal(body)
			require.NoError(t, err)
			require.Equal(t, string(want), string(got))
		})
	}
}

func TestMarshalProviderRequestBody_EmptyBodyFallsBackToJSONMarshal(t *testing.T) {
	got, err := MarshalProviderRequestBody(nil)
	require.NoError(t, err)
	require.Equal(t, "null", string(got))
}

func legacyMarshalProviderRequestBody(body map[string]interface{}) ([]byte, error) {
	if len(body) == 0 {
		return json.Marshal(body)
	}
	ordered := make([]string, 0, len(body))
	seen := make(map[string]bool, len(body))
	for _, key := range requestBodyStablePrefixKeys {
		if _, ok := body[key]; !ok || seen[key] {
			continue
		}
		seen[key] = true
		ordered = append(ordered, key)
	}
	rest := make([]string, 0, len(body)-len(ordered))
	for key := range body {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	ordered = append(ordered, rest...)

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range ordered {
		if i > 0 {
			buf.WriteByte(',')
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		buf.Write(encodedKey)
		buf.WriteByte(':')
		encodedValue, err := json.Marshal(body[key])
		if err != nil {
			return nil, err
		}
		buf.Write(encodedValue)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func BenchmarkMarshalProviderRequestBody(b *testing.B) {
	body := benchDebugRequestBody()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := MarshalProviderRequestBody(body); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMarshalProviderRequestBodyLegacy(b *testing.B) {
	body := benchDebugRequestBody()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := legacyMarshalProviderRequestBody(body); err != nil {
			b.Fatal(err)
		}
	}
}

func largeProviderRequestBody() map[string]interface{} {
	return map[string]interface{}{
		"messages": []interface{}{map[string]interface{}{
			"role":    "assistant",
			"content": strings.Repeat("x", 4<<20),
		}},
		"model":  "bench-model",
		"stream": true,
	}
}

func BenchmarkMarshalProviderRequestBodyLarge(b *testing.B) {
	body := largeProviderRequestBody()
	b.SetBytes(4 << 20)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := MarshalProviderRequestBody(body); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMarshalProviderRequestBodyLargeLegacy(b *testing.B) {
	body := largeProviderRequestBody()
	b.SetBytes(4 << 20)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := legacyMarshalProviderRequestBody(body); err != nil {
			b.Fatal(err)
		}
	}
}

// 审计四个协议适配器的 BuildRequest 键集合后固化契约：内容键必须整体排在
// 易变预算参数之前。否则 max_tokens / max_output_tokens / generationConfig
// 的任何变化都会改写可复用的缓存前缀（含 gemini 的 systemInstruction 与
// codex 的 instructions）。
func TestMarshalProviderRequestBody_AdapterKeySetsKeepContentAheadOfVolatile(t *testing.T) {
	cases := []struct {
		name        string
		body        map[string]interface{}
		content     []string
		volatile    string
		volatileNew interface{}
	}{
		{
			name: "openai",
			body: map[string]interface{}{
				"messages":         []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
				"tools":            []interface{}{map[string]interface{}{"type": "function"}},
				"model":            "m",
				"stream":           true,
				"max_tokens":       8000,
				"prompt_cache_key": "k",
				"reasoning_effort": "max",
			},
			content:     []string{"messages", "tools"},
			volatile:    "max_tokens",
			volatileNew: 16000,
		},
		{
			name: "anthropic",
			body: map[string]interface{}{
				"messages":   []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
				"system":     "sys",
				"tools":      []interface{}{map[string]interface{}{"name": "shell"}},
				"model":      "m",
				"stream":     true,
				"max_tokens": 4096,
				"thinking":   map[string]interface{}{"type": "adaptive"},
			},
			content:     []string{"messages", "system", "tools"},
			volatile:    "max_tokens",
			volatileNew: 8192,
		},
		{
			name: "gemini",
			body: map[string]interface{}{
				"contents":          []interface{}{map[string]interface{}{"role": "user", "parts": []interface{}{map[string]interface{}{"text": "hi"}}}},
				"systemInstruction": map[string]interface{}{"parts": []interface{}{map[string]interface{}{"text": "sys"}}},
				"tools":             []interface{}{map[string]interface{}{"functionDeclarations": []interface{}{}}},
				"generationConfig":  map[string]interface{}{"maxOutputTokens": 4096, "temperature": 0.7},
			},
			content:     []string{"contents", "systemInstruction", "tools"},
			volatile:    "generationConfig",
			volatileNew: map[string]interface{}{"maxOutputTokens": 8192, "temperature": 0.7},
		},
		{
			name: "codex",
			body: map[string]interface{}{
				"input":             []interface{}{map[string]interface{}{"type": "message", "role": "user"}},
				"instructions":      "sys",
				"tools":             []interface{}{map[string]interface{}{"type": "function"}},
				"model":             "m",
				"stream":            true,
				"max_output_tokens": 4096,
				"store":             false,
			},
			content:     []string{"input", "instructions", "tools"},
			volatile:    "max_output_tokens",
			volatileNew: 8192,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, err := MarshalProviderRequestBody(tc.body)
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(string(first), `{"`+tc.content[0]+`":`),
				"首个字段必须是内容键 %q: %s", tc.content[0], string(first[:min(60, len(first))]))

			volatileIdx := strings.Index(string(first), `"`+tc.volatile+`":`)
			require.Greater(t, volatileIdx, 0, "缺少易变键 %q", tc.volatile)
			for _, key := range tc.content {
				contentIdx := strings.Index(string(first), `"`+key+`":`)
				require.GreaterOrEqual(t, contentIdx, 0, "缺少内容键 %q", key)
				require.Less(t, contentIdx, volatileIdx,
					"内容键 %q 必须排在易变键 %q 之前", key, tc.volatile)
			}

			changed := make(map[string]interface{}, len(tc.body))
			for key, value := range tc.body {
				changed[key] = value
			}
			changed[tc.volatile] = tc.volatileNew
			second, err := MarshalProviderRequestBody(changed)
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(string(second), string(first)[:volatileIdx]),
				"易变键 %q 变化不得改写其之前的内容前缀", tc.volatile)
		})
	}
}
