package agent

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// TestParkTeamObligationAnnouncesOnceAndAppends 守护 §6.12 的 team 半边：
// spawn_team（auto_start）派发把 team:<id> 记到当前 turn 的挂起记录，同一
// team 重复派发不重复记录、不重复播报（turn.suspended 是边沿事件），后续
// 派发的第二个 team 只追加义务、不再播报。
func TestParkTeamObligationAnnouncesOnceAndAppends(t *testing.T) {
	ctx := context.Background()
	store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "batches.db"),
	})
	require.NoError(t, err)
	require.True(t, store.IsDurable())
	t.Cleanup(func() { _ = store.Close() })

	apiAgent := newSuspensionGateAgent(t, nil)
	apiAgent.SetSubagentBatchCoordinator(&SubagentBatchCoordinator{store: store})

	bus := runtimeevents.NewBus()
	var mu sync.Mutex
	var suspended []runtimeevents.Event
	bus.Subscribe(runtimeevents.EventTurnSuspended, func(event runtimeevents.Event) {
		mu.Lock()
		suspended = append(suspended, event)
		mu.Unlock()
	})
	apiAgent.SetEventBus(bus)

	loop := NewReActLoop(apiAgent, llm.NewLLMRuntime(nil), &LoopReActConfig{})
	loop.turnID = "turn-team-park"

	loop.parkTeamObligation(ctx, "sess-team-park", "team-1")
	// 幂等重挂（同一 spawn 重放 / 崩溃重试）：不重复记录、不重复播报。
	loop.parkTeamObligation(ctx, "sess-team-park", "team-1")
	loop.parkTeamObligation(ctx, "sess-team-park", "team-2")

	record, ok, err := store.GetTurnSuspension(ctx, "sess-team-park", "turn-team-park")
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, record)
	require.Equal(t, []string{"team:team-1", "team:team-2"}, record.ObligationIDs,
		"team: 义务必须按派发顺序写入同一挂起记录")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, suspended, 1, "turn.suspended 是边沿事件：同一 turn 只播报一次")
	require.Equal(t, "sess-team-park", suspended[0].SessionID)
	require.Equal(t, "turn-team-park", suspended[0].Payload["turn_id"], "载荷必须锚定挂起的 turn（I3）")
	require.Equal(t, "team-1", suspended[0].Payload["team_id"])
	require.Equal(t, 1, suspended[0].Payload["obligation_count"])
	require.NotEmpty(t, suspended[0].Payload["parked_at"])
}
