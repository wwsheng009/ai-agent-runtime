/**
 * 轨迹断线恢复（P3-1）：会话选中/页面刷新后，从 EventStore 增量拉取
 * `chat.sse.*` 事件并按 seq 重放进轨迹 reducer。
 *
 * - 触发：sessionId 变化（挂载/线程切换，workspace-page 已同步 reset）；
 * - 分页：after = 已收最大 seq（后端 ListEvents 注入 payload.seq）；
 * - 幂等：reducer 乱序缓冲 + 稳定 ID upsert 保证与实时流并发安全；
 * - 失败静默：恢复仅尽力而为，实时流 / history sync 仍是兜底。
 */
import { useEffect, useRef } from "react";

import { fetchSessionRuntimeEvents } from "@/api/runtime/sessions";
import type { TrajectoryStore } from "@/hooks/workspace/use-trajectory-snapshot";
import {
  nextRecoveryAfter,
  trajectoryEventAction,
  TRAJECTORY_RECOVERY_PAGE_SIZE,
} from "@/lib/trajectory/recovery";

type TrajectoryRecoveryOptions = {
  store: TrajectoryStore;
  sessionId: string | undefined;
  /** 页面可见时才恢复（后台 tab 不抢带宽；默认 true）。 */
  enabled?: boolean;
};

export function useTrajectoryRecovery({
  store,
  sessionId,
  enabled = true,
}: TrajectoryRecoveryOptions) {
  const storeRef = useRef(store);
  storeRef.current = store;
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
        let after = 0;
        for (;;) {
          const { events } = await fetchSessionRuntimeEvents(sessionId, {
            after,
            limit: TRAJECTORY_RECOVERY_PAGE_SIZE,
          });
          if (cancelled) {
            return;
          }
          // 事件按 seq 升序返回；逐个处理：可渲染的 push 进 reducer，
          // 被过滤的事件（tool_started/tool_finished 等共享同一 EventStore
          // 全局 seq）advanceCursor 跳过空洞，避免空洞之后的事件永久 pending。
          for (const event of events) {
            const action = trajectoryEventAction(event);
            if (action.kind === "push") {
              storeRef.current.push(action.push.kind, action.push.payload);
            } else if (action.kind === "skip") {
              storeRef.current.advanceCursor(action.seq);
            }
          }
          if (events.length === 0 || events.length < TRAJECTORY_RECOVERY_PAGE_SIZE) {
            break;
          }
          after = nextRecoveryAfter(events, after);
        }
        storeRef.current.flush();
        recoveredSessionRef.current = sessionId;
      } catch {
        // 恢复失败静默：实时流 / history sync 兜底，不阻塞 UI。
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [enabled, sessionId]);
}
