package chat

import (
	"context"
	"fmt"
	"strings"
)

// EC-E7 / EC-C5：挂起 turn 的"放弃"执行器（设计 §6.12 / §6.5）。
//
// 挂起 turn 在账本终态之前没有正在执行的 run，因此"用户 ESC / interrupt"这类
// 取消信号只取消 run 是不够的：必须把挂起记录连同账本一起收掉，否则
//   - 挂起记录跨中断存活 ⇒ 下一次提交复用旧 turn_id（`nextTurnID`），
//   - 账本内 obligation 无人推进 ⇒ turn 永不结束（EC-E1）。
//
// 放弃不是绕过收尾门禁（I1）：门禁要求"非终态 obligation ⇒ 不得收尾"，本执行器
// 先把账本内每个 obligation 推到终态（`canceled`，coordinator.Cancel 幂等且对终态
// 批次是 no-op），**全部成功之后**才清挂起记录。任一取消失败即中止并保留记录，
// 绝不静默丢 obligation。
//
// 与 settle 路径的差别（`TurnObligationsSettled`）：settle 是自动路径，需要"已终态"
// 的证据，账本行缺失（从未创建 / 已 GC）一律不当作完成；abandon 是**用户显式决定**
// 放弃，行缺失意味着没有可取消的实体，记入 `MissingBatchIDs` 后继续清理，不算
// 静默丢弃（决定本身与原因都落在返回值与事件载荷里）。

// SuspendedTurnAbandonResult 报告一次放弃的结果：取消了哪些批次、哪些本就终态、
// 哪些账本行缺失，以及挂起记录是否已清。
type SuspendedTurnAbandonResult struct {
	TurnID                  string   `json:"turn_id,omitempty"`
	CanceledBatchIDs        []string `json:"canceled_batch_ids,omitempty"`
	AlreadyTerminalBatchIDs []string `json:"already_terminal_batch_ids,omitempty"`
	MissingBatchIDs         []string `json:"missing_batch_ids,omitempty"`
	Cleared                 bool     `json:"cleared,omitempty"`
}

// AbandonSuspendedTurn 放弃本会话的挂起 turn：级联取消账本内全部 obligation，
// 然后清掉 §6.12 挂起记录与派生缓存。没有挂起记录时是幂等 no-op（记录已被清 /
// 本会话未接线挂起 ⇒ 只清可能残留的派生缓存）。
func (a *SessionActor) AbandonSuspendedTurn(ctx context.Context, turnID, reason string) (*SuspendedTurnAbandonResult, error) {
	if a == nil {
		return nil, fmt.Errorf("session actor is nil")
	}
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "user interrupt"
	}
	result := &SuspendedTurnAbandonResult{TurnID: turnID}
	record := a.parkedTurnRecord(ctx, turnID)
	if record == nil {
		// 没有 durable 挂起记录：可能已被清（resume 收尾 / 上一次放弃），也可能
		// 本会话未接线挂起控制面。两种情况都只需收敛派生缓存。
		a.clearSuspendedTurnID(turnID)
		return result, nil
	}
	coordinator := a.agent.GetSubagentBatchCoordinator()
	if coordinator == nil {
		return nil, fmt.Errorf("abandon parked turn %s: subagent batch coordinator is not configured", turnID)
	}
	store := coordinator.Store()
	if store == nil {
		return nil, fmt.Errorf("abandon parked turn %s: subagent batch store is not configured", turnID)
	}
	for _, batchID := range record.ObligationBatchIDs() {
		batch, err := store.GetBatch(ctx, batchID)
		if err != nil {
			return nil, fmt.Errorf("abandon parked turn %s: read obligation %s: %w", turnID, batchID, err)
		}
		if batch == nil {
			result.MissingBatchIDs = append(result.MissingBatchIDs, batchID)
			continue
		}
		if batch.Status.Terminal() {
			result.AlreadyTerminalBatchIDs = append(result.AlreadyTerminalBatchIDs, batchID)
			continue
		}
		if err := coordinator.Cancel(ctx, batchID, reason); err != nil {
			// 保留挂起记录：下一次收尾/中断会重试，绝不在部分取消后清记录。
			return nil, fmt.Errorf("abandon parked turn %s: cancel obligation %s: %w", turnID, batchID, err)
		}
		result.CanceledBatchIDs = append(result.CanceledBatchIDs, batchID)
	}
	if err := store.ClearTurnSuspension(ctx, a.id, turnID); err != nil {
		return nil, fmt.Errorf("abandon parked turn %s: clear suspension: %w", turnID, err)
	}
	result.Cleared = true
	a.clearSuspendedTurnID(turnID)
	return result, nil
}

// abandonSuspendedTurnOnInterrupt resolves which parked turn an interrupt must
// abandon and runs the cascade. Resolution order mirrors `suspendedTurnID`
// (actor_resume_episode.go): in-memory cache first, then a read-only durable
// read-back for a cold cache (actor rebuilt / process restarted), and only then
// the interrupted run's turn id. The durable read-back matters most for the
// EC-E7 shape — a parked turn has no run at all, so `run` is nil there.
// Returns the abandoned turn id (empty when there was nothing to abandon) plus a
// degradation error that must stay visible without failing the interrupt itself.
func (a *SessionActor) abandonSuspendedTurnOnInterrupt(ctx context.Context, run *sessionRunControl) (string, error) {
	if a == nil {
		return "", nil
	}
	turnID := strings.TrimSpace(a.cachedSuspendedTurnID())
	if turnID == "" {
		turnID = strings.TrimSpace(a.durableSuspendedTurnID(ctx))
	}
	if turnID == "" && run != nil {
		turnID = strings.TrimSpace(run.turnID)
	}
	if turnID == "" {
		return "", nil
	}
	result, err := a.AbandonSuspendedTurn(ctx, turnID, "user interrupt")
	if err != nil {
		return turnID, err
	}
	if result == nil || !result.Cleared {
		return "", nil
	}
	return result.TurnID, nil
}
