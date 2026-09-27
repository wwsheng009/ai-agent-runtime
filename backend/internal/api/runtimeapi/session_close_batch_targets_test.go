package runtimeapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// F6（API 宿主对等能力）：close 目标解析必须把 spawn_subagents 批次 id 展开为
// 该批次全部子会话，未知 id 不被当作批次。
func TestSessionAgentControllerResolveCloseTargetsFromBatch(t *testing.T) {
	handler, _ := newAPISupervisionBudgetTestHandler(t, "api-close-batch-targets")
	store := newAPISupervisionBatchStore(t)
	handler.SetSubagentBatchStore(store)
	runningAPIBatch(t, store, "batch_api_close", "sess_close_parent")
	controller := &sessionAgentController{handler: handler}

	ctx := context.Background()
	targetSessionID, closeIDs, ok := controller.resolveCloseTargetsFromBatch(ctx, "batch_api_close")
	require.True(t, ok)
	require.Equal(t, "child_session_1", targetSessionID)
	require.Equal(t, []string{"child_session_1"}, closeIDs)

	_, _, ok = controller.resolveCloseTargetsFromBatch(ctx, "batch_missing")
	require.False(t, ok)
}
