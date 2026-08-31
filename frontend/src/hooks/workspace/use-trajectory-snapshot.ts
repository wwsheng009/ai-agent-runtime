/**
 * Trajectory 快照订阅 store + hook
 *
 * - store：流式事件 → reducer → 不可变快照，rAF 批处理节流，订阅通知；
 * - hook：useSyncExternalStore 订阅（Phase 2 轨迹视图直接消费）。
 */
import { useSyncExternalStore } from "react";

import { TrajectoryBatcher } from "@/lib/trajectory/stream-batch";
import {
  advanceSeqCursor,
  applyEvent,
  eventSeqOf,
  makeTrajectoryEvent,
} from "@/lib/trajectory/trajectory-reducer";
import {
  createEmptyTrajectory,
  type TrajectoryEventKind,
  type TrajectorySnapshot,
} from "@/lib/trajectory/types";

const MAX_SEEN_DELTA_KEYS = 2048;

function trajectoryDeltaKey(
  kind: TrajectoryEventKind,
  payload: Record<string, unknown>,
): string {
  const streamId =
    typeof payload.stream_id === "string"
      ? payload.stream_id.trim()
      : typeof payload.streamId === "string"
        ? payload.streamId.trim()
        : "";
  const sequenceValue =
    payload.sequence ?? payload.stream_sequence ?? payload.streamSequence;
  const sequence =
    typeof sequenceValue === "number" || typeof sequenceValue === "string"
      ? String(sequenceValue).trim()
      : "";
  if (!streamId || !sequence) {
    return "";
  }

  const turnValue = payload.turn_id ?? payload.turnId ?? payload.turn;
  const turnId =
    typeof turnValue === "number" || typeof turnValue === "string"
      ? String(turnValue).trim()
      : "";
  const payloadType =
    typeof payload.type === "string" ? payload.type.trim().toLowerCase() : "";
  if (
    kind !== "reasoning" &&
    payloadType !== "" &&
    payloadType !== "text" &&
    payloadType !== "image"
  ) {
    // Tool-call chunks may carry their own stream sequence, but they do not
    // have a mirrored assistant-delta transport and must remain distinct.
    return "";
  }
  const channel =
    kind === "reasoning" || payloadType === "reasoning"
      ? "reasoning"
      : payloadType === "image"
        ? "image"
        : "text";
  return `runtime-delta|${turnId}|${streamId}|${channel}|${sequence}`;
}

export interface TrajectoryStore {
  getSnapshot(): TrajectorySnapshot;
  /** 推送一条轨迹事件（seq 从 payload._event.sequence 提取；0 = 降级按到达序）。 */
  push(
    kind: TrajectoryEventKind,
    payload: Record<string, unknown> | null | undefined,
  ): void;
  /** 立即冲刷挂起事件（页面恢复可见 / turn 收尾）。 */
  flush(): void;
  /**
   * 前移游标跳过"已知永久缺失"的持久化 seq 空洞（被过滤的 runtime
   * 事件占用的序号，如 tool_started/tool_finished/context.profile.injected
   * 等：它们与 chat.sse 事件共享同一 EventStore 全局 seq，但轨迹视图
   * 按白名单过滤，不会再被投递）。恢复/轮询链路逐条调用；不跳过已
   * 缓冲的真实事件，跳过后顺次续接 pending（见 reducer advanceSeqCursor）。
   */
  advanceCursor(targetSeq: number): void;
  /**
   * 清空快照与挂起事件。
   *
   * - 默认软重置：清空渲染项/挂起，但保留 lastEventSeq 续传游标。
   *   后端 EventStore 的持久化 seq 按 session 全局单调自增（跨 turn 不重置），
   *   硬清游标会把下一个 turn 首个事件（seq > 1）永久卡在乱序缓冲，
   *   导致 assistant/reasoning/tool 等行全部不渲染（只残留 seq=0 的 system 行）。
   * - hard=true：连游标一起清空（线程/会话切换，恢复路径会按新会话从 1 重新回放）。
   */
  reset(options?: { hard?: boolean }): void;
  /** 订阅快照变更；返回取消订阅函数。 */
  subscribe(listener: () => void): () => void;
  dispose(): void;
}

export function createTrajectoryStore(options?: {
  fallbackDelayMs?: number;
}): TrajectoryStore {
  let snapshot = createEmptyTrajectory();
  const listeners = new Set<() => void>();
  const seenDeltaKeys = new Set<string>();

  const notify = () => {
    for (const listener of listeners) {
      listener();
    }
  };

  const flush = () => {
    batcher.flushNow();
  };

  const advanceCursor = (targetSeq: number) => {
    batcher.flushNow();
    const before = snapshot.lastEventSeq;
    const result = advanceSeqCursor(snapshot, targetSeq);
    const moved = result.snapshot.lastEventSeq !== before || result.changes.length > 0;
    snapshot = result.snapshot;
    if (moved) {
      notify();
    }
  };

  const reset = (options?: { hard?: boolean }) => {
    batcher.clear();
    if (options?.hard) {
      snapshot = createEmptyTrajectory();
      seenDeltaKeys.clear();
    } else {
      snapshot = {
        ...createEmptyTrajectory(),
        lastEventSeq: snapshot.lastEventSeq,
      };
    }
    notify();
  };

  const batcher = new TrajectoryBatcher({
    fallbackDelayMs: options?.fallbackDelayMs,
    flush: (events) => {
      for (const event of events) {
        snapshot = applyEvent(snapshot, event).snapshot;
      }
      notify();
    },
  });

  return {
    getSnapshot: () => snapshot,
    push: (kind, payload) => {
      const normalizedPayload = payload ?? {};
      const deltaKey = trajectoryDeltaKey(kind, normalizedPayload);
      if (deltaKey) {
        if (seenDeltaKeys.has(deltaKey)) {
          const duplicateSeq = eventSeqOf(normalizedPayload);
          if (duplicateSeq > 0) {
            batcher.push(
              makeTrajectoryEvent("runtime", duplicateSeq, {
                __trajectory_skip: true,
              }),
            );
          }
          return;
        }
        seenDeltaKeys.add(deltaKey);
        while (seenDeltaKeys.size > MAX_SEEN_DELTA_KEYS) {
          const oldest = seenDeltaKeys.values().next().value as string | undefined;
          if (oldest === undefined) {
            break;
          }
          seenDeltaKeys.delete(oldest);
        }
      }
      batcher.push(
        makeTrajectoryEvent(kind, eventSeqOf(normalizedPayload), normalizedPayload),
      );
    },
    flush,
    advanceCursor,
    reset,
    subscribe: (listener) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    dispose: () => {
      batcher.dispose();
      listeners.clear();
      seenDeltaKeys.clear();
    },
  };
}

/** 订阅轨迹快照（Phase 2 轨迹视图消费；turn 期间持续更新）。 */
export function useTrajectorySnapshot(store: TrajectoryStore): TrajectorySnapshot {
  return useSyncExternalStore(
    store.subscribe,
    store.getSnapshot,
    store.getSnapshot,
  );
}
