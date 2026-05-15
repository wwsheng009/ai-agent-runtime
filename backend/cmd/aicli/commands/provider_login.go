package commands

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/providercompat"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelcard"
)

const (
	providerAuthModeAPIKey = "api_key"
	providerAuthModeOAuth  = "oauth"

	providerLoginProtocolAuto        = "auto"
	providerLoginProtocolOpenAIImage = "openai_image"
)

type providerLoginPrompter interface {
	PromptText(label, current string, required bool) (string, error)
	PromptSecret(label, currentMasked string, required bool) (string, error)
	PrintLine(line string)
}

type providerLoginRequest struct {
	Context       context.Context
	Config        *config.Config
	ConfigPath    string
	ProviderName  string
	LoginProtocol string
	AuthMode      string
	BaseURL       string
	APIKey        string
	ModelsPath    string
	DefaultModel  string
	SetDefault    bool
	DryRun        bool
	Yes           bool
	Interactive   bool
	Timeout       time.Duration
	Prompter      providerLoginPrompter

	AuthRef       string
	AuthStorePath string
	OAuthIssuer   string
	OAuthClientID string
	OAuthTimeout  time.Duration

	ModelCardCatalogPath string
	DisableModelCards    bool
	ModelCardsStrict     bool
}

type providerLoginResult struct {
	ProviderName            string                                    `json:"provider"`
	Protocol                string                                    `json:"protocol"`
	LoginProtocol           string                                    `json:"login_protocol"`
	AuthMode                string                                    `json:"auth_mode"`
	AuthRef                 string                                    `json:"auth_ref,omitempty"`
	APIKeyRef               string                                    `json:"api_key_ref,omitempty"`
	BaseURL                 string                                    `json:"base_url"`
	ModelsPath              string                                    `json:"models_path"`
	ModelsEndpoint          string                                    `json:"models_endpoint"`
	ModelsVerifiedAt        string                                    `json:"models_verified_at"`
	SupportedModels         []string                                  `json:"supported_models"`
	DefaultModel            string                                    `json:"default_model"`
	Models                  []providerModelInfo                       `json:"models,omitempty"`
	ProviderConfigs         []providerLoginGeneratedProviderInfo      `json:"provider_configs,omitempty"`
	ModelsSkippedByProtocol []providerLoginModelSkippedByProtocolInfo `json:"models_skipped_by_protocol,omitempty"`
	ModelCardsApplied       []providerLoginModelCardAppliedInfo       `json:"model_cards_applied,omitempty"`
	ModelCardsSkipped       []providerLoginModelCardSkippedInfo       `json:"model_cards_skipped,omitempty"`
	ModelCardWarnings       []providerLoginModelCardWarning           `json:"model_card_warnings,omitempty"`
	Created                 bool                                      `json:"created"`
	Updated                 bool                                      `json:"updated"`
	DryRun                  bool                                      `json:"dry_run"`
	ConfigPath              string                                    `json:"config_path,omitempty"`
	AuthStorePath           string                                    `json:"auth_store_path,omitempty"`
	APIKeyMasked            string                                    `json:"api_key_masked,omitempty"`
	SetDefault              bool                                      `json:"set_default"`
}

type providerLoginModelCardAppliedInfo struct {
	Model  string   `json:"model"`
	CardID string   `json:"card_id"`
	Fields []string `json:"fields,omitempty"`
}

type providerLoginGeneratedProviderInfo struct {
	ProviderName     string   `json:"provider"`
	Protocol         string   `json:"protocol"`
	LoginProtocol    string   `json:"login_protocol"`
	ProviderTemplate string   `json:"provider_template,omitempty"`
	DefaultModel     string   `json:"default_model"`
	SupportedModels  []string `json:"supported_models"`
	Created          bool     `json:"created"`
	Updated          bool     `json:"updated"`
}

type providerLoginModelCardSkippedInfo struct {
	Model  string `json:"model"`
	Reason string `json:"reason"`
}

type providerLoginModelSkippedByProtocolInfo struct {
	Model                       string `json:"model"`
	CurrentProtocol             string `json:"current_protocol,omitempty"`
	CurrentProviderTemplate     string `json:"current_provider_template,omitempty"`
	RecommendedProtocol         string `json:"recommended_protocol,omitempty"`
	RecommendedProviderTemplate string `json:"recommended_provider_template,omitempty"`
	Reason                      string `json:"reason"`
}

type providerLoginModelCardWarning struct {
	Source  string `json:"source,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func runProviderLogin(req providerLoginRequest) (*providerLoginResult, error) {
	ctx := req.Context
	if ctx == nil {
		ctx = context.Background()
	}
	cfg := req.Config
	if cfg == nil {
		cfg = &config.Config{}
	}
	if cfg.Providers.Items == nil {
		cfg.Providers.Items = make(map[string]config.Provider)
	}
	configPath := strings.TrimSpace(req.ConfigPath)
	if configPath == "" && cfg != nil {
		configPath = strings.TrimSpace(cfg.ConfigFilePath)
	}

	providerName, err := resolveLoginProviderName(req, cfg)
	if err != nil {
		return nil, err
	}
	existing, exists := cfg.Providers.Items[providerName]

	loginProtocol, authMode, err := resolveLoginProtocolAndMode(req, existing, exists)
	if err != nil {
		return nil, err
	}
	runtimeProtocol := runtimeProtocolForLoginProtocol(loginProtocol)
	candidate := existing
	candidate.Enabled = true
	candidate.Protocol = runtimeProtocol
	candidate.AuthMode = authMode

	if baseURL, err := resolveLoginBaseURL(req, existing, exists); err != nil {
		return nil, err
	} else {
		candidate.BaseURL = baseURL
	}

	resolvedAPIKey := ""
	apiKeyRef := ""
	var oauthRecord *config.ProviderAuthRecord
	if authMode == providerAuthModeOAuth {
		record, authRef, err := runCodexOAuthDeviceLogin(ctx, req, providerName)
		if err != nil {
			return nil, err
		}
		oauthRecord = record
		candidate.APIKey = record.AccessToken
		candidate.AuthMode = providerAuthModeOAuth
		candidate.AuthRef = authRef
		candidate.APIKeyRef = ""
	} else {
		apiKey, err := resolveLoginAPIKey(req, existing, exists)
		if err != nil {
			return nil, err
		}
		resolvedAPIKey = apiKey
		candidate.APIKey = apiKey
		candidate.AuthMode = providerAuthModeAPIKey
		candidate.AuthRef = ""
		apiKeyRef = resolveLoginAPIKeyRef(existing, exists, providerName)
		candidate.APIKeyRef = apiKeyRef
	}

	modelsResult, modelsPath, validationLoginProtocol, err := validateProviderLoginModels(cfg, providerName, candidate, loginProtocol, req.ModelsPath, req.Timeout)
	if err != nil {
		return nil, err
	}
	candidate.ModelsPath = modelsPath

	modelCardCatalog, modelCardWarnings, err := loadProviderLoginModelCardCatalog(req, cfg)
	if err != nil {
		return nil, err
	}
	modelsResult.Models = normalizeProviderLoginModelsForProtocol(modelsResult.Models, validationLoginProtocol)
	groupingProtocol := loginProtocol
	if isAutoLoginProtocol(groupingProtocol) {
		groupingProtocol = validationLoginProtocol
	}
	modelGroups := groupProviderLoginModelsByProviderTemplate(providerName, groupingProtocol, loginProtocol, candidate, modelsResult.Models, modelCardCatalog, authMode)
	primaryGroupIndex := selectPrimaryProviderLoginModelGroup(modelGroups, loginProtocol, candidate, modelCardCatalog)
	if primaryGroupIndex < 0 {
		return nil, fmt.Errorf("models endpoint returned no supported models")
	}
	primaryGroup := modelGroups[primaryGroupIndex]
	loginProtocol = primaryGroup.LoginProtocol
	runtimeProtocol = primaryGroup.RuntimeProtocol
	candidate.Protocol = runtimeProtocol
	modelsForLogin := primaryGroup.Models
	if primaryGroup.HasTemplate {
		previousProviderTemplate, hasPreviousProviderTemplate := resolveProviderLoginPreviousProviderTemplate(modelCardCatalog, existing, exists)
		previousDefaults := providerLoginTemplateDefaults{}
		if hasPreviousProviderTemplate {
			previousDefaults = providerLoginProviderTemplateDefaults(existing, previousProviderTemplate)
		}
		applyProviderLoginProviderTemplateDefaults(&candidate, primaryGroup.ProviderTemplate, previousDefaults, hasPreviousProviderTemplate)
	}
	if len(modelsForLogin) == 0 {
		return nil, fmt.Errorf("models endpoint returned no supported models for protocol %q", loginProtocol)
	}

	supportedModels := providerModelIDs(modelsForLogin)
	defaultModel, err := resolveLoginDefaultModel(req, candidate, supportedModels)
	if err != nil {
		return nil, err
	}
	candidate.SupportedModels = supportedModels
	candidate.DefaultModel = defaultModel
	candidate.ModelsVerifiedAt = modelsResult.VerifiedAt
	discoveredCapabilities, modelCardsApplied, modelCardsSkipped := buildProviderLoginModelCapabilitiesForLogin(providerName, loginProtocol, candidate, modelsForLogin, modelCardCatalog)
	if len(discoveredCapabilities) > 0 {
		candidate.ModelCapabilities = discoveredCapabilities
	}
	additionalProviders, additionalModelCardsApplied, additionalModelCardsSkipped, err := buildProviderLoginAdditionalGroupedProviders(
		req,
		cfg,
		providerName,
		candidate,
		modelGroups,
		primaryGroupIndex,
		modelCardCatalog,
		modelsResult.VerifiedAt,
		authMode,
	)
	if err != nil {
		return nil, err
	}
	modelCardsApplied = append(modelCardsApplied, additionalModelCardsApplied...)
	modelCardsSkipped = append(modelCardsSkipped, additionalModelCardsSkipped...)
	providerConfigs := providerLoginGeneratedProviderInfos(providerName, candidate, primaryGroup, exists, additionalProviders)

	if !req.DryRun {
		resolvedConfigPath, err := ensureWritableAICLIConfigPath(cfg, configPath)
		if err != nil {
			return nil, fmt.Errorf("无法保存配置: %w", err)
		}
		configPath = resolvedConfigPath
		if authMode == providerAuthModeAPIKey {
			if strings.TrimSpace(resolvedAPIKey) == "" {
				return nil, fmt.Errorf("api key is missing for auth store write")
			}
			if strings.TrimSpace(apiKeyRef) == "" {
				apiKeyRef = resolveLoginAPIKeyRef(existing, exists, providerName)
			}
			record := config.ProviderAuthRecord{
				KeyType:  config.AuthKeyTypeAPIKey,
				APIKey:   resolvedAPIKey,
				AuthMode: providerAuthModeAPIKey,
			}
			authStorePath := authStorePathForResult(req, authMode)
			if err := config.SaveProviderAuthToPath(authStorePath, apiKeyRef, record); err != nil {
				return nil, err
			}
			candidate.APIKey = ""
			if strings.TrimSpace(candidate.APIKeyRef) == "" {
				candidate.APIKeyRef = apiKeyRef
			}
			for i := range additionalProviders {
				additionalProviders[i].Provider.APIKey = ""
				additionalProviders[i].Provider.APIKeyRef = candidate.APIKeyRef
			}
		}
		if authMode == providerAuthModeOAuth {
			if oauthRecord == nil {
				return nil, fmt.Errorf("oauth auth record is missing")
			}
			authStorePath := authStorePathForResult(req, authMode)
			if err := config.SaveProviderAuthToPath(authStorePath, candidate.AuthRef, *oauthRecord); err != nil {
				return nil, err
			}
		}
		update := buildProviderPersistenceUpdate(providerName, candidate, loginProtocol, authMode, supportedModels, req.SetDefault, modelsResult.VerifiedAt)
		persisted, err := config.UpdateProviderConfig(configPath, update)
		if err != nil {
			return nil, err
		}
		if persisted != nil {
			candidate = *persisted
		}
		applyProviderLoginConfigUpdate(cfg, providerName, candidate, req.SetDefault)
		for i := range additionalProviders {
			grouped := additionalProviders[i]
			update := buildProviderPersistenceUpdate(grouped.Name, grouped.Provider, grouped.LoginProtocol, grouped.Provider.AuthMode, grouped.SupportedModels, false, modelsResult.VerifiedAt)
			persisted, err := config.UpdateProviderConfig(configPath, update)
			if err != nil {
				return nil, err
			}
			if persisted != nil {
				grouped.Provider = *persisted
			}
			additionalProviders[i] = grouped
			applyProviderLoginConfigUpdate(cfg, grouped.Name, grouped.Provider, false)
		}
		providerConfigs = providerLoginGeneratedProviderInfos(providerName, candidate, primaryGroup, exists, additionalProviders)
	}

	return &providerLoginResult{
		ProviderName:            providerName,
		Protocol:                runtimeProtocol,
		LoginProtocol:           loginProtocol,
		AuthMode:                authMode,
		AuthRef:                 candidate.AuthRef,
		APIKeyRef:               candidate.APIKeyRef,
		BaseURL:                 candidate.BaseURL,
		ModelsPath:              modelsPath,
		ModelsEndpoint:          modelsResult.Endpoint,
		ModelsVerifiedAt:        modelsResult.VerifiedAt,
		SupportedModels:         supportedModels,
		DefaultModel:            defaultModel,
		Models:                  modelsForLogin,
		ProviderConfigs:         providerConfigs,
		ModelsSkippedByProtocol: nil,
		ModelCardsApplied:       modelCardsApplied,
		ModelCardsSkipped:       modelCardsSkipped,
		ModelCardWarnings:       modelCardWarnings,
		Created:                 !exists,
		Updated:                 exists,
		DryRun:                  req.DryRun,
		ConfigPath:              configPath,
		AuthStorePath:           authStorePathForResult(req, authMode),
		APIKeyMasked:            maskSecretForDisplay(resolvedAPIKey),
		SetDefault:              req.SetDefault,
	}, nil
}

func resolveLoginProviderName(req providerLoginRequest, cfg *config.Config) (string, error) {
	name := strings.TrimSpace(req.ProviderName)
	if name != "" {
		return name, nil
	}
	if !req.Interactive || req.Prompter == nil {
		return "", fmt.Errorf("provider is required")
	}
	current := ""
	if cfg != nil {
		current = strings.TrimSpace(cfg.Providers.DefaultProvider)
	}
	options := loginProviderSelectionOptions(cfg, current)
	if len(options) > 0 {
		req.Prompter.PrintLine("现有 providers:")
		for i, option := range options {
			req.Prompter.PrintLine(fmt.Sprintf("  [%d] %s", i+1, option))
		}
		req.Prompter.PrintLine("提示: 输入编号选择现有 provider，或直接输入新 provider 名称")
	}
	for {
		value, err := req.Prompter.PromptText("Provider 名称（新建或现有）", current, true)
		if err != nil {
			return "", err
		}
		value = strings.TrimSpace(value)
		if selected, ok := resolveLoginProviderSelection(value, current, options); ok {
			return selected, nil
		}
		if value != "" {
			return value, nil
		}
	}
}

func loginProviderSelectionOptions(cfg *config.Config, current string) []string {
	seen := make(map[string]struct{})
	options := make([]string, 0)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		options = append(options, value)
	}
	add(current)
	if cfg != nil {
		add(cfg.Providers.DefaultProvider)
		for name := range cfg.Providers.Items {
			add(name)
		}
	}
	return options
}

func resolveLoginProviderSelection(input, current string, options []string) (string, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		if strings.TrimSpace(current) != "" {
			return strings.TrimSpace(current), true
		}
		return "", false
	}
	for i, option := range options {
		if input == fmt.Sprintf("%d", i+1) || strings.EqualFold(input, option) {
			return option, true
		}
	}
	return "", false
}

func resolveLoginProtocolAndMode(req providerLoginRequest, existing config.Provider, exists bool) (string, string, error) {
	mode := strings.TrimSpace(req.AuthMode)
	protocol := strings.TrimSpace(req.LoginProtocol)
	if protocol == "" && exists {
		protocol = loginProtocolFromProvider(existing, mode)
	}
	if protocol == "" {
		if !req.Interactive || req.Prompter == nil {
			return "", "", fmt.Errorf("protocol is required for a new provider")
		}
		selected, err := promptLoginProtocol(req.Prompter)
		if err != nil {
			return "", "", err
		}
		protocol = selected
	}
	protocol = normalizeLoginProtocol(protocol, mode)
	if !isSupportedLoginProtocol(protocol) {
		return "", "", fmt.Errorf("unsupported login protocol %q", protocol)
	}
	if mode == "" {
		if protocol == "codex-oauth" {
			mode = providerAuthModeOAuth
		} else {
			mode = providerAuthModeAPIKey
		}
	}
	mode = normalizeProviderAuthMode(mode)
	if mode == providerAuthModeOAuth && protocol != "codex-oauth" {
		return "", "", fmt.Errorf("oauth mode is currently supported only by codex-oauth")
	}
	return protocol, mode, nil
}

func resolveLoginBaseURL(req providerLoginRequest, existing config.Provider, exists bool) (string, error) {
	baseURL := strings.TrimSpace(req.BaseURL)
	if baseURL == "" && exists {
		baseURL = strings.TrimSpace(existing.BaseURL)
	}
	if strings.TrimSpace(req.BaseURL) == "" && req.Interactive && req.Prompter != nil {
		value, err := req.Prompter.PromptText("Base URL", baseURL, true)
		if err != nil {
			return "", err
		}
		baseURL = value
	}
	if baseURL == "" {
		return "", fmt.Errorf("base-url is required")
	}
	return baseURL, nil
}

func resolveLoginAPIKey(req providerLoginRequest, existing config.Provider, exists bool) (string, error) {
	if strings.TrimSpace(req.APIKey) != "" {
		return strings.TrimSpace(req.APIKey), nil
	}
	current := ""
	if exists {
		current = strings.TrimSpace(existing.APIKey)
		if current == "" && strings.TrimSpace(existing.APIKeyRef) != "" {
			if secret, err := config.LoadProviderAuthSecret(strings.TrimSpace(existing.APIKeyRef), config.AuthKeyTypeAPIKey); err == nil && strings.TrimSpace(secret) != "" {
				current = strings.TrimSpace(secret)
			}
		}
	}
	if req.Interactive && req.Prompter != nil {
		required := current == ""
		value, err := req.Prompter.PromptSecret("API key", maskSecretForDisplay(current), required)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), nil
		}
	}
	if current == "" {
		return "", fmt.Errorf("api-key is required")
	}
	return current, nil
}

func resolveLoginDefaultModel(req providerLoginRequest, provider config.Provider, supported []string) (string, error) {
	requested := strings.TrimSpace(req.DefaultModel)
	if requested != "" {
		if !stringInListFold(requested, supported) {
			return "", fmt.Errorf("default model %q is not in returned models", requested)
		}
		return canonicalStringFromList(requested, supported), nil
	}
	if strings.TrimSpace(provider.DefaultModel) != "" && stringInListFold(provider.DefaultModel, supported) {
		return canonicalStringFromList(provider.DefaultModel, supported), nil
	}
	if len(supported) == 0 {
		return "", fmt.Errorf("models endpoint returned no supported models")
	}
	return supported[0], nil
}

func loadProviderLoginModelCardCatalog(req providerLoginRequest, cfg *config.Config) (*modelcard.Catalog, []providerLoginModelCardWarning, error) {
	if req.DisableModelCards {
		return nil, nil, nil
	}
	modelCardsConfig := (*config.AICLIModelCardsConfig)(nil)
	if cfg != nil && cfg.AICLI != nil {
		modelCardsConfig = cfg.AICLI.ModelCards
	}
	if modelCardsConfig != nil && modelCardsConfig.Enabled != nil && !*modelCardsConfig.Enabled && strings.TrimSpace(req.ModelCardCatalogPath) == "" {
		return nil, nil, nil
	}

	strict := req.ModelCardsStrict
	if modelCardsConfig != nil && modelCardsConfig.Strict {
		strict = true
	}
	sources := []modelcard.Source{modelcard.BuiltinSource()}
	if modelCardsConfig != nil && strings.TrimSpace(modelCardsConfig.BuiltinPath) != "" {
		sources = append(sources, readProviderLoginModelCardFile(modelCardsConfig.BuiltinPath))
	}
	userPath := "~/.aicli/model_cards.yaml"
	if modelCardsConfig != nil && strings.TrimSpace(modelCardsConfig.UserPath) != "" {
		userPath = modelCardsConfig.UserPath
	}
	if source, ok := readExistingProviderLoginModelCardFile(userPath); ok {
		sources = append(sources, source)
	}
	if strings.TrimSpace(req.ModelCardCatalogPath) != "" {
		sources = append(sources, readProviderLoginModelCardFile(req.ModelCardCatalogPath))
	}

	catalog, warnings, err := modelcard.LoadSources(sources, strict)
	if err != nil {
		return nil, providerLoginModelCardWarnings(warnings), err
	}
	return catalog, providerLoginModelCardWarnings(warnings), nil
}

func readProviderLoginModelCardFile(path string) modelcard.Source {
	resolved := resolveProviderLoginModelCardPath(path)
	data, err := os.ReadFile(resolved)
	if err != nil {
		return modelcard.Source{Name: resolved, Err: err}
	}
	return modelcard.Source{Name: resolved, Data: data}
}

func readExistingProviderLoginModelCardFile(path string) (modelcard.Source, bool) {
	resolved := resolveProviderLoginModelCardPath(path)
	if info, err := os.Stat(resolved); err != nil || info.IsDir() {
		return modelcard.Source{}, false
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return modelcard.Source{Name: resolved, Err: err}, true
	}
	return modelcard.Source{Name: resolved, Data: data}, true
}

func resolveProviderLoginModelCardPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			return filepath.Join(home, strings.TrimLeft(path[2:], "/\\"))
		}
	}
	return filepath.Clean(path)
}

func providerLoginModelCardWarnings(input []modelcard.Warning) []providerLoginModelCardWarning {
	if len(input) == 0 {
		return nil
	}
	output := make([]providerLoginModelCardWarning, 0, len(input))
	for _, warning := range input {
		output = append(output, providerLoginModelCardWarning{
			Source:  warning.Source,
			Code:    warning.Code,
			Message: warning.Message,
		})
	}
	return output
}

func validateProviderLoginModels(
	cfg *config.Config,
	providerName string,
	provider config.Provider,
	loginProtocol string,
	modelsPath string,
	timeout time.Duration,
) (*providerModelsValidationResult, string, string, error) {
	if !isAutoLoginProtocol(loginProtocol) {
		resolvedPath := resolveProviderModelsPath(loginProtocol, provider, modelsPath)
		result, err := validateProviderModels(providerModelsValidationRequest{
			Config:        cfg,
			ProviderName:  providerName,
			Provider:      provider,
			LoginProtocol: loginProtocol,
			ModelsPath:    resolvedPath,
			Timeout:       timeout,
		})
		return result, resolvedPath, loginProtocol, err
	}

	attempts := providerLoginAutoValidationProtocols(provider)
	errors := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		candidate := provider
		candidate.Protocol = runtimeProtocolForLoginProtocol(attempt)
		resolvedPath := resolveProviderModelsPath(attempt, candidate, modelsPath)
		result, err := validateProviderModels(providerModelsValidationRequest{
			Config:        cfg,
			ProviderName:  providerName,
			Provider:      candidate,
			LoginProtocol: attempt,
			ModelsPath:    resolvedPath,
			Timeout:       timeout,
		})
		if err == nil {
			return result, resolvedPath, attempt, nil
		}
		errors = append(errors, fmt.Sprintf("%s: %v", attempt, err))
	}
	return nil, "", "", fmt.Errorf("auto protocol models validation failed; attempts: %s", strings.Join(errors, " | "))
}

func providerLoginAutoValidationProtocols(provider config.Provider) []string {
	if strings.EqualFold(provider.AuthMode, providerAuthModeOAuth) {
		return []string{"codex-oauth"}
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace(provider.BaseURL)), "chatgpt.com") {
		return []string{"codex-apikey", "openai", "anthropic", "gemini"}
	}
	return []string{"openai", "anthropic", "gemini", "codex-apikey"}
}

func isAutoLoginProtocol(protocol string) bool {
	return strings.EqualFold(strings.TrimSpace(protocol), providerLoginProtocolAuto)
}

func normalizeProviderLoginModelsForProtocol(models []providerModelInfo, loginProtocol string) []providerModelInfo {
	if len(models) == 0 {
		return nil
	}
	out := make([]providerModelInfo, 0, len(models))
	for _, model := range models {
		model.ID = normalizeProviderModelID(model.ID, loginProtocol)
		if strings.TrimSpace(model.DisplayName) == "" {
			model.DisplayName = model.ID
		}
		if strings.TrimSpace(model.ID) != "" {
			out = append(out, model)
		}
	}
	return dedupeProviderModels(out)
}

type providerLoginModelGroup struct {
	Key              string
	LoginProtocol    string
	RuntimeProtocol  string
	ProviderTemplate modelcard.ProviderTemplate
	HasTemplate      bool
	Models           []providerModelInfo
}

type providerLoginGroupedProvider struct {
	Name             string
	LoginProtocol    string
	RuntimeProtocol  string
	ProviderTemplate string
	Provider         config.Provider
	SupportedModels  []string
	DefaultModel     string
	Created          bool
	Updated          bool
}

func groupProviderLoginModelsByProviderTemplate(
	providerName, loginProtocol, requestedLoginProtocol string,
	provider config.Provider,
	models []providerModelInfo,
	catalog *modelcard.Catalog,
	authMode string,
) []providerLoginModelGroup {
	if len(models) == 0 {
		return nil
	}
	runtimeProtocol := runtimeProtocolForLoginProtocol(loginProtocol)
	currentTemplate, hasCurrentTemplate := resolveProviderLoginProviderTemplate(catalog, runtimeProtocol, provider)
	ctx := modelcard.Context{
		ProviderName:     providerName,
		LoginProtocol:    loginProtocol,
		RuntimeProtocol:  runtimeProtocol,
		BaseURL:          provider.BaseURL,
		ProviderTemplate: strings.TrimSpace(currentTemplate.ID),
	}
	groups := make([]providerLoginModelGroup, 0)
	indexByKey := make(map[string]int)
	for _, model := range models {
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		template, applied, hasTemplate := catalog.RecommendedProviderTemplate(ctx, modelID)
		if imageTemplate, ok := providerLoginAutoImageModelTemplate(catalog, requestedLoginProtocol, modelID, applied, hasTemplate); ok {
			template = imageTemplate
			hasTemplate = true
		}
		if !hasTemplate && hasCurrentTemplate {
			template = currentTemplate
			hasTemplate = true
		}
		groupLoginProtocol := loginProtocolForProviderTemplate(template, hasTemplate, loginProtocol, authMode)
		groupRuntimeProtocol := runtimeProtocolForLoginProtocol(groupLoginProtocol)
		groupKey := providerLoginModelGroupKey(groupRuntimeProtocol, template, hasTemplate)
		groupModel := model
		groupModel.ID = normalizeProviderModelID(groupModel.ID, groupLoginProtocol)
		if strings.TrimSpace(groupModel.ID) == "" {
			continue
		}
		index, ok := indexByKey[groupKey]
		if !ok {
			index = len(groups)
			indexByKey[groupKey] = index
			groups = append(groups, providerLoginModelGroup{
				Key:              groupKey,
				LoginProtocol:    groupLoginProtocol,
				RuntimeProtocol:  groupRuntimeProtocol,
				ProviderTemplate: template,
				HasTemplate:      hasTemplate,
			})
		}
		groups[index].Models = append(groups[index].Models, groupModel)
	}
	for i := range groups {
		groups[i].Models = dedupeProviderModels(groups[i].Models)
	}
	return groups
}

func providerLoginAutoImageModelTemplate(catalog *modelcard.Catalog, requestedLoginProtocol, modelID string, applied []modelcard.AppliedCard, hasTemplate bool) (modelcard.ProviderTemplate, bool) {
	if !isAutoLoginProtocol(requestedLoginProtocol) || !providerLoginModelIDContainsImageKeyword(modelID) {
		return modelcard.ProviderTemplate{}, false
	}
	if hasTemplate && !providerLoginRecommendationIsFallback(applied) {
		return modelcard.ProviderTemplate{}, false
	}
	return catalog.ProviderTemplateForProtocol(providerLoginProtocolOpenAIImage)
}

func providerLoginModelIDContainsImageKeyword(modelID string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(modelID)), "image")
}

func providerLoginRecommendationIsFallback(applied []modelcard.AppliedCard) bool {
	if len(applied) == 0 {
		return false
	}
	for _, item := range applied {
		if !item.Fallback {
			return false
		}
	}
	return true
}

func loginProtocolForProviderTemplate(template modelcard.ProviderTemplate, hasTemplate bool, fallbackLoginProtocol, authMode string) string {
	if hasTemplate {
		protocol := strings.ToLower(strings.TrimSpace(template.Protocol))
		if protocol == "codex" {
			if normalizeProviderAuthMode(authMode) == providerAuthModeOAuth && strings.EqualFold(fallbackLoginProtocol, "codex-oauth") {
				return "codex-oauth"
			}
			return "codex-apikey"
		}
		if protocol != "" {
			return protocol
		}
	}
	if isAutoLoginProtocol(fallbackLoginProtocol) {
		return "openai"
	}
	return normalizeLoginProtocol(fallbackLoginProtocol, authMode)
}

func providerLoginModelGroupKey(runtimeProtocol string, template modelcard.ProviderTemplate, hasTemplate bool) string {
	if hasTemplate && strings.TrimSpace(template.ID) != "" {
		return strings.ToLower(strings.TrimSpace(template.ID))
	}
	runtimeProtocol = strings.ToLower(strings.TrimSpace(runtimeProtocol))
	if runtimeProtocol == "" {
		return "openai"
	}
	return runtimeProtocol
}

func selectPrimaryProviderLoginModelGroup(groups []providerLoginModelGroup, requestedLoginProtocol string, provider config.Provider, catalog *modelcard.Catalog) int {
	if len(groups) == 0 {
		return -1
	}
	if !isAutoLoginProtocol(requestedLoginProtocol) {
		runtimeProtocol := runtimeProtocolForLoginProtocol(requestedLoginProtocol)
		if template, ok := resolveProviderLoginProviderTemplate(catalog, runtimeProtocol, provider); ok {
			for i, group := range groups {
				if group.HasTemplate && sameProviderLoginTemplateID(group.ProviderTemplate, template) {
					return i
				}
			}
		}
		for i, group := range groups {
			if strings.EqualFold(group.RuntimeProtocol, runtimeProtocol) {
				return i
			}
		}
	}
	best := 0
	for i := 1; i < len(groups); i++ {
		if len(groups[i].Models) > len(groups[best].Models) {
			best = i
			continue
		}
		if len(groups[i].Models) == len(groups[best].Models) && providerLoginProtocolRank(groups[i].RuntimeProtocol) < providerLoginProtocolRank(groups[best].RuntimeProtocol) {
			best = i
		}
	}
	return best
}

func providerLoginProtocolRank(protocol string) int {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "openai":
		return 0
	case providerLoginProtocolOpenAIImage:
		return 1
	case "codex":
		return 2
	case "anthropic":
		return 3
	case "gemini":
		return 4
	default:
		return 10
	}
}

func buildProviderLoginAdditionalGroupedProviders(
	req providerLoginRequest,
	cfg *config.Config,
	baseProviderName string,
	baseProvider config.Provider,
	groups []providerLoginModelGroup,
	primaryGroupIndex int,
	catalog *modelcard.Catalog,
	verifiedAt string,
	authMode string,
) ([]providerLoginGroupedProvider, []providerLoginModelCardAppliedInfo, []providerLoginModelCardSkippedInfo, error) {
	if normalizeProviderAuthMode(authMode) == providerAuthModeOAuth || len(groups) <= 1 {
		return nil, nil, nil, nil
	}
	additional := make([]providerLoginGroupedProvider, 0, len(groups)-1)
	applied := make([]providerLoginModelCardAppliedInfo, 0)
	skipped := make([]providerLoginModelCardSkippedInfo, 0)
	usedNames := map[string]struct{}{strings.ToLower(strings.TrimSpace(baseProviderName)): {}}
	for i, group := range groups {
		if i == primaryGroupIndex {
			continue
		}
		name := providerLoginGroupedProviderName(baseProviderName, group, usedNames)
		existing, exists := config.Provider{}, false
		if cfg != nil && cfg.Providers.Items != nil {
			existing, exists = cfg.Providers.Items[name]
		}
		provider := buildProviderLoginProviderForGroup(baseProvider, existing, exists, group, catalog)
		provider.ModelsVerifiedAt = verifiedAt
		supportedModels := providerModelIDs(group.Models)
		defaultModel := resolveProviderLoginGroupedDefaultModel(req.DefaultModel, provider, supportedModels)
		provider.SupportedModels = supportedModels
		provider.DefaultModel = defaultModel
		capabilities, groupApplied, groupSkipped := buildProviderLoginModelCapabilitiesForLogin(name, group.LoginProtocol, provider, group.Models, catalog)
		if len(capabilities) > 0 {
			provider.ModelCapabilities = capabilities
		}
		applied = append(applied, groupApplied...)
		skipped = append(skipped, groupSkipped...)
		additional = append(additional, providerLoginGroupedProvider{
			Name:             name,
			LoginProtocol:    group.LoginProtocol,
			RuntimeProtocol:  group.RuntimeProtocol,
			ProviderTemplate: providerLoginGroupTemplateID(group),
			Provider:         provider,
			SupportedModels:  supportedModels,
			DefaultModel:     defaultModel,
			Created:          !exists,
			Updated:          exists,
		})
	}
	return additional, applied, skipped, nil
}

func buildProviderLoginProviderForGroup(baseProvider, existing config.Provider, exists bool, group providerLoginModelGroup, catalog *modelcard.Catalog) config.Provider {
	provider := existing
	if !exists {
		provider = config.Provider{}
	}
	provider.Enabled = true
	provider.Protocol = group.RuntimeProtocol
	provider.BaseURL = baseProvider.BaseURL
	provider.APIKey = baseProvider.APIKey
	provider.APIKeyRef = baseProvider.APIKeyRef
	provider.AuthMode = providerAuthModeAPIKey
	provider.AuthRef = ""
	provider.ModelsPath = baseProvider.ModelsPath
	if group.HasTemplate {
		previousTemplate, hasPreviousTemplate := resolveProviderLoginPreviousProviderTemplate(catalog, existing, exists)
		previousDefaults := providerLoginTemplateDefaults{}
		if hasPreviousTemplate {
			previousDefaults = providerLoginProviderTemplateDefaults(existing, previousTemplate)
		}
		applyProviderLoginProviderTemplateDefaults(&provider, group.ProviderTemplate, previousDefaults, hasPreviousTemplate)
	}
	return provider
}

func resolveProviderLoginGroupedDefaultModel(requested string, provider config.Provider, supported []string) string {
	requested = strings.TrimSpace(requested)
	if requested != "" && stringInListFold(requested, supported) {
		return canonicalStringFromList(requested, supported)
	}
	if strings.TrimSpace(provider.DefaultModel) != "" && stringInListFold(provider.DefaultModel, supported) {
		return canonicalStringFromList(provider.DefaultModel, supported)
	}
	if len(supported) == 0 {
		return ""
	}
	return supported[0]
}

func providerLoginGroupedProviderName(baseProviderName string, group providerLoginModelGroup, used map[string]struct{}) string {
	baseProviderName = strings.TrimSpace(baseProviderName)
	if baseProviderName == "" {
		baseProviderName = "provider"
	}
	suffix := strings.ToLower(strings.TrimSpace(group.RuntimeProtocol))
	if suffix == "" {
		suffix = "openai"
	}
	candidate := providerLoginSanitizeProviderName(baseProviderName + "_" + suffix)
	if _, ok := used[strings.ToLower(candidate)]; !ok {
		used[strings.ToLower(candidate)] = struct{}{}
		return candidate
	}
	if group.HasTemplate {
		templateSuffix := providerLoginProviderTemplateNameSuffix(group.ProviderTemplate)
		if templateSuffix != "" && templateSuffix != suffix {
			candidate = providerLoginSanitizeProviderName(baseProviderName + "_" + templateSuffix)
			if _, ok := used[strings.ToLower(candidate)]; !ok {
				used[strings.ToLower(candidate)] = struct{}{}
				return candidate
			}
		}
	}
	for n := 2; ; n++ {
		next := fmt.Sprintf("%s_%d", candidate, n)
		if _, ok := used[strings.ToLower(next)]; !ok {
			used[strings.ToLower(next)] = struct{}{}
			return next
		}
	}
}

func providerLoginProviderTemplateNameSuffix(template modelcard.ProviderTemplate) string {
	id := strings.ToLower(strings.TrimSpace(template.ID))
	if id == "" {
		return ""
	}
	parts := strings.Split(id, ".")
	if len(parts) == 1 {
		return providerLoginSanitizeProviderName(parts[0])
	}
	if parts[0] == strings.ToLower(strings.TrimSpace(template.Protocol)) && len(parts) > 1 {
		return providerLoginSanitizeProviderName(strings.Join(parts, "_"))
	}
	return providerLoginSanitizeProviderName(strings.Join(parts, "_"))
}

func providerLoginSanitizeProviderName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "provider"
	}
	return out
}

func providerLoginGroupTemplateID(group providerLoginModelGroup) string {
	if !group.HasTemplate {
		return ""
	}
	return strings.TrimSpace(group.ProviderTemplate.ID)
}

func providerLoginGeneratedProviderInfos(
	primaryName string,
	primaryProvider config.Provider,
	primaryGroup providerLoginModelGroup,
	primaryExists bool,
	additional []providerLoginGroupedProvider,
) []providerLoginGeneratedProviderInfo {
	if len(additional) == 0 {
		return nil
	}
	infos := make([]providerLoginGeneratedProviderInfo, 0, len(additional)+1)
	infos = append(infos, providerLoginGeneratedProviderInfo{
		ProviderName:     primaryName,
		Protocol:         primaryProvider.Protocol,
		LoginProtocol:    primaryGroup.LoginProtocol,
		ProviderTemplate: providerLoginGroupTemplateID(primaryGroup),
		DefaultModel:     primaryProvider.DefaultModel,
		SupportedModels:  append([]string(nil), primaryProvider.SupportedModels...),
		Created:          !primaryExists,
		Updated:          primaryExists,
	})
	for _, item := range additional {
		infos = append(infos, providerLoginGeneratedProviderInfo{
			ProviderName:     item.Name,
			Protocol:         item.RuntimeProtocol,
			LoginProtocol:    item.LoginProtocol,
			ProviderTemplate: item.ProviderTemplate,
			DefaultModel:     item.DefaultModel,
			SupportedModels:  append([]string(nil), item.SupportedModels...),
			Created:          item.Created,
			Updated:          item.Updated,
		})
	}
	return infos
}

func filterProviderLoginModelsByProviderTemplate(
	providerName, loginProtocol string,
	provider config.Provider,
	models []providerModelInfo,
	catalog *modelcard.Catalog,
) ([]providerModelInfo, modelcard.ProviderTemplate, bool, []providerLoginModelSkippedByProtocolInfo) {
	if catalog == nil || len(models) == 0 {
		return models, modelcard.ProviderTemplate{}, false, nil
	}
	runtimeProtocol := runtimeProtocolForLoginProtocol(loginProtocol)
	currentTemplate, ok := resolveProviderLoginProviderTemplate(catalog, runtimeProtocol, provider)
	if !ok {
		return models, modelcard.ProviderTemplate{}, false, nil
	}
	ctx := modelcard.Context{
		ProviderName:     providerName,
		LoginProtocol:    loginProtocol,
		RuntimeProtocol:  runtimeProtocol,
		ProviderTemplate: strings.TrimSpace(currentTemplate.ID),
		BaseURL:          provider.BaseURL,
	}
	filtered := make([]providerModelInfo, 0, len(models))
	skipped := make([]providerLoginModelSkippedByProtocolInfo, 0)
	for _, model := range models {
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		recommended, _, ok := catalog.RecommendedProviderTemplate(ctx, modelID)
		if !ok || sameProviderLoginTemplateID(currentTemplate, recommended) {
			filtered = append(filtered, model)
			continue
		}
		skipped = append(skipped, providerLoginModelSkippedByProtocolInfo{
			Model:                       modelID,
			CurrentProtocol:             strings.TrimSpace(currentTemplate.Protocol),
			CurrentProviderTemplate:     strings.TrimSpace(currentTemplate.ID),
			RecommendedProtocol:         strings.TrimSpace(recommended.Protocol),
			RecommendedProviderTemplate: strings.TrimSpace(recommended.ID),
			Reason:                      "recommended_provider_template_mismatch",
		})
	}
	return filtered, currentTemplate, true, skipped
}

func resolveProviderLoginProviderTemplate(catalog *modelcard.Catalog, runtimeProtocol string, provider config.Provider) (modelcard.ProviderTemplate, bool) {
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

func resolveProviderLoginPreviousProviderTemplate(catalog *modelcard.Catalog, provider config.Provider, exists bool) (modelcard.ProviderTemplate, bool) {
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

func sameProviderLoginTemplateID(left, right modelcard.ProviderTemplate) bool {
	return strings.EqualFold(strings.TrimSpace(left.ID), strings.TrimSpace(right.ID))
}

type providerLoginTemplateDefaults struct {
	APIPath              string
	APIPathCandidates    []string
	ForwardURL           string
	ForwardURLCandidates []string
	SupportTypes         []string
	MaxTokensLimit       int
}

func providerLoginProviderTemplateDefaults(provider config.Provider, template modelcard.ProviderTemplate) providerLoginTemplateDefaults {
	defaults := providerLoginTemplateDefaults{
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
	return defaults
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

func applyProviderLoginProviderTemplateDefaults(provider *config.Provider, template modelcard.ProviderTemplate, previous providerLoginTemplateDefaults, hasPrevious bool) {
	if provider == nil || strings.TrimSpace(template.ID) == "" {
		return
	}
	current := providerLoginProviderTemplateDefaults(*provider, template)
	provider.APIPath = providerLoginMergedTemplatePath(provider.APIPath, current.APIPath, previous.APIPathCandidates, hasPrevious)
	provider.ForwardURL = providerLoginMergedTemplatePath(provider.ForwardURL, current.ForwardURL, previous.ForwardURLCandidates, hasPrevious)
	provider.SupportTypes = providerLoginMergedTemplateSupportTypes(provider.SupportTypes, current.SupportTypes, previous.SupportTypes, hasPrevious)
	provider.MaxTokensLimit = providerLoginMergedTemplateMaxTokensLimit(provider.MaxTokensLimit, current.MaxTokensLimit, previous.MaxTokensLimit, hasPrevious)
}

func providerLoginMergedTemplatePath(configured, currentDefault string, previousDefaults []string, hasPrevious bool) string {
	if strings.TrimSpace(configured) == "" {
		return strings.TrimSpace(currentDefault)
	}
	if hasPrevious && providerLoginConfiguredPathMatchesAny(configured, previousDefaults) {
		return strings.TrimSpace(currentDefault)
	}
	return configured
}

func providerLoginMergedTemplateSupportTypes(configured, currentDefault, previousDefault []string, hasPrevious bool) []string {
	if len(configured) == 0 {
		return append([]string(nil), currentDefault...)
	}
	if hasPrevious && providerLoginStringSlicesEqualFold(configured, previousDefault) {
		return append([]string(nil), currentDefault...)
	}
	return configured
}

func providerLoginMergedTemplateMaxTokensLimit(configured, currentDefault, previousDefault int, hasPrevious bool) int {
	if configured <= 0 {
		return currentDefault
	}
	if hasPrevious && previousDefault > 0 && configured == previousDefault {
		return currentDefault
	}
	return configured
}

func providerLoginStringSlicesEqualFold(left, right []string) bool {
	left = providerLoginNormalizedStringSlice(left)
	right = providerLoginNormalizedStringSlice(right)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func providerLoginNormalizedStringSlice(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		out = append(out, value)
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

func providerLoginAllModelsFilteredError(skipped []providerLoginModelSkippedByProtocolInfo) error {
	if len(skipped) == 0 {
		return fmt.Errorf("models endpoint returned no supported models after provider-template filtering")
	}
	return fmt.Errorf("models endpoint returned only models for other provider templates; first skipped models: %s", providerLoginSkippedByProtocolSummary(skipped, 5))
}

func providerLoginSkippedByProtocolSummary(skipped []providerLoginModelSkippedByProtocolInfo, limit int) string {
	if len(skipped) == 0 {
		return ""
	}
	if limit <= 0 || limit > len(skipped) {
		limit = len(skipped)
	}
	parts := make([]string, 0, limit+1)
	for _, item := range skipped[:limit] {
		model := strings.TrimSpace(item.Model)
		recommended := strings.TrimSpace(item.RecommendedProviderTemplate)
		if recommended == "" {
			recommended = strings.TrimSpace(item.RecommendedProtocol)
		}
		if recommended == "" {
			parts = append(parts, model)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s -> %s", model, recommended))
	}
	if len(skipped) > limit {
		parts = append(parts, fmt.Sprintf("... (%d more)", len(skipped)-limit))
	}
	return strings.Join(parts, ", ")
}

func buildProviderPersistenceUpdate(providerName string, candidate config.Provider, loginProtocol, authMode string, supportedModels []string, setDefault bool, verifiedAt string) config.ProviderConfigUpdate {
	update := config.ProviderConfigUpdate{
		Name:               providerName,
		SetDefaultProvider: setDefault,
		Enabled:            providerLoginBoolValuePtr(candidate.Enabled),
		Protocol:           providerLoginStringValuePtr(candidate.Protocol),
		BaseURL:            providerLoginStringValuePtr(candidate.BaseURL),
		APIKeyRef:          providerLoginStringValuePtr(candidate.APIKeyRef),
		AuthMode:           providerLoginStringValuePtr(authMode),
		ModelsPath:         providerLoginStringValuePtr(candidate.ModelsPath),
		ModelsVerifiedAt:   providerLoginStringValuePtr(verifiedAt),
		SupportedModels:    &supportedModels,
		DefaultModel:       providerLoginStringValuePtr(candidate.DefaultModel),
	}
	update.APIPath = providerLoginStringValuePtr(candidate.APIPath)
	update.ForwardURL = providerLoginStringValuePtr(candidate.ForwardURL)
	supportTypes := append([]string(nil), candidate.SupportTypes...)
	update.SupportTypes = &supportTypes
	maxTokensLimit := candidate.MaxTokensLimit
	update.MaxTokensLimit = &maxTokensLimit
	if len(candidate.ModelCapabilities) > 0 {
		capabilities := cloneProviderLoginModelCapabilities(candidate.ModelCapabilities)
		update.ModelCapabilities = &capabilities
	}
	if authMode == providerAuthModeOAuth {
		update.APIKey = providerLoginStringValuePtr("")
		update.AuthRef = providerLoginStringValuePtr(candidate.AuthRef)
	} else {
		update.APIKey = providerLoginStringValuePtr("")
		update.AuthRef = providerLoginStringValuePtr("")
	}
	_ = loginProtocol
	return update
}

func buildProviderLoginModelCapabilities(providerName, loginProtocol string, provider config.Provider, models []providerModelInfo) map[string]config.ModelCapabilitySpec {
	defaultEfforts := defaultProviderLoginReasoningEfforts(providerName, loginProtocol, provider, models)
	capabilities := make(map[string]config.ModelCapabilitySpec)

	for _, model := range models {
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		spec := providerLoginModelCapabilitySpec(model)
		if len(spec.ReasoningEfforts) == 0 && providerLoginModelUsesDefaultReasoningEfforts(modelID, providerName, loginProtocol, provider) {
			spec.ReasoningEfforts = append([]string(nil), defaultEfforts...)
			spec.ReasoningModel = len(spec.ReasoningEfforts) > 0
		}
		if !providerLoginModelCapabilityIsEmpty(spec) {
			capabilities[modelID] = spec
		}
	}

	if providerLoginUsesWildcardReasoningEfforts(loginProtocol, provider) && len(defaultEfforts) > 0 {
		spec := capabilities["*"]
		if len(spec.ReasoningEfforts) == 0 {
			spec.ReasoningEfforts = append([]string(nil), defaultEfforts...)
			spec.ReasoningModel = true
			capabilities["*"] = spec
		}
	}
	if len(capabilities) == 0 {
		return nil
	}
	return capabilities
}

func providerLoginModelCapabilitySpec(model providerModelInfo) config.ModelCapabilitySpec {
	spec := config.ModelCapabilitySpec{}
	if len(model.InputModalities) > 0 {
		spec.InputModalities = dedupeProviderStringOptions(model.InputModalities)
	}
	if len(model.ReasoningEfforts) > 0 {
		spec.ReasoningEfforts = dedupeProviderStringOptions(model.ReasoningEfforts)
		spec.ReasoningModel = true
	}
	if model.MaxContextTokens > 0 {
		spec.MaxContextTokens = model.MaxContextTokens
	}
	if model.SupportsRemoteCodex {
		spec.SupportsRemoteCompact = true
	}
	return spec
}

func defaultProviderLoginReasoningEfforts(providerName, loginProtocol string, provider config.Provider, models []providerModelInfo) []string {
	return providercompat.DefaultLoginReasoningEfforts(providercompat.Context{
		ProviderName: providerName,
		Protocol:     runtimeProtocolForLoginProtocol(loginProtocol),
		BaseURL:      provider.BaseURL,
		Model:        providerLoginCompatModelHint(providerName, loginProtocol, provider, models),
	})
}

func providerLoginModelUsesDefaultReasoningEfforts(modelID, providerName, loginProtocol string, provider config.Provider) bool {
	return providercompat.LoginModelUsesDefaultReasoningEfforts(providercompat.Context{
		ProviderName: providerName,
		Protocol:     runtimeProtocolForLoginProtocol(loginProtocol),
		BaseURL:      provider.BaseURL,
		Model:        modelID,
	}, modelID)
}

func providerLoginUsesWildcardReasoningEfforts(loginProtocol string, provider config.Provider) bool {
	return providercompat.LoginUsesWildcardReasoningEfforts(providercompat.Context{
		Protocol: runtimeProtocolForLoginProtocol(loginProtocol),
		BaseURL:  provider.BaseURL,
	})
}

func providerLoginCompatModelHint(providerName, loginProtocol string, provider config.Provider, models []providerModelInfo) string {
	modelIDs := make([]string, 0, len(models))
	for _, model := range models {
		if modelID := strings.TrimSpace(model.ID); modelID != "" {
			modelIDs = append(modelIDs, modelID)
		}
	}
	return providercompat.LoginModelHint(providercompat.Context{
		ProviderName: providerName,
		Protocol:     runtimeProtocolForLoginProtocol(loginProtocol),
		BaseURL:      provider.BaseURL,
	}, modelIDs)
}

func buildProviderLoginModelCapabilitiesForLogin(
	providerName, loginProtocol string,
	provider config.Provider,
	models []providerModelInfo,
	catalog *modelcard.Catalog,
) (map[string]config.ModelCapabilitySpec, []providerLoginModelCardAppliedInfo, []providerLoginModelCardSkippedInfo) {
	merged := cloneProviderLoginModelCapabilities(provider.ModelCapabilities)
	if merged == nil {
		merged = make(map[string]config.ModelCapabilitySpec)
	}
	defaultEfforts := defaultProviderLoginReasoningEfforts(providerName, loginProtocol, provider, models)
	ctx := modelcard.Context{
		ProviderName:    providerName,
		LoginProtocol:   loginProtocol,
		RuntimeProtocol: runtimeProtocolForLoginProtocol(loginProtocol),
		BaseURL:         provider.BaseURL,
	}
	if catalog != nil {
		if template, ok := resolveProviderLoginProviderTemplate(catalog, runtimeProtocolForLoginProtocol(loginProtocol), provider); ok {
			ctx.ProviderTemplate = strings.TrimSpace(template.ID)
		}
	}
	applied := make([]providerLoginModelCardAppliedInfo, 0)
	skipped := make([]providerLoginModelCardSkippedInfo, 0)

	for _, model := range models {
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		existing := merged[modelID]
		remote := providerLoginModelCapabilitySpec(model)
		compat := providerLoginCompatModelCapabilitySpec(modelID, providerName, loginProtocol, provider, defaultEfforts)
		cardCapability := config.ModelCapabilitySpec{}
		cardApplied := []modelcard.AppliedCard(nil)
		if catalog != nil {
			cardCapability, cardApplied = catalog.Resolve(ctx, modelID)
			if len(cardApplied) == 0 {
				skipped = append(skipped, providerLoginModelCardSkippedInfo{Model: modelID, Reason: "no_matching_card"})
			}
			for _, item := range cardApplied {
				applied = append(applied, providerLoginModelCardAppliedInfo{
					Model:  modelID,
					CardID: item.CardID,
					Fields: append([]string(nil), item.Fields...),
				})
			}
		}
		spec := modelcard.MergeCapability(existing, remote, cardCapability, compat)
		if !providerLoginModelCapabilityIsEmpty(spec) {
			merged[modelID] = spec
		}
	}

	if providerLoginUsesWildcardReasoningEfforts(loginProtocol, provider) && len(defaultEfforts) > 0 {
		compat := config.ModelCapabilitySpec{
			ReasoningModel:   true,
			ReasoningEfforts: append([]string(nil), defaultEfforts...),
		}
		spec := modelcard.MergeCapability(merged["*"], config.ModelCapabilitySpec{}, config.ModelCapabilitySpec{}, compat)
		if !providerLoginModelCapabilityIsEmpty(spec) {
			merged["*"] = spec
		}
	}
	if len(merged) == 0 {
		return nil, applied, skipped
	}
	return merged, applied, skipped
}

func providerLoginCompatModelCapabilitySpec(modelID, providerName, loginProtocol string, provider config.Provider, defaultEfforts []string) config.ModelCapabilitySpec {
	if len(defaultEfforts) == 0 {
		return config.ModelCapabilitySpec{}
	}
	if !providerLoginModelUsesDefaultReasoningEfforts(modelID, providerName, loginProtocol, provider) {
		return config.ModelCapabilitySpec{}
	}
	return config.ModelCapabilitySpec{
		ReasoningModel:   true,
		ReasoningEfforts: append([]string(nil), defaultEfforts...),
	}
}

func mergeProviderLoginModelCapabilities(existing, discovered map[string]config.ModelCapabilitySpec) map[string]config.ModelCapabilitySpec {
	if len(discovered) == 0 {
		return cloneProviderLoginModelCapabilities(existing)
	}
	merged := cloneProviderLoginModelCapabilities(existing)
	if merged == nil {
		merged = make(map[string]config.ModelCapabilitySpec, len(discovered))
	}
	for model, spec := range discovered {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		merged[model] = mergeProviderLoginModelCapabilitySpec(merged[model], spec)
	}
	return merged
}

func mergeProviderLoginModelCapabilitySpec(base, update config.ModelCapabilitySpec) config.ModelCapabilitySpec {
	if len(update.InputModalities) > 0 {
		base.InputModalities = append([]string(nil), update.InputModalities...)
	}
	if update.NativeTools.ImageGeneration {
		base.NativeTools.ImageGeneration = true
	}
	if update.NativeTools.ImagesGenerationsAPI {
		base.NativeTools.ImagesGenerationsAPI = true
	}
	if update.ReasoningModel {
		base.ReasoningModel = true
	}
	if len(update.ReasoningEfforts) > 0 {
		base.ReasoningEfforts = append([]string(nil), update.ReasoningEfforts...)
	}
	if update.MaxContextTokens > 0 {
		base.MaxContextTokens = update.MaxContextTokens
	}
	if update.SupportsRemoteCompact {
		base.SupportsRemoteCompact = true
	}
	return base
}

func cloneProviderLoginModelCapabilities(input map[string]config.ModelCapabilitySpec) map[string]config.ModelCapabilitySpec {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]config.ModelCapabilitySpec, len(input))
	for key, value := range input {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if len(value.InputModalities) > 0 {
			value.InputModalities = append([]string(nil), value.InputModalities...)
		}
		if len(value.ReasoningEfforts) > 0 {
			value.ReasoningEfforts = append([]string(nil), value.ReasoningEfforts...)
		}
		if len(value.ReasoningEffortBudgets) > 0 {
			budgets := make(map[string]int, len(value.ReasoningEffortBudgets))
			for budgetKey, budgetValue := range value.ReasoningEffortBudgets {
				budgets[budgetKey] = budgetValue
			}
			value.ReasoningEffortBudgets = budgets
		}
		output[strings.TrimSpace(key)] = value
	}
	return output
}

func providerLoginModelCapabilityIsEmpty(spec config.ModelCapabilitySpec) bool {
	return len(spec.InputModalities) == 0 &&
		!spec.NativeTools.ImageGeneration &&
		!spec.NativeTools.ImagesGenerationsAPI &&
		!spec.ReasoningModel &&
		len(spec.ReasoningEfforts) == 0 &&
		len(spec.ReasoningEffortBudgets) == 0 &&
		strings.TrimSpace(spec.DefaultReasoningEffort) == "" &&
		spec.MaxContextTokens == 0 &&
		spec.MaxTokens == 0 &&
		spec.AutoCompactRatio == 0 &&
		spec.AutoCompactTokenLimit == 0 &&
		strings.TrimSpace(spec.AutoCompactMode) == "" &&
		!spec.SupportsRemoteCompact &&
		strings.TrimSpace(spec.CompactReasoningEffort) == ""
}

func applyProviderLoginConfigUpdate(cfg *config.Config, providerName string, provider config.Provider, setDefault bool) {
	if cfg == nil {
		return
	}
	if cfg.Providers.Items == nil {
		cfg.Providers.Items = make(map[string]config.Provider)
	}
	cfg.Providers.Items[providerName] = provider
	if setDefault {
		cfg.Providers.DefaultProvider = providerName
	}
}

func loginProtocolFromProvider(provider config.Provider, mode string) string {
	protocol := provider.GetProtocol()
	if protocol == "codex" {
		if normalizeProviderAuthMode(mode) == providerAuthModeOAuth || strings.EqualFold(provider.AuthMode, providerAuthModeOAuth) {
			return "codex-oauth"
		}
		return "codex-apikey"
	}
	return protocol
}

func normalizeLoginProtocol(protocol, mode string) string {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	mode = normalizeProviderAuthMode(mode)
	if protocol == providerLoginProtocolAuto {
		return providerLoginProtocolAuto
	}
	if protocol == "openai-image" {
		return providerLoginProtocolOpenAIImage
	}
	if protocol == "codex" {
		if mode == providerAuthModeOAuth {
			return "codex-oauth"
		}
		return "codex-apikey"
	}
	if protocol == "codex-api-key" || protocol == "codex-apikey" {
		return "codex-apikey"
	}
	return protocol
}

func normalizeProviderAuthMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", "apikey", "api-key", "key":
		return providerAuthModeAPIKey
	case "oauth", "chatgpt", "device-code", "device_code":
		return providerAuthModeOAuth
	default:
		return mode
	}
}

func runtimeProtocolForLoginProtocol(protocol string) string {
	switch normalizeLoginProtocol(protocol, "") {
	case "codex-apikey", "codex-oauth":
		return "codex"
	case providerLoginProtocolAuto:
		return "openai"
	default:
		return normalizeLoginProtocol(protocol, "")
	}
}

func isSupportedLoginProtocol(protocol string) bool {
	switch normalizeLoginProtocol(protocol, "") {
	case providerLoginProtocolAuto, "openai", providerLoginProtocolOpenAIImage, "anthropic", "gemini", "codex-apikey", "codex-oauth":
		return true
	default:
		return false
	}
}

func loginProtocolOptions() []string {
	return []string{providerLoginProtocolAuto, "openai", providerLoginProtocolOpenAIImage, "codex-apikey", "anthropic", "gemini", "codex-oauth"}
}

func promptLoginProtocol(prompter providerLoginPrompter) (string, error) {
	options := loginProtocolOptions()
	prompter.PrintLine("请选择登录协议:")
	for i, option := range options {
		prompter.PrintLine(fmt.Sprintf("  [%d] %s", i+1, option))
	}
	for {
		value, err := prompter.PromptText("协议编号或名称", "auto", true)
		if err != nil {
			return "", err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return providerLoginProtocolAuto, nil
		}
		for i, option := range options {
			if value == fmt.Sprintf("%d", i+1) || strings.EqualFold(value, option) {
				return option, nil
			}
		}
		prompter.PrintLine("无效协议，请重新输入")
	}
}

func authStorePathForResult(req providerLoginRequest, authMode string) string {
	if authMode != providerAuthModeOAuth && authMode != providerAuthModeAPIKey {
		return ""
	}
	if strings.TrimSpace(req.AuthStorePath) != "" {
		return strings.TrimSpace(req.AuthStorePath)
	}
	return config.DefaultAuthStorePath()
}

func resolveLoginAPIKeyRef(existing config.Provider, exists bool, providerName string) string {
	if exists && strings.TrimSpace(existing.APIKeyRef) != "" {
		return strings.TrimSpace(existing.APIKeyRef)
	}
	if strings.TrimSpace(providerName) != "" {
		return strings.TrimSpace(providerName)
	}
	return ""
}

func stringInListFold(value string, list []string) bool {
	value = strings.TrimSpace(value)
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item), value) {
			return true
		}
	}
	return false
}

func canonicalStringFromList(value string, list []string) string {
	value = strings.TrimSpace(value)
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item), value) {
			return strings.TrimSpace(item)
		}
	}
	return value
}

func maskSecretForDisplay(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ""
	}
	if len(secret) <= 8 {
		return "****"
	}
	return secret[:4] + "..." + secret[len(secret)-4:]
}

func providerLoginStringValuePtr(value string) *string {
	value = strings.TrimSpace(value)
	return &value
}

func providerLoginBoolValuePtr(value bool) *bool {
	return &value
}
