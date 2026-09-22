package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/siteaccount"
)

// ============================================================================
// /accounts 的后台刷新（async submit）
//
// `/accounts` 的历史语义是「逐个 provider 实时拉取后再渲染」：N 个 provider 串行
// 叠加，最多阻塞 N × --timeout（默认 15s 一个），在 TUI 里表现为整条命令长时间卡住
// 不返回，期间用户既看不到快照也无法输入。
//
// 现在的分工（解析见 parseChatAccountsCommandArgs）：
//
//	/accounts              → 提交后台刷新 + 立刻渲染缓存快照（不阻塞）
//	/accounts refresh      → 只提交后台刷新，返回一行确认
//	/accounts display      → 只渲染缓存快照（零网络；--no-refresh 等价写法）
//	/accounts --wait       → 旧的阻塞语义：拉完再渲染
//	/accounts --json       → 隐含 --wait（脚本要的是完整数据，不是任务句柄）；
//	                         display/--no-refresh 时不隐含，只序列化缓存快照
//	非交互 / JSON 会话     → 没有「稍后再 display」的机会，缺省即按 --wait 处理
//
// 任务状态（进行中/上次完成时间/成功失败数）与结果快照都挂在会话上，受
// accountListMu 保护；后台 goroutine 只写会话内快照，绝不写 config.yaml —— 写盘仍然
// 只有 `/account refresh --save` 一条路径。
// ============================================================================

// chatAccountsNoRefreshTargetsError 是 --enabled-only 过滤后没有目标的统一错误
// 文本（异步提交与阻塞路径共用，避免两条路径漂移）。
const chatAccountsNoRefreshTargetsError = "错误: 没有可刷新的 provider（--enabled-only 过滤后为空）"

// chatAccountsRefreshTarget 是提交时刻冻结的刷新目标：provider 值副本在提交那一刻
// 就从 config 拷出，后台 goroutine 因此完全不读取可变的 Providers map。
type chatAccountsRefreshTarget struct {
	Name     string
	Provider config.Provider
}

// chatAccountsRefreshState 是 /accounts 刷新状态的可渲染投影（任务句柄与会话缓存
// 共用）。
type chatAccountsRefreshState struct {
	Running     bool
	SubmittedAt time.Time
	FinishedAt  time.Time
	Total       int
	OK          int
	Failed      int
	Warnings    []string
}

// chatAccountsRefreshJob 是一次「全部 provider 后台刷新」。同一会话同时只允许一个
// 在飞任务：重复提交复用同一个 job，避免用户连按时把上游打爆。
type chatAccountsRefreshJob struct {
	session *ChatSession
	targets []chatAccountsRefreshTarget
	refresh chatAccountBalanceRefreshFunc
	client  *siteaccount.Client
	timeout time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	state    chatAccountsRefreshState
	results  map[string]config.Provider
	canceled bool
}

func newChatAccountsRefreshJob(
	session *ChatSession,
	targets []chatAccountsRefreshTarget,
	timeout time.Duration,
	refresh chatAccountBalanceRefreshFunc,
) *chatAccountsRefreshJob {
	if timeout <= 0 {
		timeout = chatAccountBalanceRefreshTimeout
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &chatAccountsRefreshJob{
		session: session,
		targets: targets,
		refresh: refresh,
		client:  siteaccount.NewClient(nil),
		timeout: timeout,
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
		results: make(map[string]config.Provider, len(targets)),
		state: chatAccountsRefreshState{
			Running:     true,
			SubmittedAt: time.Now(),
			Total:       len(targets),
		},
	}
}

// submitChatAccountsRefresh 提交（或复用）一次全部 provider 的后台刷新，返回任务
// 句柄、本次是否为新建任务，以及错误文本。
func submitChatAccountsRefresh(
	session *ChatSession,
	req chatAccountCommandRequest,
) (*chatAccountsRefreshJob, bool, string) {
	if session == nil {
		return nil, false, "错误: 当前没有活动会话"
	}
	if session.Config == nil || len(session.Config.Providers.Items) == 0 {
		return nil, false, "错误: 当前配置没有可用的 provider"
	}

	targets := chatAccountsRefreshTargets(session, req.EnabledOnly)
	if len(targets) == 0 {
		return nil, false, chatAccountsNoRefreshTargetsError
	}

	session.accountListMu.Lock()
	defer session.accountListMu.Unlock()
	if job := session.accountListJob; job != nil && job.isRunning() {
		return job, false, ""
	}
	job := newChatAccountsRefreshJob(session, targets, req.Timeout, session.accountListRefresh)
	session.accountListJob = job
	go job.run()
	return job, true, ""
}

// chatAccountsRefreshTargets 在提交时刻冻结刷新目标：provider 副本按名字排序，当前
// 生效 provider 优先采用会话内更新的实时快照（与旧的阻塞实现保持一致）。
func chatAccountsRefreshTargets(session *ChatSession, enabledOnly bool) []chatAccountsRefreshTarget {
	if session == nil || session.Config == nil {
		return nil
	}
	names := make([]string, 0, len(session.Config.Providers.Items))
	for name := range session.Config.Providers.Items {
		names = append(names, name)
	}
	sort.Strings(names)

	snapshotName, live, hasLive := session.accountBalanceSnapshot()
	targets := make([]chatAccountsRefreshTarget, 0, len(names))
	for _, name := range names {
		provider := session.Config.Providers.Items[name]
		if enabledOnly && !provider.Enabled {
			continue
		}
		if hasLive && strings.EqualFold(strings.TrimSpace(snapshotName), name) {
			provider = live
		}
		targets = append(targets, chatAccountsRefreshTarget{Name: name, Provider: provider})
	}
	return targets
}

func (j *chatAccountsRefreshJob) run() {
	defer close(j.done)

	refresh := j.refresh
	if refresh == nil {
		refresh = refreshProviderAccountBalance
	}

	results := make(map[string]config.Provider, len(j.targets))
	warnings := make([]string, 0)
	ok, failed := 0, 0
	for _, target := range j.targets {
		if j.ctx.Err() != nil {
			break
		}
		provider := target.Provider
		outcome, err := refresh(j.ctx, j.client, target.Name, &provider, j.timeout)
		// 站点类型探测元数据即使本次取余额失败也写回快照，避免下一个周期对同一个
		// 上游重复探测（与周期刷新器的 cacheDetectedSiteType 同语义）。
		applyDetectedSiteTypeOutcome(&provider, outcome)
		if err != nil {
			failed++
			warnings = append(warnings, fmt.Sprintf("%s: %s", target.Name, err.Error()))
			if outcome.Account != nil {
				applyLiveBalanceOutcome(&provider, outcome)
			}
			results[target.Name] = provider
			continue
		}
		applyLiveBalanceOutcome(&provider, outcome)
		ok++
		results[target.Name] = provider
	}

	// 会话退出时任务被 stopChatAccountsRefresh 取消：结果不再发布，避免往一个正在
	// 回收的会话里写状态。
	canceled := j.ctx.Err() != nil

	j.mu.Lock()
	j.state.Running = false
	j.state.FinishedAt = time.Now()
	j.state.OK = ok
	j.state.Failed = failed
	j.state.Warnings = warnings
	j.canceled = canceled
	j.results = results
	j.mu.Unlock()

	if !canceled {
		j.session.publishChatAccountsRefreshResults(results)
	}
}

// publishChatAccountsRefreshResults 把后台结果写入会话快照：全部结果进 /accounts
// 缓存；当前生效 provider 的结果走 /model 同款发布路径（状态行 + 周期刷新目标）。
func (s *ChatSession) publishChatAccountsRefreshResults(results map[string]config.Provider) {
	if s == nil || len(results) == 0 {
		return
	}
	s.accountListMu.Lock()
	if s.accountListProviders == nil {
		s.accountListProviders = make(map[string]config.Provider, len(results))
	}
	for name, provider := range results {
		s.accountListProviders[name] = provider
	}
	s.accountListUpdatedAt = time.Now()
	s.accountListMu.Unlock()

	providerName := strings.TrimSpace(s.ProviderName)
	for name, provider := range results {
		if !strings.EqualFold(strings.TrimSpace(name), providerName) {
			continue
		}
		applyChatAccountProviderSnapshot(s, name, provider, true)
		break
	}
}

// accountListSnapshot 返回 /accounts 渲染所需的缓存副本与刷新状态；没有任何网络或
// 会话内其它可变状态的读取。
func (s *ChatSession) accountListSnapshot() (map[string]config.Provider, chatAccountsRefreshState) {
	if s == nil {
		return nil, chatAccountsRefreshState{}
	}
	s.accountListMu.Lock()
	providers := make(map[string]config.Provider, len(s.accountListProviders))
	for name, provider := range s.accountListProviders {
		providers[name] = provider
	}
	job := s.accountListJob
	s.accountListMu.Unlock()
	return providers, job.snapshotState()
}

// stopChatAccountsRefresh 取消在飞的后台刷新（会话退出时调用）。已完成的任务上
// cancel 是幂等的空操作。
func stopChatAccountsRefresh(session *ChatSession) {
	if session == nil {
		return
	}
	session.accountListMu.Lock()
	job := session.accountListJob
	session.accountListJob = nil
	session.accountListMu.Unlock()
	if job != nil {
		job.cancel()
	}
}

func (j *chatAccountsRefreshJob) isRunning() bool {
	if j == nil {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state.Running
}

func (j *chatAccountsRefreshJob) snapshotState() chatAccountsRefreshState {
	if j == nil {
		return chatAccountsRefreshState{}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	state := j.state
	state.Warnings = append([]string(nil), j.state.Warnings...)
	return state
}

// wait 阻塞等待任务结束（测试与需要确定性时序的调用方使用）。
func (j *chatAccountsRefreshJob) wait(timeout time.Duration) bool {
	if j == nil {
		return true
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-j.done:
		return true
	case <-timer.C:
		return false
	}
}

// applyDetectedSiteTypeOutcome 把探测到的站点类型写回 provider 副本，即使本次取余额
// 失败也保留，避免下一轮重复探测同一个上游。
func applyDetectedSiteTypeOutcome(provider *config.Provider, outcome liveBalanceOutcome) {
	if provider == nil {
		return
	}
	if outcome.SiteType != "" {
		provider.SiteType = outcome.SiteType
	}
	if outcome.SiteTypeConfidence != "" {
		provider.SiteTypeConfidence = outcome.SiteTypeConfidence
	}
	if outcome.SiteTypeDetectedAt != "" {
		provider.SiteTypeDetectedAt = outcome.SiteTypeDetectedAt
	}
}

// chatAccountsRefreshStateLabel 把任务状态翻译为「标签 + 说明」两段可渲染文本。
func chatAccountsRefreshStateLabel(state chatAccountsRefreshState, now time.Time) (string, string) {
	switch {
	case state.Running:
		detail := "已运行 " + formatChatAccountsDuration(now.Sub(state.SubmittedAt))
		if state.Total > 0 {
			detail += fmt.Sprintf("，%d provider", state.Total)
		}
		return "后台刷新中", detail + "；完成后用 /accounts display 查看"
	case !state.FinishedAt.IsZero():
		detail := fmt.Sprintf("%s 完成，成功 %d，失败 %d",
			state.FinishedAt.Format("15:04:05"), state.OK, state.Failed)
		if len(state.Warnings) > 0 {
			detail += "；" + truncateChatAccountsWarning(state.Warnings[0])
		}
		return "已刷新", detail
	default:
		return "缓存快照", "尚未提交后台刷新（/accounts refresh 提交，/accounts --wait 阻塞等待）"
	}
}

func formatChatAccountsDuration(d time.Duration) string {
	if d < time.Second {
		return "<1s"
	}
	return d.Round(time.Second).String()
}

func truncateChatAccountsWarning(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	const limit = 80
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
