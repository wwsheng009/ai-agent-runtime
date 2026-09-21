package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

var (
	mcpConfigFile   string
	mcpOutputFormat string
	mcpJSONOutput   bool

	// add 命令参数
	transportType  string
	addCommand     string
	addDescription string
	headers        []string
	authType       string

	// test-server 命令参数
	testServerShowStderr bool
)

// MCPManager 全局 MCP 管理器实例
var MCPManager manager.Manager

// MCPCommand MCP 命令
func MCPCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "管理 MCP (Model Context Protocol) 服务器",
		Long: `MCP 命令用于管理 Model Context Protocol 服务器，支持添加、列出、移除、启用、禁用 MCP 服务器。

子命令概览与配置说明见 docs/aicli/install.md（MCP 子命令概览）。`,
	}
	cmd.PersistentFlags().StringVarP(&mcpConfigFile, "config-file", "C", "", "MCP 配置文件路径")
	cmd.PersistentFlags().StringVar(&mcpOutputFormat, "output", "", "查询类子命令输出格式（text|json）")
	cmd.PersistentFlags().BoolVarP(&mcpJSONOutput, "json", "j", false, "兼容选项：等价于 --output json")

	// 添加 MCP
	addCmd := &cobra.Command{
		Use:   "add <名称> <URL|命令>",
		Short: "添加 MCP 服务器",
		Long: `添加 MCP 服务器

示例:
  # Streamable HTTP 传输 (MCP 2025-03-26 规范，推荐)
  aicli mcp add --transport streamable context7 https://mcp.context7.com/mcp

  # 本地 Streamable HTTP 端点 (如 mcp-chrome)
  aicli mcp add --transport streamable chrome-mcp http://127.0.0.1:12306/mcp

  # 传统 SSE 传输
  aicli mcp add --transport sse my-sse https://example.com/sse

  # WebSocket 传输 (ws:// 或 wss://)
  aicli mcp add --transport websocket my-mcp wss://example.com/mcp

  # stdio 传输 (本地进程)
  aicli mcp add --transport stdio -- npx chrome-devtools-mcp@latest

  # 带 Header
  aicli mcp add --transport streamable context7 https://mcp.context7.com/mcp --header "API_KEY: your-key"`,
		Args: cobra.MinimumNArgs(2),
		Run:  addMCP,
	}
	addCmd.Flags().StringVarP(&transportType, "transport", "t", "sse", "传输类型 (stdio, sse, websocket, streamable)")
	addCmd.Flags().StringVar(&addDescription, "description", "", "描述")
	addCmd.Flags().StringVar(&addCommand, "command", "", "启动命令 (stdio 类型使用)")
	addCmd.Flags().StringSliceVar(&headers, "header", []string{}, "HTTP 头部，格式: 'Key: Value'")
	addCmd.Flags().StringVar(&authType, "auth", "", "认证类型 (oauth)")

	// 移除 MCP
	removeCmd := &cobra.Command{
		Use:   "remove <mcp名称>",
		Short: "移除 MCP 服务器",
		Args:  cobra.ExactArgs(1),
		Run:   removeMCP,
	}

	// 列出所有 MCP
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "列出所有 MCP 服务器",
		Run:   listMCPs,
	}

	// MCP 状态
	statusCmd := &cobra.Command{
		Use:   "status [mcp名称]",
		Short: "查看 MCP 服务器状态",
		Args:  cobra.MaximumNArgs(1),
		Run:   mcpStatus,
	}

	// 启用 MCP
	enableCmd := &cobra.Command{
		Use:   "enable <mcp名称>",
		Short: "启用 MCP 服务器",
		Args:  cobra.ExactArgs(1),
		Run:   setMCPEnabled,
	}

	// 禁用 MCP
	disableCmd := &cobra.Command{
		Use:   "disable <mcp名称>",
		Short: "禁用 MCP 服务器",
		Args:  cobra.ExactArgs(1),
		Run:   setMCPDisabled,
	}

	// 列出工具
	toolsCmd := &cobra.Command{
		Use:   "tools [mcp名称]",
		Short: "列出 MCP 工具",
		Args:  cobra.MaximumNArgs(1),
		Run:   listTools,
	}

	// 测试工具调用
	testCmd := &cobra.Command{
		Use:   "test <mcp名称> <工具名称> [参数JSON]",
		Short: "测试 MCP 工具调用",
		Args:  cobra.MinimumNArgs(2),
		Run:   testTool,
	}

	// 测试服务器连接
	testServerCmd := &cobra.Command{
		Use:   "test-server <mcp名称>",
		Short: "测试 MCP 服务器连接",
		Args:  cobra.ExactArgs(1),
		Run:   testServer,
	}
	testServerCmd.Flags().BoolVar(&testServerShowStderr, "show-stderr", false,
		"显示 stdio 子进程 stderr 尾部（诊断启动失败或子进程噪声）")

	// 重新加载配置
	reloadCmd := &cobra.Command{
		Use:   "reload",
		Short: "重新加载 MCP 配置",
		Run:   reloadConfig,
	}

	cmd.AddCommand(addCmd, removeCmd, listCmd, statusCmd, enableCmd, disableCmd, toolsCmd, testCmd, testServerCmd, reloadCmd)

	return cmd
}

// getMCPConfigPath 获取 MCP 配置文件路径
func getMCPConfigPath() string {
	// 优先使用命令行指定的路径
	if mcpConfigFile != "" {
		return mcpConfigFile
	}

	// Reuse the root command's effective config. Re-scanning a default agent
	// config here would ignore an explicit --config selection and could cross
	// build-profile boundaries.
	if configured := resolveConfiguredMCPConfigPath(agentconfig.GetGlobalConfig()); configured != "" {
		if _, err := os.Stat(configured); err == nil {
			return configured
		}
	}

	// 默认路径（使用与 findMCPConfigPath 相同的优先级）
	paths := []string{
		filepath.Join("configs", "mcp.yaml"),
		filepath.Join(".", "mcp.yaml"),
		filepath.Join("~", ".aicli", "mcp.yaml"),
		filepath.Join("~", ".config", "aicli", "mcp.yaml"),
	}

	for _, p := range paths {
		if filepath.IsAbs(p) {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		} else if p[0] == '~' {
			home, _ := os.UserHomeDir()
			fullPath := filepath.Join(home, p[2:])
			if _, err := os.Stat(fullPath); err == nil {
				return fullPath
			}
		} else {
			// 相对路径
			if currentDir, err := os.Getwd(); err == nil {
				fullPath := filepath.Join(currentDir, p)
				if _, err := os.Stat(fullPath); err == nil {
					return fullPath
				}
			}
		}
	}

	return ""
}

// ensureMCPManager 确保 MCP 管理器已初始化
func ensureMCPManager() error {
	if MCPManager != nil {
		return nil
	}

	configPath := getMCPConfigPath()
	if configPath == "" {
		return fmt.Errorf("找不到 MCP 配置文件\n请创建配置文件或使用 --config 指定")
	}

	MCPManager = manager.NewManager()
	if err := MCPManager.LoadConfig(configPath); err != nil {
		return fmt.Errorf("加载 MCP 配置失败: %w", err)
	}

	// 启动 MCPs
	ctx := context.Background()
	if err := MCPManager.Start(ctx); err != nil {
		return fmt.Errorf("启动 MCP 失败: %w", err)
	}

	return nil
}

func prepareMCPOutput() func() {
	manager.SetStatusOutput(io.Discard)
	return func() {
		manager.SetStatusOutput(os.Stdout)
	}
}

func withMCPCommand(cmd *cobra.Command, fn func(options mcpCommandOptions)) {
	restoreOutput := prepareMCPOutput()
	defer restoreOutput()
	// 一次性 CLI 调用结束后必须关闭 MCP 客户端：部分 Streamable HTTP 服务端
	// （如 mcp-chrome）同一时刻只接受一个 transport，未关闭的会话会占死连接。
	defer shutdownMCPManager()
	registerExitCleanup(shutdownMCPManager)

	options, err := resolveMCPCommandOptions(cmd)
	if err != nil {
		exitCommandError("mcp", "json", err, nil)
	}
	fn(options)
}

// shutdownMCPManager 停止并释放全局 MCP 管理器（幂等）。
func shutdownMCPManager() {
	if MCPManager == nil {
		return
	}
	if err := MCPManager.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "[mcp] 关闭 MCP 连接失败: %v\n", err)
	}
	MCPManager = nil
}

type mcpToolOutput struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	MCPName     string                 `json:"mcp_name,omitempty"`
	Enabled     bool                   `json:"enabled"`
	InputSchema map[string]interface{} `json:"input_schema,omitempty"`
}

type mcpActionCommandResult struct {
	MCPName    string            `json:"mcpName,omitempty"`
	ConfigPath string            `json:"configPath,omitempty"`
	Enabled    *bool             `json:"enabled,omitempty"`
	Config     *config.MCPConfig `json:"config,omitempty"`
	Status     *config.MCPStatus `json:"status,omitempty"`
}

type mcpToolCommandResult struct {
	MCPName  string                   `json:"mcp_name"`
	ToolName string                   `json:"tool_name"`
	Args     map[string]interface{}   `json:"args,omitempty"`
	Result   *protocol.CallToolResult `json:"result,omitempty"`
}

type mcpServerCommandResult struct {
	Config  config.MCPConfig  `json:"config"`
	Status  *config.MCPStatus `json:"status,omitempty"`
	Tools   []mcpToolOutput   `json:"tools,omitempty"`
	Success bool              `json:"success"`
	// StderrTail 仅在 --show-stderr 时填充：stdio 子进程 stderr 尾部诊断。
	StderrTail string `json:"stderr_tail,omitempty"`
}

type mcpCommandOptions struct {
	OutputFormat string
	JSONEnvelope bool
}

type mcpAddCommandOptions struct {
	Name        string
	Target      string
	Transport   string
	Description string
	Command     string
	Headers     []string
	AuthType    string
	ExtraArgs   []string
}

func resolveMCPCommandOptions(cmd *cobra.Command) (mcpCommandOptions, error) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		return mcpCommandOptions{}, err
	}
	return mcpCommandOptions{
		OutputFormat: outputOptions.Format,
		JSONEnvelope: outputOptions.Envelope,
	}, nil
}

func collectMCPTools(filterName string) []*registry.ToolInfo {
	tools := MCPManager.ListTools()
	if strings.TrimSpace(filterName) == "" {
		return tools
	}
	filtered := make([]*registry.ToolInfo, 0, len(tools))
	for _, info := range tools {
		if info != nil && info.MCPName == filterName {
			filtered = append(filtered, info)
		}
	}
	return filtered
}

func buildMCPToolOutputs(tools []*registry.ToolInfo) []mcpToolOutput {
	result := make([]mcpToolOutput, 0, len(tools))
	for _, info := range tools {
		if info == nil {
			continue
		}
		result = append(result, mcpToolOutput{
			Name:        info.Tool.Name,
			Description: info.Tool.Description,
			MCPName:     info.MCPName,
			Enabled:     info.Enabled,
			InputSchema: info.Tool.InputSchema,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].MCPName == result[j].MCPName {
			return result[i].Name < result[j].Name
		}
		return result[i].MCPName < result[j].MCPName
	})
	return result
}

func runMCPListCommand() ([]*config.MCPStatus, error) {
	if err := ensureMCPManager(); err != nil {
		return nil, err
	}
	mcps := MCPManager.ListMCPs()
	sort.Slice(mcps, func(i, j int) bool {
		return mcps[i].Name < mcps[j].Name
	})
	return mcps, nil
}

func runMCPStatusCommand(name string) (interface{}, error) {
	if strings.TrimSpace(name) == "" {
		return runMCPListCommand()
	}
	if err := ensureMCPManager(); err != nil {
		return nil, err
	}
	return MCPManager.GetMCPStatus(name)
}

func runMCPToolsCommand(filterName string) ([]mcpToolOutput, error) {
	if err := ensureMCPManager(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(filterName) != "" {
		if _, err := MCPManager.GetMCPStatus(filterName); err != nil {
			return nil, err
		}
	}
	return buildMCPToolOutputs(collectMCPTools(filterName)), nil
}

func runMCPTestToolCommand(mcpName, toolName, jsonArg string) (*mcpToolCommandResult, error) {
	if err := ensureMCPManager(); err != nil {
		return nil, err
	}

	toolArgs := make(map[string]interface{})
	jsonArg = strings.TrimSpace(jsonArg)
	if jsonArg != "" {
		if err := json.Unmarshal([]byte(jsonArg), &toolArgs); err != nil {
			return nil, fmt.Errorf("参数 JSON 解析失败: %w", err)
		}
	}

	result, err := MCPManager.CallTool(context.Background(), mcpName, toolName, toolArgs)
	if err != nil {
		return nil, err
	}

	return &mcpToolCommandResult{
		MCPName:  mcpName,
		ToolName: toolName,
		Args:     toolArgs,
		Result:   result,
	}, nil
}

func runMCPTestServerCommand(name string, showStderr bool) (*mcpServerCommandResult, error) {
	configPath := getMCPConfigPath()
	if configPath == "" {
		return nil, fmt.Errorf("找不到 MCP 配置文件")
	}

	cfgLoader := config.NewLoader(configPath)
	cfg, err := cfgLoader.Load()
	if err != nil {
		return nil, fmt.Errorf("加载配置文件失败: %w", err)
	}

	mcpCfg, exists := cfg.MCPServers[name]
	if !exists {
		return nil, fmt.Errorf("MCP '%s' 不存在", name)
	}

	manager.SetStatusOutput(io.Discard)
	defer manager.SetStatusOutput(os.Stdout)

	testManager := manager.NewManager()
	if err := testManager.LoadConfig(configPath); err != nil {
		return nil, fmt.Errorf("加载配置失败: %w", err)
	}
	if err := testManager.Start(context.Background()); err != nil {
		if tail := mcpStderrDiagnostics(testManager, name); showStderr && tail != "" && !strings.Contains(err.Error(), tail) {
			return nil, fmt.Errorf("连接失败: %w\n%s", err, tail)
		}
		return nil, fmt.Errorf("连接失败: %w", err)
	}
	defer testManager.Stop()

	status, err := testManager.GetMCPStatus(name)
	if err != nil {
		return nil, fmt.Errorf("获取状态失败: %w", err)
	}

	tools := make([]mcpToolOutput, 0)
	for _, tool := range testManager.ListTools() {
		if tool != nil && tool.MCPName == name {
			tools = append(tools, mcpToolOutput{
				Name:        tool.Tool.Name,
				Description: tool.Tool.Description,
				MCPName:     tool.MCPName,
				Enabled:     tool.Enabled,
				InputSchema: tool.Tool.InputSchema,
			})
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

	result := &mcpServerCommandResult{
		Config:  mcpCfg,
		Status:  status,
		Tools:   tools,
		Success: status != nil && status.Connected,
	}
	if showStderr {
		result.StderrTail = mcpStderrDiagnostics(testManager, name)
	}
	return result, nil
}

// mcpStderrDiagnostics 读取 manager 的可选 stdio 诊断能力（计划 §11.5）。
// 不支持该能力时（Win7 兼容构建、测试替身）返回空串。
func mcpStderrDiagnostics(mgr manager.Manager, name string) string {
	provider, ok := mgr.(manager.StderrDiagnosticsProvider)
	if !ok || provider == nil {
		return ""
	}
	return strings.TrimSpace(provider.StderrDiagnostics(name))
}

func runMCPSetEnabledCommand(name string, enabled bool) (*mcpActionCommandResult, error) {
	service := newMCPAdminService(false)
	if _, err := service.SetEnabled(context.Background(), name, enabled); err != nil {
		return nil, err
	}
	return &mcpActionCommandResult{
		MCPName: name,
		Enabled: &enabled,
	}, nil
}

func runMCPReloadCommand() (*mcpActionCommandResult, error) {
	if err := newMCPAdminService(true).Reload(context.Background()); err != nil {
		return nil, err
	}
	return &mcpActionCommandResult{}, nil
}

func runMCPAddCommand(opts mcpAddCommandOptions) (*mcpActionCommandResult, error) {
	configPath := resolveMCPConfigPathForWrite()
	target := os.ExpandEnv(opts.Target)
	enabled := true
	request := mcpadmin.UpsertRequest{
		Name:    opts.Name,
		Type:    opts.Transport,
		Enabled: &enabled,
	}
	if strings.TrimSpace(opts.Description) != "" {
		description := opts.Description
		request.Description = &description
	}
	if mcpadmin.IsURLTransport(request.Type) {
		request.URL = target
	} else {
		if strings.TrimSpace(opts.Command) != "" {
			request.Command = opts.Command
		} else {
			request.Command = target
		}
		request.Args = append([]string(nil), opts.ExtraArgs...)
	}
	if headers := parseMCPHeaderOptions(opts.Headers); len(headers) > 0 {
		request.Headers = headers
	}

	mcpCfg, err := newMCPAdminService(false).Add(context.Background(), request)
	if err != nil {
		return nil, err
	}

	status := probeMCPStatus(configPath, opts.Name, mcpCfg.Type)
	return &mcpActionCommandResult{
		MCPName:    opts.Name,
		ConfigPath: configPath,
		Config:     mcpCfg,
		Status:     status,
	}, nil
}

func runMCPRemoveCommand(name string) (*mcpActionCommandResult, error) {
	configPath := resolveMCPConfigPathForWrite()
	if err := newMCPAdminService(false).Remove(context.Background(), name); err != nil {
		return nil, err
	}
	return &mcpActionCommandResult{
		MCPName:    name,
		ConfigPath: configPath,
	}, nil
}

// newMCPAdminService 创建 CLI 侧 MCP 管理服务；apply=false 时只做持久化。
func newMCPAdminService(apply bool) *mcpadmin.Service {
	return mcpadmin.NewService(resolveMCPConfigPathForWrite(), mcpadmin.WithApplyOnMutate(apply))
}

// resolveMCPConfigPathForWrite 解析可写配置路径：优先已存在的配置文件，
// 否则落到用户级 ~/.aicli/mcp.yaml（不存在时由 admin 包自动创建）。
func resolveMCPConfigPathForWrite() string {
	if path := getMCPConfigPath(); path != "" {
		return path
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".aicli", "mcp.yaml")
	}
	return "mcp.yaml"
}

// parseMCPHeaderOptions 解析 --header "Key: Value" 列表。
func parseMCPHeaderOptions(headers []string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	parsed := make(map[string]string, len(headers))
	for _, header := range headers {
		parts := strings.SplitN(header, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		if key == "" {
			continue
		}
		parsed[key] = strings.TrimSpace(parts[1])
	}
	return parsed
}

func probeMCPStatus(configPath, name, transportType string) *config.MCPStatus {
	manager.SetStatusOutput(io.Discard)
	defer manager.SetStatusOutput(os.Stdout)

	testManager := manager.NewManager()
	if err := testManager.LoadConfig(configPath); err != nil {
		return &config.MCPStatus{Name: name, Type: transportType, Enabled: true, Connected: false, LastError: err.Error()}
	}
	if err := testManager.Start(context.Background()); err != nil {
		return &config.MCPStatus{Name: name, Type: transportType, Enabled: true, Connected: false, LastError: err.Error()}
	}
	defer testManager.Stop()

	status, err := testManager.GetMCPStatus(name)
	if err != nil {
		return &config.MCPStatus{Name: name, Type: transportType, Enabled: true, Connected: false, LastError: err.Error()}
	}
	return status
}

// listMCPs 列出所有 MCP
func listMCPs(cmd *cobra.Command, args []string) {
	withMCPCommand(cmd, func(options mcpCommandOptions) {
		mcps, err := runMCPListCommand()
		if err != nil {
			exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "list"})
		}
		renderMCPStatuses(mcps, options)
	})
}

// mcpStatus 查看 MCP 状态
func mcpStatus(cmd *cobra.Command, args []string) {
	withMCPCommand(cmd, func(options mcpCommandOptions) {
		if len(args) == 0 {
			listMCPs(cmd, args)
			return
		}

		mcpName := args[0]
		rawStatus, err := runMCPStatusCommand(mcpName)
		if err != nil {
			exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "status", "mcpName": mcpName})
		}
		status, _ := rawStatus.(*config.MCPStatus)
		renderMCPStatusResult(status, options)
	})
}

// setMCPEnabled 启用 MCP
func setMCPEnabled(cmd *cobra.Command, args []string) {
	withMCPCommand(cmd, func(options mcpCommandOptions) {
		mcpName := args[0]
		payload, err := runMCPSetEnabledCommand(mcpName, true)
		if err != nil {
			exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "enable", "mcpName": mcpName})
		}
		renderMCPActionResult("enable", payload, options)
	})
}

// setMCPDisabled 禁用 MCP
func setMCPDisabled(cmd *cobra.Command, args []string) {
	withMCPCommand(cmd, func(options mcpCommandOptions) {
		mcpName := args[0]
		payload, err := runMCPSetEnabledCommand(mcpName, false)
		if err != nil {
			exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "disable", "mcpName": mcpName})
		}
		renderMCPActionResult("disable", payload, options)
	})
}

// listTools 列出工具
func listTools(cmd *cobra.Command, args []string) {
	withMCPCommand(cmd, func(options mcpCommandOptions) {
		filterName := ""
		if len(args) > 0 {
			filterName = args[0]
		}
		tools, err := runMCPToolsCommand(filterName)
		if err != nil {
			exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "tools", "mcpName": filterName})
		}
		renderMCPToolsResult(filterName, tools, options)
	})
}

// testTool 测试工具调用
func testTool(cmd *cobra.Command, args []string) {
	withMCPCommand(cmd, func(options mcpCommandOptions) {
		mcpName := args[0]
		toolName := args[1]
		rawArgs := ""
		if len(args) > 2 {
			rawArgs = args[2]
		}

		result, err := runMCPTestToolCommand(mcpName, toolName, rawArgs)
		if err != nil {
			exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "test", "mcpName": mcpName, "toolName": toolName})
		}
		renderMCPToolCallResult(result, options)
	})
}

// reloadConfig 重新加载配置
func reloadConfig(cmd *cobra.Command, args []string) {
	withMCPCommand(cmd, func(options mcpCommandOptions) {
		payload, err := runMCPReloadCommand()
		if err != nil {
			exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "reload"})
		}
		renderMCPActionResult("reload", payload, options)
	})
}

// addMCP 添加 MCP 服务器
func addMCP(cmd *cobra.Command, args []string) {
	withMCPCommand(cmd, func(options mcpCommandOptions) {
		name := args[0]
		payload, err := runMCPAddCommand(mcpAddCommandOptions{
			Name:        name,
			Target:      args[1],
			Transport:   transportType,
			Description: addDescription,
			Command:     addCommand,
			Headers:     headers,
			AuthType:    authType,
			ExtraArgs:   append([]string(nil), args[2:]...),
		})
		if err != nil {
			exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "add", "mcpName": name})
		}
		renderMCPAddResult("add", transportType, addDescription, payload, options)
	})
}

// removeMCP 移除 MCP 服务器
func removeMCP(cmd *cobra.Command, args []string) {
	withMCPCommand(cmd, func(options mcpCommandOptions) {
		name := args[0]
		payload, err := runMCPRemoveCommand(name)
		if err != nil {
			exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "remove", "mcpName": name})
		}
		renderMCPActionResult("remove", payload, options)
	})
}

// testServer 测试服务器连接
func testServer(cmd *cobra.Command, args []string) {
	withMCPCommand(cmd, func(options mcpCommandOptions) {
		name := args[0]
		payload, err := runMCPTestServerCommand(name, testServerShowStderr)
		if err != nil {
			exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "test-server", "mcpName": name})
		}
		renderMCPTestServerResult(name, payload, options)
	})
}

func renderMCPEmptyResult(options mcpCommandOptions, textMessage string) {
	if options.OutputFormat == "json" {
		printCommandJSONOutput("mcp", options.JSONEnvelope, []interface{}{})
		return
	}
	if strings.TrimSpace(textMessage) != "" {
		fmt.Println(textMessage)
	}
}

func renderMCPStatuses(statuses []*config.MCPStatus, options mcpCommandOptions) {
	if len(statuses) == 0 {
		renderMCPEmptyResult(options, "没有配置任何 MCP 服务器")
		return
	}
	if options.OutputFormat == "json" {
		printCommandJSONOutput("mcp", options.JSONEnvelope, statuses)
		return
	}

	fmt.Println("MCP 服务器:")
	fmt.Println("─────────────────────────────────────────")
	for _, mcp := range statuses {
		status := "disabled"
		if mcp.Enabled {
			status = "connected"
			if !mcp.Connected {
				status = "disconnected"
			}
		}
		fmt.Printf("  %s\n", mcp.Name)
		fmt.Printf("    类型: %s\n", mcp.Type)
		fmt.Printf("    状态: %s\n", status)
		fmt.Printf("    工具数量: %d\n", mcp.ToolCount)
		fmt.Println()
	}
}

func renderMCPToolsResult(filterName string, tools []mcpToolOutput, options mcpCommandOptions) {
	if len(tools) == 0 {
		renderMCPEmptyResult(options, "没有可用的工具")
		return
	}
	if options.OutputFormat == "json" {
		printCommandJSONOutput("mcp", options.JSONEnvelope, tools)
		return
	}

	if filterName == "" {
		fmt.Printf("所有 MCP 工具 (共 %d 个):\n", len(tools))
	} else {
		fmt.Printf("MCP '%s' 的工具:\n", filterName)
	}
	fmt.Println("─────────────────────────────────────────")
	for _, info := range tools {
		fmt.Printf("  %s\n", info.Name)
		fmt.Printf("    描述: %s\n", info.Description)
		fmt.Printf("    MCP: %s\n", info.MCPName)
		fmt.Println()
	}
}

func renderMCPStatusResult(status *config.MCPStatus, options mcpCommandOptions) {
	if status == nil {
		return
	}
	if options.OutputFormat == "json" {
		printCommandJSONOutput("mcp", options.JSONEnvelope, status)
		return
	}

	fmt.Println("MCP 状态:")
	fmt.Println("─────────────────────────────────────────")
	fmt.Printf("  名称: %s\n", status.Name)
	fmt.Printf("  类型: %s\n", status.Type)
	fmt.Printf("  启用: %v\n", status.Enabled)
	fmt.Printf("  已连接: %v\n", status.Connected)
	fmt.Printf("  工具数量: %d\n", status.ToolCount)
}

func renderMCPToolCallResult(result *mcpToolCommandResult, options mcpCommandOptions) {
	if result == nil {
		return
	}
	if options.OutputFormat == "json" {
		printCommandJSONOutput("mcp", options.JSONEnvelope, result)
		return
	}

	fmt.Println("工具调用结果:")
	fmt.Println("─────────────────────────────────────────")
	if result.Result == nil {
		return
	}
	for _, content := range result.Result.Content {
		switch content.Type {
		case "text":
			fmt.Printf("  %s\n", content.Text)
		default:
			fmt.Printf("  [%s] %v\n", content.Type, content)
		}
	}
}

func renderMCPActionResult(action string, payload *mcpActionCommandResult, options mcpCommandOptions) {
	if options.OutputFormat == "json" {
		printCommandActionJSON("mcp", options.JSONEnvelope, action, payload)
		return
	}

	name := ""
	if payload != nil {
		name = payload.MCPName
	}
	switch action {
	case "enable":
		fmt.Printf("MCP '%s' 已启用\n", name)
	case "disable":
		fmt.Printf("MCP '%s' 已禁用\n", name)
	case "reload":
		fmt.Println("配置已重新加载")
	case "remove":
		fmt.Printf("✅ 已移除 MCP: %s\n", name)
	}
}

func renderMCPAddResult(action, transportType, description string, payload *mcpActionCommandResult, options mcpCommandOptions) {
	if options.OutputFormat == "json" {
		printCommandActionJSON("mcp", options.JSONEnvelope, action, payload)
		return
	}

	if payload == nil || payload.Config == nil {
		return
	}

	name := payload.MCPName
	mcpCfg := *payload.Config
	status := payload.Status

	fmt.Printf("✅ 已添加 MCP: %s\n", name)
	fmt.Printf("   类型: %s\n", transportType)
	if description != "" {
		fmt.Printf("   描述: %s\n", description)
	}
	if transportType == "stdio" {
		fmt.Printf("   命令: %s\n", mcpCfg.Command)
		if len(mcpCfg.Args) > 0 {
			fmt.Printf("   参数: %v\n", mcpCfg.Args)
		}
	} else {
		fmt.Printf("   URL: %s\n", mcpCfg.URL)
	}
	fmt.Println("\n正在测试连接...")
	if status != nil && status.Connected {
		fmt.Printf("✅ 连接成功! 已加载 %d 个工具\n", status.ToolCount)
	} else if status != nil && status.LastError != "" {
		fmt.Printf("❌ 连接测试失败: %s\n", status.LastError)
	}
}

func renderMCPTestServerResult(name string, payload *mcpServerCommandResult, options mcpCommandOptions) {
	if options.OutputFormat == "json" {
		printCommandJSONOutput("mcp", options.JSONEnvelope, payload)
		return
	}

	if payload == nil {
		return
	}

	mcpCfg := payload.Config
	status := payload.Status
	tools := payload.Tools

	fmt.Printf("测试 MCP: %s\n", name)
	fmt.Printf("─────────────────────────────────────────\n")
	fmt.Printf("  类型: %s\n", mcpCfg.Type)
	if mcpCfg.Description != "" {
		fmt.Printf("  描述: %s\n", mcpCfg.Description)
	}
	if mcpCfg.Type == "stdio" {
		fmt.Printf("  命令: %s\n", mcpCfg.Command)
	} else {
		fmt.Printf("  URL: %s\n", mcpCfg.URL)
	}
	fmt.Println()

	fmt.Println("连接结果:")
	fmt.Println("─────────────────────────────────────────")
	if status != nil && status.Connected {
		fmt.Printf("  ✅ 已连接\n")
		fmt.Printf("  工具数量: %d\n", status.ToolCount)
		if len(tools) > 0 {
			fmt.Println("\n可用工具:")
			for _, tool := range tools {
				fmt.Printf("  • %s - %s\n", tool.Name, tool.Description)
			}
		}
	} else {
		fmt.Printf("  ❌ 连接失败\n")
		if status != nil && strings.TrimSpace(status.LastError) != "" {
			fmt.Printf("  错误: %s\n", status.LastError)
		}
	}
	if strings.TrimSpace(payload.StderrTail) != "" {
		fmt.Println("\nstderr 诊断:")
		fmt.Println("─────────────────────────────────────────")
		fmt.Println(payload.StderrTail)
	}
}
