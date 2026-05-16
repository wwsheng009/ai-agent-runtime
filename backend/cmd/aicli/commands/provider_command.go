package commands

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

type providerListResult struct {
	Providers []config.ProviderSummary `json:"providers"`
	Total     int                      `json:"total"`
}

type providerShowResult struct {
	config.ProviderSummary
	SupportTypes              []string          `json:"support_types,omitempty"`
	ModelMappings             map[string]string `json:"model_mappings,omitempty"`
	MaxTokensLimit            int               `json:"max_tokens_limit,omitempty"`
	SupportsMaxOutputTokens   *bool             `json:"supports_max_output_tokens,omitempty"`
	ModelCapabilitiesCount    int               `json:"model_capabilities_count,omitempty"`
	HeadersConfigured         bool              `json:"headers_configured"`
	HeaderMappingsCount       int               `json:"header_mappings_count,omitempty"`
	HeaderMappingRulesCount   int               `json:"header_mapping_rules_count,omitempty"`
	SupportedModels           []string          `json:"supported_models,omitempty"`
	SupportedModelsTruncated  bool              `json:"supported_models_truncated,omitempty"`
	SupportedModelsShownCount int               `json:"supported_models_shown_count,omitempty"`
}

// NewProviderCommand creates provider management commands.
func NewProviderCommand(configProvider func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "provider",
		Aliases: []string{"providers"},
		Short:   "管理 provider 配置",
		Long:    "列出、查看、启停、删除 provider，并维护默认 provider 配置。",
	}
	cmd.AddCommand(newProviderListCommand(configProvider))
	cmd.AddCommand(newProviderShowCommand(configProvider))
	cmd.AddCommand(newProviderRemoveCommand(configProvider))
	cmd.AddCommand(newProviderEnableCommand(configProvider, true))
	cmd.AddCommand(newProviderEnableCommand(configProvider, false))
	cmd.AddCommand(newProviderSetDefaultCommand(configProvider))
	return cmd
}

func newProviderListCommand(configProvider func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "列出 provider",
		Run: func(cmd *cobra.Command, args []string) {
			HandleProviderList(cmd, configProvider)
		},
	}
	cmd.Flags().String("protocol", "", "按协议过滤（openai|anthropic|gemini|codex 等）")
	cmd.Flags().Bool("enabled", false, "只显示已启用 provider")
	cmd.Flags().Bool("disabled", false, "只显示已禁用 provider")
	addProviderOutputFlags(cmd)
	return cmd
}

func newProviderShowCommand(configProvider func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "show <name>",
		Aliases: []string{"get"},
		Short:   "查看 provider 详情",
		Args:    cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			HandleProviderShow(cmd, configProvider, args[0])
		},
	}
	cmd.Flags().Bool("models", false, "显示 supported_models 明细")
	addProviderOutputFlags(cmd)
	return cmd
}

func newProviderRemoveCommand(configProvider func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "remove [name...]",
		Aliases: []string{"rm", "delete"},
		Short:   "删除一个或多个 provider",
		Long:    "删除一个或多个 provider。未提供名称时进入交互选择，可输入编号或名称切换选择，并支持 all、clear、invert、done、q。",
		Args:    cobra.ArbitraryArgs,
		Run: func(cmd *cobra.Command, args []string) {
			HandleProviderRemove(cmd, configProvider, args)
		},
	}
	cmd.Flags().Bool("dry-run", false, "只预览删除结果，不写配置或 auth store")
	cmd.Flags().BoolP("yes", "y", false, "确认执行删除")
	cmd.Flags().Bool("cascade", false, "同步移除 provider_groups 中的引用；空 group 会被删除")
	cmd.Flags().Bool("clear-default", false, "删除默认 provider 时清空 providers.default_provider")
	cmd.Flags().String("set-default", "", "删除默认 provider 前切换到指定 provider")
	cmd.Flags().Bool("prune-auth", false, "删除未被其他 provider 引用的 auth store 凭证")
	addProviderOutputFlags(cmd)
	return cmd
}

func newProviderEnableCommand(configProvider func() *config.Config, enabled bool) *cobra.Command {
	name := "enable"
	short := "启用 provider"
	if !enabled {
		name = "disable"
		short = "禁用 provider"
	}
	cmd := &cobra.Command{
		Use:   name + " <name...>",
		Short: short,
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			HandleProviderEnable(cmd, configProvider, args, enabled)
		},
	}
	addProviderOutputFlags(cmd)
	return cmd
}

func newProviderSetDefaultCommand(configProvider func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-default <name>",
		Short: "设置默认 provider",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			HandleProviderSetDefault(cmd, configProvider, args[0])
		},
	}
	addProviderOutputFlags(cmd)
	return cmd
}

func addProviderOutputFlags(cmd *cobra.Command) {
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
}

func HandleProviderList(cmd *cobra.Command, configProvider func() *config.Config) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("provider list", "json", err, nil)
	}
	protocol := stringFlag(cmd, "protocol")
	enabledOnly := boolFlag(cmd, "enabled")
	disabledOnly := boolFlag(cmd, "disabled")
	executeCommand("provider list", outputOptions, func() (providerListResult, map[string]interface{}, error) {
		return runProviderListCommand(providerCommandConfig(configProvider), protocol, enabledOnly, disabledOnly)
	}, renderProviderListResult)
}

func HandleProviderShow(cmd *cobra.Command, configProvider func() *config.Config, name string) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("provider show", "json", err, nil)
	}
	showModels := boolFlag(cmd, "models")
	executeCommand("provider show", outputOptions, func() (providerShowResult, map[string]interface{}, error) {
		return runProviderShowCommand(providerCommandConfig(configProvider), name, showModels)
	}, renderProviderShowResult)
}

func HandleProviderRemove(cmd *cobra.Command, configProvider func() *config.Config, names []string) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("provider remove", "json", err, nil)
	}
	req := config.ProviderDeleteRequest{
		Names:              names,
		Cascade:            boolFlag(cmd, "cascade"),
		ClearDefault:       boolFlag(cmd, "clear-default"),
		ReplacementDefault: stringFlag(cmd, "set-default"),
		PruneAuth:          boolFlag(cmd, "prune-auth"),
		DryRun:             boolFlag(cmd, "dry-run"),
	}
	yes := boolFlag(cmd, "yes")
	executeCommand("provider remove", outputOptions, func() (*config.ProviderDeleteResult, map[string]interface{}, error) {
		cfg := providerCommandConfig(configProvider)
		if cfg == nil {
			return nil, nil, fmt.Errorf("config is not loaded")
		}
		effectiveReq := req
		if len(effectiveReq.Names) == 0 {
			if isJSONOutputFormat(outputOptions.Format) {
				return nil, nil, fmt.Errorf("provider remove JSON 模式需要显式提供 provider 名称")
			}
			selected, err := promptProviderRemoveSelection(os.Stdin, os.Stdout, config.ListProviderSummaries(cfg, config.ProviderListFilter{}))
			if err != nil {
				return nil, nil, err
			}
			if len(selected) == 0 {
				return nil, nil, fmt.Errorf("未选择要删除的 provider")
			}
			effectiveReq.Names = selected
			if !effectiveReq.DryRun && !yes {
				confirmed, err := confirmProviderRemoveSelection(os.Stdin, os.Stdout, selected)
				if err != nil {
					return nil, nil, err
				}
				if !confirmed {
					return nil, nil, fmt.Errorf("已取消删除 provider")
				}
				yes = true
			}
		}
		if !effectiveReq.DryRun && !yes {
			return nil, nil, fmt.Errorf("删除 provider 会修改配置；请添加 --yes 确认，或使用 --dry-run 预览")
		}
		result, err := runProviderRemoveCommand(cfg, effectiveReq)
		if err != nil {
			return result, providerResultDetails(result), err
		}
		if len(result.Blocked) > 0 {
			return result, providerResultDetails(result), fmt.Errorf("provider 删除被阻止")
		}
		if len(result.Deleted) == 0 && len(result.NotFound) > 0 {
			return result, providerResultDetails(result), fmt.Errorf("未找到可删除的 provider")
		}
		return result, nil, nil
	}, renderProviderRemoveResult)
}

func HandleProviderEnable(cmd *cobra.Command, configProvider func() *config.Config, names []string, enabled bool) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("provider enable", "json", err, nil)
	}
	commandName := "provider enable"
	if !enabled {
		commandName = "provider disable"
	}
	executeCommand(commandName, outputOptions, func() (*config.ProviderEnableResult, map[string]interface{}, error) {
		result, err := runProviderEnableCommand(providerCommandConfig(configProvider), names, enabled)
		if err != nil {
			return result, providerResultDetails(result), err
		}
		if len(result.Updated) == 0 && len(result.NotFound) > 0 {
			return result, providerResultDetails(result), fmt.Errorf("未找到可更新的 provider")
		}
		return result, nil, nil
	}, renderProviderEnableResult)
}

func HandleProviderSetDefault(cmd *cobra.Command, configProvider func() *config.Config, name string) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("provider set-default", "json", err, nil)
	}
	executeCommand("provider set-default", outputOptions, func() (*config.ProviderDefaultResult, map[string]interface{}, error) {
		return runProviderSetDefaultCommand(providerCommandConfig(configProvider), name)
	}, renderProviderDefaultResult)
}

func runProviderListCommand(cfg *config.Config, protocol string, enabledOnly, disabledOnly bool) (providerListResult, map[string]interface{}, error) {
	if cfg == nil {
		return providerListResult{}, nil, fmt.Errorf("config is not loaded")
	}
	if enabledOnly && disabledOnly {
		return providerListResult{}, map[string]interface{}{"enabled": enabledOnly, "disabled": disabledOnly}, fmt.Errorf("--enabled 和 --disabled 不能同时使用")
	}
	filter := config.ProviderListFilter{Protocol: protocol}
	if enabledOnly || disabledOnly {
		enabled := enabledOnly
		filter.Enabled = &enabled
	}
	providers := config.ListProviderSummaries(cfg, filter)
	return providerListResult{Providers: providers, Total: len(providers)}, nil, nil
}

func runProviderShowCommand(cfg *config.Config, name string, showModels bool) (providerShowResult, map[string]interface{}, error) {
	if cfg == nil {
		return providerShowResult{}, nil, fmt.Errorf("config is not loaded")
	}
	canonical, provider, ok := findProviderByName(cfg, name)
	if !ok {
		return providerShowResult{}, map[string]interface{}{"provider": name}, fmt.Errorf("provider %q not found", name)
	}
	summary := config.ProviderSummary{}
	for _, item := range config.ListProviderSummaries(cfg, config.ProviderListFilter{}) {
		if item.Name == canonical {
			summary = item
			break
		}
	}
	models := append([]string(nil), provider.SupportedModels...)
	result := providerShowResult{
		ProviderSummary:           summary,
		SupportTypes:              append([]string(nil), provider.SupportTypes...),
		ModelMappings:             sortedStringMapCopy(provider.ModelMappings),
		MaxTokensLimit:            provider.GetMaxTokensLimit(),
		SupportsMaxOutputTokens:   provider.SupportsMaxOutputTokens,
		ModelCapabilitiesCount:    len(provider.ModelCapabilities),
		HeadersConfigured:         len(provider.Headers) > 0,
		HeaderMappingsCount:       len(provider.HeaderMappings),
		HeaderMappingRulesCount:   len(provider.HeaderMappingRules),
		SupportedModelsShownCount: len(models),
	}
	if showModels {
		result.SupportedModels = models
	} else if len(models) > 0 {
		const previewLimit = 20
		if len(models) > previewLimit {
			result.SupportedModels = append([]string(nil), models[:previewLimit]...)
			result.SupportedModelsTruncated = true
			result.SupportedModelsShownCount = previewLimit
		} else {
			result.SupportedModels = models
		}
	}
	return result, nil, nil
}

func runProviderRemoveCommand(cfg *config.Config, req config.ProviderDeleteRequest) (*config.ProviderDeleteResult, error) {
	configPath, err := providerCommandConfigPath(cfg)
	if err != nil {
		return nil, err
	}
	result, err := config.DeleteProvidersConfig(configPath, req)
	if err != nil {
		return result, err
	}
	return result, nil
}

func promptProviderRemoveSelection(reader io.Reader, writer io.Writer, providers []config.ProviderSummary) ([]string, error) {
	if reader == nil {
		reader = os.Stdin
	}
	if writer == nil {
		writer = os.Stdout
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("没有可删除的 provider")
	}
	selected := make(map[int]bool, len(providers))
	scanner := bufio.NewScanner(reader)
	for {
		renderProviderRemoveSelectionMenu(writer, providers, selected)
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("已取消删除 provider")
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		action := strings.ToLower(input)
		switch action {
		case "q", "quit", "cancel", "exit":
			return nil, fmt.Errorf("已取消删除 provider")
		case "d", "done", "ok", "confirm":
			return selectedProviderNames(providers, selected), nil
		case "a", "all", "*":
			for i := range providers {
				selected[i] = true
			}
			continue
		case "n", "none", "clear":
			for key := range selected {
				delete(selected, key)
			}
			continue
		case "i", "invert":
			for i := range providers {
				selected[i] = !selected[i]
			}
			continue
		}
		if err := toggleProviderRemoveSelection(input, providers, selected); err != nil {
			fmt.Fprintf(writer, "输入无效: %v\n\n", err)
		}
	}
}

func renderProviderRemoveSelectionMenu(writer io.Writer, providers []config.ProviderSummary, selected map[int]bool) {
	fmt.Fprintln(writer, "选择要删除的 provider:")
	for i, provider := range providers {
		mark := " "
		if selected[i] {
			mark = "x"
		}
		defaultMark := ""
		if provider.Default {
			defaultMark = " default"
		}
		enabled := "disabled"
		if provider.Enabled {
			enabled = "enabled"
		}
		fmt.Fprintf(writer, "  [%s] %2d. %-20s %-8s %-12s%s\n", mark, i+1, provider.Name, enabled, emptyIfBlank(provider.Protocol), defaultMark)
	}
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "输入编号或名称切换选择；支持 1,3、1-3、all 全选、clear 清空、invert 反选、done 继续、q 取消。")
	fmt.Fprint(writer, "provider remove> ")
}

func toggleProviderRemoveSelection(input string, providers []config.ProviderSummary, selected map[int]bool) error {
	tokens := strings.FieldsFunc(input, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	if len(tokens) == 0 {
		return fmt.Errorf("请输入编号、名称或操作")
	}
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		indexes, err := providerSelectionTokenIndexes(token, providers)
		if err != nil {
			return err
		}
		for _, index := range indexes {
			selected[index] = !selected[index]
		}
	}
	return nil
}

func providerSelectionTokenIndexes(token string, providers []config.ProviderSummary) ([]int, error) {
	if strings.Contains(token, "-") {
		parts := strings.SplitN(token, "-", 2)
		start, startErr := strconv.Atoi(strings.TrimSpace(parts[0]))
		end, endErr := strconv.Atoi(strings.TrimSpace(parts[1]))
		if startErr == nil && endErr == nil {
			if start <= 0 || end <= 0 || start > len(providers) || end > len(providers) {
				return nil, fmt.Errorf("范围超出 provider 列表: %s", token)
			}
			if start > end {
				start, end = end, start
			}
			indexes := make([]int, 0, end-start+1)
			for i := start; i <= end; i++ {
				indexes = append(indexes, i-1)
			}
			return indexes, nil
		}
	}
	if number, err := strconv.Atoi(token); err == nil {
		if number <= 0 || number > len(providers) {
			return nil, fmt.Errorf("编号超出 provider 列表: %s", token)
		}
		return []int{number - 1}, nil
	}
	for i, provider := range providers {
		if strings.EqualFold(provider.Name, token) {
			return []int{i}, nil
		}
	}
	return nil, fmt.Errorf("找不到 provider: %s", token)
}

func selectedProviderNames(providers []config.ProviderSummary, selected map[int]bool) []string {
	names := make([]string, 0, len(selected))
	for i, provider := range providers {
		if selected[i] {
			names = append(names, provider.Name)
		}
	}
	return names
}

func confirmProviderRemoveSelection(reader io.Reader, writer io.Writer, names []string) (bool, error) {
	if reader == nil {
		reader = os.Stdin
	}
	if writer == nil {
		writer = os.Stdout
	}
	fmt.Fprintf(writer, "将删除 provider: %s\n", strings.Join(names, ", "))
	fmt.Fprint(writer, "输入 yes 确认删除: ")
	scanner := bufio.NewScanner(reader)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return false, err
		}
		return false, nil
	}
	return strings.EqualFold(strings.TrimSpace(scanner.Text()), "yes"), nil
}

func runProviderEnableCommand(cfg *config.Config, names []string, enabled bool) (*config.ProviderEnableResult, error) {
	configPath, err := providerCommandConfigPath(cfg)
	if err != nil {
		return nil, err
	}
	result, err := config.SetProvidersEnabledConfig(configPath, names, enabled)
	if err != nil {
		return result, err
	}
	return result, nil
}

func runProviderSetDefaultCommand(cfg *config.Config, name string) (*config.ProviderDefaultResult, map[string]interface{}, error) {
	configPath, err := providerCommandConfigPath(cfg)
	if err != nil {
		return nil, nil, err
	}
	result, err := config.SetDefaultProviderConfig(configPath, name)
	if err != nil {
		return result, map[string]interface{}{"provider": name}, err
	}
	return result, nil, nil
}

func providerCommandConfig(configProvider func() *config.Config) *config.Config {
	if configProvider == nil {
		return nil
	}
	return configProvider()
}

func providerCommandConfigPath(cfg *config.Config) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("config is not loaded")
	}
	path := strings.TrimSpace(cfg.ConfigFilePath)
	if path == "" {
		return "", fmt.Errorf("config file path is not available")
	}
	return path, nil
}

func findProviderByName(cfg *config.Config, name string) (string, config.Provider, bool) {
	name = strings.TrimSpace(name)
	if cfg == nil || name == "" || cfg.Providers.Items == nil {
		return "", config.Provider{}, false
	}
	if provider, ok := cfg.Providers.Items[name]; ok {
		return name, provider, true
	}
	for candidate, provider := range cfg.Providers.Items {
		if strings.EqualFold(candidate, name) {
			return candidate, provider, true
		}
	}
	return "", config.Provider{}, false
}

func sortedStringMapCopy(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func providerResultDetails(value interface{}) map[string]interface{} {
	if value == nil {
		return nil
	}
	return map[string]interface{}{"result": value}
}

func renderProviderListResult(result providerListResult, outputOptions structuredOutputOptions) {
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("provider list", outputOptions.Envelope, result)
		return
	}
	if len(result.Providers) == 0 {
		fmt.Println("没有匹配的 provider")
		return
	}
	fmt.Printf("%-20s %-8s %-8s %-14s %-18s %s\n", "NAME", "ENABLED", "DEFAULT", "PROTOCOL", "AUTH_MODE", "BASE_URL")
	for _, item := range result.Providers {
		fmt.Printf("%-20s %-8t %-8t %-14s %-18s %s\n",
			item.Name, item.Enabled, item.Default, emptyIfBlank(item.Protocol), emptyIfBlank(item.AuthMode), emptyIfBlank(item.BaseURL))
	}
	fmt.Printf("\nTotal: %d\n", result.Total)
}

func renderProviderShowResult(result providerShowResult, outputOptions structuredOutputOptions) {
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("provider show", outputOptions.Envelope, result)
		return
	}
	fmt.Printf("Provider: %s\n", result.Name)
	fmt.Printf("  Enabled:              %t\n", result.Enabled)
	fmt.Printf("  Default:              %t\n", result.Default)
	fmt.Printf("  Protocol:             %s\n", emptyIfBlank(result.Protocol))
	fmt.Printf("  Auth mode:            %s\n", emptyIfBlank(result.AuthMode))
	fmt.Printf("  Base URL:             %s\n", emptyIfBlank(result.BaseURL))
	fmt.Printf("  API Path:             %s\n", emptyIfBlank(result.APIPath))
	fmt.Printf("  Forward URL:          %s\n", emptyIfBlank(result.ForwardURL))
	fmt.Printf("  Default model:        %s\n", emptyIfBlank(result.DefaultModel))
	fmt.Printf("  API key ref:          %s\n", emptyIfBlank(result.APIKeyRef))
	fmt.Printf("  Auth ref:             %s\n", emptyIfBlank(result.AuthRef))
	fmt.Printf("  Supported models:      %d\n", result.SupportedModelsCount)
	fmt.Printf("  Model capabilities:    %d\n", result.ModelCapabilitiesCount)
	fmt.Printf("  Model mappings:        %d\n", len(result.ModelMappings))
	fmt.Printf("  Support types:         %d\n", len(result.SupportTypes))
	fmt.Printf("  Headers configured:    %t\n", result.HeadersConfigured)
	fmt.Printf("  Header mappings:       %d\n", result.HeaderMappingsCount)
	fmt.Printf("  Header mapping rules:  %d\n", result.HeaderMappingRulesCount)
	fmt.Printf("  Max tokens limit:      %d\n", result.MaxTokensLimit)
	if result.SupportsMaxOutputTokens != nil {
		fmt.Printf("  Supports max output:   %t\n", *result.SupportsMaxOutputTokens)
	}
	if len(result.SupportTypes) > 0 {
		fmt.Println("  Support types detail:")
		for _, item := range result.SupportTypes {
			fmt.Printf("    - %s\n", item)
		}
	}
	if len(result.ModelMappings) > 0 {
		fmt.Println("  Model mappings:")
		keys := make([]string, 0, len(result.ModelMappings))
		for key := range result.ModelMappings {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Printf("    %s -> %s\n", key, result.ModelMappings[key])
		}
	}
	if len(result.SupportedModels) > 0 {
		fmt.Println("  Supported models:")
		for _, model := range result.SupportedModels {
			fmt.Printf("    - %s\n", model)
		}
		if result.SupportedModelsTruncated {
			fmt.Printf("    ... (%d shown)\n", result.SupportedModelsShownCount)
		}
	}
}

func renderProviderRemoveResult(result *config.ProviderDeleteResult, outputOptions structuredOutputOptions) {
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("provider remove", outputOptions.Envelope, result)
		return
	}
	fmt.Printf("Config: %s\n", emptyIfBlank(result.ConfigPath))
	fmt.Printf("Dry run: %t\n", result.DryRun)
	if len(result.Requested) > 0 {
		fmt.Printf("Requested: %s\n", strings.Join(result.Requested, ", "))
	}
	if len(result.Deleted) > 0 {
		fmt.Printf("Deleted: %s\n", strings.Join(result.Deleted, ", "))
	}
	if len(result.NotFound) > 0 {
		fmt.Printf("Not found: %s\n", strings.Join(result.NotFound, ", "))
	}
	for _, blocker := range result.Blocked {
		fmt.Printf("Blocked: %s (%s)\n", blocker.Provider, blocker.Code)
		if blocker.Message != "" {
			fmt.Printf("  %s\n", blocker.Message)
		}
		if len(blocker.References) > 0 {
			fmt.Printf("  References: %s\n", strings.Join(blocker.References, ", "))
		}
	}
	if len(result.RemovedGroupRefs) > 0 {
		fmt.Println("Removed group refs:")
		for _, ref := range result.RemovedGroupRefs {
			fmt.Printf("  - %s -> %s\n", ref.Group, ref.Provider)
		}
	}
	if len(result.RemovedGroups) > 0 {
		fmt.Printf("Removed groups: %s\n", strings.Join(result.RemovedGroups, ", "))
	}
	if len(result.ClearedDefaults) > 0 {
		fmt.Printf("Cleared defaults: %s\n", strings.Join(result.ClearedDefaults, ", "))
	}
	if result.ReplacementDefault != "" {
		fmt.Printf("Replacement default: %s\n", result.ReplacementDefault)
	}
	if len(result.AuthPruned) > 0 {
		fmt.Printf("Auth pruned: %s\n", strings.Join(result.AuthPruned, ", "))
	}
	if len(result.AuthSkipped) > 0 {
		fmt.Println("Auth skipped:")
		for _, skip := range result.AuthSkipped {
			if len(skip.Providers) > 0 {
				fmt.Printf("  - %s (%s): %s\n", skip.Ref, skip.Reason, strings.Join(skip.Providers, ", "))
				continue
			}
			fmt.Printf("  - %s (%s)\n", skip.Ref, skip.Reason)
		}
	}
}

func renderProviderEnableResult(result *config.ProviderEnableResult, outputOptions structuredOutputOptions) {
	commandName := "provider enable"
	if !result.Enabled {
		commandName = "provider disable"
	}
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput(commandName, outputOptions.Envelope, result)
		return
	}
	action := "Enabled"
	if !result.Enabled {
		action = "Disabled"
	}
	fmt.Printf("%s: %s\n", action, strings.Join(result.Updated, ", "))
	if len(result.NotFound) > 0 {
		fmt.Printf("Not found: %s\n", strings.Join(result.NotFound, ", "))
	}
}

func renderProviderDefaultResult(result *config.ProviderDefaultResult, outputOptions structuredOutputOptions) {
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("provider set-default", outputOptions.Envelope, result)
		return
	}
	fmt.Printf("Default provider: %s\n", result.DefaultProvider)
	if result.PreviousDefault != "" {
		fmt.Printf("Previous default: %s\n", result.PreviousDefault)
	}
}

func emptyIfBlank(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}
