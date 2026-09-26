package commands

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentdef"
)

// executeStructuredAgentDefsCommand serves `/agents defs [list|show <name>|lint]`.
//
// This is the definition-management view (portable agent files), distinct from
// the runtime collaboration panel verbs (pick/send/view/...). Discovery anchors
// on the chat process cwd plus the session profile root and active plugin dirs,
// matching what spawn-time resolution sees for the common CLI case.
func executeStructuredAgentDefsCommand(session *ChatSession, command string) CommandResult {
	arg := strings.TrimSpace(extractCommandArgument(command))
	fields := strings.Fields(arg)
	sub := ""
	if len(fields) > 0 {
		sub = strings.ToLower(fields[0])
	}
	rest := fields[1:]
	switch sub {
	case "", "list", "ls":
		return commandTextResult(renderAgentDefsList(session))
	case "show":
		if len(rest) == 0 {
			return commandErrorResult(fmt.Errorf("用法: /agents defs show <name>"))
		}
		return commandTextResult(renderAgentDefsShow(session, rest[0]))
	case "lint", "check":
		return commandTextResult(renderAgentDefsLint(session))
	default:
		return commandTextResult("用法: /agents defs [list|show <name>|lint]")
	}
}

func agentDefsDiscoverOptions(session *ChatSession) agentdef.DiscoverOptions {
	profileRoot := ""
	if session != nil {
		profileRoot = strings.TrimSpace(session.ProfileRoot)
	}
	return agentdefDiscoverOptions("", profileRoot, mergeActivePluginAgentDirs(nil))
}

func renderAgentDefsList(session *ChatSession) string {
	catalog, err := agentdef.Discover(agentDefsDiscoverOptions(session))
	if err != nil {
		return "Agent definitions: discovery failed: " + err.Error()
	}
	defs := catalog.List()
	if len(defs) == 0 {
		return "Agent definitions: none found."
	}
	bySource := make(map[agentdef.Source][]*agentdef.Definition, len(agentsSourceOrder))
	for _, def := range defs {
		bySource[def.Source] = append(bySource[def.Source], def)
	}
	lines := []string{fmt.Sprintf("Agent definitions (%d):", len(defs))}
	for _, source := range agentsSourceOrder {
		group := bySource[source]
		if len(group) == 0 {
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].Name < group[j].Name })
		lines = append(lines, fmt.Sprintf("%s (%d)", strings.ToUpper(string(source)), len(group)))
		for _, def := range group {
			suffix := ""
			readOnly := def.Sandbox == "read-only" || def.Sandbox == "readonly"
			if binding, bindErr := agentdef.BuildBinding(def); bindErr == nil && binding != nil && binding.ReadOnly != nil {
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

func renderAgentDefsShow(session *ChatSession, name string) string {
	opts := agentDefsDiscoverOptions(session)
	def, err := agentdef.Resolve(name, opts)
	if err != nil {
		catalog, discoverErr := agentdef.Discover(opts)
		if discoverErr == nil && catalog != nil {
			return fmt.Sprintf("Agent definition %q not found. Available: %s", name, strings.Join(catalog.Order, ", "))
		}
		return "Agent definition not found: " + err.Error()
	}
	binding, bindingErr := agentdef.BuildBinding(def)
	lines := []string{fmt.Sprintf("Agent definition: %s", def.Name)}
	if def.Description != "" {
		lines = append(lines, "  description: "+def.Description)
	}
	lines = append(lines,
		"  source:      "+string(def.Source),
		"  path:        "+def.SourcePath,
		"  permission:  "+def.PermissionMode,
		"  sandbox:     "+def.Sandbox,
		"  tools:       "+describeAgentsTools(def),
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
	for _, issue := range agentdef.LintDefinition(def) {
		lines = append(lines, fmt.Sprintf("  [%s] %s", issue.Severity, issue.Message))
	}
	return strings.Join(lines, "\n")
}

func renderAgentDefsLint(session *ChatSession) string {
	catalog, err := agentdef.Discover(agentDefsDiscoverOptions(session))
	if err != nil {
		return "Agent definitions lint: discovery failed: " + err.Error()
	}
	issues := agentdef.LintCatalog(catalog)
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
