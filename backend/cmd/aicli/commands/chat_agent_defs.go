package commands

import (
	"fmt"
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
	projectRoot := ""
	projectAllowed := projectScopeAllowed()
	if session != nil {
		profileRoot = strings.TrimSpace(session.ProfileRoot)
		projectRoot = strings.TrimSpace(session.WorkspaceAnchor)
		projectAllowed = sessionProjectScopeAllowed(session)
	}
	return agentdefDiscoverOptions(projectRoot, profileRoot, mergeActivePluginAgentDirsForRoot(nil, projectRoot, projectAllowed))
}

func renderAgentDefsList(session *ChatSession) string {
	catalog, err := agentdef.Discover(agentDefsDiscoverOptions(session))
	if err != nil {
		return "Agent definitions: discovery failed: " + err.Error()
	}
	return agentdef.RenderCatalog(catalog)
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
	return agentdef.RenderDefinition(def)
}

func renderAgentDefsLint(session *ChatSession) string {
	catalog, err := agentdef.Discover(agentDefsDiscoverOptions(session))
	if err != nil {
		return "Agent definitions lint: discovery failed: " + err.Error()
	}
	return agentdef.RenderLint(catalog)
}
