package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
)

// MCPManager MCP 管理器全局实例
var MCPManagerInstance manager.Manager
var mcpManagerConfigPath string

// initMCPManager 初始化 MCP 管理器
func initMCPManager(configPath string) error {
	configPath = strings.TrimSpace(configPath)
	if MCPManagerInstance != nil {
		if configPath == "" || configPath == mcpManagerConfigPath {
			return nil
		}
		_ = MCPManagerInstance.Stop()
		MCPManagerInstance = nil
		mcpManagerConfigPath = ""
	}
	if MCPManagerInstance != nil {
		return nil
	}

	// 查找 MCP 配置文件
	configPath = resolveMCPConfigPath(configPath)
	if configPath == "" {
		// 未找到配置文件，不报错，只是不启用 MCP
		return nil
	}

	// 创建管理器
	MCPManagerInstance = manager.NewManager()
	mcpManagerConfigPath = configPath

	// 加载配置
	if err := MCPManagerInstance.LoadConfig(configPath); err != nil {
		return fmt.Errorf("加载 MCP 配置失败: %w", err)
	}

	// 启动所有启用的 MCP
	ctx := context.Background()
	if err := MCPManagerInstance.Start(ctx); err != nil {
		return fmt.Errorf("启动 MCP 失败: %w", err)
	}

	return nil
}

// findMCPConfigPath 查找 MCP 配置文件
func findMCPConfigPath() string {
	// 可能的配置文件路径
	paths := []string{
		filepath.Join("configs", "mcp.yaml"),
		filepath.Join(".", "mcp.yaml"),
		filepath.Join("~", ".aicli", "mcp.yaml"),
		filepath.Join("~", ".config", "aicli", "mcp.yaml"),
	}

	for _, p := range paths {
		// 展开 ~
		fullPath := p
		if p[0] == '~' {
			home, err := os.UserHomeDir()
			if err != nil {
				continue
			}
			fullPath = filepath.Join(home, p[2:])
		}

		// 检查文件是否存在
		if _, err := os.Stat(fullPath); err == nil {
			return fullPath
		}
	}

	return ""
}

func resolveMCPConfigPath(explicit string) string {
	if trimmed := strings.TrimSpace(explicit); trimmed != "" {
		return trimmed
	}
	return findMCPConfigPath()
}

func resolveChatMCPConfigPath(cfg *config.Config, session *ChatSession) string {
	if session != nil && strings.TrimSpace(session.MCPConfigPath) != "" {
		return strings.TrimSpace(session.MCPConfigPath)
	}
	if cfg != nil && cfg.AICLI != nil && cfg.AICLI.MCP != nil && strings.TrimSpace(cfg.AICLI.MCP.ConfigFile) != "" {
		return strings.TrimSpace(cfg.AICLI.MCP.ConfigFile)
	}
	return findMCPConfigPath()
}

// registerMCPTools 注册 MCP 工具到 FunctionRegistry
func registerMCPTools(registry *functions.FunctionRegistry) error {
	if MCPManagerInstance == nil {
		return nil
	}

	// 获取 MCP 工具列表
	tools := MCPManagerInstance.ListTools()

	for _, info := range tools {
		if !info.Enabled {
			continue
		}

		// 创建 MCPFunction 实现 functions.Function 接口
		fn := &MCPFunction{
			mcpName:  info.MCPName,
			toolName: info.Tool.Name,
			name:     info.Tool.Name,
			desc:     info.Tool.Description,
			schema:   info.Tool.InputSchema,
		}

		// 注册到 FunctionRegistry
		registry.Register(fn)
	}

	return nil
}

// MCPFunction MCP 工具的 Function 实现
type MCPFunction struct {
	mcpName  string
	toolName string
	name     string
	desc     string
	schema   map[string]interface{}
}

// Name 返回 Function 名称
func (f *MCPFunction) Name() string {
	return f.name
}

// Description 返回 Function 描述
func (f *MCPFunction) Description() string {
	return f.desc
}

// Parameters 返回 Function 参数的 JSON Schema 描述
func (f *MCPFunction) Parameters() map[string]interface{} {
	return f.schema
}

// Execute 执行 MCP 工具
func (f *MCPFunction) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	// 调用 MCP 工具
	result, err := MCPManagerInstance.CallTool(ctx, f.mcpName, f.toolName, args)
	if err != nil {
		return "", fmt.Errorf("调用 MCP 工具 '%s' 失败: %w", f.toolName, err)
	}

	// 转换结果格式
	output := convertMCPResult(result)
	return output, nil
}

// convertMCPResult 转换 MCP 结果为 Function 格式
func convertMCPResult(result *protocol.CallToolResult) string {
	// MCP 返回 Content[]，我们需要转换为 Function 期望的格式
	if result == nil {
		return ""
	}

	// 如果只有一个内容且是文本，直接返回文本
	if len(result.Content) == 1 && result.Content[0].Type == "text" {
		return result.Content[0].Text
	}

	// 否则返回结构化数据格式的 JSON 字符串
	output := make([]map[string]interface{}, 0)
	for _, content := range result.Content {
		item := map[string]interface{}{
			"type": content.Type,
		}

		switch content.Type {
		case "text":
			item["text"] = content.Text
		case "image":
			item["data"] = content.Data
			item["mimeType"] = content.MIMEType
		case "resource":
			item["uri"] = content.URI
			item["text"] = content.Text
		}

		output = append(output, item)
	}

	// 简单的 JSON 格式化
	if len(output) == 0 {
		return ""
	}

	// 转换为字符串显示
	resultStr := ""
	for _, item := range output {
		if item["type"] == "text" {
			resultStr += item["text"].(string) + "\n"
		} else {
			resultStr += fmt.Sprintf("%+v\n", item)
		}
	}

	return resultStr
}

// StopMCPManager 停止 MCP 管理器
func StopMCPManager() error {
	if MCPManagerInstance == nil {
		return nil
	}
	err := MCPManagerInstance.Stop()
	MCPManagerInstance = nil
	mcpManagerConfigPath = ""
	return err
}

// GetMCPStatus 获取 MCP 状态信息
func GetMCPStatus() (tools int, mcpCount int, err error) {
	if MCPManagerInstance == nil {
		return 0, 0, nil
	}

	toolsList := MCPManagerInstance.ListTools()
	mcps := MCPManagerInstance.ListMCPs()

	return len(toolsList), len(mcps), nil
}

// MCPStatus MCP 状态
type MCPStatus struct {
	Enabled    bool   `json:"enabled"`
	ToolCount  int    `json:"toolCount"`
	MCPCount   int    `json:"mcpCount"`
	ConfigPath string `json:"configPath,omitempty"`
}

// Status 获取 MCP 状态
func Status() *MCPStatus {
	if MCPManagerInstance == nil {
		return &MCPStatus{
			Enabled: false,
		}
	}

	tools, mcps, _ := GetMCPStatus()
	return &MCPStatus{
		Enabled:    true,
		ToolCount:  tools,
		MCPCount:   mcps,
		ConfigPath: mcpManagerConfigPath,
	}
}

// GetMCPFunctionsAsOpenAIFormat 获取所有 MCP 工具并转换为 OpenAI 格式
func GetMCPFunctionsAsOpenAIFormat() ([]map[string]interface{}, error) {
	if MCPManagerInstance == nil {
		return nil, nil
	}

	tools := MCPManagerInstance.ListTools()
	result := make([]map[string]interface{}, 0)

	for _, info := range tools {
		if !info.Enabled {
			continue
		}

		toolFormat := map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        info.Tool.Name,
				"description": info.Tool.Description,
				"parameters":  convertMCPSchemaToOpenAIFormat(info.Tool.InputSchema),
			},
		}

		result = append(result, toolFormat)
	}

	return result, nil
}

// convertMCPSchemaToOpenAIFormat 转换 MCP Schema 为 OpenAI 格式
func convertMCPSchemaToOpenAIFormat(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		}
	}

	result := map[string]interface{}{
		"type": "object",
	}

	if props, ok := schema["properties"].(map[string]interface{}); ok {
		result["properties"] = props
	} else {
		result["properties"] = map[string]interface{}{}
	}

	if required, ok := schema["required"].([]interface{}); ok {
		result["required"] = required
	}

	return result
}
