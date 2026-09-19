package commands

import (
	"context"
	"net/http"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/providerops"
)

// ---------------------------------------------------------------------------
// /models 拉取与校验（commands 薄转发层）。
//
// 实现本体已迁移至 internal/providerops（models.go），与 runtime server 的
// /api/runtime/providers/fetch-models、auto-import、probe-models 共用同一份
// 逻辑；本文件只保留既有调用点（provider_login / web_config_handlers）与
// 单元测试使用的类型别名和转发函数。
// ---------------------------------------------------------------------------

// providerModelInfo 是 /models 响应解析出的单个模型（JSON 形状不变）。
type providerModelInfo = providerops.ModelInfo

// providerModelsValidationRequest 是一次 /models 校验请求。
type providerModelsValidationRequest = providerops.FetchModelsRequest

// providerModelsValidationResult 是一次 /models 校验结果。
type providerModelsValidationResult = providerops.FetchModelsResult

// validateProviderModels 拉取并校验 provider 的 /models 端点。
func validateProviderModels(req providerModelsValidationRequest) (*providerModelsValidationResult, error) {
	return providerops.FetchModels(context.Background(), req)
}

// modelsEndpointAllowsAnonymous 探测 models 端点是否校验 API key。
func modelsEndpointAllowsAnonymous(client *http.Client, endpoint, loginProtocol string) bool {
	return providerops.ModelsEndpointAllowsAnonymous(client, endpoint, loginProtocol)
}

func buildProviderModelsHeaders(provider config.Provider, loginProtocol string) map[string]string {
	return providerops.BuildProviderModelsHeaders(provider, loginProtocol)
}

func providerModelsAPIKey(provider config.Provider) string {
	return providerops.APIKey(provider)
}

func resolveProviderModelsPath(loginProtocol string, provider config.Provider, override string) string {
	return providerops.ResolveProviderModelsPath(loginProtocol, provider, override)
}

func defaultModelsPath(loginProtocol, baseURL string) string {
	return providerops.DefaultModelsPath(loginProtocol, baseURL)
}

func buildProviderModelsURL(provider config.Provider, modelsPath string) (string, error) {
	return providerops.BuildProviderModelsURL(provider, modelsPath)
}

func parseProviderModelsResponse(raw []byte, loginProtocol string) ([]providerModelInfo, error) {
	return providerops.ParseProviderModelsResponse(raw, loginProtocol)
}

func normalizeProviderModelID(id, loginProtocol string) string {
	return providerops.NormalizeProviderModelID(id, loginProtocol)
}

func dedupeProviderModels(models []providerModelInfo) []providerModelInfo {
	return providerops.DedupeProviderModels(models)
}

func providerModelIDs(models []providerModelInfo) []string {
	return providerops.ModelIDs(models)
}

func dedupeProviderStringOptions(values []string) []string {
	return providerops.DedupeProviderStringOptions(values)
}

func responsePreview(raw []byte, limit int) string {
	return providerops.ResponsePreview(raw, limit)
}
