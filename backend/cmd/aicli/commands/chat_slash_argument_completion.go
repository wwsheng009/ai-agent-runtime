package commands

import (
	"context"
	"sort"
	"strings"
	"sync"
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
	manager *runtimechat.SessionManager
	userID  string
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
	case "/model":
		return completeModelSlashArgs(session, argsText, cursor)
	case "/login":
		return completeLoginSlashArgs(session, argsText, cursor)
	case "/stream":
		return completeStaticSlashArgs(argsText, cursor, []chatSlashCompletionCandidate{
			{Command: "on", Summary: "开启流式输出", Group: string(chatSlashCommandGroupModel)},
			{Command: "off", Summary: "关闭流式输出", Group: string(chatSlashCommandGroupModel)},
			{Command: "toggle", Summary: "切换流式状态", Group: string(chatSlashCommandGroupModel)},
			{Command: "status", Summary: "查看当前状态", Group: string(chatSlashCommandGroupModel)},
		})
	case "/compact":
		return completeStaticSlashArgs(argsText, cursor, []chatSlashCompletionCandidate{
			{Command: "auto", Summary: "自动模式", Group: string(chatSlashCommandGroupModel)},
			{Command: "local", Summary: "本地压缩", Group: string(chatSlashCommandGroupModel)},
			{Command: "remote", Summary: "远端压缩", Group: string(chatSlashCommandGroupModel)},
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
			{Command: "off", Summary: "关闭审批复用", Group: string(chatSlashCommandGroupPermission)},
			{Command: "session_readonly_shell", Summary: "会话只读 shell", Group: string(chatSlashCommandGroupPermission)},
			{Command: "team_readonly_shell", Summary: "团队只读 shell", Group: string(chatSlashCommandGroupPermission)},
		})
	case "/image":
		return completeStaticSlashArgs(argsText, cursor, []chatSlashCompletionCandidate{
			{Command: "clear", Summary: "清空待发送图片附件", Group: string(chatSlashCommandGroupContext)},
		})
	case "/queue":
		return completeStaticSlashArgs(argsText, cursor, []chatSlashCompletionCandidate{
			{Command: "status", Summary: "查看当前状态", Group: string(chatSlashCommandGroupContext)},
			{Command: "clear", Summary: "清空排队输入", Group: string(chatSlashCommandGroupContext)},
		})
	case "/debug":
		return completeStaticSlashArgs(argsText, cursor, []chatSlashCompletionCandidate{
			{Command: "export", Summary: "打包调试文件", Group: string(chatSlashCommandGroupSession)},
			{Command: "zip", Summary: "打包调试文件", Group: string(chatSlashCommandGroupSession)},
			{Command: "--output", Summary: "指定 zip 输出文件", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
			{Command: "--dir", Summary: "指定 zip 输出目录", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
		})
	case "/agents":
		return completeAgentsSlashArgs(session, argsText, cursor)
	case "/function", "/describe", "/call", "/tool":
		return completeCatalogFunctionArgs(session, argsText, cursor, command)
	case "/skill", "/skills":
		return completeSkillArgs(session, argsText, cursor)
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
	case "send":
		return completeAgentsMessageTargetSlashArgs(session, ctx, query)
	case "followup", "task":
		return completeAgentsMessageTargetSlashArgs(session, ctx, query)
	default:
		return matchSlashArgumentCandidates(agentTopLevelArgumentCandidates(), query)
	}
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

func agentTopLevelArgumentCandidates() []chatSlashCompletionCandidate {
	return []chatSlashCompletionCandidate{
		{Command: "panel", Summary: "显示多 agent 富交互面板", Group: string(chatSlashCommandGroupSession)},
		{Command: "dashboard", Summary: "panel 的别名", Group: string(chatSlashCommandGroupSession)},
		{Command: "pick", Summary: "弹出 agent picker", Group: string(chatSlashCommandGroupSession)},
		{Command: "target", Summary: "设置或列出默认 agent 消息目标", Group: string(chatSlashCommandGroupSession)},
		{Command: "send", Summary: "向目标 agent 投递 mailbox 消息", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
		{Command: "followup", Summary: "向目标 agent 投递或触发 follow-up task", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
		{Command: "select", Summary: "pick 的别名", Group: string(chatSlashCommandGroupSession)},
		{Command: "task", Summary: "followup 的别名", Group: string(chatSlashCommandGroupSession), AcceptsArgs: true},
	}
}

func agentPanelArgumentCandidates() []chatSlashCompletionCandidate {
	return []chatSlashCompletionCandidate{
		{Command: "follow", Summary: "进入面板跟随模式或等待 mailbox 更新", Group: string(chatSlashCommandGroupSession)},
		{Command: "target", Summary: "切换 panel 到指定 agent target", Group: string(chatSlashCommandGroupSession)},
		{Command: "next", Summary: "切换 panel 到下一个 agent target", Group: string(chatSlashCommandGroupSession)},
		{Command: "prev", Summary: "切换 panel 到上一个 agent target", Group: string(chatSlashCommandGroupSession)},
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
	return strings.TrimSpace(session.SelectedAgentTarget)
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
		{Command: "--set-default", Summary: "设为默认 provider", Group: string(chatSlashCommandGroupModel)},
		{Command: "--dry-run", Summary: "只校验不写配置", Group: string(chatSlashCommandGroupModel)},
		{Command: "--switch", Summary: "登录后切换当前会话", Group: string(chatSlashCommandGroupModel)},
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
	values := []string{"openai", "codex-apikey", "anthropic", "gemini", "codex-oauth"}
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

	key := slashSessionCacheKey{
		manager: session.SessionManager,
		userID:  strings.TrimSpace(session.SessionUserID),
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

	sessions, err := session.SessionManager.List(context.Background(), session.SessionUserID)
	if err != nil {
		return nil
	}

	candidates := make([]chatSlashCompletionCandidate, 0, len(sessions)+1)
	if includeLatest {
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command:     "latest",
			Summary:     "直接恢复最近可恢复会话",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: false,
		})
	}
	currentID := currentRuntimeSessionID(session)
	for _, item := range sessions {
		if item == nil || strings.TrimSpace(item.ID) == "" {
			continue
		}
		preview := item.BuildPreview()
		summary := strings.TrimSpace(preview.Title)
		if summary == "" {
			summary = strings.TrimSpace(preview.Summary)
		}
		if summary == "" {
			summary = string(item.State)
		}
		if currentID != "" && strings.EqualFold(currentID, item.ID) {
			summary += "（当前）"
		}
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command:     item.ID,
			Summary:     summary,
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

func matchSlashArgumentCandidates(candidates []chatSlashCompletionCandidate, query string) []chatSlashCompletionCandidate {
	if len(candidates) == 0 {
		return nil
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return cloneSlashCompletionCandidates(candidates)
	}

	matches := make([]chatSlashCompletionCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		name := strings.ToLower(strings.TrimSpace(candidate.Command))
		if name == "" {
			continue
		}
		if name == query || strings.HasPrefix(name, query) {
			matches = append(matches, candidate)
		}
	}
	return matches
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
