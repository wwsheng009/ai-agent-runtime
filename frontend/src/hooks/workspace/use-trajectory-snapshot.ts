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

/**
 * 前插/重建单元：与 `trajectoryEventAction()` 同形（push / skip 两种动作）。
 *
 * 尾部优先回放（tail-first）需要「把更早的一页插到已回放窗口前面」，而
 * reducer 的投影是有状态的单向追加（delta 合并、游标消费），无法真正前插——
 * 因此 store 自己保留一份已应用动作日志，前插后**整体重建**投影：log 顺序
 * 恒为「已加载事件按 seq 升序」，重建即从空快照重放到尾，结果与一次性回放
 * 同一份事件完全一致（幂等、无重复行）。
 */
export type TrajectoryRawAction =
  | { type: "push"; kind: TrajectoryEventKind; payload: Record<string, unknown> }
  | { type: "skip"; seq: number };

/** 重建日志上限：超大会话下最老的动作会被挤出（只影响更早页的重建保真度）。 */
const MAX_REPLAY_ACTIONS = 8000;

function rawActionSeq(action: TrajectoryRawAction): number {
  return action.type === "skip" ? action.seq : eventSeqOf(action.payload);
}

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
  /**
   * 设定基准游标：窗口起点之前的 seq 视为「已跳过」。
   *
   * 尾部优先回放的首屏只回放最近一页事件（seq 从中间开始），若没有基准游标，
   * reducer 会把首个事件当作「乱序待补」，永久卡在 pending——轨迹一行都不渲染。
   * 仅在快照为空（首屏）或前插更早页前调用。
   */
  setBaselineSeq(seq: number): void;
  /**
   * 前插更早的事件帧并重建投影（「加载更早」）：保留已加载事件与实时游标，
   * 重建后快照与「一次性回放全部已加载事件」完全一致。
   */
  prependEarlier(actions: TrajectoryRawAction[]): void;
  /**
   * 回放日志是否已被上限裁剪（`MAX_REPLAY_ACTIONS`）。
   *
   * 裁剪丢弃的是**最老**的动作，而 `prependEarlier` 会整体重建投影：重建时
   * 最早保留动作之前的行无法复现（reducer 按游标丢弃过期 seq）。此时再前插
   * 更早页只会「加载即被裁掉」，调用方应停止提供「加载更早」入口，避免
   * 已渲染的最早行被静默吞掉。仅 `reset({ hard: true })` 会清除该标记。
   */
  isReplayLogTruncated(): boolean;
}

export function createTrajectoryStore(options?: {
  fallbackDelayMs?: number;
}): TrajectoryStore {
  let snapshot = createEmptyTrajectory();
  const listeners = new Set<() => void>();
  const seenDeltaKeys = new Set<string>();
  /** 已应用动作日志（前插更早页后用于重建；顺序 = 事件 seq 升序）。 */
  let replayLog: TrajectoryRawAction[] = [];
  /** 日志被上限裁剪过（更早动作已永久丢失，无法再忠实重建）。 */
  let replayLogTruncated = false;
  /** 窗口起点之前的缺口游标（尾部优先首屏；0 = 从 seq 1 起完整回放）。 */
  let baselineSeq = 0;

  const recordAction = (action: TrajectoryRawAction) => {
    replayLog.push(action);
    if (replayLog.length > MAX_REPLAY_ACTIONS) {
      replayLog.splice(0, replayLog.length - MAX_REPLAY_ACTIONS);
      replayLogTruncated = true;
    }
  };

  const notify = () => {
    for (const listener of listeners) {
      listener();
    }
  };

  const flush = () => {
    batcher.flushNow();
  };

  const advanceCursorEntry = (targetSeq: number, record: boolean) => {
    batcher.flushNow();
    const before = snapshot.lastEventSeq;
    const result = advanceSeqCursor(snapshot, targetSeq);
    const moved = result.snapshot.lastEventSeq !== before || result.changes.length > 0;
    snapshot = result.snapshot;
    if (record) {
      recordAction({ type: "skip", seq: targetSeq });
    }
    if (moved) {
      notify();
    }
  };

  const reset = (options?: { hard?: boolean }) => {
    batcher.clear();
    if (options?.hard) {
      snapshot = createEmptyTrajectory();
      seenDeltaKeys.clear();
      replayLog = [];
      replayLogTruncated = false;
      baselineSeq = 0;
    } else {
      snapshot = {
        ...createEmptyTrajectory(),
        lastEventSeq: snapshot.lastEventSeq,
      };
    }
    notify();
  };

  const pushEntry = (
    kind: TrajectoryEventKind,
    payload: Record<string, unknown> | null | undefined,
    record: boolean,
  ) => {
    const normalizedPayload = payload ?? {};
    if (record) {
      recordAction({ type: "push", kind, payload: normalizedPayload });
    }
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
  };

  /** 从零重建投影（前插更早页后调用）：基准缺口 → 日志顺序回放。 */
  const rebuildFromLog = () => {
    batcher.clear();
    snapshot = createEmptyTrajectory();
    seenDeltaKeys.clear();
    // 有效基准缺口：随「加载更早」向前推进而缩小。若仍按首屏基准（例如
    // firstSeq-1 = 799）跳过，前插进来的更老事件（seq < 799）会被 reducer
    // 判为过期 seq 直接丢弃——更早一页就白加载了。故取「日志最早动作之前」。
    const oldestSeq = replayLog.length > 0 ? rawActionSeq(replayLog[0]) : 0;
    const effectiveBaseline =
      oldestSeq > 0 && oldestSeq <= baselineSeq ? oldestSeq - 1 : baselineSeq;
    if (effectiveBaseline > 0) {
      snapshot = advanceSeqCursor(snapshot, effectiveBaseline).snapshot;
    }
    for (const action of replayLog) {
      if (action.type === "push") {
        pushEntry(action.kind, action.payload, false);
      } else {
        advanceCursorEntry(action.seq, false);
      }
    }
    batcher.flushNow();
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
    push: (kind, payload) => pushEntry(kind, payload, true),
    flush,
    advanceCursor: (targetSeq) => advanceCursorEntry(targetSeq, true),
    reset,
    setBaselineSeq: (seq) => {
      if (!Number.isFinite(seq) || seq <= baselineSeq) {
        return;
      }
      baselineSeq = seq;
      // 首屏：快照为空时直接把游标推到缺口位置，后续事件（seq > baselineSeq）
      // 因此落在「连续区间」内被立即应用，而不是全部卡在乱序缓冲。
      if (snapshot.lastEventSeq < seq) {
        advanceCursorEntry(seq, false);
      }
    },
    prependEarlier: (actions) => {
      if (actions.length === 0) {
        return;
      }
      const known = new Set(replayLog.map((action) => rawActionSeq(action)));
      const older = actions.filter((action) => {
        const seq = rawActionSeq(action);
        if (seq <= 0) {
          return false;
        }
        if (known.has(seq)) {
          return false;
        }
        known.add(seq);
        return true;
      });
      if (older.length === 0) {
        return;
      }
      // 更早页整体前插（页内已是升序）；上限裁剪与 recordAction 同口径。
      replayLog = [...older, ...replayLog];
      if (replayLog.length > MAX_REPLAY_ACTIONS) {
        replayLog.splice(0, replayLog.length - MAX_REPLAY_ACTIONS);
        replayLogTruncated = true;
      }
      rebuildFromLog();
    },
    isReplayLogTruncated: () => replayLogTruncated,
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
