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

// TestSQLiteGlobalAgentRegistryStoreConcurrentReserveCloseConservesQuota 固化 P2-10 的并发压测项：
// 多个 goroutine 在同一 store 上并发 reserve/release/close，断言
//  1. 配额计数守恒：结束后 active 集合只剩 root，终态行数等于成功预留次数；
//  2. 额度可完全复用：并发结束后仍能按同一 limit 重新预留（无“配额僵尸”泄漏）；
//  3. 无死锁：整批操作必须在期限内完成。
//
// 该用例不依赖时间注入，只用短重试消化并发争抢，因此可在 CI 稳定运行。
func TestSQLiteGlobalAgentRegistryStoreConcurrentReserveCloseConservesQuota(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)

	const (
		limit      = 4
		workers    = 8
		iterations = 6
	)
	root := AgentRecord{
		AgentID:       "root",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
	}
	_, err := store.UpsertAgentControlAgent(ctx, root)
	require.NoError(t, err)

	deadline := time.Now().Add(60 * time.Second)
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				agentID := fmt.Sprintf("child-%d-%d", worker, iteration)
				child := AgentRecord{
					AgentID:         agentID,
					RootSessionID:   root.RootSessionID,
					ParentAgentID:   root.AgentID,
					ParentSessionID: root.SessionID,
					SessionID:       agentID + "-session",
					AgentPath:       "/root/" + agentID,
					Depth:           1,
					AgentType:       AgentTypeChild,
					Workflow:        WorkflowSpawnAgent,
				}
				for {
					_, reserveErr := store.ReserveAgentControlAgentSpawn(ctx, root, child, limit)
					if reserveErr == nil {
						break
					}
					if strings.Contains(reserveErr.Error(), "agent spawn thread limit reached") && time.Now().Before(deadline) {
						time.Sleep(5 * time.Millisecond)
						continue
					}
					errCh <- fmt.Errorf("reserve %s: %w", agentID, reserveErr)
					return
				}
				// 交替走两条终态路径：回滚（release→stale）与正常关闭（close→closed），
				// 两者都必须把额度还给后来的预留。
				if iteration%2 == 0 {
					if _, releaseErr := store.ReleaseAgentControlAgentSpawn(ctx, agentID, "concurrency stress release"); releaseErr != nil {
						errCh <- fmt.Errorf("release %s: %w", agentID, releaseErr)
						return
					}
					continue
				}
				if _, closeErr := store.CloseAgentControlAgentSubtree(ctx, root.RootSessionID, child.AgentPath, time.Now().UTC()); closeErr != nil {
					errCh <- fmt.Errorf("close %s: %w", agentID, closeErr)
					return
				}
			}
		}()
	}

	joined := make(chan struct{})
	go func() {
		wg.Wait()
		close(joined)
	}()
	select {
	case <-joined:
	case <-time.After(90 * time.Second):
		t.Fatal("concurrent reserve/close did not finish in time; suspect deadlock")
	}
	close(errCh)
	for workerErr := range errCh {
		require.NoError(t, workerErr)
	}

	active, err := store.ListAgentControlAgents(ctx, AgentFilter{RootSessionID: root.RootSessionID})
	require.NoError(t, err)
	require.Len(t, active, 1, "only the root row may stay active after every child reached a terminal state")
	require.Equal(t, root.AgentID, active[0].AgentID)

	all, err := store.ListAgentControlAgents(ctx, AgentFilter{RootSessionID: root.RootSessionID, IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, all, 1+workers*iterations, "every successful reservation must leave exactly one terminal row")

	terminalByStatus := map[string]int{}
	for _, record := range all {
		if record.AgentID == root.AgentID {
			continue
		}
		terminalByStatus[record.Status]++
	}
	expectedPerPath := (workers * iterations) / 2
	require.Equal(t, expectedPerPath, terminalByStatus[AgentStatusStale], "release path must end stale")
	require.Equal(t, expectedPerPath, terminalByStatus[AgentStatusClosed], "close path must end closed")

	// 额度完全复用：若并发期间丢失过一次 release/close，这里的预留会撞上限。
	probe := AgentRecord{
		AgentID:         "child-probe",
		RootSessionID:   root.RootSessionID,
		ParentAgentID:   root.AgentID,
		ParentSessionID: root.SessionID,
		SessionID:       "child-probe-session",
		AgentPath:       "/root/child-probe",
		Depth:           1,
		AgentType:       AgentTypeChild,
		Workflow:        WorkflowSpawnAgent,
	}
	_, err = store.ReserveAgentControlAgentSpawn(ctx, root, probe, limit)
	require.NoError(t, err, "quota must be fully reclaimable after concurrent spawn/close")
	_, err = store.ReleaseAgentControlAgentSpawn(ctx, probe.AgentID, "stress cleanup")
	require.NoError(t, err)
}
