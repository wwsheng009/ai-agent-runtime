package commands

// /stop（IM）在 aicli 本地宿主的接线：绑定会话 = 展示会话时走 TUI Esc / /stop
// 同一决策点（R11）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

func TestLocalConnectorInterruptUsesBaseSessionDecision(t *testing.T) {
	kh := ui.NewKeyHandler()
	kh.Start()
	t.Cleanup(kh.Stop)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &ChatSession{
		KeyHandler:     kh,
		cancelCtx:      ctx,
		cancelFunc:     cancel,
		RuntimeSession: &runtimechat.Session{ID: "sess-base"},
	}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord
	stopConsumer := startChatEscapeInterruptWatcher(session)
	defer stopConsumer()

	backend := localConnectorBackend{host: &localChatRuntimeHost{
		BaseSession:   session,
		ActorRegistry: &localActorRegistry{},
	}}
	result, err := backend.Interrupt(context.Background(), connectors.InterruptRequest{
		ConnectorID: "c1",
		Chat:        "chat-1",
		Session:     connectors.SessionRef{SessionID: "sess-base"},
	})
	require.NoError(t, err)
	require.True(t, result.Handled)
	require.True(t, result.Stopped, "有在途回合（消费者）时必须真的发起中断")
	require.True(t, session.IsInterrupted())
	select {
	case <-ctx.Done():
	default:
		t.Fatal("中断必须取消会话 ctx")
	}
}

func TestLocalConnectorInterruptIdleBaseSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &ChatSession{
		cancelCtx:      ctx,
		cancelFunc:     cancel,
		RuntimeSession: &runtimechat.Session{ID: "sess-base"},
	}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord

	backend := localConnectorBackend{host: &localChatRuntimeHost{
		BaseSession:   session,
		ActorRegistry: &localActorRegistry{},
	}}
	result, err := backend.Interrupt(context.Background(), connectors.InterruptRequest{
		ConnectorID: "c1",
		Chat:        "chat-1",
		Session:     connectors.SessionRef{SessionID: "sess-base"},
	})
	require.NoError(t, err)
	require.True(t, result.Handled)
	require.False(t, result.Stopped, "空闲不得谎报已停止")
	require.False(t, session.IsInterrupted())
}
