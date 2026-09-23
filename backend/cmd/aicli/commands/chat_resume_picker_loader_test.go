package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func newResumePickerTestManager(t *testing.T, sessions int) (*runtimechat.SessionManager, []*runtimechat.Session) {
	t.Helper()
	storage, err := runtimechat.NewSQLiteSessionStorage(runtimechat.DefaultPersistentSessionStorageConfig(t.TempDir()))
	require.NoError(t, err)
	manager := runtimechat.NewSessionManager(storage, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		MaxHistory:      0,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	t.Cleanup(manager.Stop)

	ctx := context.Background()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	created := make([]*runtimechat.Session, 0, sessions)
	for index := 0; index < sessions; index++ {
		session, err := manager.Create(ctx, "tester")
		require.NoError(t, err)
		session.Metadata.Title = fmt.Sprintf("alpha-%02d", index)
		session.ReplaceHistory([]runtimetypes.Message{
			*runtimetypes.NewUserMessage(fmt.Sprintf("prompt %d", index)),
			*runtimetypes.NewAssistantMessage("answer"),
		})
		// Deterministic recency: alpha-00 is the oldest, alpha-NN the newest.
		session.UpdatedAt = base.Add(time.Duration(index) * time.Minute)
		session.PreserveUpdatedAt = true
		require.NoError(t, storage.Save(ctx, session))
		created = append(created, session)
	}
	return manager, created
}

// TestResumePickerSessionLoaderPagesWithoutSessionLimit 固化 P0 修复：
// /resume 选择器不再继承 --session-limit（默认 20），而是按元数据分页，
// 用户可以滚到任意历史会话，且每页只读元数据（不加载历史）。
func TestResumePickerSessionLoaderPagesWithoutSessionLimit(t *testing.T) {
	const total = 55
	manager, created := newResumePickerTestManager(t, total)
	ctx := context.Background()

	session := &ChatSession{SessionManager: manager, SessionUserID: "tester", NoInteractive: true}
	// --session-limit 的默认值 20 以前会把候选截断在 20 条。
	loader := newResumePickerSessionLoader(session, ChatSessionListFilter{Limit: 20}, "", nil)

	first, err := loader.LoadPage(ctx, ui.FullScreenListPageRequest{Limit: resumePickerPageSize})
	require.NoError(t, err)
	require.Len(t, first.Items, resumePickerPageSize)
	require.True(t, first.HasMore, "首页之后仍有会话，列表必须允许继续加载")

	second, err := loader.LoadPage(ctx, ui.FullScreenListPageRequest{
		Offset: len(loader.Items()),
		Limit:  resumePickerPageSize,
		Query:  "",
	})
	require.NoError(t, err)
	require.Len(t, second.Items, total-resumePickerPageSize)
	require.False(t, second.HasMore)
	require.Equal(t, total, second.Total)
	require.Len(t, loader.Items(), total, "--session-limit 不得再截断交互选择器")
	require.Equal(t, created[total-1].ID, loader.SessionAt(0).ID, "最新会话必须排在第一行")
	require.Equal(t, created[0].ID, loader.SessionAt(total-1).ID)

	// 分页只读元数据：行内不再声称已加载历史（轮次数未知，只报消息数）。
	require.Contains(t, first.Items[0].Leading, "【2条】")
	require.NotContains(t, first.Items[0].Leading, "0轮")
	// 行内不再追加目录等尾部信息，元数据下沉到底部详情区。
	require.Empty(t, strings.TrimSpace(first.Items[0].Detail))
	require.Contains(t, first.Items[0].SearchText, "alpha-54")
}

// TestResumePickerSessionLoaderFiltersByQuery 固化动态搜索：查询条件在存储层
// 生效，返回的结果集与查询一致且不再受首页窗口限制。
func TestResumePickerSessionLoaderFiltersByQuery(t *testing.T) {
	manager, _ := newResumePickerTestManager(t, 12)
	ctx := context.Background()

	session := &ChatSession{SessionManager: manager, SessionUserID: "tester", NoInteractive: true}
	loader := newResumePickerSessionLoader(session, ChatSessionListFilter{}, "", nil)

	page, err := loader.LoadPage(ctx, ui.FullScreenListPageRequest{Limit: resumePickerPageSize, Query: "alpha-07"})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.False(t, page.HasMore)
	require.Contains(t, page.Items[0].Title, "alpha-07")
	require.Equal(t, 1, page.Total)

	// 清空查询后回到完整目录（同一 loader 复用，窗口必须重置）。
	page, err = loader.LoadPage(ctx, ui.FullScreenListPageRequest{Limit: resumePickerPageSize, Query: ""})
	require.NoError(t, err)
	require.Len(t, page.Items, 12)
	require.False(t, page.HasMore)
}

// TestResumePickerSessionLoaderPinsCurrentSession 固化当前会话语义：默认视图把
// 当前会话置顶为不可选行，它不参与候选计数，也不会被重复列在下面。
func TestResumePickerSessionLoaderPinsCurrentSession(t *testing.T) {
	manager, created := newResumePickerTestManager(t, 3)
	ctx := context.Background()

	current := created[2]
	current.Metadata.Title = "current session"
	session := &ChatSession{SessionManager: manager, SessionUserID: "tester", NoInteractive: true}
	loader := newResumePickerSessionLoader(session, ChatSessionListFilter{}, current.ID, current)

	page, err := loader.LoadPage(ctx, ui.FullScreenListPageRequest{Limit: resumePickerPageSize})
	require.NoError(t, err)
	require.Len(t, page.Items, 3, "2 个候选 + 1 个置顶的当前会话")
	require.True(t, page.Items[0].Disabled)
	require.Contains(t, page.Items[0].Title, "current session")
	require.Nil(t, loader.SessionAt(0), "当前会话行不可选")
	require.True(t, loader.HasCandidates())
	for _, item := range page.Items[1:] {
		require.NotContains(t, item.Title, "current session")
	}

	// 搜索时不再保留置顶行：用户找的是其它会话。
	page, err = loader.LoadPage(ctx, ui.FullScreenListPageRequest{Limit: resumePickerPageSize, Query: "alpha-00"})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.False(t, page.Items[0].Disabled)
	require.Equal(t, created[0].ID, loader.SessionAt(0).ID)
}

// TestResumePickerSessionLoaderHasNoCandidatesWithoutConversation 固化候选判据：
// 从未产生消息的占位会话不得出现在恢复列表里。
func TestResumePickerSessionLoaderHasNoCandidatesWithoutConversation(t *testing.T) {
	storage, err := runtimechat.NewSQLiteSessionStorage(runtimechat.DefaultPersistentSessionStorageConfig(t.TempDir()))
	require.NoError(t, err)
	manager := runtimechat.NewSessionManager(storage, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		MaxHistory:      0,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	t.Cleanup(manager.Stop)

	_, err = manager.Create(context.Background(), "tester")
	require.NoError(t, err)

	session := &ChatSession{SessionManager: manager, SessionUserID: "tester", NoInteractive: true}
	loader := newResumePickerSessionLoader(session, ChatSessionListFilter{}, "", nil)
	page, err := loader.LoadPage(context.Background(), ui.FullScreenListPageRequest{Limit: resumePickerPageSize})
	require.NoError(t, err)
	require.Empty(t, page.Items)
	require.False(t, page.HasMore)
	require.False(t, loader.HasCandidates())
}

// TestNewResumePickerWindowKeepsPrebuiltListWithoutManager 固化向后兼容：
// 没有会话管理器的调用方（测试/嵌入）仍走预建列表，索引语义不变。
func TestNewResumePickerWindowKeepsPrebuiltListWithoutManager(t *testing.T) {
	now := time.Now()
	history := runtimechat.NewSession("tester")
	history.ID = "history-1"
	history.ReplaceHistory([]runtimetypes.Message{
		*runtimetypes.NewUserMessage("hello"),
		*runtimetypes.NewAssistantMessage("hi"),
	})
	history.UpdatedAt = now

	window, err := newResumePickerWindow(&ChatSession{}, ChatSessionListFilter{}, []*runtimechat.Session{history}, nil)
	require.NoError(t, err)
	require.Len(t, window.items, 1)
	require.Nil(t, window.pageLoader())
	require.True(t, window.HasCandidates())
	require.Equal(t, history, window.SessionAt(0))
	require.Contains(t, window.subtitle(), "最近更新优先")
}
