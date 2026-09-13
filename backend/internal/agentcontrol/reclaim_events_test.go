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
	// summary 复用闸门诊断文案（ReclaimOutcome.Summary 不去重），结构化 reasons
	// 才是去重后的集合——两者口径不同是刻意的：文案保持与超限错误一致。
	require.Equal(t, "reclaimed=3 reclaimed_rows=3 reclaim_reasons=session_terminal,idle_timeout,session_terminal", payload["summary"])
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
