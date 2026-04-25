package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
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

	sort.Slice(toolsList, func(i, j int) bool {
		return toolsList[i].Name < toolsList[j].Name
	})

	return toolsList
}

// Execute runs a tool by name, preferring MCP tools over toolkit tools.
func (m *Manager) Execute(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	output, _, err := m.ExecuteWithMeta(ctx, name, args)
	return output, err
}

// ExecuteWithMeta runs a tool and preserves structured metadata for runtime callers.
func (m *Manager) ExecuteWithMeta(ctx context.Context, name string, args map[string]interface{}) (string, map[string]interface{}, error) {
	if name == "list_mcp_resources" {
		metadata := toolresult.WithSource(toolresult.WithKind(nil, toolresult.KindText), toolresult.SourceMeta)
		output, err := m.listMCPResources(ctx, args)
		if strings.TrimSpace(output) != "" {
			metadata["output_size"] = len(output)
		}
		return output, metadata, err
	}

	if m.toolkit != nil {
		if tool, ok := m.toolkit.Get(name); ok {
			if m.shouldPreferLocalToolkitTool(name) {
				result, err := tool.Execute(ctx, args)
				if err != nil {
					return "", nil, err
				}
				return formatToolkitResultWithSource(result, toolresult.SourceToolkit)
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
						return "", nil, execErr
					}
					return formatToolkitResultWithSource(result, toolresult.SourceToolkit)
				}
			}
			result, callErr := m.mcp.CallTool(ctx, info.MCPName, name, args)
			if callErr != nil {
				return "", nil, callErr
			}
			return formatMCPResultWithSource(result, toolresult.SourceMCP)
		}
	}

	if m.toolkit != nil {
		if tool, ok := m.toolkit.Get(name); ok {
			result, err := tool.Execute(ctx, args)
			if err != nil {
				return "", nil, err
			}
			return formatToolkitResultWithSource(result, toolresult.SourceToolkit)
		}
	}

	return "", nil, fmt.Errorf("tool '%s' not found", name)
}

func (m *Manager) resolveToolSource(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if name == "list_mcp_resources" {
		return toolresult.SourceMeta
	}
	if m.toolkit != nil {
		if _, ok := m.toolkit.Get(name); ok && m.shouldPreferLocalToolkitTool(name) {
			return toolresult.SourceToolkit
		}
	}
	if m.mcp != nil {
		info, err := m.mcp.FindTool(name)
		if err == nil && info != nil {
			if m.shouldPreferLocalToolkit(info.MCPName, name) {
				return toolresult.SourceToolkit
			}
			return toolresult.SourceMCP
		}
	}
	if m.toolkit != nil {
		if _, ok := m.toolkit.Get(name); ok {
			return toolresult.SourceToolkit
		}
	}
	return ""
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
	register(tools.NewApplyPatchTool())
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

func formatToolkitResult(result *toolkit.ToolResult) (string, map[string]interface{}, error) {
	if result == nil {
		return "", nil, nil
	}
	metadata := result.MetadataWithOutputKind()
	if result.Error != nil {
		return result.Content, metadata, result.Error
	}
	if result.Success {
		switch result.NormalizedOutputKind() {
		case toolresult.KindText:
			if result.Content != "" {
				return result.Content, metadata, nil
			}
			return "", metadata, nil
		case toolresult.KindEmpty:
			return "", metadata, nil
		case toolresult.KindStructured:
			if strings.TrimSpace(result.Content) != "" {
				return result.Content, metadata, nil
			}
			if data, err := result.ToJSON(); err == nil && len(data) > 0 {
				return string(data), metadata, nil
			}
			return "", metadata, nil
		case toolresult.KindBinary:
			if data, err := result.ToJSON(); err == nil && len(data) > 0 {
				return string(data), metadata, nil
			}
			return "", metadata, nil
		}
		if data, err := result.ToJSON(); err == nil && len(data) > 0 {
			return string(data), metadata, nil
		}
		return "", metadata, nil
	}
	return "", metadata, fmt.Errorf("tool execution failed")
}

func formatToolkitResultWithSource(result *toolkit.ToolResult, source string) (string, map[string]interface{}, error) {
	output, metadata, err := formatToolkitResult(result)
	return output, toolresult.WithSource(metadata, source), err
}

func formatMCPResult(result *protocol.CallToolResult) (string, map[string]interface{}, error) {
	if result == nil {
		return "", nil, nil
	}
	metadata := cloneMetadataMap(result.Meta)
	if kind := toolresult.KindFromMetadata(metadata); kind != "" {
		metadata = toolresult.WithKind(metadata, kind)
	}
	if len(result.Content) == 1 && result.Content[0].Type == "text" {
		text := result.Content[0].Text
		if result.IsError {
			if strings.TrimSpace(text) == "" {
				return "", metadata, errors.New("tool execution failed")
			}
			return text, metadata, errors.New(text)
		}
		return text, metadata, nil
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
	rendered := strings.TrimSpace(output.String())

	if result.IsError {
		if rendered == "" {
			return "", metadata, errors.New("tool execution failed")
		}
		return rendered, metadata, errors.New(rendered)
	}
	return rendered, metadata, nil
}

func formatMCPResultWithSource(result *protocol.CallToolResult, source string) (string, map[string]interface{}, error) {
	output, metadata, err := formatMCPResult(result)
	return output, toolresult.WithSource(metadata, source), err
}

func cloneMetadataMap(input map[string]any) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
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
