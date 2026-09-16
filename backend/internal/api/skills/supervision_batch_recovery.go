package skills

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// 缺口背景（2026-09-16）：API 宿主（runtime-server）的 spawn_subagents 控制面
// 相对 CLI 少了两条腿，父会话因此可能永远收不到「batch 结束」的 durable 汇报：
//
//  1. 没有 TerminalSink。CLI 在 chat_actor_host.go 的 agent 构造路径上装
//     localSubagentBatchTerminalSink，把 host-neutral 的终态通知翻成
//     BuildSubagentBatchTerminalMailboxMessage 写进父会话 mailbox；API 侧
//     subagentBatchCoordinator 只装了 Store+Scheduler，agent 从 agent 上取
//     Emitter/LifecycleProjector，但没有 sink ⇒ 终态只进了 supervision store 与
//     wake，父会话的 durable mailbox 里没有那条汇报消息。
//  2. 没有启动恢复。CLI 用 runLocalSubagentStartupRecovery 做「立即 + 宽限期后」
//     两趟有界恢复（RecoverStaleBatches → ReplayTerminalDeliveries），把上次进程
//     遗留的 queued/running batch 收敛为终态、把已终态但投递失败的批次重放一次。
//     API 宿主没有这个入口，进程重启后 file-backed store 里的遗留行会永远停在
//     非终态（没有 actor 去物化它），终态投递也永远不会重试。
//
// 本文件补齐这两条腿。语义严格对齐 CLI：
//   - 恢复只做有界的有界扫描（limit/超时），不创建 actor、不启动 worker；
//   - 恢复 coordinator 只装 Store/Emitter/TerminalSink/LifecycleProjector，
//     不装 Scheduler，因此不会误接管别的进程正在跑的 batch；
//   - 第一趟在进程启动时跑，第二趟在 restartGrace 之后跑：上一进程崩溃瞬间写入
//     的行对第一趟来说「太新」（在宽限期内），但也不能永远留在非终态。
const (
	// apiSubagentBatchRestartGrace 与 CLI 的 localSubagentBatchRestartGrace 同值。
	apiSubagentBatchRestartGrace = 5 * time.Minute
	// apiSubagentBatchRecoveryPassTimeout 单趟恢复的硬超时，避免大 store 拖住启动。
	apiSubagentBatchRecoveryPassTimeout = 15 * time.Second
	// apiSubagentBatchRecoveryLimit 单趟恢复最多处理的批次数。
	apiSubagentBatchRecoveryLimit = 512
)

// apiSubagentBatchEmitter 是 API 宿主的 batch 展示镜像：与 CLI 同口径，事件挂到
// 父会话上（parent_session_id），ToolName 固定为 spawn_subagents，便于前端按
// 同一规则渲染。
func (h *Handler) apiSubagentBatchEmitter() agent.BatchEmitter {
	return func(eventType string, payload map[string]interface{}) {
		if h == nil || strings.TrimSpace(eventType) == "" {
			return
		}
		sessionID := ""
		if payload != nil {
			if value, ok := payload["parent_session_id"].(string); ok {
				sessionID = strings.TrimSpace(value)
			}
		}
		h.getRuntimeEventBus().Publish(runtimeevents.Event{
			Type:      eventType,
			SessionID: sessionID,
			ToolName:  "spawn_subagents",
			Payload:   payload,
		})
	}
}

// apiSubagentBatchTerminalSink 是 API 宿主的 durable 终态投递口：把终态通知写成
// 父会话 mailbox 消息（先落 event store，再通知 runtime bus）。
//
// 幂等边界是 mailbox 消息 id（BuildSubagentBatchTerminalMailboxMessage 由
// ParentSessionID + BatchID + DeliveryKey 生成），因此重放是安全的：跨进程重放
// 会命中同一条消息并被识别为 Duplicate，而不是给父会话塞两条汇报。
func (h *Handler) apiSubagentBatchTerminalSink() agent.BatchTerminalSink {
	return func(ctx context.Context, notification agent.BatchTerminalNotification) agent.BatchTerminalDelivery {
		if h == nil {
			return agent.BatchTerminalDelivery{
				Status: agent.BatchTerminalDeliveryFailed,
				Err:    fmt.Errorf("api handler is nil"),
			}
		}
		parentSessionID := strings.TrimSpace(notification.Batch.ParentSessionID)
		if parentSessionID == "" {
			return agent.BatchTerminalDelivery{
				Status: agent.BatchTerminalDeliveryFailed,
				Err:    fmt.Errorf("subagent batch terminal delivery requires a parent session"),
			}
		}
		message := toolbroker.BuildSubagentBatchTerminalMailboxMessage(
			parentSessionID,
			notification.Batch.BatchID,
			notification.EventType,
			notification.DeliveryKey,
			notification.Payload,
		)
		result, err := chat.DeliverMailboxEventFirstResult(
			ctx,
			h.getSessionEventStore(),
			h.getRuntimeEventBus(),
			nil,
			parentSessionID,
			message,
		)
		if err != nil {
			return agent.BatchTerminalDelivery{
				Status:      agent.BatchTerminalDeliveryFailed,
				DeliveryKey: notification.DeliveryKey,
				Err:         err,
			}
		}
		return agent.BatchTerminalDelivery{
			Status:           agent.BatchTerminalDeliveryPersisted,
			DeliveryKey:      notification.DeliveryKey,
			AlreadyDelivered: result.Duplicate,
		}
	}
}

// apiSubagentBatchRecoveryCoordinator 组装只用于恢复的 coordinator：与 CLI 的
// 恢复路径同形，不带 Scheduler（恢复只收敛 durable 行 + 重放投递，不执行任务），
// 因此不会与真正持有 worker 的 coordinator 抢所有权。
//
// store 为 nil（宿主没有 durable 控制面）时返回 nil：调用方必须把它当作
// 「无需恢复」，而不是把它当成错误。
func (h *Handler) apiSubagentBatchRecoveryCoordinator(store subagentbatch.BatchStore) *agent.SubagentBatchCoordinator {
	if h == nil || store == nil {
		return nil
	}
	return agent.NewSubagentBatchCoordinator(agent.SubagentBatchCoordinatorConfig{
		Store:              store,
		Emitter:            h.apiSubagentBatchEmitter(),
		TerminalSink:       h.apiSubagentBatchTerminalSink(),
		LifecycleProjector: h.apiBatchLifecycleProjector(),
	})
}

// runAPISubagentBatchRecoveryOnce 执行一趟有界恢复，返回收敛数与重放结果。
//
// 顺序固定为「先收敛非终态，再重放终态投递」：收敛会把遗留的 queued/running 行
// 写成终态，这些新终态同样需要投递，所以重放必须排在其后，否则这批行要等到下
// 一趟恢复才有汇报。
func (h *Handler) runAPISubagentBatchRecoveryOnce(ctx context.Context, store subagentbatch.BatchStore, restartGrace time.Duration, limit int) (int, agent.BatchTerminalReplayResult, error) {
	var replay agent.BatchTerminalReplayResult
	coordinator := h.apiSubagentBatchRecoveryCoordinator(store)
	if coordinator == nil {
		return 0, replay, nil
	}
	// 恢复会投影生命周期行；没有 durable supervision 控制面的宿主不能做恢复
	// （projector 会直接报错），此时保持现状：既不复位遗留行，也不重放投递。
	if h.getSupervisionStore() == nil {
		return 0, replay, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = apiSubagentBatchRecoveryLimit
	}
	// staleAfter = 宽限期：比宽限期更新的行可能属于另一个仍在运行的宿主进程，
	// 恢复必须放过它们（同 CLI 口径）。第二趟在宽限期过期后运行，正是为了兜住
	// 「启动瞬间还太新」的那批行。
	recovered, err := coordinator.RecoverStaleBatches(ctx, restartGrace, "", limit)
	if err != nil {
		return recovered, replay, err
	}
	replay, err = coordinator.ReplayTerminalDeliveries(ctx, "", limit)
	if err != nil {
		return recovered, replay, err
	}
	return recovered, replay, nil
}

// injectedSubagentBatchStore 只读宿主已注入的 store，不做惰性创建：恢复绝不能
// 为了"扫一遍"而把未接线宿主的默认内存库建出来（那是 per-process 的，跨重启没
// 有任何可恢复的行，只会改变未接线宿主的启动行为）。
func (h *Handler) injectedSubagentBatchStore() subagentbatch.BatchStore {
	if h == nil {
		return nil
	}
	h.subagentBatchMu.Lock()
	defer h.subagentBatchMu.Unlock()
	return h.subagentBatchStore
}

// StartSubagentBatchRecovery 用宿主已注入的 durable batch store 启动恢复，并按
// 默认宽限期跑第二趟。未注入 store（未接线宿主、只有进程内默认库）时是 no-op。
func (h *Handler) StartSubagentBatchRecovery() {
	h.startSubagentBatchRecovery(h.injectedSubagentBatchStore(), apiSubagentBatchRestartGrace)
}

// startSubagentBatchRecovery 启动 API 宿主的后台 batch 恢复：立即跑一趟，然后在
// restartGrace 之后跑第二趟，随后退出（不是周期循环，与 CLI 的
// runLocalSubagentStartupRecovery 同形）。
//
// 重复调用是 no-op；StopSubagentBatchRecovery 可取消尚未运行的第二趟。宿主在
// 关闭前应调用 Stop（测试同理），否则会留下一个等待宽限期的 goroutine。
func (h *Handler) startSubagentBatchRecovery(store subagentbatch.BatchStore, restartGrace time.Duration) {
	if h == nil || store == nil {
		return
	}
	if restartGrace < 0 {
		restartGrace = apiSubagentBatchRestartGrace
	}
	h.subagentBatchRecoveryMu.Lock()
	if h.subagentBatchRecoveryStop != nil {
		h.subagentBatchRecoveryMu.Unlock()
		return
	}
	// API 宿主没有 CLI 的 lifecycleCtx（进程即宿主），恢复挂在自己的 root
	// context 上，由 Stop 收敛。
	ctx, cancel := context.WithCancel(context.Background())
	h.subagentBatchRecoveryStop = cancel
	h.subagentBatchRecoveryWG.Add(1)
	h.subagentBatchRecoveryMu.Unlock()

	go func() {
		defer h.subagentBatchRecoveryWG.Done()
		h.runSubagentBatchRecovery(ctx, store, restartGrace)
	}()
}

// StopSubagentBatchRecovery 取消恢复循环并等到 goroutine 真正退出。返回时保证
// 没有残留 goroutine，也不会再有新的恢复写入。未启动时是 no-op。
func (h *Handler) StopSubagentBatchRecovery() {
	if h == nil {
		return
	}
	h.subagentBatchRecoveryMu.Lock()
	cancel := h.subagentBatchRecoveryStop
	h.subagentBatchRecoveryStop = nil
	h.subagentBatchRecoveryMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	h.subagentBatchRecoveryWG.Wait()
}

// runSubagentBatchRecovery 是两趟恢复的调度：立即一趟 + 宽限期后一趟。每趟都
// 有自己的超时，避免大 store 或慢 event store 拖住进程。
func (h *Handler) runSubagentBatchRecovery(ctx context.Context, store subagentbatch.BatchStore, restartGrace time.Duration) {
	if h == nil || store == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	pass := func() bool {
		if ctx.Err() != nil {
			return false
		}
		passCtx, cancel := context.WithTimeout(ctx, apiSubagentBatchRecoveryPassTimeout)
		_, _, _ = h.runAPISubagentBatchRecoveryOnce(passCtx, store, restartGrace, apiSubagentBatchRecoveryLimit)
		cancel()
		return ctx.Err() == nil
	}
	if !pass() {
		return
	}
	if restartGrace <= 0 {
		return
	}
	timer := time.NewTimer(restartGrace)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		pass()
	}
}

// EnableDurableSubagentBatches 是 runtime-server 的接线入口：在给定目录下建一个
// file-backed batch store（跨重启可见），注入宿主并随即启动两趟有界恢复。
//
// 缺口背景（2026-09-16）：API 宿主此前从不注入 batch store，于是
// getSubagentBatchStore 的惰性默认值是 per-process 的内存库（resolveBatchDSN
// 在无 Path/DSN 时生成 uuid 内存 DSN），重启后既没有可恢复的行，也没有地方记
// 恢复结果。把 store 落到与 supervision 同级的数据目录，恢复才有意义。
//
// 返回的 io.Closer 必须在宿主关闭前关闭（先 StopSubagentBatchRecovery，再
// Close），否则会留下等待宽限期的 goroutine 在已关闭的库上扫描。
// 目录不可写/建库失败时返回错误，由调用方决定降级策略（runtime-server 只告警，
// 与 usage ledger 同口径：没有跨重启 batch 控制面不等于服务不可用）。
func (h *Handler) EnableDurableSubagentBatches(dir string) (io.Closer, error) {
	if h == nil {
		return nil, fmt.Errorf("api handler is nil")
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, fmt.Errorf("subagent batch store dir is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create subagent batch store dir: %w", err)
	}
	store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(dir, "batches.db"),
	})
	if err != nil {
		return nil, fmt.Errorf("open subagent batch store: %w", err)
	}
	h.SetSubagentBatchStore(store)
	h.StartSubagentBatchRecovery()
	closer, _ := store.(io.Closer)
	return closer, nil
}
