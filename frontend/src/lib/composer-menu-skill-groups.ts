// 技能候选的 composer 菜单分组构建：
// - `$` 模式：叶子组直出（不做 launcher 下钻）；
// - `+` 模式根层：launcher 入口（下钻展开，与引用组一致），使技能可从按钮菜单发现。
//
// 与 composer-menu.ts 分址的原因：菜单模型受 500 非空行门禁约束，技能分组
// 构建器（leaf）连同通用排序工具一并在此维护；本模块只依赖
// composer-menu 的类型（type-only import，无运行时循环依赖）。

import type {
  ComposerMenuGroup,
  ComposerReferenceGroup,
} from "@/lib/composer-menu";

export const COMPOSER_MENU_MAX_ITEMS_PER_GROUP = 8;

export function rankMatch(label: string, extra: string, query: string): number {
  if (query.length === 0) {
    return 0;
  }
  const haystack = `${label} ${extra}`.toLowerCase();
  if (!haystack.includes(query)) {
    return -1;
  }
  return label.toLowerCase().startsWith(query) ? 0 : 1;
}

export function sortByRank<T>(
  entries: readonly { item: T; rank: number }[],
): T[] {
  return entries
    .map((entry, index) => ({ ...entry, index }))
    .sort((left, right) => left.rank - right.rank || left.index - right.index)
    .map((entry) => entry.item);
}

/** `$` 模式的技能叶子组：名称/描述子串过滤，前缀命中优先。 */
export function buildSkillLeafGroup(
  group: ComposerReferenceGroup,
  query: string,
): ComposerMenuGroup | null {
  const messages = {
    status: group.status,
    statusText: group.statusText,
    emptyText: group.emptyText,
  };
  const ranked = group.items
    .map((reference) => ({
      item: {
        id: `skill:${group.id}:${reference.id}`,
        groupId: group.id,
        label: reference.label,
        description: reference.description,
        level: "leaf" as const,
        action: { kind: "skill" as const, name: reference.insertText },
      },
      rank: rankMatch(
        reference.label,
        `${reference.id} ${reference.insertText} ${reference.description ?? ""} ${reference.keywords ?? ""}`,
        query,
      ),
    }))
    .filter((entry) => entry.rank >= 0);

  if (ranked.length === 0 && !group.statusText && !group.emptyText && !group.status) {
    return null;
  }
  return {
    id: group.id,
    label: group.label,
    items: sortByRank(ranked).slice(0, COMPOSER_MENU_MAX_ITEMS_PER_GROUP),
    ...messages,
  };
}

/**
 * `+`（all 模式）根层的技能入口：单组 launcher 下钻。
 * 与引用 launcher 同构；count 用于点选前的候选数量提示。
 */
export function buildSkillLauncherGroup(
  group: ComposerReferenceGroup,
): ComposerMenuGroup | null {
  const loading = group.status === "loading";
  if (group.items.length === 0 && !loading) {
    return null;
  }
  return {
    id: group.id,
    label: group.label,
    status: group.status,
    hasMore: group.hasMore,
    truncated: group.truncated,
    statusText: group.statusText,
    emptyText: group.emptyText,
    truncatedText: group.truncatedText,
    items: [
      {
        id: `launcher:${group.id}`,
        groupId: group.id,
        label: group.label,
        count: group.items.length,
        level: "launcher",
        // launcher 的 action 只作占位（selectItem 先按 level 下钻），name 留空避免误补全。
        action: { kind: "skill", name: "" },
      },
    ],
  };
}
