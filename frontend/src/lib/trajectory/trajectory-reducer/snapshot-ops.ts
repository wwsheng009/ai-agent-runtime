// 由 lib/trajectory/trajectory-reducer.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TrajectoryChange, type TrajectoryHead, type TrajectoryItem, type TrajectoryItemKind, type TrajectoryItemStatus, type TrajectorySnapshot } from "../types";

export const TERMINAL_STATUSES: ReadonlySet<TrajectoryItemStatus> = new Set([
  "completed",
  "failed",
  "canceled",
]);

export function cloneSnapshot(snapshot: TrajectorySnapshot): TrajectorySnapshot {
  return {
    items: snapshot.items.map((item) => ({ ...item })),
    nextId: snapshot.nextId,
    lastEventSeq: snapshot.lastEventSeq,
    revisions: { ...snapshot.revisions },
    pending: { ...snapshot.pending },
  };
}

export function cloneItem(item: TrajectoryItem): TrajectoryItem {
  return { ...item, head: { ...item.head } };
}

export function appendChange(
  changes: TrajectoryChange[],
  op: TrajectoryChange["op"],
  itemId: string,
  item: TrajectoryItem | undefined,
  revision: number,
) {
  changes.push({ op, itemId, item, revision });
}

function headsEqual(a: TrajectoryHead, b: TrajectoryHead): boolean {
  if (a.kind !== b.kind) {
    return false;
  }
  switch (a.kind) {
    case "text":
      return b.kind === "text" && a.content === b.content;
    case "reasoning":
      return b.kind === "reasoning" && a.content === b.content;
    case "tool":
      return (
        b.kind === "tool" &&
        a.name === b.name &&
        a.phase === b.phase &&
        a.argsSummary === b.argsSummary &&
        a.resultSummary === b.resultSummary &&
        a.errorMessage === b.errorMessage
      );
    case "structured":
      return (
        b.kind === "structured" &&
        JSON.stringify(a.payload) === JSON.stringify(b.payload)
      );
    case "system":
      return b.kind === "system" && a.note === b.note;
  }
}

export function findItem(
  snapshot: TrajectorySnapshot,
  itemId: string,
): TrajectoryItem | undefined {
  return snapshot.items.find((item) => item.id === itemId);
}

/**
 * upsert：按 itemId 更新既有 Item；找不到则退化为 append（spec §4.2 幂等规则）。
 * 同 ID 同内容重复 upsert → 跳过（幂等）；终态后 upsert → 拒绝（仅允许 remove）。
 */
export function upsertItem(
  snapshot: TrajectorySnapshot,
  changes: TrajectoryChange[],
  itemId: string,
  kind: TrajectoryItemKind,
  head: TrajectoryHead,
  status: TrajectoryItemStatus,
  eventSeq: number,
  causeId = "",
) {
  const existing = findItem(snapshot, itemId);
  if (existing) {
    if (TERMINAL_STATUSES.has(existing.status)) {
      return; // 终态冻结：拒绝 upsert
    }
    if (headsEqual(existing.head, head) && existing.status === status) {
      return; // 幂等跳过
    }
    const next = cloneItem(existing);
    next.head = head;
    next.status = status;
    next.updatedAt = eventSeq;
    const revision = (snapshot.revisions[itemId] ?? 0) + 1;
    snapshot.revisions[itemId] = revision;
    snapshot.items = snapshot.items.map((item) =>
      item.id === itemId ? next : item,
    );
    appendChange(changes, "upsert", itemId, next, revision);
    return;
  }

  const item: TrajectoryItem = {
    id: itemId,
    seq: eventSeq,
    kind,
    causeId,
    status,
    head,
    createdAt: eventSeq,
    updatedAt: eventSeq,
  };
  snapshot.items = [...snapshot.items, item];
  snapshot.nextId += 1;
  const revision = (snapshot.revisions[itemId] ?? 0) + 1;
  snapshot.revisions[itemId] = revision;
  appendChange(changes, "append", itemId, item, revision);
}
