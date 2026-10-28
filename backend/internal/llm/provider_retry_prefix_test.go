package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 端到端固化本次修复：length 型 reasoning-only 触发的「扩容重试」不得再改写
// 请求体内容前缀。修复前 max_tokens 是序列化首字段（map 字典序），8000→16000
// 会让请求体从第 14 字节起分叉，provider 侧前缀缓存整段失配
// （session_20261028063206_ZW5KbyLq seq4→7/7→8 抓包）。
func TestProviderRetryWidenedMaxTokensKeepsCachePrefix(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		mu.Lock()
		bodies = append(bodies, string(raw))
		requests++
		current := requests
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if current == 1 {
			// 预算被思维链吃满：finish_reason=length 型 reasoning-only，
			// provider 内层按 8k→16k 扩容后重试。
			fmt.Fprint(w, `{"id":"chatcmpl-1","object":"chat.completion","model":"cn:hy3",`+
				`"choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_content":"thinking hard"},"finish_reason":"length"}],`+
				`"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"completion_tokens_details":{"reasoning_tokens":50}}}`)
			return
		}
		fmt.Fprint(w, `{"id":"chatcmpl-2","object":"chat.completion","model":"cn:hy3",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105}}`)
	}))
	defer server.Close()

	provider, err := NewProvider(&ProviderConfig{
		Type:       "openai",
		BaseURL:    server.URL,
		MaxRetries: 2,
	})
	require.NoError(t, err)

	resp, err := provider.Call(context.Background(), &LLMRequest{
		Model:     "cn:hy3",
		MaxTokens: 8000,
		Messages: []types.Message{{
			Role:    "user",
			Content: "retry reasoning only",
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "done", resp.Content)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, bodies, 2, "第一次退化应答必须触发一次扩容重试")

	first, second := bodies[0], bodies[1]
	require.True(t, strings.HasPrefix(first, `{"messages":`),
		"内容主体必须是请求体的首字段: %s", first[:min(80, len(first))])
	cut := strings.Index(first, `,"max_tokens":`)
	require.Greater(t, cut, 0)
	require.True(t, strings.HasPrefix(second, first[:cut]),
		"扩容重试必须逐字节保留上一次 attempt 的 messages/tools 前缀")

	var decodedFirst, decodedSecond map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(first), &decodedFirst))
	require.NoError(t, json.Unmarshal([]byte(second), &decodedSecond))
	require.Equal(t, decodedFirst["messages"], decodedSecond["messages"])
	require.Greater(t,
		decodedSecond["max_tokens"].(float64),
		decodedFirst["max_tokens"].(float64),
		"重试应加宽 max_tokens")
}
