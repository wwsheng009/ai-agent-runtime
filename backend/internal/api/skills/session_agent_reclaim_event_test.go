package skills

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// TestSessionAgentController_SpawnGatePublishesReclaimEvent pins plan §P2-8
// 方案 4 on the API host: when the spawn gate evicts an idle child to admit a new
// one, the eviction reaches the runtime event bus as `agent.reclaimed`, scoped
// to the parent session so the frontend session stream can render it, and the
// handler event bridge persists that type into the session event store.
func TestSessionAgentController_SpawnGatePublishesReclaimEvent(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Agents.MaxThreads = 1
	// Opt-in idle eviction: the freshly spawned child is live, so only the idle
	// timeout can make it reclaimable (the conservative default would refuse).
	cfg.Agents.ReclaimIdleMs = 1
	handler.SetRuntimeConfig(cfg, "")
	store, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: t.TempDir() + "/agents.db",
	})
	require.NoError(t, err)
	defer store.Close()
	handler.SetAgentControlAgentStore(store)

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-reclaim-event")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-reclaim-child-1"})
	require.NoError(t, err)
	// Let the child session's UpdatedAt age past the 1ms idle timeout.
	time.Sleep(10 * time.Millisecond)
	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-reclaim-child-2"})
	require.NoError(t, err, "the reclaim pass must free the slot for the second child")

	var reclaimEvents []runtimeevents.Event
	for _, event := range handler.getRuntimeEventBus().Recent(64) {
		if strings.TrimSpace(event.Type) == agentcontrol.EventAgentReclaimed {
			reclaimEvents = append(reclaimEvents, event)
		}
	}
	require.Len(t, reclaimEvents, 1, "exactly one eviction pass must be reported")
	event := reclaimEvents[0]
	require.Equal(t, rootSession.ID, event.SessionID, "the product event belongs to the parent session stream")
	require.Equal(t, agentcontrol.ReclaimSourceSpawnGate, event.Payload["source"])
	require.Equal(t, 1, event.Payload["reclaimed"])
	require.Equal(t, int64(1), event.Payload["rows"])
	require.Equal(t, []string{agentcontrol.ReclaimReasonIdleTimeout}, event.Payload["reasons"])
	require.Equal(t, rootSession.ID, event.Payload["root_session_id"])
	agentPath, _ := event.Payload["agent_path"].(string)
	require.Contains(t, agentPath, "api-reclaim-child-1", "the evicted child must be named")
	require.NotEmpty(t, event.Payload["summary"])

	// Persistence contract: the handler bridge must mirror the product event
	// into the parent session event stream (frontend /runtime/events).
	require.True(t, shouldPersistRuntimeSessionEvent(event), "agent.reclaimed must be persisted into the session stream")
	require.False(t, shouldPersistRuntimeSessionEvent(runtimeevents.Event{Type: agentcontrol.EventAgentReclaimed}),
		"an event without a session id must not be persisted")
}

// TestSessionAgentController_NoReclaimNoEvent keeps the stream clean: a gate
// that only rejects the spawn (nothing evictable) must not publish an empty
// agent.reclaimed event.
func TestSessionAgentController_NoReclaimNoEvent(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Agents.MaxThreads = 1
	handler.SetRuntimeConfig(cfg, "")
	store, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: t.TempDir() + "/agents.db",
	})
	require.NoError(t, err)
	defer store.Close()
	handler.SetAgentControlAgentStore(store)

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-reclaim-quiet")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-quiet-child-1"})
	require.NoError(t, err)
	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-quiet-child-2"})
	require.Error(t, err)
	message := err.Error()
	require.Contains(t, message, "agent spawn thread limit reached: max_threads=1 active_children=1")
	require.Contains(t, message, "next_action="+agentcontrol.ThreadLimitNextAction)
	require.Contains(t, message, "occupants=[")
	require.Contains(t, message, "status=active")
	// A no-op reclaim pass (idle eviction disabled, nothing evictable) must not
	// claim a reclaim: the diagnostic stays clean instead of printing reclaimed=0.
	require.NotContains(t, message, "reclaimed=")
	require.NotContains(t, message, "reclaim_error=")

	for _, event := range handler.getRuntimeEventBus().Recent(64) {
		require.NotEqual(t, agentcontrol.EventAgentReclaimed, strings.TrimSpace(event.Type),
			"a refused spawn without eviction must not claim a reclaim")
	}
}
