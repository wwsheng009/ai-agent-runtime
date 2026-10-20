package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// doctorHooksReport is the health view of the hooks subsystem. `hooks check`
// answers "did it load", while this report also answers "what will actually run
// and is anything configured in a way that cannot work".
type doctorHooksReport struct {
	Workspace       string                 `json:"workspace"`
	ProjectTrust    hooksTrustInfo         `json:"project_trust"`
	Sources         []string               `json:"sources,omitempty"`
	Warnings        []string               `json:"warnings,omitempty"`
	Health          string                 `json:"health"`
	HandlerCount    int                    `json:"handler_count"`
	EventCounts     map[string]int         `json:"event_counts,omitempty"`
	HandlerTypes    map[string]int         `json:"handler_types,omitempty"`
	AsyncCount      int                    `json:"async_count"`
	DisabledCount   int                    `json:"disabled_count"`
	LegacyHTTPCount int                    `json:"legacy_http_count"`
	Findings        []doctorHooksFinding   `json:"findings,omitempty"`
	SampleHandlers  []hooksListEntry       `json:"sample_handlers,omitempty"`
	Extra           map[string]interface{} `json:"-"`
}

type doctorHooksFinding struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

func newDoctorHooksCommand() *cobra.Command {
	var (
		workspace  string
		configFile string
	)
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "体检 hooks 配置：来源、信任、告警与可执行性",
		Long: `体检 hooks 配置，不执行任何 handler。

与 ` + "`aicli hooks check`" + ` 的区别：
  hooks check   只看加载结果与告警，适合 CI 门禁（有告警即非零退出）
  doctor hooks  给出整体健康结论与可操作建议，适合人工排障

输出包含作用域来源、folder trust 状态、handler 类型分布，以及
“这类 handler 当前能否真正运行”的检查（例如 mcp_tool 需要 MCP 管理器）。`,
		Example: `  aicli doctor hooks
  aicli doctor hooks --workspace ./my-repo
  aicli doctor hooks --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
			if err != nil {
				return err
			}
			opts := &hooksCommandOptions{Workspace: workspace, ConfigFile: configFile}
			report := inspectDoctorHooks(opts)
			return renderDoctorHooksReport(cmd.OutOrStdout(), report, outputOptions.Format)
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "工作区根目录（默认当前目录）")
	cmd.Flags().StringVarP(&configFile, "config-file", "f", "", "runtime.yaml 路径")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().Bool("json", false, "以 JSON 格式输出")
	return cmd
}

func inspectDoctorHooks(opts *hooksCommandOptions) doctorHooksReport {
	list := collectHooksList(opts)
	report := doctorHooksReport{
		Workspace:    list.Workspace,
		ProjectTrust: list.Trust,
		Sources:      list.Sources,
		Warnings:     list.Warnings,
		HandlerCount: list.Count,
		EventCounts:  map[string]int{},
		HandlerTypes: map[string]int{},
	}
	for _, hook := range list.Hooks {
		report.EventCounts[hook.Event]++
		report.HandlerTypes[hook.HandlerType]++
		if hook.ExecutionMode == "async" {
			report.AsyncCount++
		}
		if !hook.Enabled {
			report.DisabledCount++
		}
		if hook.HandlerType == "http" {
			report.LegacyHTTPCount++
		}
	}
	report.SampleHandlers = list.Hooks
	if len(report.SampleHandlers) > 20 {
		report.SampleHandlers = report.SampleHandlers[:20]
	}
	report.Findings = buildDoctorHooksFindings(report)
	report.Health = doctorHooksHealth(report.Findings)
	return report
}

func buildDoctorHooksFindings(report doctorHooksReport) []doctorHooksFinding {
	var findings []doctorHooksFinding
	add := func(severity, message string) {
		findings = append(findings, doctorHooksFinding{Severity: severity, Message: message})
	}

	if !report.ProjectTrust.FeatureEnabled {
		// Nothing to say: project scope is governed by the feature switch.
	} else if !report.ProjectTrust.Trusted {
		add("warning", fmt.Sprintf("项目作用域被跳过：该工作区未通过 folder trust（source=%s）", report.ProjectTrust.Source))
	}
	if len(report.Warnings) > 0 {
		add("error", fmt.Sprintf("%d 条加载告警：部分 handler 被丢弃，详见 warnings", len(report.Warnings)))
	}
	if report.HandlerCount == 0 && len(report.Warnings) == 0 {
		add("info", "未发现任何 handler；如需项目级 hook，放在 <workspace>/.aicli/hooks.json")
	}
	if report.LegacyHTTPCount > 0 {
		add("info", fmt.Sprintf("%d 个 http handler 仍在使用旧式列表格式；Codex 声明式格式不支持 http", report.LegacyHTTPCount))
	}
	if report.AsyncCount > 0 {
		add("info", fmt.Sprintf("%d 个 async handler 只做观测，block/stop 等控制效果会被丢弃", report.AsyncCount))
	}
	if report.DisabledCount > 0 {
		add("info", fmt.Sprintf("%d 个 handler 在 state 里被禁用", report.DisabledCount))
	}
	return findings
}

func doctorHooksHealth(findings []doctorHooksFinding) string {
	health := "ok"
	for _, finding := range findings {
		switch finding.Severity {
		case "error":
			return "error"
		case "warning":
			health = "warning"
		}
	}
	return health
}

func renderDoctorHooksReport(writer io.Writer, report doctorHooksReport, format string) error {
	if format == "json" {
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(report)
	}
	if _, err := fmt.Fprintf(writer, "workspace: %s\nhealth: %s\n", report.Workspace, report.Health); err != nil {
		return err
	}
	switch {
	case !report.ProjectTrust.FeatureEnabled:
		fmt.Fprintln(writer, "project trust: feature off")
	case report.ProjectTrust.Trusted:
		fmt.Fprintf(writer, "project trust: trusted (%s)\n", report.ProjectTrust.Source)
	default:
		fmt.Fprintf(writer, "project trust: untrusted (%s) — project hooks skipped\n", report.ProjectTrust.Source)
	}
	fmt.Fprintf(writer, "sources: %d, handlers: %d, warnings: %d\n", len(report.Sources), report.HandlerCount, len(report.Warnings))
	if len(report.HandlerTypes) > 0 {
		fmt.Fprintf(writer, "types: %s\n", formatHookCounts(report.HandlerTypes))
	}
	if len(report.EventCounts) > 0 {
		fmt.Fprintf(writer, "events: %s\n", formatHookCounts(report.EventCounts))
	}
	if len(report.Findings) > 0 {
		fmt.Fprintln(writer, "findings:")
		for _, finding := range report.Findings {
			fmt.Fprintf(writer, "  [%s] %s\n", finding.Severity, finding.Message)
		}
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(writer, "  ! %s\n", warning)
	}
	return nil
}

func formatHookCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return strings.Join(parts, " ")
}
