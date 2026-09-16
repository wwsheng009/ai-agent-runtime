package agentcontrol

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSQLiteGlobalMailboxRegistryStoreCloseRacesOperations 固化与 agent store 同源的竞态：
// Close 在 openMu 下把 s.db 置 nil，而业务方法此前在 ensure() 之后裸读 s.db，
// 两者并发时既是数据竞争（-race 可判定），又会在窗口命中时解引用 nil *sql.DB
// 直接 panic。修复后每个方法都先经 openMu 保护的 accessor 取本地句柄，因此断言：
//  1. 并发 Close 与读写混合操作不 panic（本用例在 -race 下运行即为回归门禁）；
//  2. 每个操作要么成功，要么失败于 "closed" 语义，不允许出现别的失败模式；
//  3. Close 之后 store 不复活：写路径与读路径都确定性返回 closed 错误。
func TestSQLiteGlobalMailboxRegistryStoreCloseRacesOperations(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteGlobalMailboxRegistryStore(&GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// 先落一次成功写入：既建立库文件（保证 Close 后读路径走 ensure 而不是
	// “从未打开且无文件则跳过”短路），也让后续并发操作有真实数据可读写。
	_, err = store.AppendGlobalMailboxRecord(ctx, "runtime_sessions", MailboxRecord{
		Seq:               1,
		Workflow:          WorkflowSpawnTeam,
		Scope:             MailboxScopeSession,
		SessionID:         "race-seed-session",
		SessionMailboxSeq: 1,
		MessageID:         "race-seed-mail",
		Kind:              MailboxKindTeamLifecycle,
		Body:              "seed",
		CreatedAt:         time.Now().UTC(),
	})
	require.NoError(t, err)

	const (
		workers    = 8
		iterations = 24
	)
	type mailboxOperation func(worker, iteration int) error
	operations := []struct {
		name string
		run  mailboxOperation
	}{
		{"append", func(worker, iteration int) error {
			_, opErr := store.AppendGlobalMailboxRecord(ctx, "runtime_sessions", MailboxRecord{
				Seq:               int64(iteration + 1),
				Workflow:          WorkflowSpawnTeam,
				Scope:             MailboxScopeSession,
				SessionID:         fmt.Sprintf("race-session-%d-%d", worker, iteration),
				SessionMailboxSeq: int64(iteration + 1),
				MessageID:         fmt.Sprintf("race-session-mail-%d-%d", worker, iteration),
				Kind:              MailboxKindTeamLifecycle,
				Body:              "session body",
				CreatedAt:         time.Now().UTC(),
			})
			return opErr
		}},
		{"append-primary", func(worker, iteration int) error {
			_, opErr := store.AppendPrimaryGlobalMailboxRecord(ctx, MailboxRecord{
				Workflow:  WorkflowSpawnAgent,
				Scope:     MailboxScopeTeam,
				TeamID:    fmt.Sprintf("race-team-%d-%d", worker, iteration),
				TeamSeq:   int64(iteration + 1),
				MessageID: fmt.Sprintf("race-team-mail-%d-%d", worker, iteration),
				Kind:      MailboxKindSubagentCompleted,
				Body:      "agent body",
				CreatedAt: time.Now().UTC(),
			})
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
				op := operations[(worker+iteration)%len(operations)]
				if opErr := op.run(worker, iteration); !closedOrNil(opErr) {
					errCh <- fmt.Errorf("%s %d-%d: %w", op.name, worker, iteration, opErr)
					return
				}
				if _, listErr := store.ListAgentControlMailboxRecords(ctx, MailboxRecordFilter{Limit: 8}); !closedOrNil(listErr) {
					errCh <- fmt.Errorf("list %d-%d: %w", worker, iteration, listErr)
					return
				}
				if _, seqErr := store.LastAgentControlMailboxRecordSeq(ctx, MailboxRecordFilter{}); !closedOrNil(seqErr) {
					errCh <- fmt.Errorf("record-seq %d-%d: %w", worker, iteration, seqErr)
					return
				}
				if _, wakeErr := store.LastAgentControlMailboxWakeSeq(ctx, MailboxWakeFilter{}); !closedOrNil(wakeErr) {
					errCh <- fmt.Errorf("wake-seq %d-%d: %w", worker, iteration, wakeErr)
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
	_, err = store.AppendGlobalMailboxRecord(ctx, "runtime_sessions", MailboxRecord{
		Seq:       99,
		Scope:     MailboxScopeSession,
		SessionID: "after-close-session",
		MessageID: "after-close-mail",
		CreatedAt: time.Now().UTC(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "closed")

	_, err = store.ListAgentControlMailboxRecords(ctx, MailboxRecordFilter{Limit: 1})
	require.Error(t, err)
	require.Contains(t, err.Error(), "closed")

	_, err = store.LastAgentControlMailboxWakeSeq(ctx, MailboxWakeFilter{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "closed")
}

// TestSQLiteGlobalMailboxRegistryStoreHandleAccessorSurvivesCloseInWindow 直接固定
// 历史竞态窗口本身：ensure() 已通过、业务方法即将读取句柄时 Close 抢先执行。
// 修复前该窗口里是裸读字段，命中即 database/sql.(*DB).conn(0x0) 的 nil 解引用；
// 修复后句柄读取与 Close 共用 openMu，必须确定性地返回 closed 错误而不是 nil。
// 上面的并发压测是概率性的（窗口窄，修复前跑多遍也未必命中），本用例不依赖时序。
func TestSQLiteGlobalMailboxRegistryStoreHandleAccessorSurvivesCloseInWindow(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteGlobalMailboxRegistryStore(&GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// 先成功写入一次，让库文件落地：Close 后的读路径必须走 ensure 而不是
	// “从未打开且无文件则跳过”短路，否则 closed 语义会被 skip 掩盖。
	_, err = store.AppendGlobalMailboxRecord(ctx, "runtime_sessions", MailboxRecord{
		Seq:               1,
		Scope:             MailboxScopeSession,
		SessionID:         "window-session",
		SessionMailboxSeq: 1,
		MessageID:         "window-mail",
		Body:              "window body",
		CreatedAt:         time.Now().UTC(),
	})
	require.NoError(t, err)

	// 窗口前半段：ensure 通过，store 已打开；历史代码在此之后裸读 s.db。
	db, err := store.dbHandle()
	require.NoError(t, err)
	require.NotNil(t, db)
	// 窗口后半段：Close 在锁内置空 s.db。
	require.NoError(t, store.Close())

	handle, err := store.liveDBHandle()
	require.Error(t, err, "a closed store must never hand out a handle")
	require.Nil(t, handle)
	require.Contains(t, err.Error(), "closed")

	// 窗口过后，读路径、写路径与内部取值路径同样只能得到 closed 语义。
	_, err = store.LastAgentControlMailboxRecordSeq(ctx, MailboxRecordFilter{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "closed")

	_, err = store.AppendPrimaryGlobalMailboxRecord(ctx, MailboxRecord{
		Scope:     MailboxScopeSession,
		SessionID: "window-primary",
		MessageID: "window-primary-mail",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "closed")

	_, err = store.getGlobalMailboxRecordByPrimaryKey(ctx, "window-primary-key")
	require.Error(t, err)
	require.Contains(t, err.Error(), "closed")
}
