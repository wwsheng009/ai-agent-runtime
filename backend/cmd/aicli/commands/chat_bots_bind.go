package commands

// /bots 绑定管理子命令与参数补全（docs/bots/09-multi-instance-binding.md §5.4）：
//
//	/bots                    打开机器人副屏（既有行为）
//	/bots list               内联列出全部机器人
//	/bots status <id>        连接器详情：运行归属 / 心跳 / 绑定 / 最近错误
//	/bots bind <id> [session] 绑定当前（或指定）会话到连接器；chat 取最近活跃 IM 会话
//	/bots unbind <id> [chat] 解绑（写零值墓碑，等价 IM /new）；缺省 chat = 该连接器唯一活跃绑定
//
// 实现走本机宿主 Store/Service（与 loopback REST /connectors/{id}/bindings 同一
// 数据源与校验口径：enabled + allowed_users + CAS 乐观锁），不直读 state 文件。
// 补全（completeBotsSlashArgs）：二级命令位 → 连接器 id 位（动态读取配置）→
// status 的多余参数位不猜、bind 的 session 位给提示性候选。
//
// 语义约束：
//   - bind 是覆盖式指针切换：旧会话上下文原样保留，仅 bot 换指向；
//   - 会话存在性校验（v1 REST 未做，见 restapi/bindings.go 注释）在 TUI 侧
//     加固：session id 非当前会话时要求出现在 SessionManager 列表内；
//   - 白名单：bind 目标 chat 必须命中 cfg.Policy.AllowedUsers（与入站同源）。

import (
	"context"
	"fmt"
	"strings"
	"time"

	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// botsSubcommands 是二级命令候选（与 botsParseSubcommand / catalog Args 同源）。
func botsSubcommandArgumentCandidates() []chatSlashCompletionCandidate {
	group := string(chatSlashCommandGroupWeb)
	return []chatSlashCompletionCandidate{
		{Command: "status", Summary: "连接器详情：归属 / 心跳 / 绑定 / 最近错误", Group: group, AcceptsArgs: true},
		{Command: "bind", Summary: "绑定当前（或指定）会话到连接器", Group: group, AcceptsArgs: true},
		{Command: "unbind", Summary: "解绑（墓碑；等价 IM /new）", Group: group, AcceptsArgs: true},
		{Command: "list", Summary: "内联列出全部机器人", Group: group},
		{Command: "ls", Summary: "list 的别名", Group: group, AliasOf: "list"},
		{Command: "help", Summary: "显示 /bots 子命令用法", Group: group},
	}
}

// completeBotsSlashArgs 是 `/bots` 参数补全入口：先取宿主（可能触发按需装配，
// 失败则静默降级为仅二级命令候选），再逐位补全。
func completeBotsSlashArgs(session *ChatSession, argsText string, cursor int) []chatSlashCompletionCandidate {
	ctx := parseSlashArgumentContext(argsText, cursor)
	query := activeSlashArgumentQuery(ctx)
	first := strings.ToLower(slashArgumentTokenText(ctx, 0))

	// 首位：二级子命令（或还没输入）。
	if first == "" || slashArgumentCursorInToken(ctx, 0) {
		return matchSlashArgumentCandidates(botsSubcommandArgumentCandidates(), query)
	}

	host, _ := botsResolveHost(session)
	switch first {
	case "status", "unbind":
		// 连接器 id 位（空位或正在输入）：补全已配置的连接器 id。
		if slashArgumentTokenText(ctx, 1) == "" || slashArgumentCursorInToken(ctx, 1) {
			return matchSlashArgumentCandidates(botsConnectorIDArgumentCandidates(host), query)
		}
		if first == "unbind" {
			// /bots unbind <id> [chat]：chat 位补全该连接器现有绑定。
			if slashArgumentTokenText(ctx, 2) == "" || slashArgumentCursorInToken(ctx, 2) {
				return matchSlashArgumentCandidates(botsChatArgumentCandidates(host, slashArgumentTokenText(ctx, 1)), query)
			}
		}
		return nil
	case "bind":
		// /bots bind <id> [session]：id 位补全（仅 enabled，REST 拒绝停用连接器）。
		if slashArgumentTokenText(ctx, 1) == "" || slashArgumentCursorInToken(ctx, 1) {
			return matchSlashArgumentCandidates(botsBindableConnectorIDArgumentCandidates(host), query)
		}
		// session 位：提示性候选（缺省=当前会话），不做枚举猜测。
		if slashArgumentTokenText(ctx, 2) == "" || slashArgumentCursorInToken(ctx, 2) {
			return []chatSlashCompletionCandidate{{
				Command:       "（缺省 = 当前会话）",
				Summary:       "回车跳过即绑定当前会话；也可粘贴目标 session id",
				Group:         string(chatSlashCommandGroupWeb),
				Informational: true,
			}}
		}
		return nil
	default:
		// list/help 与多余参数位不猜。
		return nil
	}
}

// botsConnectorIDArgumentCandidates 返回全部已配置连接器 id（含停用，供 status）。
func botsConnectorIDArgumentCandidates(host *LocalConnectorHost) []chatSlashCompletionCandidate {
	group := string(chatSlashCommandGroupWeb)
	configs := botsCompletionConfigs(host)
	out := make([]chatSlashCompletionCandidate, 0, len(configs))
	for _, cfg := range configs {
		out = append(out, chatSlashCompletionCandidate{
			Command: cfg.ID,
			Summary: botsCompletionConnectorSummary(cfg),
			Group:   group,
		})
	}
	return out
}

// botsBindableConnectorIDArgumentCandidates 返回可绑定的连接器 id（enabled，
// 与 REST PUT bindings 的 409 口径一致）。
func botsBindableConnectorIDArgumentCandidates(host *LocalConnectorHost) []chatSlashCompletionCandidate {
	group := string(chatSlashCommandGroupWeb)
	configs := botsCompletionConfigs(host)
	out := make([]chatSlashCompletionCandidate, 0, len(configs))
	for _, cfg := range configs {
		if !cfg.Enabled {
			continue
		}
		out = append(out, chatSlashCompletionCandidate{
			Command: cfg.ID,
			Summary: botsCompletionConnectorSummary(cfg),
			Group:   group,
		})
	}
	return out
}

// botsChatArgumentCandidates 返回指定连接器的现有绑定 chat（供 unbind 补全；
// 未知连接器返回 nil）。
func botsChatArgumentCandidates(host *LocalConnectorHost, connectorID string) []chatSlashCompletionCandidate {
	if host == nil || host.Store() == nil {
		return nil
	}
	connectorID = strings.TrimSpace(connectorID)
	if connectorID == "" {
		return nil
	}
	if _, ok := host.Store().GetConfig(connectorID); !ok {
		return nil
	}
	group := string(chatSlashCommandGroupWeb)
	bindings := host.Store().ListBindings(connectorID)
	out := make([]chatSlashCompletionCandidate, 0, len(bindings))
	for _, binding := range bindings {
		summary := "未绑定会话（墓碑）"
		if sessionID := strings.TrimSpace(binding.Session.SessionID); sessionID != "" {
			summary = "→ " + sessionID
		}
		out = append(out, chatSlashCompletionCandidate{
			Command: binding.Chat,
			Summary: summary,
			Group:   group,
		})
	}
	return out
}

// botsCompletionConfigs 宿主为空时静默返回空切片（补全不报错、不阻塞输入）。
func botsCompletionConfigs(host *LocalConnectorHost) []connectors.Config {
	if host == nil || host.Store() == nil {
		return nil
	}
	return botsSortedConfigs(host)
}

// botsCompletionConnectorSummary 是 id 候选的摘要（id 已是候选文本本身）。
func botsCompletionConnectorSummary(cfg connectors.Config) string {
	parts := []string{cfg.Provider}
	if name := strings.TrimSpace(cfg.Name); name != "" && name != cfg.ID {
		parts = append(parts, name)
	}
	if cfg.Enabled {
		parts = append(parts, "已启用")
	} else {
		parts = append(parts, "已停用")
	}
	return strings.Join(parts, " · ")
}

// ── 子命令执行 ───────────────────────────────────────────────────────────────

// executeBotsSubcommand 统一命令管线的子命令执行体（structured 输出）。
// command 是完整命令行（/bots status tg-1），从中解析子命令与参数。
func executeBotsSubcommand(session *ChatSession, command string) CommandResult {
	return commandTextResult(renderBotsSubcommandText(session, command))
}

// renderBotsSubcommandText 执行 /bots 子命令并产出纯文本文档（structured 与
// legacy 出口共用；错误也走文档而不是直接失败，便于统一渲染）。
func renderBotsSubcommandText(session *ChatSession, command string) string {
	sub, rest := botsSubcommandArgumentSplit(command)
	switch sub {
	case "list":
		host, reason := botsResolveHost(session)
		if host == nil {
			return botsUnavailableText(reason)
		}
		return botsInlineDoc(host)
	case "help":
		return botsSubcommandHelpText()
	case "status":
		return renderBotsStatusSubcommand(session, rest)
	case "bind":
		return renderBotsBindSubcommand(session, rest)
	case "unbind":
		return renderBotsUnbindSubcommand(session, rest)
	default:
		return botsSubcommandHelpText()
	}
}

// botsSubcommandHelpText 是 /bots help 的说明。
func botsSubcommandHelpText() string {
	return strings.Join([]string{
		"/bots 子命令（docs/bots/09 §5.4）:",
		"  /bots                     打开机器人管理副屏（新增/扫码/启停/日志）",
		"  /bots list                内联列出全部机器人",
		"  /bots status [id]         连接器详情：运行归属 / 心跳 / 绑定 / 最近错误；缺省列全部",
		"  /bots bind <id> [session] 绑定当前（或指定）会话到连接器；chat 取最近活跃 IM 会话",
		"  /bots unbind <id> [chat]  解绑（缺省 chat = 该连接器最近活跃绑定）",
	}, "\n")
}

// botsSubcommandArgumentSplit 取规范化子命令与其后的参数字段。
func botsSubcommandArgumentSplit(command string) (sub string, rest []string) {
	sub = botsParseSubcommand(command)
	if sub == "" {
		return "", nil
	}
	argument := strings.TrimSpace(extractCommandArgument(command))
	fields := strings.Fields(argument)
	if len(fields) > 1 {
		rest = fields[1:]
	}
	return sub, rest
}

// botsPickMostRecentBinding 选最近活跃绑定（UpdatedAt 最新；墓碑也参与——
// 它代表最近一次显式 /new 的对象）。
func botsPickMostRecentBinding(bindings []connectors.Binding) (connectors.Binding, bool) {
	var best connectors.Binding
	found := false
	for _, binding := range bindings {
		if !found || binding.UpdatedAt.After(best.UpdatedAt) {
			best = binding
			found = true
		}
	}
	return best, found
}

// botsPickMostRecentActiveBinding 选最近活跃且仍有会话的绑定（墓碑跳过）：
// bind 的改绑目标与 unbind 的缺省对象都应是"仍在服务的 chat"。
func botsPickMostRecentActiveBinding(bindings []connectors.Binding) (connectors.Binding, bool) {
	var best connectors.Binding
	found := false
	for _, binding := range bindings {
		if binding.Session.IsZero() {
			continue
		}
		if !found || binding.UpdatedAt.After(best.UpdatedAt) {
			best = binding
			found = true
		}
	}
	return best, found
}

// botsFormatBindingText 是绑定的单行展示。
func botsFormatBindingText(binding connectors.Binding) string {
	if sessionID := strings.TrimSpace(binding.Session.SessionID); sessionID != "" {
		nodeID := strings.TrimSpace(binding.Session.NodeID)
		if nodeID != "" {
			return fmt.Sprintf("%s → %s @ %s", binding.Chat, sessionID, nodeID)
		}
		return fmt.Sprintf("%s → %s", binding.Chat, sessionID)
	}
	return fmt.Sprintf("%s → （未绑定）", binding.Chat)
}

// botsFormatOwnerText 是运行归属的单行展示（含陈旧标记）。
func botsFormatOwnerText(record connectors.OwnerRecord, now time.Time) string {
	if record.Owner == "" {
		return "（无归属）"
	}
	state := "运行中"
	if record.Expired(now, 0) {
		state = "已陈旧（可接管）"
	}
	heartbeat := "无心跳"
	if !record.HeartbeatAt.IsZero() {
		heartbeat = record.HeartbeatAt.Local().Format("01-02 15:04:05")
	}
	return fmt.Sprintf("%s（%s，心跳 %s）", record.Owner, state, heartbeat)
}

// botsSortBindingsByIDChat 输出确定（复用 connectors.SortBindings 口径）。
func botsSortBindingsByIDChat(bindings []connectors.Binding) {
	connectors.SortBindings(bindings)
}

// renderBotsStatusSubcommand 处理 /bots status [id]：缺省按 id 列出全部。
func renderBotsStatusSubcommand(session *ChatSession, rest []string) string {
	host, reason := botsResolveHost(session)
	if host == nil {
		return botsUnavailableText(reason)
	}
	store := host.Store()
	if store == nil {
		return "机器人管理不可用：连接器存储未就绪。"
	}
	now := time.Now()
	if len(rest) > 0 {
		id := rest[0]
		cfg, ok := store.GetConfig(id)
		if !ok {
			return fmt.Sprintf("未知连接器：%s（/bots list 查看全部）", id)
		}
		return strings.Join(botsStatusLines(host, cfg, now), "\n")
	}
	configs := botsSortedConfigs(host)
	if len(configs) == 0 {
		return "（暂无机器人；输入 /bots 打开副屏新增）"
	}
	lines := []string{"机器人（连接器）状态:"}
	for _, cfg := range configs {
		lines = append(lines, "  "+botsConnectorTitle(cfg)+" ["+cfg.ID+"]")
		for _, line := range botsStatusDetailLines(host, cfg, now) {
			lines = append(lines, "    "+line)
		}
	}
	return strings.Join(lines, "\n")
}

// botsStatusLines 是单连接器的 status 输出（title 行 + 详情行）。
func botsStatusLines(host *LocalConnectorHost, cfg connectors.Config, now time.Time) []string {
	lines := []string{"机器人：" + botsConnectorTitle(cfg) + " [" + cfg.ID + "]"}
	lines = append(lines, botsStatusDetailLines(host, cfg, now)...)
	return lines
}

// botsStatusDetailLines 组装 status 详情：启停 / 归属 / 心跳 / 绑定 / 统计。
func botsStatusDetailLines(host *LocalConnectorHost, cfg connectors.Config, now time.Time) []string {
	store := host.Store()
	lines := []string{
		fmt.Sprintf("  通道=%s 模式=%s 状态=%s", cfg.Provider, firstNonEmptyChatValue(cfg.Mode, "（默认）"),
			boolChatEnabledText(cfg.Enabled)),
	}
	if store != nil {
		if owner, ok := store.OwnerOf(cfg.ID); ok {
			lines = append(lines, "  归属: "+botsFormatOwnerText(owner, now))
		} else {
			lines = append(lines, "  归属: （无记录，未被任何宿主持有）")
		}
		bindings := store.ListBindings(cfg.ID)
		if len(bindings) == 0 {
			lines = append(lines, "  绑定: （无；IM 新消息将 auto-create 会话）")
		} else {
			botsSortBindingsByIDChat(bindings)
			rendered := make([]string, 0, len(bindings))
			for _, binding := range bindings {
				rendered = append(rendered, "    "+botsFormatBindingText(binding))
			}
			lines = append(lines, append([]string{fmt.Sprintf("  绑定: %d 条", len(bindings))}, rendered...)...)
		}
	}
	if service := host.Service(); service != nil {
		if stats, ok := service.Stats(cfg.ID); ok {
			summary := fmt.Sprintf("  统计: 入%d/出%d/错%d（入队去重跳过%d，队列满%d）",
				stats.Inbound, stats.Outbound, stats.Errors, stats.DedupSkipped, stats.QueueFull)
			if stats.LastError != "" {
				at := ""
				if !stats.LastErrorAt.IsZero() {
					at = " @ " + stats.LastErrorAt.Local().Format("01-02 15:04:05")
				}
				summary += "\n    最近错误: " + stats.LastError + at
			}
			lines = append(lines, summary)
		}
	}
	return lines
}

// boolChatEnabledText 是启停状态的中文展示。
func boolChatEnabledText(enabled bool) string {
	if enabled {
		return "已启用"
	}
	return "已停用"
}

// renderBotsBindSubcommand 处理 /bots bind <id> [session]：
//   - session 缺省 = 当前会话（currentRuntimeSessionID）；
//   - session 显式指定时做存在性校验（SessionManager 可加载）；
//   - chat 取该连接器最近活跃绑定；无绑定时报错（没有最近活跃 IM 会话时，
//     直接在 IM 里发一条消息产生绑定后即可用 /bots bind 改绑）。
func renderBotsBindSubcommand(session *ChatSession, rest []string) string {
	if len(rest) == 0 {
		return "用法: /bots bind <连接器id> [session]\n（id 见 /bots list；session 缺省 = 当前会话）"
	}
	connectorID := rest[0]
	host, reason := botsResolveHost(session)
	if host == nil {
		return botsUnavailableText(reason)
	}
	store := host.Store()
	if store == nil {
		return "机器人管理不可用：连接器存储未就绪。"
	}
	cfg, ok := store.GetConfig(connectorID)
	if !ok {
		return fmt.Sprintf("未知连接器：%s（/bots list 查看全部）", connectorID)
	}
	if !cfg.Enabled {
		return fmt.Sprintf("连接器已停用：%s（先在 /bots 副屏或 web 页启用后再绑定）", connectorID)
	}

	sessionID := currentRuntimeSessionID(session)
	if len(rest) > 1 {
		sessionID = rest[1]
		if session.SessionManager != nil {
			_, err := session.SessionManager.Get(context.Background(), sessionID)
			if err != nil {
				return fmt.Sprintf("会话不存在或不可读取：%s（缺省参数即绑定当前会话）", sessionID)
			}
		}
	}
	if sessionID == "" {
		return "当前没有活动会话，无法绑定；请指定 session id 或先创建会话。"
	}

	bindings := store.ListBindings(connectorID)
	binding, hasChat := botsPickMostRecentActiveBinding(bindings)
	if !hasChat {
		return fmt.Sprintf("连接器 %s 尚无活跃的 IM 会话绑定：先在 IM 里给机器人发一条消息产生会话，再用 /bots bind %s 改绑。",
			connectorID, connectorID)
	}
	chat := binding.Chat
	// 与 REST PUT bindings 同源校验（docs/bots/09 §8）：白名单外的 chat 不能 bind。
	if !cfg.Policy.AllowsUser(chat) {
		return fmt.Sprintf("chat %q 不在连接器 %s 的 allowed_users 白名单内，拒绝绑定。", chat, connectorID)
	}

	expected := binding.UpdatedAt
	next := connectors.Binding{ConnectorID: connectorID, Chat: chat}
	next.Session = connectors.SessionRef{SessionID: sessionID}
	swapped, err := botsCASBinding(store, connectorID, chat, expected, next, !binding.Session.IsZero())
	if err != nil {
		return "绑定写入失败：" + err.Error()
	}
	if !swapped {
		return fmt.Sprintf("绑定已被其它操作更新（乐观锁未命中），请重试：/bots bind %s", connectorID)
	}
	logpkg.Infof("connectors: bind %s chat=%s session=%q (via tui /bots bind)", connectorID, chat, sessionID)
	return fmt.Sprintf("已绑定：连接器 %s 的 chat %q → 会话 %s\n（覆盖式指针切换：旧会话上下文原样保留）",
		connectorID, chat, sessionID)
}

// renderBotsUnbindSubcommand 处理 /bots unbind <id> [chat]：
// chat 缺省 = 最近活跃绑定；写零值墓碑（显式解绑 ≠ 删除记录）。
func renderBotsUnbindSubcommand(session *ChatSession, rest []string) string {
	if len(rest) == 0 {
		return "用法: /bots unbind <连接器id> [chat]\n（chat 缺省 = 该连接器最近活跃绑定）"
	}
	connectorID := rest[0]
	host, reason := botsResolveHost(session)
	if host == nil {
		return botsUnavailableText(reason)
	}
	store := host.Store()
	if store == nil {
		return "机器人管理不可用：连接器存储未就绪。"
	}
	if _, ok := store.GetConfig(connectorID); !ok {
		return fmt.Sprintf("未知连接器：%s（/bots list 查看全部）", connectorID)
	}
	var chat string
	if len(rest) > 1 {
		chat = rest[0+1]
		current, ok := store.GetBinding(connectorID, chat)
		if !ok {
			return fmt.Sprintf("连接器 %s 没有 chat %q 的绑定记录。", connectorID, chat)
		}
		if swapped, err := botsCASBinding(store, connectorID, chat, current.UpdatedAt,
			connectors.Binding{ConnectorID: connectorID, Chat: chat}, true); err != nil {
			return "解绑写入失败：" + err.Error()
		} else if !swapped {
			return fmt.Sprintf("绑定已被其它操作更新（乐观锁未命中），请重试：/bots unbind %s %s", connectorID, chat)
		}
		botsLogUnbind(host, connectorID, chat)
		return fmt.Sprintf("已解绑：连接器 %s 的 chat %q（写入墓碑；新消息将 auto-create 新会话）。", connectorID, chat)
	}

	// 缺省 chat = 最近活跃绑定（优先仍有会话的绑定；墓碑已解绑，跳过）。
	binding, ok := botsPickMostRecentActiveBinding(store.ListBindings(connectorID))
	if !ok {
		return fmt.Sprintf("连接器 %s 没有可解绑的活跃绑定（仅有墓碑或无记录）。", connectorID)
	}
	chat = binding.Chat
	swapped, err := botsCASBinding(store, connectorID, chat, binding.UpdatedAt,
		connectors.Binding{ConnectorID: connectorID, Chat: chat}, true)
	if err != nil {
		return "解绑写入失败：" + err.Error()
	}
	if !swapped {
		return fmt.Sprintf("绑定已被其它操作更新（乐观锁未命中），请重试：/bots unbind %s", connectorID)
	}
	botsLogUnbind(host, connectorID, chat)
	return fmt.Sprintf("已解绑：连接器 %s 的 chat %q（写入墓碑；新消息将 auto-create 新会话）。", connectorID, chat)
}

// botsLogUnbind 记录解绑审计日志（与 REST deleteBinding 同款格式）。
func botsLogUnbind(_ *LocalConnectorHost, connectorID, chat string) {
	logpkg.Infof("connectors: unbind %s chat=%s (via tui /bots unbind)", connectorID, chat)
}

// botsCASBinding 是 Store.CompareAndSwapBinding 的 TUI 侧包装（nil store 防御）。
func botsCASBinding(store connectors.Store, connectorID, chat string, expected time.Time, next connectors.Binding, hasExpected bool) (bool, error) {
	if store == nil {
		return false, fmt.Errorf("连接器存储未就绪")
	}
	if !hasExpected {
		expected = time.Time{}
	}
	_, swapped, err := store.CompareAndSwapBinding(connectorID, chat, expected, next)
	return swapped, err
}
