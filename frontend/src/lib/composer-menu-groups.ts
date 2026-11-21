// Composer 菜单的纯分组构建器：只做候选组装 / 过滤 / 排序 / 截断，不读 DOM。

import type {
  ComposerCommandOptionsSource,
  ComposerMenuItemAction,
  ComposerMenuGroup,
  ComposerReferenceGroup,
  ComposerWorkspaceSelection,
} from "@/lib/composer-menu-types";
import type { ComposerCommand } from "@/lib/composer-commands";
import {
  COMPOSER_MENU_MAX_ITEMS_PER_GROUP,
  rankMatch,
  sortByRank,
} from "@/lib/composer-menu-skill-groups";

const COMPOSER_MENU_ADVANCED_COMMANDS = new Set(["model", "rename"]);

export function buildCommandGroup(
  commands: readonly ComposerCommand[],
  query: string,
  hideAdvanced = false,
): ComposerMenuGroup | null {
  const ranked = commands
    // 高频命令在底部工具栏已有等价控件：`+` 菜单不再重复出现，
    // 但输入 `/` 与命令提交路径保持不变（这里只影响展开菜单）。
    .filter(
      (command) =>
        !(hideAdvanced && COMPOSER_MENU_ADVANCED_COMMANDS.has(command.name)),
    )
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

export function buildAttachGroup(
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

export function buildWorkspaceGroup(
  selection: ComposerWorkspaceSelection,
  query: string,
): ComposerMenuGroup | null {
  const groupId = "workspace";
  const options = [
    {
      id: "none",
      label: selection.clearLabel,
      description: selection.clearDescription,
      directoryId: null,
      selected: selection.selectedId === null,
    },
    ...selection.options.map((option) => ({
      id: option.id,
      label: option.label,
      description: option.path,
      directoryId: option.id,
      selected: option.id === selection.selectedId,
    })),
  ];
  const items = options
    .map((option) => ({
      item: {
        id: `workspace:${option.id}`,
        groupId,
        label: option.label,
        description: option.description,
        selected: option.selected,
        level: "leaf" as const,
        action: {
          kind: "workspace" as const,
          directoryId: option.directoryId,
        },
      },
      rank: rankMatch(option.label, option.description ?? "", query),
    }))
    .filter((entry) => entry.rank >= 0)
    .sort((left, right) => left.rank - right.rank)
    .slice(0, COMPOSER_MENU_MAX_ITEMS_PER_GROUP)
    .map((entry) => entry.item);

  if (items.length === 0 && !selection.statusText && !selection.emptyText) {
    return null;
  }
  return {
    id: groupId,
    label: selection.label,
    labelKey: "composer.menu.workspace",
    items,
    statusText: selection.statusText,
    emptyText: selection.emptyText,
  };
}

export function buildCategoryLauncherGroup(
  id: string,
  label: string,
  labelKey: string,
  count: number,
  action: ComposerMenuItemAction,
): ComposerMenuGroup {
  return {
    id,
    label,
    labelKey,
    items: [
      {
        id: `launcher:${id}`,
        groupId: id,
        label,
        labelKey,
        count,
        level: "launcher",
        action,
      },
    ],
  };
}

export function hasReferenceGroupContent(
  group: ComposerReferenceGroup,
): boolean {
  return (
    group.items.length > 0 ||
    Boolean(group.status || group.statusText || group.emptyText)
  );
}

export function buildReferenceLauncherGroup(
  group: ComposerReferenceGroup,
): ComposerMenuGroup | null {
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

export function buildReferenceLeafGroup(
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
      rank: rankMatch(
        reference.label,
        `${reference.id} ${reference.insertText}`,
        query,
      ),
    }))
    .filter((entry) => entry.rank >= 0);

  // 本地过滤后为空时，仍保留带状态/空态文案的分组（异步数据源需要可解释的空态行）；
  // 既有的同步空组（无任何文案）行为不变：整组剔除。
  if (
    ranked.length === 0 &&
    !group.statusText &&
    !group.emptyText &&
    !group.status
  ) {
    return null;
  }
  return {
    id: group.id,
    label: group.label,
    items: sortByRank(ranked).slice(0, COMPOSER_MENU_MAX_ITEMS_PER_GROUP),
    ...messages,
  };
}

export function buildCommandOptionGroup(
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