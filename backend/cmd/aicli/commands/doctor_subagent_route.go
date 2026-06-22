package commands

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelrouting"
)

type doctorSubagentRouteOptions struct {
	Role                  string
	Goal                  string
	Difficulty            string
	DifficultyRationale   string
	Provider              string
	Model                 string
	ReasoningEffort       string
	BudgetTokens          int
	Timeout               time.Duration
	ReadOnly              bool
	ParentProvider        string
	ParentModel           string
	ParentReasoningEffort string
	OutputOptions         structuredOutputOptions
}

type doctorSubagentRouteReport struct {
	ConfigPath     string                             `json:"config_path,omitempty"`
	RoutingEnabled bool                               `json:"routing_enabled"`
	Request        doctorSubagentRouteRequestReport   `json:"request"`
	Parent         doctorSubagentRouteParentReport    `json:"parent"`
	Decision       doctorSubagentRouteDecisionReport  `json:"decision"`
	Warnings       []string                           `json:"warnings,omitempty"`
	Providers      []doctorSubagentRouteProviderBrief `json:"providers,omitempty"`
}

type doctorSubagentRouteRequestReport struct {
	Role                string `json:"role,omitempty"`
	Goal                string `json:"goal,omitempty"`
	Difficulty          string `json:"difficulty,omitempty"`
	DifficultyRationale string `json:"difficulty_rationale,omitempty"`
	Provider            string `json:"provider,omitempty"`
	Model               string `json:"model,omitempty"`
	ReasoningEffort     string `json:"reasoning_effort,omitempty"`
	BudgetTokens        int    `json:"budget_tokens,omitempty"`
	Timeout             string `json:"timeout,omitempty"`
	ReadOnly            bool   `json:"read_only,omitempty"`
}

type doctorSubagentRouteParentReport struct {
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	MaxTokens       int    `json:"max_tokens,omitempty"`
	Timeout         string `json:"timeout,omitempty"`
}

type doctorSubagentRouteDecisionReport struct {
	Difficulty          string   `json:"difficulty,omitempty"`
	DifficultySource    string   `json:"difficulty_source,omitempty"`
	DifficultyRationale string   `json:"difficulty_rationale,omitempty"`
	Provider            string   `json:"provider,omitempty"`
	Model               string   `json:"model,omitempty"`
	ReasoningEffort     string   `json:"reasoning_effort,omitempty"`
	MaxTokens           int      `json:"max_tokens,omitempty"`
	Timeout             string   `json:"timeout,omitempty"`
	Source              string   `json:"source,omitempty"`
	Warnings            []string `json:"warnings,omitempty"`
	FallbackUsed        bool     `json:"fallback_used,omitempty"`
	FallbackReason      string   `json:"fallback_reason,omitempty"`
}

type doctorSubagentRouteProviderBrief struct {
	Name           string   `json:"name"`
	DefaultModel   string   `json:"default_model,omitempty"`
	Supported      []string `json:"supported_models,omitempty"`
	CapabilityKeys []string `json:"capability_keys,omitempty"`
}

func newDoctorSubagentRouteCommand(getCfg func() *config.Config) *cobra.Command {
	var opts doctorSubagentRouteOptions
	cmd := &cobra.Command{
		Use:   "subagent-route",
		Short: "预览子 agent 难度路由结果，不调用模型",
		Run: func(cmd *cobra.Command, args []string) {
			outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
			if err != nil {
				exitCommandError("doctor subagent-route", "json", err, nil)
			}
			opts.OutputOptions = outputOptions
			report, details, err := runDoctorSubagentRoute(getCfg(), opts)
			if err != nil {
				exitCommandError("doctor subagent-route", outputOptions.Format, err, details)
			}
			renderDoctorSubagentRouteReport(report, outputOptions)
		},
	}
	cmd.Flags().StringVar(&opts.Role, "role", "", "子任务 role，例如 researcher、writer、verifier")
	cmd.Flags().StringVar(&opts.Goal, "goal", "", "子任务 goal，用于难度缺失时的启发式推断")
	cmd.Flags().StringVar(&opts.Difficulty, "difficulty", "", "子任务难度 easy|normal|hard|expert 或别名")
	cmd.Flags().StringVar(&opts.DifficultyRationale, "difficulty-rationale", "", "难度评级理由")
	cmd.Flags().StringVar(&opts.Provider, "provider", "", "显式 provider override hint")
	cmd.Flags().StringVar(&opts.Model, "model", "", "显式 model override hint")
	cmd.Flags().StringVar(&opts.ReasoningEffort, "reasoning-effort", "", "显式 reasoning_effort hint")
	cmd.Flags().IntVar(&opts.BudgetTokens, "budget-tokens", 0, "子任务 token 预算")
	cmd.Flags().DurationVar(&opts.Timeout, "timeout", 0, "子任务 timeout，例如 300s")
	cmd.Flags().BoolVar(&opts.ReadOnly, "read-only", true, "按只读子任务预览")
	cmd.Flags().StringVar(&opts.ParentProvider, "parent-provider", "", "parent provider，留空使用默认 provider")
	cmd.Flags().StringVar(&opts.ParentModel, "parent-model", "", "parent model，留空使用 parent provider 默认模型")
	cmd.Flags().StringVar(&opts.ParentReasoningEffort, "parent-reasoning-effort", "", "parent reasoning_effort fallback")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().Bool("json", false, "以 JSON 格式输出")
	return cmd
}

func runDoctorSubagentRoute(cfg *config.Config, opts doctorSubagentRouteOptions) (*doctorSubagentRouteReport, map[string]interface{}, error) {
	details := map[string]interface{}{}
	if cfg == nil {
		return nil, details, fmt.Errorf("config is nil")
	}

	catalog := newDoctorSubagentRouteCatalog(cfg)
	parent := resolveDoctorSubagentRouteParent(cfg, catalog, opts)
	task := modelrouting.TaskHint{
		Role:                strings.TrimSpace(opts.Role),
		Goal:                strings.TrimSpace(opts.Goal),
		Difficulty:          strings.TrimSpace(opts.Difficulty),
		DifficultyRationale: strings.TrimSpace(opts.DifficultyRationale),
		Provider:            strings.TrimSpace(opts.Provider),
		Model:               strings.TrimSpace(opts.Model),
		ReasoningEffort:     strings.TrimSpace(opts.ReasoningEffort),
		BudgetTokens:        opts.BudgetTokens,
		Timeout:             opts.Timeout,
		ReadOnly:            opts.ReadOnly,
	}
	routing := (*config.AICLISubagentRoutingConfig)(nil)
	if cfg.AICLI != nil && cfg.AICLI.Subagents != nil {
		routing = cfg.AICLI.Subagents.Routing
	}
	decision, err := (modelrouting.Resolver{
		Config:  routing,
		Catalog: catalog,
	}).Resolve(parent, task)
	if err != nil {
		details["role"] = task.Role
		details["difficulty"] = task.Difficulty
		return nil, details, err
	}

	report := &doctorSubagentRouteReport{
		ConfigPath:     strings.TrimSpace(cfg.ConfigFilePath),
		RoutingEnabled: modelrouting.RoutingEnabled(routing),
		Request: doctorSubagentRouteRequestReport{
			Role:                task.Role,
			Goal:                task.Goal,
			Difficulty:          task.Difficulty,
			DifficultyRationale: task.DifficultyRationale,
			Provider:            task.Provider,
			Model:               task.Model,
			ReasoningEffort:     task.ReasoningEffort,
			BudgetTokens:        task.BudgetTokens,
			Timeout:             durationString(task.Timeout),
			ReadOnly:            task.ReadOnly,
		},
		Parent: doctorSubagentRouteParentReport{
			Provider:        parent.Provider,
			Model:           parent.Model,
			ReasoningEffort: parent.ReasoningEffort,
			MaxTokens:       parent.MaxTokens,
			Timeout:         durationString(parent.Timeout),
		},
		Decision: doctorSubagentRouteDecisionReport{
			Difficulty:          decision.Difficulty,
			DifficultySource:    decision.DifficultySource,
			DifficultyRationale: decision.DifficultyRationale,
			Provider:            decision.Provider,
			Model:               decision.Model,
			ReasoningEffort:     decision.ReasoningEffort,
			MaxTokens:           decision.MaxTokens,
			Timeout:             durationString(decision.Timeout),
			Source:              decision.Source,
			Warnings:            append([]string(nil), decision.Warnings...),
			FallbackUsed:        decision.FallbackUsed,
			FallbackReason:      decision.FallbackReason,
		},
		Warnings:  append([]string(nil), decision.Warnings...),
		Providers: catalog.ProviderBriefs(),
	}
	return report, nil, nil
}

func renderDoctorSubagentRouteReport(report *doctorSubagentRouteReport, outputOptions structuredOutputOptions) {
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("doctor subagent-route", outputOptions.Envelope, report)
		return
	}
	if report == nil {
		fmt.Println("Subagent route: <nil>")
		return
	}
	fmt.Println("================================================================================")
	fmt.Println("                         Subagent Route Dry Run")
	fmt.Println("================================================================================")
	if report.ConfigPath != "" {
		fmt.Printf("Config:          %s\n", report.ConfigPath)
	}
	fmt.Printf("Routing enabled: %v\n", report.RoutingEnabled)
	fmt.Println()
	fmt.Println("[Request]")
	fmt.Printf("  Role:          %s\n", emptyIfBlank(report.Request.Role))
	fmt.Printf("  Difficulty:    %s\n", emptyIfBlank(report.Request.Difficulty))
	fmt.Printf("  Read only:     %v\n", report.Request.ReadOnly)
	if report.Request.Goal != "" {
		fmt.Printf("  Goal:          %s\n", report.Request.Goal)
	}
	if report.Request.Provider != "" || report.Request.Model != "" || report.Request.ReasoningEffort != "" {
		fmt.Printf("  Overrides:     provider=%s model=%s reasoning_effort=%s\n",
			emptyIfBlank(report.Request.Provider),
			emptyIfBlank(report.Request.Model),
			emptyIfBlank(report.Request.ReasoningEffort),
		)
	}
	fmt.Println()
	fmt.Println("[Parent]")
	fmt.Printf("  Provider:      %s\n", emptyIfBlank(report.Parent.Provider))
	fmt.Printf("  Model:         %s\n", emptyIfBlank(report.Parent.Model))
	fmt.Printf("  Reasoning:     %s\n", emptyIfBlank(report.Parent.ReasoningEffort))
	if report.Parent.MaxTokens > 0 {
		fmt.Printf("  Max tokens:    %d\n", report.Parent.MaxTokens)
	}
	fmt.Println()
	fmt.Println("[Decision]")
	fmt.Printf("  Difficulty:    %s", emptyIfBlank(report.Decision.Difficulty))
	if report.Decision.DifficultySource != "" {
		fmt.Printf(" (%s)", report.Decision.DifficultySource)
	}
	fmt.Println()
	fmt.Printf("  Provider:      %s\n", emptyIfBlank(report.Decision.Provider))
	fmt.Printf("  Model:         %s\n", emptyIfBlank(report.Decision.Model))
	fmt.Printf("  Reasoning:     %s\n", emptyIfBlank(report.Decision.ReasoningEffort))
	fmt.Printf("  Source:        %s\n", emptyIfBlank(report.Decision.Source))
	if report.Decision.FallbackUsed || report.Decision.FallbackReason != "" {
		fmt.Printf("  Fallback:      used=%v reason=%s\n", report.Decision.FallbackUsed, emptyIfBlank(report.Decision.FallbackReason))
	}
	if report.Decision.MaxTokens > 0 {
		fmt.Printf("  Max tokens:    %d\n", report.Decision.MaxTokens)
	}
	if report.Decision.Timeout != "" {
		fmt.Printf("  Timeout:       %s\n", report.Decision.Timeout)
	}
	if len(report.Warnings) > 0 {
		fmt.Println()
		fmt.Println("[Warnings]")
		for _, warning := range report.Warnings {
			fmt.Printf("  - %s\n", warning)
		}
	}
}

func resolveDoctorSubagentRouteParent(cfg *config.Config, catalog *doctorSubagentRouteCatalog, opts doctorSubagentRouteOptions) modelrouting.ParentDefaults {
	parentProvider := strings.TrimSpace(opts.ParentProvider)
	if parentProvider == "" && cfg != nil && cfg.AICLI != nil && cfg.AICLI.Chat != nil {
		if preferred := strings.TrimSpace(cfg.AICLI.Chat.DefaultProvider); preferred != "" && isEnabledProvider(cfg, preferred) {
			parentProvider = preferred
		}
	}
	if parentProvider == "" && cfg != nil {
		parentProvider = strings.TrimSpace(cfg.Providers.DefaultProvider)
	}
	if catalog != nil {
		if resolved := catalog.ResolveProviderName(parentProvider); resolved != "" {
			parentProvider = resolved
		}
	}

	parentModel := strings.TrimSpace(opts.ParentModel)
	if parentModel == "" && cfg != nil && cfg.AICLI != nil && cfg.AICLI.Chat != nil {
		parentModel = strings.TrimSpace(cfg.AICLI.Chat.DefaultModel)
	}
	parentMaxTokens := 0
	parentTimeout := time.Duration(0)
	if cfg != nil && parentProvider != "" {
		if provider, ok := cfg.Providers.Items[parentProvider]; ok {
			if parentModel == "" {
				parentModel = strings.TrimSpace(provider.DefaultModel)
			}
			if parentModel != "" {
				parentModel = config.ApplyModelMapping(&provider, parentModel)
			}
			parentMaxTokens = provider.GetMaxTokensLimit()
			parentTimeout = provider.Timeout
		}
	}
	return modelrouting.ParentDefaults{
		Provider:        parentProvider,
		Model:           parentModel,
		ReasoningEffort: resolveDoctorSubagentRouteParentReasoning(cfg, opts),
		MaxTokens:       parentMaxTokens,
		Timeout:         parentTimeout,
	}
}

func resolveDoctorSubagentRouteParentReasoning(cfg *config.Config, opts doctorSubagentRouteOptions) string {
	if effort := strings.TrimSpace(opts.ParentReasoningEffort); effort != "" {
		return effort
	}
	if cfg != nil && cfg.AICLI != nil && cfg.AICLI.Chat != nil {
		return strings.TrimSpace(cfg.AICLI.Chat.ReasoningEffort)
	}
	return ""
}

type doctorSubagentRouteCatalog struct {
	cfg     *config.Config
	aliases map[string]string
}

func newDoctorSubagentRouteCatalog(cfg *config.Config) *doctorSubagentRouteCatalog {
	c := &doctorSubagentRouteCatalog{
		cfg:     cfg,
		aliases: map[string]string{},
	}
	if cfg == nil {
		return c
	}
	for name, provider := range cfg.Providers.Items {
		if !provider.Enabled {
			continue
		}
		c.addAlias(name, name)
		c.addAlias(provider.DefaultModel, name)
		for _, model := range provider.SupportedModels {
			c.addAlias(model, name)
		}
	}
	return c
}

func (c *doctorSubagentRouteCatalog) addAlias(alias, provider string) {
	alias = strings.TrimSpace(alias)
	provider = strings.TrimSpace(provider)
	if alias == "" || provider == "" {
		return
	}
	c.aliases[alias] = provider
}

func (c *doctorSubagentRouteCatalog) ResolveProviderName(name string) string {
	if c == nil {
		return ""
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if provider, ok := c.aliases[name]; ok {
		return provider
	}
	for alias, provider := range c.aliases {
		if strings.EqualFold(alias, name) {
			return provider
		}
	}
	return ""
}

func (c *doctorSubagentRouteCatalog) DefaultModel(provider string) string {
	if c == nil || c.cfg == nil {
		return ""
	}
	provider = c.ResolveProviderName(provider)
	if provider == "" {
		return ""
	}
	cfgProvider, ok := c.cfg.Providers.Items[provider]
	if !ok || !cfgProvider.Enabled {
		return ""
	}
	model := strings.TrimSpace(cfgProvider.DefaultModel)
	if model == "" {
		return ""
	}
	return config.ApplyModelMapping(&cfgProvider, model)
}

func (c *doctorSubagentRouteCatalog) SupportsModel(provider, model string) (bool, bool) {
	cfgProvider, mappedModel, ok := c.resolveProviderModel(provider, model)
	if !ok {
		return false, false
	}
	if len(cfgProvider.ModelCapabilities) > 0 {
		if _, ok := runtimellm.ResolveModelCapabilitySpec(mappedModel, cfgProvider.ModelCapabilities); ok {
			return true, true
		}
		if strings.TrimSpace(model) != "" && mappedModel != strings.TrimSpace(model) {
			if _, ok := runtimellm.ResolveModelCapabilitySpec(strings.TrimSpace(model), cfgProvider.ModelCapabilities); ok {
				return true, true
			}
		}
		return false, true
	}
	if len(cfgProvider.SupportedModels) > 0 {
		for _, supported := range cfgProvider.SupportedModels {
			if strings.EqualFold(strings.TrimSpace(supported), strings.TrimSpace(model)) ||
				strings.EqualFold(strings.TrimSpace(supported), mappedModel) {
				return true, true
			}
		}
		return false, true
	}
	return false, false
}

func (c *doctorSubagentRouteCatalog) SupportsReasoningEffort(provider, model, effort string) (bool, bool) {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return true, true
	}
	capability, ok := c.resolveModelCapability(provider, model)
	if !ok {
		return false, false
	}
	if len(capability.ReasoningEfforts) == 0 {
		return capability.ReasoningModel, true
	}
	for _, allowed := range capability.ReasoningEfforts {
		if strings.EqualFold(strings.TrimSpace(allowed), effort) {
			return true, true
		}
	}
	return false, true
}

func (c *doctorSubagentRouteCatalog) SupportedReasoningEfforts(provider, model string) ([]string, bool) {
	capability, ok := c.resolveModelCapability(provider, model)
	if !ok {
		return nil, false
	}
	efforts := make([]string, 0, len(capability.ReasoningEfforts))
	for _, effort := range capability.ReasoningEfforts {
		if effort = strings.TrimSpace(effort); effort != "" {
			efforts = append(efforts, effort)
		}
	}
	return efforts, true
}

func (c *doctorSubagentRouteCatalog) resolveProviderModel(provider, model string) (config.Provider, string, bool) {
	if c == nil || c.cfg == nil {
		return config.Provider{}, "", false
	}
	provider = c.ResolveProviderName(provider)
	if provider == "" {
		return config.Provider{}, "", false
	}
	cfgProvider, ok := c.cfg.Providers.Items[provider]
	if !ok || !cfgProvider.Enabled {
		return config.Provider{}, "", false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = strings.TrimSpace(cfgProvider.DefaultModel)
	}
	if model == "" {
		return cfgProvider, "", false
	}
	return cfgProvider, config.ApplyModelMapping(&cfgProvider, model), true
}

func (c *doctorSubagentRouteCatalog) resolveModelCapability(provider, model string) (config.ModelCapabilitySpec, bool) {
	cfgProvider, mappedModel, ok := c.resolveProviderModel(provider, model)
	if !ok {
		return config.ModelCapabilitySpec{}, false
	}
	capability, ok := runtimellm.ResolveModelCapabilitySpec(mappedModel, cfgProvider.ModelCapabilities)
	if !ok && mappedModel != strings.TrimSpace(model) {
		capability, ok = runtimellm.ResolveModelCapabilitySpec(strings.TrimSpace(model), cfgProvider.ModelCapabilities)
	}
	return capability, ok
}

func (c *doctorSubagentRouteCatalog) ProviderBriefs() []doctorSubagentRouteProviderBrief {
	if c == nil || c.cfg == nil {
		return nil
	}
	names := make([]string, 0, len(c.cfg.Providers.Items))
	for name, provider := range c.cfg.Providers.Items {
		if provider.Enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	briefs := make([]doctorSubagentRouteProviderBrief, 0, len(names))
	for _, name := range names {
		provider := c.cfg.Providers.Items[name]
		capabilityKeys := make([]string, 0, len(provider.ModelCapabilities))
		for key := range provider.ModelCapabilities {
			capabilityKeys = append(capabilityKeys, key)
		}
		sort.Strings(capabilityKeys)
		briefs = append(briefs, doctorSubagentRouteProviderBrief{
			Name:           name,
			DefaultModel:   strings.TrimSpace(provider.DefaultModel),
			Supported:      append([]string(nil), provider.SupportedModels...),
			CapabilityKeys: capabilityKeys,
		})
	}
	return briefs
}

func durationString(value time.Duration) string {
	if value <= 0 {
		return ""
	}
	return value.String()
}
