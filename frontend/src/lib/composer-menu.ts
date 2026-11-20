// P1-4 子片 3：Composer 触发菜单的纯模型（`/` 命令、`@` 引用、`+` 按钮三入口同源）。
// 模型只做「候选组装 / 过滤 / 排序 / 高亮位移 / Tab 语义」，不渲染、不读 DOM。

import {
  type ComposerCommand,
  type ComposerCommandOption,
} from "@/lib/composer-commands";

import {
  buildSkillLeafGroup,
  rankMatch,
  sortByRank,
  COMPOSER_MENU_MAX_ITEMS_PER_GROUP,
} from "@/lib/composer-menu-skill-groups";

export { COMPOSER_MENU_MAX_ITEMS_PER_GROUP };

/** `commands` = `/` 触发；`references` = `@` 触发；`skills` = `$` 触发；`all` = `+` 按钮（同源菜单）。 */
export type ComposerMenuMode = "commands" | "references" | "skills" | "all";

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
  /** `$` 技能提及：在触发 token 处补全为 `$name `。 */
  | { kind: "skill"; name: string }
  /** 工作区选择由宿主持有状态；不写入 composer 草稿。 */
  | { kind: "workspace"; directoryId: string | null }
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
  /** 非当前键盘焦点的持久选择状态（例如当前已选工作区）。 */
  selected?: boolean;
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
  /** 附加检索词（不展示；如技能分类），仅参与本地过滤命中。 */
  keywords?: string;
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

export type ComposerWorkspaceOption = {
  id: string;
  label: string;
  path: string;
};

/** `+` 菜单中新会话可用的工作区选择数据；文案由宿主本地化。 */
export type ComposerWorkspaceSelection = {
  label: string;
  options: readonly ComposerWorkspaceOption[];
  selectedId: string | null;
  clearLabel: string;
  clearDescription?: string;
  statusText?: string;
  emptyText?: string;
};

export type ComposerMenuSource = {
  mode: ComposerMenuMode;
  level: ComposerMenuLevel;
  query: string;
  commands: readonly ComposerCommand[];
  referenceGroups: readonly ComposerReferenceGroup[];
  /** `$` 技能提及候选（`skills` 模式与 `all` 模式消费）。 */
  skillGroups?: readonly ComposerReferenceGroup[];
  /** `level.kind === "command-options"` 时的候选项来源；缺省按无候选处理。 */
  commandOptions?: ComposerCommandOptionsSource | null;
  /** 是否提供「添加附件」动作。 */
  hasAttachAction: boolean;
  /** 「添加附件」的本地化文案（模型不持文案）。 */
  attachLabel: string;
  /** 仅新会话传入；未提供时不展示工作区选择。 */
  workspaceSelection?: ComposerWorkspaceSelection;
};

export type ComposerCommandOptionsSource = {
  commandKey: string;
  /** 命令名（不含前导 `/`），候选派发时回传。 */
  commandName: string;
  /** 分组标题：命令名（数据原文，非文案）。 */
  label: string;
  options: readonly ComposerCommandOption[];
};

function buildCommandGroup(
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

const COMPOSER_MENU_ADVANCED_COMMANDS = new Set(["model", "rename"]);

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

function buildWorkspaceGroup(
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

function buildCategoryLauncherGroup(
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

function hasReferenceGroupContent(group: ComposerReferenceGroup): boolean {
  return (
    group.items.length > 0 ||
    Boolean(group.status || group.statusText || group.emptyText)
  );
}

function buildReferenceLauncherGroup(
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
export function buildComposerMenu(
  source: ComposerMenuSource,
): ComposerMenuSnapshot {
  const query = source.query.trim().toLowerCase();
  const drilledGroupId =
    source.level.kind === "group" ? source.level.groupId : null;
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

  if (source.mode === "all") {
    const groups: ComposerMenuGroup[] = [];

    if (source.level.kind === "root") {
      if (source.hasAttachAction) {
        groups.push(
          buildCategoryLauncherGroup(
            "actions",
            "actions",
            "composer.menu.actions",
            1,
            { kind: "attach" },
          ),
        );
      }
      if (source.workspaceSelection) {
        groups.push(
          buildCategoryLauncherGroup(
            "workspace",
            "workspace",
            "composer.menu.workspace",
            source.workspaceSelection.options.length + 1,
            { kind: "workspace", directoryId: null },
          ),
        );
      }

      const skillGroups = (source.skillGroups ?? []).filter(
        hasReferenceGroupContent,
      );
      const skillCount = skillGroups.reduce(
        (count, group) => count + group.items.length,
        0,
      );
      if (skillGroups.length > 0) {
        groups.push(
          buildCategoryLauncherGroup(
            "skills",
            "skills",
            "composer.references.skills",
            skillCount,
            { kind: "skill", name: "" },
          ),
        );
      }

      const referenceGroups = source.referenceGroups.filter(
        hasReferenceGroupContent,
      );
      const referenceCount = referenceGroups.reduce(
        (count, group) => count + group.items.length,
        0,
      );
      if (referenceGroups.length > 0) {
        groups.push(
          buildCategoryLauncherGroup(
            "references",
            "files",
            "composer.references.files",
            referenceCount,
            { kind: "reference", text: "" },
          ),
        );
      }

      const commandGroup = buildCommandGroup(source.commands, query, true);
      if (commandGroup) {
        groups.push(
          buildCategoryLauncherGroup(
            "commands",
            "commands",
            "composer.menu.commands",
            commandGroup.items.length,
            { kind: "command", name: "" },
          ),
        );
      }
    } else if (source.level.kind === "group") {
      const groupId = source.level.groupId;
      if (groupId === "actions") {
        const attachGroup = buildAttachGroup(
          source.hasAttachAction,
          source.attachLabel,
          query,
        );
        if (attachGroup) {
          groups.push(attachGroup);
        }
      } else if (groupId === "workspace") {
        if (source.workspaceSelection) {
          const workspaceGroup = buildWorkspaceGroup(
            source.workspaceSelection,
            query,
          );
          if (workspaceGroup) {
            groups.push(workspaceGroup);
          }
        }
      } else if (groupId === "skills") {
        for (const skillGroup of source.skillGroups ?? []) {
          const skillLeaves = buildSkillLeafGroup(skillGroup, query);
          if (skillLeaves) {
            groups.push(skillLeaves);
          }
        }
      } else if (groupId === "references") {
        for (const referenceGroup of source.referenceGroups) {
          const referenceLeaves = buildReferenceLeafGroup(
            referenceGroup,
            query,
          );
          if (referenceLeaves) {
            groups.push(referenceLeaves);
          }
        }
      } else if (groupId === "commands") {
        const commandGroup = buildCommandGroup(source.commands, query, true);
        if (commandGroup) {
          groups.push(commandGroup);
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

  const allowCommands = source.mode === "commands";
  const allowReferences = source.mode === "references";
  // `$`（skills）直接给叶子候选；`+`（all）根层/分组候选已在上方统一处理。
  const allowSkills = source.mode === "skills";
  const drilled = (groupId: string) =>
    drilledGroupId === null || drilledGroupId === groupId;

  if (allowSkills) {
    for (const skillGroup of source.skillGroups ?? []) {
      if (!drilled(skillGroup.id)) {
        continue;
      }
      const group = buildSkillLeafGroup(skillGroup, query);
      if (group) {
        groups.push(group);
      }
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

  if (allowCommands && drilled("commands")) {
    const commandGroup = buildCommandGroup(
      source.commands,
      query,
      false,
    );
    if (commandGroup) {
      groups.push(commandGroup);
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
