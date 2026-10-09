package cacheanalytics

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRootViewMergesChildRequests 锁定 schema v9 父会话视图：带父链载荷的子代理
// 请求在根会话的 overview/requests/request 中合并；子会话自身视图只含自身；
// 子会话请求记录携带 parent/root/subagent 字段（明细展示用）。
func TestRootViewMergesChildRequests(t *testing.T) {
	bus, service := attachTest(t, 0)
	source := service.Source()
	require.NotNil(t, source)

	// 根会话自身请求。
	publishStarted(bus, "session-root", "req-root", nil)
	publishFinished(bus, "session-root", "req-root", true, map[string]interface{}{
		"usage_prompt_tokens":     10,
		"usage_completion_tokens": 1,
		"usage_total_tokens":      11,
	})

	// 子代理请求：父链只在 started 载荷（finished 不带，覆盖兜底路径）。
	childExtra := map[string]interface{}{
		"parent_session_id": "session-root",
		"root_session_id":   "session-root",
		"subagent_id":       "task-1",
	}
	publishStarted(bus, "subagent_task_1_x", "req-child", childExtra)
	publishFinished(bus, "subagent_task_1_x", "req-child", true, map[string]interface{}{
		"usage_prompt_tokens":     20,
		"usage_completion_tokens": 2,
		"usage_total_tokens":      22,
	})

	// 根会话总览合并后代 + 子会话计数。
	overview, err := source.Overview("session-root")
	require.NoError(t, err)
	require.Equal(t, 2, overview.RequestsTotal)
	require.Equal(t, int64(33), overview.Tokens.TotalTokens)
	require.Equal(t, 1, overview.ChildSessionCount)
	require.Equal(t, 1, overview.ChildRequestsTotal)

	// 根会话明细包含子会话请求并带父链字段。
	list, err := source.Requests("session-root", RequestQuery{Limit: 10})
	require.NoError(t, err)
	require.Equal(t, 2, list.Total)
	var child *CacheRequestRecord
	for i := range list.Requests {
		if list.Requests[i].SessionID == "subagent_task_1_x" {
			child = &list.Requests[i]
		}
	}
	require.NotNil(t, child, "根会话明细必须包含子代理请求")
	require.Equal(t, "session-root", child.RootSessionID)
	require.Equal(t, "session-root", child.ParentSessionID)
	require.Equal(t, "task-1", child.SubagentID)

	// 子会话自身视图只含自身请求。
	childOverview, err := source.Overview("subagent_task_1_x")
	require.NoError(t, err)
	require.Equal(t, 1, childOverview.RequestsTotal)
	require.Equal(t, 0, childOverview.ChildRequestsTotal)

	// 单请求详情可从根视图按 llm_request_id 命中子会话请求。
	record, err := source.Request("session-root", "req-child")
	require.NoError(t, err)
	require.Equal(t, "subagent_task_1_x", record.SessionID)
}
