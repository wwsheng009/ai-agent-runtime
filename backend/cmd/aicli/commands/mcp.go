package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/config/loader"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
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
)

// MCPManager 全局 MCP 管理器实例
var MCPManager manager.Manager

// MCPCommand MCP 命令
func MCPCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "管理 MCP (Model Context Protocol) 服务器",
		Long:  `MCP 命令用于管理 Model Context Protocol 服务器，支持添加、列出、移除、启用、禁用 MCP 服务器。`,
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
  # HTTP/SSE 传输
  aicli mcp add --transport sse context7 https://mcp.context7.com/mcp

  # WebSocket 传输 (ws:// 或 wss://)
  aicli mcp add --transport websocket my-mcp wss://example.com/mcp

  # stdio 传输 (本地进程)
  aicli mcp add --transport stdio -- npx chrome-devtools-mcp@latest

  # 带 Header
  aicli mcp add --transport sse context7 https://mcp.context7.com/mcp --header "API_KEY: your-key"`,
		Args: cobra.MinimumNArgs(2),
		Run:  addMCP,
	}
	addCmd.Flags().StringVarP(&transportType, "transport", "t", "sse", "传输类型 (stdio, sse, websocket)")
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

	// 先尝试从 configs/config.yaml 读取
	mainConfigPath := "configs/config.yaml"
	if loader.NewLoader(mainConfigPath).ConfigExists() {
		type cfgStruct struct {
			AICLI *struct {
				MCP *struct {
					ConfigFile string `yaml:"config_file"`
				} `yaml:"mcp"`
			} `yaml:"aicli"`
		}
		var cfg cfgStruct
		ldr := loader.NewLoader(mainConfigPath)
		if err := ldr.Load(&cfg); err == nil && cfg.AICLI != nil && cfg.AICLI.MCP != nil && cfg.AICLI.MCP.ConfigFile != "" {
			// 如果指定路径存在且文件存在，则返回
			if _, err := os.Stat(cfg.AICLI.MCP.ConfigFile); err == nil {
				return cfg.AICLI.MCP.ConfigFile
			}
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

	options, err := resolveMCPCommandOptions(cmd)
	if err != nil {
		exitCommandError("mcp", "json", err, nil)
	}
	fn(options)
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

func runMCPTestServerCommand(name string) (*mcpServerCommandResult, error) {
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

	return &mcpServerCommandResult{
		Config:  mcpCfg,
		Status:  status,
		Tools:   tools,
		Success: status != nil && status.Connected,
	}, nil
}

func runMCPSetEnabledCommand(name string, enabled bool) (*mcpActionCommandResult, error) {
	if err := ensureMCPManager(); err != nil {
		return nil, err
	}
	if err := MCPManager.SetMCPEnabled(name, enabled); err != nil {
		return nil, err
	}
	return &mcpActionCommandResult{
		MCPName: name,
		Enabled: &enabled,
	}, nil
}

func runMCPReloadCommand() (*mcpActionCommandResult, error) {
	if err := ensureMCPManager(); err != nil {
		return nil, err
	}
	if err := MCPManager.ReloadConfig(); err != nil {
		return nil, err
	}
	return &mcpActionCommandResult{}, nil
}

func runMCPAddCommand(opts mcpAddCommandOptions) (*mcpActionCommandResult, error) {
	configPath := getMCPConfigPath()
	if configPath == "" {
		return nil, fmt.Errorf("找不到 MCP 配置文件")
	}

	existingConfig, err := loadMCPConfigFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("加载配置文件失败: %w", err)
	}
	if _, exists := existingConfig.MCPServers[opts.Name]; exists {
		return nil, fmt.Errorf("MCP '%s' 已存在", opts.Name)
	}

	mcpCfg, err := buildMCPConfigForAdd(opts)
	if err != nil {
		return nil, err
	}

	if existingConfig.MCPServers == nil {
		existingConfig.MCPServers = make(map[string]config.MCPConfig)
	}
	existingConfig.MCPServers[opts.Name] = mcpCfg
	if err := writeMCPConfigFile(configPath, existingConfig); err != nil {
		return nil, fmt.Errorf("保存配置文件失败: %w", err)
	}

	status := probeMCPStatus(configPath, opts.Name, opts.Transport)
	return &mcpActionCommandResult{
		MCPName:    opts.Name,
		ConfigPath: configPath,
		Config:     &mcpCfg,
		Status:     status,
	}, nil
}

func runMCPRemoveCommand(name string) (*mcpActionCommandResult, error) {
	configPath := getMCPConfigPath()
	if configPath == "" {
		return nil, fmt.Errorf("找不到 MCP 配置文件")
	}

	existingConfig, err := loadMCPConfigFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("加载配置文件失败: %w", err)
	}
	if _, exists := existingConfig.MCPServers[name]; !exists {
		return nil, fmt.Errorf("MCP '%s' 不存在", name)
	}

	delete(existingConfig.MCPServers, name)
	if err := writeMCPConfigFile(configPath, existingConfig); err != nil {
		return nil, fmt.Errorf("保存配置文件失败: %w", err)
	}

	return &mcpActionCommandResult{
		MCPName:    name,
		ConfigPath: configPath,
	}, nil
}

func loadMCPConfigFile(configPath string) (*config.Config, error) {
	cfgLoader := config.NewLoader(configPath)
	return cfgLoader.Load()
}

func buildMCPConfigForAdd(opts mcpAddCommandOptions) (config.MCPConfig, error) {
	mcpCfg := config.MCPConfig{
		Name:        opts.Name,
		Description: opts.Description,
		Type:        opts.Transport,
		Enabled:     true,
		Timeout:     config.Duration{Duration: 30 * time.Second},
		MaxRetry:    3,
	}

	target := os.ExpandEnv(opts.Target)
	switch opts.Transport {
	case "stdio":
		if opts.Command != "" {
			mcpCfg.Command = opts.Command
		} else {
			mcpCfg.Command = target
		}
		if len(opts.ExtraArgs) > 0 {
			mcpCfg.Args = append([]string(nil), opts.ExtraArgs...)
		}
	case "sse", "http", "websocket":
		mcpCfg.URL = target
		if len(opts.Headers) > 0 {
			mcpCfg.Env = make(map[string]string)
			for _, header := range opts.Headers {
				parts := strings.SplitN(header, ":", 2)
				if len(parts) == 2 {
					mcpCfg.Env[fmt.Sprintf("HEADER_%s", strings.TrimSpace(parts[0]))] = strings.TrimSpace(parts[1])
				}
			}
		}
	default:
		return config.MCPConfig{}, fmt.Errorf("不支持的传输类型: %s", opts.Transport)
	}

	return mcpCfg, nil
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
		payload, err := runMCPTestServerCommand(name)
		if err != nil {
			exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "test-server", "mcpName": name})
		}
		renderMCPTestServerResult(name, payload, options)
	})
}

func writeMCPConfigFile(configPath string, cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	var (
		data []byte
		err  error
	)
	if strings.HasSuffix(strings.ToLower(configPath), ".json") {
		data, err = json.MarshalIndent(cfg, "", "  ")
	} else {
		var buf bytes.Buffer
		encoder := yaml.NewEncoder(&buf)
		encoder.SetIndent(2)
		if err = encoder.Encode(cfg); err == nil {
			err = encoder.Close()
		}
		data = buf.Bytes()
	}
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0644)
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
	}
}
