package commands

import (
	"fmt"
	"os"
	"sort"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/spf13/cobra"
)

type configCommandResult struct {
	Config       *config.Config
	ProviderFlag string
	ShowGroups   bool
	ShowModels   bool
	JSONPayload  interface{}
}

// HandleConfig 处理 config 命令
func HandleConfig(cmd *cobra.Command, cfg *config.Config) {
	providerFlag, _ := cmd.Flags().GetString("provider")
	showGroups, _ := cmd.Flags().GetBool("groups")
	showModels, _ := cmd.Flags().GetBool("models")
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("config", "json", err, nil)
	}

	executeStructuredCommand("config", outputOptions, func() (configCommandResult, map[string]interface{}, error) {
		return runConfigCommand(cfg, providerFlag, showGroups, showModels)
	}, func(result configCommandResult) interface{} {
		return result.JSONPayload
	}, renderConfigResult)
}

func runConfigCommand(cfg *config.Config, providerFlag string, showGroups, showModels bool) (configCommandResult, map[string]interface{}, error) {
	details := map[string]interface{}{
		"provider": providerFlag,
		"groups":   showGroups,
		"models":   showModels,
	}
	result := configCommandResult{
		Config:       cfg,
		ProviderFlag: providerFlag,
		ShowGroups:   showGroups,
		ShowModels:   showModels,
	}

	if providerFlag != "" {
		if _, ok := cfg.Providers.Items[providerFlag]; !ok {
			return result, details, fmt.Errorf("provider '%s' not found", providerFlag)
		}
	}

	payload, err := buildConfigJSONPayload(cfg, providerFlag, showGroups, showModels)
	if err != nil {
		return result, details, err
	}
	result.JSONPayload = payload
	return result, nil, nil
}

func renderConfigResult(result configCommandResult) {
	switch {
	case result.ProviderFlag != "":
		displayProvider(result.Config, result.ProviderFlag, result.ShowModels)
	case result.ShowGroups:
		displayProviderGroups(result.Config)
	case result.ShowModels:
		displayAllModels(result.Config)
	default:
		displayConfigSummary(result.Config)
	}
}

// displayConfigSummary 显示配置摘要
func displayConfigSummary(cfg *config.Config) {
	fmt.Println("================================================================================")
	fmt.Println("                           AI Gateway 配置信息")
	fmt.Println("================================================================================")
	fmt.Println()

	// Server 配置
	fmt.Println("[Server]")
	fmt.Printf("  Name:         %s\n", cfg.Server.Name)
	fmt.Printf("  Host:         %s\n", cfg.Server.Host)
	fmt.Printf("  Port:         %d\n", cfg.Server.Port)
	fmt.Printf("  Development:  %v\n", cfg.Server.Development)
	fmt.Println()

	// Database 配置
	fmt.Println("[Database]")
	fmt.Printf("  Driver:       %s\n", cfg.Database.Driver)
	fmt.Printf("  DSN:          %s\n", cfg.Database.DSN)
	fmt.Println()

	// Providers 配置
	fmt.Println("[Providers]")
	fmt.Printf("  Default:      %s\n", cfg.Providers.DefaultProvider)
	fmt.Printf("  Timeout:      %s\n", cfg.Providers.Timeout)
	fmt.Printf("  Max Retries:  %d\n", cfg.Providers.MaxRetries)
	fmt.Printf("  Count:        %d\n", len(cfg.Providers.Items))
	fmt.Println()

	// Provider Groups 配置
	fmt.Println("[Provider Groups]")
	fmt.Printf("  Count:        %d\n", len(cfg.ProviderGroups))
	fmt.Println()

	fmt.Println("Hint: 使用 'aicli config --provider <name>' 查看具体 provider 详情")
	fmt.Println("      使用 'aicli config --groups' 查看 provider groups")
	fmt.Println("      使用 'aicli config --models' 列出所有可用模型")
}

// displayProvider 显示 provider 详情
func displayProvider(cfg *config.Config, providerName string, showModels bool) {
	provider, ok := cfg.Providers.Items[providerName]
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: Provider '%s' not found\n", providerName)
		fmt.Println("\nAvailable providers:")
		for name := range cfg.Providers.Items {
			fmt.Printf("  - %s\n", name)
		}
		return
	}

	fmt.Println("================================================================================")
	fmt.Printf("                          Provider: %s\n", providerName)
	fmt.Println("================================================================================")
	fmt.Println()

	fmt.Printf("Enabled:         %v\n", provider.Enabled)
	fmt.Printf("Type:            %s\n", provider.GetProtocol())
	fmt.Printf("Base URL:        %s\n", provider.BaseURL)
	fmt.Printf("API Path:        %s\n", provider.APIPath)
	fmt.Printf("Forward URL:     %s\n", provider.ForwardURL)

	// API Key 显示（支持多个 keys）
	allKeys := provider.GetAllAPIKeys()
	apiKeyDisplay := "(not set)"
	if len(allKeys) > 0 {
		if len(allKeys) > 1 {
			// 多个 keys：显示数量和第一个 key
			firstKey := allKeys[0]
			if len(firstKey) > 15 {
				apiKeyDisplay = firstKey[:15] + "... (" + fmt.Sprintf("%d", len(allKeys)) + " keys)"
			} else {
				apiKeyDisplay = firstKey + "... (" + fmt.Sprintf("%d", len(allKeys)) + " keys)"
			}
		} else {
			// 单个 key
			if len(allKeys[0]) > 15 {
				apiKeyDisplay = allKeys[0][:15] + "..."
			} else {
				apiKeyDisplay = allKeys[0] + "..."
			}
		}
	}
	fmt.Printf("API Key:         %s\n", apiKeyDisplay)
	fmt.Printf("Default Model:   %s\n", provider.DefaultModel)
	fmt.Printf("Max Tokens:      %d\n", provider.MaxTokensLimit)
	fmt.Println()

	// 支持的协议类型
	if len(provider.SupportTypes) > 0 {
		fmt.Println("Support Types:")
		for _, pt := range provider.SupportTypes {
			fmt.Printf("  - %s\n", pt)
		}
		fmt.Println()
	}

	// 支持的模型
	if showModels && len(provider.SupportedModels) > 0 {
		fmt.Println("Supported Models:")
		for _, model := range provider.SupportedModels {
			fmt.Printf("  - %s\n", model)
		}
		fmt.Println()
	}

	// 模型映射
	if len(provider.ModelMappings) > 0 {
		fmt.Println("Model Mappings:")
		keys := make([]string, 0, len(provider.ModelMappings))
		for k := range provider.ModelMappings {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("  %s -> %s\n", k, provider.ModelMappings[k])
		}
		fmt.Println()
	}
}

// displayProviderGroups 显示 provider groups
func displayProviderGroups(cfg *config.Config) {
	fmt.Println("================================================================================")
	fmt.Println("                         Provider Groups")
	fmt.Println("================================================================================")
	fmt.Println()

	if len(cfg.ProviderGroups) == 0 {
		fmt.Println("No provider groups configured")
		return
	}

	for _, group := range cfg.ProviderGroups {
		fmt.Printf("Group: %s\n", group.Name)
		fmt.Printf("  Strategy:       %s\n", group.Strategy)
		fmt.Printf("  Provider Count: %d\n", len(group.Providers))
		if len(group.Providers) > 0 {
			fmt.Println("  Providers:")
			for _, p := range group.Providers {
				fmt.Printf("    - Name: %s, Weight: %d\n", p.Name, p.Weight)
			}
		}
		fmt.Printf("  Max Retries:    %d\n", group.MaxRetries)
		if group.Truncation != nil {
			fmt.Printf("  Truncation Enabled: %v\n", group.Truncation.Enabled)
			fmt.Printf("  Truncation Max Retries: %d\n", group.Truncation.MaxRetries)
		}
		fmt.Println()
	}
}

// displayAllModels 列出所有可用模型
func displayAllModels(cfg *config.Config) {
	fmt.Println("================================================================================")
	fmt.Println("                      所有可用模型 (All Available Models)")
	fmt.Println("================================================================================")
	fmt.Println()

	modelsMap := make(map[string]string) // model -> provider
	for providerName, provider := range cfg.Providers.Items {
		if !provider.Enabled {
			continue
		}
		for _, model := range provider.SupportedModels {
			modelsMap[model] = providerName
		}
	}

	if len(modelsMap) == 0 {
		fmt.Println("No models found")
		return
	}

	// 按模型名称排序
	models := make([]string, 0, len(modelsMap))
	for model := range modelsMap {
		models = append(models, model)
	}
	sort.Strings(models)

	// 计算 provider 长度作为对齐参考
	maxProviderLen := 0
	for _, providerName := range modelsMap {
		if len(providerName) > maxProviderLen {
			maxProviderLen = len(providerName)
		}
	}

	fmt.Printf("Total: %d models\n\n", len(models))
	for _, model := range models {
		fmt.Printf("%-50s -> %s\n", model, modelsMap[model])
	}
	fmt.Println()

	// 显示模型映射
	fmt.Println("Model Mappings:")
	fmt.Println()
	for providerName, provider := range cfg.Providers.Items {
		if !provider.Enabled {
			continue
		}
		if len(provider.ModelMappings) > 0 {
			if providerName == cfg.Providers.DefaultProvider {
				fmt.Printf("  [%s] (Default):\n", providerName)
			} else {
				fmt.Printf("  [%s]:\n", providerName)
			}
			keys := make([]string, 0, len(provider.ModelMappings))
			for k := range provider.ModelMappings {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Printf("    %-40s -> %s\n", k, provider.ModelMappings[k])
			}
			fmt.Println()
		}
	}
}

// outputConfigJSON 输出 JSON 格式的配置
func buildConfigJSONPayload(cfg *config.Config, providerFlag string, showGroups, showModels bool) (interface{}, error) {
	if providerFlag != "" {
		provider, ok := cfg.Providers.Items[providerFlag]
		if !ok {
			return nil, fmt.Errorf("provider '%s' not found", providerFlag)
		}
		return provider, nil
	}

	if showGroups {
		return cfg.ProviderGroups, nil
	}

	if showModels {
		modelsMap := make(map[string][]string) // provider -> models
		for providerName, provider := range cfg.Providers.Items {
			if !provider.Enabled {
				continue
			}
			if len(provider.SupportedModels) > 0 {
				modelsMap[providerName] = provider.SupportedModels
			}
		}
		return modelsMap, nil
	}

	// 默认输出全部配置摘要
	summary := map[string]interface{}{
		"server": map[string]interface{}{
			"name":        cfg.Server.Name,
			"host":        cfg.Server.Host,
			"port":        cfg.Server.Port,
			"development": cfg.Server.Development,
		},
		"database": map[string]interface{}{
			"driver": cfg.Database.Driver,
			"dsn":    cfg.Database.DSN,
		},
		"providers": map[string]interface{}{
			"default_provider": cfg.Providers.DefaultProvider,
			"timeout":          cfg.Providers.Timeout,
			"max_retries":      cfg.Providers.MaxRetries,
			"count":            len(cfg.Providers.Items),
		},
		"provider_groups": map[string]interface{}{
			"count": len(cfg.ProviderGroups),
		},
	}
	return summary, nil
}
