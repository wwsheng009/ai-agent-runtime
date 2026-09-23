package chat

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// C3-7 / AC-P2-7a：挂起 turn 的"新 episode"语义（设计 §6.15 / §6.12）。
//
// 托管 turn（父派发 durable background obligations 后收尾）在账本终态之前处于
// **挂起态**：turn 没有结束，只是没有正在执行的 run。此时到达的 steer（用户输入 /
// send_input / continue / wake resume）不是"新 turn"，而是**同一 turn 的新
// episode**：
//
//   - `turn_id` 复用挂起 turn 的 id（"resume 复用同一 turn"，与 AC-P1-1a 同口径）；
//   - 用户输入就是本 episode 的 prompt ⇒ 天然置顶，无需改写历史（设计 §6.15）；
//   - 不触碰 supervision 账本：episode 的投递不经过账本写路径，因此
//     `progress_seq` / 延长计数 / stall 判定逐字段不变（AC-P2-7c）。
//
// 唯一事实来源是 durable batch 控制面上的 §6.12 挂起记录；RuntimeState 里的
// SuspendedTurnID 只是缓存，每次使用前都回查记录，记录消失即停止复用。判读失败
// （未接线 / 非 durable / 读错误）一律按"未挂起"处理：最坏情况只是回到旧行为
// （新开 turn），绝不丢输入、绝不静默改账本。

// nextTurnID 为一次新提交选择 turn_id：挂起态复用挂起 turn 的 id（新 episode），
// 否则新开 turn。
func (a *SessionActor) nextTurnID(ctx context.Context) string {
	if suspended := a.suspendedTurnID(ctx); suspended != "" {
		return suspended
	}
	return "turn_" + uuid.NewString()
}

// suspendedTurnID 返回本会话当前可续跑的挂起 turn_id；无挂起 turn 时返回空串。
func (a *SessionActor) suspendedTurnID(ctx context.Context) string {
	if a == nil {
		return ""
	}
	candidate := strings.TrimSpace(a.cachedSuspendedTurnID())
	if candidate == "" {
		// 冷启动（actor 重建 / 本进程首次提交，内存缓存尚为空）：durable 状态里
		// 可能留着上一次 run 收尾写下的挂起 turn。只读回查一次；命中后补进内存
		// 缓存，本回合的收尾与持久化就仍带着这个 turn_id（重启续跑，AC-P1-1a）。
		candidate = strings.TrimSpace(a.durableSuspendedTurnID(ctx))
		if candidate != "" {
			a.adoptSuspendedTurnID(ctx, candidate)
		}
	}
	if candidate == "" {
		return ""
	}
	if !a.turnSuspended(ctx, candidate) {
		// 记录已被清（resume 收尾 / 放弃）：停止复用并清掉缓存，避免 turn_id 被
		// 永久粘住（EC-E1"turn 永不结束"的防线之一）。
		a.clearSuspendedTurnID(candidate)
		return ""
	}
	return candidate
}

// cachedSuspendedTurnID 读内存缓存里的挂起 turn_id（克隆，不持锁返回）。
func (a *SessionActor) cachedSuspendedTurnID() string {
	state := a.stateWithoutToolSurfaces()
	if state == nil {
		return ""
	}
	return state.SuspendedTurnID
}

// durableSuspendedTurnID 只读地回查一次持久化状态里的挂起缓存：不安装状态、不落盘。
func (a *SessionActor) durableSuspendedTurnID(ctx context.Context) string {
	if a == nil || a.stateStore == nil {
		return ""
	}
	if ctx == nil {
		ctx = context.Background()
	}
	state, err := a.stateStore.LoadState(ctx, a.id)
	if err != nil || state == nil {
		return ""
	}
	return state.SuspendedTurnID
}

// adoptSuspendedTurnID 把 durable 里读到的挂起 turn 补进内存缓存（仅当缓存仍为空，
// 收敛写：并发路径已经写入的值不会被覆盖）。
func (a *SessionActor) adoptSuspendedTurnID(ctx context.Context, turnID string) {
	turnID = strings.TrimSpace(turnID)
	if a == nil || turnID == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_ = a.updateStateConvergent(ctx, func(state *RuntimeState) error {
		if strings.TrimSpace(state.SuspendedTurnID) != "" {
			return nil
		}
		state.SuspendedTurnID = turnID
		state.UpdatedAt = time.Now().UTC()
		return nil
	})
}

// turnSuspended 回查 durable batch 控制面：该 turn 是否仍有 §6.12 挂起记录。
func (a *SessionActor) turnSuspended(ctx context.Context, turnID string) bool {
	return a.parkedTurnRecord(ctx, turnID) != nil
}

// parkedTurnRecord 读取本会话在 durable batch 控制面上的 §6.12 挂起记录。
// 未接线、非 durable、记录不存在或读失败一律返回 nil。
func (a *SessionActor) parkedTurnRecord(ctx context.Context, turnID string) *subagentbatch.TurnSuspension {
	turnID = strings.TrimSpace(turnID)
	if a == nil || a.agent == nil || turnID == "" {
		return nil
	}
	coordinator := a.agent.GetSubagentBatchCoordinator()
	if coordinator == nil {
		return nil
	}
	store := coordinator.Store()
	if store == nil || !store.IsDurable() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	record, ok, err := store.GetTurnSuspension(ctx, a.id, turnID)
	if err != nil || !ok || record == nil {
		return nil
	}
	return record
}

// syncSuspendedTurn 在 run 收尾时把"本回合是否仍在挂起态"写回 RuntimeState，
// 返回应写入的 turn_id（空串＝未挂起）。它只做一次 durable 读，不写账本：
// 挂起记录由派发方（agent loop §6.12）与 resume/放弃路径维护，actor 不越权清理。
func (a *SessionActor) syncSuspendedTurn(ctx context.Context, turnID string) string {
	if !a.turnSuspended(ctx, turnID) {
		return ""
	}
	return strings.TrimSpace(turnID)
}

// clearSuspendedTurnID 清掉派生缓存（仅当缓存值仍等于已知失效值时写状态）。
func (a *SessionActor) clearSuspendedTurnID(stale string) {
	if a == nil {
		return
	}
	stale = strings.TrimSpace(stale)
	if stale == "" {
		return
	}
	_ = a.updateStateConvergent(context.Background(), func(state *RuntimeState) error {
		if strings.TrimSpace(state.SuspendedTurnID) != stale {
			return nil
		}
		state.SuspendedTurnID = ""
		state.UpdatedAt = time.Now().UTC()
		return nil
	})
}
