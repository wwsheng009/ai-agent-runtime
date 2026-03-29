package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ai-gateway/ai-agent-runtime/internal/mcp/manager"
	"github.com/ai-gateway/ai-agent-runtime/internal/mcp/protocol"
	runtimecfg "github.com/ai-gateway/ai-agent-runtime/internal/config"
	runtimeexecutor "github.com/ai-gateway/ai-agent-runtime/internal/executor"
	"github.com/ai-gateway/ai-agent-runtime/internal/llm/adapter"
	"github.com/ai-gateway/ai-agent-runtime/internal/toolkit"
	"github.com/ai-gateway/ai-agent-runtime/internal/toolkit/tools"
)

// ToolDescriptor describes a tool available to the runtime.
type ToolDescriptor struct {
	Name        string
	Description string
	Parameters  map[string]interface{}
}

// Manager unifies toolkit tools and MCP tools.
type Manager struct {
	toolkit *toolkit.Registry
	mcp     manager.Manager
	sandbox *runtimeexecutor.Sandbox
}

const toolkitMCPName = "toolkit"

var localToolkitPriorityTools = map[string]struct{}{
	"ls":   {},
	"glob": {},
	"grep": {},
	"view": {},
}

// NewDefaultManager registers the built-in toolkit tools and merges MCP tools.
func NewDefaultManager(mcp manager.Manager) *Manager {
	return NewDefaultManagerWithRuntimeConfig(mcp, nil)
}

// NewDefaultManagerWithRuntimeConfig registers built-in toolkit tools with optional sandbox policy.
func NewDefaultManagerWithRuntimeConfig(mcp manager.Manager, config *runtimecfg.RuntimeConfig) *Manager {
	registry := toolkit.NewRegistry()
	var sandbox *runtimeexecutor.Sandbox
	workspaceRoot := ""
	if config != nil {
		sandbox = runtimeexecutor.NewSandbox(&config.Sandbox)
		workspaceRoot = strings.TrimSpace(config.Workspace.Root)
	}
	registerBuiltinToolkitTools(registry, sandbox, workspaceRoot)
	return &Manager{
		toolkit: registry,
		mcp:     mcp,
		sandbox: sandbox,
	}
}

// ListTools returns the unified tool list, preferring MCP tools on name conflict.
func (m *Manager) ListTools() []ToolDescriptor {
	seen := make(map[string]struct{})
	toolsList := make([]ToolDescriptor, 0)

	if m.mcp != nil {
		for _, info := range m.mcp.ListTools() {
			if !info.Enabled || info.Tool == nil {
				continue
			}
			if m.shouldPreferLocalToolkit(info.MCPName, info.Tool.Name) {
				continue
			}
			if _, exists := seen[info.Tool.Name]; exists {
				continue
			}
			seen[info.Tool.Name] = struct{}{}
			toolsList = append(toolsList, ToolDescriptor{
				Name:        info.Tool.Name,
				Description: info.Tool.Description,
				Parameters:  normalizeParameters(info.Tool.InputSchema),
			})
		}
	}

	if m.toolkit != nil {
		for _, tool := range m.toolkit.List() {
			name := tool.Name()
			if _, exists := seen[name]; exists {
				continue
			}
			seen[name] = struct{}{}
			toolsList = append(toolsList, ToolDescriptor{
				Name:        name,
				Description: tool.Description(),
				Parameters:  normalizeParameters(tool.Parameters()),
			})
		}
	}

	if meta := m.metaToolDescriptor(); meta != nil {
		if _, exists := seen[meta.Name]; !exists {
			toolsList = append(toolsList, *meta)
		}
	}

	return toolsList
}

// Execute runs a tool by name, preferring MCP tools over toolkit tools.
func (m *Manager) Execute(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	if name == "list_mcp_resources" {
		return m.listMCPResources(ctx, args)
	}

	if m.toolkit != nil {
		if tool, ok := m.toolkit.Get(name); ok {
			if m.shouldPreferLocalToolkitTool(name) {
				result, err := tool.Execute(ctx, args)
				if err != nil {
					return "", err
				}
				return formatToolkitResult(result)
			}
		}
	}

	if m.mcp != nil {
		info, err := m.mcp.FindTool(name)
		if err == nil && info != nil {
			if m.shouldPreferLocalToolkit(info.MCPName, name) && m.toolkit != nil {
				if tool, ok := m.toolkit.Get(name); ok {
					result, execErr := tool.Execute(ctx, args)
					if execErr != nil {
						return "", execErr
					}
					return formatToolkitResult(result)
				}
			}
			result, callErr := m.mcp.CallTool(ctx, info.MCPName, name, args)
			if callErr != nil {
				return "", callErr
			}
			return formatMCPResult(result)
		}
	}

	if m.toolkit != nil {
		if tool, ok := m.toolkit.Get(name); ok {
			result, err := tool.Execute(ctx, args)
			if err != nil {
				return "", err
			}
			return formatToolkitResult(result)
		}
	}

	return "", fmt.Errorf("tool '%s' not found", name)
}

func (m *Manager) shouldPreferLocalToolkit(mcpName, toolName string) bool {
	if m == nil || m.toolkit == nil {
		return false
	}
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return false
	}
	_, ok := m.toolkit.Get(toolName)
	if !ok {
		return false
	}
	if m.shouldForceLocalToolkitTool(toolName) {
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(mcpName), toolkitMCPName) {
		return false
	}
	return true
}

func (m *Manager) shouldForceLocalToolkitTool(toolName string) bool {
	if m == nil {
		return false
	}
	_, ok := localToolkitPriorityTools[strings.ToLower(strings.TrimSpace(toolName))]
	return ok
}

func (m *Manager) shouldPreferLocalToolkitTool(toolName string) bool {
	return m.shouldPreferLocalToolkit(toolkitMCPName, toolName)
}

func registerBuiltinToolkitTools(registry *toolkit.Registry, sandbox *runtimeexecutor.Sandbox, workspaceRoot string) {
	register := func(tool toolkit.Tool) {
		if configurable, ok := tool.(interface {
			SetSandbox(*runtimeexecutor.Sandbox)
		}); ok {
			configurable.SetSandbox(sandbox)
		}
		if configurable, ok := tool.(interface {
			SetBasePath(string)
		}); ok {
			configurable.SetBasePath(workspaceRoot)
		}
		_ = registry.Register(tool)
	}

	// Ignore duplicates; registry will reject with error.
	register(tools.NewBashTool())
	register(tools.NewExecuteShellCommandTool())
	register(tools.NewViewTool())
	register(tools.NewEditTool())
	register(tools.NewWriteTool())
	register(tools.NewGlobTool())
	register(tools.NewGrepTool())
	register(tools.NewLsTool())
	register(tools.NewDownloadTool())
	register(tools.NewFetchTool())
	register(tools.NewMultieditTool())
	register(tools.NewTodosTool())
	register(tools.NewSourcegraphTool())
	register(tools.NewWebSearchTool())
}

func formatToolkitResult(result *toolkit.ToolResult) (string, error) {
	if result == nil {
		return "", nil
	}
	if result.Success {
		if result.Content != "" {
			return result.Content, nil
		}
		if data, err := result.ToJSON(); err == nil && len(data) > 0 {
			return string(data), nil
		}
		return "", nil
	}
	if result.Error != nil {
		return "", result.Error
	}
	return "", fmt.Errorf("tool execution failed")
}

func formatMCPResult(result *protocol.CallToolResult) (string, error) {
	if result == nil {
		return "", nil
	}
	if len(result.Content) == 1 && result.Content[0].Type == "text" {
		if result.IsError {
			return "", errors.New(result.Content[0].Text)
		}
		return result.Content[0].Text, nil
	}

	var output strings.Builder
	for _, content := range result.Content {
		switch content.Type {
		case "text":
			output.WriteString(content.Text)
			output.WriteString("\n")
		case "image":
			output.WriteString(fmt.Sprintf("image: %s\n", content.MIMEType))
		case "resource":
			output.WriteString(fmt.Sprintf("resource: %s\n", content.URI))
		default:
			output.WriteString(fmt.Sprintf("content: %s\n", content.Type))
		}
	}

	if result.IsError {
		return "", errors.New(output.String())
	}
	return strings.TrimSpace(output.String()), nil
}

func (m *Manager) metaToolDescriptor() *ToolDescriptor {
	meta := adapter.BuildMCPMetaTools()
	if len(meta) == 0 {
		return nil
	}
	tool := meta[0]
	name, _ := tool["name"].(string)
	if name == "" {
		return nil
	}
	desc, _ := tool["description"].(string)
	params, _ := tool["parameters"].(map[string]interface{})
	return &ToolDescriptor{
		Name:        name,
		Description: desc,
		Parameters:  normalizeParameters(params),
	}
}

func (m *Manager) listMCPResources(ctx context.Context, args map[string]interface{}) (string, error) {
	if m.mcp == nil {
		return "", fmt.Errorf("mcp manager not configured")
	}

	var server string
	if raw, ok := args["server"].(string); ok {
		server = strings.TrimSpace(raw)
	}

	var cursor *string
	if raw, ok := args["cursor"].(string); ok {
		trimmed := strings.TrimSpace(raw)
		if trimmed != "" {
			cursor = &trimmed
		}
	}

	if server != "" {
		result, err := m.mcp.ListResources(ctx, server, cursor)
		if err != nil {
			return "", err
		}
		payload := map[string]interface{}{
			"server":    server,
			"resources": formatResources(result.Resources),
		}
		if result.NextCursor != nil {
			payload["next_cursor"] = *result.NextCursor
		}
		data, _ := json.Marshal(payload)
		return string(data), nil
	}

	servers := make(map[string]interface{})
	for _, status := range m.mcp.ListMCPs() {
		if status == nil || !status.Enabled || status.Name == "" {
			continue
		}
		result, err := m.mcp.ListResources(ctx, status.Name, nil)
		entry := map[string]interface{}{}
		if err != nil {
			entry["error"] = err.Error()
		} else {
			entry["resources"] = formatResources(result.Resources)
			if result.NextCursor != nil {
				entry["next_cursor"] = *result.NextCursor
			}
		}
		servers[status.Name] = entry
	}

	payload := map[string]interface{}{
		"servers": servers,
	}
	if cursor != nil {
		payload["warning"] = "cursor is only supported when server is specified"
	}

	data, _ := json.Marshal(payload)
	return string(data), nil
}

func formatResources(resources []*protocol.Resource) []map[string]interface{} {
	if len(resources) == 0 {
		return []map[string]interface{}{}
	}
	out := make([]map[string]interface{}, 0, len(resources))
	for _, res := range resources {
		if res == nil {
			continue
		}
		item := map[string]interface{}{
			"uri":  res.URI,
			"name": res.Name,
		}
		if res.Description != "" {
			item["description"] = res.Description
		}
		if res.MIMEType != "" {
			item["mimeType"] = res.MIMEType
		}
		if res.Annotations != nil {
			item["annotations"] = res.Annotations
		}
		out = append(out, item)
	}
	return out
}

func normalizeParameters(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return map[string]interface{}{
			"type":                 "object",
			"additionalProperties": false,
		}
	}

	normalized := make(map[string]interface{}, len(schema)+1)
	for key, value := range schema {
		normalized[key] = value
	}

	if _, ok := normalized["type"]; !ok {
		normalized["type"] = "object"
	}
	if paramType, ok := normalized["type"].(string); ok && paramType == "object" {
		if _, ok := normalized["additionalProperties"]; !ok {
			normalized["additionalProperties"] = false
		}
	}

	return normalized
}
