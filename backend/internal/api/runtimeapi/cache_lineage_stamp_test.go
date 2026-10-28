package runtimeapi

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type fakeResumeLineageStore struct {
	session   *chat.Session
	getErr    error
	updateErr error
	updated   int
}

func (s *fakeResumeLineageStore) Get(context.Context, string) (*chat.Session, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.session, nil
}

func (s *fakeResumeLineageStore) Update(context.Context, *chat.Session) error {
	s.updated++
	return s.updateErr
}

// TestStampResumeCacheLineageInSession 锁定 §8.4 窄读法：resume 标签只打在
// 「确有持久化历史」的会话上并回写（executor 每轮从 store 重载，内存标签会丢）；
// 空会话 / 读取失败 / 回写失败都不产生误导性标签，也不阻塞 resume（warn 只是
// 观测面）。批子代理会话不落 SessionStore 的路径同形于「读取失败 → no-op」。
func TestStampResumeCacheLineageInSession(t *testing.T) {
	ctx := context.Background()

	withHistory := chat.NewSession("u1")
	withHistory.AddMessage(*runtimetypes.NewUserMessage("hello"))
	store := &fakeResumeLineageStore{session: withHistory}
	var warned []error
	stampResumeCacheLineageInSession(ctx, store, "s-1", func(err error) { warned = append(warned, err) })
	require.Equal(t, 1, store.updated, "resume 标签必须回写存储")
	value, ok := withHistory.GetContext(agent.CacheLineageModeSessionContextKey)
	require.True(t, ok)
	require.Equal(t, agent.CacheLineageModeResume, value)
	require.Empty(t, warned)

	// 空会话：不打标、不回写（没有可续接的前缀，贴 resume 是误导）。
	empty := chat.NewSession("u1")
	store = &fakeResumeLineageStore{session: empty}
	stampResumeCacheLineageInSession(ctx, store, "s-2", nil)
	require.Zero(t, store.updated)
	if _, ok := empty.GetContext(agent.CacheLineageModeSessionContextKey); ok {
		t.Fatal("empty session must not be stamped as resume")
	}

	// 读取失败（含批子代理会话不落 SessionStore）：no-op。
	store = &fakeResumeLineageStore{getErr: errors.New("session not found")}
	stampResumeCacheLineageInSession(ctx, store, "s-3", nil)
	require.Zero(t, store.updated)

	// 回写失败：标签已置但只走 warn，不阻塞 resume。
	failed := chat.NewSession("u1")
	failed.AddMessage(*runtimetypes.NewUserMessage("hello"))
	store = &fakeResumeLineageStore{session: failed, updateErr: errors.New("io")}
	warned = nil
	stampResumeCacheLineageInSession(ctx, store, "s-4", func(err error) { warned = append(warned, err) })
	require.Len(t, warned, 1)
	require.Equal(t, 1, store.updated)

	// 空 id / nil store：no-op（防御路径）。
	store = &fakeResumeLineageStore{session: withHistory}
	stampResumeCacheLineageInSession(ctx, store, "  ", nil)
	stampResumeCacheLineageInSession(ctx, nil, "s-5", nil)
	require.Zero(t, store.updated)
}
