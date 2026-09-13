// P1-4 子片 3：Composer 触发菜单的纯模型（`/` 命令、`@` 引用、`+` 按钮三入口同源）。
// 模型只做「候选组装 / 过滤 / 排序 / 高亮位移 / Tab 语义」，不渲染、不读 DOM。

import { type ComposerCommand } from "@/lib/composer-commands";

export const COMPOSER_MENU_MAX_ITEMS_PER_GROUP = 8;

/** `commands` = `/` 触发；`references` = `@` 触发；`all` = `+` 按钮（同源菜单）。 */
export type ComposerMenuMode = "commands" | "references" | "all";

export type ComposerMenuLevel =
  | { kind: "root" }
  | { kind: "group"; groupId: string };

export type ComposerMenuItemAction =
  | { kind: "command"; name: string }
  | { kind: "reference"; text: string }
  | { kind: "attach" };

export type ComposerMenuItem = {
  id: string;
  groupId: string;
  /** 已解析的展示文本；给 `labelKey` 时渲染层优先用 t(labelKey)。 */
  label: string;
  labelKey?: string;
  description?: string;
  descriptionKey?: string;
  /** 分组 launcher 的候选数量（drill-down 前的「单组 launcher」）。 */
  count?: number;
  level: "launcher" | "leaf";
  action: ComposerMenuItemAction;
};

export type ComposerMenuGroup = {
  id: string;
  label: string;
  labelKey?: string;
  items: ComposerMenuItem[];
};

export type ComposerMenuSnapshot = {
  mode: ComposerMenuMode;
  level: ComposerMenuLevel;
  query: string;
  groups: ComposerMenuGroup[];
  items: ComposerMenuItem[];
  activeId: string | null;
  empty: boolean;
};

export type ComposerReferenceItem = {
  id: string;
  label: string;
  /** 实际插入草稿的文本（不含 `@`）。 */
  insertText: string;
  description?: string;
};

export type ComposerReferenceGroup = {
  id: string;
  /** 已由调用方本地化的分组名。 */
  label: string;
  items: readonly ComposerReferenceItem[];
};

export type ComposerMenuSource = {
  mode: ComposerMenuMode;
  level: ComposerMenuLevel;
  query: string;
  commands: readonly ComposerCommand[];
  referenceGroups: readonly ComposerReferenceGroup[];
  /** 是否提供「添加附件」动作。 */
  hasAttachAction: boolean;
  /** 「添加附件」的本地化文案（模型不持文案）。 */
  attachLabel: string;
};

function rankMatch(label: string, extra: string, query: string): number {
  if (query.length === 0) {
    return 0;
  }
  const haystack = `${label} ${extra}`.toLowerCase();
  if (!haystack.includes(query)) {
    return -1;
  }
  return label.toLowerCase().startsWith(query) ? 0 : 1;
}

function sortByRank<T>(
  entries: readonly { item: T; rank: number }[],
): T[] {
  return entries
    .map((entry, index) => ({ ...entry, index }))
    .sort((left, right) => left.rank - right.rank || left.index - right.index)
    .map((entry) => entry.item);
}

function buildCommandGroup(
  commands: readonly ComposerCommand[],
  query: string,
): ComposerMenuGroup | null {
  const ranked = commands
    .map((command) => ({
      item: {
        id: `command:${command.key}`,
        groupId: "commands",
        label: `/${command.name}`,
        descriptionKey: command.descriptionKey,
        level: "leaf" as const,
        action: { kind: "command" as const, name: command.name },
      },
      rank: rankMatch(command.name, command.key, query),
    }))
    .filter((entry) => entry.rank >= 0);

  if (ranked.length === 0) {
    return null;
  }
  return {
    id: "commands",
    label: "commands",
    labelKey: "composer.menu.commands",
    items: sortByRank(ranked).slice(0, COMPOSER_MENU_MAX_ITEMS_PER_GROUP),
  };
}

function buildAttachGroup(
  hasAttachAction: boolean,
  attachLabel: string,
  query: string,
): ComposerMenuGroup | null {
  if (!hasAttachAction) {
    return null;
  }
  const rank = rankMatch(attachLabel, "attach file", query);
  if (rank < 0) {
    return null;
  }
  return {
    id: "actions",
    label: "actions",
    labelKey: "composer.menu.actions",
    items: [
      {
        id: "action:attach",
        groupId: "actions",
        label: attachLabel,
        level: "leaf",
        action: { kind: "attach" },
      },
    ],
  };
}

function buildReferenceLauncherGroup(group: ComposerReferenceGroup): ComposerMenuGroup | null {
  if (group.items.length === 0) {
    return null;
  }
  return {
    id: group.id,
    label: group.label,
    items: [
      {
        id: `launcher:${group.id}`,
        groupId: group.id,
        label: group.label,
        count: group.items.length,
        level: "launcher",
        action: { kind: "reference", text: "" },
      },
    ],
  };
}

function buildReferenceLeafGroup(
  group: ComposerReferenceGroup,
  query: string,
): ComposerMenuGroup | null {
  const ranked = group.items
    .map((reference) => ({
      item: {
        id: `reference:${group.id}:${reference.id}`,
        groupId: group.id,
        label: reference.label,
        description: reference.description,
        level: "leaf" as const,
        action: { kind: "reference" as const, text: reference.insertText },
      },
      rank: rankMatch(reference.label, `${reference.id} ${reference.insertText}`, query),
    }))
    .filter((entry) => entry.rank >= 0);

  if (ranked.length === 0) {
    return null;
  }
  return {
    id: group.id,
    label: group.label,
    items: sortByRank(ranked).slice(0, COMPOSER_MENU_MAX_ITEMS_PER_GROUP),
  };
}

/** 组装菜单快照；空分组一律剔除，`empty` 表示整体无候选。 */
export function buildComposerMenu(source: ComposerMenuSource): ComposerMenuSnapshot {
  const query = source.query.trim().toLowerCase();
  const drilledGroupId = source.level.kind === "group" ? source.level.groupId : null;
  const groups: ComposerMenuGroup[] = [];

  const allowCommands = source.mode === "commands" || source.mode === "all";
  const allowReferences = source.mode === "references" || source.mode === "all";
  const drilled = (groupId: string) => drilledGroupId === null || drilledGroupId === groupId;

  if (allowCommands && drilled("commands")) {
    const commandGroup = buildCommandGroup(source.commands, query);
    if (commandGroup) {
      groups.push(commandGroup);
    }
  }

  if (source.mode === "all" && drilled("actions")) {
    const attachGroup = buildAttachGroup(source.hasAttachAction, source.attachLabel, query);
    if (attachGroup) {
      groups.push(attachGroup);
    }
  }

  if (allowReferences) {
    const useLaunchers = drilledGroupId === null && query.length === 0;
    for (const referenceGroup of source.referenceGroups) {
      if (!drilled(referenceGroup.id)) {
        continue;
      }
      const group = useLaunchers
        ? buildReferenceLauncherGroup(referenceGroup)
        : buildReferenceLeafGroup(referenceGroup, query);
      if (group) {
        groups.push(group);
      }
    }
  }

  const items = groups.flatMap((group) => group.items);
  return {
    mode: source.mode,
    level: source.level,
    query: source.query,
    groups,
    items,
    activeId: clampComposerMenuActive(items, null),
    empty: items.length === 0,
  };
}

export function clampComposerMenuActive(
  items: readonly ComposerMenuItem[],
  activeId: string | null,
): string | null {
  if (items.length === 0) {
    return null;
  }
  if (activeId && items.some((item) => item.id === activeId)) {
    return activeId;
  }
  return items[0].id;
}

/** 高亮位移（环形）；返回下一个 activeId，空列表返回 null。 */
export function moveComposerMenuActive(
  items: readonly ComposerMenuItem[],
  activeId: string | null,
  delta: number,
): string | null {
  if (items.length === 0) {
    return null;
  }
  const currentIndex = items.findIndex((item) => item.id === activeId);
  const base = currentIndex === -1 ? (delta < 0 ? 0 : -1) : currentIndex;
  const nextIndex = (base + delta + items.length) % items.length;
  return items[nextIndex].id;
}

/**
 * Tab 语义：launcher → drill（下钻展开该组），leaf → pick（补全），
 * 无高亮 → pass（原样放行，保住浏览器原生焦点遍历）。
 */
export function resolveComposerMenuTab(
  items: readonly ComposerMenuItem[],
  activeId: string | null,
): "drill" | "pick" | "pass" {
  if (!activeId) {
    return "pass";
  }
  const item = items.find((candidate) => candidate.id === activeId);
  if (!item) {
    return "pass";
  }
  return item.level === "launcher" ? "drill" : "pick";
}

export function findComposerMenuItem(
  items: readonly ComposerMenuItem[],
  itemId: string | null,
): ComposerMenuItem | null {
  if (!itemId) {
    return null;
  }
  return items.find((item) => item.id === itemId) ?? null;
}
