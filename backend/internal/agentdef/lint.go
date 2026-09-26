package agentdef

import (
	"fmt"
	"sort"
	"strings"
)

// LintIssue is one non-fatal diagnostic from a definition or catalog review.
type LintIssue struct {
	Severity string // "warning" | "info"
	Name     string
	Path     string
	Message  string
}

// LintDefinition reviews a single definition for risky or surprising
// declarations. It never rejects: load-time errors already fail in Validate.
func LintDefinition(def *Definition) []LintIssue {
	if def == nil {
		return nil
	}
	issues := make([]LintIssue, 0, 4)
	add := func(severity, message string) {
		issues = append(issues, LintIssue{
			Severity: severity,
			Name:     def.Name,
			Path:     def.SourcePath,
			Message:  message,
		})
	}

	for _, warning := range def.Warnings {
		switch {
		case strings.HasPrefix(warning, "reasoning_effort_unknown:"):
			add("warning", fmt.Sprintf("reasoningEffort %q 不是已知档位（accepted: none|low|medium|high|xhigh|max），已丢弃；运行时将使用模型默认",
				strings.TrimPrefix(warning, "reasoning_effort_unknown:")))
		case warning == "max_turns_negative":
			add("warning", "maxTurns 为负数，已归一化为 0（继承运行时默认）")
		default:
			add("warning", warning)
		}
	}

	if !def.HasExplicitTools() {
		add("info", "tools 未声明：继承完整工具箱（与 CommandCode 的 fail-closed 语义相反）；如需显式全量请写 tools: [\"*\"]，如需收窄请列出工具名")
	}
	if def.ToolsWildcard() {
		add("info", "tools: \"*\" 显式全量（包含 MCP 工具）；disallowedTools 仍然生效")
	}
	if strings.TrimSpace(def.Description) == "" {
		add("warning", "description 为空：父模型无法据此判断何时委派给该角色（catalog/委派路由都会缺少说明）")
	}
	if def.Source != "" && def.Source != SourceBuiltin && IsReservedAgentName(def.Name) {
		add("info", fmt.Sprintf("该定义覆盖内置角色 %q；内置行为将被替换（移除该文件可恢复）", def.Name))
	}
	if len(def.DisallowedTools) > 0 && def.HasExplicitTools() && !def.ToolsWildcard() {
		allowed := make(map[string]struct{}, len(def.Tools))
		for _, tool := range def.Tools {
			allowed[strings.ToLower(strings.TrimSpace(tool))] = struct{}{}
		}
		for _, tool := range def.DisallowedTools {
			if _, ok := allowed[strings.ToLower(strings.TrimSpace(tool))]; ok {
				add("info", fmt.Sprintf("工具 %q 同时出现在 tools 与 disallowedTools；deny 优先", tool))
			}
		}
	}
	return issues
}

// LintCatalog reviews a discovered catalog: per-definition issues plus
// shadowing diagnostics recorded during discovery.
func LintCatalog(catalog *Catalog) []LintIssue {
	if catalog == nil {
		return nil
	}
	issues := make([]LintIssue, 0, 8)
	for _, def := range catalog.List() {
		issues = append(issues, LintDefinition(def)...)
	}
	if len(catalog.Overridden) > 0 {
		names := make([]string, 0, len(catalog.Overridden))
		for name := range catalog.Overridden {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if IsReservedAgentName(name) {
				// Already reported per definition (reserved-name case) above;
				// avoid a duplicate line for the same override.
				continue
			}
			def, _ := catalog.Get(name)
			path := ""
			if def != nil {
				path = def.SourcePath
			}
			issues = append(issues, LintIssue{
				Severity: "info",
				Name:     name,
				Path:     path,
				Message: fmt.Sprintf("同名定义覆盖了 %s 来源；高层来源（user/project/profile）静默生效，移除高层文件可恢复",
					catalog.Overridden[name]),
			})
		}
	}
	return issues
}
