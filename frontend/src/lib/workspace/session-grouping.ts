// P2-6 子片 2：侧栏会话**分组视图**的纯口径（无 IO、无 React）。
// 分组视图（工作区分组 / 平铺）与排序双模式正交：分组只决定呈现形状，
// 不改分组口径（仍按会话元数据的工作目录归属分组），也不改写排序账目。

export const SESSION_GROUPING_MODES = ["directory", "flat"] as const;

export type SessionGroupingMode = (typeof SESSION_GROUPING_MODES)[number];

export function isSessionGroupingMode(
  value: unknown,
): value is SessionGroupingMode {
  return (
    typeof value === "string" &&
    (SESSION_GROUPING_MODES as readonly string[]).includes(value)
  );
}

/**
 * 平铺视图的会话序列：按分组顺序 → 组内顺序拼接。
 *
 * 平铺与分组视图共享同一份「组内账目」——平铺只是去掉组头，不引入第二套
 * 顺序账目，因此在两个视图之间切换不会让手动顺序在两者间分叉。
 */
export function flattenSessionDirectoryGroups<S>(
  groups: readonly { sessions: readonly S[] }[],
): S[] {
  return groups.flatMap((group) => [...group.sessions]);
}

/** 组内渐进呈现的默认可见上限（超出部分折叠为「展开其余 N 个」）。 */
export const SESSION_GROUP_VISIBLE_LIMIT = 5;

export type SessionGroupVisibility<T> = {
  /** 实际渲染的会话（保持传入顺序）。 */
  visible: readonly T[];
  /** 被折叠隐藏的会话数（展开时为 0）。 */
  hiddenCount: number;
  /** 是否给出展开/收起控件：只有超出上限的组才有。 */
  collapsible: boolean;
  /** 生效的展开态（选中会话落在折叠区时自动展开）。 */
  expanded: boolean;
};

/**
 * 组内展开 / 折叠（Show N more）的纯判定：
 * - 不超出上限的组整体可见，且不给出控件；
 * - 折叠态只渲染前 `limit` 个；`pinnedId`（当前选中会话）落在折叠区时自动展开，
 *   避免出现「已选中却看不见」的假态；
 * - 只影响呈现，不改会话顺序，也不读写任何排序账目。
 */
export function resolveSessionGroupVisibility<T extends { id: string }>(
  sessions: readonly T[],
  options: { expanded: boolean; pinnedId?: string | null; limit?: number },
): SessionGroupVisibility<T> {
  const limit = Math.max(
    1,
    Math.trunc(options.limit ?? SESSION_GROUP_VISIBLE_LIMIT),
  );
  if (sessions.length <= limit) {
    return {
      visible: sessions,
      hiddenCount: 0,
      collapsible: false,
      expanded: true,
    };
  }

  const pinnedIndex =
    options.pinnedId == null
      ? -1
      : sessions.findIndex((session) => session.id === options.pinnedId);
  if (options.expanded || pinnedIndex >= limit) {
    return {
      visible: sessions,
      hiddenCount: 0,
      collapsible: true,
      expanded: true,
    };
  }

  return {
    visible: sessions.slice(0, limit),
    hiddenCount: sessions.length - limit,
    collapsible: true,
    expanded: false,
  };
}
