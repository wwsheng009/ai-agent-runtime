// P1-4 子片 3：Composer 触发菜单的纯模型（`/` 命令、`@` 引用、`+` 按钮三入口同源）。
// 模型只做「候选组装 / 过滤 / 排序 / 高亮位移 / Tab 语义」，不渲染、不读 DOM。
//
// 类型定义在 composer-menu-types.ts，分组构建器在 composer-menu-groups.ts；
// 本文件保留快照组装与高亮/导航语义，并作为公开 barrel 保持既有 import 路径。

import {
  buildAttachGroup,
  buildCategoryLauncherGroup,
  buildCommandGroup,
  buildCommandOptionGroup,
  buildReferenceLeafGroup,
  buildReferenceLauncherGroup,
  buildWorkspaceGroup,
  hasReferenceGroupContent,
} from "@/lib/composer-menu-groups";
import {
  buildSkillLeafGroup,
  COMPOSER_MENU_MAX_ITEMS_PER_GROUP,
} from "@/lib/composer-menu-skill-groups";
import type {
  ComposerCommandOptionsSource,
  ComposerMenuGroup,
  ComposerMenuItem,
  ComposerMenuItemAction,
  ComposerMenuLevel,
  ComposerMenuMode,
  ComposerMenuSnapshot,
  ComposerMenuSource,
  ComposerReferenceGroup,
  ComposerReferenceItem,
  ComposerReferenceStatus,
  ComposerWorkspaceOption,
  ComposerWorkspaceSelection,
} from "@/lib/composer-menu-types";

export { COMPOSER_MENU_MAX_ITEMS_PER_GROUP };

export type {
  ComposerCommandOptionsSource,
  ComposerMenuGroup,
  ComposerMenuItem,
  ComposerMenuItemAction,
  ComposerMenuLevel,
  ComposerMenuMode,
  ComposerMenuSnapshot,
  ComposerMenuSource,
  ComposerReferenceGroup,
  ComposerReferenceItem,
  ComposerReferenceStatus,
  ComposerWorkspaceOption,
  ComposerWorkspaceSelection,
};

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
    const commandGroup = buildCommandGroup(source.commands, query, false);
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