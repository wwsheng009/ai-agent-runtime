package subagentbatch

import (
	"context"
	"strings"
)

// FindTaskByIDInParentSession 在**一个父会话的**批任务行里按 task id 定位任务
// （H7 / G4 的解析侧入口：生产侧已在派发时把 child_session_id 早绑定写进 task
// 行，但 `wait_agent(task_id)` / `read_agent_events(task_id)` 的工具面此前没有
// 把这层身份映射起来，运行期只能报 missing / 0 events）。
//
// 作用域纪律：只扫该父会话自己的批次（ListBatches 按 ParentSessionID 过滤），
// 看不到别的父会话的批次——调用方必须先取得调用者会话 id，不得由模型指定。
//
// 命中优先级：同名 task id 可能跨批次重复（重派 / 幂等重放）。优先返回**非终态**
// 任务（运行期身份解析的目标就是"现在还在跑的那个"），否则回退到最新批次里的
// 第一行终态任务（终态任务的 child_session_id 仍可用于读取结果）。
func FindTaskByIDInParentSession(ctx context.Context, store BatchStore, parentSessionID, taskID string) (SubagentTaskRecord, string, bool, error) {
	parentSessionID = strings.TrimSpace(parentSessionID)
	taskID = strings.TrimSpace(taskID)
	if store == nil || parentSessionID == "" || taskID == "" {
		return SubagentTaskRecord{}, "", false, nil
	}
	batches, err := store.ListBatches(ctx, BatchFilter{ParentSessionID: parentSessionID})
	if err != nil {
		return SubagentTaskRecord{}, "", false, err
	}
	var (
		fallback        SubagentTaskRecord
		fallbackBatchID string
	)
	for _, batch := range batches {
		batchID := strings.TrimSpace(batch.BatchID)
		if batchID == "" {
			continue
		}
		tasks, err := store.ListTasks(ctx, batchID)
		if err != nil {
			return SubagentTaskRecord{}, "", false, err
		}
		for _, task := range tasks {
			if strings.TrimSpace(task.TaskID) != taskID {
				continue
			}
			if !task.Status.Terminal() {
				return task, batchID, true, nil
			}
			if fallbackBatchID == "" {
				fallback = task
				fallbackBatchID = batchID
			}
		}
	}
	if fallbackBatchID == "" {
		return SubagentTaskRecord{}, "", false, nil
	}
	return fallback, fallbackBatchID, true, nil
}

// FindTaskByChildSessionIDInParentSession 是 FindTaskByIDInParentSession 的镜像：
// 按**子会话 id** 反查该父会话自己的批任务行。
//
// 为什么需要它（2026-09-26 真机 E2E 缺口）：生产形态里子代理会话不是 durable
// session——子代理运行只把身份（child_session_id）与结果写进批任务行 + 事件流，
// 并不落 SessionStore。因此 `wait_agent(task_id)` 解析出 child_session_id 后，
// 会话快照仍是 missing/exists=false，父代理依旧拿不到状态与输出。调用方应回退到
// 这张账本行投影状态/结果。
//
// 作用域纪律与镜像一致：只扫该父会话自己的批次；child_session_id 由
// subagent_<task>_<uuid> 生成，全局唯一，命中即返回。
func FindTaskByChildSessionIDInParentSession(ctx context.Context, store BatchStore, parentSessionID, childSessionID string) (SubagentTaskRecord, string, bool, error) {
	parentSessionID = strings.TrimSpace(parentSessionID)
	childSessionID = strings.TrimSpace(childSessionID)
	if store == nil || parentSessionID == "" || childSessionID == "" {
		return SubagentTaskRecord{}, "", false, nil
	}
	batches, err := store.ListBatches(ctx, BatchFilter{ParentSessionID: parentSessionID})
	if err != nil {
		return SubagentTaskRecord{}, "", false, err
	}
	for _, batch := range batches {
		batchID := strings.TrimSpace(batch.BatchID)
		if batchID == "" {
			continue
		}
		tasks, err := store.ListTasks(ctx, batchID)
		if err != nil {
			return SubagentTaskRecord{}, "", false, err
		}
		for _, task := range tasks {
			if strings.TrimSpace(task.ChildSessionID) != childSessionID {
				continue
			}
			return task, batchID, true, nil
		}
	}
	return SubagentTaskRecord{}, "", false, nil
}
