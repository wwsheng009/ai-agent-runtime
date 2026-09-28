// 由 lib/trajectory/trajectory-reducer.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TrajectoryChange, type TrajectoryChangeSet, type TrajectoryEvent, type TrajectorySnapshot } from "../types";

import { applySequencedEvent } from "./apply";
import { appendChange, cloneSnapshot, findItem } from "./snapshot-ops";

/** remove：按 ID 移除；不存在则忽略（幂等）。 */
export function removeItem(
  snapshot: TrajectorySnapshot,
  itemId: string,
): TrajectoryChangeSet {
  const next = cloneSnapshot(snapshot);
  const changes: TrajectoryChange[] = [];
  const existing = findItem(next, itemId);
  if (!existing) {
    return { changes, snapshot: next };
  }
  next.items = next.items.filter((item) => item.id !== itemId);
  const revision = (next.revisions[itemId] ?? 0) + 1;
  next.revisions[itemId] = revision;
  appendChange(changes, "remove", itemId, undefined, revision);
  return { changes, snapshot: next };
}

/**
 * 单条事件的**就地应用核心**（seq 幂等 / 乱序缓冲 / 按序续接缓冲链）。
 *
 * 调用方负责先克隆快照：`applyEvent` 单条克隆，`applyEvents` 整批只克隆一次。
 * 缓冲续接不建数组队列——每步至多续接一个事件，直接用游标推进，既没有
 * `shift()` 的剩余引用搬运，也不产生中间队列（P0-1 排空审计）。
 */
function applySequencedWithBuffer(
  next: TrajectorySnapshot,
  event: TrajectoryEvent,
  changes: TrajectoryChange[],
): void {
  if (event.seq <= 0) {
    // 无持久化 seq（降级帧）：按到达序直接应用，不参与乱序/续传边界。
    changes.push(...applySequencedEvent(next, event));
    return;
  }

  if (event.seq <= next.lastEventSeq) {
    return; // 幂等：重复事件跳过
  }

  if (event.seq > next.lastEventSeq + 1) {
    next.pending[event.seq] = event; // 乱序：缓冲等待前序
    return;
  }

  // 顺序就绪：应用本事件，再消费缓冲中的后续事件（单链推进，无队列数组）。
  let cursor = next.lastEventSeq;
  let current: TrajectoryEvent | undefined = event;
  while (current) {
    cursor = current.seq;
    changes.push(...applySequencedEvent(next, current));
    const following = next.pending[cursor + 1];
    if (following) {
      delete next.pending[cursor + 1];
      current = following;
    } else {
      current = undefined;
    }
  }
  next.lastEventSeq = cursor;
}

/**
 * 应用单条事件：seq 幂等（<= lastEventSeq 跳过）、乱序缓冲、按序应用。
 * 返回变更集与推进后的快照。
 */
export function applyEvent(
  snapshot: TrajectorySnapshot,
  event: TrajectoryEvent,
): TrajectoryChangeSet {
  const next = cloneSnapshot(snapshot);
  const changes: TrajectoryChange[] = [];
  applySequencedWithBuffer(next, event, changes);
  return { changes, snapshot: next };
}

/**
 * 前移游标跳过"已知永久缺失"的持久化 seq 空洞。
 *
 * 背景：后端把 chat.sse.* 事件与 runtime 生命周期事件（tool_started/
 * tool_finished/context.profile.injected 等）写入同一 EventStore，
 * 在同一个全局 seq 序列上自增；前端渲染轨迹时按白名单过滤部分事件
 * （不重复呈现工具生命周期、不呈现 profile 注入等）。被过滤的事件在
 * seq 链上留下永久空洞——没有任何来源会再投递它们——若只在 applyEvent
 * 中等待 lastEventSeq+1，空洞之后（含新一轮 turn 的实时事件）将永远
 * 卡在 pending，轨迹只剩 seq=0 的降级事件（system 行）。
 *
 * 恢复/轮询链路持有完整事件列表，对每个被过滤事件的 seq 调用本函数：
 * 安全边界是绝不跳过已缓冲的真实事件（只跳过第一个 pending 之前的洞），
 * 跳过后再按与 applyEvent 相同的顺序消费循环续接 pending。
 */
export function advanceSeqCursor(
  snapshot: TrajectorySnapshot,
  targetSeq: number,
): TrajectoryChangeSet {
  const next = cloneSnapshot(snapshot);
  const changes: TrajectoryChange[] = [];

  if (targetSeq <= next.lastEventSeq) {
    return { changes, snapshot: next };
  }

  const pendingKeys = Object.keys(next.pending)
    .map((key) => Number(key))
    .filter((seq) => Number.isFinite(seq))
    .sort((left, right) => left - right);
  const firstPending = pendingKeys.length > 0 ? pendingKeys[0] : Infinity;
  const effective = Math.min(targetSeq, firstPending - 1);
  if (effective <= next.lastEventSeq) {
    return { changes, snapshot: next };
  }

  next.lastEventSeq = effective;

  // 与 applyEvent 相同的顺次消费循环：跳过空洞后立即续接可用的 pending
  //（单链推进，无队列数组 / 无 shift）。
  let cursor = next.lastEventSeq;
  let current: TrajectoryEvent | undefined = next.pending[cursor + 1];
  if (current) {
    delete next.pending[cursor + 1];
  }
  while (current) {
    cursor = current.seq;
    changes.push(...applySequencedEvent(next, current));
    const following = next.pending[cursor + 1];
    if (following) {
      delete next.pending[cursor + 1];
      current = following;
    } else {
      current = undefined;
    }
  }
  next.lastEventSeq = cursor;
  return { changes, snapshot: next };
}

/**
 * 批量应用（rAF 帧内一批）：逐个应用并合并 ChangeSet——
 * 同一 Item 的多次变更合并为最新快照（对齐 spec §6 去重合并）。
 */
export function applyEvents(
  snapshot: TrajectorySnapshot,
  events: TrajectoryEvent[],
): TrajectoryChangeSet {
  if (events.length === 0) {
    return { changes: [], snapshot };
  }
  // P0-1：整批只克隆一次快照，再就地顺序应用。原实现逐条调用 applyEvent，
  // 每条事件都整份克隆 items / revisions / pending——批量 K 条 × S 行时是
  // O(K·S) 的对象拷贝，万级积压 + 长会话下会把同一帧的工作一起拖死。
  const next = cloneSnapshot(snapshot);
  const merged = new Map<string, TrajectoryChange>();
  const changes: TrajectoryChange[] = [];
  for (const event of events) {
    changes.length = 0;
    applySequencedWithBuffer(next, event, changes);
    for (const change of changes) {
      merged.set(change.itemId, change);
    }
  }
  return { changes: [...merged.values()], snapshot: next };
}
