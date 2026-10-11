package commands

import (
	"strings"
)

// chat_gateway_completion.go 为 `/gateway` 提供逐位参数补全（Batch 13 G4 同款
// 纪律，见 chat_profile_completion.go）：
//
//   - 首位：二级子命令（status/connect/.../config/help，与
//     executeStructuredGatewayCommand 的 switch 分派一一对应）；
//   - config <key>：可配置键（url/token/name/auto-connect）；
//   - config auto-connect <value>：on|off 枚举（`--gateway-auto-connect=false`
//     等价 on/off 的反义）；
//   - config url/token/name <value>：自由文本位，不枚举、不猜值；
//   - 其余子命令无参数位。
//
// 补全只影响输入：不改解析、不改执行语义；读不到当前配置时不伪造候选。
//
// gatewaySubcommandArgumentCandidates 与 executeStructuredGatewayCommand 的
// switch 分支保持同源（新增子命令时两处同步）。
func gatewaySubcommandArgumentCandidates() []chatSlashCompletionCandidate {
	group := string(chatSlashCommandGroupWeb)
	return []chatSlashCompletionCandidate{
		{Command: "status", Summary: "显示网关连接状态（默认）", Group: group},
		{Command: "connect", Summary: "手动连接（需已配置 url/token）", Group: group},
		{Command: "disconnect", Summary: "手动断开（抑制自动重连，不改配置）", Group: group},
		{Command: "reconnect", Summary: "断开后立即重连", Group: group},
		{Command: "url", Summary: "显示网关注册/控制台地址", Group: group},
		{Command: "config", Summary: "查看或写入持久化配置（url/token/name/auto-connect）", Group: group, AcceptsArgs: true},
		{Command: "help", Summary: "显示 /gateway 用法", Group: group},
	}
}

// gatewayConfigKeyArgumentCandidates 是 `/gateway config` 的 <key> 位候选。
// 与 gatewayCommandToken / parseGatewayConfigArgs 的解析键保持同源。
func gatewayConfigKeyArgumentCandidates() []chatSlashCompletionCandidate {
	group := string(chatSlashCommandGroupWeb)
	return []chatSlashCompletionCandidate{
		{Command: "url", Summary: "网关地址（如 wss://gw.example.com）", Group: group, AcceptsArgs: true},
		{Command: "token", Summary: "设备注册令牌（agw_...，网关控制台签发）", Group: group, AcceptsArgs: true},
		{Command: "name", Summary: "节点显示名", Group: group, AcceptsArgs: true},
		{Command: "auto-connect", Summary: "启动自动连接 on|off", Group: group, AcceptsArgs: true},
		{Command: "disable", Summary: "清空 url/token 并停用网关", Group: group},
	}
}

// gatewayAutoConnectValueArgumentCandidates 是 `/gateway config auto-connect`
// 取值位的枚举候选。
func gatewayAutoConnectValueArgumentCandidates() []chatSlashCompletionCandidate {
	group := string(chatSlashCommandGroupWeb)
	return []chatSlashCompletionCandidate{
		{Command: "on", Summary: "会话启动时自动连接网关", Group: group},
		{Command: "off", Summary: "启动不自动连接，仍可用 /gateway connect 手动连接", Group: group},
	}
}

// completeGatewaySlashArgs 是 `/gateway` 参数补全入口
// （CompleteSlashArgs 的 case）。逐位：子命令 → config <key> → key 取值。
func completeGatewaySlashArgs(session *ChatSession, argsText string, cursor int) []chatSlashCompletionCandidate {
	_ = session // 与其它补全入口同签名；gateway 解析不读会话状态
	ctx := parseSlashArgumentContext(argsText, cursor)
	query := activeSlashArgumentQuery(ctx)

	if gatewayAutoConnectValueFocus(ctx) {
		return matchSlashArgumentCandidates(gatewayAutoConnectValueArgumentCandidates(), query)
	}

	first := strings.ToLower(slashArgumentTokenText(ctx, 0))
	if first == "" || slashArgumentCursorInToken(ctx, 0) {
		return matchSlashArgumentCandidates(gatewaySubcommandArgumentCandidates(), query)
	}
	if first != "config" {
		// 其余子命令不接受参数：多余的参数位不猜。
		return nil
	}
	// `/gateway config `（或正在输入 key）。
	if slashArgumentTokenText(ctx, 1) == "" || slashArgumentCursorInToken(ctx, 1) {
		return matchSlashArgumentCandidates(gatewayConfigKeyArgumentCandidates(), query)
	}
	// 已有 key：url/token/name 是自由文本位（不枚举）；auto-connect 的取值位
	// 已在 gatewayAutoConnectValueFocus 处理；其余位置不猜。
	return nil
}

// gatewayAutoConnectValueFocus 判断光标是否落在 config auto-connect 的取值位
// （"auto-connect=on" 的等号后取值，或 "auto-connect " 之后的下一个 token）。
func gatewayAutoConnectValueFocus(ctx slashArgumentContext) bool {
	if strings.HasPrefix(strings.ToLower(slashArgumentTokenText(ctx, 0)), "config") &&
		strings.HasPrefix(strings.ToLower(slashArgumentTokenText(ctx, 1)), "auto-connect") {
		// 光标在 auto-connect token 内且已越过等号 → 取值位。
		token := strings.TrimSpace(ctx.Current.Text)
		if slashArgumentCursorInToken(ctx, 1) {
			if eq := strings.Index(strings.ToLower(token), "="); eq >= 0 &&
				ctx.Cursor-ctx.Current.Start > eq {
				return true
			}
			return false
		}
		// 光标在第三个 token（等号被空格拆开或独立取值 token）。
		return slashArgumentTokenText(ctx, 2) == "" || slashArgumentCursorInToken(ctx, 2)
	}
	// `config auto-connect=on` 形式：等号在同一 token。
	if strings.EqualFold(slashArgumentTokenText(ctx, 1), "config") {
		token := strings.ToLower(strings.TrimSpace(ctx.Current.Text))
		if strings.HasPrefix(token, "auto-connect=") {
			return true
		}
	}
	return false
}
