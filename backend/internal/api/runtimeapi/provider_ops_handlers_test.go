package runtimeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/providerops"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

type fakeProviderOpsService struct {
	fetchReq      RuntimeProviderModelsRequest
	autoImportReq RuntimeProviderAutoImportRequest
	probeReq      RuntimeProviderProbeRequest

	fetchResult      *RuntimeProviderModelsResult
	autoImportResult *RuntimeProviderAutoImportResult
	probeResult      *RuntimeProviderProbeResult
	err              error
}

func (s *fakeProviderOpsService) FetchModels(_ context.Context, req RuntimeProviderModelsRequest) (*RuntimeProviderModelsResult, error) {
	s.fetchReq = req
	if s.err != nil {
		return nil, s.err
	}
	return s.fetchResult, nil
}

func (s *fakeProviderOpsService) AutoImport(_ context.Context, req RuntimeProviderAutoImportRequest) (*RuntimeProviderAutoImportResult, error) {
	s.autoImportReq = req
	if s.err != nil {
		return nil, s.err
	}
	return s.autoImportResult, nil
}

func (s *fakeProviderOpsService) ProbeModels(_ context.Context, req RuntimeProviderProbeRequest) (*RuntimeProviderProbeResult, error) {
	s.probeReq = req
	if s.err != nil {
		return nil, s.err
	}
	return s.probeResult, nil
}

func newProviderOpsTestRouter(service ProviderOpsService) *mux.Router {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetProviderOpsService(service)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	return router
}

func TestFetchRuntimeProviderModels(t *testing.T) {
	service := &fakeProviderOpsService{
		fetchResult: &RuntimeProviderModelsResult{
			Endpoint:         "https://example.com/v1/models",
			StatusCode:       200,
			VerifiedAt:       "2026-09-18T00:00:00Z",
			ModelIDs:         []string{"gpt-4o"},
			AllModelIDs:      []string{"gpt-4o", "claude-sonnet-4"},
			AnonymousAllowed: true,
			Models:           []providerops.ModelInfo{{ID: "gpt-4o", DisplayName: "gpt-4o"}},
			Classification: &RuntimeProviderClassification{
				LoginProtocol:   "openai",
				RuntimeProtocol: "openai",
				TotalModels:     2,
				PrimaryModelIDs: []string{"gpt-4o"},
			},
		},
	}
	router := newProviderOpsTestRouter(service)

	body := []byte(`{"name":"my-gateway","base_url":"https://example.com","api_key":"sk-test","protocol":"openai","timeout_seconds":45}`)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/providers/fetch-models", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "my-gateway", service.fetchReq.Name)
	require.Equal(t, "https://example.com", service.fetchReq.BaseURL)
	require.Equal(t, "sk-test", service.fetchReq.APIKey)
	require.Equal(t, "openai", service.fetchReq.Protocol)
	require.Equal(t, 45, service.fetchReq.TimeoutSeconds)

	var payload RuntimeProviderModelsResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, "https://example.com/v1/models", payload.Endpoint)
	require.Equal(t, []string{"gpt-4o"}, payload.ModelIDs)
	require.True(t, payload.AnonymousAllowed)
	require.NotNil(t, payload.Classification)
	require.Equal(t, 2, payload.Classification.TotalModels)
}

func TestFetchRuntimeProviderModelsRequiresTarget(t *testing.T) {
	router := newProviderOpsTestRouter(&fakeProviderOpsService{})

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/providers/fetch-models", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFetchRuntimeProviderModelsWithoutServiceIsUnavailable(t *testing.T) {
	// 未注入服务且 handler 无配置快照时也必须走默认实现而不是 503：
	// 只要请求里带 base_url，就应由 providerops 处理（这里断言不是 503）。
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/providers/fetch-models", bytes.NewReader([]byte(`{"base_url":""}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAutoImportRuntimeProvider(t *testing.T) {
	service := &fakeProviderOpsService{
		autoImportResult: &RuntimeProviderAutoImportResult{
			Name:            "my-gateway",
			Protocol:        "openai",
			BaseURL:         "https://example.com",
			APIPath:         "/v1/chat/completions",
			DefaultModel:    "gpt-4o",
			SupportedModels: []string{"gpt-4o"},
			SupportTypes:    []string{"chat"},
		},
	}
	router := newProviderOpsTestRouter(service)

	body := []byte(`{"name":"my-gateway","base_url":"https://example.com","default_model":"gpt-4o"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/providers/auto-import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "gpt-4o", service.autoImportReq.DefaultModel)
	require.Equal(t, "my-gateway", service.autoImportReq.Name)

	var payload RuntimeProviderAutoImportResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, "/v1/chat/completions", payload.APIPath)
	require.Equal(t, []string{"gpt-4o"}, payload.SupportedModels)
}

func TestProbeRuntimeProviderModels(t *testing.T) {
	service := &fakeProviderOpsService{
		probeResult: &RuntimeProviderProbeResult{
			Results: []providerops.ProbeResult{{ModelID: "gpt-4o", Protocol: "openai", Verdict: "ok", StatusCode: 200}},
		},
	}
	router := newProviderOpsTestRouter(service)

	body := []byte(`{"name":"my-gateway","base_url":"https://example.com","models":["gpt-4o"],"protocols":["openai"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/providers/probe-models", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"gpt-4o"}, service.probeReq.Models)
	require.Equal(t, []string{"openai"}, service.probeReq.Protocols)

	var payload RuntimeProviderProbeResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Results, 1)
	require.Equal(t, "ok", payload.Results[0].Verdict)
}

func TestRuntimeProviderOpsResolveProviderPrefersRequestFields(t *testing.T) {
	config := &agentconfig.Config{}
	config.Providers.Items = map[string]agentconfig.Provider{
		"saved": {
			BaseURL:      "https://saved.example.com",
			Protocol:     "anthropic",
			APIPath:      "/v1/messages",
			APIKeyRef:    "saved-ref",
			SupportTypes: []string{"chat"},
		},
	}
	service := newRuntimeProviderOpsService(func() *agentconfig.Config { return config })

	provider := service.resolveProvider(RuntimeProviderModelsRequest{
		Name:     "saved",
		BaseURL:  "https://override.example.com",
		APIKey:   "sk-inline",
		Protocol: "openai",
	})
	require.Equal(t, "https://override.example.com", provider.BaseURL)
	require.Equal(t, "openai", provider.Protocol)
	require.Equal(t, "sk-inline", provider.APIKey)
	// 未在请求里覆盖的已保存字段仍然保留。
	require.Equal(t, "/v1/messages", provider.APIPath)
	require.Equal(t, "saved-ref", provider.APIKeyRef)
	require.Equal(t, "api_key", provider.AuthMode)
}

func TestRuntimeProviderOpsTimeoutIsCapped(t *testing.T) {
	require.Equal(t, runtimeProviderOpsDefaultTimeout, runtimeProviderOpsTimeout(0))
	// Duration/Duration 相除会退化为整数（45ns），这里显式断言 45 秒透传。
	require.Equal(t, 45*time.Second, runtimeProviderOpsTimeout(45))
	require.Equal(t, runtimeProviderOpsMaxTimeout, runtimeProviderOpsTimeout(600))
}

func TestPickRuntimeProviderDefaultModel(t *testing.T) {
	require.Equal(t, "gpt-4o", pickRuntimeProviderDefaultModel("gpt-4o", []string{"gpt-4o", "claude-sonnet-4"}))
	// 大小写不敏感命中时回填配置里的规范 ID。
	require.Equal(t, "gpt-4o", pickRuntimeProviderDefaultModel("GPT-4O", []string{"gpt-4o"}))
	// 请求的默认模型不在列表里时退化为第一个可用模型。
	require.Equal(t, "gpt-4o", pickRuntimeProviderDefaultModel("missing", []string{"gpt-4o"}))
	require.Equal(t, "", pickRuntimeProviderDefaultModel("", nil))
}
