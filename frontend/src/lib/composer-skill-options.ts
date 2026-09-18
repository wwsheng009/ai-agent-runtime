// P2-7 扩展：`/skill` 的候选项组装（运行时技能目录 → 菜单 / 弹窗可搬运的数据）。
//
// 边界：纯映射 skill 目录到命令候选，不读网络、不缓存、不推断能力：
// - 目录未就绪（null / 空数组）时返回空集合，菜单与弹窗据此进入空态；
// - skill 名与描述保持数据原文（不翻译、不美化）；
// - 按 category 分组，无 category 归入 "Other"；
// - 候选项的 value 是 skill name（选中后作为命令参数）。

import type { ComposerCommandOption } from "@/lib/composer-commands";
import type { RuntimeSkillCatalog } from "@/types/runtime";

export type ComposerSkillCatalogGroup = {
  /** category 名（数据原文，无 category 时为 "Other"）。 */
  category: string;
  /** 该 category 下的 skill 名（去空行、去组内重复，保持目录顺序）。 */
  skills: readonly string[];
};

/**
 * 目录 → 按 category 分组的只读视图。
 * 空 category（无 skill）不产出空分组；category 顺序沿用目录顺序。
 */
export function composerSkillCatalogGroups(
  catalog: RuntimeSkillCatalog | null | undefined,
): ComposerSkillCatalogGroup[] {
  if (!catalog || !catalog.skills) {
    return [];
  }
  const groupMap = new Map<string, Set<string>>();
  const categoryOrder: string[] = [];
  
  for (const skill of catalog.skills) {
    const category = skill.category || "Other";
    if (!groupMap.has(category)) {
      groupMap.set(category, new Set());
      categoryOrder.push(category);
    }
    if (skill.name.trim().length > 0) {
      groupMap.get(category)!.add(skill.name);
    }
  }
  
  return categoryOrder
    .map((category) => ({
      category,
      skills: [...groupMap.get(category)!],
    }))
    .filter((group) => group.skills.length > 0);
}

/**
 * 候选面的本地检索（与 `PopupSelectView` 的 `filterOptions` 同口径）。
 * - 空 query 原样返回；
 * - 大小写不敏感的子串命中：skill 名或 category 名任一命中即保留该行；
 * - 全部落空的 category 分组不保留空壳。
 */
export function filterComposerSkillGroups(
  groups: readonly ComposerSkillCatalogGroup[],
  query: string,
): ComposerSkillCatalogGroup[] {
  const needle = query.trim().toLowerCase();
  if (needle.length === 0) {
    return [...groups];
  }
  return groups
    .map((group) => ({
      category: group.category,
      skills: group.skills.filter(
        (skill) =>
          skill.toLowerCase().includes(needle) ||
          group.category.toLowerCase().includes(needle),
      ),
    }))
    .filter((group) => group.skills.length > 0);
}

/**
 * 目录 → `/skill` 的第二级候选（扁平）。
 * `value` 是 skill name（选中后作为命令参数回填执行器），`description` 是所属 category。
 */
export function composerSkillCommandOptions(
  catalog: RuntimeSkillCatalog | null | undefined,
): ComposerCommandOption[] {
  const seen = new Set<string>();
  const options: ComposerCommandOption[] = [];
  for (const group of composerSkillCatalogGroups(catalog)) {
    for (const skill of group.skills) {
      if (seen.has(skill)) {
        continue;
      }
      seen.add(skill);
      options.push({
        value: skill,
        label: skill,
        description: group.category,
      });
    }
  }
  return options;
}

/**
 * 目录中的全部 skill name（`/skill <name>` 的校验集合）。
 * 与候选项口径一致：只报目录里真实存在的 skill，不做前缀 / 模糊推断。
 */
export function composerSkillNames(
  catalog: RuntimeSkillCatalog | null | undefined,
): string[] {
  return [...new Set(composerSkillCatalogGroups(catalog).flatMap((group) => group.skills))];
}

/**
 * 选中 skill 后回填到输入框的命令文本。
 *
 * 尾随空格是刻意的：光标落在参数位，用户直接续写 prompt 即可；
 * 回填只写命令行、不触发执行——执行发生在用户提交 `/skill <name> <prompt>` 时。
 */
export function composerSkillCommandText(skillName: string): string {
  const name = skillName.trim();
  return `/skill ${name} `;
}

/**
 * `/skill` 回合提交时的用户消息文本：`/skill <name> <prompt>`。
 *
 * 与输入框回填形态一致——线程里能直接看出这是一次 skill 调用而不是普通提问；
 * 后端注入的 ProgramGuide 会说明该前缀只是调用标记，真正的参数是其后的文本。
 */
export function composerSkillTurnPrompt(skillName: string, prompt: string): string {
  return `${composerSkillCommandText(skillName)}${prompt}`;
}
