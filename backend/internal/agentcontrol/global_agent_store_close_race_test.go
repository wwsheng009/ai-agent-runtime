package agentcontrol

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSQLiteGlobalAgentRegistryStoreCloseRacesOperations 固化本轮修复的竞态：
// Close 在 openMu 下把 s.db 置 nil，而业务方法此前在 ensure() 之后裸读 s.db，
// 两者并发时既是数据竞争（-race 可判定），又会在窗口命中时解引用 nil *sql.DB
// 直接 panic（真实栈：api/skills 对账器 Upsert → database/sql.(*DB).conn(0x0)）。
// 修复后每个方法都先经 openMu 保护的 accessor 取本地句柄，因此断言：
//  1. 并发 Close 与读写混合操作不 panic（本用例在 -race 下运行即为回归门禁）；
//  2. 每个操作要么成功，要么失败于 "closed" 语义，不允许出现别的失败模式；
//  3. Close 之后 store 不复活：写路径与读路径都确定性返回 closed 错误。
func TestSQLiteGlobalAgentRegistryStoreCloseRacesOperations(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)

	root := AgentRecord{
		AgentID:       "race-root",
		RootSessionID: "race-root-session",
		SessionID:     "race-root-session",
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
	}
	// 先落一次成功写入：既建立库文件（保证 Close 后读路径走 ensure 而不是
	// “从未打开则跳过”短路），也让后续并发操作有真实数据可读写。
	_, err := store.UpsertAgentControlAgent(ctx, root)
	require.NoError(t, err)

	const (
		workers    = 8
		iterations = 24
	)
	operations := []struct {
		name string
		run  func(record AgentRecord) error
	}{
		{"upsert", func(record AgentRecord) error {
			_, opErr := store.UpsertAgentControlAgent(ctx, record)
			return opErr
		}},
		{"reserve", func(record AgentRecord) error {
			_, opErr := store.ReserveAgentControlAgentSpawn(ctx, root, record, 0)
			return opErr
		}},
		{"close-subtree", func(record AgentRecord) error {
			_, opErr := store.CloseAgentControlAgentSubtree(ctx, root.RootSessionID, record.AgentPath, time.Now().UTC())
			return opErr
		}},
		{"purge", func(record AgentRecord) error {
			if _, opErr := store.PurgeAgentControlTerminalAgents(ctx, time.Now().UTC(), 8); opErr != nil {
				return opErr
			}
			_, opErr := store.PurgeAgentControlAgentWakeEvents(ctx, time.Now().UTC(), 8)
			return opErr
		}},
	}

	start := make(chan struct{})
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for iteration := 0; iteration < iterations; iteration++ {
				record := AgentRecord{
					AgentID:         fmt.Sprintf("race-child-%d-%d", worker, iteration),
					RootSessionID:   root.RootSessionID,
					ParentAgentID:   root.AgentID,
					ParentSessionID: root.SessionID,
					SessionID:       fmt.Sprintf("race-child-%d-%d-session", worker, iteration),
					AgentPath:       fmt.Sprintf("/root/race-child-%d-%d", worker, iteration),
					Depth:           1,
					AgentType:       AgentTypeChild,
					Workflow:        WorkflowSpawnAgent,
				}
				op := operations[(worker+iteration)%len(operations)]
				if opErr := op.run(record); !closedOrNil(opErr) {
					errCh <- fmt.Errorf("%s %s: %w", op.name, record.AgentID, opErr)
					return
				}
				if _, listErr := store.ListAgentControlAgents(ctx, AgentFilter{RootSessionID: root.RootSessionID}); !closedOrNil(listErr) {
					errCh <- fmt.Errorf("list %s: %w", record.AgentID, listErr)
					return
				}
				if _, seqErr := store.LastAgentControlAgentWakeSeq(ctx, AgentWakeFilter{RootSessionID: root.RootSessionID}); !closedOrNil(seqErr) {
					errCh <- fmt.Errorf("wake-seq %s: %w", record.AgentID, seqErr)
					return
				}
			}
		}()
	}

	// 让 8 个 goroutine 与 Close 同时冲线：Close 必然与某些操作的
	// “ensure 通过 → 读取 s.db”窗口重叠，修复前这里要么被 -race 判定为数据竞争，
	// 要么直接 nil 解引用 panic。
	close(start)
	require.NoError(t, store.Close())
	wg.Wait()
	close(errCh)
	for workerErr := range errCh {
		require.NoError(t, workerErr)
	}

	// Close 必须粘滞：写路径与读路径都不允许复活 store。
	_, err = store.UpsertAgentControlAgent(ctx, root)
	require.Error(t, err)
	require.Contains(t, err.Error(), "closed")

	_, err = store.ListAgentControlAgents(ctx, AgentFilter{RootSessionID: root.RootSessionID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "closed")

	_, err = store.LastAgentControlAgentWakeSeq(ctx, AgentWakeFilter{RootSessionID: root.RootSessionID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "closed")
}

// closedOrNil 只接受成功或 Close 语义的失败（store closed / database is closed）；
// 其它错误说明并发修复引入了新的失败模式。
func closedOrNil(err error) bool {
	if err == nil {
		return true
	}
	return strings.Contains(err.Error(), "closed")
}

// TestSQLiteGlobalAgentRegistryStoreHandleAccessorSurvivesCloseInWindow 直接固定
// 历史竞态窗口本身：ensure() 已通过、业务方法即将读取句柄时 Close 抢先执行。
// 修复前该窗口里是裸读字段，命中即 database/sql.(*DB).conn(0x0) 的 nil 解引用；
// 修复后句柄读取与 Close 共用 openMu，必须确定性地返回 closed 错误而不是 nil。
// 上面的并发压测是概率性的（窗口窄，修复前跑 4 遍也未必命中），本用例不依赖时序。
func TestSQLiteGlobalAgentRegistryStoreHandleAccessorSurvivesCloseInWindow(t *testing.T) {
	store := newTestGlobalAgentRegistryStore(t)

	// 窗口前半段：ensure 通过，store 已打开；历史代码在此之后裸读 s.db。
	require.NoError(t, store.ensure())
	// 窗口后半段：Close 在锁内置空 s.db。
	require.NoError(t, store.Close())

	db, err := store.liveDBHandle()
	require.Error(t, err, "a closed store must never hand out a handle")
	require.Nil(t, db)
	require.Contains(t, err.Error(), "closed")

	// 窗口过后，读路径与写路径同样只能得到 closed 语义。
	_, err = store.ListAgentControlAgents(context.Background(), AgentFilter{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "closed")
	_, err = store.UpsertAgentControlAgent(context.Background(), AgentRecord{
		AgentID:       "window",
		RootSessionID: "window-root",
		AgentPath:     "/root/window",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "closed")
}
