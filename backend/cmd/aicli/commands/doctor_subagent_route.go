package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelrouting"
)

type doctorSubagentRouteOptions struct {
	Scope                 string
	Workflow              string
	TeamID                string
	Teammate              string
	TaskID                string
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
	WritePaths            []string
	ParentProvider        string
	ParentModel           string
	ParentReasoningEffort string
	OutputOptions         structuredOutputOptions
}

type doctorSubagentRouteReport struct {
	ConfigPath     string                             `json:"config_path,omitempty"`
	Scope          string                             `json:"scope"`
	RoutingSource  string                             `json:"routing_source"`
	RoutingEnabled bool                               `json:"routing_enabled"`
	Request        doctorSubagentRouteRequestReport   `json:"request"`
	Parent         doctorSubagentRouteParentReport    `json:"parent"`
	Decision       doctorSubagentRouteDecisionReport  `json:"decision"`
	Warnings       []string                           `json:"warnings,omitempty"`
	Providers      []doctorSubagentRouteProviderBrief `json:"providers,omitempty"`
}

type doctorSubagentRouteRequestReport struct {
	Workflow            string   `json:"workflow,omitempty"`
	TeamID              string   `json:"team_id,omitempty"`
	Teammate            string   `json:"teammate,omitempty"`
	TaskID              string   `json:"task_id,omitempty"`
	Role                string   `json:"role,omitempty"`
	Goal                string   `json:"goal,omitempty"`
	Difficulty          string   `json:"difficulty,omitempty"`
	DifficultyRationale string   `json:"difficulty_rationale,omitempty"`
	Provider            string   `json:"provider,omitempty"`
	Model               string   `json:"model,omitempty"`
	ReasoningEffort     string   `json:"reasoning_effort,omitempty"`
	BudgetTokens        int      `json:"budget_tokens,omitempty"`
	Timeout             string   `json:"timeout,omitempty"`
	ReadOnly            bool     `json:"read_only,omitempty"`
	WritePaths          []string `json:"write_paths,omitempty"`
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

type doctorSubagentRouteProviderBrief = modelrouting.ProviderBrief

func newDoctorSubagentRouteCommand(getCfg func() *config.Config) *cobra.Command {
	var opts doctorSubagentRouteOptions
	cmd := &cobra.Command{
		Use:   "subagent-route",
		Short: "预览子 Agent / Team 难度路由结果，不调用模型",
		Long: `预览 spawn_agent / spawn_team 的难度路由结果，不实际调用模型。

用于确认 difficulty、provider/model override、read-only 与 team 上下文如何落到路由决策。
角色 / agent 定义见 docs/aicli/agents.md；排错见 docs/aicli/faq.md。

示例：
  aicli doctor subagent-route --difficulty hard --goal "review auth changes"
  aicli doctor subagent-route --scope team --workflow spawn_team --task-id t1 --difficulty expert --json`,
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
	cmd.Flags().StringVar(&opts.Scope, "scope", "auto", "路由范围 auto|subagent|team；auto 根据 workflow 选择")
	cmd.Flags().StringVar(&opts.Workflow, "workflow", "", "workflow hint，例如 spawn_agent 或 spawn_team")
	cmd.Flags().StringVar(&opts.TeamID, "team-id", "", "spawn_team team id，仅用于 dry-run 上下文")
	cmd.Flags().StringVar(&opts.Teammate, "teammate", "", "spawn_team teammate id/profile hint，仅用于 dry-run 上下文")
	cmd.Flags().StringVar(&opts.TaskID, "task", "", "spawn_team task id，仅用于 dry-run 上下文")
	cmd.Flags().StringVar(&opts.TaskID, "task-id", "", "spawn_team task id，仅用于 dry-run 上下文")
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
	cmd.Flags().StringArrayVar(&opts.WritePaths, "write-path", nil, "spawn_team 写路径 hint，可重复")
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

	catalog := modelrouting.NewConfigCatalog(cfg)
	parent := resolveDoctorSubagentRouteParent(cfg, catalog, opts)
	workflow := normalizeDoctorRouteWorkflow(opts.Workflow)
	scope, routingSource, routing, err := modelrouting.ResolveConfigScope(cfg, opts.Scope, workflow)
	if err != nil {
		details["scope"] = strings.TrimSpace(opts.Scope)
		return nil, details, err
	}
	writePaths := normalizeDoctorRouteStringSlice(opts.WritePaths)
	readOnly := opts.ReadOnly
	if len(writePaths) > 0 {
		readOnly = false
	}
	role := strings.TrimSpace(opts.Role)
	if role == "" && workflow == "spawn_team" {
		if len(writePaths) > 0 {
			role = "writer"
		} else {
			role = strings.TrimSpace(opts.Teammate)
		}
	}
	task := modelrouting.TaskHint{
		Role:                role,
		Goal:                strings.TrimSpace(opts.Goal),
		Difficulty:          strings.TrimSpace(opts.Difficulty),
		DifficultyRationale: strings.TrimSpace(opts.DifficultyRationale),
		Provider:            strings.TrimSpace(opts.Provider),
		Model:               strings.TrimSpace(opts.Model),
		ReasoningEffort:     strings.TrimSpace(opts.ReasoningEffort),
		BudgetTokens:        opts.BudgetTokens,
		Timeout:             opts.Timeout,
		ReadOnly:            readOnly,
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
		Scope:          scope,
		RoutingSource:  routingSource,
		RoutingEnabled: modelrouting.RoutingEnabled(routing),
		Request: doctorSubagentRouteRequestReport{
			Workflow:            workflow,
			TeamID:              strings.TrimSpace(opts.TeamID),
			Teammate:            strings.TrimSpace(opts.Teammate),
			TaskID:              strings.TrimSpace(opts.TaskID),
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
			WritePaths:          writePaths,
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

func normalizeDoctorRouteWorkflow(workflow string) string {
	workflow = strings.ToLower(strings.TrimSpace(workflow))
	switch workflow {
	case "", "subagent", "spawn_agent", "spawn-agent":
		return ""
	case "team", "spawn_team", "spawn-team":
		return "spawn_team"
	default:
		return workflow
	}
}

func normalizeDoctorRouteStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			key := strings.ToLower(item)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			normalized = append(normalized, item)
		}
	}
	return normalized
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
	if report.Scope == "team" {
		fmt.Println("                           Team Route Dry Run")
	} else {
		fmt.Println("                         Subagent Route Dry Run")
	}
	fmt.Println("================================================================================")
	if report.ConfigPath != "" {
		fmt.Printf("Config:          %s\n", report.ConfigPath)
	}
	fmt.Printf("Scope:           %s\n", report.Scope)
	fmt.Printf("Routing source:  %s\n", report.RoutingSource)
	fmt.Printf("Routing enabled: %v\n", report.RoutingEnabled)
	fmt.Println()
	fmt.Println("[Request]")
	if report.Request.Workflow != "" {
		fmt.Printf("  Workflow:      %s\n", report.Request.Workflow)
	}
	if report.Request.TeamID != "" || report.Request.Teammate != "" || report.Request.TaskID != "" {
		fmt.Printf("  Team task:     team=%s teammate=%s task=%s\n",
			emptyIfBlank(report.Request.TeamID),
			emptyIfBlank(report.Request.Teammate),
			emptyIfBlank(report.Request.TaskID),
		)
	}
	fmt.Printf("  Role:          %s\n", emptyIfBlank(report.Request.Role))
	fmt.Printf("  Difficulty:    %s\n", emptyIfBlank(report.Request.Difficulty))
	fmt.Printf("  Read only:     %v\n", report.Request.ReadOnly)
	if len(report.Request.WritePaths) > 0 {
		fmt.Printf("  Write paths:   %s\n", strings.Join(report.Request.WritePaths, ", "))
	}
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

func resolveDoctorSubagentRouteParent(
	cfg *config.Config,
	catalog modelrouting.ProviderCatalog,
	opts doctorSubagentRouteOptions,
) modelrouting.ParentDefaults {
	return modelrouting.ResolveParentDefaults(cfg, catalog, modelrouting.ConfigParentOverrides{
		Provider:        opts.ParentProvider,
		Model:           opts.ParentModel,
		ReasoningEffort: opts.ParentReasoningEffort,
	})
}

func durationString(value time.Duration) string {
	if value <= 0 {
		return ""
	}
	return value.String()
}
