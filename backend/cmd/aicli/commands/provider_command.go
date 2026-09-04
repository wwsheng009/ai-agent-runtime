package commands

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
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
		Long: `列出、查看、启停、删除 provider，并维护默认 provider 配置。

常用子命令：
  aicli provider list
  aicli provider show <name> [--models]
  aicli provider set-default <name>
  aicli provider enable|disable <name...>
  aicli provider remove [name...]
  aicli provider proxy set <name> --http <url> [--https <url>] [--no-proxy <list>]
  aicli provider proxy remove <name>

首次配置 provider 请用 aicli login；连通性诊断用 aicli doctor provider。
更多说明见 docs/aicli/quickstart.md 与 docs/aicli/faq.md。`,
		Example: `  aicli provider list
  aicli provider list --enabled --json
  aicli provider show openai --models
  aicli provider set-default openai
  aicli provider enable openai
  aicli provider disable old-provider
  aicli provider remove old-provider --yes --cascade
  aicli provider proxy set openai --http http://127.0.0.1:7890
  aicli provider proxy remove openai`,
	}
	cmd.AddCommand(newProviderListCommand(configProvider))
	cmd.AddCommand(newProviderShowCommand(configProvider))
	cmd.AddCommand(newProviderRemoveCommand(configProvider))
	cmd.AddCommand(newProviderEnableCommand(configProvider, true))
	cmd.AddCommand(newProviderEnableCommand(configProvider, false))
	cmd.AddCommand(newProviderSetDefaultCommand(configProvider))
	cmd.AddCommand(newProviderRefreshModelCardsCommand(configProvider))
	cmd.AddCommand(newProviderProxyCommand(configProvider))
	return cmd
}

func newProviderProxyCommand(configProvider func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "proxy",
		Aliases: []string{"px"},
		Short:   "配置 provider 的代理（proxy）",
		Long: `为指定 provider 新增、更新或删除代理（proxy）配置。

子命令：
  aicli provider proxy set <name> --http <url> [--https <url>] [--no-proxy <list>] [--disable]
  aicli provider proxy remove <name>
  aicli provider proxy global set --http <url> [--https <url>] [--no-proxy <list>] [--disable]
  aicli provider proxy global remove

说明：
  - 代理地址支持 http://、https://、socks5://，设置时会校验格式。
  - 配置写入 providers.items.<name>.proxy，与全局 providers.proxy 合并时 provider 级优先。
  - global 系列子命令管理全局 providers.proxy，对所有未配置自身代理的 provider 生效。
  - 只传部分字段时保留原有其他字段；--disable 可保留配置但停用代理。
  - remove 只删除该 provider 的 proxy，不影响全局 providers.proxy；global remove 删除全局代理。`,
		Example: `  aicli provider proxy set openai --http http://127.0.0.1:7890
  aicli provider proxy set openai --http http://127.0.0.1:7890 --https http://127.0.0.1:7890 --no-proxy localhost,api.openai.com
  aicli provider proxy set openai --disable
  aicli provider proxy remove openai
  aicli provider proxy global set --http http://127.0.0.1:7890
  aicli provider proxy global remove`,
	}
	cmd.AddCommand(newProviderProxySetCommand(configProvider))
	cmd.AddCommand(newProviderProxyRemoveCommand(configProvider))
	cmd.AddCommand(newProviderProxyGlobalCommand(configProvider))
	return cmd
}

func newProviderProxyGlobalCommand(configProvider func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "global",
		Short: "配置全局代理（providers.proxy）",
		Long: `管理全局代理 providers.proxy，对所有未配置自身代理的 provider 生效。

子命令：
  aicli provider proxy global set --http <url> [--https <url>] [--no-proxy <list>] [--disable]
  aicli provider proxy global remove

说明：
  - 与 provider 级代理合并时，provider 级字段优先。
  - 只传部分字段时保留原有其他字段；--disable 可保留配置但停用代理。`,
		Example: `  aicli provider proxy global set --http http://127.0.0.1:7890
  aicli provider proxy global set --http http://127.0.0.1:7890 --https http://127.0.0.1:7890 --no-proxy localhost
  aicli provider proxy global set --disable
  aicli provider proxy global remove`,
	}
	cmd.AddCommand(newProviderProxyGlobalSetCommand(configProvider))
	cmd.AddCommand(newProviderProxyGlobalRemoveCommand(configProvider))
	return cmd
}

func newProviderProxyGlobalSetCommand(configProvider func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "set",
		Aliases: []string{"add", "update"},
		Short:   "新增或更新全局 proxy 配置",
		Run: func(cmd *cobra.Command, args []string) {
			HandleProviderProxyGlobalSet(cmd, configProvider)
		},
	}
	cmd.Flags().String("http", "", "http 代理地址，如 http://127.0.0.1:7890")
	cmd.Flags().String("https", "", "https 代理地址，如 http://127.0.0.1:7890")
	cmd.Flags().String("no-proxy", "", "不走代理的地址列表（逗号分隔）")
	cmd.Flags().Bool("enable", false, "启用该 proxy（首次设置时的默认行为）")
	cmd.Flags().Bool("disable", false, "停用该 proxy（保留配置）")
	addProviderOutputFlags(cmd)
	return cmd
}

func newProviderProxyGlobalRemoveCommand(configProvider func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "remove",
		Aliases: []string{"rm", "clear", "delete"},
		Short:   "删除全局 proxy 配置",
		Run: func(cmd *cobra.Command, args []string) {
			HandleProviderProxyGlobalRemove(cmd, configProvider)
		},
	}
	addProviderOutputFlags(cmd)
	return cmd
}

func newProviderProxySetCommand(configProvider func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "set <name>",
		Aliases: []string{"add", "update"},
		Short:   "新增或更新 provider 的 proxy 配置",
		Args:    cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			HandleProviderProxySet(cmd, configProvider, args[0])
		},
	}
	cmd.Flags().String("http", "", "http 代理地址，如 http://127.0.0.1:7890")
	cmd.Flags().String("https", "", "https 代理地址，如 http://127.0.0.1:7890")
	cmd.Flags().String("no-proxy", "", "不走代理的地址列表（逗号分隔）")
	cmd.Flags().Bool("enable", false, "启用该 proxy（首次设置时的默认行为）")
	cmd.Flags().Bool("disable", false, "停用该 proxy（保留配置）")
	addProviderOutputFlags(cmd)
	return cmd
}

func newProviderProxyRemoveCommand(configProvider func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm", "clear", "delete"},
		Short:   "删除 provider 的 proxy 配置",
		Args:    cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			HandleProviderProxyRemove(cmd, configProvider, args[0])
		},
	}
	addProviderOutputFlags(cmd)
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

func HandleProviderProxySet(cmd *cobra.Command, configProvider func() *config.Config, name string) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("provider proxy set", "json", err, nil)
	}
	if boolFlag(cmd, "enable") && boolFlag(cmd, "disable") {
		exitCommandError("provider proxy set", outputOptions.Format, fmt.Errorf("--enable 和 --disable 不能同时使用"), nil)
	}
	update := config.ProviderProxyUpdate{
		Name:    name,
		HTTP:    changedStringFlag(cmd, "http"),
		HTTPS:   changedStringFlag(cmd, "https"),
		NoProxy: changedStringFlag(cmd, "no-proxy"),
	}
	if boolFlag(cmd, "enable") {
		enabled := true
		update.Enabled = &enabled
	}
	if boolFlag(cmd, "disable") {
		disabled := false
		update.Enabled = &disabled
	}
	executeCommand("provider proxy set", outputOptions, func() (*config.ProviderProxyResult, map[string]interface{}, error) {
		cfg := providerCommandConfig(configProvider)
		if cfg == nil {
			return nil, nil, fmt.Errorf("config is not loaded")
		}
		result, err := runProviderProxySetCommand(cfg, name, update)
		if err != nil {
			return result, providerResultDetails(result), err
		}
		return result, nil, nil
	}, renderProviderProxyResult)
}

func HandleProviderProxyRemove(cmd *cobra.Command, configProvider func() *config.Config, name string) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("provider proxy remove", "json", err, nil)
	}
	executeCommand("provider proxy remove", outputOptions, func() (*config.ProviderProxyResult, map[string]interface{}, error) {
		cfg := providerCommandConfig(configProvider)
		if cfg == nil {
			return nil, nil, fmt.Errorf("config is not loaded")
		}
		result, err := runProviderProxyRemoveCommand(cfg, name)
		if err != nil {
			return result, providerResultDetails(result), err
		}
		return result, nil, nil
	}, renderProviderProxyResult)
}

func HandleProviderProxyGlobalSet(cmd *cobra.Command, configProvider func() *config.Config) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("provider proxy global set", "json", err, nil)
	}
	if boolFlag(cmd, "enable") && boolFlag(cmd, "disable") {
		exitCommandError("provider proxy global set", outputOptions.Format, fmt.Errorf("--enable 和 --disable 不能同时使用"), nil)
	}
	update := config.GlobalProxyUpdate{
		HTTP:    changedStringFlag(cmd, "http"),
		HTTPS:   changedStringFlag(cmd, "https"),
		NoProxy: changedStringFlag(cmd, "no-proxy"),
	}
	if boolFlag(cmd, "enable") {
		enabled := true
		update.Enabled = &enabled
	}
	if boolFlag(cmd, "disable") {
		disabled := false
		update.Enabled = &disabled
	}
	executeCommand("provider proxy global set", outputOptions, func() (*config.GlobalProxyResult, map[string]interface{}, error) {
		cfg := providerCommandConfig(configProvider)
		if cfg == nil {
			return nil, nil, fmt.Errorf("config is not loaded")
		}
		result, err := runProviderProxyGlobalSetCommand(cfg, update)
		if err != nil {
			return result, providerResultDetails(result), err
		}
		return result, nil, nil
	}, renderGlobalProxyResult)
}

func HandleProviderProxyGlobalRemove(cmd *cobra.Command, configProvider func() *config.Config) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("provider proxy global remove", "json", err, nil)
	}
	executeCommand("provider proxy global remove", outputOptions, func() (*config.GlobalProxyResult, map[string]interface{}, error) {
		cfg := providerCommandConfig(configProvider)
		if cfg == nil {
			return nil, nil, fmt.Errorf("config is not loaded")
		}
		result, err := runProviderProxyGlobalRemoveCommand(cfg)
		if err != nil {
			return result, providerResultDetails(result), err
		}
		return result, nil, nil
	}, renderGlobalProxyResult)
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
		HeadersConfigured:         len(config.EffectiveProviderHeaders(cfg.Providers.Headers, provider.Headers)) > 0,
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

func runProviderProxySetCommand(cfg *config.Config, name string, update config.ProviderProxyUpdate) (*config.ProviderProxyResult, error) {
	configPath, err := providerCommandConfigPath(cfg)
	if err != nil {
		return nil, err
	}
	return config.SetProviderProxyConfig(configPath, name, update)
}

func runProviderProxyRemoveCommand(cfg *config.Config, name string) (*config.ProviderProxyResult, error) {
	configPath, err := providerCommandConfigPath(cfg)
	if err != nil {
		return nil, err
	}
	return config.RemoveProviderProxyConfig(configPath, name)
}

func runProviderProxyGlobalSetCommand(cfg *config.Config, update config.GlobalProxyUpdate) (*config.GlobalProxyResult, error) {
	configPath, err := providerCommandConfigPath(cfg)
	if err != nil {
		return nil, err
	}
	return config.SetGlobalProxyConfig(configPath, update)
}

func runProviderProxyGlobalRemoveCommand(cfg *config.Config) (*config.GlobalProxyResult, error) {
	configPath, err := providerCommandConfigPath(cfg)
	if err != nil {
		return nil, err
	}
	return config.RemoveGlobalProxyConfig(configPath)
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
	if result.Proxy != nil && !result.Proxy.IsEmpty() {
		fmt.Printf("  Proxy:                 %s\n", formatProviderProxyDisplay(result.Proxy))
	} else {
		fmt.Printf("  Proxy:                 (not set)\n")
	}
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

func renderProviderProxyResult(result *config.ProviderProxyResult, outputOptions structuredOutputOptions) {
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("provider proxy", outputOptions.Envelope, result)
		return
	}
	if result.Removed {
		fmt.Printf("Removed proxy for provider: %s\n", result.Name)
		if result.ConfigPath != "" {
			fmt.Printf("Config: %s\n", result.ConfigPath)
		}
		return
	}
	fmt.Printf("Provider: %s\n", result.Name)
	if result.Proxy == nil || result.Proxy.IsEmpty() {
		fmt.Println("Proxy: (not set)")
	} else {
		fmt.Printf("Proxy: %s\n", formatProviderProxyDisplay(result.Proxy))
	}
	if result.ConfigPath != "" {
		fmt.Printf("Config: %s\n", result.ConfigPath)
	}
}

// renderGlobalProxyResult renders providers.proxy set/remove results.
func renderGlobalProxyResult(result *config.GlobalProxyResult, outputOptions structuredOutputOptions) {
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("provider proxy global", outputOptions.Envelope, result)
		return
	}
	if result.Removed {
		fmt.Println("Removed global proxy (providers.proxy)")
	} else {
		fmt.Printf("Global proxy: %s\n", formatProviderProxyDisplay(result.Proxy))
	}
	if result.ConfigPath != "" {
		fmt.Printf("Config: %s\n", result.ConfigPath)
	}
}

// formatProviderProxyDisplay renders a proxy config with passwords masked,
// e.g. "enabled (http=http://127.0.0.1:7890, https=http://user:****@proxy:8080)".
func formatProviderProxyDisplay(proxy *config.ProxyConfig) string {
	if proxy == nil || proxy.IsEmpty() {
		return "(not set)"
	}
	state := "disabled"
	if proxy.Enabled {
		state = "enabled"
	}
	var parts []string
	if strings.TrimSpace(proxy.HTTP) != "" {
		parts = append(parts, "http="+maskProxyURLForDisplay(proxy.HTTP))
	}
	if strings.TrimSpace(proxy.HTTPS) != "" {
		parts = append(parts, "https="+maskProxyURLForDisplay(proxy.HTTPS))
	}
	if strings.TrimSpace(proxy.NoProxy) != "" {
		parts = append(parts, "no_proxy="+strings.TrimSpace(proxy.NoProxy))
	}
	if len(parts) == 0 {
		return state
	}
	return state + " (" + strings.Join(parts, ", ") + ")"
}

func maskProxyURLForDisplay(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	if parsed.User != nil {
		if _, has := parsed.User.Password(); has {
			parsed.User = url.UserPassword(parsed.User.Username(), "****")
		}
	}
	return parsed.String()
}

// changedStringFlag returns a pointer to the flag value only when the flag was
// explicitly provided, so unset flags do not overwrite existing config values.
func changedStringFlag(cmd *cobra.Command, name string) *string {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	value := strings.TrimSpace(stringFlag(cmd, name))
	return &value
}

func emptyIfBlank(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}
