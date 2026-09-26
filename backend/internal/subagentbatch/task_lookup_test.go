package subagentbatch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func newLookupTestStore(t *testing.T) BatchStore {
	t.Helper()
	store, err := NewSQLiteBatchStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedLookupBatch(t *testing.T, store BatchStore, parentSessionID, batchID string, status BatchStatus, tasks ...SubagentTaskRecord) {
	t.Helper()
	now := Now()
	for index := range tasks {
		if tasks[index].OrderIndex == 0 {
			tasks[index].OrderIndex = index + 1
		}
		if tasks[index].UpdatedAt.IsZero() {
			tasks[index].UpdatedAt = now
		}
	}
	batch := &SubagentBatch{
		BatchID:         batchID,
		RootScopeID:     parentSessionID,
		ParentSessionID: parentSessionID,
		ExecutionMode:   ExecutionModeBackground,
		Status:          status,
		TaskCount:       len(tasks),
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	if status.Terminal() {
		finished := now
		batch.FinishedAt = &finished
	}
	created, err := store.CreateBatch(context.Background(), batch, tasks)
	require.NoError(t, err)
	require.True(t, created)
}

// TestFindTaskByIDInParentSessionPrefersLiveTask 钉住解析的命中优先级：同名 task id
// 跨批次重复时，运行期身份解析必须拿到**还在跑的那个**，而不是最新批次里的历史行
// （派发重放/重派都会造成重复 id；拿错行会把 wait_agent 定位到已关闭的子会话）。
func TestFindTaskByIDInParentSessionPrefersLiveTask(t *testing.T) {
	store := newLookupTestStore(t)
	ctx := context.Background()
	// 旧批次：运行中（晚绑定的那一次派发）；新批次：已终态（重放出的历史行）。
	seedLookupBatch(t, store, "parent-1", "batch-live", BatchRunning, SubagentTaskRecord{
		TaskID:         "t-shared",
		ChildSessionID: "child-live",
		Status:         TaskRunning,
	})
	seedLookupBatch(t, store, "parent-1", "batch-settled", BatchCompleted, SubagentTaskRecord{
		TaskID:         "t-shared",
		ChildSessionID: "child-settled",
		Status:         TaskSucceeded,
	})

	task, batchID, ok, err := FindTaskByIDInParentSession(ctx, store, "parent-1", "t-shared")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "child-live", task.ChildSessionID, "live binding must win over a settled history row")
	require.Equal(t, "batch-live", batchID)
}

// TestFindTaskByIDInParentSessionScopedToCallerParent 钉住作用域：只能看调用方父会话
// 自己的批次（模型给出的 task id 不得成为跨会话取身份的入口）。
func TestFindTaskByIDInParentSessionScopedToCallerParent(t *testing.T) {
	store := newLookupTestStore(t)
	ctx := context.Background()
	seedLookupBatch(t, store, "parent-1", "batch-own", BatchRunning, SubagentTaskRecord{
		TaskID:         "t-own",
		ChildSessionID: "child-own",
		Status:         TaskRunning,
	})
	seedLookupBatch(t, store, "parent-2", "batch-foreign", BatchRunning, SubagentTaskRecord{
		TaskID:         "t-foreign",
		ChildSessionID: "child-foreign",
		Status:         TaskRunning,
	})

	if _, _, ok, err := FindTaskByIDInParentSession(ctx, store, "parent-1", "t-foreign"); err != nil || ok {
		t.Fatalf("foreign task resolved across parent scope: ok=%v err=%v", ok, err)
	}
	task, _, ok, err := FindTaskByIDInParentSession(ctx, store, "parent-1", "t-own")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "child-own", task.ChildSessionID)
}

// TestFindTaskByIDInParentSessionFallsBackToSettled 钉住回退：只有历史行时仍返回它
// （终态任务的 child_session_id 是读取结果/事件的合法入口），但不得越权。
func TestFindTaskByIDInParentSessionFallsBackToSettled(t *testing.T) {
	store := newLookupTestStore(t)
	ctx := context.Background()
	seedLookupBatch(t, store, "parent-1", "batch-settled", BatchCompleted, SubagentTaskRecord{
		TaskID:         "t-done",
		ChildSessionID: "child-done",
		Status:         TaskSucceeded,
	})
	task, batchID, ok, err := FindTaskByIDInParentSession(ctx, store, "parent-1", "t-done")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "child-done", task.ChildSessionID)
	require.Equal(t, "batch-settled", batchID)

	if _, _, ok, err := FindTaskByIDInParentSession(ctx, store, "parent-1", "t-unknown"); err != nil || ok {
		t.Fatalf("unknown task reported found: ok=%v err=%v", ok, err)
	}
	// 空输入不得触发全表扫描，也不得报 found。
	if _, _, ok, err := FindTaskByIDInParentSession(ctx, store, "", "t-done"); err != nil || ok {
		t.Fatalf("empty parent resolved a task: ok=%v err=%v", ok, err)
	}
	if _, _, ok, err := FindTaskByIDInParentSession(ctx, nil, "parent-1", "t-done"); err != nil || ok {
		t.Fatalf("nil store resolved a task: ok=%v err=%v", ok, err)
	}
}
