package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/auth"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

// chat_mcp_auth.go 把 MCP OAuth 接进 chat 文本通道（以及选择器动作）：
//
//   - `/mcp auth`             列出所有 auth: oauth 的 server 授权状态
//   - `/mcp auth <name>`      发起/继续授权：起流程给出授权链接；已收到浏览器回调时直接兑换
//   - `/mcp auth <name> <粘贴内容>`
//     用回调 URL 或裸 code 完成兑换
//   - `/mcp auth <name> --clear`
//     清除该 server 的令牌（等同 aicli mcp logout）
//   - `--no-browser`          起流程时不自动打开浏览器（无桌面环境）
//
// 设计要点：与 CLI 共用 internal/mcp/auth 的同一套 PKCE 流程与令牌存储
// （~/.aicli/mcp-tokens.json，可用 AICLI_MCP_TOKENS_FILE 覆盖）；chat 不能阻塞终端读
// stdin，因此把「起流程」与「兑换」拆到多个用户回合完成，用进程内注册表暂存未完成的
// PendingAuth，TTL 与 auth.DefaultFlowTimeout 对齐，超时即释放回调端口。

// chatMCPAuthPendingTTL 是 chat 侧保留未完成授权流程的时长。
const chatMCPAuthPendingTTL = auth.DefaultFlowTimeout

// chatMCPAuthPending 是一次未完成的授权流程（按 server 名索引，进程内有效）。
type chatMCPAuthPending struct {
	session *auth.Session
	pending *auth.PendingAuth
}

var chatMCPAuthRegistry = struct {
	mu      sync.Mutex
	entries map[string]*chatMCPAuthPending
}{entries: map[string]*chatMCPAuthPending{}}

// takeChatMCPAuthPending 取出未完成流程；已过期时清理并返回 nil。
func takeChatMCPAuthPending(name string) *chatMCPAuthPending {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	chatMCPAuthRegistry.mu.Lock()
	entry := chatMCPAuthRegistry.entries[name]
	if entry != nil && entry.pending != nil && entry.pending.Expired() {
		delete(chatMCPAuthRegistry.entries, name)
		_ = entry.pending.Close()
		entry = nil
	}
	chatMCPAuthRegistry.mu.Unlock()
	return entry
}

func storeChatMCPAuthPending(name string, entry *chatMCPAuthPending) {
	name = strings.TrimSpace(name)
	if name == "" || entry == nil {
		return
	}
	chatMCPAuthRegistry.mu.Lock()
	if old := chatMCPAuthRegistry.entries[name]; old != nil && old.pending != nil {
		_ = old.pending.Close()
	}
	chatMCPAuthRegistry.entries[name] = entry
	chatMCPAuthRegistry.mu.Unlock()
}

func dropChatMCPAuthPending(name string) *chatMCPAuthPending {
	name = strings.TrimSpace(name)
	chatMCPAuthRegistry.mu.Lock()
	entry := chatMCPAuthRegistry.entries[name]
	delete(chatMCPAuthRegistry.entries, name)
	chatMCPAuthRegistry.mu.Unlock()
	return entry
}

// chatMCPAuthPendingActive 报告该 server 是否有未完成的授权流程（选择器据此换动作标签）。
func chatMCPAuthPendingActive(name string) bool {
	return takeChatMCPAuthPending(name) != nil
}

// resetChatMCPAuthPendings 仅用于测试：清空注册表并释放回调端口。
func resetChatMCPAuthPendings() {
	chatMCPAuthRegistry.mu.Lock()
	entries := chatMCPAuthRegistry.entries
	chatMCPAuthRegistry.entries = map[string]*chatMCPAuthPending{}
	chatMCPAuthRegistry.mu.Unlock()
	for _, entry := range entries {
		if entry != nil && entry.pending != nil {
			_ = entry.pending.Close()
		}
	}
}

// chatMCPAuthText 执行 `/mcp auth` 子命令族。
//
// rawRemainder 是命令里「子命令 + server 名之后」的原始文本：粘贴的回调 URL 含 `&`，
// 会被命令 tokenizer 拆成多个 token（`...?code=x`、`&`、`state=y`），因此完成授权
// 必须按原文而不是按 token 还原，否则会丢掉 state（CSRF 校验形同虚设）。
func chatMCPAuthText(service chatMCPService, args []string, rawRemainder string, onMutate func()) string {
	if service == nil {
		return "错误: MCP 管理服务不可用"
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatMCPCommandTimeout)
	defer cancel()

	if len(args) == 0 || isChatMCPAuthStatusFlag(args[0]) {
		return chatMCPAuthStatusText(service, ctx)
	}
	name := strings.TrimSpace(args[0])
	if name == "" {
		return chatMCPCommandUsage
	}

	noBrowser := false
	clear := false
	pasted := make([]string, 0, len(args)-1)
	for _, arg := range args[1:] {
		switch {
		case isChatMCPAuthClearFlag(arg):
			clear = true
		case strings.EqualFold(strings.TrimSpace(arg), "--no-browser"):
			noBrowser = true
		case isChatMCPAuthStatusFlag(arg):
			// 允许 `/mcp auth <name> --status`：查看单个 server 的授权状态。
			return chatMCPAuthSingleStatusText(service, ctx, name)
		default:
			pasted = append(pasted, arg)
		}
	}
	switch {
	case clear:
		return chatMCPAuthClearText(service, ctx, name)
	case len(pasted) > 0:
		input := strings.TrimSpace(rawRemainder)
		if input == "" {
			input = strings.Join(pasted, " ")
		}
		return chatMCPAuthCompleteText(service, ctx, name, strings.Trim(input, `"'`), onMutate)
	default:
		return chatMCPAuthBeginOrContinueText(service, ctx, name, noBrowser, onMutate)
	}
}

// dropChatCommandWords 从原始参数文本里去掉前 n 个词（支持带引号的词），
// 保留其余部分原文（含 `&` 等 tokenizer 会拆开的字符）。
func dropChatCommandWords(argument string, n int) string {
	rest := strings.TrimSpace(argument)
	for i := 0; i < n; i++ {
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return ""
		}
		if quote := rest[0]; quote == '"' || quote == '\'' {
			if end := strings.IndexByte(rest[1:], quote); end >= 0 {
				rest = rest[end+2:]
				continue
			}
			return ""
		}
		if idx := strings.IndexAny(rest, " \t"); idx >= 0 {
			rest = rest[idx+1:]
			continue
		}
		return ""
	}
	return strings.TrimSpace(rest)
}

func isChatMCPAuthStatusFlag(arg string) bool {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "--status", "-s", "status":
		return true
	default:
		return false
	}
}

func isChatMCPAuthClearFlag(arg string) bool {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "--clear", "clear", "logout", "--logout":
		return true
	default:
		return false
	}
}

// newChatMCPAuthSession 用「分层合并后生效」的 server 配置构造 OAuth 会话，
// 令牌读写与 CLI（aicli mcp auth）落在同一份存储上。
func newChatMCPAuthSession(service chatMCPService, ctx context.Context, name string) (*auth.Session, *config.MCPConfig, error) {
	server, err := service.Get(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	if server == nil {
		return nil, nil, fmt.Errorf("MCP %q 不存在", name)
	}
	if server.Auth == nil {
		return nil, server, fmt.Errorf("MCP %q 未配置 auth: oauth", name)
	}
	store, err := auth.NewTokenStore("")
	if err != nil {
		return nil, server, err
	}
	session, err := auth.NewSession(server.Name, server.URL, *server.Auth, auth.SessionOptions{Store: store})
	if err != nil {
		return nil, server, err
	}
	return session, server, nil
}

// chatMCPAuthStatusFor 直接由配置构造会话并读授权状态（零网络）；选择器用它判断
// 是否已有令牌，从而决定「认证 / 重新认证」与「清除授权」动作。第二个返回值为 false
// 表示配置不可用（缺 URL、auth 结构非法等）。
func chatMCPAuthStatusFor(cfg config.MCPConfig) (auth.SessionStatus, bool) {
	if cfg.Auth == nil {
		return auth.SessionStatus{}, false
	}
	store, err := auth.NewTokenStore("")
	if err != nil {
		return auth.SessionStatus{}, false
	}
	session, err := auth.NewSession(cfg.Name, cfg.URL, *cfg.Auth, auth.SessionOptions{Store: store})
	if err != nil {
		return auth.SessionStatus{}, false
	}
	return session.Status(), true
}

// chatMCPOAuthServers 返回所有配置了 auth: oauth 的 server（按名称排序）。
func chatMCPOAuthServers(service chatMCPService, ctx context.Context) ([]mcpadmin.Item, error) {
	items, err := service.List(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]mcpadmin.Item, 0, len(items))
	for _, item := range items {
		if item.Config.Auth == nil {
			continue
		}
		views = append(views, item)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Config.Name < views[j].Config.Name })
	return views, nil
}

func chatMCPAuthStatusText(service chatMCPService, ctx context.Context) string {
	views, err := chatMCPOAuthServers(service, ctx)
	if err != nil {
		return "错误: 读取 MCP 列表失败: " + err.Error()
	}
	if len(views) == 0 {
		return "当前没有配置 auth: oauth 的 MCP Server\n用 /mcp add <name> <url> --auth oauth 添加；CLI 侧同口径：aicli mcp auth --status。"
	}
	lines := []string{fmt.Sprintf("MCP OAuth 授权状态（%d 个）", len(views))}
	authorized := 0
	for _, view := range views {
		name := view.Config.Name
		session, _, sessionErr := newChatMCPAuthSession(service, ctx, name)
		if sessionErr != nil {
			lines = append(lines, fmt.Sprintf("- %s: %s", name, sessionErr.Error()))
			continue
		}
		status := session.Status()
		lines = append(lines, chatMCPAuthStatusLine(status))
		if status.Authenticated {
			authorized++
		}
	}
	lines = append(lines, fmt.Sprintf("已授权 %d/%d；发起或继续授权：/mcp auth <name>", authorized, len(views)))
	return strings.Join(lines, "\n")
}

func chatMCPAuthSingleStatusText(service chatMCPService, ctx context.Context, name string) string {
	session, _, err := newChatMCPAuthSession(service, ctx, name)
	if err != nil {
		return chatMCPAuthSessionErrorText(name, err)
	}
	status := session.Status()
	lines := []string{chatMCPAuthStatusLine(status)}
	if url := strings.TrimSpace(status.ServerURL); url != "" {
		lines = append(lines, "地址: "+url)
	}
	if authServer := strings.TrimSpace(status.AuthServer); authServer != "" {
		lines = append(lines, "授权服务器: "+authServer)
	}
	if pending := takeChatMCPAuthPending(name); pending != nil {
		lines = append(lines, "有进行中的授权流程；运行 /mcp auth "+name+" 继续。")
	}
	return strings.Join(lines, "\n")
}

func chatMCPAuthStatusLine(status auth.SessionStatus) string {
	marker := "○"
	detail := "未授权"
	switch {
	case status.Authenticated:
		marker = "●"
		detail = "已授权"
		if scope := strings.TrimSpace(status.Scope); scope != "" {
			detail += "（scope: " + scope + "）"
		}
		if !status.ExpiresAt.IsZero() {
			detail += "，到期 " + status.ExpiresAt.Local().Format("2006-01-02 15:04")
		}
		if status.HasRefreshToken {
			detail += "，可自动刷新"
		}
	case status.NeedsAuth:
		marker = "!"
		detail = "需认证"
		if reason := strings.TrimSpace(status.Reason); reason != "" {
			detail += "：" + reason
		}
	}
	return fmt.Sprintf("%s %s [%s]", marker, status.ServerName, detail)
}

// chatMCPAuthBeginOrContinueText 是 `/mcp auth <name>`：已有回调则直接兑换，
// 已有未完成流程则提示继续，否则发起新流程。
func chatMCPAuthBeginOrContinueText(service chatMCPService, ctx context.Context, name string, noBrowser bool, onMutate func()) string {
	if entry := takeChatMCPAuthPending(name); entry != nil {
		token, ok, err := entry.pending.TakeCallback(ctx)
		if err != nil {
			dropChatMCPAuthPending(name)
			_ = entry.pending.Close()
			return fmt.Sprintf("错误: %s 授权失败: %v", name, err)
		}
		if ok {
			dropChatMCPAuthPending(name)
			_ = entry.pending.Close()
			if token == nil {
				return fmt.Sprintf("错误: %s 授权失败（未取得令牌）", name)
			}
			return chatMCPAuthSuccessText(service, ctx, token.ServerName, token.Scope, onMutate)
		}
		return chatMCPAuthWaitingText(entry.pending, name)
	}

	session, server, err := newChatMCPAuthSession(service, ctx, name)
	if err != nil {
		return chatMCPAuthSessionErrorText(name, err)
	}
	pending, err := session.BeginAuth(ctx, auth.AuthorizeOptions{NoBrowser: noBrowser, Timeout: chatMCPAuthPendingTTL})
	if err != nil {
		return chatMCPAuthSessionErrorText(name, err)
	}
	storeChatMCPAuthPending(server.Name, &chatMCPAuthPending{session: session, pending: pending})
	return chatMCPAuthStartedText(pending, server.Name)
}

func chatMCPAuthStartedText(pending *auth.PendingAuth, name string) string {
	lines := []string{fmt.Sprintf("已发起 OAuth 授权：%s", name)}
	if err := pending.BrowserErr(); err != nil {
		lines = append(lines,
			fmt.Sprintf("自动打开浏览器失败（%v），请手动打开：", err),
			"  "+pending.AuthURL())
	} else {
		lines = append(lines, "已尝试用默认浏览器打开授权链接；未自动跳转时请手动访问：", "  "+pending.AuthURL())
	}
	if uri := pending.RedirectURI(); uri != "" {
		lines = append(lines, "回调地址: "+uri)
	}
	lines = append(lines,
		"完成后（任一方式）：",
		fmt.Sprintf("  1) 浏览器回调页面提示成功时，再次运行 /mcp auth %s 确认", name),
		fmt.Sprintf("  2) 回调页面打不开时，把地址栏里的完整回调 URL（或其中的 code）粘贴回来：/mcp auth %s <回调URL 或 code>", name),
		fmt.Sprintf("有效期至 %s；查看全部状态：/mcp auth", pending.ExpiresAt().Local().Format("2006-01-02 15:04:05")))
	return strings.Join(lines, "\n")
}

func chatMCPAuthWaitingText(pending *auth.PendingAuth, name string) string {
	lines := []string{fmt.Sprintf("仍在等待浏览器回调：%s", name)}
	if url := pending.AuthURL(); url != "" {
		lines = append(lines, "授权链接:", "  "+url)
	}
	lines = append(lines,
		fmt.Sprintf("若回调页面已打不开或已完成授权，请把完整回调 URL（或 code）粘贴回来：/mcp auth %s <回调URL 或 code>", name))
	return strings.Join(lines, "\n")
}

// chatMCPAuthCompleteText 用粘贴的回调 URL / code 完成兑换。
func chatMCPAuthCompleteText(service chatMCPService, ctx context.Context, name, input string, onMutate func()) string {
	entry := takeChatMCPAuthPending(name)
	if entry == nil {
		return fmt.Sprintf("错误: 没有进行中的授权流程（%s）\n先运行 /mcp auth %s 获取授权链接。", name, name)
	}
	token, err := entry.pending.Complete(ctx, input)
	dropChatMCPAuthPending(name)
	_ = entry.pending.Close()
	if err != nil {
		return fmt.Sprintf("错误: %s 授权失败: %v", name, err)
	}
	return chatMCPAuthSuccessText(service, ctx, token.ServerName, token.Scope, onMutate)
}

// chatMCPAuthClearText 清除令牌（等同 aicli mcp logout）。
func chatMCPAuthClearText(service chatMCPService, ctx context.Context, name string) string {
	session, server, err := newChatMCPAuthSession(service, ctx, name)
	if err != nil {
		return chatMCPAuthSessionErrorText(name, err)
	}
	cleared, err := session.Logout()
	if err != nil {
		return fmt.Sprintf("错误: 清除 %s 的令牌失败: %v", name, err)
	}
	if entry := dropChatMCPAuthPending(server.Name); entry != nil && entry.pending != nil {
		_ = entry.pending.Close()
	}
	if !cleared {
		return fmt.Sprintf("MCP %q 没有已保存的 OAuth 令牌", name)
	}
	return fmt.Sprintf("已清除 OAuth 令牌：%s\n下次连接会重新要求授权（/mcp auth %s）。", name, name)
}

// chatMCPAuthSuccessText 授权成功后的统一收尾：热重载让工具面立即生效。
func chatMCPAuthSuccessText(service chatMCPService, ctx context.Context, name, scope string, onMutate func()) string {
	lines := []string{fmt.Sprintf("✅ 授权成功：%s（scope: %s）", name, firstNonEmptyTrimmed(scope, "(默认)"))}
	if err := service.Reload(ctx); err != nil {
		lines = append(lines, "警告: 热重载失败（"+err.Error()+"），可运行 /mcp reload 重试")
	} else if onMutate != nil {
		onMutate()
	}
	lines = append(lines, "用 /mcp status "+name+" 查看连接状态。")
	return strings.Join(lines, "\n")
}

func chatMCPAuthSessionErrorText(name string, err error) string {
	if err == nil {
		return fmt.Sprintf("错误: %s 的 OAuth 配置不可用", name)
	}
	if strings.Contains(err.Error(), "auth: oauth") {
		return fmt.Sprintf("%v\n用法: aicli mcp add %s <URL> --auth oauth（或 aicli mcp auth %s）", err, name, name)
	}
	return fmt.Sprintf("错误: %s 的 OAuth 会话不可用: %v", name, err)
}
