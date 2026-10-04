// 文本类 skill 的 `$name` 提及候选：运行时技能目录 → composer 菜单分组。
//
// 对齐 aicli TUI（`chat_skill_mention_completion.go`）的候选口径：
// - 只列文本类技能：排除带 workflow 步骤的技能（handler/workflow 技能走 `/skill`）；
// - 只列可补全名称：名称必须全部是 `[A-Za-z0-9_-]`，带空格/Unicode 的名称
//   无法被词法解析，不得补出永远无法命中的 `$name`；
// - 同名去重（web 目录无 path 绑定，同一名称只保留首个条目）；
// - 名称保持数据原文（不翻译、不美化）；描述优先展示 description，缺省退回
//   category，category 同时作为附加检索词（对齐 TUI 的短描述展示）。

import {
  applyComposerSkillPrefixInsertion,
  isCompletableSkillMentionName,
  type ComposerTrigger,
} from "@/lib/composer-trigger";
import type {
  ComposerMenuItem,
  ComposerReferenceGroup,
  ComposerReferenceItem,
} from "@/lib/composer-menu";
import type { RuntimeSkill, RuntimeSkillCatalog } from "@/types/runtime";

export const COMPOSER_SKILL_MENTION_GROUP_ID = "skills";

export type ComposerSkillMentionLabels = {
  /** 分组名（技能）。 */
  group: string;
  /** 就绪但无候选时的空态文案。 */
  empty: string;
  /** 目录加载中的状态文案。 */
  loading?: string;
  /** 目录加载失败时的状态文案。 */
  error?: string;
};

export type ComposerSkillMentionEntry = {
  name: string;
  description: string;
  /** 附加检索词（分类）；不参与展示。 */
  keywords: string;
};

/**
 * 文本类候选：`workflowSteps` 为空（web 目录不暴露 handler，无法再细分）。
 * 空名丢弃；同名（大小写不敏感）只保留首个，避免重复菜单行。
 */
export function skillMentionCatalogEntries(
  catalog: RuntimeSkillCatalog | null | undefined,
): ComposerSkillMentionEntry[] {
  if (!catalog || !catalog.skills) {
    return [];
  }
  const entries: ComposerSkillMentionEntry[] = [];
  const seen = new Set<string>();
  for (const skill of catalog.skills) {
    if (!isTextSkill(skill)) {
      continue;
    }
    const name = skill.name.trim();
    // 带空格/Unicode 的名称补不出可解析的 `$name`：与 TUI 一致直接丢弃。
    if (!isCompletableSkillMentionName(name)) {
      continue;
    }
    const key = name.toLowerCase();
    if (seen.has(key)) {
      continue;
    }
    seen.add(key);
    entries.push({
      name,
      description: firstNonEmpty(skill.description, skill.category),
      keywords: skill.category.trim(),
    });
  }
  return entries;
}

function isTextSkill(skill: RuntimeSkill): boolean {
  return skill.workflowSteps.length === 0;
}

function firstNonEmpty(...values: readonly string[]): string {
  for (const value of values) {
    const trimmed = value.trim();
    if (trimmed.length > 0) {
      return trimmed;
    }
  }
  return "";
}

/**
 * 候选名公共前缀（对齐 TUI `skillMentionCompletionCommonPrefix`）：
 * 大小写不敏感比较，输出保留第一条候选的原始大小写；空候选或首字符即不同返回空串。
 * 用于 Tab 的公共前缀延伸（多候选时只加深到公共部分，不直接落下整名）。
 */
export function skillMentionCompletionCommonPrefix(names: readonly string[]): string {
  if (names.length === 0) {
    return "";
  }
  const base = [...names[0]];
  let length = base.length;
  for (const name of names.slice(1)) {
    const other = [...name];
    if (other.length < length) {
      length = other.length;
    }
    for (let index = 0; index < length; index += 1) {
      if (base[index].toLowerCase() !== other[index].toLowerCase()) {
        length = index;
        break;
      }
    }
    if (length === 0) {
      return "";
    }
  }
  return length <= 0 ? "" : base.slice(0, length).join("");
}

/**
 * Tab 的 `$` 公共前缀延伸判定（对齐 TUI 多候选分支）：
 * - 只在「名称以 query 开头」的子集上计算公共前缀——本地面板还允许子串命中，
 *   直接用全量候选会被子串项稀释（TUI 的候选列表本身即前缀过滤）；
 * - 多候选且公共前缀可加深时，返回替换后的草稿/光标与更新后的触发 token；
 *   否则返回 null（唯一候选或不可再加深 → 走通用 pick 接受高亮项）。
 */
export function resolveSkillMentionTabExtension(
  value: string,
  trigger: ComposerTrigger,
  items: readonly ComposerMenuItem[],
): { value: string; caret: number; trigger: ComposerTrigger } | null {
  const queryLower = trigger.query.toLowerCase();
  const names = items
    .map((item) => item.label)
    .filter((label) => label.toLowerCase().startsWith(queryLower));
  const prefix = skillMentionCompletionCommonPrefix(names);
  if (names.length <= 1 || prefix.length <= trigger.query.length) {
    return null;
  }
  const next = applyComposerSkillPrefixInsertion(value, trigger, prefix);
  return {
    value: next.value,
    caret: next.caret,
    trigger: {
      ...trigger,
      query: prefix,
      end: trigger.start + 1 + prefix.length,
      key: `skill:${trigger.start}:${prefix}`,
    },
  };
}

/**
 * 程序化写入后的旧 token select 回声判定：同一 token 起点、query 严格回退为
 * 更短前缀（如刚写成 `$doc`、回声却报告 `$do`）。命中时忽略该事件，保持新锚点。
 */
export function isSkillMentionCaretEcho(
  trigger: ComposerTrigger | null,
  guard: { tokenStart: number; query: string },
): boolean {
  return (
    trigger?.kind === "skill" &&
    trigger.start === guard.tokenStart &&
    trigger.query.length < guard.query.length &&
    guard.query.toLowerCase().startsWith(trigger.query.toLowerCase())
  );
}

/**
 * 目录 → `$` 提及引用分组；无可消费候选且无状态文案时返回 null。
 *
 * - 条目插入文本是技能名；菜单层负责在触发 token 处补全为 `$<name> `；
 * - `status="loading"` 的空组仍产出分组占位（打开瞬间不闪空）；
 * - `labels.empty` 只在就绪且无候选时展示（加载中/失败由状态行承担）。
 */
export function skillMentionReferenceGroup(
  catalog: RuntimeSkillCatalog | null | undefined,
  labels: ComposerSkillMentionLabels,
  status: "loading" | "ready" | "error" = "ready",
): ComposerReferenceGroup | null {
  const entries = skillMentionCatalogEntries(catalog);
  const items: ComposerReferenceItem[] = entries.map((entry) => ({
    id: entry.name,
    label: entry.name,
    insertText: entry.name,
    description: entry.description,
    ...(entry.keywords ? { keywords: entry.keywords } : {}),
  }));

  const statusText =
    status === "loading"
      ? labels.loading
      : status === "error"
        ? labels.error
        : undefined;
  const emptyText = status === "ready" && items.length === 0 ? labels.empty : undefined;
  if (items.length === 0 && !statusText && !emptyText) {
    return null;
  }

  return {
    id: COMPOSER_SKILL_MENTION_GROUP_ID,
    label: labels.group,
    items,
    status,
    ...(statusText ? { statusText } : {}),
    ...(emptyText ? { emptyText } : {}),
  };
}
