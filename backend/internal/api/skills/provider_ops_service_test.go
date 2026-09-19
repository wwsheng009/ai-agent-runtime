package skills

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// TestRuntimeProviderOpsFetchModelsUsesSharedCore 是 runtime server 入口与
// aicli micro web client 共用同一套后端核心逻辑的端到端证明：这里不注入 fake
// service，而是让默认实现真正走 internal/providerops 去拉取 /v1/models。
func TestRuntimeProviderOpsFetchModelsUsesSharedCore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"}]}`))
	}))
	defer server.Close()

	service := newRuntimeProviderOpsService(func() *agentconfig.Config { return nil })
	result, err := service.FetchModels(context.Background(), RuntimeProviderModelsRequest{
		Name:     "smoke",
		BaseURL:  server.URL,
		APIKey:   "sk-smoke",
		Protocol: "openai",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.ModelIDs)
	require.Contains(t, result.ModelIDs, "gpt-4o")
	require.Equal(t, server.URL+"/v1/models", result.Endpoint)
}
