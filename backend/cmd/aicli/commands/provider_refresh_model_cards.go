package commands

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelcard"
)

type providerRefreshModelCardsRequest struct {
	Names                []string
	All                  bool
	Protocol             string
	DryRun               bool
	ModelCardCatalogPath string
	NoUserCards          bool
	Strict               bool
}

type providerRefreshModelCardsResult struct {
	ConfigPath    string                                    `json:"config_path,omitempty"`
	DryRun        bool                                      `json:"dry_run"`
	Providers     []providerRefreshModelCardsProviderResult `json:"providers"`
	UpdatedCount  int                                       `json:"updated_count"`
	ChangedModels int                                       `json:"changed_models"`
	SkippedModels int                                       `json:"skipped_models"`
	Warnings      []providerLoginModelCardWarning           `json:"warnings,omitempty"`
}

type providerRefreshModelCardsProviderResult struct {
	Name          string                                 `json:"name"`
	Protocol      string                                 `json:"protocol,omitempty"`
	Updated       bool                                   `json:"updated"`
	ChangedModels int                                    `json:"changed_models"`
	SkippedModels int                                    `json:"skipped_models"`
	Models        []providerRefreshModelCardsModelResult `json:"models,omitempty"`
	Skipped       []providerLoginModelCardSkippedInfo    `json:"skipped,omitempty"`
	Applied       []providerLoginModelCardAppliedInfo    `json:"applied,omitempty"`
	Reason        string                                 `json:"reason,omitempty"`
}

type providerRefreshModelCardsModelResult struct {
	Model           string   `json:"model"`
	Changed         bool     `json:"changed"`
	CardIDs         []string `json:"card_ids,omitempty"`
	ChangedFields   []string `json:"changed_fields,omitempty"`
	BeforeContext   int      `json:"before_max_context_tokens,omitempty"`
	AfterContext    int      `json:"after_max_context_tokens,omitempty"`
	BeforeReasoning []string `json:"before_reasoning_efforts,omitempty"`
	AfterReasoning  []string `json:"after_reasoning_efforts,omitempty"`
}

func newProviderRefreshModelCardsCommand(configProvider func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "refresh-model-cards [name...]",
		Aliases: []string{"refresh-capabilities", "sync-model-cards"},
		Short:   "按 model cards 重刷 provider 的 model_capabilities",
		Long: strings.TrimSpace(`
按 model card catalog 重刷已配置 provider 的 model_capabilities。

与 login 不同：本命令以 model card 字段为权威来源，会覆盖已有但过期的 card 管理字段
（例如 max_context_tokens、reasoning_efforts），同时保留 card 未声明的本地字段。

未指定 provider 名称时，默认处理全部 providers（等价于 --all）。
`),
		Example: strings.TrimSpace(`
  aicli provider refresh-model-cards --dry-run
  aicli provider refresh-model-cards openai_codex
  aicli provider refresh-model-cards --protocol codex
  aicli provider refresh-model-cards --model-cards ./extra_cards.yaml --strict
`),
		Args: cobra.ArbitraryArgs,
		Run: func(cmd *cobra.Command, args []string) {
			HandleProviderRefreshModelCards(cmd, configProvider, args)
		},
	}
	cmd.Flags().Bool("all", false, "刷新全部 providers（未提供名称时默认启用）")
	cmd.Flags().Bool("dry-run", false, "只预览变更，不写配置")
	cmd.Flags().String("protocol", "", "按协议过滤（openai|anthropic|gemini|codex 等）")
	cmd.Flags().String("model-cards", "", "额外模型卡片 catalog 文件路径")
	cmd.Flags().Bool("no-user-cards", false, "忽略用户目录下的 model_cards.yaml")
	cmd.Flags().Bool("strict", false, "模型卡片加载或校验失败时中止")
	addProviderOutputFlags(cmd)
	return cmd
}

func HandleProviderRefreshModelCards(cmd *cobra.Command, configProvider func() *config.Config, names []string) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("provider refresh-model-cards", "json", err, nil)
	}
	req := providerRefreshModelCardsRequest{
		Names:                append([]string(nil), names...),
		All:                  boolFlag(cmd, "all"),
		Protocol:             stringFlag(cmd, "protocol"),
		DryRun:               boolFlag(cmd, "dry-run"),
		ModelCardCatalogPath: stringFlag(cmd, "model-cards"),
		NoUserCards:          boolFlag(cmd, "no-user-cards"),
		Strict:               boolFlag(cmd, "strict"),
	}
	executeCommand("provider refresh-model-cards", outputOptions, func() (*providerRefreshModelCardsResult, map[string]interface{}, error) {
		result, err := runProviderRefreshModelCardsCommand(providerCommandConfig(configProvider), req)
		if err != nil {
			return nil, providerResultDetails(result), err
		}
		return result, nil, nil
	}, renderProviderRefreshModelCardsResult)
}

func runProviderRefreshModelCardsCommand(cfg *config.Config, req providerRefreshModelCardsRequest) (*providerRefreshModelCardsResult, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is not loaded")
	}
	configPath, err := providerCommandConfigPath(cfg)
	if err != nil {
		return nil, err
	}

	selected, err := selectProvidersForModelCardRefresh(cfg, req)
	if err != nil {
		return nil, err
	}

	catalog, warnings, err := loadProviderRefreshModelCardCatalog(req, cfg)
	if err != nil {
		return &providerRefreshModelCardsResult{
			ConfigPath: configPath,
			DryRun:     req.DryRun,
			Warnings:   warnings,
		}, err
	}
	if catalog == nil {
		return nil, fmt.Errorf("model card catalog is disabled or empty; pass --model-cards or enable aicli.model_cards")
	}

	result := &providerRefreshModelCardsResult{
		ConfigPath: configPath,
		DryRun:     req.DryRun,
		Warnings:   warnings,
		Providers:  make([]providerRefreshModelCardsProviderResult, 0, len(selected)),
	}

	for _, item := range selected {
		providerResult := refreshProviderModelCapabilitiesFromCards(item.Name, item.Provider, catalog)
		if providerResult.summary.Updated && !req.DryRun {
			capabilities := cloneProviderLoginModelCapabilities(providerResult.nextCapabilities)
			if capabilities == nil {
				empty := map[string]config.ModelCapabilitySpec{}
				capabilities = empty
			}
			persisted, err := config.UpdateProviderConfig(configPath, config.ProviderConfigUpdate{
				Name:              item.Name,
				ModelCapabilities: &capabilities,
			})
			if err != nil {
				return result, fmt.Errorf("update provider %q: %w", item.Name, err)
			}
			if persisted != nil {
				applyProviderLoginConfigUpdate(cfg, item.Name, *persisted, false)
			} else if current, ok := cfg.Providers.Items[item.Name]; ok {
				current.ModelCapabilities = capabilities
				applyProviderLoginConfigUpdate(cfg, item.Name, current, false)
			}
		}

		summary := providerResult.summary
		result.Providers = append(result.Providers, summary)
		if summary.Updated {
			result.UpdatedCount++
		}
		result.ChangedModels += summary.ChangedModels
		result.SkippedModels += summary.SkippedModels
	}

	return result, nil
}

type providerRefreshSelection struct {
	Name     string
	Provider config.Provider
}

type providerRefreshBuildResult struct {
	summary          providerRefreshModelCardsProviderResult
	nextCapabilities map[string]config.ModelCapabilitySpec
}

func selectProvidersForModelCardRefresh(cfg *config.Config, req providerRefreshModelCardsRequest) ([]providerRefreshSelection, error) {
	if cfg == nil || cfg.Providers.Items == nil {
		return nil, fmt.Errorf("no providers configured")
	}

	protocolFilter := strings.ToLower(strings.TrimSpace(req.Protocol))
	names := make([]string, 0, len(req.Names))
	for _, name := range req.Names {
		name = strings.TrimSpace(name)
		if name != "" {
			names = append(names, name)
		}
	}

	useAll := req.All || len(names) == 0
	selected := make([]providerRefreshSelection, 0)
	seen := make(map[string]struct{})

	if useAll {
		allNames := make([]string, 0, len(cfg.Providers.Items))
		for name := range cfg.Providers.Items {
			allNames = append(allNames, name)
		}
		sort.Strings(allNames)
		for _, name := range allNames {
			provider := cfg.Providers.Items[name]
			if protocolFilter != "" && !strings.EqualFold(provider.GetProtocol(), protocolFilter) {
				continue
			}
			selected = append(selected, providerRefreshSelection{Name: name, Provider: provider})
		}
		if len(selected) == 0 {
			if protocolFilter != "" {
				return nil, fmt.Errorf("no providers matched protocol %q", protocolFilter)
			}
			return nil, fmt.Errorf("no providers configured")
		}
		return selected, nil
	}

	missing := make([]string, 0)
	for _, requested := range names {
		name, provider, ok := findProviderByName(cfg, requested)
		if !ok {
			missing = append(missing, requested)
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		if protocolFilter != "" && !strings.EqualFold(provider.GetProtocol(), protocolFilter) {
			continue
		}
		seen[name] = struct{}{}
		selected = append(selected, providerRefreshSelection{Name: name, Provider: provider})
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("provider not found: %s", strings.Join(missing, ", "))
	}
	if len(selected) == 0 {
		if protocolFilter != "" {
			return nil, fmt.Errorf("no selected providers matched protocol %q", protocolFilter)
		}
		return nil, fmt.Errorf("no providers selected")
	}
	return selected, nil
}

func loadProviderRefreshModelCardCatalog(req providerRefreshModelCardsRequest, cfg *config.Config) (*modelcard.Catalog, []providerLoginModelCardWarning, error) {
	modelCardsConfig := (*config.AICLIModelCardsConfig)(nil)
	if cfg != nil && cfg.AICLI != nil {
		modelCardsConfig = cfg.AICLI.ModelCards
	}
	if modelCardsConfig != nil && modelCardsConfig.Enabled != nil && !*modelCardsConfig.Enabled && strings.TrimSpace(req.ModelCardCatalogPath) == "" {
		return nil, nil, nil
	}

	strict := req.Strict
	if modelCardsConfig != nil && modelCardsConfig.Strict {
		strict = true
	}
	sources := []modelcard.Source{modelcard.BuiltinSource()}
	if modelCardsConfig != nil && strings.TrimSpace(modelCardsConfig.BuiltinPath) != "" {
		sources = append(sources, readProviderLoginModelCardFile(modelCardsConfig.BuiltinPath))
	}
	if !req.NoUserCards {
		userPath := "~/.aicli/model_cards.yaml"
		if modelCardsConfig != nil && strings.TrimSpace(modelCardsConfig.UserPath) != "" {
			userPath = modelCardsConfig.UserPath
		}
		if source, ok := readExistingProviderLoginModelCardFile(userPath); ok {
			sources = append(sources, source)
		}
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

func refreshProviderModelCapabilitiesFromCards(providerName string, provider config.Provider, catalog *modelcard.Catalog) providerRefreshBuildResult {
	protocol := provider.GetProtocol()
	loginProtocol := loginProtocolFromProvider(provider, provider.AuthMode)
	runtimeProtocol := runtimeProtocolForLoginProtocol(loginProtocol)
	if runtimeProtocol == "" {
		runtimeProtocol = protocol
	}

	ctx := modelcard.Context{
		ProviderName:    providerName,
		LoginProtocol:   loginProtocol,
		RuntimeProtocol: runtimeProtocol,
		BaseURL:         provider.BaseURL,
	}
	if catalog != nil {
		if template, ok := resolveProviderLoginProviderTemplate(catalog, runtimeProtocol, provider); ok {
			ctx.ProviderTemplate = strings.TrimSpace(template.ID)
		}
	}

	models := collectProviderModelsForCapabilityRefresh(provider)
	merged := cloneProviderLoginModelCapabilities(provider.ModelCapabilities)
	if merged == nil {
		merged = make(map[string]config.ModelCapabilitySpec)
	}

	summary := providerRefreshModelCardsProviderResult{
		Name:     providerName,
		Protocol: protocol,
		Models:   make([]providerRefreshModelCardsModelResult, 0),
		Skipped:  make([]providerLoginModelCardSkippedInfo, 0),
		Applied:  make([]providerLoginModelCardAppliedInfo, 0),
	}

	if catalog == nil {
		summary.Reason = "catalog_unavailable"
		return providerRefreshBuildResult{summary: summary, nextCapabilities: merged}
	}
	if len(models) == 0 {
		summary.Reason = "no_models"
		return providerRefreshBuildResult{summary: summary, nextCapabilities: merged}
	}

	changed := false
	for _, modelID := range models {
		existing := merged[modelID]
		cardCapability, appliedCards := catalog.Resolve(ctx, modelID)
		if len(appliedCards) == 0 || modelcardCapabilityIsOnlyFallback(appliedCards) || providerLoginModelCapabilityIsEmpty(cardCapability) {
			reason := "no_matching_card"
			switch {
			case len(appliedCards) == 0:
				reason = "no_matching_card"
			case modelcardCapabilityIsOnlyFallback(appliedCards):
				reason = "fallback_only"
			case providerLoginModelCapabilityIsEmpty(cardCapability):
				reason = "empty_card_capability"
			}
			summary.Skipped = append(summary.Skipped, providerLoginModelCardSkippedInfo{Model: modelID, Reason: reason})
			summary.SkippedModels++
			continue
		}

		next := modelcard.MergeCapabilityPreferCard(cardCapability, existing)
		cardIDs := make([]string, 0, len(appliedCards))
		for _, item := range appliedCards {
			cardIDs = append(cardIDs, item.CardID)
			summary.Applied = append(summary.Applied, providerLoginModelCardAppliedInfo{
				Model:  modelID,
				CardID: item.CardID,
				Fields: append([]string(nil), item.Fields...),
			})
		}

		modelResult := providerRefreshModelCardsModelResult{
			Model:           modelID,
			CardIDs:         cardIDs,
			BeforeContext:   existing.MaxContextTokens,
			AfterContext:    next.MaxContextTokens,
			BeforeReasoning: append([]string(nil), existing.ReasoningEfforts...),
			AfterReasoning:  append([]string(nil), next.ReasoningEfforts...),
		}
		if !modelcard.CapabilitySpecsEqual(existing, next) {
			modelResult.Changed = true
			modelResult.ChangedFields = diffModelCapabilityFields(existing, next)
			merged[modelID] = next
			changed = true
			summary.ChangedModels++
		}
		summary.Models = append(summary.Models, modelResult)
	}

	summary.Updated = changed
	if !changed && summary.SkippedModels == len(models) {
		summary.Reason = "no_card_matches"
	} else if !changed {
		summary.Reason = "already_up_to_date"
	}
	return providerRefreshBuildResult{summary: summary, nextCapabilities: merged}
}

func collectProviderModelsForCapabilityRefresh(provider config.Provider) []string {
	seen := make(map[string]struct{})
	models := make([]string, 0)

	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || value == "*" {
			return
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		models = append(models, value)
	}

	for _, model := range provider.SupportedModels {
		add(model)
	}
	if len(provider.ModelCapabilities) > 0 {
		keys := make([]string, 0, len(provider.ModelCapabilities))
		for model := range provider.ModelCapabilities {
			keys = append(keys, model)
		}
		sort.Strings(keys)
		for _, model := range keys {
			add(model)
		}
	}
	add(provider.DefaultModel)
	sort.Strings(models)
	return models
}

func modelcardCapabilityIsOnlyFallback(applied []modelcard.AppliedCard) bool {
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

func diffModelCapabilityFields(before, after config.ModelCapabilitySpec) []string {
	fields := make([]string, 0, 12)
	if !stringSlicesEqualExact(before.InputModalities, after.InputModalities) {
		fields = append(fields, "input_modalities")
	}
	if before.NativeTools.ImageGeneration != after.NativeTools.ImageGeneration {
		fields = append(fields, "native_tools.image_generation")
	}
	if before.NativeTools.ImagesGenerationsAPI != after.NativeTools.ImagesGenerationsAPI {
		fields = append(fields, "native_tools.images_generations_api")
	}
	if before.ReasoningModel != after.ReasoningModel {
		fields = append(fields, "reasoning_model")
	}
	if !stringSlicesEqualExact(before.ReasoningEfforts, after.ReasoningEfforts) {
		fields = append(fields, "reasoning_efforts")
	}
	if !intMapsEqualExact(before.ReasoningEffortBudgets, after.ReasoningEffortBudgets) {
		fields = append(fields, "reasoning_effort_budgets")
	}
	if strings.TrimSpace(before.DefaultReasoningEffort) != strings.TrimSpace(after.DefaultReasoningEffort) {
		fields = append(fields, "default_reasoning_effort")
	}
	if before.MaxContextTokens != after.MaxContextTokens {
		fields = append(fields, "max_context_tokens")
	}
	if before.MaxTokens != after.MaxTokens {
		fields = append(fields, "max_tokens")
	}
	if before.AutoCompactRatio != after.AutoCompactRatio {
		fields = append(fields, "auto_compact_ratio")
	}
	if before.AutoCompactTokenLimit != after.AutoCompactTokenLimit {
		fields = append(fields, "auto_compact_token_limit")
	}
	if strings.TrimSpace(before.AutoCompactMode) != strings.TrimSpace(after.AutoCompactMode) {
		fields = append(fields, "auto_compact_mode")
	}
	if before.SupportsRemoteCompact != after.SupportsRemoteCompact {
		fields = append(fields, "supports_remote_compact")
	}
	if strings.TrimSpace(before.CompactReasoningEffort) != strings.TrimSpace(after.CompactReasoningEffort) {
		fields = append(fields, "compact_reasoning_effort")
	}
	return fields
}

func stringSlicesEqualExact(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func intMapsEqualExact(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func renderProviderRefreshModelCardsResult(result *providerRefreshModelCardsResult, outputOptions structuredOutputOptions) {
	if result == nil {
		fmt.Println("no result")
		return
	}
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("provider refresh-model-cards", outputOptions.Envelope, result)
		return
	}

	mode := "updated"
	if result.DryRun {
		mode = "dry-run"
	}
	fmt.Printf("provider refresh-model-cards (%s)\n", mode)
	if result.ConfigPath != "" {
		fmt.Printf("config: %s\n", result.ConfigPath)
	}
	fmt.Printf("providers_updated: %d  models_changed: %d  models_skipped: %d\n",
		result.UpdatedCount, result.ChangedModels, result.SkippedModels)

	for _, provider := range result.Providers {
		status := "unchanged"
		if provider.Updated {
			if result.DryRun {
				status = "would_update"
			} else {
				status = "updated"
			}
		} else if provider.Reason != "" {
			status = provider.Reason
		}
		fmt.Printf("\n- %s [%s] protocol=%s changed_models=%d skipped_models=%d\n",
			provider.Name, status, emptyIfBlank(provider.Protocol), provider.ChangedModels, provider.SkippedModels)
		for _, model := range provider.Models {
			if !model.Changed {
				continue
			}
			fields := strings.Join(model.ChangedFields, ",")
			if fields == "" {
				fields = "-"
			}
			fmt.Printf("    * %s cards=%s fields=%s max_context_tokens=%d->%d\n",
				model.Model,
				strings.Join(model.CardIDs, ","),
				fields,
				model.BeforeContext,
				model.AfterContext,
			)
		}
		if len(provider.Skipped) > 0 {
			shown := 0
			for _, skipped := range provider.Skipped {
				if shown >= 5 {
					fmt.Printf("    ... %d more skipped\n", len(provider.Skipped)-shown)
					break
				}
				fmt.Printf("    - skipped %s (%s)\n", skipped.Model, skipped.Reason)
				shown++
			}
		}
	}

	if len(result.Warnings) > 0 {
		fmt.Printf("\nwarnings:\n")
		for _, warning := range result.Warnings {
			source := strings.TrimSpace(warning.Source)
			if source == "" {
				source = "model_cards"
			}
			fmt.Printf("  - [%s] %s: %s\n", warning.Code, source, warning.Message)
		}
	}
}
