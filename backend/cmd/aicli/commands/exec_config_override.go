package commands

import (
	"fmt"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func applyConfigOverrides(cfg *config.Config, overrides []string) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	for _, override := range overrides {
		parts := strings.SplitN(override, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("无效的配置覆盖格式: %q（期望 key=value）", override)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			return fmt.Errorf("配置覆盖 key 不能为空")
		}
		if err := applySingleConfigOverride(cfg, key, value); err != nil {
			return fmt.Errorf("应用配置覆盖 %q 失败: %w", override, err)
		}
	}
	return nil
}

func applySingleConfigOverride(cfg *config.Config, key, value string) error {
	switch {
	case key == "model":
		providerName := strings.TrimSpace(cfg.Providers.DefaultProvider)
		if providerName == "" {
			return fmt.Errorf("未设置默认 provider")
		}
		provider, ok := cfg.Providers.Items[providerName]
		if !ok {
			return fmt.Errorf("provider %q 不存在", providerName)
		}
		provider.DefaultModel = value
		cfg.Providers.Items[providerName] = provider
	case strings.HasPrefix(key, "provider."):
		return applyProviderConfigOverride(cfg, strings.TrimPrefix(key, "provider."), value)
	default:
		return fmt.Errorf("不支持的配置 key: %q", key)
	}
	return nil
}

func applyProviderConfigOverride(cfg *config.Config, subKey, value string) error {
	providerName := strings.TrimSpace(cfg.Providers.DefaultProvider)
	if providerName == "" {
		return fmt.Errorf("未设置默认 provider")
	}
	provider, ok := cfg.Providers.Items[providerName]
	if !ok {
		return fmt.Errorf("provider %q 不存在", providerName)
	}
	switch strings.TrimSpace(subKey) {
	case "base_url":
		provider.BaseURL = value
	case "api_key":
		provider.APIKey = value
	case "forward_url":
		provider.ForwardURL = value
	default:
		return fmt.Errorf("不支持的 provider 配置 key: %q", subKey)
	}
	cfg.Providers.Items[providerName] = provider
	return nil
}
