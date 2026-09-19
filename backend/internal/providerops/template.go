package providerops

import (
	"net/url"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelcard"
)

// ---------------------------------------------------------------------------
// provider template 解析与默认值：从 model card 目录里为 provider 找到匹配
// 的模板，并把模板默认值投影到 provider 字段。login 自动导入与 runtime
// 编辑器共用同一实现。
// ---------------------------------------------------------------------------

// TemplateDefaults 是模板默认值的契约投影（provider 编辑器直接可用的字段）。
type TemplateDefaults struct {
	APIPath        string
	ForwardURL     string
	SupportTypes   []string
	MaxTokensLimit int
}

// TemplateDefaultsDetail 在契约投影之外附带候选列表，供 login 侧“默认值
// 变更迁移”逻辑（旧默认值升级为新默认值）复用；字段保持扁平，允许调用方
// 用字段名构造字面量。
type TemplateDefaultsDetail struct {
	APIPath                  string
	ForwardURL               string
	SupportTypes             []string
	MaxTokensLimit           int
	APIPathCandidates        []string
	ForwardURLCandidates     []string
	MaxTokensLimitCandidates []int
}

// ProviderTemplateDefaults 返回模板默认值的契约投影。
func ProviderTemplateDefaults(provider config.Provider, template modelcard.ProviderTemplate) TemplateDefaults {
	detail := ProviderTemplateDefaultsDetail(provider, template)
	return TemplateDefaults{
		APIPath:        detail.APIPath,
		ForwardURL:     detail.ForwardURL,
		SupportTypes:   detail.SupportTypes,
		MaxTokensLimit: detail.MaxTokensLimit,
	}
}

// ProviderTemplateDefaultsDetail 返回含候选列表的完整模板默认值。
func ProviderTemplateDefaultsDetail(provider config.Provider, template modelcard.ProviderTemplate) TemplateDefaultsDetail {
	defaults := TemplateDefaultsDetail{
		APIPath:        strings.TrimSpace(template.APIPath),
		ForwardURL:     strings.TrimSpace(template.ForwardURL),
		SupportTypes:   append([]string(nil), template.SupportTypes...),
		MaxTokensLimit: template.MaxTokensLimit,
	}
	if strings.EqualFold(strings.TrimSpace(template.ID), "codex.responses") {
		if codexPath, ok := providerLoginCodexResponsesPathForBaseURL(provider.BaseURL); ok {
			defaults.APIPath = codexPath
			defaults.ForwardURL = codexPath
		}
	}
	defaults.APIPathCandidates = providerLoginProviderTemplatePathCandidates(template, defaults.APIPath, template.APIPath)
	defaults.ForwardURLCandidates = providerLoginProviderTemplatePathCandidates(template, defaults.ForwardURL, template.ForwardURL)
	defaults.MaxTokensLimitCandidates = providerLoginProviderTemplateMaxTokensLimitCandidates(template, defaults.MaxTokensLimit)
	return defaults
}

// providerLoginProviderTemplateDefaults 是包内既有实现的内部名。
func providerLoginProviderTemplateDefaults(provider config.Provider, template modelcard.ProviderTemplate) TemplateDefaultsDetail {
	return ProviderTemplateDefaultsDetail(provider, template)
}

func providerLoginProviderTemplateAPIPathCandidates(provider config.Provider, template modelcard.ProviderTemplate) []string {
	return providerLoginProviderTemplateDefaults(provider, template).APIPathCandidates
}

func providerLoginProviderTemplateForwardURLCandidates(provider config.Provider, template modelcard.ProviderTemplate) []string {
	return providerLoginProviderTemplateDefaults(provider, template).ForwardURLCandidates
}

func providerLoginProviderTemplatePathCandidates(template modelcard.ProviderTemplate, values ...string) []string {
	out := make([]string, 0, len(values)+3)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		normalized := normalizeProviderLoginTemplatePath(value)
		for _, existing := range out {
			if normalizeProviderLoginTemplatePath(existing) == normalized {
				return
			}
		}
		out = append(out, value)
	}
	for _, value := range values {
		add(value)
	}
	if strings.EqualFold(strings.TrimSpace(template.ID), "codex.responses") {
		add("/v1/responses")
		add("/responses")
		add("/backend-api/codex/responses")
	}
	return out
}

func providerLoginProviderTemplateMaxTokensLimitCandidates(template modelcard.ProviderTemplate, values ...int) []int {
	out := make([]int, 0, len(values)+2)
	add := func(value int) {
		if value <= 0 {
			return
		}
		for _, existing := range out {
			if existing == value {
				return
			}
		}
		out = append(out, value)
	}
	for _, value := range values {
		add(value)
	}
	// Keep historical template defaults so protocol switches still rewrite
	// providers that were provisioned before the catalog limit changed.
	switch strings.ToLower(strings.TrimSpace(template.ID)) {
	case "openai.chat", "codex.responses":
		add(10000)
		add(128000)
	case "anthropic.messages":
		add(10000)
		add(131072)
	}
	return out
}

func providerLoginCodexResponsesPathForBaseURL(baseURL string) (string, bool) {
	lower := strings.ToLower(strings.TrimSpace(baseURL))
	if !strings.Contains(lower, "chatgpt.com") {
		return "", false
	}
	if strings.Contains(lower, "/backend-api/codex") {
		return "/responses", true
	}
	return "/backend-api/codex/responses", true
}

// ResolveProviderTemplate 为 provider 选择 provider template：优先取“已配置
// 的 api_path/forward_url 与模板候选匹配”的模板（说明该 provider 就是按此
// 模板开通的），否则回退到目录里该运行时协议的默认模板。
func ResolveProviderTemplate(catalog *modelcard.Catalog, runtimeProtocol string, provider config.Provider) (modelcard.ProviderTemplate, bool) {
	if catalog == nil {
		return modelcard.ProviderTemplate{}, false
	}
	runtimeProtocol = strings.TrimSpace(runtimeProtocol)
	for _, template := range catalog.ProviderTemplateList() {
		if !strings.EqualFold(strings.TrimSpace(template.Protocol), runtimeProtocol) {
			continue
		}
		if providerLoginConfiguredProviderMatchesTemplate(provider, template) {
			return template, true
		}
	}
	return catalog.ProviderTemplateForProtocol(runtimeProtocol)
}

// resolveProviderLoginProviderTemplate 是包内既有点名的兼容包装。
func resolveProviderLoginProviderTemplate(catalog *modelcard.Catalog, runtimeProtocol string, provider config.Provider) (modelcard.ProviderTemplate, bool) {
	return ResolveProviderTemplate(catalog, runtimeProtocol, provider)
}

// ResolveProviderLoginPreviousProviderTemplate 解析 provider 更新前的模板：
// 仅在 provider 已存在时生效，优先按已配置路径匹配，再按已保存协议兜底。
func ResolveProviderLoginPreviousProviderTemplate(catalog *modelcard.Catalog, provider config.Provider, exists bool) (modelcard.ProviderTemplate, bool) {
	if !exists || catalog == nil {
		return modelcard.ProviderTemplate{}, false
	}
	if template, ok := resolveProviderLoginConfiguredProviderTemplate(catalog, "", provider); ok {
		return template, true
	}
	if protocol := provider.GetProtocol(); strings.TrimSpace(protocol) != "" {
		return catalog.ProviderTemplateForProtocol(protocol)
	}
	return modelcard.ProviderTemplate{}, false
}

// resolveProviderLoginPreviousProviderTemplate 是包内既有点名的兼容包装。
func resolveProviderLoginPreviousProviderTemplate(catalog *modelcard.Catalog, provider config.Provider, exists bool) (modelcard.ProviderTemplate, bool) {
	return ResolveProviderLoginPreviousProviderTemplate(catalog, provider, exists)
}

// resolveProviderLoginConfiguredProviderTemplate 仅按已配置路径匹配模板
// （不做协议默认兜底），runtimeProtocol 为空时在全部模板中匹配。
func resolveProviderLoginConfiguredProviderTemplate(catalog *modelcard.Catalog, runtimeProtocol string, provider config.Provider) (modelcard.ProviderTemplate, bool) {
	if catalog == nil {
		return modelcard.ProviderTemplate{}, false
	}
	templates := catalog.ProviderTemplateList()
	for _, template := range templates {
		if !providerLoginProviderTemplateProtocolMatches(template, runtimeProtocol) {
			continue
		}
		if providerLoginConfiguredPathMatchesAny(provider.APIPath, providerLoginProviderTemplateAPIPathCandidates(provider, template)) {
			return template, true
		}
	}
	for _, template := range templates {
		if !providerLoginProviderTemplateProtocolMatches(template, runtimeProtocol) {
			continue
		}
		if providerLoginConfiguredPathMatchesAny(provider.ForwardURL, providerLoginProviderTemplateForwardURLCandidates(provider, template)) {
			return template, true
		}
	}
	return modelcard.ProviderTemplate{}, false
}

func providerLoginProviderTemplateProtocolMatches(template modelcard.ProviderTemplate, runtimeProtocol string) bool {
	runtimeProtocol = strings.TrimSpace(runtimeProtocol)
	return runtimeProtocol == "" || strings.EqualFold(strings.TrimSpace(template.Protocol), runtimeProtocol)
}

func providerLoginConfiguredProviderMatchesTemplate(provider config.Provider, template modelcard.ProviderTemplate) bool {
	return providerLoginConfiguredPathMatchesAny(provider.APIPath, providerLoginProviderTemplateAPIPathCandidates(provider, template)) ||
		providerLoginConfiguredPathMatchesAny(provider.ForwardURL, providerLoginProviderTemplateForwardURLCandidates(provider, template))
}

func providerLoginConfiguredPathMatchesAny(configured string, templates []string) bool {
	configured = normalizeProviderLoginTemplatePath(configured)
	if configured == "" {
		return false
	}
	for _, template := range templates {
		if configured == normalizeProviderLoginTemplatePath(template) && configured != "" {
			return true
		}
	}
	return false
}

func normalizeProviderLoginTemplatePath(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	if parsed, err := url.Parse(value); err == nil && parsed.IsAbs() {
		value = parsed.Path
		if parsed.RawQuery != "" {
			value += "?" + parsed.RawQuery
		}
	}
	return strings.TrimRight(value, "/")
}
