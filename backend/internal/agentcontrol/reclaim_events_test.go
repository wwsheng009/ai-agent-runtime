package agentcontrol

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReclaimEventPayloadSummarizesEviction pins the P2-8 方案 4 product-event
// payload contract: counters, deduplicated reasons, the evicted paths/sessions
// and the one-line summary shared with the gate diagnostic.
func TestReclaimEventPayloadSummarizesEviction(t *testing.T) {
	outcome := ReclaimOutcome{
		Rows: 3,
		Decisions: []ReclaimDecision{
			{AgentID: "child-a", AgentPath: "/root/a", SessionID: "sess-a", Reason: ReclaimReasonSessionTerminal},
			{AgentID: "child-b", AgentPath: "/root/b", SessionID: "sess-b", Reason: ReclaimReasonIdleTimeout},
			{AgentID: "child-c", AgentPath: "/root/c", SessionID: "sess-c", Reason: ReclaimReasonSessionTerminal},
		},
	}

	payload := ReclaimEventPayload(ReclaimSourceSpawnGate, outcome)

	require.Equal(t, ReclaimSourceSpawnGate, payload["source"])
	require.Equal(t, 3, payload["reclaimed"])
	require.Equal(t, int64(3), payload["rows"])
	require.Equal(t, []string{ReclaimReasonSessionTerminal, ReclaimReasonIdleTimeout}, payload["reasons"])
	require.Equal(t, []string{"/root/a", "/root/b", "/root/c"}, payload["agent_paths"])
	require.Equal(t, "/root/a", payload["agent_path"], "single-child consumers read agent_path")
	require.Equal(t, []string{"sess-a", "sess-b", "sess-c"}, payload["session_ids"])
	require.Equal(t, "sess-a", payload["session_id"])
	// summary 复用闸门诊断文案，并与结构化 reasons 同口径：两处都折叠重复原因，
	// 否则 3 个同因驱逐会渲染成 session_terminal,session_terminal,session_terminal。
	require.Equal(t, "reclaimed=3 reclaimed_rows=3 reclaim_reasons=session_terminal,idle_timeout", payload["summary"])
	_, truncated := payload["truncated"]
	require.False(t, truncated, "a small pass must not advertise truncation")
	_, failed := payload["failed"]
	require.False(t, failed)
}

// TestReclaimEventPayloadCountsFailuresWithoutListingThem keeps the report
// honest: a subtree the store refused to close is still registered, so it must
// never appear in agent_paths, only in the failure counters.
func TestReclaimEventPayloadCountsFailuresWithoutListingThem(t *testing.T) {
	outcome := ReclaimOutcome{
		Rows:       2,
		Failed:     1,
		FirstError: "registry row is locked",
		Decisions: []ReclaimDecision{
			{AgentPath: "/root/a", SessionID: "sess-a", Reason: ReclaimReasonSessionMissing},
		},
	}

	payload := ReclaimEventPayload(ReclaimSourceManualCleanup, outcome)

	require.Equal(t, ReclaimSourceManualCleanup, payload["source"])
	require.Equal(t, 1, payload["reclaimed"])
	require.Equal(t, 1, payload["failed"])
	require.Equal(t, "registry row is locked", payload["error"])
	require.Equal(t, []string{"/root/a"}, payload["agent_paths"])
	require.Equal(t, "reclaimed=1 reclaimed_rows=2 reclaim_failed=1 reclaim_reasons=session_missing reclaim_error=registry row is locked", payload["summary"])
}

// TestReclaimEventPayloadBoundsWidePasses caps the listed rows so one wide
// eviction cannot bloat the session event stream; the durable rows keep the
// detail and `truncated` tells how many were summarised away.
func TestReclaimEventPayloadBoundsWidePasses(t *testing.T) {
	outcome := ReclaimOutcome{}
	for i := 0; i < reclaimEventAgentLimit+4; i++ {
		outcome.Decisions = append(outcome.Decisions, ReclaimDecision{
			AgentPath: agentTestPath(i),
			SessionID: agentTestSession(i),
			Reason:    ReclaimReasonSessionTerminal,
		})
	}

	payload := ReclaimEventPayload(ReclaimSourceSpawnGate, outcome)

	require.Equal(t, reclaimEventAgentLimit+4, payload["reclaimed"])
	require.Equal(t, 4, payload["truncated"])
	require.Len(t, payload["agent_paths"], reclaimEventAgentLimit)
	require.Len(t, payload["session_ids"], reclaimEventAgentLimit)
	require.Equal(t, agentTestPath(0), payload["agent_path"])
	require.Equal(t, []string{ReclaimReasonSessionTerminal}, payload["reasons"])
}

func TestReclaimEventPayloadDefaultsSourceAndSkipsEmptyLists(t *testing.T) {
	payload := ReclaimEventPayload("   ", ReclaimOutcome{Rows: 1, Decisions: []ReclaimDecision{{Reason: ReclaimReasonSessionMissing}}})

	require.Equal(t, ReclaimSourceSpawnGate, payload["source"], "an unlabelled eviction is a spawn-gate eviction")
	require.Equal(t, 1, payload["reclaimed"])
	_, hasPaths := payload["agent_paths"]
	require.False(t, hasPaths, "no path must not fake an empty list")
	_, hasSessions := payload["session_ids"]
	require.False(t, hasSessions)
	_, hasAgentPath := payload["agent_path"]
	require.False(t, hasAgentPath)
}

func agentTestPath(i int) string {
	return "/root/child-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
}

func agentTestSession(i int) string {
	return "sess-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
}

// TestHumanReclaimSummaryForPayload pins the human projection of the
// `agent.reclaimed` payload: consumers that only see the event stream (CLI
// timeline note, logs) get the same one-liner as `/agents cleanup`, led by who
// triggered the pass, while the payload keeps its machine-readable counters.
func TestHumanReclaimSummaryForPayload(t *testing.T) {
	payload := ReclaimEventPayload(ReclaimSourceReconcile, ReclaimOutcome{
		Rows: 3,
		Decisions: []ReclaimDecision{
			{AgentPath: "/root/a", Reason: ReclaimReasonSessionTerminal},
			{AgentPath: "/root/b", Reason: ReclaimReasonSessionTerminal},
			{AgentPath: "/root/c", Reason: ReclaimReasonSessionTerminal},
		},
	})
	require.Equal(t, "reclaimed=3 reclaimed_rows=3 reclaim_reasons=session_terminal", payload["summary"],
		"the machine contract is unchanged")
	require.Equal(t,
		"周期对账：已自动回收 3 个已结束的子会话；释放 3 个线程槽位",
		HumanReclaimSummaryForPayload(payload))

	// A durable-stream round trip hands over float64 counters and []interface{}
	// reasons; the projection must survive it (hosts and CLI read the same event).
	require.Equal(t,
		"手动清理：已自动回收 1 个长期空闲的子会话；释放 1 个线程槽位",
		HumanReclaimSummaryForPayload(map[string]interface{}{
			"source":    ReclaimSourceManualCleanup,
			"reclaimed": float64(1),
			"reasons":   []interface{}{ReclaimReasonIdleTimeout},
		}))

	// Failure-only pass: still a sentence, and it says the reader has to look.
	require.Equal(t,
		"spawn 配额回收：未能回收任何子 agent；1 个未能回收：store closed（需要关注）",
		HumanReclaimSummaryForPayload(map[string]interface{}{
			"source":    ReclaimSourceSpawnGate,
			"reclaimed": 0,
			"failed":    1,
			"error":     "store closed",
		}))

	// Unknown sources fall through verbatim instead of being dropped.
	require.Equal(t,
		"future_source：已自动回收 1 个子 agent；释放 1 个线程槽位",
		HumanReclaimSummaryForPayload(map[string]interface{}{"source": "future_source", "reclaimed": 1}))

	// No counters → no line (callers omit the note instead of printing noise).
	require.Empty(t, HumanReclaimSummaryForPayload(nil))
	require.Empty(t, HumanReclaimSummaryForPayload(map[string]interface{}{"source": ReclaimSourceSpawnGate}))
}
