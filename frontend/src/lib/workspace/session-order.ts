// P2-6 子片 1：侧栏会话顺序的纯口径（无 IO、无 React）。
// 与目标项目 `ui-workspace/src/client/rows/WorkspaceBrowser.tsx` 的
// `reconciledSessionOrder` / `compareSessionRecency` 同口径：账目顺序优先、
// 账目里已消失的会话忽略、未入账的新会话按最近更新顺序追加到末尾。
// 本批只维护**浏览器本地**顺序，不做 Host 写回（跨组移动见后续子片）。

export const SESSION_ORDER_MODES = ["updated", "manual"] as const;

export type SessionOrderMode = (typeof SESSION_ORDER_MODES)[number];

/** 一个分组的顺序账目：该分组下一个会话 id 的显式顺序（浏览器本地）。 */
export type SessionOrderAccount = readonly string[];

/** 分组键（目录 id / 路径键）→ 顺序账目。 */
export type SessionOrderAccounts = Readonly<Record<string, SessionOrderAccount>>;

export type SessionOrderCandidate = {
  id: string;
  updatedAt?: string;
  createdAt?: string;
};

export function isSessionOrderMode(value: unknown): value is SessionOrderMode {
  return (
    typeof value === "string" &&
    (SESSION_ORDER_MODES as readonly string[]).includes(value)
  );
}

/**
 * 排序用的最近更新时间（毫秒）。时间缺失或非法一律按 `-Infinity`：
 * 沉底且不参与「自上次观察以来发生过更新」的判定，不做任何时间猜测。
 */
export function sessionRecencyTime(session: SessionOrderCandidate): number {
  const parsed = Date.parse(session.updatedAt || session.createdAt || "");
  return Number.isFinite(parsed) ? parsed : Number.NEGATIVE_INFINITY;
}

/** 最近更新优先；同一时刻按会话 id 升序（稳定 tiebreak，与会话列表既有口径一致）。 */
export function compareSessionsByRecency(
  left: SessionOrderCandidate,
  right: SessionOrderCandidate,
): number {
  const leftTime = sessionRecencyTime(left);
  const rightTime = sessionRecencyTime(right);
  if (leftTime !== rightTime) {
    return rightTime - leftTime;
  }
  return left.id.localeCompare(right.id);
}

export function isSameSessionOrder(
  left: readonly string[],
  right: readonly string[],
): boolean {
  return (
    left.length === right.length &&
    left.every((id, index) => id === right[index])
  );
}

/**
 * 把账目顺序对齐到当前会话集合：账目里已消失的会话忽略、重复项忽略；
 * 账目里没有的会话按入参顺序（调用方已按最近更新排好）追加到末尾。
 * 账目缺省时原样返回入参顺序。
 */
export function reconcileSessionOrder(
  sessionIds: readonly string[],
  stored: SessionOrderAccount | undefined,
): string[] {
  if (stored === undefined) {
    return [...sessionIds];
  }

  const present = new Set(sessionIds);
  const ordered: string[] = [];
  const included = new Set<string>();
  for (const id of stored) {
    if (!present.has(id) || included.has(id)) {
      continue;
    }
    ordered.push(id);
    included.add(id);
  }
  for (const id of sessionIds) {
    if (included.has(id)) {
      continue;
    }
    ordered.push(id);
  }
  return ordered;
}

/**
 * 按排序模式取分组内的会话顺序：
 * - `updated`：恒按最近更新实时排序，**不读也不写**账目；
 * - `manual`：有账目按账目（新会话追加到末尾），无账目按最近更新呈现
 *   （账目只在用户拖拽时建立，因此不会在渲染期写存储）。
 * 返回新数组，不改动入参。
 */
export function orderSessionsForMode<T extends SessionOrderCandidate>(
  sessions: readonly T[],
  mode: SessionOrderMode,
  stored: SessionOrderAccount | undefined,
): T[] {
  const recencySorted = [...sessions].sort(compareSessionsByRecency);
  if (mode === "updated") {
    return recencySorted;
  }

  const byId = new Map(recencySorted.map((session) => [session.id, session]));
  return reconcileSessionOrder(
    recencySorted.map((session) => session.id),
    stored,
  ).flatMap((id) => {
    const session = byId.get(id);
    return session === undefined ? [] : [session];
  });
}

export type SessionDropEdge = "before" | "after";

/**
 * 拖拽结算：把 `sourceId` 放到 `targetId` 之前 / 之后。
 * 任一侧不在顺序里、或落点与现状等价时返回**原数组引用**（调用方据此跳过写入）。
 */
export function moveSessionInOrder(
  order: readonly string[],
  sourceId: string,
  targetId: string,
  edge: SessionDropEdge,
): readonly string[] {
  if (sourceId === targetId) {
    return order;
  }

  const withoutSource = order.filter((id) => id !== sourceId);
  if (withoutSource.length === order.length || !order.includes(targetId)) {
    return order;
  }

  const anchorIndex = withoutSource.indexOf(targetId);
  if (anchorIndex === -1) {
    return order;
  }

  const next = [...withoutSource];
  next.splice(edge === "before" ? anchorIndex : anchorIndex + 1, 0, sourceId);
  return isSameSessionOrder(next, order) ? order : next;
}
