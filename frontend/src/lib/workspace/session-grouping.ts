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
