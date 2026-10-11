package commands

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
)

// chat_gateway_config.go 实现 `/gateway config`（docs/gateway/
// 12-connection-control.md 的持久化配置面）：在 aicli TUI 内直接写入
// gateway.enabled/url/token/name/auto_connect，并热应用到当前会话的
// controller——用户在网关控制台生成 token（agw_...）后，无需退出会话即可
// `/gateway config token agw_xxx` + `/gateway config url wss://...` 完成配置，
// 再 `/gateway connect`（或 auto-connect=on 重启后自动）注册。
//
// 纪律：
//   - 机密不回显：token 只写入配置，输出仅显示 TokenHint（agw_xxxx…）；
//   - 热应用优先 controller.Configure；本会话没有 controller（尚未配置）时
//     写盘后重建 controller（attachChatGateway 同一条路径），保持「配置即可用」；
//   - 环境变量仍最高优先（chatGatewayResolveConfig 的既有语义）：env 存在时
//     config 写入对当次解析无效，输出显式提醒。
//   - disable 是显式动作：清空 url/token 并停用 controller（与 connect/
//     disconnect 的"不改配置"语义相反，属于持久化变更）。

// gatewayCommandToken 是 /gateway config <key> 接受的配置键（与补全词表同源：
// chat_gateway_completion.go 的 gatewayConfigKeyArgumentCandidates）。
type gatewayCommandToken string

const (
	gatewayConfigKeyURL         gatewayCommandToken = "url"
	gatewayConfigKeyToken       gatewayCommandToken = "token"
	gatewayConfigKeyName        gatewayCommandToken = "name"
	gatewayConfigKeyAutoConnect gatewayCommandToken = "auto-connect"
	gatewayConfigKeyDisable     gatewayCommandToken = "disable"
	gatewayConfigKeyProxyURL    gatewayCommandToken = "proxy"
)

// gatewayConfigTokenForArgument 把小写参数映射为配置键；未匹配返回空。
func gatewayConfigTokenForArgument(arg string) gatewayCommandToken {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "url":
		return gatewayConfigKeyURL
	case "token":
		return gatewayConfigKeyToken
	case "name":
		return gatewayConfigKeyName
	case "auto-connect", "auto_connect", "autoconnect":
		return gatewayConfigKeyAutoConnect
	case "proxy", "proxy-url", "proxy_url", "proxyurl":
		return gatewayConfigKeyProxyURL
	case "disable":
		return gatewayConfigKeyDisable
	}
	return ""
}

// gatewayConfigArgs 是一次 /gateway config 调用的解析结果。
type gatewayConfigArgs struct {
	key      gatewayCommandToken
	value    string
	hasValue bool
}

// parseGatewayConfigArgs 解析 `/gateway config [key [value]]`。value 允许包含
// 空格（节点名场景），所以取 key token 之后的整段原文。
//
// 调用约定：executeStructuredGatewayCommand 只在 gatewaySubcommand == "config"
// 时进入本命令，因此 extractCommandArgument 的首 token 恒为 "config"（大小写
// 不敏感）；去掉它后剩余首 token 是 key，其余是 value。
func parseGatewayConfigArgs(command string) gatewayConfigArgs {
	arg := strings.TrimSpace(extractCommandArgument(command))
	parsed := gatewayConfigArgs{}
	if arg == "" {
		return parsed
	}
	// 去掉分派 token "config"，得到 "<key> [value...]"。
	rest := trimGatewayConfigDispatchToken(arg)
	if rest == "" {
		return parsed
	}
	fields := strings.Fields(rest)
	parsed.key = gatewayConfigTokenForArgument(fields[0])
	if parsed.key == "" {
		return parsed
	}
	// value = key token 之后的原文（保留大小写与空格）。
	if end := strings.IndexAny(rest, " \t"); end >= 0 {
		parsed.value = strings.TrimSpace(rest[end+1:])
		parsed.hasValue = parsed.value != ""
	}
	return parsed
}

// trimGatewayConfigDispatchToken 去掉 arg 的首 token（"config"，大小写不敏感）
// 及其后的空白，返回剩余文本。首 token 不是 "config" 时原样返回。
func trimGatewayConfigDispatchToken(arg string) string {
	prefix := "config"
	if len(arg) < len(prefix) || !strings.EqualFold(arg[:len(prefix)], prefix) {
		return strings.TrimSpace(arg)
	}
	rest := arg[len(prefix):]
	return strings.TrimLeft(rest, " \t")
}

// executeStructuredGatewayConfigCommand handles `/gateway config` variants.
// It always claims the command (handled=true), mirroring the bare /gateway
// contract: unknown keys and missing values render usage text instead of
// falling through to the model.
func executeStructuredGatewayConfigCommand(session *ChatSession, command string) (CommandResult, bool) {
	if session == nil {
		return commandTextResult("gateway: 当前没有活动会话"), true
	}
	// 读取路径前置同步：会话启动时配置里还没有 gateway 段（或测试会话直接构造）
	// 时 Config.Gateway 为 nil，而磁盘文件可能已经声明了该段。重载一次让视图
	// 与 chatGatewayResolveConfig 看到同一份数据（与 /model 的重载语义同源）。
	syncChatGatewayMemoryConfig(session)
	parsed := parseGatewayConfigArgs(command)
	// 只读视图（bare `/gateway config`）：显示合并后的持久化配置（token 只给
	// hint）；未知 key 落回用法说明。bare 判断必须剥掉分派 token "config"——
	// extractCommandArgument 对 "/gateway config" 返回 "config"，永远非空。
	var result CommandResult
	if parsed.key == "" {
		if trimGatewayConfigDispatchToken(strings.TrimSpace(extractCommandArgument(command))) == "" {
			return commandTextResult(gatewayConfigSummaryText(session)), true
		}
		return commandTextResult(gatewayConfigUsageText()), true
	}

	// disable 无取值；其余键必须有值。
	if parsed.key == gatewayConfigKeyDisable {
		if parsed.hasValue {
			return commandTextResult("gateway config: disable 不接受取值\n\n" + gatewayConfigUsageText()), true
		}
		result, _ = gatewayConfigApply(session, config.GatewayPreferenceUpdate{
			URL:     ptrString(""),
			Token:   ptrString(""),
			Enabled: ptrBool(false),
		}, "disable")
		return result, true
	}
	if !parsed.hasValue {
		// `/gateway config url`（无取值）= 只读查看该键。
		return commandTextResult(gatewayConfigKeyText(session, parsed.key)), true
	}

	switch parsed.key {
	case gatewayConfigKeyURL:
		if err := validateGatewayURLValue(parsed.value); err != nil {
			return commandTextResult("gateway config: " + err.Error() + "\n\n" + gatewayConfigUsageText()), true
		}
		result, _ = gatewayConfigApply(session, config.GatewayPreferenceUpdate{URL: ptrString(parsed.value)}, "url "+parsed.value)
	case gatewayConfigKeyToken:
		result, _ = gatewayConfigApply(session, config.GatewayPreferenceUpdate{Token: ptrString(parsed.value)}, "token "+gatewayagent.Config{Token: parsed.value}.TokenHint())
	case gatewayConfigKeyName:
		result, _ = gatewayConfigApply(session, config.GatewayPreferenceUpdate{Name: ptrString(parsed.value)}, "name "+parsed.value)
	case gatewayConfigKeyAutoConnect:
		value, ok := parseGatewayAutoConnectValue(parsed.value)
		if !ok {
			return commandTextResult("gateway config: auto-connect 取值必须是 on|off\n\n" + gatewayConfigUsageText()), true
		}
		result, _ = gatewayConfigApply(session, config.GatewayPreferenceUpdate{AutoConnect: &value}, fmt.Sprintf("auto-connect=%t", value))
	case gatewayConfigKeyProxyURL:
		if err := validateGatewayProxyValue(parsed.value); err != nil {
			return commandTextResult("gateway config: " + err.Error() + "\n\n" + gatewayConfigUsageText()), true
		}
		value := parsed.value
		label := "proxy " + value
		// off/none = 清除代理恢复直连（必须写空串，而不是原样落盘——
		// parseProxyURL 会把 "off" 补全成 socks5://off 当主机名）。
		if strings.EqualFold(value, "off") || strings.EqualFold(value, "none") {
			value = ""
			label = "proxy off"
		}
		result, _ = gatewayConfigApply(session, config.GatewayPreferenceUpdate{ProxyURL: ptrString(value)}, label)
	default:
		return commandTextResult(gatewayConfigUsageText()), true
	}
	return result, true
}

// syncChatGatewayMemoryConfig re-loads the on-disk config into session.Config
// when the in-memory view has no gateway section but the file does. The read
// paths (bare /gateway config and single-key lookups) resolve through
// chatGatewayResolveConfig, which reads session.Config — without this sync a
// gateway section written outside the session (or by an older aicli) is
// invisible to /gateway status/config until restart.
func syncChatGatewayMemoryConfig(session *ChatSession) {
	if session == nil || session.Config == nil || session.Config.Gateway != nil {
		return
	}
	configPath := strings.TrimSpace(session.Config.ConfigFilePath)
	if configPath == "" {
		return
	}
	info, err := os.Stat(configPath)
	if err != nil || info.IsDir() {
		// Missing/sentinel path: keep the in-memory config (same contract as
		// reloadChatConfigForModelCommand).
		return
	}
	reloaded, err := config.ReloadGlobalConfig(configPath)
	if err != nil || reloaded == nil || reloaded.Gateway == nil {
		return
	}
	session.Config = reloaded
	rememberChatConfigFingerprint(session, configPath)
}

// gatewayConfigApply persists the update and hot-applies it to the live
// controller (or rebuilds one when the session had none because gateway was
// unconfigured). The returned text never contains the raw token.
func gatewayConfigApply(session *ChatSession, update config.GatewayPreferenceUpdate, label string) (CommandResult, bool) {
	if session.Config == nil {
		return commandTextResult("gateway config: 当前会话没有可用配置文件（in-memory 会话不支持持久化）"), true
	}
	configPath, err := ensureWritableAICLIConfigPath(session.Config, session.Config.ConfigFilePath)
	if err != nil {
		return commandTextResult("gateway config: 解析配置文件路径失败: " + err.Error()), true
	}
	gatewayCfg, err := config.UpdateGatewayConfig(configPath, update)
	if err != nil {
		return commandTextResult("gateway config: 写入配置失败: " + err.Error()), true
	}
	// 把落盘结果同步回内存配置（与 /theme、/profile 的持久化命令同一口径），
	// 让 chatGatewayResolveConfig 立即读到新值。
	session.Config.Gateway = gatewayCfg

	if envToken := strings.TrimSpace(os.Getenv(gatewayagent.EnvToken)); envToken != "" && update.Token != nil {
		// env 覆盖提醒（resolution 优先级：env > config）。
		return commandTextResult("gateway config 已写入 " + configPath +
			"（" + label + "）\n注意: 环境变量 " + gatewayagent.EnvToken +
			" 仍然生效并覆盖配置文件中的 token；如需使用配置文件请移除该环境变量。"), true
	}
	if envURL := strings.TrimSpace(os.Getenv(gatewayagent.EnvURL)); envURL != "" && update.URL != nil {
		return commandTextResult("gateway config 已写入 " + configPath +
			"（" + label + "）\n注意: 环境变量 " + gatewayagent.EnvURL +
			" 仍然生效并覆盖配置文件中的 url；如需使用配置文件请移除该环境变量。"), true
	}
	if envProxy := strings.TrimSpace(os.Getenv(gatewayagent.EnvProxyURL)); envProxy != "" && update.ProxyURL != nil {
		return commandTextResult("gateway config 已写入 " + configPath +
			"（" + label + "）\n注意: 环境变量 " + gatewayagent.EnvProxyURL +
			" 仍然生效并覆盖配置文件中的 proxy_url；如需使用配置文件请移除该环境变量。"), true
	}

	hotApplied := hotApplyChatGatewayConfig(session)
	var b strings.Builder
	b.WriteString("gateway config 已写入 ")
	b.WriteString(configPath)
	b.WriteString("（")
	b.WriteString(label)
	b.WriteString("）\n")
	if hotApplied {
		b.WriteString("已热应用到当前会话。")
	} else {
		b.WriteString("当前会话暂未接入网关（url/token 尚未配齐），配齐后重启会话生效。")
	}
	if gatewayCfg.Enabled {
		b.WriteString("\n使用 /gateway status 查看状态，/gateway connect 立即连接。")
	} else if !strings.Contains(label, "disable") {
		// 只有部分键写好时，明确说还差什么，避免"配置了却不生效"的困惑。
		if gatewayCfg.URL == "" {
			b.WriteString("\n还差: /gateway config url <wss://...>")
		}
		if gatewayCfg.Token == "" {
			b.WriteString("\n还差: /gateway config token <agw_...>（网关控制台签发）")
		}
	}
	return commandTextResult(strings.TrimRight(b.String(), "\n")), true
}

// hotApplyChatGatewayConfig re-resolves the gateway config and reconciles the
// live controller with it. Returns true when a controller exists and accepted
// the new configuration.
func hotApplyChatGatewayConfig(session *ChatSession) bool {
	if session == nil {
		return false
	}
	cfg := chatGatewayResolveConfig(session)
	if session.Gateway == nil {
		// Previously unconfigured: attach now so /gateway connect works without
		// a session restart (attachChatGateway with auto-connect suppressed —
		// the operator dials explicitly).
		if !cfg.Enabled {
			return false
		}
		attachChatGateway(session)
		return session.Gateway != nil
	}
	if err := session.Gateway.Configure(cfg); err != nil {
		return false
	}
	return true
}

// gatewayConfigSummaryText renders the persisted (merged) gateway config with
// the token masked.
func gatewayConfigSummaryText(session *ChatSession) string {
	cfg := chatGatewayResolveConfig(session)
	var b strings.Builder
	b.WriteString("gateway 配置（配置文件 + 环境变量合并）：\n")
	fmt.Fprintf(&b, "  enabled      %t\n", cfg.Enabled)
	if cfg.URL != "" {
		fmt.Fprintf(&b, "  url          %s\n", cfg.URL)
	} else {
		b.WriteString("  url          （未设置）\n")
	}
	if hint := cfg.TokenHint(); hint != "" {
		fmt.Fprintf(&b, "  token        %s\n", hint)
	} else {
		b.WriteString("  token        （未设置）\n")
	}
	if cfg.Name != "" {
		fmt.Fprintf(&b, "  name         %s\n", cfg.Name)
	}
	fmt.Fprintf(&b, "  auto_connect %t\n", cfg.AutoConnect)
	if cfg.ProxyURL != "" {
		fmt.Fprintf(&b, "  proxy_url    %s\n", cfg.ProxyURL)
	} else {
		b.WriteString("  proxy_url    （未设置，直连）\n")
	}
	b.WriteString("\n修改: /gateway config url|token|name|auto-connect|proxy <value>；/gateway config disable 停用并清空。")
	return b.String()
}

// gatewayConfigKeyText renders one key's current value (token masked).
func gatewayConfigKeyText(session *ChatSession, key gatewayCommandToken) string {
	cfg := chatGatewayResolveConfig(session)
	switch key {
	case gatewayConfigKeyURL:
		if cfg.URL == "" {
			return "gateway url: （未设置）"
		}
		return "gateway url: " + cfg.URL
	case gatewayConfigKeyToken:
		if hint := cfg.TokenHint(); hint != "" {
			return "gateway token: " + hint
		}
		return "gateway token: （未设置）"
	case gatewayConfigKeyName:
		if cfg.Name == "" {
			return "gateway name: （未设置）"
		}
		return "gateway name: " + cfg.Name
	case gatewayConfigKeyAutoConnect:
		return fmt.Sprintf("gateway auto_connect: %t", cfg.AutoConnect)
	case gatewayConfigKeyProxyURL:
		if cfg.ProxyURL == "" {
			return "gateway proxy: （未设置，直连）"
		}
		return "gateway proxy: " + cfg.ProxyURL
	}
	return gatewayConfigUsageText()
}

// gatewayConfigUsageText explains the /gateway config surface.
func gatewayConfigUsageText() string {
	return strings.Join([]string{
		"用法: /gateway config [key [value]]",
		"  /gateway config                 查看合并后的网关配置（token 仅显示 hint）",
		"  /gateway config url <wss://...> 写入网关地址",
		"  /gateway config token <agw_...> 写入设备令牌（网关控制台签发，只写不回显）",
		"  /gateway config name <名称>     写入节点显示名",
		"  /gateway config auto-connect on|off",
		"                                  启动自动连接开关",
		"  /gateway config proxy <socks5://host:port|http://host:port|off>",
		"                                  经代理隧道连接网关（off=直连）",
		"  /gateway config disable         清空 url/token 并停用网关（持久化变更）",
		"写入立即落盘并热应用到当前会话；环境变量 AICLI_GATEWAY_* 优先于配置文件。",
	}, "\n")
}

// validateGatewayURLValue performs the same URL shape checks the connector
// enforces at dial time so a bad value fails at config time, not at connect.
func validateGatewayURLValue(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("url 不能为空（清除请使用 /gateway config disable）")
	}
	if _, err := gatewayagent.AgentEndpoint(raw); err != nil {
		return fmt.Errorf("url 无效: %v", err)
	}
	return nil
}

// validateGatewayProxyValue checks a proxy specification at config time (the
// same parsing the dialer performs). "off"/"none" clears the proxy.
func validateGatewayProxyValue(raw string) error {
	value := strings.TrimSpace(raw)
	if value == "" {
		return fmt.Errorf("proxy 不能为空（清除请使用 /gateway config proxy off）")
	}
	if strings.EqualFold(value, "off") || strings.EqualFold(value, "none") {
		return nil
	}
	if !strings.Contains(value, "://") {
		value = "socks5://" + value
	}
	if _, err := url.Parse(value); err != nil {
		return fmt.Errorf("proxy 无效: %v", err)
	}
	switch {
	case strings.HasPrefix(strings.ToLower(value), "socks5://"),
		strings.HasPrefix(strings.ToLower(value), "socks5h://"),
		strings.HasPrefix(strings.ToLower(value), "http://"),
		strings.HasPrefix(strings.ToLower(value), "https://"):
		return nil
	}
	return fmt.Errorf("proxy scheme 只支持 socks5/http/https（got %q）", value)
}

// parseGatewayAutoConnectValue maps on/off/true/false to the boolean value.
func parseGatewayAutoConnectValue(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "true", "1", "yes":
		return true, true
	case "off", "false", "0", "no":
		return false, true
	}
	return false, false
}

func ptrString(value string) *string { return &value }

func ptrBool(value bool) *bool { return &value }
