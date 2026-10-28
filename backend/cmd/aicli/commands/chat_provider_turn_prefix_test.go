package commands

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// prefixProbeAdapter 暴露真实的预算字段，用于验证 aicliProviderTurnExecutor
// 走的是统一的前缀缓存友好序列化器（而不是 map 字典序的 json.Marshal）。
type prefixProbeAdapter struct {
	recordingProtocolAdapter
}

func (a *prefixProbeAdapter) BuildRequest(cfg adapter.RequestConfig) map[string]interface{} {
	return map[string]interface{}{
		"model":       cfg.Model,
		"messages":    cfg.Messages,
		"stream":      cfg.Stream,
		"max_tokens":  8000,
		"temperature": 0.7,
	}
}

// 本地 chat 的 provider 执行器自己构造请求体：若退回 json.Marshal(map)，
// max_tokens 会排在 messages 之前，任何预算变化都改写缓存前缀。这里固化
// 它必须与 llm/gateway 共用 MarshalProviderRequestBody 的字段序契约。
func TestAICLIProviderTurnExecutor_BodyKeepsContentPrefixAheadOfMaxTokens(t *testing.T) {
	var rawBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		rawBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer server.Close()

	session := &ChatSession{
		ProviderName:  "north",
		Provider:      config.Provider{Protocol: "openai", BaseURL: server.URL},
		Adapter:       &prefixProbeAdapter{recordingProtocolAdapter: recordingProtocolAdapter{response: map[string]interface{}{"role": "assistant", "content": "done"}}},
		Model:         "cn:hy3",
		BaseURL:       server.URL,
		HTTPClient:    server.Client(),
		cancelCtx:     context.Background(),
		NoInteractive: true,
	}

	turn, err := (&aicliProviderTurnExecutor{session: session}).Complete(context.Background(), runtimechatcore.ProviderTurnRequest{
		Messages: []types.Message{*types.NewUserMessage("hello")},
	})
	require.NoError(t, err)
	require.NotNil(t, turn)
	require.NotEmpty(t, rawBody, "请求体必须已发出")

	require.True(t, strings.HasPrefix(rawBody, `{"messages":`),
		"内容主体必须是首字段: %s", rawBody[:min(80, len(rawBody))])
	maxTokensIdx := strings.Index(rawBody, `"max_tokens":`)
	require.Greater(t, maxTokensIdx, 0)
	require.Greater(t, maxTokensIdx, strings.Index(rawBody, `"messages":`),
		"max_tokens 必须排在 messages 之后，避免预算变化改写缓存前缀")
}
