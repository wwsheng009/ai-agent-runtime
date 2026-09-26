package runtimeapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	httpclient "github.com/wwsheng009/ai-agent-runtime/internal/pkg/httpclient"
	"github.com/wwsheng009/ai-agent-runtime/internal/providerops"
)

const (
	runtimeProviderOpsDefaultTimeout = 30 * time.Second
	runtimeProviderOpsMaxTimeout     = 120 * time.Second
)

// runtimeProviderOpsService 是 ProviderOpsService 的默认实现：它只依赖
// runtime server 持有的 aicli 配置快照（用于补齐已保存 provider 的字段与
// provider 级代理/超时），模型发现、分类、元数据匹配与探测全部委托给
// internal/providerops —— 与 aicli CLI / micro web client 完全同一份实现。
type runtimeProviderOpsService struct {
	configSnapshot func() *agentconfig.Config
}

func newRuntimeProviderOpsService(configSnapshot func() *agentconfig.Config) *runtimeProviderOpsService {
	return &runtimeProviderOpsService{configSnapshot: configSnapshot}
}

func (s *runtimeProviderOpsService) snapshot() *agentconfig.Config {
	if s == nil || s.configSnapshot == nil {
		return nil
	}
	return s.configSnapshot()
}

// resolveProvider 用配置快照里的已保存 provider 作为基底，再叠加请求里显式给出的
// 字段。api_key 的优先级与 aicli login 一致：内联 key > auth store 引用 > 配置快照。
func (s *runtimeProviderOpsService) resolveProvider(req RuntimeProviderModelsRequest) agentconfig.Provider {
	var provider agentconfig.Provider
	cfg := s.snapshot()
	name := strings.TrimSpace(req.Name)
	if cfg != nil && name != "" {
		if existing, ok := cfg.Providers.Items[name]; ok {
			provider = existing
		}
	}
	if value := strings.TrimSpace(req.BaseURL); value != "" {
		provider.BaseURL = value
	}
	if value := strings.TrimSpace(req.Protocol); value != "" {
		provider.Protocol = value
	}
	if value := strings.TrimSpace(req.AuthMode); value != "" {
		provider.AuthMode = value
	}
	if value := strings.TrimSpace(req.ModelsPath); value != "" {
		provider.ModelsPath = value
	}
	if value := strings.TrimSpace(req.APIKey); value != "" {
		// 编辑器里新输入的 key 必须优先，否则会拿 auth store 旧值去校验。
		provider.APIKey = value
	}
	if value := strings.TrimSpace(req.APIKeyRef); value != "" {
		provider.APIKeyRef = value
	}
	if len(req.Headers) > 0 {
		merged := make(map[string]string, len(provider.Headers)+len(req.Headers))
		for key, value := range provider.Headers {
			merged[key] = value
		}
		for key, value := range req.Headers {
			merged[key] = value
		}
		provider.Headers = merged
	}
	if strings.TrimSpace(provider.AuthMode) == "" {
		provider.AuthMode = "api_key"
	}
	return provider
}

func (s *runtimeProviderOpsService) FetchModels(ctx context.Context, req RuntimeProviderModelsRequest) (*RuntimeProviderModelsResult, error) {
	cfg := s.snapshot()
	provider := s.resolveProvider(req)
	if strings.TrimSpace(provider.BaseURL) == "" {
		return nil, fmt.Errorf("base_url is required")
	}
	groupingProtocol := providerops.ResolveFetchModelsLoginProtocol(req.Protocol, provider)
	requestedProtocol := strings.TrimSpace(req.Protocol)
	if requestedProtocol == "" {
		requestedProtocol = groupingProtocol
	}

	fetch, err := providerops.FetchModels(ctx, providerops.FetchModelsRequest{
		Config:        cfg,
		ProviderName:  strings.TrimSpace(req.Name),
		Provider:      provider,
		LoginProtocol: groupingProtocol,
		ModelsPath:    provider.ModelsPath,
		Timeout:       runtimeProviderOpsTimeout(req.TimeoutSeconds),
	})
	if err != nil {
		return nil, err
	}

	result := &RuntimeProviderModelsResult{
		Endpoint:        fetch.Endpoint,
		StatusCode:      fetch.StatusCode,
		VerifiedAt:      fetch.VerifiedAt,
		Models:          fetch.Models,
		AllModelIDs:     providerops.ModelIDs(fetch.Models),
		LoginProtocol:   groupingProtocol,
		RuntimeProtocol: providerops.RuntimeProtocolForLoginProtocol(groupingProtocol),
	}
	if _, warnings, err := providerops.LoadModelCardCatalog(cfg); err == nil {
		result.Warnings = append(result.Warnings, modelCardWarningMessages(warnings)...)
	}

	metadataModels := fetch.Models
	if classification := providerops.Classify(providerops.ClassifyRequest{
		ProviderName:           strings.TrimSpace(req.Name),
		RequestedLoginProtocol: requestedProtocol,
		GroupingLoginProtocol:  groupingProtocol,
		Provider:               provider,
		Models:                 fetch.Models,
		Config:                 cfg,
	}); classification != nil {
		primary := classification.PrimaryModels
		if !strings.EqualFold(classification.RuntimeProtocol, "openai") && len(classification.AssumedModels) > 0 {
			primary = classification.VerifiedModels
			result.AssumedModelIDs = providerops.ModelIDs(classification.AssumedModels)
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"%d 个模型未匹配到 model card 证据，未自动合并进 supported_models；如确认可用请手动加入。",
				len(classification.AssumedModels),
			))
		}
		metadataModels = classification.PrimaryModels
		result.ModelIDs = providerops.ModelIDs(primary)
		result.LoginProtocol = classification.LoginProtocol
		result.RuntimeProtocol = classification.RuntimeProtocol
		result.Classification = &RuntimeProviderClassification{
			LoginProtocol:    classification.LoginProtocol,
			RuntimeProtocol:  classification.RuntimeProtocol,
			TotalModels:      classification.TotalModels,
			PrimaryModelIDs:  providerops.ModelIDs(classification.PrimaryModels),
			VerifiedModelIDs: providerops.ModelIDs(classification.VerifiedModels),
			AssumedModelIDs:  providerops.ModelIDs(classification.AssumedModels),
			OtherModelsCount: classification.TotalModels - len(classification.PrimaryModels),
			Groups:           classification.Groups,
		}
	} else {
		result.ModelIDs = append([]string(nil), result.AllModelIDs...)
	}
	result.Metadata = providerops.MatchMetadata(providerops.MetadataRequest{
		ProviderName:  strings.TrimSpace(req.Name),
		LoginProtocol: groupingProtocol,
		Provider:      provider,
		Models:        metadataModels,
		Config:        cfg,
	})

	// 与 micro web client 一致：探测 models 端点是否匿名可访问，避免把公开端点
	// 的成功误判为 key 有效。
	if providerops.ModelsEndpointAllowsAnonymous(runtimeProviderOpsHTTPClient(cfg, &provider), fetch.Endpoint, groupingProtocol) {
		result.AnonymousAllowed = true
		result.Warnings = append(result.Warnings, "该模型列表端点未校验 API key（匿名可访问），获取模型列表成功不代表 key 有效；key 是否有效请以聊天/补全请求为准。")
	}
	return result, nil
}

func (s *runtimeProviderOpsService) AutoImport(ctx context.Context, req RuntimeProviderAutoImportRequest) (*RuntimeProviderAutoImportResult, error) {
	fetchResult, err := s.FetchModels(ctx, req.RuntimeProviderModelsRequest)
	if err != nil {
		return nil, err
	}
	cfg := s.snapshot()
	provider := s.resolveProvider(req.RuntimeProviderModelsRequest)
	// 自动导入与 aicli login 同源：supported_models 取主组全量（含 assumed），
	// assumed 单独返回给 UI 提示，避免像「获取模型列表」那样静默丢弃。
	supported := append([]string(nil), fetchResult.ModelIDs...)
	if fetchResult.Classification != nil {
		supported = append([]string(nil), fetchResult.Classification.PrimaryModelIDs...)
	}

	result := &RuntimeProviderAutoImportResult{
		Name:            strings.TrimSpace(req.Name),
		Protocol:        fetchResult.RuntimeProtocol,
		LoginProtocol:   fetchResult.LoginProtocol,
		BaseURL:         provider.BaseURL,
		SupportedModels: supported,
		AssumedModelIDs: append([]string(nil), fetchResult.AssumedModelIDs...),
		Models:          fetchResult.Models,
		Warnings:        append([]string(nil), fetchResult.Warnings...),
	}
	result.DefaultModel = pickRuntimeProviderDefaultModel(req.DefaultModel, supported)

	base := provider
	base.ModelCapabilities = nil // 「覆盖重匹配」：旧配置不参与合并。
	if catalog, _, err := providerops.LoadModelCardCatalog(cfg); err == nil && catalog != nil {
		if template, ok := providerops.ResolveProviderTemplate(catalog, fetchResult.RuntimeProtocol, provider); ok {
			defaults := providerops.ProviderTemplateDefaults(provider, template)
			result.APIPath = defaults.APIPath
			result.ForwardURL = defaults.ForwardURL
			result.SupportTypes = defaults.SupportTypes
			result.MaxTokensLimit = defaults.MaxTokensLimit
		}
	}
	result.ModelCapabilities = providerops.BuildModelCapabilities(providerops.MetadataRequest{
		ProviderName:  strings.TrimSpace(req.Name),
		LoginProtocol: fetchResult.LoginProtocol,
		Provider:      base,
		Models:        fetchResult.Models,
		Config:        cfg,
	})
	if len(result.ModelCapabilities) == 0 {
		result.ModelCapabilities = nil
	}

	// 站点类型 / 账号是可选的既有缓存，只有已保存 provider 才有；缺失时留空，
	// 由前端决定是否保留用户草稿里的旧值。
	if cfg != nil {
		if existing, ok := cfg.Providers.Items[strings.TrimSpace(req.Name)]; ok {
			result.SiteType = existing.SiteType
			result.SiteTypeConfidence = existing.SiteTypeConfidence
			result.SiteTypeScores = existing.SiteTypeScores
			result.Account = existing.Account
		}
	}
	return result, nil
}

func (s *runtimeProviderOpsService) ProbeModels(ctx context.Context, req RuntimeProviderProbeRequest) (*RuntimeProviderProbeResult, error) {
	cfg := s.snapshot()
	provider := s.resolveProvider(req.RuntimeProviderModelsRequest)
	if strings.TrimSpace(provider.BaseURL) == "" {
		return nil, fmt.Errorf("base_url is required")
	}
	loginProtocol := providerops.ResolveFetchModelsLoginProtocol(req.Protocol, provider)
	models := make([]providerops.ModelInfo, 0, len(req.Models))
	for _, id := range req.Models {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			models = append(models, providerops.ModelInfo{ID: trimmed, DisplayName: trimmed})
		}
	}
	if len(models) == 0 {
		for _, id := range provider.SupportedModels {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				models = append(models, providerops.ModelInfo{ID: trimmed, DisplayName: trimmed})
			}
		}
	}
	results := providerops.ProbeModels(providerops.ProbeRequest{
		Config:        cfg,
		ProviderName:  strings.TrimSpace(req.Name),
		Provider:      provider,
		LoginProtocol: loginProtocol,
		Models:        models,
		Protocols:     req.Protocols,
		Timeout:       runtimeProviderOpsTimeout(req.TimeoutSeconds),
	})
	return &RuntimeProviderProbeResult{Results: results}, nil
}

func runtimeProviderOpsTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return runtimeProviderOpsDefaultTimeout
	}
	timeout := time.Duration(seconds) * time.Second
	if timeout > runtimeProviderOpsMaxTimeout {
		return runtimeProviderOpsMaxTimeout
	}
	return timeout
}

func pickRuntimeProviderDefaultModel(requested string, supported []string) string {
	if trimmed := strings.TrimSpace(requested); trimmed != "" {
		for _, id := range supported {
			if strings.EqualFold(strings.TrimSpace(id), trimmed) {
				return strings.TrimSpace(id)
			}
		}
	}
	if len(supported) > 0 {
		return strings.TrimSpace(supported[0])
	}
	return strings.TrimSpace(requested)
}

func modelCardWarningMessages(warnings []providerops.ModelCardWarning) []string {
	if len(warnings) == 0 {
		return nil
	}
	out := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		message := strings.TrimSpace(warning.Message)
		if message == "" {
			continue
		}
		if template := strings.TrimSpace(warning.ProviderTemplate); template != "" {
			message = template + ": " + message
		}
		out = append(out, message)
	}
	return out
}

func runtimeProviderOpsHTTPClient(cfg *agentconfig.Config, provider *agentconfig.Provider) *http.Client {
	if cfg != nil && provider != nil {
		if client := httpclient.GetHTTPClientWithProvider(cfg, provider); client != nil {
			return client
		}
	}
	return &http.Client{Timeout: 6 * time.Second}
}
