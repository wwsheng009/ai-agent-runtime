package runtimeapi

import (
	"net/http"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentdef"
)

// AgentDefinitions is the runtime-server read surface for portable agent
// definitions. It uses the same discovery roots and folder-trust gate as
// server-side child spawn, and returns the shared human-readable rendering so
// remote TUI `/agents defs` can match local mode.
func (h *Handler) AgentDefinitions(w http.ResponseWriter, r *http.Request) {
	workspace := strings.TrimSpace(firstNonEmptyString(
		r.URL.Query().Get("workspace_path"),
		r.URL.Query().Get("workspace"),
	))
	profileRoot := strings.TrimSpace(r.URL.Query().Get("profile_root"))
	action := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
	if action == "" {
		action = "list"
	}
	name := strings.TrimSpace(firstNonEmptyString(r.URL.Query().Get("name"), r.URL.Query().Get("agent")))

	trust := workspaceFolderTrust(workspace)
	opts := agentdef.DiscoverOptions{
		ProfileRoot:     profileRoot,
		SkipProjectRoot: !trust.Trusted,
	}
	if strings.TrimSpace(workspace) != "" {
		opts.ProjectRoot = workspace
	}

	switch action {
	case "list", "ls":
		catalog, err := agentdef.Discover(opts)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		names := make([]string, 0, len(catalog.Order))
		names = append(names, catalog.Order...)
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"action":         "list",
			"source":         "server_agentdef_registry",
			"workspace_path": workspace,
			"profile_root":   profileRoot,
			"count":          len(names),
			"names":          names,
			"text":           agentdef.RenderCatalog(catalog),
		})
	case "show":
		if name == "" {
			h.writeError(w, http.StatusBadRequest, errAgentDefinitionNameRequired)
			return
		}
		def, err := agentdef.Resolve(name, opts)
		if err != nil {
			h.writeError(w, http.StatusNotFound, err)
			return
		}
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"action":         "show",
			"source":         "server_agentdef_registry",
			"workspace_path": workspace,
			"profile_root":   profileRoot,
			"name":           def.Name,
			"definition":     def,
			"text":           agentdef.RenderDefinition(def),
		})
	case "lint", "check":
		catalog, err := agentdef.Discover(opts)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		issues := agentdef.LintCatalog(catalog)
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"action":         "lint",
			"source":         "server_agentdef_registry",
			"workspace_path": workspace,
			"profile_root":   profileRoot,
			"count":          len(issues),
			"issues":         issues,
			"text":           agentdef.RenderLint(catalog),
		})
	default:
		h.writeError(w, http.StatusBadRequest, errAgentDefinitionAction)
	}
}

type agentDefinitionRequestError string

func (e agentDefinitionRequestError) Error() string { return string(e) }

const (
	errAgentDefinitionNameRequired agentDefinitionRequestError = "agent definition name is required"
	errAgentDefinitionAction       agentDefinitionRequestError = "action must be list, show, or lint"
)
