package skills

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// surfaceInvalidatorStub 记录批量失效调用次数；内嵌接口满足 RuntimeStateStore 其余方法。
type surfaceInvalidatorStub struct {
	chat.RuntimeStateStore
	calls int
}

func (s *surfaceInvalidatorStub) InvalidateStableToolSurfaces(ctx context.Context) (int, error) {
	s.calls++
	return 3, nil
}

// plainRuntimeStateStoreStub 只实现 RuntimeStateStore，不实现失效扩展。
type plainRuntimeStateStoreStub struct {
	chat.RuntimeStateStore
}

func TestHandler_InvalidateSessionRuntimeToolSurfaces(t *testing.T) {
	store := &surfaceInvalidatorStub{}
	handler := &Handler{sessionRuntimeStore: store}
	handler.invalidateSessionRuntimeToolSurfaces()
	require.Equal(t, 1, store.calls)

	// 不支持失效扩展的 store 不得 panic，也不得触发调用。
	plain := &plainRuntimeStateStoreStub{}
	require.NotPanics(t, func() {
		(&Handler{sessionRuntimeStore: plain}).invalidateSessionRuntimeToolSurfaces()
	})
}
