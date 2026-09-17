// P1-4 子片 3：Composer 触发菜单的纯模型（`/` 命令、`@` 引用、`+` 按钮三入口同源）。
// 模型只做「候选组装 / 过滤 / 排序 / 高亮位移 / Tab 语义」，不渲染、不读 DOM。

import {
  type ComposerCommand,
  type ComposerCommandOption,
} from "@/lib/composer-commands";

export const COMPOSER_MENU_MAX_ITEMS_PER_GROUP = 8;

/** `commands` = `/` 触发；`references` = `@` 触发；`all` = `+` 按钮（同源菜单）。 */
export type ComposerMenuMode = "commands" | "references" | "all";

export type ComposerMenuLevel =
  | { kind: "root" }
  | { kind: "group"; groupId: string }
  /** 命令专属候选（`popupSelect` 选中后的第二级）。 */
  | { kind: "command-options"; commandKey: string };

export type ComposerMenuItemAction =
  | { kind: "command"; name: string }
  /** 命令专属候选：`name` 回查命令，`value` 作为参数派发。 */
  | { kind: "command-option"; name: string; value: string }
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
  /** 引用组的数据源状态（异步 loader 的 loading / error 表达）。 */
  status?: ComposerReferenceStatus;
  /** true = 还有下一页（服务端语义；仅展示"继续输入缩小范围"提示）。 */
  hasMore?: boolean;
  /** true = 服务端因深度/扫描量/预算截断，结果可能不完备。 */
  truncated?: boolean;
  /** 状态行文案（已本地化；loading/error 时渲染）。 */
  statusText?: string;
  /** 就绪但无候选时的空态文案（已本地化）。 */
  emptyText?: string;
  /** truncated=true 时的页脚文案（已本地化）。 */
  truncatedText?: string;
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

export type ComposerReferenceStatus = "loading" | "ready" | "error";

export type ComposerReferenceGroup = {
  id: string;
  /** 已由调用方本地化的分组名。 */
  label: string;
  items: readonly ComposerReferenceItem[];
  /**
   * 异步数据源状态：`loading` 时空组也进入 launcher（打开瞬间不闪空），
   * 缺省视为同步数据源（与既有行为完全一致）。
   */
  status?: ComposerReferenceStatus;
  /**
   * true = 本组结果已由服务端过滤/排序，客户端**禁止**再做 `rankMatch` 与顺序重排
   * （模糊命中不是子串，二次过滤会误杀）。
   */
  serverFiltered?: boolean;
  /** 服务端提示：还有下一页 / 结果被截断（渲染页脚用）。 */
  hasMore?: boolean;
  truncated?: boolean;
  /** 状态行文案（已本地化；loading/error 时渲染）。 */
  statusText?: string;
  /** 就绪但无候选时的空态文案（已本地化）。 */
  emptyText?: string;
  /** truncated=true 时的页脚文案（已本地化）。 */
  truncatedText?: string;
};

export type ComposerMenuSource = {
  mode: ComposerMenuMode;
  level: ComposerMenuLevel;
  query: string;
  commands: readonly ComposerCommand[];
  referenceGroups: readonly ComposerReferenceGroup[];
  /** `level.kind === "command-options"` 时的候选项来源；缺省按无候选处理。 */
  commandOptions?: ComposerCommandOptionsSource | null;
  /** 是否提供「添加附件」动作。 */
  hasAttachAction: boolean;
  /** 「添加附件」的本地化文案（模型不持文案）。 */
  attachLabel: string;
};

export type ComposerCommandOptionsSource = {
  commandKey: string;
  /** 命令名（不含前导 `/`），候选派发时回传。 */
  commandName: string;
  /** 分组标题：命令名（数据原文，非文案）。 */
  label: string;
  options: readonly ComposerCommandOption[];
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
  const loading = group.status === "loading";
  // loading 中的空组也要进 launcher：打开菜单瞬间显示"加载中"而不是整段闪空。
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
        action: { kind: "reference", text: "" },
      },
    ],
  };
}

function buildReferenceLeafGroup(
  group: ComposerReferenceGroup,
  query: string,
): ComposerMenuGroup | null {
  const messages = {
    status: group.status,
    hasMore: group.hasMore,
    truncated: group.truncated,
    statusText: group.statusText,
    emptyText: group.emptyText,
    truncatedText: group.truncatedText,
  };
  // 服务端已过滤/排序：原样截断，禁止客户端二次 rankMatch 与重排。
  if (group.serverFiltered === true) {
    const items = group.items
      .slice(0, COMPOSER_MENU_MAX_ITEMS_PER_GROUP)
      .map((reference) => ({
        id: `reference:${group.id}:${reference.id}`,
        groupId: group.id,
        label: reference.label,
        description: reference.description,
        level: "leaf" as const,
        action: { kind: "reference" as const, text: reference.insertText },
      }));
    if (
      items.length === 0 &&
      !group.statusText &&
      !group.emptyText &&
      !group.status
    ) {
      return null;
    }
    return { id: group.id, label: group.label, items, ...messages };
  }

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

  // 本地过滤后为空时，仍保留带状态/空态文案的分组（异步数据源需要可解释的空态行）；
  // 既有的同步空组（无任何文案）行为不变：整组剔除。
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

function buildCommandOptionGroup(
  source: ComposerCommandOptionsSource | null | undefined,
  query: string,
): ComposerMenuGroup | null {
  if (!source || source.options.length === 0) {
    return null;
  }
  const groupId = `command-options:${source.commandKey}`;
  const ranked = source.options
    .map((option) => ({
      item: {
        id: `${groupId}:${option.value}`,
        groupId,
        label: option.label,
        description: option.description,
        descriptionKey: option.descriptionKey,
        level: "leaf" as const,
        action: {
          kind: "command-option" as const,
          name: source.commandName,
          value: option.value,
        },
      },
      rank: rankMatch(
        option.label,
        `${option.value} ${option.description ?? ""}`,
        query,
      ),
    }))
    .filter((entry) => entry.rank >= 0);

  if (ranked.length === 0) {
    return null;
  }
  return {
    id: groupId,
    label: source.label,
    items: sortByRank(ranked).slice(0, COMPOSER_MENU_MAX_ITEMS_PER_GROUP),
  };
}

/** 组装菜单快照；空分组一律剔除，`empty` 表示整体无候选。 */
export function buildComposerMenu(source: ComposerMenuSource): ComposerMenuSnapshot {
  const query = source.query.trim().toLowerCase();
  const drilledGroupId = source.level.kind === "group" ? source.level.groupId : null;
  const groups: ComposerMenuGroup[] = [];

  // 命令专属候选独占一层：不再混入命令 / 引用 / 附件分组，避免与其它入口互相干扰。
  if (source.level.kind === "command-options") {
    const optionsGroup = buildCommandOptionGroup(source.commandOptions, query);
    if (optionsGroup) {
      groups.push(optionsGroup);
    }
    const optionsItems = groups.flatMap((group) => group.items);
    return {
      mode: source.mode,
      level: source.level,
      query: source.query,
      groups,
      items: optionsItems,
      activeId: clampComposerMenuActive(optionsItems, null),
      empty: optionsItems.length === 0,
    };
  }

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
