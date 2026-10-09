package usageanalytics

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// publishChildRequest 发布一条带父链载荷的子代理请求（started + finished）。
func publishChildRequest(t *testing.T, bus *runtimeevents.Bus, childSessionID, llmRequestID, parentSessionID, rootSessionID, subagentID string, at time.Time, usage map[string]interface{}) {
	t.Helper()
	bus.Publish(runtimeevents.Event{
		Type:      EventLLMRequestStarted,
		TraceID:   llmRequestID + "-trace",
		SessionID: childSessionID,
		Timestamp: at,
		Payload: map[string]interface{}{
			"llm_request_id":    llmRequestID,
			"trace_id":          llmRequestID + "-trace",
			"turn_id":           llmRequestID + "-turn",
			"step":              1,
			"provider":          "acme",
			"model":             "m1",
			"parent_session_id": parentSessionID,
			"root_session_id":   rootSessionID,
			"subagent_id":       subagentID,
		},
	})
	publishRequestFinished(bus, childSessionID, llmRequestID, usage)
}

// TestParentSessionRollupIncludesChildRequests 锁定 schema v9 的核心口径：
//   - 子代理请求同时计入子会话行（自身）与根会话行（rollup）；
//   - 会话列表默认只返回根行（全局不重复计数），IncludeChildSessions 可展开；
//   - 根会话明细展开后代并标注子会话/子代理 ID；子会话详情只看自身；
//   - drift 对账按 root/session 双口径零偏差；重复终态幂等。
func TestParentSessionRollupIncludesChildRequests(t *testing.T) {
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	service, bus := newTestService(t, nil, now)
	store := service.Store()

	// 根会话自身请求。
	publishRequestStarted(bus, "session-root", "req-root", "trace-root", "turn-root", 1, now, "acme", "m1")
	publishRequestFinished(bus, "session-root", "req-root", usagePayload(100, 10, 110, 0))

	// 子代理请求（父链由调度器 payload 注入）。
	childSession := "subagent_task_1_abc"
	publishChildRequest(t, bus, childSession, "req-child", "session-root", "session-root", "task-1",
		now.Add(time.Second), usagePayload(200, 20, 220, 50))

	// 子会话行 = 自身请求。
	child := readStoredStats(t, store, childSession)
	require.Equal(t, 1, child.totalRequests)
	require.Equal(t, 220, child.totalTokens)

	// 根会话行 = 自身 + 后代。
	root := readStoredStats(t, store, "session-root")
	require.Equal(t, 2, root.totalRequests)
	require.Equal(t, 330, root.totalTokens)
	require.Equal(t, 2, root.turnCount)

	// 父链列落库（供查询与对账）。
	row := queryRow(t, store, `SELECT parent_session_id, root_session_id, subagent_id FROM usage_requests WHERE llm_request_id = 'req-child'`)
	require.Equal(t, "session-root", row[0])
	require.Equal(t, "session-root", row[1])
	require.Equal(t, "task-1", row[2])

	// 列表默认只返回根行；显式 IncludeChildSessions 时子行可见。
	list, err := store.ListSessions(Query{})
	require.NoError(t, err)
	require.Len(t, list.Sessions, 1)
	require.Equal(t, "session-root", list.Sessions[0].SessionID)
	require.Equal(t, 2, list.Sessions[0].LLMRequests)

	withChildren, err := store.ListSessions(Query{IncludeChildSessions: true})
	require.NoError(t, err)
	require.Len(t, withChildren.Sessions, 2)

	// 根会话详情展开后代，Step/Turn 带子会话标识。
	detail, err := store.SessionUsage("session-root")
	require.NoError(t, err)
	require.Len(t, detail.Steps, 2)
	var childStep *StepUsage
	for i := range detail.Steps {
		if detail.Steps[i].SessionID == childSession {
			childStep = &detail.Steps[i]
		}
	}
	require.NotNil(t, childStep, "根会话明细必须包含子会话请求")
	require.Equal(t, "session-root", childStep.ParentSessionID)
	require.Equal(t, "task-1", childStep.SubagentID)

	foundChildTurn := false
	for _, turn := range detail.Turns {
		if turn.SessionID == childSession {
			foundChildTurn = true
			require.Equal(t, "task-1", turn.SubagentID)
		}
	}
	require.True(t, foundChildTurn, "根会话轮次必须包含子会话轮次")

	// 子会话详情只看自身。
	childDetail, err := store.SessionUsage(childSession)
	require.NoError(t, err)
	require.Len(t, childDetail.Steps, 1)
	require.Equal(t, 1, childDetail.Session.LLMRequests)

	// drift 对账：根行按 root 口径、子行按 session 口径，均无偏差。
	drift := store.sampleStatsDrift(10)
	require.Equal(t, 0, drift.Drifted, "对账样本: %+v", drift.Samples)

	// 重复终态幂等：计数不重复累加。
	publishRequestFinished(bus, childSession, "req-child", usagePayload(200, 20, 220, 50))
	require.Equal(t, 2, readStoredStats(t, store, "session-root").totalRequests)
	require.Equal(t, 1, readStoredStats(t, store, childSession).totalRequests)
}

// TestParentSessionRowNotPollutedBySubagentAttributedRequests 锁定实测缺陷
// （2026-10-09）：父会话在子代理在途期间替子代理代跑的请求带
// parent_session_id=自身/subagent_id=子会话（请求级归因）；会话行必须保持顶层
// 口径（父链为空），否则根会话会被列表过滤（s.parent_session_id = ''）漏掉、
// 会话视图的 rollup 口径退化为"仅自身"。
func TestParentSessionRowNotPollutedBySubagentAttributedRequests(t *testing.T) {
	now := time.Date(2026, 10, 9, 11, 0, 0, 0, time.UTC)
	service, bus := newTestService(t, nil, now)
	store := service.Store()

	bus.Publish(runtimeevents.Event{
		Type: EventLLMRequestStarted, TraceID: "trace-parent", SessionID: "session-root", Timestamp: now,
		Payload: map[string]interface{}{
			"llm_request_id": "req-parent", "trace_id": "trace-parent", "turn_id": "turn-parent",
			"step": 1, "provider": "acme", "model": "m1",
			"parent_session_id": "session-root", "root_session_id": "session-root", "subagent_id": "task-1",
		},
	})
	publishRequestFinished(bus, "session-root", "req-parent", usagePayload(100, 10, 110, 0))

	// 会话行：顶层口径（请求级自指归因不得写入）。
	sessionRow := queryRow(t, store, `SELECT parent_session_id, subagent_id FROM usage_sessions WHERE session_id = 'session-root'`)
	require.Equal(t, "", sessionRow[0])
	require.Equal(t, "", sessionRow[1])

	// 请求行：保留请求级归因（代跑标注），与设计一致。
	requestRow := queryRow(t, store, `SELECT parent_session_id, subagent_id FROM usage_requests WHERE llm_request_id = 'req-parent'`)
	require.Equal(t, "session-root", requestRow[0])
	require.Equal(t, "task-1", requestRow[1])

	// 根会话仍出现在列表（未被根行过滤漏掉）。
	list, err := store.ListSessions(Query{})
	require.NoError(t, err)
	require.Len(t, list.Sessions, 1)
	require.Equal(t, "session-root", list.Sessions[0].SessionID)
}

// TestSessionRowLineagePrefersLookupOverRequestAttribution 锁定会话行归属优先级：
// 有宿主 lookup 时用会话自身归属（子会话补父链）；lookup 无结论时才退回请求级
// 归因，且自指父值一律按顶层处理。
func TestSessionRowLineagePrefersLookupOverRequestAttribution(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	bus := runtimeevents.NewBus()
	service, err := Attach(bus, Options{
		Config: Config{Path: filepath.Join(t.TempDir(), DefaultDBFileName)},
		Lineage: testLineageLookup{lineage: map[string]SessionLineage{
			"child-1": {ParentSessionID: "session-root", RootSessionID: "session-root", SubagentID: "task-1"},
		}},
		Now: func() time.Time { return now },
	})
	require.NoError(t, err)
	require.NotNil(t, service)
	t.Cleanup(service.Close)
	store := service.Store()

	// 子会话请求（载荷无父链）：lookup 补齐会话行。
	publishRequestStarted(bus, "child-1", "req-child-2", "trace-child-2", "turn-child-2", 1, now, "acme", "m1")
	publishRequestFinished(bus, "child-1", "req-child-2", usagePayload(50, 5, 55, 0))
	childRow := queryRow(t, store, `SELECT parent_session_id, subagent_id FROM usage_sessions WHERE session_id = 'child-1'`)
	require.Equal(t, "session-root", childRow[0])
	require.Equal(t, "task-1", childRow[1])

	// 根会话：lookup 无记录，会话行保持顶层。
	publishRequestStarted(bus, "session-root", "req-root-2", "trace-root-2", "turn-root-2", 1, now, "acme", "m1")
	publishRequestFinished(bus, "session-root", "req-root-2", usagePayload(10, 1, 11, 0))
	rootRow := queryRow(t, store, `SELECT parent_session_id, subagent_id FROM usage_sessions WHERE session_id = 'session-root'`)
	require.Equal(t, "", rootRow[0])
	require.Equal(t, "", rootRow[1])
}

// testLineageLookup 是可控的 SessionLineageLookup 测试桩。
type testLineageLookup struct {
	lineage map[string]SessionLineage
}

func (l testLineageLookup) SessionLineage(sessionID string) (SessionLineage, bool) {
	lineage, ok := l.lineage[sessionID]
	return lineage, ok
}

// TestLineageResolutionSources 锁定父链解析优先级：事件载荷 > 宿主 lookup >
// subagent.started 学习映射；三级都缺时按顶层处理（root=自身）。
func TestLineageResolutionSources(t *testing.T) {
	now := time.Date(2026, 10, 9, 11, 0, 0, 0, time.UTC)
	bus := runtimeevents.NewBus()
	service, err := Attach(bus, Options{
		Config: Config{Path: t.TempDir() + "/" + DefaultDBFileName},
		Lookup: nil,
		Lineage: testLineageLookup{lineage: map[string]SessionLineage{
			"child-lookup": {ParentSessionID: "session-lookup-root", RootSessionID: "session-lookup-root", SubagentID: "agent-9"},
		}},
		Now: func() time.Time { return now },
	})
	require.NoError(t, err)
	t.Cleanup(service.Close)
	store := service.Store()

	// 1) payload 优先。
	publishChildRequest(t, bus, "child-payload", "req-payload", "session-payload-root", "session-payload-root", "task-p", now, usagePayload(10, 1, 11, 0))
	// 2) lookup 次之。
	publishRequestStarted(bus, "child-lookup", "req-lookup", "trace-lookup", "turn-lookup", 1, now, "acme", "m1")
	publishRequestFinished(bus, "child-lookup", "req-lookup", usagePayload(20, 2, 22, 0))
	// 3) subagent.started 学习映射兜底（llm.request.* 载荷无父链）。
	bus.Publish(runtimeevents.Event{
		Type:      EventSubagentStarted,
		SessionID: "child-learned",
		Timestamp: now,
		Payload: map[string]interface{}{
			"subagent_id":       "task-l",
			"parent_session_id": "session-learned-root",
		},
	})
	publishRequestStarted(bus, "child-learned", "req-learned", "trace-learned", "turn-learned", 1, now, "acme", "m1")
	publishRequestFinished(bus, "child-learned", "req-learned", usagePayload(30, 3, 33, 0))
	// 4) 三级都缺：顶层（root=自身）。
	publishRequestStarted(bus, "session-solo", "req-solo", "trace-solo", "turn-solo", 1, now, "acme", "m1")
	publishRequestFinished(bus, "session-solo", "req-solo", usagePayload(40, 4, 44, 0))

	check := func(llmRequestID, wantParent, wantRoot, wantSubagent string) {
		t.Helper()
		row := queryRow(t, store, `SELECT parent_session_id, root_session_id, subagent_id FROM usage_requests WHERE llm_request_id = '`+llmRequestID+`'`)
		require.Equal(t, wantParent, row[0], llmRequestID)
		require.Equal(t, wantRoot, row[1], llmRequestID)
		require.Equal(t, wantSubagent, row[2], llmRequestID)
	}
	check("req-payload", "session-payload-root", "session-payload-root", "task-p")
	check("req-lookup", "session-lookup-root", "session-lookup-root", "agent-9")
	check("req-learned", "session-learned-root", "session-learned-root", "task-l")
	check("req-solo", "", "session-solo", "")
}
