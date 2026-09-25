package commands

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestStreamOlderResumeHistoryPagesVisitsEachPageAsItArrives 固化「边读取、边渲染」
// 的读取半程：更早的历史必须一页一页地交给 visit（较新页 → 较早页），而不是先
// 全部收集进内存再返回。收集式实现会让调用方在整场会话读盘期间无内容可画。
func TestStreamOlderResumeHistoryPagesVisitsEachPageAsItArrives(t *testing.T) {
	storage, err := runtimechat.NewSQLiteSessionStorage(runtimechat.DefaultPersistentSessionStorageConfig(t.TempDir()))
	require.NoError(t, err)
	manager := runtimechat.NewSessionManager(storage, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		MaxHistory:      0,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	defer manager.Stop()

	ctx := context.Background()
	session, err := manager.Create(ctx, "tester")
	require.NoError(t, err)

	const messageCount = 250 // 单页 HistoryPageMessages=100 → 最新页 + 2 个更早的页
	messages := make([]runtimetypes.Message, 0, messageCount)
	for index := 0; index < messageCount; index++ {
		messages = append(messages, *runtimetypes.NewUserMessage(fmt.Sprintf("user %d", index)))
	}
	session.ReplaceHistory(messages)
	require.NoError(t, storage.Save(ctx, session))

	chatSession := &ChatSession{SessionManager: manager, SessionUserID: "tester"}
	first, ok := loadNewestResumeHistoryPage(chatSession, session.ID)
	require.True(t, ok)
	require.True(t, first.HasMore, "%d 条消息应超过单页", messageCount)

	var visited [][]string
	var lastSeqs []int
	pages, err := streamOlderResumeHistoryPages(ctx, manager, session.ID, first.NextBeforeSeq,
		func(page *runtimechat.SessionHistoryPage) bool {
			contents := make([]string, 0, len(page.Messages))
			for _, message := range page.Messages {
				contents = append(contents, message.Content)
			}
			visited = append(visited, contents)
			lastSeqs = append(lastSeqs, page.LastSeq)
			return true
		})
	require.NoError(t, err)
	require.Equal(t, 2, pages)
	require.Len(t, visited, 2)
	require.Len(t, visited[0], 100)
	require.Len(t, visited[1], 50)
	require.Equal(t, "user 50", visited[0][0])
	require.Equal(t, "user 149", visited[0][len(visited[0])-1])
	require.Equal(t, "user 0", visited[1][0])
	require.Equal(t, "user 49", visited[1][len(visited[1])-1])
	// 游标严格往前推进：页序是「较新页 → 较早页」。
	require.Equal(t, []int{150, 50}, lastSeqs)

	// 兼容入口（一次性收集）必须给出与流式读取相同的页内容。
	collected := fetchOlderResumeHistoryPages(ctx, manager, session.ID, first.NextBeforeSeq)
	require.Len(t, collected, 2)
	require.Equal(t, visited[0], messageContentsForTest(collected[0]))
	require.Equal(t, visited[1], messageContentsForTest(collected[1]))
}

func messageContentsForTest(messages []runtimetypes.Message) []string {
	contents := make([]string, 0, len(messages))
	for _, message := range messages {
		contents = append(contents, message.Content)
	}
	return contents
}

// TestSeedPersistedHistoryPagePrependsOlderPagesInCanonicalOrder 固化增量装载的
// 渲染半程：更早的一页必须整体插入到已装载区域之前（页内仍是正序），并且内容与
// 最新页完全相同的另一条真实消息不得被「已表达」判定吞掉。
func TestSeedPersistedHistoryPagePrependsOlderPagesInCanonicalOrder(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge

	newest := []runtimetypes.Message{
		*runtimetypes.NewUserMessage("重复出现的内容"),
		*runtimetypes.NewAssistantMessage("最新一页的回复"),
	}
	bridge.seedPersistedHistory(newest, "")
	require.Equal(t, []string{"重复出现的内容", "最新一页的回复"}, encoderItemHeadsForTest(bridge))

	older := []runtimetypes.Message{
		*runtimetypes.NewUserMessage("重复出现的内容"),
		*runtimetypes.NewAssistantMessage("更早一页的回复"),
	}
	require.True(t, bridge.seedPersistedHistoryPage(older))
	require.Equal(t,
		[]string{"重复出现的内容", "更早一页的回复", "重复出现的内容", "最新一页的回复"},
		encoderItemHeadsForTest(bridge))

	// 幂等：同一页重复装载不得产生重复单元格，也不得改变顺序。
	require.False(t, bridge.seedPersistedHistoryPage(older))
	require.Equal(t,
		[]string{"重复出现的内容", "更早一页的回复", "重复出现的内容", "最新一页的回复"},
		encoderItemHeadsForTest(bridge))

	// 再补更早的一页：仍然插到最前面。
	oldest := []runtimetypes.Message{*runtimetypes.NewAssistantMessage("最早一页的回复")}
	require.True(t, bridge.seedPersistedHistoryPage(oldest))
	require.Equal(t,
		[]string{"最早一页的回复", "重复出现的内容", "更早一页的回复", "重复出现的内容", "最新一页的回复"},
		encoderItemHeadsForTest(bridge))

	// 装载收尾（以及 dispatch 的整场回放）会对完整 canonical 转录再做一次
	// reconcile：增量页已经画在屏幕上的内容必须被按序配对命中，不得重复导入。
	all := make([]runtimetypes.Message, 0, len(oldest)+len(older)+len(newest))
	all = append(all, oldest...)
	all = append(all, older...)
	all = append(all, newest...)
	bridge.seedPersistedHistoryForSessionLoad(all, "已加载历史会话")
	require.Equal(t,
		[]string{"最早一页的回复", "重复出现的内容", "更早一页的回复", "重复出现的内容", "最新一页的回复"},
		encoderItemHeadsForTest(bridge))
}

// encoderItemHeadsForTest 返回编码器模型里条目的渲染头部（调用方保证没有并发写）。
func encoderItemHeadsForTest(bridge *chatRuntimeEventBridge) []string {
	if bridge == nil || bridge.renderEncoder == nil {
		return nil
	}
	snapshot := bridge.renderEncoder.Snapshot()
	heads := make([]string, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		if item == nil {
			continue
		}
		heads = append(heads, item.Head)
	}
	return heads
}

// gatedHistoryPagerStorage 在第 gateCall 次分页读取上阻塞，让测试能确定性地观察
// 「一页读回就立即装载/绘制」的中间状态（而不是等全部页读完）。
type gatedHistoryPagerStorage struct {
	runtimechat.SessionStorage
	gateCall int
	reached  chan struct{}
	release  chan struct{}

	mu    sync.Mutex
	calls int
	once  sync.Once
}

func (s *gatedHistoryPagerStorage) GetMessagePage(
	ctx context.Context, sessionID string, beforeSeq, limit int,
) (*runtimechat.SessionHistoryPage, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if s.gateCall == call {
		s.once.Do(func() { close(s.reached) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	pager, ok := s.SessionStorage.(runtimechat.SessionStorageHistoryPager)
	if !ok {
		return nil, fmt.Errorf("inner storage does not support history paging")
	}
	return pager.GetMessagePage(ctx, sessionID, beforeSeq, limit)
}

// unifiedTranscriptContains 读取统一渲染 AppState 的语义转录（actor 保护下的
// 快照），供并发断言使用：不得在 Eventually 的条件里调用 t.Fatal 之类的方法。
func unifiedTranscriptContains(coordinator *chatInteractionCoordinator, needle string) bool {
	if coordinator == nil {
		return false
	}
	actor := coordinator.ensureUIActor()
	if actor == nil {
		return false
	}
	for _, cell := range actor.AppState().Transcript.Cells {
		// TranscriptCell 是值类型：不能与 nil 比较（编译期类型错误）。
		if strings.Contains(cell.Source, needle) {
			return true
		}
	}
	return false
}

// TestDeferredResumeHistoryBackfillStreamsPagesIntoScene 固化窗口化恢复的补齐半程：
// 更早的页每读回一页就立即前插展示历史并增量装配进统一渲染数据面，最后一页还挡在
// 存储读取上时，前面几页已经可见——不再等全部页读完才一次性绘制。
func TestDeferredResumeHistoryBackfillStreamsPagesIntoScene(t *testing.T) {
	oldInteractive := chatIsInteractiveTerminal
	chatIsInteractiveTerminal = func() bool { return true }
	t.Cleanup(func() { chatIsInteractiveTerminal = oldInteractive })
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	inner, err := runtimechat.NewSQLiteSessionStorage(runtimechat.DefaultPersistentSessionStorageConfig(t.TempDir()))
	require.NoError(t, err)
	// SQLite 保持文件句柄打开，Windows 上 TempDir 清理会因此失败；本清理注册在
	// t.TempDir 之后（LIFO 先执行），保证删除目录前先关掉数据库。
	t.Cleanup(func() { _ = inner.CloseStorage() })
	gated := &gatedHistoryPagerStorage{
		SessionStorage: inner,
		gateCall:       3, // 最新页 + 第 2 页之后，第 3 次分页读取（第 2 个更早的页）被挡住
		reached:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	var releaseOnce sync.Once
	openGate := func() { releaseOnce.Do(func() { close(gated.release) }) }
	defer openGate()

	manager := runtimechat.NewSessionManager(gated, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		MaxHistory:      0,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	defer manager.Stop()

	ctx := context.Background()
	runtimeSession, err := manager.Create(ctx, "tester")
	require.NoError(t, err)

	const messageCount = 250
	messages := make([]runtimetypes.Message, 0, messageCount)
	for index := 0; index < messageCount; index++ {
		if index%2 == 0 {
			messages = append(messages, *runtimetypes.NewUserMessage(fmt.Sprintf("user %d", index)))
		} else {
			messages = append(messages, *runtimetypes.NewAssistantMessage(fmt.Sprintf("assistant %d", index)))
		}
	}
	runtimeSession.ReplaceHistory(messages)
	require.NoError(t, inner.Save(ctx, runtimeSession))

	session := &ChatSession{SessionManager: manager, SessionUserID: "tester"}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	require.True(t, coordinator.enableUnifiedRendererWithWriter(&terminal))

	// 首帧：只同步装载最新一页（与启动恢复/会话内装载的首帧一致）。
	first, ok := loadNewestResumeHistoryPage(session, runtimeSession.ID)
	require.True(t, ok)
	require.True(t, first.HasMore)
	firstPage := session.resumeHistorySnapshot()
	require.Len(t, firstPage, 100)
	bridge.seedPersistedHistoryForSessionLoad(firstPage, "已加载历史会话")

	session.deferResumeHistoryCompletion(runtimeSession.ID, first.NextBeforeSeq)
	startDeferredResumeHistoryLoad(session)

	select {
	case <-gated.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("较早页的分页读取没有推进到被挡住的第三页")
	}
	// 第 3 页仍被挡住：第 2 页必须已经前插进展示历史，并且已经装配进统一渲染面。
	require.Eventually(t, func() bool {
		return len(session.resumeHistorySnapshot()) == 200
	}, 10*time.Second, 5*time.Millisecond, "第 2 页读回后必须立即前插展示历史")
	require.Eventually(t, func() bool {
		return unifiedTranscriptContains(coordinator, "assistant 149")
	}, 10*time.Second, 5*time.Millisecond, "第 2 页读回后必须立即增量绘制到统一渲染面")

	openGate()

	require.Eventually(t, func() bool {
		return len(session.resumeHistorySnapshot()) == messageCount
	}, 10*time.Second, 10*time.Millisecond, "最后一个更早的页也必须在门放开后补齐")
	history := session.resumeHistorySnapshot()
	require.Equal(t, "user 0", history[0].Content)
	require.Equal(t, "assistant 249", history[messageCount-1].Content)
	require.Eventually(t, func() bool {
		return unifiedTranscriptContains(coordinator, "user 0")
	}, 10*time.Second, 5*time.Millisecond, "补回的更早页必须进入统一渲染面")
}

// TestDeferredResumeHistoryBackfillAbortsWhenSnapshotReplaced 固化补齐任务的代际
// 隔离：展示快照在补齐途中被整体替换（切换会话/压缩）后，旧补齐取回的页不得再写回
// 新快照，也不得继续触发装载收尾。
func TestDeferredResumeHistoryBackfillAbortsWhenSnapshotReplaced(t *testing.T) {
	inner, err := runtimechat.NewSQLiteSessionStorage(runtimechat.DefaultPersistentSessionStorageConfig(t.TempDir()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.CloseStorage() })
	gated := &gatedHistoryPagerStorage{
		SessionStorage: inner,
		gateCall:       2, // 第 1 个更早的页读取被挡住
		reached:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	var releaseOnce sync.Once
	openGate := func() { releaseOnce.Do(func() { close(gated.release) }) }
	defer openGate()

	manager := runtimechat.NewSessionManager(gated, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		MaxHistory:      0,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	defer manager.Stop()

	ctx := context.Background()
	runtimeSession, err := manager.Create(ctx, "tester")
	require.NoError(t, err)
	const messageCount = 250
	messages := make([]runtimetypes.Message, 0, messageCount)
	for index := 0; index < messageCount; index++ {
		messages = append(messages, *runtimetypes.NewUserMessage(fmt.Sprintf("user %d", index)))
	}
	runtimeSession.ReplaceHistory(messages)
	require.NoError(t, inner.Save(ctx, runtimeSession))

	session := &ChatSession{SessionManager: manager, SessionUserID: "tester"}
	first, ok := loadNewestResumeHistoryPage(session, runtimeSession.ID)
	require.True(t, ok)
	require.True(t, first.HasMore)
	session.deferResumeHistoryCompletion(runtimeSession.ID, first.NextBeforeSeq)
	startDeferredResumeHistoryLoad(session)

	select {
	case <-gated.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("较早页的分页读取没有发生")
	}
	// 展示快照被整体替换（等价于恢复另一个会话/压缩）：generation 递增。
	session.clearResumeHistory()
	openGate()

	time.Sleep(300 * time.Millisecond)
	require.Empty(t, session.resumeHistorySnapshot(), "被替换的展示快照不得被旧补齐写回")
}

// TestResumeHistoryIncrementalPublishSchedule 固化「按步长发布」的取舍：首页必发
// （首帧之后用户立刻看到最新一页），其后每 stride 页发一次；尾部由装载收尾的授权
// 式快照兜住。逐页发布会让一次恢复把整份转录反复重规划（实测同一会话 42 页、
// 每次发布 ~130ms），所以 stride 必须 > 1，且首页不能被合并掉。
func TestResumeHistoryIncrementalPublishSchedule(t *testing.T) {
	const stride = 4
	published := make([]int, 0, 8)
	for page := 1; page <= 12; page++ {
		if shouldPublishResumeHistoryIncrementalPage(page, stride) {
			published = append(published, page)
		}
	}
	require.Equal(t, []int{1, 4, 8, 12}, published)
	require.True(t, shouldPublishResumeHistoryIncrementalPage(1, stride),
		"首页必须发布：否则首帧之后长时间只显示最新一页")
	require.Less(t, len(published), 12, "12 页不得发布 12 次（合并必须真实生效）")
	// stride <= 0 视为最小步长：调用方尚未读到首页 Total 时不得退化成逐页发布。
	require.False(t, shouldPublishResumeHistoryIncrementalPage(2, 0))
	require.True(t, shouldPublishResumeHistoryIncrementalPage(resumeHistoryIncrementalPublishMinStride, 0))
}

// TestResumeHistoryIncrementalPublishStride 固化「发布次数随历史规模有界」：
// 步长随页数增长（把补齐期间的发布次数钳在 ~6 次），小历史退回最小步长，
// Total 缺失时保守处理而不是退化成逐页发布。
func TestResumeHistoryIncrementalPublishStride(t *testing.T) {
	page := func(total, size int) *runtimechat.SessionHistoryPage {
		messages := make([]runtimetypes.Message, size)
		return &runtimechat.SessionHistoryPage{Messages: messages, Total: total}
	}
	require.Equal(t, resumeHistoryIncrementalPublishMinStride, resumeHistoryIncrementalPublishStride(nil))
	require.Equal(t, resumeHistoryIncrementalPublishMinStride,
		resumeHistoryIncrementalPublishStride(&runtimechat.SessionHistoryPage{Messages: nil, Total: 0}))

	// 42 页（4221 条）的历史：钳在 ~6 步 ⇒ 步长 8。
	require.Equal(t, 8, resumeHistoryIncrementalPublishStride(page(4221, 100)))
	// 400 页：步长 67 ⇒ 发布 ~6 次，而不是固定步长下的 133 次。
	require.Equal(t, 67, resumeHistoryIncrementalPublishStride(page(40000, 100)))
	// 小历史：不低于最小步长（观感优先，成本可忽略）。
	require.Equal(t, resumeHistoryIncrementalPublishMinStride, resumeHistoryIncrementalPublishStride(page(600, 100)))
	// 页大小按实际观测值估算，不写死 100：40 页 ⇒ 步长 7。
	require.Equal(t, 7, resumeHistoryIncrementalPublishStride(page(2000, 50)))
}

// TestResumeHistoryIncrementalStepIsLast 固化「最后一次可见步不铸造非授权快照」的
// 边界：快照单次 0.25-2.3s，被收尾的授权式替换覆盖即为纯浪费；但首步与小历史必须
// 保留（否则补齐过程会整段看不见——真实回归见
// TestDeferredResumeHistoryBackfillStreamsPagesIntoScene）。
func TestResumeHistoryIncrementalStepIsLast(t *testing.T) {
	// 42 页 + 步长 8：40 页之后只剩 3 页，本次必被收尾覆盖 ⇒ 跳过。
	require.True(t, resumeHistoryIncrementalStepIsLast(40, 8, 43))
	// 同一序列的前几次发布：后面还有整段历史要读，快照是用户实际看到的更新 ⇒ 不跳过。
	require.False(t, resumeHistoryIncrementalStepIsLast(8, 8, 43))
	require.False(t, resumeHistoryIncrementalStepIsLast(32, 8, 43))
	// 首步永不跳过（小历史的唯一可见更新）。
	require.False(t, resumeHistoryIncrementalStepIsLast(1, 3, 3))
	require.False(t, resumeHistoryIncrementalStepIsLast(1, 8, 43))
	// 页数未知：保守发布快照，不退化成「最后一步看不见」。
	require.False(t, resumeHistoryIncrementalStepIsLast(9, 3, 0))
}

// TestResumeHistorySnapshotModeForStep 固化逐页补齐的三档快照策略：首步阻塞投递
// （保证第一次可见）、中间步子非阻塞投递（actor 忙就不再停等）、末步跳过（收尾的
// 授权式替换必覆盖它）。成本依据见 docs/e2e/resume-incremental-publish-coalescing.md §10。
func TestResumeHistorySnapshotModeForStep(t *testing.T) {
	// 42 页 + 步长 8。
	require.Equal(t, resumeHistorySnapshotAwait, resumeHistorySnapshotModeForStep(1, 8, 43, true))
	require.Equal(t, resumeHistorySnapshotTry, resumeHistorySnapshotModeForStep(8, 8, 43, true))
	require.Equal(t, resumeHistorySnapshotTry, resumeHistorySnapshotModeForStep(32, 8, 43, true))
	require.Equal(t, resumeHistorySnapshotSkip, resumeHistorySnapshotModeForStep(40, 8, 43, true))

	// 最后一页必跳过，即便它是首步（其后紧跟装载收尾）。
	require.Equal(t, resumeHistorySnapshotSkip, resumeHistorySnapshotModeForStep(1, 3, 3, false))
	// 小历史的唯一可见更新：首步仍阻塞投递（回归见
	// TestDeferredResumeHistoryBackfillStreamsPagesIntoScene）。
	require.Equal(t, resumeHistorySnapshotAwait, resumeHistorySnapshotModeForStep(1, 3, 3, true))
}
