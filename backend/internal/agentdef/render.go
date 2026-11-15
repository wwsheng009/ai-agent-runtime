package agentdef

import (
	"fmt"
	"sort"
	"strings"
)

// ViewSourceOrder is the stable source grouping used by every human-readable
// agent-definition catalog (CLI/TUI today, runtime-server HTTP surface).
var ViewSourceOrder = []Source{
	SourceBuiltin,
	SourceUser,
	SourceProject,
	SourceProfile,
}

// RenderCatalog renders the same grouped, human-readable catalog used by
// `/agents defs list`. It intentionally has no host dependencies so the
// runtime-server endpoint can return byte-identical text.
func RenderCatalog(catalog *Catalog) string {
	if catalog == nil {
		return "Agent definitions: none found."
	}
	defs := catalog.List()
	if len(defs) == 0 {
		return "Agent definitions: none found."
	}
	bySource := make(map[Source][]*Definition, len(ViewSourceOrder))
	for _, def := range defs {
		bySource[def.Source] = append(bySource[def.Source], def)
	}
	lines := []string{fmt.Sprintf("Agent definitions (%d):", len(defs))}
	for _, source := range ViewSourceOrder {
		group := bySource[source]
		if len(group) == 0 {
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].Name < group[j].Name })
		lines = append(lines, fmt.Sprintf("%s (%d)", strings.ToUpper(string(source)), len(group)))
		for _, def := range group {
			suffix := ""
			readOnly := sandboxImpliesReadOnly(def.Sandbox)
			if binding, bindErr := BuildBinding(def); bindErr == nil && binding != nil && binding.ReadOnly != nil {
				readOnly = *binding.ReadOnly
			}
			if readOnly {
				suffix += " [read-only]"
			}
			if overridden, ok := catalog.Overridden[def.Name]; ok {
				suffix += " [overrides " + string(overridden) + "]"
			}
			desc := strings.TrimSpace(def.Description)
			if desc == "" {
				desc = "(no description)"
			}
			lines = append(lines, fmt.Sprintf("  %-20s %s%s", def.Name, desc, suffix))
		}
	}
	if len(catalog.Overridden) > 0 {
		lines = append(lines, "提示：同名覆盖静默生效；/agents defs lint 可查看全部告警。")
	}
	return strings.Join(lines, "\n")
}

// RenderDefinition renders one definition with its raw declaration, binding,
// and per-definition lint issues.
func RenderDefinition(def *Definition) string {
	if def == nil {
		return "Agent definition not found"
	}
	binding, bindingErr := BuildBinding(def)
	lines := []string{fmt.Sprintf("Agent definition: %s", def.Name)}
	if def.Description != "" {
		lines = append(lines, "  description: "+def.Description)
	}
	lines = append(lines,
		"  source:      "+string(def.Source),
		"  path:        "+def.SourcePath,
		"  permission:  "+def.PermissionMode,
		"  sandbox:     "+def.Sandbox,
		"  tools:       "+describeTools(def),
		"  skills:      "+strings.Join(def.Skills, ", "),
		fmt.Sprintf("  maxTurns:    %d", def.MaxTurns),
		"  reasoning:   "+def.ReasoningEffort,
		fmt.Sprintf("  background:  %t", def.Background),
		fmt.Sprintf("  showOutput:  %t", def.ShowOutput),
	)
	if bindingErr != nil {
		lines = append(lines, "  binding:     error: "+bindingErr.Error())
	} else if binding != nil {
		readOnly := false
		if binding.ReadOnly != nil {
			readOnly = *binding.ReadOnly
		}
		lines = append(lines, fmt.Sprintf("  effective:   read_only=%t permission=%s", readOnly, binding.PermissionMode))
	}
	for _, issue := range LintDefinition(def) {
		lines = append(lines, fmt.Sprintf("  [%s] %s", issue.Severity, issue.Message))
	}
	return strings.Join(lines, "\n")
}

// RenderLint renders the full catalog lint report with the established
// warning/info counts.
func RenderLint(catalog *Catalog) string {
	if catalog == nil {
		return "Agent definitions lint: OK (0 issues)"
	}
	issues := LintCatalog(catalog)
	if len(issues) == 0 {
		return "Agent definitions lint: OK (0 issues)"
	}
	lines := []string{fmt.Sprintf("Agent definitions lint: %d issue(s)", len(issues))}
	warnings := 0
	for _, issue := range issues {
		if issue.Severity == "warning" {
			warnings++
		}
		label := issue.Name
		if issue.Path != "" {
			label = issue.Path
		}
		lines = append(lines, fmt.Sprintf("[%s] %s: %s", issue.Severity, label, issue.Message))
	}
	lines = append(lines, fmt.Sprintf("%d warning(s), %d info", warnings, len(issues)-warnings))
	return strings.Join(lines, "\n")
}

func describeTools(def *Definition) string {
	if def == nil || !def.HasExplicitTools() {
		return "(omitted: inherit full toolkit)"
	}
	if def.ToolsWildcard() {
		return "\"*\" (all tools, MCP included)"
	}
	return strings.Join(def.Tools, ", ")
}
