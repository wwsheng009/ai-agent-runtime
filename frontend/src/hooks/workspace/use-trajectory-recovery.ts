/**
 * 轨迹断线恢复（P3-1）：会话选中/页面刷新后，从 EventStore 增量拉取
 * `chat.sse.*` 事件并按 seq 重放进轨迹 reducer。
 *
 * - 触发：sessionId 变化（挂载/线程切换，workspace-page 已同步 reset）；
 * - 分页：after = 已收最大 seq（后端 ListEvents 注入 payload.seq）；
 * - 幂等：reducer 乱序缓冲 + 稳定 ID upsert 保证与实时流并发安全；
 * - 失败可见：下拉失败时通过 onError 上报 UI（thread lastError banner），
 *   不再静默——实时流 / history sync 仍兜底内容，但连接问题必须可感知。
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
