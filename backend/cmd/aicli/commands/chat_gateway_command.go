package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
)

// gatewayCommandTimeout bounds a manual connect/disconnect/reconnect so a hung
// dial cannot wedge the interactive command path.
const gatewayCommandTimeout = 15 * time.Second

// executeStructuredGatewayCommand implements the `/gateway` command
// (docs/gateway/12-connection-control.md §4). It always claims the command
// (handled=true) so no variant falls through to a legacy handler or the model.
func executeStructuredGatewayCommand(session *ChatSession, command string) (CommandResult, bool) {
	if session == nil {
		return commandTextResult("gateway: 当前没有活动会话"), true
	}
	controller := session.Gateway
	sub := gatewaySubcommand(command)
	// config 是持久化配置面，必须在 controller==nil 之前处理：它正是"未配置
	// 时写入配置"的入口（docs/gateway/12-connection-control.md §6.2）。
	if sub == "config" {
		return executeStructuredGatewayConfigCommand(session, command)
	}
	if controller == nil {
		return commandTextResult(gatewayNotConfiguredText()), true
	}
	switch sub {
	case "", "status":
		return commandTextResult(formatGatewayStatus(controller.Status())), true
	case "url":
		return commandTextResult(formatGatewayURL(controller.Status())), true
	case "connect":
		return gatewayAction(controller, "connect", controller.ConnectManual), true
	case "disconnect":
		return gatewayAction(controller, "disconnect", controller.Disconnect), true
	case "reconnect":
		return gatewayAction(controller, "reconnect", controller.Reconnect), true
	case "config":
		// `/gateway config` 是持久化配置面（docs/gateway/
		// 12-connection-control.md §6.2）：写 gateway.* 键并热应用。
		return executeStructuredGatewayConfigCommand(session, command)
	case "help":
		return commandTextResult(gatewayHelpText()), true
	default:
		return commandTextResult("未知的 /gateway 子命令: " + sub + "\n\n" + gatewayHelpText()), true
	}
}

// gatewayAction runs one connection mutation and renders the resulting status.
func gatewayAction(controller *gatewayagent.Controller, action string, run func(context.Context) error) CommandResult {
	ctx, cancel := context.WithTimeout(context.Background(), gatewayCommandTimeout)
	defer cancel()
	if err := run(ctx); err != nil {
		return commandTextResult("gateway " + action + " 失败: " + err.Error() + "\n\n" + formatGatewayStatus(controller.Status()))
	}
	return commandTextResult("gateway " + action + " 完成\n\n" + formatGatewayStatus(controller.Status()))
}

// gatewaySubcommand extracts the first argument token, lower-cased.
func gatewaySubcommand(command string) string {
	arg := strings.TrimSpace(extractCommandArgument(command))
	if arg == "" {
		return ""
	}
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(fields[0])
}

// formatGatewayStatus renders a status snapshot as a compact multi-line report.
func formatGatewayStatus(st gatewayagent.Status) string {
	var b strings.Builder
	b.WriteString("gateway: ")
	b.WriteString(string(st.State))
	if st.OperatorDisconnected {
		b.WriteString("（已手动断开，自动重连已抑制）")
	}
	b.WriteByte('\n')
	if st.NodeID != "" {
		fmt.Fprintf(&b, "  node_id     %s\n", st.NodeID)
	}
	if st.GatewayURL != "" {
		fmt.Fprintf(&b, "  gateway     %s\n", st.GatewayURL)
	}
	if st.ConsoleURL != "" {
		fmt.Fprintf(&b, "  console     %s\n", st.ConsoleURL)
	}
	if !st.ConnectedAt.IsZero() {
		fmt.Fprintf(&b, "  connected   %s\n", st.ConnectedAt.Format(time.RFC3339))
	}
	if !st.LastHeartbeat.IsZero() {
		fmt.Fprintf(&b, "  heartbeat   %s\n", st.LastHeartbeat.Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "  sessions    %d\n", st.Sessions)
	fmt.Fprintf(&b, "  reconnects  %d\n", st.ReconnectCount)
	fmt.Fprintf(&b, "  mode        %s\n", st.Mode)
	if st.ErrorCode != "" || st.LastError != "" {
		fmt.Fprintf(&b, "  error       %s %s\n", st.ErrorCode, st.LastError)
		if !st.Retryable {
			b.WriteString("  hint        该错误不可自动重试；请检查令牌/配置后执行 /gateway connect\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatGatewayURL renders the registration/console address.
func formatGatewayURL(st gatewayagent.Status) string {
	if st.ConsoleURL != "" {
		return "console: " + st.ConsoleURL
	}
	if st.GatewayURL != "" {
		return "gateway: " + st.GatewayURL + "（尚未注册，暂无 console 地址）"
	}
	return "gateway: 未配置"
}

// gatewayNotConfiguredText explains how to enable the gateway.
func gatewayNotConfiguredText() string {
	return "gateway: 未配置\n\n" +
		"本会话未接入网关。可即时写入配置（无需重启会话）：\n" +
		"  /gateway config url wss://gw.example.com\n" +
		"  /gateway config token agw_xxx（网关控制台签发，配置后 /gateway connect 立即连接）\n" +
		"或以启动参数/环境变量提供：\n" +
		"  --gateway <wss://gw.example.com> --gateway-token <agw_...>\n" +
		"  或配置 gateway.enabled/url/token（见 docs/gateway/12-connection-control.md）\n\n" +
		gatewayHelpText()
}

// gatewayHelpText lists the supported subcommands.
func gatewayHelpText() string {
	return strings.Join([]string{
		"/gateway            显示网关连接状态",
		"/gateway status     同 /gateway",
		"/gateway connect    手动连接（需已配置 url/token）",
		"/gateway disconnect 手动断开（抑制自动重连，不改配置）",
		"/gateway reconnect  断开后立即重连",
		"/gateway url        显示网关注册/控制台地址",
		"/gateway config     查看/写入持久化配置（url/token/name/auto-connect/disable）",
		"/gateway help       显示本帮助",
	}, "\n")
}
