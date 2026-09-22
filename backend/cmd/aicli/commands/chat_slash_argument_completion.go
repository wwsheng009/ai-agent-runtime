package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

type chatSlashArgumentProvider interface {
	CompleteSlashArgs(session *ChatSession, command string, argsText string, cursor int) []chatSlashCompletionCandidate
}

type chatSlashArgumentCompletionProvider struct {
	mu          sync.Mutex
	resumeCache map[slashSessionCacheKey][]chatSlashCompletionCandidate
	loadCache   map[slashSessionCacheKey][]chatSlashCompletionCandidate
}

type slashSessionCacheKey struct {
	manager   *runtimechat.SessionManager
	userID    string
	currentID string
}

type slashArgumentToken struct {
	Text  string
	Start int
	End   int
}

type slashArgumentContext struct {
	Tokens    []slashArgumentToken
	Current   slashArgumentToken
	Previous  slashArgumentToken
	CurrentOK bool
	Query     string
	Cursor    int
}

func newChatSlashArgumentCompletionProvider() chatSlashArgumentProvider {
	return &chatSlashArgumentCompletionProvider{
		resumeCache: make(map[slashSessionCacheKey][]chatSlashCompletionCandidate),
		loadCache:   make(map[slashSessionCacheKey][]chatSlashCompletionCandidate),
	}
}

func (p *chatSlashArgumentCompletionProvider) CompleteSlashArgs(session *ChatSession, command string, argsText string, cursor int) []chatSlashCompletionCandidate {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	if spec, ok := resolveSlashCommandSpecForToken(command); ok {
		command = spec.Name
	}

	switch command {
	case "/provider":
		return completeModelSlashArgs(session, argsText, cursor)
	case "/model":
		return completeModelSlashArgs(session, argsText, cursor)
	case "/login":
		return completeLoginSlashArgs(session, argsText, cursor)
	case "/account":
		return completeChatAccountSlashArgs(session, argsText, cursor)
	case "/accounts":
		return completeChatAccountsSlashArgs(session, argsText, cursor)
	case "/stream":
		return completeStaticSlashArgs(argsText, cursor, []chatSlashCompletionCandidate{
			{Command: "on", Summary: "开启流式输出", Group: string(chatSlashCommandGroupModel)},
			{Command: "off", Summary: "关闭流式输出", Group: string(chatSlashCommandGroupModel)},
			{Command: "toggle", Summary: "切换流式状态", Group: string(chatSlashCommandGroupModel)},
			{Command: "status", Summary: "查看当前状态", Group: string(chatSlashCommandGroupModel)},
		})
	case "/fast":
		return completeStaticSlashArgs(argsText, cursor, []chatSlashCompletionCandidate{
			{Command: "on", Summary: "开启 Fast（priority）", Group: string(chatSlashCommandGroupModel)},
			{Command: "off", Summary: "关闭 Fast", Group: string(chatSlashCommandGroupModel)},
			{Command: "toggle", Summary: "切换 Fast 状态", Group: string(chatSlashCommandGroupModel)},
			{Command: "status", Summary: "查看当前状态", Group: string(chatSlashCommandGroupModel)},
		})
	case "/theme":
		return completeStaticSlashArgs(argsText, cursor, themeSlashArgumentCandidates())
	case "/reasoning":
		return completeStaticSlashArgs(argsText, cursor, []chatSlashCompletionCandidate{
			{Command: "on", Summary: "输出 reasoning 内容", Group: string(chatSlashCommandGroupModel)},
			{Command: "off", Summary: "只显示 thinking 状态", Group: string(chatSlashCommandGroupModel)},
			{Command: "status", Summary: "查看当前状态", Group: string(chatSlashCommandGroupModel)},
		})
	case "/reasoning_effort":
		return completeReasoningEffortSlashArgs(session, argsText, cursor)
	case "/compact":
		return completeStaticSlashArgs(argsText, cursor, []chatSlashCompletionCandidate{
			{Command: "auto", Summary: "自动模式", Group: string(chatSlashCommandGroupModel)},
			{Command: "local", Summary: "本地压缩", Group: string(chatSlashCommandGroupModel)},
			{Command: "remote", Summary: "远端压缩", Group: string(chatSlashCommandGroupModel)},
		})
	case "/backtrack", "/rewind":
		return completeStaticSlashArgs(argsText, cursor, []chatSlashCompletionCandidate{
			{Command: "list", Summary: "列出 user turns", Group: string(chatSlashCommandGroupSession)},
			{Command: "select", Summary: "打开交互选择器（Esc 空输入等价）", Group: string(chatSlashCommandGroupSession)},
			{Command: "audit", Summary: "列出 durable tombstone 审计摘要", Group: string(chatSlashCommandGroupSession)},
			{Command: "--apply", Summary: "执行截断", Group: string(chatSlashCommandGroupSession)},
			{Command: "--both", Summary: "对话+代码联合回退", Group: string(chatSlashCommandGroupSession)},
			{Command: "--code", Summary: "仅代码恢复", Group: string(chatSlashCommandGroupSession)},
			{Command: "--edit", Summary: "编辑后的提示", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
			{Command: "--submit", Summary: "截断后自动发送", Group: string(chatSlashCommandGroupSession)},
			{Command: "--include-anchor", Summary: "保留锚点消息", Group: string(chatSlashCommandGroupSession)},
		})
	case "/memory":
		return completeStaticSlashArgs(argsText, cursor, []chatSlashCompletionCandidate{
			{Command: "status", Summary: "查看记忆路径与最近笔记", Group: string(chatSlashCommandGroupContext)},
			{Command: "add", Summary: "追加一条笔记", Group: string(chatSlashCommandGroupContext), AcceptsArgs: true},
			{Command: "note", Summary: "add 的别名", Group: string(chatSlashCommandGroupContext), AcceptsArgs: true},
			{Command: "list", Summary: "列出最近笔记", Group: string(chatSlashCommandGroupContext), AcceptsArgs: true},
			{Command: "search", Summary: "关键词搜索", Group: string(chatSlashCommandGroupContext), AcceptsArgs: true},
			{Command: "flush", Summary: "add 的别名", Group: string(chatSlashCommandGroupContext), AcceptsArgs: true},
		})
	case "/permission-mode":
		return completeStaticSlashArgs(argsText, cursor, []chatSlashCompletionCandidate{
			{Command: "default", Summary: "默认权限", Group: string(chatSlashCommandGroupPermission)},
			{Command: "accept_edits", Summary: "允许编辑", Group: string(chatSlashCommandGroupPermission)},
			{Command: "plan", Summary: "计划模式", Group: string(chatSlashCommandGroupPermission)},
			{Command: "bypass_permissions", Summary: "绕过权限", Group: string(chatSlashCommandGroupPermission)},
		})
	case "/approval-reuse":
		return completeStaticSlashArgs(argsText, cursor, []chatSlashCompletionCandidate{
			{Command: "status", Summary: "查看复用授权和剩余时间", Group: string(chatSlashCommandGroupPermission)},
			{Command: "clear", Summary: "撤销全部复用授权", Group: string(chatSlashCommandGroupPermission)},
			{Command: "off", Summary: "关闭审批复用", Group: string(chatSlashCommandGroupPermission)},
			{Command: "session_readonly_shell", Summary: "会话只读 shell", Group: string(chatSlashCommandGroupPermission)},
			{Command: "team_readonly_shell", Summary: "团队只读 shell", Group: string(chatSlashCommandGroupPermission)},
		})
	case "/attach":
		return completeStaticSlashArgs(argsText, cursor, []chatSlashCompletionCandidate{
			{Command: "clear", Summary: "清空待发送图片附件", Group: string(chatSlashCommandGroupContext)},
			{Command: "remove", Summary: "移除指定图片附件", Group: string(chatSlashCommandGroupContext), AcceptsArgs: true},
		})
	case "/image":
		return completeStaticSlashArgs(argsText, cursor, []chatSlashCompletionCandidate{
			{Command: "--prompt", Summary: "图片提示词", Group: string(chatSlashCommandGroupContext), AcceptsArgs: true},
			{Command: "--provider", Summary: "指定图片生成 provider", Group: string(chatSlashCommandGroupContext), AcceptsArgs: true},
			{Command: "--model", Summary: "指定图像模型", Group: string(chatSlashCommandGroupContext), AcceptsArgs: true},
			{Command: "--path", Summary: "图片生成路径：auto|api|codex_native", Group: string(chatSlashCommandGroupContext), AcceptsArgs: true},
			{Command: "--n", Summary: "生成图片数量", Group: string(chatSlashCommandGroupContext), AcceptsArgs: true},
			{Command: "--size", Summary: "图片尺寸", Group: string(chatSlashCommandGroupContext), AcceptsArgs: true},
			{Command: "--quality", Summary: "图片质量", Group: string(chatSlashCommandGroupContext), AcceptsArgs: true},
			{Command: "--background", Summary: "背景模式", Group: string(chatSlashCommandGroupContext), AcceptsArgs: true},
			{Command: "--output-format", Summary: "图片输出格式", Group: string(chatSlashCommandGroupContext), AcceptsArgs: true},
			{Command: "--output-dir", Summary: "生成图片保存目录", Group: string(chatSlashCommandGroupContext), AcceptsArgs: true},
			{Command: "--json", Summary: "输出 JSON", Group: string(chatSlashCommandGroupContext)},
			{Command: "--debug", Summary: "输出调试信息", Group: string(chatSlashCommandGroupContext)},
		})
	case "/queue":
		return completeStaticSlashArgs(argsText, cursor, []chatSlashCompletionCandidate{
			{Command: "status", Summary: "查看当前状态", Group: string(chatSlashCommandGroupContext)},
			{Command: "clear", Summary: "清空排队输入", Group: string(chatSlashCommandGroupContext)},
		})
	case "/goal":
		return completeGoalSlashArgs(argsText, cursor)
	case "/usage":
		return completeStaticSlashArgs(argsText, cursor, []chatSlashCompletionCandidate{
			{Command: "cache", Summary: "会话缓存统计（默认视图）", Group: string(chatSlashCommandGroupSession)},
			{Command: "requests", Summary: "最近 N 条 LLM 请求明细", Group: string(chatSlashCommandGroupSession)},
			{Command: "trace", Summary: "按消息 id 追溯", Group: string(chatSlashCommandGroupSession)},
			{Command: "tools", Summary: "工具调用/失败/耗时表", Group: string(chatSlashCommandGroupSession)},
			{Command: "subagents", Summary: "子代理完成率/失败分类/重试/耗时", Group: string(chatSlashCommandGroupSession)},
			{Command: "errors", Summary: "失败模式 Top-N", Group: string(chatSlashCommandGroupSession)},
		})
	case "/debug":
		return completeStaticSlashArgs(argsText, cursor, []chatSlashCompletionCandidate{
			{Command: "on", Summary: "开启会话 debug 模式", Group: string(chatSlashCommandGroupSession)},
			{Command: "off", Summary: "关闭会话 debug 模式", Group: string(chatSlashCommandGroupSession)},
			{Command: "status", Summary: "查看会话 debug 模式状态", Group: string(chatSlashCommandGroupSession)},
			{Command: "display", Summary: "显示当前会话调试信息", Group: string(chatSlashCommandGroupSession)},
			{Command: "routing", Summary: "显示子 Agent / Team routing 配置摘要", Group: string(chatSlashCommandGroupSession)},
			{Command: "export", Summary: "打包调试文件", Group: string(chatSlashCommandGroupSession)},
			{Command: "zip", Summary: "打包调试文件", Group: string(chatSlashCommandGroupSession)},
			{Command: "--output", Summary: "指定 zip 输出文件", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
			{Command: "--dir", Summary: "指定 zip 输出目录", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
		})
	case "/supervision":
		// 2026-09-22 手动核查：/supervision 此前只注册了 catalog 与 handler，
		// 参数模式没有候选，输入 "/supervision " 后弹窗为空，子命令只能靠
		// /help 记忆。这里按 chat_supervision.go 的 parseChatSupervisionRequest
		// 与委托的 /debug supervision 用法逐位给出候选。
		return completeSupervisionSlashArgs(argsText, cursor)
	case "/agents":
		return completeAgentsSlashArgs(session, argsText, cursor)
	case "/agent":
		ctx := parseSlashArgumentContext(argsText, cursor)
		return matchSlashArgumentCandidates(agentTargetArgumentCandidates(session, false, true), activeSlashArgumentQuery(ctx))
	case "/function", "/describe", "/call", "/tool":
		return completeCatalogFunctionArgs(session, argsText, cursor, command)
	case "/skill", "/skills":
		return completeSkillArgs(session, argsText, cursor)
	case "/mcp":
		return completeStaticSlashArgs(argsText, cursor, []chatSlashCompletionCandidate{
			{Command: "list", Summary: "列出全部 MCP 与连接状态", Group: string(chatSlashCommandGroupFunctions)},
			{Command: "status", Summary: "查看单个 MCP 的配置与运行状态", Group: string(chatSlashCommandGroupFunctions), AcceptsArgs: true},
			{Command: "add", Summary: "新增 MCP（<name> <url> 或 --command <cmd>）", Group: string(chatSlashCommandGroupFunctions), AcceptsArgs: true},
			{Command: "enable", Summary: "启用并热重载", Group: string(chatSlashCommandGroupFunctions), AcceptsArgs: true},
			{Command: "disable", Summary: "停用并热重载", Group: string(chatSlashCommandGroupFunctions), AcceptsArgs: true},
			{Command: "remove", Summary: "删除并热重载", Group: string(chatSlashCommandGroupFunctions), AcceptsArgs: true},
			{Command: "reload", Summary: "重新加载配置并重连", Group: string(chatSlashCommandGroupFunctions)},
		})
	case "/web":
		return completeStaticSlashArgs(argsText, cursor, []chatSlashCompletionCandidate{
			{Command: "status", Summary: "显示 Web 服务器状态", Group: string(chatSlashCommandGroupWeb)},
			{Command: "token", Summary: "显示当前 Web 写令牌", Group: string(chatSlashCommandGroupWeb)},
			{Command: "endpoints", Summary: "显示全部 Web 调试端点清单", Group: string(chatSlashCommandGroupWeb)},
			{Command: "open", Summary: "在浏览器中打开 Web 客户端", Group: string(chatSlashCommandGroupWeb)},
		})
	case "/export":
		return p.completeExportArgs(session, argsText, cursor)
	case "/resume":
		return p.completeResumeArgs(session, argsText, cursor)
	case "/load":
		return p.completeLoadArgs(session, argsText, cursor)
	default:
		return nil
	}
}

func completeStaticSlashArgs(argsText string, cursor int, candidates []chatSlashCompletionCandidate) []chatSlashCompletionCandidate {
	ctx := parseSlashArgumentContext(argsText, cursor)
	return matchSlashArgumentCandidates(candidates, activeSlashArgumentQuery(ctx))
}

func completeGoalSlashArgs(argsText string, cursor int) []chatSlashCompletionCandidate {
	ctx := parseSlashArgumentContext(argsText, cursor)
	query := activeSlashArgumentQuery(ctx)
	candidates := matchSlashArgumentCandidates([]chatSlashCompletionCandidate{
		{Command: "status", Summary: "显示当前 goal", Group: string(chatSlashCommandGroupSession)},
		{Command: "clear", Summary: "清除当前 goal", Group: string(chatSlashCommandGroupSession)},
		{Command: "pause", Summary: "暂停当前 goal", Group: string(chatSlashCommandGroupSession)},
		{Command: "resume", Summary: "恢复当前 goal", Group: string(chatSlashCommandGroupSession)},
		{Command: "complete", Summary: "标记当前 goal 完成", Group: string(chatSlashCommandGroupSession)},
		{Command: "--json", Summary: "以 JSON 输出当前 goal", Group: string(chatSlashCommandGroupSession)},
	}, query)
	return append(candidates, chatSlashCompletionCandidate{
		Command:       "<objective>",
		Summary:       "直接输入目标文本以设置或替换当前 goal",
		Group:         string(chatSlashCommandGroupSession),
		Informational: true,
	})
}

func completeAgentsSlashArgs(session *ChatSession, argsText string, cursor int) []chatSlashCompletionCandidate {
	ctx := parseSlashArgumentContext(argsText, cursor)
	query := activeSlashArgumentQuery(ctx)
	first := slashArgumentTokenText(ctx, 0)

	if first == "" || slashArgumentCursorInToken(ctx, 0) {
		return matchSlashArgumentCandidates(agentTopLevelArgumentCandidates(), query)
	}

	switch strings.ToLower(first) {
	case "panel", "pane", "dashboard":
		return completeAgentsPanelSlashArgs(session, ctx, query)
	case "pick", "select":
		return nil
	case "target":
		return matchSlashArgumentCandidates(agentTargetArgumentCandidates(session, false, true), query)
	case "view", "open", "transcript":
		return matchSlashArgumentCandidates(agentTargetArgumentCandidates(session, false, true), query)
	case "approve", "allow", "deny", "reject", "answer":
		return completeAgentsApprovalTargetSlashArgs(session, ctx, query)
	case "send":
		return completeAgentsMessageTargetSlashArgs(session, ctx, query)
	case "followup", "task":
		return completeAgentsMessageTargetSlashArgs(session, ctx, query)
	case "routing", "route":
		return completeAgentsRoutingSlashArgs(session, ctx, query)
	case "cleanup", "prune", "gc":
		return completeAgentsCleanupSlashArgs(ctx, query)
	default:
		return matchSlashArgumentCandidates(agentTopLevelArgumentCandidates(), query)
	}
}

func completeAgentsCleanupSlashArgs(ctx slashArgumentContext, query string) []chatSlashCompletionCandidate {
	if slashArgumentTokenText(ctx, 1) == "" || slashArgumentCursorInToken(ctx, 1) {
		return matchSlashArgumentCandidates([]chatSlashCompletionCandidate{
			{Command: "--dry-run", Summary: "只预览可回收对象，不执行回收", Group: string(chatSlashCommandGroupSession)},
			{Command: "--idle", Summary: "额外回收空闲超过给定时长的子 agent", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
		}, query)
	}
	if strings.EqualFold(slashArgumentTokenText(ctx, 1), "--idle") {
		return matchSlashArgumentCandidates([]chatSlashCompletionCandidate{
			{Command: "30m", Summary: "回收空闲 30 分钟以上的子 agent", Group: string(chatSlashCommandGroupSession)},
			{Command: "1h", Summary: "回收空闲 1 小时以上的子 agent", Group: string(chatSlashCommandGroupSession)},
		}, query)
	}
	return nil
}

func completeAgentsPanelSlashArgs(session *ChatSession, ctx slashArgumentContext, query string) []chatSlashCompletionCandidate {
	second := slashArgumentTokenText(ctx, 1)
	if second == "" || slashArgumentCursorInToken(ctx, 1) {
		return matchSlashArgumentCandidates(agentPanelArgumentCandidates(), query)
	}
	if strings.EqualFold(second, "target") {
		return matchSlashArgumentCandidates(agentTargetArgumentCandidates(session, false, false), query)
	}
	return nil
}

func completeAgentsMessageTargetSlashArgs(session *ChatSession, ctx slashArgumentContext, query string) []chatSlashCompletionCandidate {
	if slashArgumentTokenText(ctx, 1) == "" || slashArgumentCursorInToken(ctx, 1) {
		candidates := agentTargetArgumentCandidates(session, true, false)
		matches := matchSlashArgumentCandidates(candidates, query)
		if len(matches) == 0 && strings.TrimSpace(sessionSelectedAgentTarget(session)) != "" {
			return nil
		}
		return matches
	}
	return nil
}

// completeAgentsApprovalTargetSlashArgs 补全 /agents approve|deny|answer 的 target
// 位（request_id / question_id 是运行期值，不做枚举）。
func completeAgentsApprovalTargetSlashArgs(session *ChatSession, ctx slashArgumentContext, query string) []chatSlashCompletionCandidate {
	if slashArgumentTokenText(ctx, 1) == "" || slashArgumentCursorInToken(ctx, 1) {
		return matchSlashArgumentCandidates(agentTargetArgumentCandidates(session, true, false), query)
	}
	return nil
}

func agentTopLevelArgumentCandidates() []chatSlashCompletionCandidate {
	return []chatSlashCompletionCandidate{
		{Command: "panel", Summary: "显示多 agent 富交互面板", Group: string(chatSlashCommandGroupSession)},
		{Command: "dashboard", Summary: "panel 的别名", Group: string(chatSlashCommandGroupSession)},
		{Command: "pick", Summary: "弹出 agent picker", Group: string(chatSlashCommandGroupSession)},
		{Command: "target", Summary: "设置或列出默认 agent 消息目标", Group: string(chatSlashCommandGroupSession)},
		{Command: "view", Summary: "只读查看子 agent transcript", Group: string(chatSlashCommandGroupSession)},
		{Command: "open", Summary: "view 的别名", Group: string(chatSlashCommandGroupSession)},
		{Command: "transcript", Summary: "view 的别名", Group: string(chatSlashCommandGroupSession)},
		{Command: "approve", Summary: "批准子 agent 待审批工具调用（复用 supervision 审批链）", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
		{Command: "deny", Summary: "拒绝子 agent 待审批工具调用", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
		{Command: "answer", Summary: "回答子 agent 的待回答问题", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
		{Command: "send", Summary: "向目标 agent 投递 mailbox 消息", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
		{Command: "followup", Summary: "向目标 agent 投递或触发 follow-up task", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
		{Command: "select", Summary: "pick 的别名", Group: string(chatSlashCommandGroupSession)},
		{Command: "task", Summary: "followup 的别名", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
		{Command: "routing", Summary: "预览子 Agent / Team difficulty route", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
		{Command: "cleanup", Summary: "回收可安全关闭的子 agent，释放配额", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
	}
}

func completeAgentsRoutingSlashArgs(session *ChatSession, ctx slashArgumentContext, query string) []chatSlashCompletionCandidate {
	second := slashArgumentTokenText(ctx, 1)
	if second == "" || slashArgumentCursorInToken(ctx, 1) {
		return matchSlashArgumentCandidates([]chatSlashCompletionCandidate{
			{Command: "test", Summary: "dry-run route decision", Group: string(chatSlashCommandGroupSession)},
			{Command: "dry-run", Summary: "test 的别名", Group: string(chatSlashCommandGroupSession)},
			{Command: "dryrun", Summary: "test 的别名", Group: string(chatSlashCommandGroupSession)},
			{Command: "preview", Summary: "test 的别名", Group: string(chatSlashCommandGroupSession)},
			{Command: "summary", Summary: "显示 routing 配置摘要", Group: string(chatSlashCommandGroupSession)},
			{Command: "status", Summary: "summary 的别名", Group: string(chatSlashCommandGroupSession)},
			{Command: "config", Summary: "summary 的别名", Group: string(chatSlashCommandGroupSession)},
		}, query)
	}
	if strings.EqualFold(second, "test") || strings.EqualFold(second, "dry-run") || strings.EqualFold(second, "dryrun") || strings.EqualFold(second, "preview") {
		valueQuery := slashAgentsRoutingArgumentQuery(ctx)
		switch slashAgentsRoutingArgumentFocus(ctx) {
		case "scope":
			return matchSlashArgumentCandidates([]chatSlashCompletionCandidate{
				{Command: "auto", Summary: "根据 workflow 自动选择", Group: string(chatSlashCommandGroupSession)},
				{Command: "subagent", Summary: "使用子 Agent 路由", Group: string(chatSlashCommandGroupSession)},
				{Command: "team", Summary: "使用 Team 有效路由", Group: string(chatSlashCommandGroupSession)},
			}, valueQuery)
		case "workflow":
			return matchSlashArgumentCandidates([]chatSlashCompletionCandidate{
				{Command: "spawn_team", Summary: "预览 spawn_team task route", Group: string(chatSlashCommandGroupSession)},
				{Command: "spawn_agent", Summary: "预览 spawn_agent/subagent route", Group: string(chatSlashCommandGroupSession)},
			}, valueQuery)
		case "difficulty":
			return matchSlashArgumentCandidates(agentRoutingDifficultyArgumentCandidates(), valueQuery)
		case "provider":
			return matchSlashArgumentCandidates(providerNameArgumentCandidates(session), valueQuery)
		case "model":
			return matchSlashArgumentCandidates(runtimeModelArgumentCandidates(session), valueQuery)
		case "reasoning":
			return matchSlashArgumentCandidates(reasoningEffortArgumentCandidates(session), valueQuery)
		}
		return matchSlashArgumentCandidates([]chatSlashCompletionCandidate{
			{Command: "--scope", Summary: "路由范围 auto|subagent|team", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
			{Command: "--workflow", Summary: "workflow hint", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
			{Command: "--team-id", Summary: "spawn_team team id", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
			{Command: "--teammate", Summary: "spawn_team teammate hint", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
			{Command: "--task", Summary: "spawn_team task id", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
			{Command: "--task-id", Summary: "spawn_team task id", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
			{Command: "--role", Summary: "子任务 role", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
			{Command: "--difficulty", Summary: "子任务难度", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
			{Command: "--goal", Summary: "子任务目标", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
			{Command: "--provider", Summary: "显式 provider override", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
			{Command: "--model", Summary: "显式 model override", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
			{Command: "--reasoning-effort", Summary: "显式 reasoning_effort", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
			{Command: "--read-only=true", Summary: "按只读任务预览", Group: string(chatSlashCommandGroupSession)},
			{Command: "--read-only=false", Summary: "按可写任务预览", Group: string(chatSlashCommandGroupSession)},
			{Command: "--write-path", Summary: "spawn_team 写路径 hint", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
			{Command: "--write", Summary: "按可写任务预览", Group: string(chatSlashCommandGroupSession)},
		}, query)
	}
	return nil
}

func slashAgentsRoutingArgumentFocus(ctx slashArgumentContext) string {
	current := strings.ToLower(strings.TrimSpace(ctx.Current.Text))
	previous := strings.ToLower(strings.TrimSpace(ctx.Previous.Text))
	switch {
	case strings.HasPrefix(current, "--scope="):
		return "scope"
	case strings.HasPrefix(current, "--workflow="):
		return "workflow"
	case strings.HasPrefix(current, "--difficulty="):
		return "difficulty"
	case strings.HasPrefix(current, "--provider="):
		return "provider"
	case strings.HasPrefix(current, "--model="):
		return "model"
	case strings.HasPrefix(current, "--reasoning-effort="):
		return "reasoning"
	case current == "--scope" || current == "--workflow" || current == "--difficulty" || current == "--provider" || current == "--model" || current == "--reasoning-effort":
		return "flags"
	}
	if ctx.CurrentOK && strings.HasPrefix(current, "-") {
		return "flags"
	}
	switch previous {
	case "--scope":
		return "scope"
	case "--workflow":
		return "workflow"
	case "--difficulty":
		return "difficulty"
	case "--provider":
		return "provider"
	case "--model":
		return "model"
	case "--reasoning-effort":
		return "reasoning"
	default:
		return "general"
	}
}

func slashAgentsRoutingArgumentQuery(ctx slashArgumentContext) string {
	current := strings.TrimSpace(ctx.Current.Text)
	switch slashAgentsRoutingArgumentFocus(ctx) {
	case "scope", "difficulty", "provider", "model", "reasoning":
		if value := slashArgumentAssignmentValue(current); value != "" || strings.Contains(current, "=") {
			return value
		}
		return activeSlashArgumentQuery(ctx)
	default:
		return activeSlashArgumentQuery(ctx)
	}
}

func agentRoutingDifficultyArgumentCandidates() []chatSlashCompletionCandidate {
	return []chatSlashCompletionCandidate{
		{Command: "easy", Summary: "低复杂度任务", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
		{Command: "normal", Summary: "常规任务", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
		{Command: "hard", Summary: "复杂任务", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
		{Command: "expert", Summary: "高风险或高不确定性任务", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
	}
}

func agentPanelArgumentCandidates() []chatSlashCompletionCandidate {
	return []chatSlashCompletionCandidate{
		{Command: "full", Summary: "显示 mailbox 与 timeline 完整详情", Group: string(chatSlashCommandGroupSession)},
		{Command: "follow", Summary: "进入面板跟随模式或等待 mailbox 更新", Group: string(chatSlashCommandGroupSession)},
		{Command: "target", Summary: "切换 panel 到指定 agent target", Group: string(chatSlashCommandGroupSession)},
		{Command: "next", Summary: "切换 panel 到下一个 agent target", Group: string(chatSlashCommandGroupSession)},
		{Command: "prev", Summary: "切换 panel 到上一个 agent target", Group: string(chatSlashCommandGroupSession)},
		{Command: "close", Summary: "关闭当前固定面板", Group: string(chatSlashCommandGroupSession)},
		{Command: "watch", Summary: "follow 的别名", Group: string(chatSlashCommandGroupSession)},
		{Command: "previous", Summary: "prev 的别名", Group: string(chatSlashCommandGroupSession)},
	}
}

func agentTargetArgumentCandidates(session *ChatSession, acceptsArgs bool, includeClear bool) []chatSlashCompletionCandidate {
	candidates := make([]chatSlashCompletionCandidate, 0, 8)
	if includeClear {
		candidates = append(candidates,
			chatSlashCompletionCandidate{Command: "clear", Summary: "清空默认 agent target", Group: string(chatSlashCommandGroupSession)},
			chatSlashCompletionCandidate{Command: "none", Summary: "清空默认 agent target", Group: string(chatSlashCommandGroupSession)},
		)
	}
	agents, err := chatAgentPickerItems(session)
	if err != nil {
		return candidates
	}
	for _, agent := range agents {
		target := firstNonEmptyChatValue(agent.Path, agent.SessionID, agent.ID)
		if strings.TrimSpace(target) == "" {
			continue
		}
		summary := "agent target"
		if status := strings.TrimSpace(agent.Status); status != "" {
			summary = "status=" + status
		}
		if sessionID := firstNonEmptyChatValue(agent.SessionID, agent.ID); strings.TrimSpace(sessionID) != "" {
			summary += " session=" + sessionID
		}
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command:     target,
			Summary:     summary,
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: acceptsArgs,
		})
	}
	return candidates
}

func slashArgumentTokenText(ctx slashArgumentContext, index int) string {
	if index < 0 || index >= len(ctx.Tokens) {
		return ""
	}
	return strings.TrimSpace(ctx.Tokens[index].Text)
}

func slashArgumentCursorInToken(ctx slashArgumentContext, index int) bool {
	if !ctx.CurrentOK || index < 0 || index >= len(ctx.Tokens) {
		return false
	}
	token := ctx.Tokens[index]
	return ctx.Current.Start == token.Start && ctx.Current.End == token.End
}

func sessionSelectedAgentTarget(session *ChatSession) string {
	if session == nil {
		return ""
	}
	return strings.TrimSpace(chatSessionSelectedAgentTarget(session))
}

func completeModelSlashArgs(session *ChatSession, argsText string, cursor int) []chatSlashCompletionCandidate {
	ctx := parseSlashArgumentContext(argsText, cursor)
	query := slashModelArgumentQuery(ctx)
	staticCandidates := []chatSlashCompletionCandidate{
		{Command: "--provider", Summary: "选择 provider", Group: string(chatSlashCommandGroupModel), AcceptsArgs: true},
		{Command: "--model", Summary: "选择 model", Group: string(chatSlashCommandGroupModel), AcceptsArgs: true},
		{Command: "--reasoning-effort", Summary: "设置 reasoning_effort", Group: string(chatSlashCommandGroupModel), AcceptsArgs: true},
		{Command: "status", Summary: "查看当前模型状态", Group: string(chatSlashCommandGroupModel)},
		{Command: "clear-reasoning", Summary: "清空 reasoning_effort", Group: string(chatSlashCommandGroupModel)},
	}

	switch slashModelArgumentFocus(ctx) {
	case "provider":
		return matchSlashArgumentCandidates(providerNameArgumentCandidates(session), query)
	case "model":
		return matchSlashArgumentCandidates(runtimeModelArgumentCandidates(session), query)
	case "reasoning":
		return matchSlashArgumentCandidates(reasoningEffortArgumentCandidates(session), query)
	case "flags":
		return matchSlashArgumentCandidates(staticCandidates, query)
	default:
		candidates := make([]chatSlashCompletionCandidate, 0, len(staticCandidates)+16)
		candidates = append(candidates, staticCandidates...)
		candidates = append(candidates, providerNameArgumentCandidates(session)...)
		candidates = append(candidates, runtimeModelArgumentCandidates(session)...)
		candidates = append(candidates, reasoningEffortArgumentCandidates(session)...)
		return matchSlashArgumentCandidates(dedupeSlashArgumentCandidates(candidates), query)
	}
}

func completeReasoningEffortSlashArgs(session *ChatSession, argsText string, cursor int) []chatSlashCompletionCandidate {
	staticCandidates := []chatSlashCompletionCandidate{
		{Command: "status", Summary: "查看当前 reasoning_effort", Group: string(chatSlashCommandGroupModel)},
		{Command: "select", Summary: "交互选择 reasoning_effort", Group: string(chatSlashCommandGroupModel)},
		{Command: "clear", Summary: "清空 reasoning_effort", Group: string(chatSlashCommandGroupModel)},
	}
	candidates := make([]chatSlashCompletionCandidate, 0, len(staticCandidates)+8)
	candidates = append(candidates, staticCandidates...)
	candidates = append(candidates, reasoningEffortArgumentCandidates(session)...)
	return completeStaticSlashArgs(argsText, cursor, dedupeSlashArgumentCandidates(candidates))
}

func completeLoginSlashArgs(session *ChatSession, argsText string, cursor int) []chatSlashCompletionCandidate {
	ctx := parseSlashArgumentContext(argsText, cursor)
	query := slashLoginArgumentQuery(ctx)
	staticCandidates := []chatSlashCompletionCandidate{
		{Command: "--provider", Summary: "provider 名称", Group: string(chatSlashCommandGroupModel), AcceptsArgs: true},
		{Command: "--protocol", Summary: "登录协议", Group: string(chatSlashCommandGroupModel), AcceptsArgs: true},
		{Command: "--mode", Summary: "认证模式", Group: string(chatSlashCommandGroupModel), AcceptsArgs: true},
		{Command: "--base-url", Summary: "provider base URL", Group: string(chatSlashCommandGroupModel), AcceptsArgs: true},
		{Command: "--api-key", Summary: "API key", Group: string(chatSlashCommandGroupModel), AcceptsArgs: true},
		{Command: "--models-path", Summary: "models endpoint", Group: string(chatSlashCommandGroupModel), AcceptsArgs: true},
		{Command: "--default-model", Summary: "默认模型", Group: string(chatSlashCommandGroupModel), AcceptsArgs: true},
		{Command: "--model-cards", Summary: "额外模型卡片 catalog 文件", Group: string(chatSlashCommandGroupModel), AcceptsArgs: true},
		{Command: "--set-default", Summary: "设为默认 provider", Group: string(chatSlashCommandGroupModel)},
		{Command: "--dry-run", Summary: "只校验不写配置", Group: string(chatSlashCommandGroupModel)},
		{Command: "--switch", Summary: "登录后切换当前会话", Group: string(chatSlashCommandGroupModel)},
		{Command: "--no-model-cards", Summary: "禁用模型卡片补齐", Group: string(chatSlashCommandGroupModel)},
		{Command: "--model-cards-strict", Summary: "模型卡片错误时中止登录", Group: string(chatSlashCommandGroupModel)},
	}
	switch slashLoginArgumentFocus(ctx) {
	case "provider":
		return matchSlashArgumentCandidates(providerNameArgumentCandidates(session), query)
	case "protocol":
		return matchSlashArgumentCandidates(loginProtocolArgumentCandidates(), query)
	case "mode":
		return matchSlashArgumentCandidates([]chatSlashCompletionCandidate{
			{Command: "apikey", Summary: "API key", Group: string(chatSlashCommandGroupModel)},
			{Command: "oauth", Summary: "OAuth", Group: string(chatSlashCommandGroupModel)},
		}, query)
	case "flags":
		return matchSlashArgumentCandidates(staticCandidates, query)
	default:
		candidates := make([]chatSlashCompletionCandidate, 0, len(staticCandidates)+16)
		candidates = append(candidates, staticCandidates...)
		candidates = append(candidates, providerNameArgumentCandidates(session)...)
		candidates = append(candidates, loginProtocolArgumentCandidates()...)
		return matchSlashArgumentCandidates(dedupeSlashArgumentCandidates(candidates), query)
	}
}

func completeCatalogFunctionArgs(session *ChatSession, argsText string, cursor int, command string) []chatSlashCompletionCandidate {
	ctx := parseSlashArgumentContext(argsText, cursor)
	candidates := catalogFunctionArgumentCandidates(session, command)
	return matchSlashArgumentCandidates(candidates, activeSlashArgumentQuery(ctx))
}

func completeSkillArgs(session *ChatSession, argsText string, cursor int) []chatSlashCompletionCandidate {
	ctx := parseSlashArgumentContext(argsText, cursor)
	candidates := skillArgumentCandidates(session)
	return matchSlashArgumentCandidates(candidates, activeSlashArgumentQuery(ctx))
}

func (p *chatSlashArgumentCompletionProvider) completeResumeArgs(session *ChatSession, argsText string, cursor int) []chatSlashCompletionCandidate {
	ctx := parseSlashArgumentContext(argsText, cursor)
	candidates := p.resumeArgumentCandidates(session)
	return matchSlashArgumentCandidates(candidates, activeSlashArgumentQuery(ctx))
}

func (p *chatSlashArgumentCompletionProvider) completeLoadArgs(session *ChatSession, argsText string, cursor int) []chatSlashCompletionCandidate {
	ctx := parseSlashArgumentContext(argsText, cursor)
	candidates := p.loadArgumentCandidates(session)
	return matchSlashArgumentCandidates(candidates, activeSlashArgumentQuery(ctx))
}

func (p *chatSlashArgumentCompletionProvider) completeExportArgs(session *ChatSession, argsText string, cursor int) []chatSlashCompletionCandidate {
	ctx := parseSlashArgumentContext(argsText, cursor)
	candidates := []chatSlashCompletionCandidate{
		{Command: "current", Summary: "导出当前会话", Group: string(chatSlashCommandGroupSession)},
		{Command: "--full", Summary: "完整 JSON", Group: string(chatSlashCommandGroupSession)},
		{Command: "--body", Summary: "正文 Markdown", Group: string(chatSlashCommandGroupSession)},
		{Command: "--output", Summary: "指定输出文件", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
		{Command: "--dir", Summary: "指定输出目录", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
	}
	candidates = append(candidates, p.resumeArgumentCandidates(session)...)
	return matchSlashArgumentCandidates(dedupeSlashArgumentCandidates(candidates), activeSlashArgumentQuery(ctx))
}

func activeSlashArgumentQuery(ctx slashArgumentContext) string {
	query := strings.TrimSpace(ctx.Query)
	if query == "" && ctx.CurrentOK {
		query = strings.TrimSpace(ctx.Current.Text)
	}
	if query == "" {
		return ""
	}
	if value := slashArgumentAssignmentValue(query); value != "" || strings.Contains(query, "=") {
		return value
	}
	return query
}

func parseSlashArgumentContext(argsText string, cursor int) slashArgumentContext {
	runes := []rune(argsText)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}

	ctx := slashArgumentContext{
		Tokens: make([]slashArgumentToken, 0, 8),
		Cursor: cursor,
	}
	for i := 0; i < len(runes); {
		for i < len(runes) && unicode.IsSpace(runes[i]) {
			i++
		}
		if i >= len(runes) {
			break
		}
		start := i
		for i < len(runes) && !unicode.IsSpace(runes[i]) {
			i++
		}
		end := i
		ctx.Tokens = append(ctx.Tokens, slashArgumentToken{
			Text:  string(runes[start:end]),
			Start: start,
			End:   end,
		})
	}

	for i, token := range ctx.Tokens {
		if cursor >= token.Start && cursor <= token.End {
			ctx.Current = token
			ctx.CurrentOK = true
			ctx.Query = string(runes[token.Start:cursor])
			if i > 0 {
				ctx.Previous = ctx.Tokens[i-1]
			}
			return ctx
		}
		if cursor < token.Start {
			if i > 0 {
				ctx.Previous = ctx.Tokens[i-1]
			}
			return ctx
		}
	}

	if len(ctx.Tokens) > 0 {
		ctx.Previous = ctx.Tokens[len(ctx.Tokens)-1]
	}
	return ctx
}

func slashArgumentCompletionRange(ctx slashArgumentContext) (int, int) {
	start := ctx.Cursor
	end := ctx.Cursor
	if !ctx.CurrentOK {
		return start, end
	}

	start = ctx.Current.Start
	end = ctx.Current.End
	if token := strings.TrimSpace(ctx.Current.Text); token != "" {
		if eqIndex := strings.Index(token, "="); eqIndex >= 0 {
			cursorWithinToken := ctx.Cursor - ctx.Current.Start
			if cursorWithinToken >= eqIndex+1 {
				start = ctx.Current.Start + eqIndex + 1
			}
		}
	}
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	return start, end
}

func slashModelArgumentFocus(ctx slashArgumentContext) string {
	current := strings.TrimSpace(ctx.Current.Text)
	previous := strings.TrimSpace(ctx.Previous.Text)

	switch {
	case strings.HasPrefix(current, "--provider=") || strings.HasPrefix(current, "-p="):
		return "provider"
	case strings.HasPrefix(current, "--model=") || strings.HasPrefix(current, "-m="):
		return "model"
	case strings.HasPrefix(current, "--reasoning-effort=") || strings.HasPrefix(current, "-r="):
		return "reasoning"
	case current == "--provider" || current == "-p":
		return "flags"
	case current == "--model" || current == "-m":
		return "flags"
	case current == "--reasoning-effort" || current == "-r":
		return "flags"
	}

	switch previous {
	case "--provider", "-p":
		return "provider"
	case "--model", "-m":
		return "model"
	case "--reasoning-effort", "-r":
		return "reasoning"
	}

	return "general"
}

func slashLoginArgumentFocus(ctx slashArgumentContext) string {
	current := strings.TrimSpace(ctx.Current.Text)
	previous := strings.TrimSpace(ctx.Previous.Text)
	switch {
	case strings.HasPrefix(current, "--provider=") || strings.HasPrefix(current, "-p="):
		return "provider"
	case strings.HasPrefix(current, "--protocol="):
		return "protocol"
	case strings.HasPrefix(current, "--mode="):
		return "mode"
	case current == "--provider" || current == "-p" || current == "--protocol" || current == "--mode":
		return "flags"
	}
	switch previous {
	case "--provider", "-p":
		return "provider"
	case "--protocol":
		return "protocol"
	case "--mode":
		return "mode"
	}
	return "general"
}

func slashModelArgumentQuery(ctx slashArgumentContext) string {
	current := strings.TrimSpace(ctx.Current.Text)
	switch slashModelArgumentFocus(ctx) {
	case "provider", "model", "reasoning":
		if value := slashArgumentAssignmentValue(current); value != "" || strings.Contains(current, "=") {
			return value
		}
		return activeSlashArgumentQuery(ctx)
	default:
		return activeSlashArgumentQuery(ctx)
	}
}

func slashLoginArgumentQuery(ctx slashArgumentContext) string {
	current := strings.TrimSpace(ctx.Current.Text)
	switch slashLoginArgumentFocus(ctx) {
	case "provider", "protocol", "mode":
		if value := slashArgumentAssignmentValue(current); value != "" || strings.Contains(current, "=") {
			return value
		}
		return activeSlashArgumentQuery(ctx)
	default:
		return activeSlashArgumentQuery(ctx)
	}
}

func slashArgumentAssignmentValue(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if idx := strings.Index(token, "="); idx >= 0 {
		return strings.TrimSpace(token[idx+1:])
	}
	return ""
}

func providerNameArgumentCandidates(session *ChatSession) []chatSlashCompletionCandidate {
	var names []string
	if session != nil && session.Config != nil {
		names = listEnabledProviderNames(session.Config)
	}
	if len(names) == 0 && session != nil && strings.TrimSpace(session.ProviderName) != "" {
		names = []string{strings.TrimSpace(session.ProviderName)}
	}

	candidates := make([]chatSlashCompletionCandidate, 0, len(names))
	for _, name := range names {
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command:     name,
			Summary:     "provider",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: true,
		})
	}
	return candidates
}

func loginProtocolArgumentCandidates() []chatSlashCompletionCandidate {
	values := loginProtocolOptions()
	candidates := make([]chatSlashCompletionCandidate, 0, len(values))
	for _, value := range values {
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command: value,
			Summary: "login protocol",
			Group:   string(chatSlashCommandGroupModel),
		})
	}
	return candidates
}

func runtimeModelArgumentCandidates(session *ChatSession) []chatSlashCompletionCandidate {
	if session == nil {
		return nil
	}
	names := runtimeModelSelectionOptions(session)
	candidates := make([]chatSlashCompletionCandidate, 0, len(names))
	for _, name := range names {
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command:     name,
			Summary:     "model",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: true,
		})
	}
	return candidates
}

func reasoningEffortArgumentCandidates(session *ChatSession) []chatSlashCompletionCandidate {
	if session == nil {
		return nil
	}
	catalog := reasoningEffortCatalogForModel(session.Provider, effectiveRuntimeModel(session))
	if len(catalog.options) == 0 {
		return nil
	}
	candidates := make([]chatSlashCompletionCandidate, 0, len(catalog.options))
	for _, option := range catalog.options {
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command:     option,
			Summary:     "reasoning_effort",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: false,
		})
	}
	return candidates
}

func catalogFunctionArgumentCandidates(session *ChatSession, command string) []chatSlashCompletionCandidate {
	catalog := ensureFunctionCatalog(session)
	if catalog == nil {
		return nil
	}
	descriptors := catalog.Descriptors()
	if len(descriptors) == 0 {
		return nil
	}

	type descriptorCandidate struct {
		name      string
		candidate chatSlashCompletionCandidate
	}

	candidates := make([]descriptorCandidate, 0, len(descriptors))
	seen := make(map[string]struct{}, len(descriptors))
	for _, desc := range descriptors {
		if desc == nil || strings.TrimSpace(desc.Name) == "" {
			continue
		}
		name := strings.TrimSpace(desc.Name)
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}

		summary := strings.TrimSpace(desc.Description)
		if summary == "" {
			summary = strings.TrimSpace(string(desc.Kind))
		}
		if summary == "" {
			summary = "function"
		}
		candidates = append(candidates, descriptorCandidate{
			name: name,
			candidate: chatSlashCompletionCandidate{
				Command:     name,
				Summary:     summary,
				Group:       string(chatSlashCommandGroupFunctions),
				AcceptsArgs: true,
			},
		})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		left := strings.ToLower(candidates[i].name)
		right := strings.ToLower(candidates[j].name)
		if left == right {
			return candidates[i].name < candidates[j].name
		}
		return left < right
	})

	out := make([]chatSlashCompletionCandidate, 0, len(candidates))
	for _, item := range candidates {
		out = append(out, item.candidate)
	}
	return out
}

func skillArgumentCandidates(session *ChatSession) []chatSlashCompletionCandidate {
	catalog := ensureFunctionCatalog(session)
	if catalog == nil {
		return nil
	}
	names := catalog.SkillFunctionNames()
	if len(names) == 0 && session != nil && session.SkillsBinding != nil {
		names = session.SkillsBinding.orderedSkillFunctionNames()
	}
	if len(names) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(names))
	candidates := make([]chatSlashCompletionCandidate, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}

		summary := ""
		if desc := catalog.Descriptor(name); desc != nil {
			summary = strings.TrimSpace(desc.Description)
		}
		if summary == "" {
			summary = "skill"
		}
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command:     name,
			Summary:     summary,
			Group:       string(chatSlashCommandGroupFunctions),
			AcceptsArgs: true,
		})
	}
	return candidates
}

func (p *chatSlashArgumentCompletionProvider) resumeArgumentCandidates(session *ChatSession) []chatSlashCompletionCandidate {
	return p.cachedSessionArgumentCandidates(session, true)
}

func (p *chatSlashArgumentCompletionProvider) loadArgumentCandidates(session *ChatSession) []chatSlashCompletionCandidate {
	return p.cachedSessionArgumentCandidates(session, false)
}

func (p *chatSlashArgumentCompletionProvider) cachedSessionArgumentCandidates(session *ChatSession, includeLatest bool) []chatSlashCompletionCandidate {
	if session == nil || session.SessionManager == nil || strings.TrimSpace(session.SessionUserID) == "" {
		return nil
	}

	currentID := currentRuntimeSessionID(session)
	key := slashSessionCacheKey{
		manager:   session.SessionManager,
		userID:    strings.TrimSpace(session.SessionUserID),
		currentID: strings.TrimSpace(currentID),
	}

	p.mu.Lock()
	cache := p.loadCache
	if includeLatest {
		cache = p.resumeCache
	}
	if cached, ok := cache[key]; ok {
		out := cloneSlashCompletionCandidates(cached)
		p.mu.Unlock()
		return out
	}
	p.mu.Unlock()

	sessions, err := session.SessionManager.ListMetadataPage(context.Background(), session.SessionUserID, 100, 0)
	if err != nil {
		return nil
	}

	candidates := make([]chatSlashCompletionCandidate, 0, len(sessions)+2)
	if includeLatest {
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command:     "latest",
			Summary:     "直接恢复最近的其他会话",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: false,
		})
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command:     "--cwd",
			Summary:     "显式仅显示并恢复当前工作目录的会话（默认行为）",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: false,
		})
	}
	now := time.Now()
	for _, item := range sessions {
		if item == nil || strings.TrimSpace(item.ID) == "" {
			continue
		}
		if currentID != "" && strings.EqualFold(strings.TrimSpace(item.ID), strings.TrimSpace(currentID)) {
			continue
		}
		if includeLatest {
			loaded, loadErr := session.SessionManager.Get(context.Background(), item.ID)
			if loadErr != nil || !runtimeSessionHasConversation(loaded) {
				continue
			}
			item = loaded
		}
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command:     item.ID,
			Summary:     formatResumeSessionCompletionSummary(item, now),
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: false,
		})
	}

	p.mu.Lock()
	if includeLatest {
		p.resumeCache[key] = cloneSlashCompletionCandidates(candidates)
	} else {
		p.loadCache[key] = cloneSlashCompletionCandidates(candidates)
	}
	p.mu.Unlock()

	return candidates
}

// formatResumeSessionCompletionSummary builds the /resume and /load argument
// completion summary: title first, optional compact badge, then counts/time.
func formatResumeSessionCompletionSummary(session *runtimechat.Session, now time.Time) string {
	if session == nil {
		return ""
	}
	turnCount, messageCount := runtimeSessionConversationCounts(session)
	title := runtimeResumeSessionTitle(session)
	// Keep title first; surface compact generation when present so completion
	// rows match the interactive list badge without duplicating sticky titles.
	if generation := runtimeSessionCompactGeneration(session); generation > 0 {
		badge := fmt.Sprintf("compact #%d", generation)
		if !strings.Contains(strings.ToLower(title), strings.ToLower(badge)) {
			title = fmt.Sprintf("%s · %s", title, badge)
		}
	}
	return fmt.Sprintf("%s · %d轮/%d条消息 · 最后更新 %s",
		title,
		turnCount,
		messageCount,
		formatSessionUpdatedAt(session.UpdatedAt, now),
	)
}

// completeSupervisionSlashArgs 补全 /supervision 的子命令、开关与枚举取值。
func completeSupervisionSlashArgs(argsText string, cursor int) []chatSlashCompletionCandidate {
	ctx := parseSlashArgumentContext(argsText, cursor)
	first := strings.ToLower(slashArgumentTokenText(ctx, 0))
	if first == "" || slashArgumentCursorInToken(ctx, 0) {
		return matchSlashArgumentCandidates(supervisionTopLevelArgumentCandidates(), activeSlashArgumentQuery(ctx))
	}

	switch first {
	case "status", "audit":
		return completeSupervisionFlagArguments(ctx, supervisionScopeFlagCandidates())
	case "wake":
		flags := append(supervisionWakeFlagCandidates(), supervisionScopeFlagCandidates()...)
		return completeSupervisionFlagArguments(ctx, flags)
	case "list":
		return completeSupervisionFlagArguments(ctx, supervisionListFlagCandidates())
	case "ack":
		flags := append(supervisionNotificationIDCandidates(), supervisionAckFlagCandidates()...)
		return completeSupervisionFlagArguments(ctx, flags)
	case "defer":
		flags := append(supervisionNotificationIDCandidates(), supervisionDeferFlagCandidates()...)
		return completeSupervisionFlagArguments(ctx, flags)
	case "resolve":
		flags := append(supervisionNotificationIDCandidates(), supervisionResolveFlagCandidates()...)
		return completeSupervisionFlagArguments(ctx, flags)
	case "control":
		flags := append(supervisionNotificationIDCandidates(), supervisionControlFlagCandidates()...)
		return completeSupervisionFlagArguments(ctx, flags)
	case "watchdog", "supervisor", "execution-supervisor":
		// 委托 /debug supervision watchdog：只读，不接受额外参数。
		return nil
	default:
		return matchSlashArgumentCandidates(supervisionTopLevelArgumentCandidates(), activeSlashArgumentQuery(ctx))
	}
}

// completeSupervisionFlagArguments 在 flag 位给出开关候选，在取值位给出枚举值；
// 自由文本/数值取值位返回 nil 关闭弹窗（与 /agents cleanup --idle 的既有行为一致）。
func completeSupervisionFlagArguments(ctx slashArgumentContext, flags []chatSlashCompletionCandidate) []chatSlashCompletionCandidate {
	if flag, valueQuery := supervisionFlagValueFocus(ctx); flag != "" {
		if values, known := supervisionArgumentValueCandidates(flag, valueQuery); known {
			return values
		}
	}
	return matchSlashArgumentCandidates(flags, activeSlashArgumentQuery(ctx))
}

// supervisionFlagValueFocus 判断光标是否落在某个 flag 的取值位（"--state " 之后
// 或 "--state=cl" 之中），返回 flag 名与该取值的前缀。
func supervisionFlagValueFocus(ctx slashArgumentContext) (string, string) {
	current := strings.ToLower(strings.TrimSpace(ctx.Current.Text))
	previous := strings.ToLower(strings.TrimSpace(ctx.Previous.Text))
	if strings.HasPrefix(current, "--") {
		if idx := strings.Index(current, "="); idx > 0 {
			return current[:idx], slashArgumentAssignmentValue(current)
		}
	}
	if strings.HasPrefix(previous, "--") && !strings.HasPrefix(current, "--") {
		if current == "" {
			return previous, activeSlashArgumentQuery(ctx)
		}
		return previous, current
	}
	return "", ""
}

// supervisionArgumentValueCandidates 返回 flag 的取值候选。known=false 表示该
// flag 不在 /supervision 的语义内（继续按开关名匹配）。
func supervisionArgumentValueCandidates(flag, query string) ([]chatSlashCompletionCandidate, bool) {
	group := string(chatSlashCommandGroupSession)
	switch flag {
	case "--state":
		return matchSlashArgumentCandidates([]chatSlashCompletionCandidate{
			{Command: "closed", Summary: "通知已关闭", Group: group},
			{Command: "recovered", Summary: "标的已恢复", Group: group},
			{Command: "failed", Summary: "标的已失败收敛", Group: group},
		}, query), true
	case "--action":
		return matchSlashArgumentCandidates([]chatSlashCompletionCandidate{
			{Command: "cancel", Summary: "取消标的", Group: group},
			{Command: "close", Summary: "关闭标的", Group: group},
			{Command: "cancel_subtree", Summary: "取消标的下整棵子树", Group: group},
			{Command: "retry", Summary: "重试标的", Group: group},
			{Command: "reassign", Summary: "重新指派标的", Group: group},
		}, query), true
	case "--cascade":
		return matchSlashArgumentCandidates([]chatSlashCompletionCandidate{
			{Command: "target", Summary: "只作用于标的本身", Group: group},
			{Command: "descendants", Summary: "作用于标的下所有后代", Group: group},
		}, query), true
	case "--until":
		return matchSlashArgumentCandidates([]chatSlashCompletionCandidate{
			{Command: "30m", Summary: "延后 30 分钟", Group: group},
			{Command: "2h", Summary: "延后 2 小时", Group: group},
			{Command: "<RFC3339>", Summary: "绝对时间，如 2026-09-22T15:00:00+08:00", Group: group, Informational: true},
		}, query), true
	case "--note", "--reason", "--team", "--limit", "--expected-version":
		// 自由文本/数值取值：不做枚举补全，弹窗关闭。
		return nil, true
	default:
		return nil, false
	}
}

func supervisionTopLevelArgumentCandidates() []chatSlashCompletionCandidate {
	group := string(chatSlashCommandGroupSession)
	return []chatSlashCompletionCandidate{
		{Command: "status", Summary: "只读：自动核查开关（turn_end_check）与 digest 计数", Group: group},
		{Command: "audit", Summary: "只读：digest 明细 + descendants 矩阵 + pending wake", Group: group},
		{Command: "wake", Summary: "查看 durable wake 与预算；--deliver 显式投递", Group: group, AcceptsArgs: true},
		{Command: "list", Summary: "列出监督通知与版本号（委托 /debug supervision list）", Group: group, AcceptsArgs: true},
		{Command: "ack", Summary: "确认通知，需 --note（委托 /debug supervision ack）", Group: group, AcceptsArgs: true},
		{Command: "defer", Summary: "延后通知，需 --until（委托 /debug supervision defer）", Group: group, AcceptsArgs: true},
		{Command: "resolve", Summary: "收敛 resolution 状态，需 --state（委托 /debug supervision resolve）", Group: group, AcceptsArgs: true},
		{Command: "control", Summary: "执行 durable 控制动作，需 --action/--reason（委托 /debug supervision control）", Group: group, AcceptsArgs: true},
		{Command: "watchdog", Summary: "打印本地执行看门狗状态（委托 /debug supervision watchdog）", Group: group},
		{Command: "help", Summary: "显示 /supervision 用法", Group: group},
	}
}

func supervisionNotificationIDCandidates() []chatSlashCompletionCandidate {
	return []chatSlashCompletionCandidate{
		{
			Command:       "<notification_id>",
			Summary:       "从 /supervision list 复制的通知 id（运行期取值，不做枚举）",
			Group:         string(chatSlashCommandGroupSession),
			Informational: true,
		},
	}
}

func supervisionScopeFlagCandidates() []chatSlashCompletionCandidate {
	group := string(chatSlashCommandGroupSession)
	return []chatSlashCompletionCandidate{
		{Command: "--team", Summary: "指定 team id（缺省沿用当前 scope）", Group: group, AcceptsArgs: true},
		{Command: "--limit", Summary: "限制 digest / descendants 矩阵行数", Group: group, AcceptsArgs: true},
		{Command: "--json", Summary: "以 JSON 输出核查结果", Group: group},
	}
}

func supervisionWakeFlagCandidates() []chatSlashCompletionCandidate {
	group := string(chatSlashCommandGroupSession)
	return []chatSlashCompletionCandidate{
		{Command: "--deliver", Summary: "真正投递一次 durable wake（父会话忙或预算耗尽时保持 pending）", Group: group},
		{Command: "--dry-run", Summary: "只预览不投递（默认行为，显式写出便于脚本自解释）", Group: group},
	}
}

func supervisionListFlagCandidates() []chatSlashCompletionCandidate {
	group := string(chatSlashCommandGroupSession)
	return []chatSlashCompletionCandidate{
		{Command: "--all", Summary: "包含已收敛/已过期的通知", Group: group},
		{Command: "--limit", Summary: "限制返回行数", Group: group, AcceptsArgs: true},
		{Command: "--team", Summary: "指定 team id（缺省沿用当前 scope）", Group: group, AcceptsArgs: true},
	}
}

func supervisionAckFlagCandidates() []chatSlashCompletionCandidate {
	group := string(chatSlashCommandGroupSession)
	return []chatSlashCompletionCandidate{
		{Command: "--note", Summary: "审计说明（ack 必填）", Group: group, AcceptsArgs: true},
		{Command: "--expected-version", Summary: "CAS 版本校验（可选）", Group: group, AcceptsArgs: true},
		{Command: "--team", Summary: "指定 team id（缺省沿用当前 scope）", Group: group, AcceptsArgs: true},
	}
}

func supervisionDeferFlagCandidates() []chatSlashCompletionCandidate {
	group := string(chatSlashCommandGroupSession)
	return []chatSlashCompletionCandidate{
		{Command: "--until", Summary: "延后到 30m / 2h / RFC3339 时间", Group: group, AcceptsArgs: true},
		{Command: "--reason", Summary: "延后原因（审计）", Group: group, AcceptsArgs: true},
		{Command: "--expected-version", Summary: "CAS 版本校验（可选）", Group: group, AcceptsArgs: true},
		{Command: "--team", Summary: "指定 team id（缺省沿用当前 scope）", Group: group, AcceptsArgs: true},
	}
}

func supervisionResolveFlagCandidates() []chatSlashCompletionCandidate {
	group := string(chatSlashCommandGroupSession)
	return []chatSlashCompletionCandidate{
		{Command: "--state", Summary: "收敛状态 closed|recovered|failed", Group: group, AcceptsArgs: true},
		{Command: "--expected-version", Summary: "CAS 版本校验（可选）", Group: group, AcceptsArgs: true},
		{Command: "--team", Summary: "指定 team id（缺省沿用当前 scope）", Group: group, AcceptsArgs: true},
	}
}

func supervisionControlFlagCandidates() []chatSlashCompletionCandidate {
	group := string(chatSlashCommandGroupSession)
	return []chatSlashCompletionCandidate{
		{Command: "--action", Summary: "控制动作 cancel|close|cancel_subtree|retry|reassign", Group: group, AcceptsArgs: true},
		{Command: "--reason", Summary: "控制动作原因（审计必填）", Group: group, AcceptsArgs: true},
		{Command: "--cascade", Summary: "作用范围 target|descendants", Group: group, AcceptsArgs: true},
		{Command: "--expected-version", Summary: "CAS 版本校验（可选）", Group: group, AcceptsArgs: true},
		{Command: "--team", Summary: "指定 team id（缺省沿用当前 scope）", Group: group, AcceptsArgs: true},
	}
}

func matchSlashArgumentCandidates(candidates []chatSlashCompletionCandidate, query string) []chatSlashCompletionCandidate {
	if len(candidates) == 0 {
		return nil
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return cloneSlashCompletionCandidates(candidates)
	}

	// Prefer ID prefix matches, then fall back to title/summary substring matches
	// so /resume and /load can complete by human-readable session titles.
	exactOrPrefix := make([]chatSlashCompletionCandidate, 0, len(candidates))
	titleMatches := make([]chatSlashCompletionCandidate, 0)
	for _, candidate := range candidates {
		name := strings.ToLower(strings.TrimSpace(candidate.Command))
		if name == "" {
			continue
		}
		if name == query || strings.HasPrefix(name, query) {
			exactOrPrefix = append(exactOrPrefix, candidate)
			continue
		}
		summary := strings.ToLower(strings.TrimSpace(candidate.Summary))
		if summary != "" && strings.Contains(summary, query) {
			titleMatches = append(titleMatches, candidate)
		}
	}
	if len(exactOrPrefix) > 0 {
		return exactOrPrefix
	}
	return titleMatches
}

func dedupeSlashArgumentCandidates(candidates []chatSlashCompletionCandidate) []chatSlashCompletionCandidate {
	if len(candidates) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(candidates))
	out := make([]chatSlashCompletionCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		name := strings.ToLower(strings.TrimSpace(candidate.Command))
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, candidate)
	}
	return out
}

func cloneSlashCompletionCandidates(candidates []chatSlashCompletionCandidate) []chatSlashCompletionCandidate {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]chatSlashCompletionCandidate, len(candidates))
	copy(out, candidates)
	return out
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func reasoningEffortArgumentCandidatesFromOptions(options []string) []chatSlashCompletionCandidate {
	if len(options) == 0 {
		return nil
	}
	candidates := make([]chatSlashCompletionCandidate, 0, len(options))
	for _, option := range options {
		option = strings.TrimSpace(option)
		if option == "" {
			continue
		}
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command:     option,
			Summary:     "reasoning_effort",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: false,
		})
	}
	return candidates
}

func providerCandidatesFromNames(names []string) []chatSlashCompletionCandidate {
	if len(names) == 0 {
		return nil
	}
	candidates := make([]chatSlashCompletionCandidate, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command:     name,
			Summary:     "provider",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: true,
		})
	}
	return candidates
}

func runtimeModelCandidatesFromNames(names []string) []chatSlashCompletionCandidate {
	if len(names) == 0 {
		return nil
	}
	candidates := make([]chatSlashCompletionCandidate, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command:     name,
			Summary:     "model",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: true,
		})
	}
	return candidates
}
