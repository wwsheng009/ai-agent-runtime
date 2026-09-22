package commands

// /accounts 异步化回归（见 chat_account_async.go）：
//   - 默认路径只提交后台刷新并立刻渲染缓存快照，绝不等待网络；
//   - refresh 只提交、display 零网络、--wait 才是旧的阻塞语义；
//   - 同一会话同时只允许一个在飞任务，重复提交复用同一个 job；
//   - 会话退出时取消在飞任务，且取消的任务不发布半成品快照；
//   - 后台结果写入会话缓存，当前生效 provider 走 /model 同款发布路径。

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/siteaccount"
)

// chatAccountsTestOutcome 构造一次成功的余额结果，value 为剩余额度。
func chatAccountsTestOutcome(value float64) liveBalanceOutcome {
	remaining := value
	return liveBalanceOutcome{
		Status:   "ok",
		SiteType: string(siteaccount.SiteTypeSub2API),
		Account: &config.ProviderAccountSnapshot{
			Source:           string(siteaccount.SiteTypeSub2API),
			Mode:             "subscription",
			Currency:         "USD",
			QuotaRemaining:   &remaining,
			QuotaDisplayUnit: "USD",
			FetchedAt:        time.Now().UTC().Format(time.RFC3339),
		},
	}
}

// blockingChatAccountsRefresh 返回一个在 release 关闭前一直阻塞的假刷新函数：
// 用它证明命令返回与后台网络 I/O 已经解耦。
func blockingChatAccountsRefresh(
	calls *atomic.Int32,
	entered chan<- struct{},
	release <-chan struct{},
	value float64,
) chatAccountBalanceRefreshFunc {
	return func(
		_ context.Context,
		_ *siteaccount.Client,
		_ string,
		_ *config.Provider,
		_ time.Duration,
	) (liveBalanceOutcome, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return chatAccountsTestOutcome(value), nil
	}
}

func chatAccountsMustParse(t *testing.T, command string) chatAccountCommandRequest {
	t.Helper()
	req, errText := parseChatAccountCommand(command)
	if errText != "" {
		t.Fatalf("parse %q: %s", command, errText)
	}
	return req
}

func chatAccountsCurrentJob(session *ChatSession) *chatAccountsRefreshJob {
	session.accountListMu.Lock()
	defer session.accountListMu.Unlock()
	return session.accountListJob
}

func chatAccountsRemaining(provider config.Provider) float64 {
	if provider.Account == nil || provider.Account.QuotaRemaining == nil {
		return -1
	}
	return *provider.Account.QuotaRemaining
}

// releaseChatAccountsRefresh 关闭 release 通道并等待后台任务结束，避免测试泄漏
// goroutine；重复调用是幂等的。
func releaseChatAccountsRefresh(t *testing.T, session *ChatSession, release chan struct{}, job *chatAccountsRefreshJob) {
	t.Helper()
	select {
	case <-release:
	default:
		close(release)
	}
	if !job.wait(5 * time.Second) {
		t.Fatal("后台刷新未在预期时间内结束")
	}
	if current := chatAccountsCurrentJob(session); current != job {
		t.Fatalf("会话内任务句柄被替换: %p != %p", current, job)
	}
}

func TestChatAccountsRefreshSubmitsWithoutWaitingForNetwork(t *testing.T) {
	session := chatAccountTestSession()
	release := make(chan struct{})
	entered := make(chan struct{}, 8)
	var calls atomic.Int32
	session.accountListRefresh = blockingChatAccountsRefresh(&calls, entered, release, 77.5)

	start := time.Now()
	result := chatAccountsResult(session, chatAccountsMustParse(t, "/accounts refresh"))
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("提交后台刷新必须立刻返回，实际耗时 %s", elapsed)
	}

	text := chatAccountCommandText(t, result)
	if !strings.Contains(text, "已提交") {
		t.Fatalf("提交确认文本 = %q", text)
	}
	if strings.Contains(text, "77.50") {
		t.Fatalf("提交阶段不应包含本次刷新结果: %q", text)
	}
	if result.OpenAccountsScreen != nil {
		t.Fatal("/accounts refresh 不打开账户屏（没有新数据可看）")
	}

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("后台任务未启动")
	}
	if cached, _ := session.accountListSnapshot(); len(cached) != 0 {
		t.Fatalf("完成前不应发布快照: %+v", cached)
	}

	job := chatAccountsCurrentJob(session)
	if job == nil {
		t.Fatal("会话内没有在飞任务句柄")
	}
	releaseChatAccountsRefresh(t, session, release, job)

	if !job.wait(0) {
		t.Fatal("任务应已结束")
	}
	state := job.snapshotState()
	if state.Running || state.Total != 2 || state.OK != 2 || state.Failed != 0 {
		t.Fatalf("任务终态 = %+v", state)
	}
	if calls.Load() != 2 {
		t.Fatalf("刷新调用次数 = %d，want 2（alpha + beta）", calls.Load())
	}

	cached, _ := session.accountListSnapshot()
	if len(cached) != 2 || chatAccountsRemaining(cached["alpha"]) != 77.5 {
		t.Fatalf("缓存快照 = %+v", cached)
	}
	name, live, ok := session.accountBalanceSnapshot()
	if !ok || name != "alpha" || chatAccountsRemaining(live) != 77.5 {
		t.Fatalf("当前 provider 实时快照 = %q/%+v/%v", name, live.Account, ok)
	}
}

func TestChatAccountsDisplayOnlyReadsCache(t *testing.T) {
	session := chatAccountTestSession()
	var calls atomic.Int32
	session.accountListRefresh = func(
		_ context.Context,
		_ *siteaccount.Client,
		_ string,
		_ *config.Provider,
		_ time.Duration,
	) (liveBalanceOutcome, error) {
		calls.Add(1)
		return chatAccountsTestOutcome(1), nil
	}

	cached := session.Config.Providers.Items["alpha"]
	cached.Account = chatAccountsTestOutcome(5.25).Account
	session.accountListMu.Lock()
	session.accountListProviders = map[string]config.Provider{"alpha": cached}
	session.accountListUpdatedAt = time.Now()
	session.accountListMu.Unlock()

	result := chatAccountsResult(session, chatAccountsMustParse(t, "/accounts display"))
	if calls.Load() != 0 {
		t.Fatalf("display 不得发起刷新，调用次数 = %d", calls.Load())
	}
	text := chatAccountCommandText(t, result)
	if !strings.Contains(text, "5.25 USD") {
		t.Fatalf("display 未渲染缓存余额: %q", text)
	}
	if !strings.Contains(text, "刷新: 缓存快照") {
		t.Fatalf("display 状态行 = %q", text)
	}
	if chatAccountsCurrentJob(session) != nil {
		t.Fatal("display 不得创建后台任务")
	}
}

func TestChatAccountsDefaultSubmitsAndRendersCacheImmediately(t *testing.T) {
	session := chatAccountTestSession()
	release := make(chan struct{})
	entered := make(chan struct{}, 8)
	var calls atomic.Int32
	session.accountListRefresh = blockingChatAccountsRefresh(&calls, entered, release, 12.0)

	start := time.Now()
	result := chatAccountsResult(session, chatAccountsMustParse(t, "/accounts"))
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("/accounts 默认路径必须立刻返回，实际耗时 %s", elapsed)
	}
	text := chatAccountCommandText(t, result)
	if !strings.Contains(text, "刷新: 后台刷新中") {
		t.Fatalf("状态行应显示后台刷新中: %q", text)
	}
	// 提交那一刻还没有新结果，只能渲染 config / 实时快照里的 12.34。
	if !strings.Contains(text, "12.34 USD") {
		t.Fatalf("默认路径应立刻渲染缓存快照: %q", text)
	}

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("后台任务未启动")
	}
	job := chatAccountsCurrentJob(session)
	releaseChatAccountsRefresh(t, session, release, job)

	after := chatAccountCommandText(t, chatAccountsResult(session, chatAccountsMustParse(t, "/accounts display")))
	if !strings.Contains(after, "刷新: 已刷新") {
		t.Fatalf("刷新完成后状态行 = %q", after)
	}
	if !strings.Contains(after, "12.00 USD") {
		t.Fatalf("刷新完成后应渲染新快照: %q", after)
	}
}

func TestChatAccountsWaitKeepsBlockingSemantics(t *testing.T) {
	session := chatAccountTestSession()
	var calls atomic.Int32
	session.accountListRefresh = func(
		_ context.Context,
		_ *siteaccount.Client,
		_ string,
		_ *config.Provider,
		_ time.Duration,
	) (liveBalanceOutcome, error) {
		calls.Add(1)
		return chatAccountsTestOutcome(7.5), nil
	}

	result := chatAccountsResult(session, chatAccountsMustParse(t, "/accounts --wait"))
	if calls.Load() != 2 {
		t.Fatalf("--wait 应逐个 provider 拉取，调用次数 = %d", calls.Load())
	}
	text := chatAccountCommandText(t, result)
	if !strings.Contains(text, "刷新: 已刷新") || !strings.Contains(text, "成功 2，失败 0") {
		t.Fatalf("--wait 状态行 = %q", text)
	}
	if !strings.Contains(text, "7.50 USD") {
		t.Fatalf("--wait 应渲染本次结果: %q", text)
	}
	// 阻塞路径的结果同样进缓存，紧接着的 display 不会回退到旧快照。
	cached, _ := session.accountListSnapshot()
	if len(cached) != 2 || chatAccountsRemaining(cached["alpha"]) != 7.5 {
		t.Fatalf("阻塞路径未发布结果: %+v", cached)
	}
}

func TestChatAccountsRefreshReusesInFlightJob(t *testing.T) {
	session := chatAccountTestSession()
	release := make(chan struct{})
	entered := make(chan struct{}, 8)
	var calls atomic.Int32
	session.accountListRefresh = blockingChatAccountsRefresh(&calls, entered, release, 3.0)

	req := chatAccountsMustParse(t, "/accounts refresh")
	first := chatAccountsResult(session, req)
	if !strings.Contains(chatAccountCommandText(t, first), "已提交") {
		t.Fatalf("首次提交文本 = %q", chatAccountCommandText(t, first))
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("后台任务未启动")
	}

	second := chatAccountsResult(session, req)
	if text := chatAccountCommandText(t, second); !strings.Contains(text, "刷新已在进行中") {
		t.Fatalf("重复提交应复用任务: %q", text)
	}
	if calls.Load() != 1 {
		t.Fatalf("重复提交不得重复拉取，调用次数 = %d", calls.Load())
	}
	job := chatAccountsCurrentJob(session)
	if job == nil {
		t.Fatal("缺少在飞任务句柄")
	}
	releaseChatAccountsRefresh(t, session, release, job)
	if calls.Load() != 2 {
		t.Fatalf("复用的任务应覆盖全部 provider，调用次数 = %d", calls.Load())
	}
}

func TestStopChatAccountsRefreshCancelsWithoutPublishing(t *testing.T) {
	session := chatAccountTestSession()
	release := make(chan struct{})
	entered := make(chan struct{}, 8)
	var calls atomic.Int32
	session.accountListRefresh = blockingChatAccountsRefresh(&calls, entered, release, 9.0)

	job, started, errText := submitChatAccountsRefresh(session, chatAccountsMustParse(t, "/accounts refresh"))
	if errText != "" || !started {
		t.Fatalf("submit = %v/%v/%q", job, started, errText)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("后台任务未启动")
	}

	stopChatAccountsRefresh(session)
	close(release)
	if !job.wait(5 * time.Second) {
		t.Fatal("取消后任务未及时结束")
	}
	if state := job.snapshotState(); state.Running {
		t.Fatalf("取消后任务仍在运行: %+v", state)
	}
	cached, _ := session.accountListSnapshot()
	if len(cached) != 0 {
		t.Fatalf("取消的任务不得发布结果: %+v", cached)
	}
	if session.accountListJob != nil {
		t.Fatal("stopChatAccountsRefresh 应清空任务句柄")
	}
}

func TestChatAccountsNonInteractiveKeepsBlockingSemantics(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ChatSession)
	}{
		{name: "no-interactive", mutate: func(session *ChatSession) { session.NoInteractive = true }},
		{name: "json output", mutate: func(session *ChatSession) { session.JSONOutput = true }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			session := chatAccountTestSession()
			test.mutate(session)
			var calls atomic.Int32
			session.accountListRefresh = func(
				_ context.Context,
				_ *siteaccount.Client,
				_ string,
				_ *config.Provider,
				_ time.Duration,
			) (liveBalanceOutcome, error) {
				calls.Add(1)
				return chatAccountsTestOutcome(4.5), nil
			}

			result := chatAccountsResult(session, chatAccountsMustParse(t, "/accounts"))
			if calls.Load() != 2 {
				t.Fatalf("非交互路径必须同步拉取完整数据，调用次数 = %d", calls.Load())
			}
			text := chatAccountCommandText(t, result)
			if !strings.Contains(text, "刷新: 已刷新") || !strings.Contains(text, "4.50 USD") {
				t.Fatalf("非交互输出 = %q", text)
			}
			if chatAccountsCurrentJob(session) != nil {
				t.Fatal("非交互路径不应留下后台任务")
			}
		})
	}
}

func TestChatAccountsRefreshRejectsEmptyTargets(t *testing.T) {
	session := chatAccountTestSession()
	for name, provider := range session.Config.Providers.Items {
		provider.Enabled = false
		session.Config.Providers.Items[name] = provider
	}
	result := chatAccountsResult(session, chatAccountsMustParse(t, "/accounts --enabled-only"))
	if text := chatAccountCommandText(t, result); !strings.Contains(text, "没有可刷新的 provider") {
		t.Fatalf("空目标错误文本 = %q", text)
	}
	if chatAccountsCurrentJob(session) != nil {
		t.Fatal("空目标不得留下任务句柄")
	}

	empty := &ChatSession{Config: &config.Config{}}
	noProviders := chatAccountsResult(empty, chatAccountsMustParse(t, "/accounts refresh"))
	if text := chatAccountCommandText(t, noProviders); !strings.Contains(text, "没有可用的 provider") {
		t.Fatalf("无配置错误文本 = %q", text)
	}
}

func TestChatAccountsJSONKeepsWaitSemantics(t *testing.T) {
	req := chatAccountsMustParse(t, "/accounts --json")
	if !req.JSON || !req.Wait || req.Mode != chatAccountModeList {
		t.Fatalf("--json 默认应等待刷新: %+v", req)
	}

	req = chatAccountsMustParse(t, "/accounts display --json")
	if !req.JSON || req.Wait || req.Mode != chatAccountModeListDisplay {
		t.Fatalf("display --json 只序列化缓存: %+v", req)
	}

	req = chatAccountsMustParse(t, "/accounts --no-refresh --json")
	if req.Wait || req.Mode != chatAccountModeListDisplay {
		t.Fatalf("--no-refresh --json 不得隐式等待: %+v", req)
	}
}

func TestChatAccountsRefreshStateLabel(t *testing.T) {
	now := time.Now()

	label, detail := chatAccountsRefreshStateLabel(chatAccountsRefreshState{Running: true, SubmittedAt: now.Add(-3 * time.Second), Total: 2}, now)
	if label != "后台刷新中" || !strings.Contains(detail, "2 provider") {
		t.Fatalf("运行中标签 = %q/%q", label, detail)
	}

	long := strings.Repeat("x", 120)
	label, detail = chatAccountsRefreshStateLabel(chatAccountsRefreshState{
		FinishedAt: now,
		OK:         1,
		Failed:     2,
		Warnings:   []string{long},
	}, now)
	if label != "已刷新" || !strings.Contains(detail, "成功 1，失败 2") {
		t.Fatalf("完成标签 = %q/%q", label, detail)
	}
	if !strings.Contains(detail, "…") || len(detail) > 160 {
		t.Fatalf("告警应被截断: %q", detail)
	}

	label, detail = chatAccountsRefreshStateLabel(chatAccountsRefreshState{}, now)
	if label != "缓存快照" || !strings.Contains(detail, "尚未提交后台刷新") {
		t.Fatalf("空状态标签 = %q/%q", label, detail)
	}
}
