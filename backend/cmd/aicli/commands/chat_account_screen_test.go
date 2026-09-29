package commands

// /account 与 /accounts 独立屏幕迁移回归：
//  - 统一 interactive TTY 中两条命令各自只携带 CommandResult.Screen
//    （account.screen / accounts.screen），不提交 Scene cell，也绝不共用同一块屏；
//  - 备用屏不可用时降级为与屏内正文同源的文档 cell，绝不静默吞掉命令；
//  - 屏内正文是纯渲染：与 formatChatAccountReportLines / formatChatAccountListLines
//    完全同源，因此备用屏与降级输出不会漂移。

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/siteaccount"
)

// newUnifiedChatAccountSession 在缓存账户会话上挂载统一渲染链路（bridge +
// TerminalSession surface），用于验证两条账户命令的 alternate-screen 请求与降级
// 行为。测试环境无真实 TTY，备用屏打开器始终 fail-closed，因此这里只验证请求
// 携带与文档降级两条路径。
func newUnifiedChatAccountSession(t *testing.T) (*ChatSession, *bytes.Buffer) {
	t.Helper()
	session := chatAccountTestSession()

	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(100, 40)
	coordinator.SetSurface(surface)

	output := &bytes.Buffer{}
	if !coordinator.enableUnifiedRendererWithWriter(output) {
		t.Fatal("unified renderer did not attach")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	output.Reset()
	return session, output
}

func TestCanOpenChatAccountScreenGates(t *testing.T) {
	if canOpenChatAccountScreen(nil) {
		t.Fatal("canOpenChatAccountScreen(nil) must be false")
	}
	if canOpenChatAccountScreen(&ChatSession{}) {
		t.Fatal("canOpenChatAccountScreen must be false without a unified surface")
	}
	if canOpenChatAccountScreen(&ChatSession{NoInteractive: true}) {
		t.Fatal("canOpenChatAccountScreen must be false for noninteractive sessions")
	}
	if canOpenChatAccountScreen(&ChatSession{JSONOutput: true}) {
		t.Fatal("canOpenChatAccountScreen must be false for JSON sessions")
	}

	// 统一 surface 已挂载但无全屏 TTY（测试环境 Layout 为 nil）：必须 fail-closed，
	// 与 /usage 屏、/debug overlay 同一门槛。
	session, _ := newUnifiedChatAccountSession(t)
	if canOpenChatAccountScreen(session) {
		t.Fatal("canOpenChatAccountScreen must be false without a full-screen TTY")
	}
}

func TestAccountScreenTitlesSeparateTheTwoViews(t *testing.T) {
	if got := accountScreenTitle(chatAccountReport{Provider: "alpha"}); got != "账户余额 · alpha" {
		t.Fatalf("single-account title = %q", got)
	}
	if got := accountScreenTitle(chatAccountReport{}); got != accountScreenTitlePrefix {
		t.Fatalf("title without provider = %q", got)
	}

	list := chatAccountListReport{Total: 3}
	if got := accountsScreenTitleFor(list); got != "全部账户 · 3 provider" {
		t.Fatalf("all-accounts title = %q", got)
	}
	if got := accountsScreenTitleFor(chatAccountListReport{}); got != accountsScreenTitle {
		t.Fatalf("empty all-accounts title = %q", got)
	}

	// 两块屏的标题必须不同，否则用户无法分辨当前处于哪一个视图。
	if accountScreenTitlePrefix == accountsScreenTitle {
		t.Fatal("the two screens must not share a title prefix")
	}
}

func TestAccountScreenBodyLinesMatchDocumentCells(t *testing.T) {
	session := chatAccountTestSession()
	report := buildChatAccountReport("alpha", session.Provider)
	list := chatAccountListReport{
		Total:       1,
		WithAccount: 1,
		Providers:   []chatAccountReport{report},
	}

	single := accountScreenBodyLines(report)
	if !containsLine(single, screenViewerFooter) {
		t.Fatalf("single-account body is missing the footer: %+v", single)
	}
	for _, line := range formatChatAccountReportLines(report) {
		if !containsLine(single, line) {
			t.Fatalf("single-account body drifted from the document cell: missing %q", line)
		}
	}

	all := accountsScreenBodyLines(list)
	if !containsLine(all, screenViewerFooter) {
		t.Fatalf("all-accounts body is missing the footer: %+v", all)
	}
	for _, line := range formatChatAccountListLines(list) {
		if !containsLine(all, line) {
			t.Fatalf("all-accounts body drifted from the document cell: missing %q", line)
		}
	}
}

func TestExecuteStructuredChatAccountCommandsOnUnifiedTTYRequestTheirOwnScreen(t *testing.T) {
	session, _ := newUnifiedChatAccountSession(t)

	result, handled, err := tryExecuteStructuredChatCommand(session, "/account show")
	if err != nil || !handled {
		t.Fatalf("tryExecuteStructuredChatCommand(/account show) handled=%v err=%v", handled, err)
	}
	if result.Screen == nil || result.Screen.ID != "account.screen" {
		t.Fatalf("/account on a unified TTY must request the single-account screen: %+v", result.Screen)
	}
	if got := result.Screen.Title; got != "账户余额 · alpha" {
		t.Fatalf("single-account screen title = %q", got)
	}
	if got := strings.TrimSpace(ui.RenderDocumentPlain(result.Document())); got != "" {
		t.Fatalf("/account screen request must not carry a Scene cell, got %q", got)
	}

	result, handled, err = tryExecuteStructuredChatCommand(session, "/accounts --no-refresh")
	if err != nil || !handled {
		t.Fatalf("tryExecuteStructuredChatCommand(/accounts --no-refresh) handled=%v err=%v", handled, err)
	}
	if result.Screen == nil || result.Screen.ID != "accounts.screen" {
		t.Fatalf("/accounts on a unified TTY must request the all-accounts screen: %+v", result.Screen)
	}
	// 标题携带 provider 总数，同时证明请求里带着 provider 表（Total=2）。
	if got := result.Screen.Title; got != "全部账户 · 2 provider" {
		t.Fatalf("all-accounts screen title = %q", got)
	}
	if got := strings.TrimSpace(ui.RenderDocumentPlain(result.Document())); got != "" {
		t.Fatalf("/accounts screen request must not carry a Scene cell, got %q", got)
	}
}

func TestOpenChatAccountScreensDegradeToDocumentCells(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session, output := newUnifiedChatAccountSession(t)

	if canOpenChatAccountScreen(session) {
		t.Fatal("the fixture is expected to be a fail-closed TTY (no full-screen terminal)")
	}
	report := buildChatAccountReport("alpha", session.Provider)
	// 走与命令生产点同一条派发路径：不可承载备用屏时 Spec 带 ForceInline，
	// openChatScreen 把内容降级为主屏内联文档单元格（行为与旧 opener 一致）。
	dispatchChatScreenEffects(session, CommandResult{
		Action: CommandContinue,
		Screen: chatScreenSpecRef(chatScreenAccountSpec(session, AccountScreenRequest{Report: report})),
	})
	dispatchChatScreenEffects(session, CommandResult{
		Action: CommandContinue,
		Screen: chatScreenSpecRef(chatScreenAccountsSpec(session, AccountListScreenRequest{
			List: chatAccountListReport{
				Total:       1,
				WithAccount: 1,
				Providers:   []chatAccountReport{report},
			},
		})),
	})
	coordinator := session.Interaction
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	rendered := output.String()
	for _, want := range []string{"Provider: alpha", "1 provider(s), 1 with account"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("degraded output is missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, screenViewerFooter) {
		t.Fatalf("the document-cell degradation must not carry viewer chrome:\n%s", rendered)
	}
}

func TestAccountScreenFallbackMatchesPlainProjection(t *testing.T) {
	session := chatAccountTestSession()
	report := buildChatAccountReport("alpha", session.Provider)

	plain := executeStructuredChatAccountCommand(session, "/account show")
	fallback := accountScreenFallbackResult(AccountScreenRequest{Report: report})
	if fallback.Screen != nil {
		t.Fatal("the fallback must not request a screen")
	}
	if got, want := chatAccountCommandText(t, fallback), chatAccountCommandText(t, plain); got != want {
		t.Fatalf("fallback drifted from the plain projection:\nfallback=%q\nplain=%q", got, want)
	}

	list := chatAccountListReport{Total: 1, WithAccount: 1, Providers: []chatAccountReport{report}}
	listFallback := accountsScreenFallbackResult(AccountListScreenRequest{List: list})
	if listFallback.Screen != nil {
		t.Fatal("the all-accounts fallback must not request a screen")
	}
	if got, want := chatAccountCommandText(t, listFallback), strings.Join(formatChatAccountListLines(list), "\n"); got != want {
		t.Fatalf("all-accounts fallback drifted:\ngot=%q\nwant=%q", got, want)
	}
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}

// TestChatAccountsScreenOptionsCarryRefreshKey 覆盖 /accounts 屏的刷新接线：可开屏
// 路径由 chatAccountsScreenOptions 携带刷新回调与页脚提示（开屏第一帧与按 r 之后
// 的帧同源）；测试夹具没有全屏 TTY，Spec 必须 fail-closed 降级（ForceInline 且清空
// 查看器 chrome），但保留打开那一刻冻结的请求参数（--enabled-only）。
func TestChatAccountsScreenOptionsCarryRefreshKey(t *testing.T) {
	session, _ := newUnifiedChatAccountSession(t)
	// beta 置为未启用：--enabled-only 的冻结参数只有作用于真实 provider 集合
	// 才能在屏内快照上被观测到（alpha 保持启用）。
	beta := session.Config.Providers.Items["beta"]
	beta.Enabled = false
	session.Config.Providers.Items["beta"] = beta

	// 接线源头：屏内刷新回调与页脚提示必须由 options 携带（Spec 在可开屏时
	// 原样复制它们；测试环境不可开屏，故在源头断言）。
	options := chatAccountsScreenOptions(session, AccountListScreenRequest{})
	if options.Refresh == nil {
		t.Fatal("全部账户屏必须携带屏内刷新回调")
	}
	if options.RefreshHint != accountsScreenRefreshHint {
		t.Fatalf("刷新提示 = %q want %q", options.RefreshHint, accountsScreenRefreshHint)
	}
	if !strings.Contains(options.Title, accountsScreenTitle) {
		t.Fatalf("屏标题 = %q", options.Title)
	}

	result, handled, err := tryExecuteStructuredChatCommand(session, "/accounts display")
	if err != nil || !handled || result.Screen == nil || result.Screen.ID != "accounts.screen" {
		t.Fatalf("/accounts display handled=%v err=%v result=%+v", handled, err, result.Screen)
	}
	spec := *result.Screen
	// 无全屏 TTY：fail-closed 降级，查看器 chrome（刷新回调/页脚提示）必须清空。
	if !spec.ForceInline || spec.ForceInlineReason != "unavailable" {
		t.Fatalf("降级 Spec 必须 ForceInline(unavailable): %+v", spec)
	}
	if spec.Refresh != nil || spec.RefreshHint != "" {
		t.Fatalf("降级 Spec 不得携带查看器刷新 chrome: refresh=%v hint=%q", spec.Refresh != nil, spec.RefreshHint)
	}
	if !strings.Contains(spec.Title, accountsScreenTitle) {
		t.Fatalf("屏标题 = %q", spec.Title)
	}
	body := ui.RenderDocumentPlain(spec.Doc)
	if containsLine(strings.Split(body, "\n"), screenViewerFooter) {
		t.Fatalf("降级文档不得携带查看器页脚:\n%s", body)
	}
	if !strings.Contains(body, "beta") {
		t.Fatal("display 未带 --enabled-only，冻结快照必须包含未启用的 provider")
	}

	// 子命令必须排在标志之前（/accounts --enabled-only display 会被解析拒绝）。
	result, handled, err = tryExecuteStructuredChatCommand(session, "/accounts display --enabled-only")
	if err != nil || !handled || result.Screen == nil || result.Screen.ID != "accounts.screen" {
		t.Fatalf("/accounts display --enabled-only handled=%v err=%v result=%+v", handled, err, result.Screen)
	}
	frozen := ui.RenderDocumentPlain(result.Screen.Doc)
	if strings.Contains(frozen, "beta") {
		t.Fatalf("屏内刷新必须复用打开屏幕时的 --enabled-only 过滤:\n%s", frozen)
	}
	if !strings.Contains(frozen, "alpha") {
		t.Fatalf("--enabled-only 不得吞掉启用中的 provider:\n%s", frozen)
	}
}

// TestChatAccountsScreenRefresherSubmitsAndReprojects 覆盖屏内 r 的完整时序：
// 按 r 提交（或复用）后台刷新并立刻重投影出「后台刷新中」；任务完成后节拍重投影
// 自动翻到「已刷新」并带上新余额；没有任何变化时报告 changed=false。
func TestChatAccountsScreenRefresherSubmitsAndReprojects(t *testing.T) {
	session := chatAccountTestSession()
	release := make(chan struct{})
	entered := make(chan struct{}, 8)
	var calls atomic.Int32
	session.accountListRefresh = blockingChatAccountsRefresh(&calls, entered, release, 88.5)

	refresher := newChatAccountsScreenRefresher(session, AccountListScreenRequest{
		Refresh: chatAccountRefreshParams{Timeout: 3 * time.Second},
	})

	title, body := refresher.current()
	if !strings.Contains(title, accountsScreenTitle) {
		t.Fatalf("第一帧标题 = %q", title)
	}
	if !strings.Contains(body, "缓存快照") {
		t.Fatalf("第一帧状态行必须是缓存快照:\n%s", body)
	}

	if _, _, changed := refresher.refresh(ui.DebugOverlayRefreshTick); changed {
		t.Fatal("空闲节拍不得重绘（内容没有变化）")
	}

	_, body, changed := refresher.refresh(ui.DebugOverlayRefreshKey)
	if !changed {
		t.Fatal("按 r 必须重绘一帧")
	}
	if !strings.Contains(body, "后台刷新中") {
		t.Fatalf("按 r 后状态行 = %s", body)
	}
	job := chatAccountsCurrentJob(session)
	if job == nil {
		t.Fatal("按 r 必须提交后台刷新")
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("后台任务未启动")
	}

	// 连按 r 复用同一个在飞任务（单飞），不会把上游打爆。
	_, _, _ = refresher.refresh(ui.DebugOverlayRefreshKey)
	if current := chatAccountsCurrentJob(session); current != job {
		t.Fatalf("连按 r 必须复用在飞任务: %p != %p", current, job)
	}

	releaseChatAccountsRefresh(t, session, release, job)

	_, body, changed = refresher.refresh(ui.DebugOverlayRefreshTick)
	if !changed {
		t.Fatal("任务完成后节拍必须重绘")
	}
	if !strings.Contains(body, "已刷新") || !strings.Contains(body, "88.50") {
		t.Fatalf("任务完成后的正文 = %s", body)
	}
	if calls.Load() != 2 {
		t.Fatalf("刷新调用次数 = %d，want 2（alpha + beta）", calls.Load())
	}
}

// TestChatAccountsScreenRefresherFreezesProviderItems 覆盖冻结快照契约：屏内重投影
// 不得读取可变的 Providers map，否则备用屏会重新暴露「渲染循环读活会话状态」的
// 竞态（这是两块账户屏当初把正文捕获到屏外的原因）。
func TestChatAccountsScreenRefresherFreezesProviderItems(t *testing.T) {
	session := chatAccountTestSession()
	refresher := newChatAccountsScreenRefresher(session, AccountListScreenRequest{})

	_, before := refresher.current()
	if !strings.Contains(before, "alpha") || strings.Contains(before, "gamma") {
		t.Fatalf("第一帧应是开屏时的 provider 集合:\n%s", before)
	}

	// 开屏之后 config 变化（配置重载/新 provider 上线）不得进入屏内重投影。
	session.Config.Providers.Items["gamma"] = config.Provider{
		Enabled: true, Protocol: "openai", BaseURL: "https://gamma.test",
	}
	_, after, _ := refresher.refresh(ui.DebugOverlayRefreshTick)
	if strings.Contains(after, "gamma") {
		t.Fatalf("屏内重投影读取了可变的 Providers map:\n%s", after)
	}
	if !strings.Contains(after, "alpha") {
		t.Fatalf("冻结的 provider 行丢失:\n%s", after)
	}
}

// TestChatAccountsScreenRefresherReportsSubmitFailure 覆盖提交失败：只改状态行，
// 缓存表格照常渲染，且不留下任务句柄。
func TestChatAccountsScreenRefresherReportsSubmitFailure(t *testing.T) {
	session := chatAccountTestSession()
	for name, provider := range session.Config.Providers.Items {
		provider.Enabled = false
		session.Config.Providers.Items[name] = provider
	}
	session.accountListRefresh = func(
		_ context.Context,
		_ *siteaccount.Client,
		_ string,
		_ *config.Provider,
		_ time.Duration,
	) (liveBalanceOutcome, error) {
		t.Fatal("提交失败时不得发起刷新")
		return liveBalanceOutcome{}, nil
	}

	refresher := newChatAccountsScreenRefresher(session, AccountListScreenRequest{
		Refresh: chatAccountRefreshParams{EnabledOnly: true},
	})
	_, body, changed := refresher.refresh(ui.DebugOverlayRefreshKey)
	if !changed {
		t.Fatal("提交失败也要重绘状态行")
	}
	if !strings.Contains(body, accountsScreenSubmitFailedState) {
		t.Fatalf("失败状态行缺失:\n%s", body)
	}
	if !strings.Contains(body, "没有可刷新的 provider") {
		t.Fatalf("失败原因缺失:\n%s", body)
	}
	if chatAccountsCurrentJob(session) != nil {
		t.Fatal("提交失败不得留下任务句柄")
	}
	if strings.Contains(body, "alpha") {
		t.Fatalf("--enabled-only 过滤同样作用于屏内表格:\n%s", body)
	}
}
