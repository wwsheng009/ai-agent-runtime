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

// Tool listing helpers.
//
// Policy (2026-09-18): there is no automatic directory-size search projection.
// Every tool that passes should_list/list_when and the execution policy is listed
// directly, including all enabled MCP tools. search_tool is only usable when a
// host registers it explicitly; metadata-hidden tools remain hidden by design.
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
			if allowed != nil && !allowed[mt.Name] {
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
		if shouldExposeSpawnSubagents(loop.agent, allowed) {
			definition := spawnSubagentsToolDefinition()
			if !seen[definition.Name] {
				seen[definition.Name] = true
				tools = append(tools, definition)
			}
		}
	}

	if broker := loop.agent.GetToolBroker(); broker != nil {
		for _, def := range broker.DefinitionsForContext(ctx) {
			if allowed != nil && !allowed[def.Name] {
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

	// Drop anything the execution policy would deny before the request is built,
	// including an injected spawn_subagents the policy blocks via delegation.
	tools, _ = filterPolicyBlockedToolDefinitions(tools, loop.agent.GetToolExecutionPolicy())

	listCtx := listToolsContextForAgent(ctx, loop.agent, len(tools))
	tools = filterToolDefinitionsByShouldList(tools, listCtx)
	tools = optimizeModelToolSurface(tools)
	sortToolDefinitionsByName(tools)
	return tools
}
