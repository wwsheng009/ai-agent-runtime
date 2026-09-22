package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// ============================================================================
// /account 与 /accounts 各自的独立屏幕（alternate screen）
//
// 两个命令不再把表格追加到主消息流，而是各自打开一块只读备用屏：
//
//	/account  → 单账户屏（标题带 provider 名）：当前/指定 provider 的账户明细；
//	/accounts → 全部账户屏：所有 provider 的账户总览表。
//
// 与 /usage、/debug display、各 picker 共用同一 ScreenLease 契约：快照（含实时
// 余额拉取）在进入备用屏之前就已捕获，屏内只做纯渲染。全部账户屏额外提供屏内刷新
// 键（r）：它只提交/复用一次后台刷新，然后重投影「开屏前冻结的 provider 副本 +
// mutex 保护的会话缓存」，既不阻塞渲染循环，也不读取可变的 Providers map。统一
// 交互 TTY 之外的投影（JSON / plain / 备用屏降级）仍提交普通文档 cell。
// ============================================================================

const (
	// accountScreenTitlePrefix 是 /account 单账户屏的标题前缀（后接 provider 名）。
	accountScreenTitlePrefix = "账户余额"
	// accountsScreenTitle 是 /accounts 全部账户屏的标题。
	accountsScreenTitle = "全部账户"
	// screenViewerFooter 是两块账户屏共用的返回提示。
	screenViewerFooter = "提示: q/Esc 返回会话"
	// accountsScreenRefreshHint 是全部账户屏页脚里的刷新键提示（由备用屏页脚渲染，
	// 因此滚动到任何位置都看得见）。
	accountsScreenRefreshHint = "r 刷新显示"
	// accountsScreenSubmitFailedState 是屏内刷新提交失败时的状态行标签：提交失败
	// 不能覆盖缓存内容，只在状态行上说明「这一下没提交出去」。
	accountsScreenSubmitFailedState = "刷新未提交"
)

// chatAccountRefreshParams 是打开 /accounts 屏那一刻冻结的刷新参数（值类型）：
// 屏内按 r 时用它们重新提交后台刷新，屏幕因此不读取任何可变的请求状态。
type chatAccountRefreshParams struct {
	EnabledOnly bool
	Timeout     time.Duration
}

// commandRequest 还原提交后台刷新所需的命令请求：屏内刷新只关心目标过滤与单
// provider 超时，模式/等待/JSON 这些提交无关的字段保持零值。
func (p chatAccountRefreshParams) commandRequest() chatAccountCommandRequest {
	return chatAccountCommandRequest{
		Variant:     chatAccountVariantAll,
		EnabledOnly: p.EnabledOnly,
		Timeout:     p.Timeout,
	}
}

// AccountScreenRequest is the immutable payload of the typed /account
// alternate-screen effect. The report (including the live balance fetch when
// the command ran in refresh mode) is captured before the lease-bound screen
// is entered, so the viewer itself performs no I/O and reads no mutable
// session state.
type AccountScreenRequest struct {
	Report chatAccountReport
}

// AccountListScreenRequest is the immutable payload of the typed /accounts
// alternate-screen effect (all configured providers, same capture contract as
// AccountScreenRequest). Refresh carries the frozen parameters the in-screen
// refresh key re-uses to submit another background refresh.
type AccountListScreenRequest struct {
	List    chatAccountListReport
	Refresh chatAccountRefreshParams
}

// canOpenChatAccountScreen keeps /account and /accounts strictly inside the
// unified alternate-screen contract. Both screens borrow the same ScreenLease
// the /usage viewer, /debug overlay, resume picker and transcript pager use;
// when any prerequisite is absent the command degrades to its document cell
// instead of flashing a broken alternate screen.
func canOpenChatAccountScreen(session *ChatSession) bool {
	if session == nil || session.NoInteractive || session.JSONOutput ||
		session.Interaction == nil || session.Surface == nil {
		return false
	}
	if !session.Surface.Enabled() || !session.Surface.OwnedViewport() ||
		session.Surface.LeaseActive() || session.Surface.HasActivePopup() {
		return false
	}
	return ui.CanUseFullScreenList(resumeFullScreenTerminal(session))
}

// openChatAccountScreen renders the single-account report on its own screen.
// The body is built from the already captured report; a lost prerequisite
// degrades to the same document cell the plain projection uses.
func openChatAccountScreen(session *ChatSession, req AccountScreenRequest) {
	if !canOpenChatAccountScreen(session) {
		_ = renderChatCommandResult(session, accountScreenFallbackResult(req), false)
		return
	}
	openChatLeaseBoundTextViewer(session, ui.DebugOverlayOptions{
		Title: accountScreenTitle(req.Report),
		Body:  strings.Join(accountScreenBodyLines(req.Report), "\n"),
	}, "账户界面")
}

// openChatAccountsScreen renders the whole-configuration provider table on its
// own screen (distinct title from /account so the two views are never
// confused). The screen is refreshable: `r` submits (or reuses) one background
// refresh and immediately re-projects the cached snapshot, and the viewer keeps
// polling so a finished refresh shows up without another key press.
func openChatAccountsScreen(session *ChatSession, req AccountListScreenRequest) {
	if !canOpenChatAccountScreen(session) {
		_ = renderChatCommandResult(session, accountsScreenFallbackResult(req), false)
		return
	}
	openChatLeaseBoundTextViewer(session, chatAccountsScreenOptions(session, req), "全部账户界面")
}

// chatAccountsScreenOptions 组装全部账户屏的渲染选项。正文与屏内刷新回调同源
// （chatAccountsScreenRefresher.render），因此开屏第一帧与按 r 之后的帧不会漂移。
func chatAccountsScreenOptions(session *ChatSession, req AccountListScreenRequest) ui.DebugOverlayOptions {
	refresher := newChatAccountsScreenRefresher(session, req)
	title, body := refresher.current()
	return ui.DebugOverlayOptions{
		Title:       title,
		Body:        body,
		Refresh:     refresher.refresh,
		RefreshHint: accountsScreenRefreshHint,
	}
}

// chatAccountsScreenRefresher 把 /accounts 备用屏接到后台刷新上：按 r 先提交（或
// 复用）一次后台刷新，再重投影缓存快照；备用屏的每个轮询节拍也重投影一次，所以
// 任务完成时屏幕会自己从「后台刷新中」翻到「已刷新」。
//
// 契约（与两块账户屏的静态快照约定同源）：
//   - provider 值快照在开屏前冻结，缓存与当前 provider 的实时快照都取 mutex 保护的
//     副本，渲染循环因此不读取任何可变的会话状态；
//   - 回调只在备用屏的渲染循环里串行调用，lastTitle/lastBody 缓存无需加锁；
//   - 提交失败只改状态行，绝不吞掉缓存表格。
type chatAccountsScreenRefresher struct {
	session *ChatSession
	items   map[string]config.Provider
	params  chatAccountRefreshParams

	lastTitle string
	lastBody  string
	// submitError 是最近一次按 r 的提交错误：它随下一帧的状态行渲染，成功后清零。
	submitError string
}

func newChatAccountsScreenRefresher(session *ChatSession, req AccountListScreenRequest) *chatAccountsScreenRefresher {
	refresher := &chatAccountsScreenRefresher{
		session: session,
		items:   chatAccountProviderItems(session),
		params:  req.Refresh,
	}
	refresher.lastTitle, refresher.lastBody = refresher.render()
	return refresher
}

// current 返回已投影的当前帧（开屏第一帧）。
func (r *chatAccountsScreenRefresher) current() (string, string) {
	if r == nil {
		return "", ""
	}
	return r.lastTitle, r.lastBody
}

// refresh 是备用屏的刷新回调：key 触发时先提交（或复用）后台刷新，再重投影并与
// 上一帧对比；changed 决定备用屏是否重绘。
func (r *chatAccountsScreenRefresher) refresh(trigger ui.DebugOverlayRefreshTrigger) (string, string, bool) {
	if r == nil {
		return "", "", false
	}
	if trigger == ui.DebugOverlayRefreshKey {
		r.submit()
	}
	title, body := r.render()
	changed := title != r.lastTitle || body != r.lastBody
	r.lastTitle, r.lastBody = title, body
	return title, body, changed
}

// submit 提交（或复用）一次后台刷新：单飞任务由 submitChatAccountsRefresh 保证，
// 连按 r 不会把上游打爆；提交失败只记在状态行上，屏幕继续显示缓存。
func (r *chatAccountsScreenRefresher) submit() {
	_, _, errText := submitChatAccountsRefresh(r.session, r.params.commandRequest())
	r.submitError = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(errText), "错误:"))
}

// render 重投影一帧（零网络）：表格来自冻结的 provider 快照 + 会话缓存，状态行
// 来自后台任务状态；最近一次提交失败优先于任务状态显示。
func (r *chatAccountsScreenRefresher) render() (string, string) {
	list := chatAccountsCachedReport(r.session, r.items, r.params.commandRequest())
	if r.submitError != "" {
		list.RefreshState = accountsScreenSubmitFailedState
		list.RefreshDetail = r.submitError
	}
	return accountsScreenTitleFor(list), strings.Join(accountsScreenBodyLines(list), "\n")
}

// openChatLeaseBoundTextViewer publishes one already rendered frame through the
// shared lease-bound overlay viewer: acquire → wait for the lease barrier →
// run the read-only viewer → release. The caller owns the prerequisite check
// (canOpenChatAccountScreen), the first frame capture and — for refreshable
// screens — the refresh callback, so this helper never triggers I/O by itself.
func openChatLeaseBoundTextViewer(session *ChatSession, options ui.DebugOverlayOptions, label string) {
	lease, err := session.Surface.AcquireAlternateScreen(context.Background(), ui.FullscreenRequest{Title: options.Title})
	if err != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("打开%s失败: %w", label, err)), false)
		return
	}
	// The surface posts LeaseAcquired as part of the acquire transaction. Wait
	// for that barrier so the first frame is never raced by a pending primary
	// flush; the viewer itself owns no actor semantic state.
	if !session.Interaction.waitUIActorIdleBounded("open " + label) {
		_ = lease.Release(context.Background())
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("%s渲染未就绪", label)), false)
		return
	}

	runErr := ui.RunDebugOverlayWithLease(context.Background(), resumeFullScreenTerminal(session), options, lease)
	releaseErr := lease.Release(context.Background())
	if runErr != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("%s异常: %w", label, runErr)), false)
		return
	}
	if releaseErr != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("关闭%s失败: %w", label, releaseErr)), false)
	}
}

// accountScreenTitle 让单账户屏的标题始终带 provider 名：同一屏的重复打开与
// 「当前账户是谁」在标题上就能回答。
func accountScreenTitle(report chatAccountReport) string {
	name := strings.TrimSpace(report.Provider)
	if name == "" {
		return accountScreenTitlePrefix
	}
	return accountScreenTitlePrefix + " · " + name
}

// accountsScreenTitleFor 让全部账户屏的标题带上 provider 总数。
func accountsScreenTitleFor(list chatAccountListReport) string {
	if list.Total <= 0 {
		return accountsScreenTitle
	}
	return fmt.Sprintf("%s · %d provider", accountsScreenTitle, list.Total)
}

// accountScreenBodyLines 是单账户屏的纯渲染：正文与文档 cell 同源
// （formatChatAccountReportLines），因此备用屏与降级输出不会漂移。
func accountScreenBodyLines(report chatAccountReport) []string {
	lines := formatChatAccountReportLines(report)
	lines = append(lines, "", screenViewerFooter)
	return lines
}

// accountsScreenBodyLines 是全部账户屏的纯渲染：表头 + 每行 provider 的
// 余额/状态 + 汇总行。
func accountsScreenBodyLines(list chatAccountListReport) []string {
	lines := formatChatAccountListLines(list)
	lines = append(lines, "", screenViewerFooter)
	return lines
}

// accountScreenFallbackResult rebuilds the single-account document cell for
// sessions that cannot host the alternate screen.
func accountScreenFallbackResult(req AccountScreenRequest) CommandResult {
	return commandTextResult(strings.Join(formatChatAccountReportLines(req.Report), "\n"))
}

// accountsScreenFallbackResult rebuilds the provider table document cell for
// sessions that cannot host the alternate screen.
func accountsScreenFallbackResult(req AccountListScreenRequest) CommandResult {
	return commandTextResult(strings.Join(formatChatAccountListLines(req.List), "\n"))
}
