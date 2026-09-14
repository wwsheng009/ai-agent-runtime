// 批次 3（方案 §5.5）：侧栏会话「分支谱系」的纯口径（无 IO、无 React）。
//
// 谱系来源两选一，统一归一为 `SessionLineageRef`：
// - `Thread.forkedFrom`（由 `lib/thread-state/sessions.ts` 从快照 metadata.context 映射）；
// - 运行时会话记录自身的 `metadata.context` 谱系键（侧栏分组行直接读，不依赖线程物化）。
//
// 口径约束（对齐方案 §5.5）：
// - 只认「父也在这批行里」的父子边：父行跨组 / 被过滤 / 已消失时行保持原位；
// - 子行紧随父行，但**只移动子行**：父行与其余行的相对顺序（含手动排序账目）不被改写，
//   因此父行被手动移动时子簇自然跟随；
// - 折叠口径：子行计入 `limit`、不单独折叠——子行紧邻父行之后，按前缀裁剪时
//   「子行可见 ⇒ 父行可见」（`session-grouping.ts` 的裁剪口径无需改动）；
// - 层级只到 0/1（方案 §1.4 非目标：不做消息树 / 多级分支图）。

import { type Thread } from "@/data/mock";

/** 后端建分支会话时写入 `metadata.context` 的谱系键（缺键 = 非分支会话）。 */
export const SESSION_FORK_CONTEXT_KEYS = {
  parentSessionId: "fork_parent_session_id",
  sourceMessageId: "fork_source_message_id",
  originTitle: "fork_origin_title",
} as const;

/** 分支来源引用：与 `Thread.forkedFrom`、后端谱系键同构。 */
export type SessionLineageRef = NonNullable<Thread["forkedFrom"]>;

/** 父行身份 → 子行身份（按输入相对顺序；父不在同批行里 / 自引用 / 重复项不记录）。 */
export type SessionLineageIndex = ReadonlyMap<string, readonly string[]>;

/**
 * 可参与谱系结算的最小行形状：
 * - `Thread`（id / sessionId / 已映射的 forkedFrom / title）；
 * - `RuntimeSessionRecord`（id / metadata.context 原始谱系键 / metadata.title）。
 * 本地在途线程的 `id` 可能不等于会话 id，因此身份一律以 `sessionId` 优先。
 */
export type SessionLineageRow = {
  id: string;
  /** 会话身份；缺省回落到行 id。 */
  sessionId?: string | null;
  forkedFrom?: SessionLineageRef | null;
  metadata?: { context?: unknown; title?: string } | null;
  title?: string;
};

/** 行身份：优先会话 id（本地线程 id 只是占位），空值回落到行 id。 */
export function lineageRowIdentity(row: SessionLineageRow): string {
  const sessionId = readNonEmptyString(row.sessionId);
  return sessionId ?? readNonEmptyString(row.id) ?? "";
}

/** 从 `metadata.context` 防御性读取谱系：缺 `fork_parent_session_id`（或值为空）即非分支会话。 */
export function readSessionLineage(
  context: unknown,
): SessionLineageRef | undefined {
  if (typeof context !== "object" || context === null || Array.isArray(context)) {
    return undefined;
  }
  const record = context as Record<string, unknown>;
  const sessionId = readNonEmptyString(
    record[SESSION_FORK_CONTEXT_KEYS.parentSessionId],
  );
  if (!sessionId) {
    return undefined;
  }
  const anchorMessageId = readNonEmptyString(
    record[SESSION_FORK_CONTEXT_KEYS.sourceMessageId],
  );
  const originTitle = readNonEmptyString(
    record[SESSION_FORK_CONTEXT_KEYS.originTitle],
  );
  return {
    sessionId,
    ...(anchorMessageId ? { anchorMessageId } : {}),
    ...(originTitle ? { originTitle } : {}),
  };
}

/** 行上的谱系来源：优先已映射的 `forkedFrom`，否则读原始 context 键。 */
export function readRowLineage(
  row: SessionLineageRow,
): SessionLineageRef | undefined {
  return row.forkedFrom ?? readSessionLineage(row.metadata?.context);
}

/** 两次快照映射出的谱系是否等价（合并快照时的变更检测）。 */
export function isSameSessionLineage(
  left: SessionLineageRef | undefined,
  right: SessionLineageRef | undefined,
): boolean {
  return (
    left?.sessionId === right?.sessionId &&
    left?.anchorMessageId === right?.anchorMessageId &&
    left?.originTitle === right?.originTitle
  );
}

/**
 * 父 id → 子列表 的索引。只认「父也在这批行里」的边：父不在行集合里
 * （跨组 / 被过滤 / 已消失）时不建立父子关系，调用方据此保持原位。
 */
export function buildLineageIndex<T extends SessionLineageRow>(
  rows: readonly T[],
): SessionLineageIndex {
  const positions = firstIdentityPositions(rows);
  const index = new Map<string, string[]>();

  for (const row of rows) {
    const lineage = readRowLineage(row);
    if (!lineage) {
      continue;
    }
    const parentId = lineage.sessionId;
    const parentPosition = positions.get(parentId);
    if (parentPosition === undefined) {
      continue;
    }
    const childId = lineageRowIdentity(row);
    if (!childId || positions.get(childId) === parentPosition) {
      // 身份缺失 / 自引用：不建立父子边（脏数据防御）。
      continue;
    }
    const children = index.get(parentId);
    if (children === undefined) {
      index.set(parentId, [childId]);
    } else if (!children.includes(childId)) {
      children.push(childId);
    }
  }

  return index;
}

/**
 * 层级稳定化：子行恒紧随父行（不论原位置在父行之前还是之后）。
 * - 父不在 `rows` 里（跨组 / 被过滤 / 已消失）→ 行保持原位；无位移时返回入参引用；
 * - 多子行保持原有相对顺序；自引用与环形谱系（脏数据）不参与重排；
 * - 只移动子行、不移动父行：用户显式排过的行序不被层级改写，父行被移动时子簇跟随；
 * - `options.anchoredIds`（§5.5 约束 3）：手动账目里显式摆过的行不参与位移——
 *   其显式位置优先于层级稳定化；父行被手动移到别处时，其**非锚定**子簇仍旧跟随。
 */
export function stabilizeLineageOrder<T extends SessionLineageRow>(
  rows: readonly T[],
  index: SessionLineageIndex,
  options?: { anchoredIds?: ReadonlySet<string> | undefined },
): readonly T[] {
  const anchoredIds = options?.anchoredIds;
  if (rows.length < 2 || index.size === 0) {
    return rows;
  }

  const positions = firstIdentityPositions(rows);
  // 行下标 → 父行下标：只保留父行也在本批 rows 里的边。
  const parentOf = new Map<number, number>();
  for (const [parentId, children] of index) {
    const parentPosition = positions.get(parentId);
    if (parentPosition === undefined) {
      continue;
    }
    for (const childId of children) {
      if (anchoredIds?.has(childId)) {
        // 用户显式排过的子行：位置优先，不跟随父行重排。
        continue;
      }
      const childPosition = positions.get(childId);
      if (
        childPosition === undefined ||
        childPosition === parentPosition ||
        parentOf.has(childPosition)
      ) {
        continue;
      }
      parentOf.set(childPosition, parentPosition);
    }
  }
  dropCyclicLineage(parentOf);
  if (parentOf.size === 0) {
    return rows;
  }

  const childrenOf = new Map<number, number[]>();
  for (const [childPosition, parentPosition] of parentOf) {
    const children = childrenOf.get(parentPosition);
    if (children === undefined) {
      childrenOf.set(parentPosition, [childPosition]);
    } else {
      children.push(childPosition);
    }
  }

  const emitted = new Set<number>();
  const order: number[] = [];

  const emit = (position: number) => {
    if (emitted.has(position)) {
      return;
    }
    emitted.add(position);
    order.push(position);
    for (const childPosition of childrenOf.get(position) ?? []) {
      emit(childPosition);
    }
  };

  for (let position = 0; position < rows.length; position += 1) {
    const parentPosition = parentOf.get(position);
    if (parentPosition !== undefined && !emitted.has(parentPosition)) {
      // 父行还没发出：挂起，等父行发出时紧随其后（不管原位置在父行前还是后）。
      continue;
    }
    emit(position);
  }
  // 兜底（理论不可达）：去环后父行必先发出，保证一行不丢。
  for (let position = 0; position < rows.length; position += 1) {
    emit(position);
  }

  const changed = order.some((position, current) => position !== current);
  return changed ? order.map((position) => rows[position]) : rows;
}

/**
 * 行层级：父在本批可见（索引里有该父边）→ 1，否则 0。
 * `index` 必须与调用方渲染的行集合同源（同一批行），否则跨组父行会被误判为可见。
 */
export function resolveRowDepth(
  row: SessionLineageRow,
  index: SessionLineageIndex,
): 0 | 1 {
  const lineage = readRowLineage(row);
  if (!lineage) {
    return 0;
  }
  const identity = lineageRowIdentity(row);
  if (!identity || identity === lineage.sessionId) {
    return 0;
  }
  return index.has(lineage.sessionId) ? 1 : 0;
}

/**
 * 徽标提示用的来源标题：优先后端谱系键 `fork_origin_title`，
 * 缺失时回落到父行标题；均不可得时返回 undefined（不伪造标题）。
 */
export function resolveLineageOriginTitle(
  row: SessionLineageRow,
  rows: readonly SessionLineageRow[],
): string | undefined {
  const lineage = readRowLineage(row);
  if (!lineage) {
    return undefined;
  }
  if (lineage.originTitle) {
    return lineage.originTitle;
  }
  const parent = rows.find(
    (candidate) => lineageRowIdentity(candidate) === lineage.sessionId,
  );
  return readNonEmptyString(parent?.title) ?? readNonEmptyString(parent?.metadata?.title);
}

/** 身份 → 首个出现下标（重复身份只认第一条，结算时不丢行）。 */
function firstIdentityPositions(
  rows: readonly SessionLineageRow[],
): Map<string, number> {
  const positions = new Map<string, number>();
  rows.forEach((row, position) => {
    const identity = lineageRowIdentity(row);
    if (identity && !positions.has(identity)) {
      positions.set(identity, position);
    }
  });
  return positions;
}

/** 去掉成环的谱系边（脏数据防御）：成环的行不参与层级重排，保持原位。 */
function dropCyclicLineage(parentOf: Map<number, number>): void {
  for (const start of [...parentOf.keys()]) {
    const seen = new Set<number>([start]);
    let current = parentOf.get(start);
    while (current !== undefined) {
      if (seen.has(current)) {
        parentOf.delete(start);
        break;
      }
      seen.add(current);
      current = parentOf.get(current);
    }
  }
}

function readNonEmptyString(value: unknown): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  const trimmed = value.trim();
  return trimmed ? trimmed : undefined;
}
