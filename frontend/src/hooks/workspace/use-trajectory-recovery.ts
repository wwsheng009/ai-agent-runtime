/**
 * 轨迹恢复（P3-1 事件重放 + P4 历史兜底）：会话选中/页面刷新后
 * 从 EventStore 增量拉取事件并按 seq 重放进轨迹 reducer。
 *
 * - 触发：sessionId 变化（挂载/线程切换，workspace-page 已同步 reset）；
 * - 分页：after = 已收最大 seq（后端 ListEvents 注入 payload.seq）；
 * - 幂等：reducer 乱序缓冲 + 稳定 ID upsert 保证与实时流并发安全；
 * - 失败可见：下拉失败时通过 onError 上报 UI（thread lastError banner），
 *   不再静默——实时流 / history sync 仍兜底内容，但连接问题必须可感知。
 *
 * P4 历史兜底：一部分会话（由 aicli 进程内 chat 运行时执行、只落生命周期
 * 事件与诊断事件的会话）EventStore 里没有任何 `chat.sse.*` 内容帧，轨迹于是
 * 只剩 system 行。这类会话的消息本体在持久化会话历史里，因此恢复链路在此
 * 回退到「会话历史 → 轨迹帧」投影（lib/trajectory/session-history.ts）并按
 * turn 插回事件序列，保证轨迹能看到所有消息、且重启（内存态清空）后仍可查看。
 */
import { useEffect, useRef } from "react";

import {
  fetchSessionRuntimeEvents,
} from "@/api/runtime/sessions";
import type { SessionRuntimeEvent } from "@/types/runtime";
import type { TrajectoryStore } from "@/hooks/workspace/use-trajectory-snapshot";
import {
  fetchSessionHistoryMessages,
  hasTrajectoryContentFrames,
  mergeTrajectoryHistoryFallback,
  trajectoryReplaySteps,
} from "@/lib/trajectory/history-fallback";
import {
  nextRecoveryAfter,
  TRAJECTORY_RECOVERY_PAGE_SIZE,
} from "@/lib/trajectory/recovery";

type TrajectoryRecoveryOptions = {
  store: TrajectoryStore;
  sessionId: string | undefined;
  /** 页面可见时才恢复（后台 tab 不抢带宽；默认 true）。 */
  enabled?: boolean;
  /** 增量拉取失败回调（HTTP 错误 / 网络错误）；参数为发起恢复的会话与错误文案。 */
  onError?: (sessionId: string, message: string) => void;
};

export function useTrajectoryRecovery({
  store,
  sessionId,
  enabled = true,
  onError,
}: TrajectoryRecoveryOptions) {
  const storeRef = useRef(store);
  storeRef.current = store;
  const onErrorRef = useRef(onError);
  onErrorRef.current = onError;
  const recoveredSessionRef = useRef<string | null>(null);

  useEffect(() => {
    if (!sessionId || !enabled) {
      return;
    }
    // 同一会话只恢复一次（成功完成后标记）；线程切换（sessionId 变化）
    // 重新恢复。取消/失败不标记——reload 后会话数据短暂清空再恢复时
    // （selectedThread 经 undefined 往返）允许重试，避免恢复永远丢失。
    if (recoveredSessionRef.current === sessionId) {
      return;
    }

    let cancelled = false;

    void (async () => {
      try {
        const events: SessionRuntimeEvent[] = [];
        let after = 0;
        for (;;) {
          const { events: batch } = await fetchSessionRuntimeEvents(sessionId, {
            after,
            limit: TRAJECTORY_RECOVERY_PAGE_SIZE,
          });
          if (cancelled) {
            return;
          }
          for (const event of batch) {
            events.push(event);
          }
          if (batch.length === 0 || batch.length < TRAJECTORY_RECOVERY_PAGE_SIZE) {
            break;
          }
          after = nextRecoveryAfter(batch, after);
        }

        // 事件序列 → 计划（可渲染帧 push；被过滤事件的 seq 空洞 advanceCursor）。
        // 逐个处理保持既有语义：被过滤的 tool_started/context.profile.injected
        // 等事件占据同一 EventStore 全局 seq，必须前移游标否则后续事件永久 pending。
        let plan = trajectoryReplaySteps(events);

        if (!hasTrajectoryContentFrames(events)) {
          const history = await fetchSessionHistoryMessages(
            sessionId,
            () => cancelled,
          );
          if (cancelled) {
            return;
          }
          if (history.length > 0) {
            plan = mergeTrajectoryHistoryFallback(plan, history);
          }
        }

        for (const step of plan) {
          if (step.type === "push") {
            storeRef.current.push(step.push.kind, step.push.payload);
          } else {
            storeRef.current.advanceCursor(step.seq);
          }
        }
        storeRef.current.flush();
        recoveredSessionRef.current = sessionId;
      } catch (error) {
        // 恢复失败不再静默：连接问题必须让用户可见（thread 标为降级）。
        // 已取消的恢复（会话已切换）不再上报，避免把迟到失败错标到
        // 当前（无关）线程上。
        if (cancelled) {
          return;
        }
        const message =
          error instanceof Error && error.message
            ? error.message
            : String(error);
        onErrorRef.current?.(sessionId, message);
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [enabled, sessionId]);
}
