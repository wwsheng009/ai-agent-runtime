package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Tool search / dynamic listing (C4b).
//
// Policy:
//   - Optional ListableTool / metadata should_list|list_when filter every catalog path.
//   - When catalog size >= toolkit.DefaultToolSearchThreshold, inject search_tool and
//     keep core tools listed while projecting non-core / defer_loading tools out of
//     the direct model surface (they remain executable and searchable).
//   - Broker / MCP tools stay callable even when projected; search_tool returns schemas.
const (
	toolSearchName = toolkit.ToolSearchName
)

// listToolsContextForAgent builds the listing context used by ShouldList filters.
func listToolsContextForAgent(ctx context.Context, agent *Agent, catalogSize int) toolkit.ListToolsContext {
	listCtx := toolkit.ListToolsContext{
		CatalogSize: catalogSize,
	}
	if ctx != nil {
		listCtx.PermissionMode = strings.TrimSpace(string(permissionModeFromContext(ctx)))
		if runMeta, ok := team.GetRunMeta(ctx); ok && runMeta != nil && runMeta.Team != nil {
			listCtx.TeamActive = strings.TrimSpace(runMeta.Team.TeamID) != ""
		}
	}
	if agent != nil {
		if policy := agent.GetToolExecutionPolicy(); policy != nil {
			listCtx.ReadOnly = policy.ReadOnly
		}
	}
	// Plan mode is communicated via permission mode string when hosts set it.
	if strings.EqualFold(listCtx.PermissionMode, "plan") {
		listCtx.PlanMode = true
	}
	return toolkit.ListToolsContextFromContext(ctx, listCtx)
}

func ensureSearchToolPresent(tools []types.ToolDefinition) []types.ToolDefinition {
	for _, def := range tools {
		if strings.EqualFold(strings.TrimSpace(def.Name), toolSearchName) {
			return tools
		}
	}
	return append(tools, searchToolDefinition())
}

func searchToolVisibleInTurn(ctx context.Context) bool {
	for _, def := range frozenTurnToolSurface(ctx) {
		if strings.EqualFold(strings.TrimSpace(def.Name), toolSearchName) {
			return true
		}
	}
	return false
}

func filterToolDefinitionsByShouldList(tools []types.ToolDefinition, listCtx toolkit.ListToolsContext) []types.ToolDefinition {
	if len(tools) == 0 {
		return nil
	}
	filtered := make([]types.ToolDefinition, 0, len(tools))
	for _, def := range tools {
		if toolkit.ShouldListMetadata(def.Metadata, listCtx) {
			filtered = append(filtered, def)
		}
	}
	return filtered
}

func projectToolSurfaceWithSearch(tools []types.ToolDefinition, threshold int) []types.ToolDefinition {
	if len(tools) == 0 {
		return nil
	}
	if threshold <= 0 {
		threshold = toolkit.DefaultToolSearchThreshold
	}
	// Always ensure search_tool is not duplicated if already present.
	hasSearch := false
	for _, def := range tools {
		if strings.EqualFold(strings.TrimSpace(def.Name), toolSearchName) {
			hasSearch = true
			break
		}
	}
	if len(tools) < threshold {
		// Small catalogs: list everything that passed ShouldList; do not inject search.
		if hasSearch {
			// Keep explicit search_tool if a host already registered one.
			return tools
		}
		return tools
	}

	projected := make([]types.ToolDefinition, 0, len(tools)+1)
	for _, def := range tools {
		name := strings.TrimSpace(def.Name)
		if name == "" {
			continue
		}
		if strings.EqualFold(name, toolSearchName) {
			hasSearch = true
			projected = append(projected, def)
			continue
		}
		if toolkit.IsCoreTool(def.Metadata, name) {
			projected = append(projected, def)
			continue
		}
		// Non-core tools with defer_loading (or generic non-core) are hidden from
		// the direct surface when search projection is active.
		if deferred, ok := metadataBoolValue(def.Metadata, toolkit.MetaDeferLoading); ok && !deferred {
			// Explicit defer_loading=false keeps the tool listed even when non-core.
			projected = append(projected, def)
			continue
		}
		// Default for non-core under large catalog: project out (searchable).
		_ = def
	}
	return ensureSearchToolPresent(projected)
}

func searchToolDefinition() types.ToolDefinition {
	tool := toolkit.NewSearchTool(nil)
	return types.ToolDefinition{
		Name:        tool.Name(),
		Description: tool.Description(),
		Parameters:  tool.Parameters(),
		Metadata:    tool.DefinitionMetadata(),
	}
}

func buildToolSearchIndex(tools []types.ToolDefinition) *toolkit.InMemoryToolSearchIndex {
	entries := make([]toolkit.ToolSearchEntry, 0, len(tools))
	for _, def := range tools {
		name := strings.TrimSpace(def.Name)
		if name == "" || strings.EqualFold(name, toolSearchName) {
			continue
		}
		server := ""
		if def.Metadata != nil {
			if raw, ok := def.Metadata["mcp_name"].(string); ok {
				server = strings.TrimSpace(raw)
			} else if raw, ok := def.Metadata["server_name"].(string); ok {
				server = strings.TrimSpace(raw)
			} else if raw, ok := def.Metadata[toolresult.SourceKey].(string); ok {
				server = strings.TrimSpace(raw)
			}
		}
		entries = append(entries, toolkit.ToolSearchEntry{
			Name:        name,
			Description: def.Description,
			ServerName:  server,
			Parameters:  def.Parameters,
			Metadata:    def.Metadata,
		})
	}
	return toolkit.NewInMemoryToolSearchIndex(entries)
}

func executeSearchTool(args map[string]interface{}, catalog []types.ToolDefinition) (string, map[string]interface{}, error) {
	index := buildToolSearchIndex(catalog)
	tool := toolkit.NewSearchTool(index)
	result, err := tool.Execute(context.Background(), args)
	if result == nil {
		if err != nil {
			return "", nil, err
		}
		return "", nil, fmt.Errorf("search_tool returned empty result")
	}
	meta := result.MetadataWithOutputKind()
	if err != nil {
		return result.Content, meta, err
	}
	// Prefer structured content; fall back to re-marshal if empty.
	if strings.TrimSpace(result.Content) != "" {
		return result.Content, meta, nil
	}
	payload, marshalErr := json.Marshal(map[string]interface{}{
		"results": []interface{}{},
		"query":   args["query"],
	})
	if marshalErr != nil {
		return "", meta, marshalErr
	}
	return string(payload), meta, nil
}

func metadataBoolValue(metadata map[string]interface{}, key string) (bool, bool) {
	if len(metadata) == 0 {
		return false, false
	}
	raw, ok := metadata[key]
	if !ok {
		return false, false
	}
	switch typed := raw.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "on":
			return true, true
		case "false", "0", "no", "off":
			return false, true
		}
	}
	return false, false
}

// fullCatalogForSearch rebuilds the pre-projection catalog for search_tool execution.
// It intentionally skips goal projection and search projection so hidden tools remain findable.
func (loop *ReActLoop) fullCatalogForSearch(ctx context.Context, toolWhitelist []string) []types.ToolDefinition {
	if loop == nil || loop.agent == nil {
		return nil
	}
	allowed := whitelistSet(toolWhitelist)
	tools := make([]types.ToolDefinition, 0, 8)
	seen := make(map[string]bool)

	if loop.agent.mcpManager != nil {
		for _, mt := range loop.agent.mcpManager.ListTools() {
			if len(allowed) > 0 && !allowed[mt.Name] {
				continue
			}
			if policy := loop.agent.GetToolExecutionPolicy(); policy != nil && policy.AllowToolInfo(mt) != nil {
				continue
			}
			if seen[mt.Name] {
				continue
			}
			seen[mt.Name] = true
			definition := types.ToolDefinition{
				Name:        mt.Name,
				Description: mt.Description,
				Parameters:  normalizeToolParameters(mt.InputSchema),
				Metadata:    cloneInterfaceMap(mt.Metadata),
			}
			if source := resolveToolSourceForRequest(loop.agent, mt.Name); source != "" {
				if definition.Metadata == nil {
					definition.Metadata = map[string]interface{}{}
				}
				definition.Metadata[toolresult.SourceKey] = source
			}
			if strings.TrimSpace(mt.MCPName) != "" {
				if definition.Metadata == nil {
					definition.Metadata = map[string]interface{}{}
				}
				definition.Metadata["mcp_name"] = mt.MCPName
			}
			tools = append(tools, definition)
		}
	}

	if scheduler := loop.agent.GetSubagentScheduler(); scheduler != nil {
		if (len(allowed) == 0 || allowed["spawn_subagents"]) &&
			(loop.agent.GetToolExecutionPolicy() == nil || loop.agent.GetToolExecutionPolicy().AllowsDefinition("spawn_subagents")) {
			definition := spawnSubagentsToolDefinition()
			if !seen[definition.Name] {
				seen[definition.Name] = true
				tools = append(tools, definition)
			}
		}
	}

	if broker := loop.agent.GetToolBroker(); broker != nil {
		for _, def := range broker.DefinitionsForContext(ctx) {
			if len(allowed) > 0 && !allowed[def.Name] {
				continue
			}
			if policy := loop.agent.GetToolExecutionPolicy(); policy != nil && !policy.AllowsDefinition(def.Name) {
				continue
			}
			if seen[def.Name] {
				continue
			}
			seen[def.Name] = true
			tools = append(tools, def)
		}
	}

	listCtx := listToolsContextForAgent(ctx, loop.agent, len(tools))
	tools = filterToolDefinitionsByShouldList(tools, listCtx)
	tools = optimizeModelToolSurface(tools)
	sortToolDefinitionsByName(tools)
	return tools
}
