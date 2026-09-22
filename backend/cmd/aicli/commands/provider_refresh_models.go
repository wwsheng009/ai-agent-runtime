package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelcard"
	"github.com/wwsheng009/ai-agent-runtime/internal/providerops"
)

// ---------------------------------------------------------------------------
// aicli provider refresh-models：拉取 /models 端点 → 按类别解析元数据（openrouter
// 等嵌套形状）→ 合并进已有 model_capabilities（端点优先）→ 可选落盘。
//
// 与 refresh-model-cards 的分工：
//   - refresh-model-cards：以 model card 为权威来源（离线，不访问网络）；
//   - refresh-models：    以 /models 端点元数据为权威来源（在线），覆盖端点能
//     声明的字段（input_modalities / reasoning_* / max_context_tokens /
//     max_tokens），保留端点不声明的本地字段。
//
// 与 login 的差异：login 是「已有配置优先」，不会用端点元数据覆盖已保存值；
// 本命令是「端点优先」，用于修复历史遗留的过期能力值（例如兜底 model card
// 写下的 input_modalities=[text] 遮蔽了端点的 [text,image,video]）。
// ---------------------------------------------------------------------------

type providerRefreshModelsRequest struct {
	Names      []string
	All        bool
	Protocol   string
	DryRun     bool
	ModelsPath string
	Timeout    time.Duration
}

type providerRefreshModelsResult struct {
	ConfigPath    string                                `json:"config_path,omitempty"`
	DryRun        bool                                  `json:"dry_run"`
	Providers     []providerRefreshModelsProviderResult `json:"providers"`
	UpdatedCount  int                                   `json:"updated_count"`
	ChangedModels int                                   `json:"changed_models"`
	AddedModels   int                                   `json:"added_models"`
	FailedCount   int                                   `json:"failed_count"`
}

type providerRefreshModelsProviderResult struct {
	Name          string                             `json:"name"`
	Protocol      string                             `json:"protocol,omitempty"`
	Endpoint      string                             `json:"endpoint,omitempty"`
	Category      string                             `json:"category,omitempty"`
	ModelCount    int                                `json:"model_count"`
	Updated       bool                               `json:"updated"`
	ChangedModels int                                `json:"changed_models"`
	AddedModels   int                                `json:"added_models"`
	Models        []providerRefreshModelsModelResult `json:"models,omitempty"`
	Reason        string                             `json:"reason,omitempty"`
}

type providerRefreshModelsModelResult struct {
	Model            string   `json:"model"`
	Changed          bool     `json:"changed"`
	Added            bool     `json:"added,omitempty"`
	ChangedFields    []string `json:"changed_fields,omitempty"`
	BeforeModalities []string `json:"before_input_modalities,omitempty"`
	AfterModalities  []string `json:"after_input_modalities,omitempty"`
	BeforeReasoning  []string `json:"before_reasoning_efforts,omitempty"`
	AfterReasoning   []string `json:"after_reasoning_efforts,omitempty"`
	BeforeContext    int      `json:"before_max_context_tokens,omitempty"`
	AfterContext     int      `json:"after_max_context_tokens,omitempty"`
	BeforeMaxTokens  int      `json:"before_max_tokens,omitempty"`
	AfterMaxTokens   int      `json:"after_max_tokens,omitempty"`
}

func newProviderRefreshModelsCommand(configProvider func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "refresh-models [name...]",
		Aliases: []string{"sync-models", "refresh-model-metadata"},
		Short:   "按 /models 端点元数据重刷 provider 的 model_capabilities",
		Long: strings.TrimSpace(`
拉取 provider 的 /models 端点，按响应的解析类别（openrouter 等嵌套形状 / generic
扁平形状）解析模型元数据，并合并进已配置的 model_capabilities。

合并语义（端点优先）：端点能声明的字段以端点为权威，可覆盖配置里由兜底 model
card 或旧端点写下的过期值；端点未声明的本地字段（native_tools、auto_compact_*、
replay_reasoning_content、compact_reasoning_effort 等）保持不变。端点新出现、
本地还没有的模型会被补充进来。

与 aicli login 的差异：login 以已有配置优先，不会用端点元数据覆盖已保存值。
本命令用于显式刷新，例如修复 input_modalities=[text] 遮蔽端点多模态声明的情况。

未指定 provider 名称时默认处理全部 providers（等价于 --all）。
`),
		Example: strings.TrimSpace(`
  aicli provider refresh-models openrouter --dry-run
  aicli provider refresh-models openrouter
  aicli provider refresh-models --all --protocol openai --json
  aicli provider refresh-models openrouter --models-path /api/v1/models
`),
		Args: cobra.ArbitraryArgs,
		Run: func(cmd *cobra.Command, args []string) {
			HandleProviderRefreshModels(cmd, configProvider, args)
		},
	}
	cmd.Flags().Bool("all", false, "刷新全部 providers（未提供名称时默认启用）")
	cmd.Flags().Bool("dry-run", false, "只预览变更，不写配置")
	cmd.Flags().String("protocol", "", "按协议过滤（openai|anthropic|gemini|codex 等）")
	cmd.Flags().String("models-path", "", "覆盖 provider 的 models 端点路径")
	cmd.Flags().Int("timeout", 0, "单次 /models 请求超时（秒），0 表示使用 provider 配置")
	addProviderOutputFlags(cmd)
	return cmd
}

func HandleProviderRefreshModels(cmd *cobra.Command, configProvider func() *config.Config, names []string) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("provider refresh-models", "json", err, nil)
	}
	req := providerRefreshModelsRequest{
		Names:      append([]string(nil), names...),
		All:        boolFlag(cmd, "all"),
		Protocol:   stringFlag(cmd, "protocol"),
		DryRun:     boolFlag(cmd, "dry-run"),
		ModelsPath: stringFlag(cmd, "models-path"),
		Timeout:    time.Duration(intFlag(cmd, "timeout")) * time.Second,
	}
	executeCommand("provider refresh-models", outputOptions, func() (*providerRefreshModelsResult, map[string]interface{}, error) {
		result, err := runProviderRefreshModelsCommand(providerCommandConfig(configProvider), req)
		if err != nil {
			return nil, providerResultDetails(result), err
		}
		return result, nil, nil
	}, renderProviderRefreshModelsResult)
}

func runProviderRefreshModelsCommand(cfg *config.Config, req providerRefreshModelsRequest) (*providerRefreshModelsResult, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is not loaded")
	}
	configPath, err := providerCommandConfigPath(cfg)
	if err != nil {
		return nil, err
	}
	selected, err := selectProvidersForRefresh(cfg, req.Names, req.All, req.Protocol)
	if err != nil {
		return nil, err
	}

	result := &providerRefreshModelsResult{
		ConfigPath: configPath,
		DryRun:     req.DryRun,
		Providers:  make([]providerRefreshModelsProviderResult, 0, len(selected)),
	}

	for _, item := range selected {
		loginProtocol := loginProtocolFromProvider(item.Provider, item.Provider.AuthMode)
		fetch, fetchErr := providerops.FetchModels(context.Background(), providerops.FetchModelsRequest{
			Config:        cfg,
			ProviderName:  item.Name,
			Provider:      item.Provider,
			LoginProtocol: loginProtocol,
			ModelsPath:    req.ModelsPath,
			Timeout:       req.Timeout,
		})
		if fetchErr != nil {
			// 单个 provider 失败不阻断其余 provider（--all 场景），记录原因继续。
			result.FailedCount++
			result.Providers = append(result.Providers, providerRefreshModelsProviderResult{
				Name:     item.Name,
				Protocol: item.Provider.GetProtocol(),
				Reason:   "fetch_failed: " + fetchErr.Error(),
			})
			continue
		}

		build := refreshProviderModelCapabilitiesFromEndpoint(item.Name, loginProtocol, item.Provider, fetch.Models)
		build.summary.Endpoint = fetch.Endpoint
		build.summary.Category = providerops.ModelListCategoryName(fetch.Category)
		if build.summary.Updated && !req.DryRun {
			capabilities := cloneProviderLoginModelCapabilities(build.nextCapabilities)
			if capabilities == nil {
				capabilities = map[string]config.ModelCapabilitySpec{}
			}
			verifiedAt := strings.TrimSpace(fetch.VerifiedAt)
			persisted, persistErr := config.UpdateProviderConfig(configPath, config.ProviderConfigUpdate{
				Name:              item.Name,
				ModelCapabilities: &capabilities,
				ModelsVerifiedAt:  &verifiedAt,
			})
			if persistErr != nil {
				return result, fmt.Errorf("update provider %q: %w", item.Name, persistErr)
			}
			if persisted != nil {
				applyProviderLoginConfigUpdate(cfg, item.Name, *persisted, false)
			} else if current, ok := cfg.Providers.Items[item.Name]; ok {
				current.ModelCapabilities = capabilities
				current.ModelsVerifiedAt = verifiedAt
				applyProviderLoginConfigUpdate(cfg, item.Name, current, false)
			}
		}

		result.Providers = append(result.Providers, build.summary)
		if build.summary.Updated {
			result.UpdatedCount++
		}
		result.ChangedModels += build.summary.ChangedModels
		result.AddedModels += build.summary.AddedModels
	}

	return result, nil
}

type providerRefreshModelsBuildResult struct {
	summary          providerRefreshModelsProviderResult
	nextCapabilities map[string]config.ModelCapabilitySpec
}

// refreshProviderModelCapabilitiesFromEndpoint 是命令的核心合并逻辑：以已有
// capabilities 为底，用端点元数据（端点优先）合并每个模型，返回变更摘要与
// 下一版 capabilities。独立成函数便于单测，不依赖网络与配置文件。
func refreshProviderModelCapabilitiesFromEndpoint(
	providerName, loginProtocol string,
	provider config.Provider,
	models []providerModelInfo,
) providerRefreshModelsBuildResult {
	merged := cloneProviderLoginModelCapabilities(provider.ModelCapabilities)
	if merged == nil {
		merged = make(map[string]config.ModelCapabilitySpec)
	}
	summary := providerRefreshModelsProviderResult{
		Name:       providerName,
		Protocol:   provider.GetProtocol(),
		ModelCount: len(models),
		Models:     make([]providerRefreshModelsModelResult, 0, len(models)),
	}

	changed := false
	for _, model := range models {
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		existing, existed := merged[modelID]
		remote := providerLoginModelCapabilitySpec(model)
		next := modelcard.MergeCapabilityPreferRemote(remote, existing)
		if providerLoginModelCapabilityIsEmpty(next) {
			// 端点与本地都没有任何可写字段：不制造空条目。
			continue
		}

		modelResult := providerRefreshModelsModelResult{
			Model:            modelID,
			Added:            !existed,
			BeforeModalities: append([]string(nil), existing.InputModalities...),
			AfterModalities:  append([]string(nil), next.InputModalities...),
			BeforeReasoning:  append([]string(nil), existing.ReasoningEfforts...),
			AfterReasoning:   append([]string(nil), next.ReasoningEfforts...),
			BeforeContext:    existing.MaxContextTokens,
			AfterContext:     next.MaxContextTokens,
			BeforeMaxTokens:  existing.MaxTokens,
			AfterMaxTokens:   next.MaxTokens,
		}
		if !modelcard.CapabilitySpecsEqual(existing, next) {
			modelResult.Changed = true
			modelResult.ChangedFields = diffModelCapabilityFields(existing, next)
			merged[modelID] = next
			changed = true
			summary.ChangedModels++
			if !existed {
				summary.AddedModels++
			}
		}
		summary.Models = append(summary.Models, modelResult)
	}

	summary.Updated = changed
	if !changed {
		summary.Reason = "already_up_to_date"
	}
	return providerRefreshModelsBuildResult{summary: summary, nextCapabilities: merged}
}

func renderProviderRefreshModelsResult(result *providerRefreshModelsResult, outputOptions structuredOutputOptions) {
	if result == nil {
		fmt.Println("no result")
		return
	}
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("provider refresh-models", outputOptions.Envelope, result)
		return
	}

	mode := "updated"
	if result.DryRun {
		mode = "dry-run"
	}
	fmt.Printf("provider refresh-models (%s)\n", mode)
	if result.ConfigPath != "" {
		fmt.Printf("config: %s\n", result.ConfigPath)
	}
	fmt.Printf("providers_updated: %d  models_changed: %d  models_added: %d  providers_failed: %d\n",
		result.UpdatedCount, result.ChangedModels, result.AddedModels, result.FailedCount)

	for _, provider := range result.Providers {
		status := "unchanged"
		switch {
		case provider.Reason != "" && !provider.Updated:
			status = provider.Reason
		case provider.Updated && result.DryRun:
			status = "would_update"
		case provider.Updated:
			status = "updated"
		}
		fmt.Printf("\n- %s [%s] protocol=%s category=%s models=%d changed=%d added=%d\n",
			provider.Name,
			status,
			emptyIfBlank(provider.Protocol),
			emptyIfBlank(provider.Category),
			provider.ModelCount,
			provider.ChangedModels,
			provider.AddedModels,
		)
		for _, model := range provider.Models {
			if !model.Changed {
				continue
			}
			fields := strings.Join(model.ChangedFields, ",")
			if fields == "" {
				fields = "-"
			}
			fmt.Printf("    * %s fields=%s modalities=%s->%s reasoning=%s->%s max_context_tokens=%d->%d max_tokens=%d->%d\n",
				model.Model,
				fields,
				strings.Join(model.BeforeModalities, "|"),
				strings.Join(model.AfterModalities, "|"),
				strings.Join(model.BeforeReasoning, "|"),
				strings.Join(model.AfterReasoning, "|"),
				model.BeforeContext,
				model.AfterContext,
				model.BeforeMaxTokens,
				model.AfterMaxTokens,
			)
		}
	}
}
