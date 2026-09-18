package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

const chatMCPCommandTimeout = 60 * time.Second

const chatMCPCommandUsage = `用法:
  /mcp                                    列出全部 MCP（同 /mcp list）
  /mcp list                               列出全部 MCP 与连接状态
  /mcp status <name>                      查看单个 MCP 的配置与运行状态
  /mcp add <name> <url> [options]         新增 URL 传输（http(s)→streamable，ws(s)→websocket）
       --type streamable|sse|websocket    显式指定传输类型
       --trust local|trusted_remote|untrusted_remote
       --header "Name: Value"             可重复；URL 传输会映射为 HEADER_* 环境变量
       --env KEY=VALUE                    可重复
       --description <text> | --disabled
  /mcp add <name> --command <cmd> [--arg <arg>]...   新增 stdio 传输
  /mcp enable <name> | disable <name>     启用/停用并热重载
  /mcp remove <name>                      删除并热重载
  /mcp reload                             重新加载配置并重连
  /mcp help                               显示本帮助`

// chatMCPService 抽象 /mcp 需要的管理能力，便于测试注入替身。
type chatMCPService interface {
	ConfigPath() string
	List(ctx context.Context) ([]mcpadmin.Item, error)
	Get(ctx context.Context, name string) (*config.MCPConfig, error)
	Add(ctx context.Context, req mcpadmin.UpsertRequest) (*config.MCPConfig, error)
	Remove(ctx context.Context, name string) error
	SetEnabled(ctx context.Context, name string, enabled bool) (*config.MCPConfig, error)
	Reload(ctx context.Context) error
}

// newChatMCPService 构造 /mcp 使用的管理服务：复用 chat 进程内的 MCP manager，
// 使启停/热重载直接作用于当前会话的连接与工具。
func newChatMCPService() chatMCPService {
	options := []mcpadmin.Option{mcpadmin.WithApplyOnMutate(true)}
	if manager := MCPManagerInstance; manager != nil {
		options = append(options, mcpadmin.WithManager(manager))
	}
	return mcpadmin.NewService(resolveMCPConfigPathForWrite(), options...)
}

// refreshChatMCPTools 把最新 MCP 工具重新注册进当前会话的 FunctionRegistry；
// 失败不阻断命令输出（下一次注册或重启会话会覆盖）。
func refreshChatMCPTools(session *ChatSession) {
	if session == nil || session.FunctionRegistry == nil || MCPManagerInstance == nil {
		return
	}
	_ = registerMCPTools(session.FunctionRegistry)
}

func chatMCPCommandText(command string) string {
	return chatMCPCommandTextWithService(command, newChatMCPService(), nil)
}

// chatMCPCommandTextWithService 解析并执行 /mcp 子命令，返回纯文本结果；
// onMutate 在写操作成功后回调（用于刷新会话工具注册表）。
func chatMCPCommandTextWithService(command string, service chatMCPService, onMutate func()) string {
	args := splitChatCommandFields(extractCommandArgument(command))
	if len(args) == 0 {
		return chatMCPListText(service)
	}
	switch strings.ToLower(args[0]) {
	case "help", "-h", "--help":
		return chatMCPCommandUsage
	case "list", "ls":
		return chatMCPListText(service)
	case "status", "show", "info":
		if len(args) < 2 {
			return "错误: 需要指定 MCP 名称\n用法: /mcp status <name>"
		}
		return chatMCPStatusText(service, args[1])
	case "add":
		return chatMCPAddText(service, args[1:], onMutate)
	case "remove", "rm", "delete":
		if len(args) < 2 {
			return "错误: 需要指定 MCP 名称\n用法: /mcp remove <name>"
		}
		return chatMCPMutationText(service, onMutate, "移除", args[1], func(ctx context.Context, name string) error {
			return service.Remove(ctx, name)
		})
	case "enable", "disable":
		if len(args) < 2 {
			return "错误: 需要指定 MCP 名称\n用法: /mcp " + strings.ToLower(args[0]) + " <name>"
		}
		enabled := strings.ToLower(args[0]) == "enable"
		action := "启用"
		if !enabled {
			action = "停用"
		}
		return chatMCPMutationText(service, onMutate, action, args[1], func(ctx context.Context, name string) error {
			_, err := service.SetEnabled(ctx, name, enabled)
			return err
		})
	case "reload":
		return chatMCPReloadText(service, onMutate)
	default:
		return fmt.Sprintf("错误: 未知子命令 %q\n%s", args[0], chatMCPCommandUsage)
	}
}

func chatMCPListText(service chatMCPService) string {
	if service == nil {
		return "错误: MCP 管理服务不可用"
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatMCPCommandTimeout)
	defer cancel()
	items, err := service.List(ctx)
	if err != nil {
		return "错误: 读取 MCP 列表失败: " + err.Error()
	}
	configPath := strings.TrimSpace(service.ConfigPath())
	if len(items) == 0 {
		text := "当前没有配置 MCP Server"
		if configPath != "" {
			text += "（配置文件: " + configPath + "）"
		}
		return text + "\n用 /mcp add <name> <url> 或 aicli mcp add 新增。"
	}
	connected := 0
	for _, item := range items {
		if item.Status != nil && item.Status.Connected {
			connected++
		}
	}
	lines := []string{fmt.Sprintf("MCP Servers（%d 个，已连接 %d 个）", len(items), connected)}
	if configPath != "" {
		lines = append(lines, "配置: "+configPath)
	}
	for _, item := range items {
		lines = append(lines, chatMCPItemLines(item)...)
	}
	return strings.Join(lines, "\n")
}

func chatMCPItemLines(item mcpadmin.Item) []string {
	name := strings.TrimSpace(item.Config.Name)
	if name == "" {
		name = "(未命名)"
	}
	enabled := item.Config.IsEnabled()
	marker := "○"
	status := "已停用"
	if enabled {
		marker = "◐"
		status = "已启用，未连接"
	}
	if item.Status != nil {
		switch {
		case item.Status.Connected:
			marker = "●"
			status = "已连接"
		case !enabled:
			status = "已停用"
		}
		if item.Status.ToolCount > 0 {
			status += fmt.Sprintf(" · %d tools", item.Status.ToolCount)
		}
		if trust := strings.TrimSpace(string(item.Status.TrustLevel)); trust != "" {
			status += " · " + trust
		}
		if lastError := strings.TrimSpace(item.Status.LastError); lastError != "" {
			status += " · 错误: " + lastError
		}
	}
	transport := mcpadmin.NormalizeTransportType(item.Config.Type)
	if transport == "" {
		transport = "unknown"
	}
	lines := []string{fmt.Sprintf("%s %s [%s] %s", marker, name, transport, status)}
	if target := chatMCPEndpoint(item.Config); target != "" {
		lines = append(lines, "    "+target)
	}
	return lines
}

func chatMCPEndpoint(cfg config.MCPConfig) string {
	if mcpadmin.IsURLTransport(cfg.Type) {
		return strings.TrimSpace(cfg.URL)
	}
	parts := make([]string, 0, len(cfg.Args)+1)
	if command := strings.TrimSpace(cfg.Command); command != "" {
		parts = append(parts, command)
	}
	parts = append(parts, cfg.Args...)
	return strings.TrimSpace(strings.Join(parts, " "))
}

func chatMCPStatusText(service chatMCPService, name string) string {
	if service == nil {
		return "错误: MCP 管理服务不可用"
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatMCPCommandTimeout)
	defer cancel()
	cfg, err := service.Get(ctx, name)
	if err != nil {
		return fmt.Sprintf("错误: 读取 MCP %q 失败: %v", name, err)
	}
	if cfg == nil {
		return fmt.Sprintf("错误: 未找到 MCP %q", name)
	}

	lines := []string{strings.TrimSpace(cfg.Name)}
	appendField := func(label, value string) {
		if strings.TrimSpace(value) != "" {
			lines = append(lines, "  "+label+": "+value)
		}
	}
	transport := mcpadmin.NormalizeTransportType(cfg.Type)
	if transport == "" {
		transport = "unknown"
	}
	appendField("类型", transport)
	appendField("信任级别", strings.TrimSpace(string(cfg.ResolvedTrustLevel())))
	appendField("地址", strings.TrimSpace(cfg.URL))
	appendField("命令", chatMCPEndpoint(*cfg))
	appendField("描述", strings.TrimSpace(cfg.Description))
	if cfg.MaxParallelCalls > 0 {
		appendField("并行上限", fmt.Sprintf("%d", cfg.MaxParallelCalls))
	}
	if cfg.Timeout.Duration > 0 {
		appendField("超时", cfg.Timeout.Duration.String())
	}
	if len(cfg.Env) > 0 {
		appendField("环境变量", fmt.Sprintf("%d 项", len(cfg.Env)))
	}

	if items, err := service.List(ctx); err == nil {
		for _, item := range items {
			if !strings.EqualFold(strings.TrimSpace(item.Config.Name), strings.TrimSpace(cfg.Name)) || item.Status == nil {
				continue
			}
			status := "未连接"
			if item.Status.Connected {
				status = "已连接"
			}
			status += fmt.Sprintf("（%d tools）", item.Status.ToolCount)
			appendField("运行状态", status)
			if !item.Status.LastConnect.IsZero() {
				appendField("最近连接", item.Status.LastConnect.Format(time.RFC3339))
			}
			appendField("最近错误", strings.TrimSpace(item.Status.LastError))
			break
		}
	}
	return strings.Join(lines, "\n")
}

type chatMCPAddOptions struct {
	transport   string
	target      string
	command     string
	args        []string
	headers     []string
	env         []string
	description string
	trust       string
	disabled    bool
}

func chatMCPAddText(service chatMCPService, args []string, onMutate func()) string {
	if service == nil {
		return "错误: MCP 管理服务不可用"
	}
	if len(args) == 0 {
		return "错误: 需要指定 MCP 名称\n用法: /mcp add <name> <url> 或 /mcp add <name> --command <cmd> [--arg <arg>]..."
	}
	name := strings.TrimSpace(args[0])
	if name == "" {
		return "错误: MCP 名称不能为空"
	}
	opts, errText := parseChatMCPAddOptions(args[1:])
	if errText != "" {
		return errText
	}
	transport := chatMCPResolveTransport(opts)
	enabled := !opts.disabled
	request := mcpadmin.UpsertRequest{Name: name, Type: transport, Enabled: &enabled}
	if opts.description != "" {
		description := opts.description
		request.Description = &description
	}
	if opts.trust != "" {
		request.TrustLevel = opts.trust
	}
	if mcpadmin.IsURLTransport(transport) {
		target := strings.TrimSpace(opts.target)
		if target == "" {
			return "错误: URL 传输需要 <url> 或 --url\n用法: /mcp add " + name + " <url> [--type streamable|sse|websocket]"
		}
		request.URL = target
	} else {
		command := strings.TrimSpace(opts.command)
		if command == "" {
			command = strings.TrimSpace(opts.target)
		}
		if command == "" {
			return "错误: stdio 传输需要 --command <cmd> 或 <command>\n用法: /mcp add " + name + " --command <cmd> [--arg <arg>]..."
		}
		request.Command = command
		request.Args = append([]string(nil), opts.args...)
	}
	if headers := parseMCPHeaderOptions(opts.headers); len(headers) > 0 {
		request.Headers = headers
	}
	if env := parseChatMCPEnvOptions(opts.env); len(env) > 0 {
		request.Env = env
	}

	ctx, cancel := context.WithTimeout(context.Background(), chatMCPCommandTimeout)
	defer cancel()
	if _, err := service.Add(ctx, request); err != nil {
		return fmt.Sprintf("错误: 新增 MCP %q 失败: %v", name, err)
	}
	if onMutate != nil {
		onMutate()
	}
	lines := []string{fmt.Sprintf("✓ 已新增 MCP %q（%s）", name, transport)}
	if path := strings.TrimSpace(service.ConfigPath()); path != "" {
		lines = append(lines, "  配置: "+path)
	}
	lines = append(lines, "  用 /mcp status "+name+" 查看连接状态与工具数。")
	return strings.Join(lines, "\n")
}

func parseChatMCPAddOptions(args []string) (chatMCPAddOptions, string) {
	var opts chatMCPAddOptions
	for index := 0; index < len(args); index++ {
		token := args[index]
		if !strings.HasPrefix(token, "--") && !strings.HasPrefix(token, "-") {
			if opts.target == "" {
				opts.target = token
				continue
			}
			return opts, "错误: 多余的参数 " + token
		}
		next := func() (string, bool) {
			if index+1 < len(args) {
				index++
				return args[index], true
			}
			return "", false
		}
		switch token {
		case "--type", "-t":
			value, ok := next()
			if !ok {
				return opts, "错误: " + token + " 需要取值"
			}
			opts.transport = value
		case "--url":
			value, ok := next()
			if !ok {
				return opts, "错误: --url 需要取值"
			}
			opts.target = value
		case "--command", "-c":
			value, ok := next()
			if !ok {
				return opts, "错误: --command 需要取值"
			}
			opts.command = value
		case "--arg", "-a":
			value, ok := next()
			if !ok {
				return opts, "错误: --arg 需要取值"
			}
			opts.args = append(opts.args, value)
		case "--header", "-H":
			value, ok := next()
			if !ok {
				return opts, "错误: --header 需要取值（示例: --header \"Authorization: Bearer x\"）"
			}
			opts.headers = append(opts.headers, value)
		case "--env", "-e":
			value, ok := next()
			if !ok {
				return opts, "错误: --env 需要取值（示例: --env API_KEY=xxx）"
			}
			opts.env = append(opts.env, value)
		case "--description", "-d":
			value, ok := next()
			if !ok {
				return opts, "错误: --description 需要取值"
			}
			opts.description = value
		case "--trust":
			value, ok := next()
			if !ok {
				return opts, "错误: --trust 需要取值"
			}
			opts.trust = value
		case "--disabled":
			opts.disabled = true
		case "--enabled":
			opts.disabled = false
		default:
			return opts, "错误: 未知参数 " + token + "\n" + chatMCPCommandUsage
		}
	}
	return opts, ""
}

func chatMCPResolveTransport(opts chatMCPAddOptions) string {
	if explicit := strings.TrimSpace(opts.transport); explicit != "" {
		return mcpadmin.NormalizeTransportType(explicit)
	}
	if strings.TrimSpace(opts.command) != "" {
		return "stdio"
	}
	target := strings.ToLower(strings.TrimSpace(opts.target))
	switch {
	case strings.HasPrefix(target, "ws://"), strings.HasPrefix(target, "wss://"):
		return "websocket"
	case strings.HasPrefix(target, "http://"), strings.HasPrefix(target, "https://"):
		return "streamable"
	default:
		return "stdio"
	}
}

func parseChatMCPEnvOptions(values []string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	parsed := make(map[string]string, len(values))
	for _, item := range values {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		if key == "" {
			continue
		}
		parsed[key] = parts[1]
	}
	return parsed
}

func chatMCPMutationText(
	service chatMCPService,
	onMutate func(),
	action string,
	name string,
	mutate func(context.Context, string) error,
) string {
	if service == nil {
		return "错误: MCP 管理服务不可用"
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatMCPCommandTimeout)
	defer cancel()
	if err := mutate(ctx, name); err != nil {
		return fmt.Sprintf("错误: %s MCP %q 失败: %v", action, name, err)
	}
	if onMutate != nil {
		onMutate()
	}
	lines := []string{fmt.Sprintf("✓ 已%s MCP %q", action, name)}
	if path := strings.TrimSpace(service.ConfigPath()); path != "" {
		lines = append(lines, "  配置: "+path)
	}
	return strings.Join(lines, "\n")
}

func chatMCPReloadText(service chatMCPService, onMutate func()) string {
	if service == nil {
		return "错误: MCP 管理服务不可用"
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatMCPCommandTimeout)
	defer cancel()
	if err := service.Reload(ctx); err != nil {
		return "错误: 热重载 MCP 配置失败: " + err.Error()
	}
	if onMutate != nil {
		onMutate()
	}
	summary := ""
	if items, err := service.List(ctx); err == nil {
		connected := 0
		for _, item := range items {
			if item.Status != nil && item.Status.Connected {
				connected++
			}
		}
		summary = fmt.Sprintf("（%d 个，已连接 %d 个）", len(items), connected)
	}
	return "✓ 已热重载 MCP 配置" + summary
}

// executeStructuredMCPCommand 把 /mcp 结果投影为统一的命令单元。
func executeStructuredMCPCommand(session *ChatSession, command string) CommandResult {
	text := chatMCPCommandTextWithService(command, newChatMCPService(), func() {
		refreshChatMCPTools(session)
	})
	return commandTextResult(text)
}
