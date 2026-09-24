package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/foldertrust"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	runtimeprofileinput "github.com/wwsheng009/ai-agent-runtime/internal/profileinput"
)

// MCPManager MCP 管理器全局实例
var MCPManagerInstance manager.Manager
var (
	mcpManagerConfigPath string
	// mcpManagerSelectionKey 记录当前实例生效的 profile 服务器选择指纹，
	// 选择变化时必须重建管理器，避免复用未过滤的旧实例。
	mcpManagerSelectionKey string
)

// initMCPManager 初始化 MCP 管理器
func initMCPManager(configPath string) error {
	return initMCPManagerWithSelection(configPath, runtimeprofileinput.ResolvedMCPSelection{}, false)
}

// initMCPManagerAsync 初始化 MCP 管理器，并触发后台并行建连后立即返回。
// 供 aicli chat/TUI 启动路径使用：不再等待全部 MCP 服务器就绪，
// MCP 工具随各客户端连接完成动态出现在工具面（ListTools 实时读取注册表）。
func initMCPManagerAsync(configPath string) error {
	return initMCPManagerWithSelection(configPath, runtimeprofileinput.ResolvedMCPSelection{}, true)
}

func initMCPManagerWithMode(configPath string, async bool) error {
	return initMCPManagerWithSelection(configPath, runtimeprofileinput.ResolvedMCPSelection{}, async)
}

// initMCPManagerWithSelection 初始化 MCP 管理器。selection 非空时先按 profile
// 的 use/exclude 过滤服务器再加载（被排除的服务器既不建连也不进入工具面）；
// selection 为空时保持原有文件加载路径不变（NFR-1 零变化）。
func initMCPManagerWithSelection(configPath string, selection runtimeprofileinput.ResolvedMCPSelection, async bool) error {
	configPath = strings.TrimSpace(configPath)
	selectionKey := mcpSelectionKey(selection)
	if MCPManagerInstance != nil {
		if configPath == "" || (configPath == mcpManagerConfigPath && selectionKey == mcpManagerSelectionKey) {
			return nil
		}
		_ = MCPManagerInstance.Stop()
		MCPManagerInstance = nil
		mcpManagerConfigPath = ""
		mcpManagerSelectionKey = ""
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
	mcpManagerSelectionKey = selectionKey

	// 加载配置
	if err := loadMCPConfigForSelection(MCPManagerInstance, configPath, selection); err != nil {
		return err
	}

	// 启动所有启用的 MCP：chat 启动路径走后台并行建连，其余调用方保持同步语义。
	ctx := context.Background()
	if err := startMCPManager(MCPManagerInstance, ctx, async); err != nil {
		return fmt.Errorf("启动 MCP 失败: %w", err)
	}
	wireChatMCPToolSurfaceInvalidation(MCPManagerInstance)

	return nil
}

// loadMCPConfigForSelection 加载 MCP 配置：无 profile 选择时沿用文件加载；
// 有选择时改为「读文件 → 过滤服务器 → 内存快照」路径。
func loadMCPConfigForSelection(mgr manager.Manager, configPath string, selection runtimeprofileinput.ResolvedMCPSelection) error {
	if mcpSelectionKey(selection) == "" {
		if err := mgr.LoadConfig(configPath); err != nil {
			return fmt.Errorf("加载 MCP 配置失败: %w", err)
		}
		return nil
	}
	scoped, ok := mgr.(manager.ScopedManager)
	if !ok {
		return fmt.Errorf("MCP 管理器不支持内存配置快照，无法应用 profile 服务器选择")
	}
	cfg, dropped, err := runtimeprofileinput.LoadMCPConfigSnapshot(configPath, selection)
	if err != nil {
		return fmt.Errorf("加载 MCP 配置失败: %w", err)
	}
	if err := scoped.LoadConfigFromConfig(cfg); err != nil {
		return fmt.Errorf("加载 MCP 配置失败: %w", err)
	}
	emitMCPSelectionNotice(dropped)
	return nil
}

// mcpSelectionKey 生成选择声明的稳定指纹；无有效声明时返回空串。
func mcpSelectionKey(selection runtimeprofileinput.ResolvedMCPSelection) string {
	use := normalizeSelectionKeyPart(selection.UseServers)
	exclude := normalizeSelectionKeyPart(selection.ExcludeServers)
	if use == "" && exclude == "" {
		return ""
	}
	return use + "|" + exclude
}

func normalizeSelectionKeyPart(values []string) string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			cleaned = append(cleaned, strings.ToLower(trimmed))
		}
	}
	sort.Strings(cleaned)
	return strings.Join(cleaned, ",")
}

// emitMCPSelectionNotice 把被 profile 选择过滤掉的服务器显式告知用户，
// 避免"配置了却静默不生效"。
func emitMCPSelectionNotice(dropped []string) {
	if len(dropped) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "Profile MCP selection: skipped server(s) %s\n", strings.Join(dropped, ", "))
}

// startMCPManager 优先使用 AsyncManager 的后台启动能力；不支持时回退同步 Start。
func startMCPManager(mgr manager.Manager, ctx context.Context, async bool) error {
	if async {
		if starter, ok := mgr.(manager.AsyncManager); ok {
			return starter.StartAsync(ctx)
		}
	}
	return mgr.Start(ctx)
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
	// 与 CLI（aicli mcp *）和 runtime-server 共用同一优先级解析：
	// ./.aicli/mcp.yaml > ~/.aicli/mcp.yaml > 显式覆盖 > 向上搜索 > configs/mcp.yaml。
	// 直接返回 cfg 里的字面值会让 chat 会话加载 configs/mcp.yaml，而管理面/服务端
	// 却在读写 .aicli/mcp.yaml，出现“面板改了、会话没变”的错位。
	if resolved := resolveConfiguredMCPConfigPath(cfg); resolved != "" {
		return resolved
	}
	return findMCPConfigPath()
}

func resolveChatMCPStartupConfigPath(cfg *config.Config, session *ChatSession) (string, bool) {
	configPath := strings.TrimSpace(resolveChatMCPConfigPath(cfg, session))
	if configPath == "" {
		return "", false
	}

	// Folder trust (R2): block project-scoped MCP configs when untrusted.
	// User-global (~/.aicli, ~/.config/aicli) paths remain allowed.
	if !sessionProjectScopeAllowed(session) {
		projectRoot := folderTrustProjectRoot(session)
		if foldertrust.IsProjectScopedPath(configPath, projectRoot) {
			return "", false
		}
	}

	if _, err := os.Stat(configPath); err == nil {
		return configPath, true
	} else if os.IsNotExist(err) {
		// 配置缺失：静默跳过 MCP 初始化（默认建连只针对实际存在的配置）。
		return "", false
	}

	return configPath, true
}

func prepareChatMCPManager(cfg *config.Config, session *ChatSession) error {
	configPath, shouldInit := resolveChatMCPStartupConfigPath(cfg, session)
	if !shouldInit {
		return StopMCPManager()
	}
	var selection runtimeprofileinput.ResolvedMCPSelection
	if session != nil {
		selection = session.ProfileMCPSelection
	}
	return initMCPManagerWithSelection(configPath, selection, true)
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
