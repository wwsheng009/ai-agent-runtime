package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

const (
	providerAuthModeAPIKey = "api_key"
	providerAuthModeOAuth  = "oauth"
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
}

type providerLoginResult struct {
	ProviderName     string              `json:"provider"`
	Protocol         string              `json:"protocol"`
	LoginProtocol    string              `json:"login_protocol"`
	AuthMode         string              `json:"auth_mode"`
	AuthRef          string              `json:"auth_ref,omitempty"`
	APIKeyRef        string              `json:"api_key_ref,omitempty"`
	BaseURL          string              `json:"base_url"`
	ModelsPath       string              `json:"models_path"`
	ModelsEndpoint   string              `json:"models_endpoint"`
	ModelsVerifiedAt string              `json:"models_verified_at"`
	SupportedModels  []string            `json:"supported_models"`
	DefaultModel     string              `json:"default_model"`
	Models           []providerModelInfo `json:"models,omitempty"`
	Created          bool                `json:"created"`
	Updated          bool                `json:"updated"`
	DryRun           bool                `json:"dry_run"`
	ConfigPath       string              `json:"config_path,omitempty"`
	AuthStorePath    string              `json:"auth_store_path,omitempty"`
	APIKeyMasked     string              `json:"api_key_masked,omitempty"`
	SetDefault       bool                `json:"set_default"`
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

	modelsPath := resolveProviderModelsPath(loginProtocol, candidate, req.ModelsPath)
	candidate.ModelsPath = modelsPath
	modelsResult, err := validateProviderModels(providerModelsValidationRequest{
		Config:        cfg,
		ProviderName:  providerName,
		Provider:      candidate,
		LoginProtocol: loginProtocol,
		ModelsPath:    modelsPath,
		Timeout:       req.Timeout,
	})
	if err != nil {
		return nil, err
	}

	supportedModels := providerModelIDs(modelsResult.Models)
	defaultModel, err := resolveLoginDefaultModel(req, candidate, supportedModels)
	if err != nil {
		return nil, err
	}
	candidate.SupportedModels = supportedModels
	candidate.DefaultModel = defaultModel
	candidate.ModelsVerifiedAt = modelsResult.VerifiedAt

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
	}

	return &providerLoginResult{
		ProviderName:     providerName,
		Protocol:         runtimeProtocol,
		LoginProtocol:    loginProtocol,
		AuthMode:         authMode,
		AuthRef:          candidate.AuthRef,
		APIKeyRef:        candidate.APIKeyRef,
		BaseURL:          candidate.BaseURL,
		ModelsPath:       modelsPath,
		ModelsEndpoint:   modelsResult.Endpoint,
		ModelsVerifiedAt: modelsResult.VerifiedAt,
		SupportedModels:  supportedModels,
		DefaultModel:     defaultModel,
		Models:           modelsResult.Models,
		Created:          !exists,
		Updated:          exists,
		DryRun:           req.DryRun,
		ConfigPath:       configPath,
		AuthStorePath:    authStorePathForResult(req, authMode),
		APIKeyMasked:     maskSecretForDisplay(resolvedAPIKey),
		SetDefault:       req.SetDefault,
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
	default:
		return normalizeLoginProtocol(protocol, "")
	}
}

func isSupportedLoginProtocol(protocol string) bool {
	switch normalizeLoginProtocol(protocol, "") {
	case "openai", "anthropic", "gemini", "codex-apikey", "codex-oauth":
		return true
	default:
		return false
	}
}

func promptLoginProtocol(prompter providerLoginPrompter) (string, error) {
	options := []string{"openai", "codex-apikey", "anthropic", "gemini", "codex-oauth"}
	prompter.PrintLine("请选择登录协议:")
	for i, option := range options {
		prompter.PrintLine(fmt.Sprintf("  [%d] %s", i+1, option))
	}
	for {
		value, err := prompter.PromptText("协议编号或名称", "openai", true)
		if err != nil {
			return "", err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return "openai", nil
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
