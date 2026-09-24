package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// profileValidateIssue 是 `profile validate` 的一条发现。
type profileValidateIssue struct {
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Message  string `json:"message"`
}

type profileValidateResult struct {
	Reference    string                 `json:"reference"`
	ProfileName  string                 `json:"profile_name,omitempty"`
	ProfileRoot  string                 `json:"profile_root"`
	AgentID      string                 `json:"agent_id,omitempty"`
	Valid        bool                   `json:"valid"`
	ErrorCount   int                    `json:"error_count"`
	WarningCount int                    `json:"warning_count"`
	Issues       []profileValidateIssue `json:"issues"`
}

const (
	profileValidateSeverityError   = "error"
	profileValidateSeverityWarning = "warning"
)

func newProfileValidateCommand(getConfig func() *config.Config) *cobra.Command {
	var agentFlag string
	cmd := &cobra.Command{
		Use:   "validate <profile>",
		Short: "校验 profile 声明",
		Long: `校验 profile 声明，复用 profile 包的校验器与真实解析结果：

  1. profile.yaml 语法与声明（prompts.mode / 选择列表重复与冲突等）
  2. profile.name 必填
  3. 工具名是否在工具清单中登记（未登记按 warning：可能是 MCP/动态工具）
  4. skills 引用是否存在（allowlist 缺失=error，denylist 缺失=warning 冗余）
  5. mcp use/exclude 引用是否存在（use 缺失=error，exclude 缺失=warning）
  6. prompt 文件可读性与空文件检测

存在 error 级问题时退出码为 1（可直接用于 CI 门禁），warning 不影响退出码。`,
		Example: `  aicli profile validate coding
  aicli profile validate .\profiles\review --agent reviewer
  aicli profile validate coding --output json`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
			if err != nil {
				exitCommandError("profile validate", "json", err, nil)
			}
			result, runErr := runProfileValidateCommand(getConfig(), args[0], agentFlag)
			if runErr != nil {
				exitCommandError("profile validate", outputOptions.Format, runErr, nil)
			}
			if isJSONOutputFormat(outputOptions.Format) {
				printCommandJSONOutput("profile validate", outputOptions.Envelope, result)
			} else {
				renderProfileValidateText(result)
			}
			if !result.Valid {
				runExitCleanup()
				os.Exit(1)
			}
		},
	}
	cmd.Flags().StringVar(&agentFlag, "agent", "", "指定 profile 内的 agent id")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	return cmd
}

func runProfileValidateCommand(cfg *config.Config, ref, agent string) (profileValidateResult, error) {
	state, err := resolveProfileStateForCLI(cfg, ref, agent)
	if err != nil {
		return profileValidateResult{}, annotateProfileResolveError(err)
	}
	resolved := state.Resolved
	result := profileValidateResult{
		Reference:   state.Reference,
		ProfileName: resolved.ProfileName,
		ProfileRoot: resolved.ProfileRoot,
		AgentID:     resolved.AgentID,
		Issues:      make([]profileValidateIssue, 0, 4),
	}
	addIssue := func(severity, path, format string, args ...interface{}) {
		result.Issues = append(result.Issues, profileValidateIssue{
			Severity: severity,
			Path:     path,
			Message:  fmt.Sprintf(format, args...),
		})
	}

	// 1/2. profile.yaml 声明（复用 profile 包校验器，避免第二套校验口径）。
	spec, specErr := loadProfileSpecForCLI(resolved.ProfileRoot)
	if specErr != nil {
		addIssue(profileValidateSeverityError, "profile.yaml", "读取失败：%v", specErr)
	} else {
		for _, issue := range profilesys.ValidateProfileSpec(spec) {
			addIssue(string(issue.Severity), issue.Path, "%s", issue.Message)
		}
		if strings.TrimSpace(spec.Profile.Name) == "" {
			addIssue(profileValidateSeverityError, "profile.name", "未声明 profile.name")
		}
		hasPromptFile := strings.TrimSpace(resolved.Prompts.System) != "" ||
			strings.TrimSpace(resolved.Prompts.Role) != "" ||
			strings.TrimSpace(resolved.Prompts.Tools) != ""
		if strings.TrimSpace(spec.Prompts.Mode) != "" && !hasPromptFile {
			addIssue(profileValidateSeverityWarning, "prompts.mode",
				"声明了 prompts.mode=%q，但没有发现任何 prompt 文件（不会产生注入）", spec.Prompts.Mode)
		}
	}

	// 3. 工具名登记（未登记 = warning：MCP/动态工具名合法出现在名单中）。
	validateToolNames := func(path string, names []string) {
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == "" || name == "*" {
				continue
			}
			if _, ok := runtimepolicy.LookupToolTaxonomy(name); !ok {
				addIssue(profileValidateSeverityWarning, path,
					"未在工具清单中登记：%q（若为 MCP/动态工具可忽略）", name)
			}
		}
	}
	validateToolNames("tools.allowlist", resolved.ToolPolicy.Allowlist)
	validateToolNames("tools.denylist", resolved.ToolPolicy.Denylist)

	// 4. skills 引用。
	discovered := discoverProfileSkillNames(resolved.SkillDirs)
	discoveredSet := make(map[string]struct{}, len(discovered))
	for _, name := range discovered {
		discoveredSet[strings.ToLower(name)] = struct{}{}
	}
	if !resolved.Skills.Empty() && len(resolved.SkillDirs) == 0 {
		// 纯通配 deny（denylist: ["*"]）在"没有任何技能目录"时是无操作，
		// 不产生噪音；只有正向 allow 或具体 deny 才提示。
		switch {
		case hasEffectiveProfileNames(resolved.Skills.Allowlist):
			addIssue(profileValidateSeverityWarning, "skills",
				"声明了 skills allowlist，但没有发现任何技能目录（选择不会生效）")
		case hasEffectiveProfileNames(resolved.Skills.Denylist):
			addIssue(profileValidateSeverityWarning, "skills.denylist",
				"声明了 skills denylist，但没有发现任何技能目录（deny 冗余）")
		}
	}
	for _, name := range resolved.Skills.Allowlist {
		name = strings.TrimSpace(name)
		if name == "" || name == "*" {
			continue
		}
		if _, ok := discoveredSet[strings.ToLower(name)]; !ok {
			addIssue(profileValidateSeverityError, "skills.allowlist",
				"技能不存在：%q（已发现：%s）", name, formatProfileNameList(discovered))
		}
	}
	for _, name := range resolved.Skills.Denylist {
		name = strings.TrimSpace(name)
		if name == "" || name == "*" {
			continue
		}
		if _, ok := discoveredSet[strings.ToLower(name)]; !ok {
			addIssue(profileValidateSeverityWarning, "skills.denylist",
				"技能不存在，deny 冗余：%q", name)
		}
	}

	// 5. mcp 引用。
	if !resolved.MCPSelection.Empty() {
		if strings.TrimSpace(resolved.MCPConfig) == "" {
			// 没有 mcp.yaml 时：use_servers 是正向需求 → error；
			// 具体 exclude 只是冗余 → warning；纯通配 exclude 是无操作 → 不提示。
			switch {
			case hasEffectiveProfileNames(resolved.MCPSelection.UseServers):
				addIssue(profileValidateSeverityError, "mcp.use_servers",
					"声明了 use_servers，但没有发现 mcp.yaml")
			case hasEffectiveProfileNames(resolved.MCPSelection.ExcludeServers):
				addIssue(profileValidateSeverityWarning, "mcp.exclude_servers",
					"声明了 exclude_servers，但没有发现 mcp.yaml（exclude 冗余）")
			}
		} else {
			servers, err := loadProfileMCPServers(resolved.MCPConfig)
			if err != nil {
				addIssue(profileValidateSeverityError, "mcp", "mcp.yaml 解析失败：%v", err)
			} else {
				serverSet := make(map[string]struct{}, len(servers))
				for _, name := range servers {
					serverSet[strings.ToLower(name)] = struct{}{}
				}
				if len(servers) == 0 {
					addIssue(profileValidateSeverityWarning, "mcp",
						"mcp.yaml 中没有配置任何 server")
				}
				for _, name := range resolved.MCPSelection.UseServers {
					name = strings.TrimSpace(name)
					if name == "" || name == "*" {
						continue
					}
					if _, ok := serverSet[strings.ToLower(name)]; !ok {
						addIssue(profileValidateSeverityError, "mcp.use_servers",
							"MCP server 不存在：%q（已配置：%s）", name, formatProfileNameList(servers))
					}
				}
				for _, name := range resolved.MCPSelection.ExcludeServers {
					name = strings.TrimSpace(name)
					if name == "" || name == "*" {
						continue
					}
					if _, ok := serverSet[strings.ToLower(name)]; !ok {
						addIssue(profileValidateSeverityWarning, "mcp.exclude_servers",
							"MCP server 不存在，exclude 冗余：%q", name)
					}
				}
			}
		}
	}

	// 6. prompt 文件可读性 / 空文件。
	promptFiles := []struct {
		path  string
		label string
	}{
		{resolved.Prompts.System, "prompts.system"},
		{resolved.Prompts.Role, "prompts.role"},
		{resolved.Prompts.Tools, "prompts.tools"},
	}
	for _, file := range promptFiles {
		path := strings.TrimSpace(file.path)
		if path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			addIssue(profileValidateSeverityError, file.label, "prompt 文件不可读：%v", err)
			continue
		}
		if strings.TrimSpace(string(raw)) == "" {
			addIssue(profileValidateSeverityWarning, file.label,
				"prompt 文件为空，不会注入任何内容：%s", path)
		}
	}

	for _, issue := range result.Issues {
		if issue.Severity == profileValidateSeverityError {
			result.ErrorCount++
		} else {
			result.WarningCount++
		}
	}
	result.Valid = result.ErrorCount == 0
	return result, nil
}

func formatProfileNameList(names []string) string {
	if len(names) == 0 {
		return "（无）"
	}
	return strings.Join(names, ", ")
}

// hasEffectiveProfileNames reports whether a selection list carries a concrete
// requirement, i.e. at least one non-blank, non-wildcard name. Wildcard-only
// lists are no-ops when the target set is empty, so they must not raise noise.
func hasEffectiveProfileNames(names []string) bool {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" && name != "*" {
			return true
		}
	}
	return false
}

func renderProfileValidateText(result profileValidateResult) {
	title := result.ProfileName
	if title == "" {
		title = result.Reference
	}
	fmt.Fprintf(os.Stdout, "profile validate: %s\n", title)
	fmt.Fprintf(os.Stdout, "  root:  %s\n", result.ProfileRoot)
	if result.AgentID != "" {
		fmt.Fprintf(os.Stdout, "  agent: %s\n", result.AgentID)
	}
	fmt.Fprintf(os.Stdout, "  错误 %d / 警告 %d\n", result.ErrorCount, result.WarningCount)
	if len(result.Issues) == 0 {
		fmt.Fprintln(os.Stdout, "  ✓ 校验通过")
		return
	}
	for _, issue := range result.Issues {
		mark := "!"
		if issue.Severity == profileValidateSeverityError {
			mark = "✗"
		}
		fmt.Fprintf(os.Stdout, "  %s [%s] %s：%s\n", mark, issue.Severity, issue.Path, issue.Message)
	}
	if result.Valid {
		fmt.Fprintln(os.Stdout, "  ✓ 无错误（存在警告）")
	} else {
		fmt.Fprintln(os.Stdout, "  ✗ 校验未通过")
	}
}
