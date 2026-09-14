// P2-1A：技能市场页的纯函数集合（可单测，不依赖 React）。
//
// 目的：把「后端字段 → 展示口径」的转换集中在一处，避免 UI 里散落猜测：
//   * 来源标签只拼接后端真实给出的 layer / dir / path；
//   * 策略标志（read_only / disable_*）缺席即 unknown，绝不当成 false；
//   * 热重载原始统计只展示后端确实返回的标量键，结构化键单独呈现，不重复也不臆造。

import { isSkillsForbidden, isSkillsUnavailable } from "@/api/runtime/skills";
import type {
  RuntimeHotReloadStats,
  RuntimeSkill,
  RuntimeSkillMutationPolicy,
  RuntimeSkillSearchMode,
  RuntimeSkillStatRow,
} from "@/types/runtime";

export type SkillsErrorKind = "forbidden" | "unavailable" | "failed";

/** 错误分类：403 = 策略/鉴权拒绝；404/405/501/503 = 端点或服务不可用；其余 = 真实失败。 */
export function classifySkillsError(error: unknown): SkillsErrorKind {
  if (isSkillsForbidden(error)) {
    return "forbidden";
  }
  if (isSkillsUnavailable(error)) {
    return "unavailable";
  }
  return "failed";
}

/** 来源描述：优先 `layer · dir`，只有 layer 时给 layer，都没有时给 path。 */
export function describeSkillSource(skill: RuntimeSkill): string {
  const { source } = skill;
  if (!source) {
    return "";
  }
  const parts: string[] = [];
  if (source.layer) {
    parts.push(source.layer);
  }
  if (source.dir) {
    parts.push(source.dir);
  }
  if (parts.length > 0) {
    return parts.join(" · ");
  }
  return source.path;
}

export function skillSubtitle(skill: RuntimeSkill): string {
  const parts: string[] = [];
  if (skill.category) {
    parts.push(skill.category);
  }
  if (skill.version) {
    parts.push(`v${skill.version}`);
  }
  return parts.join(" · ");
}

export function formatSuccessRate(value: number): string {
  if (!Number.isFinite(value)) {
    return "";
  }
  return `${Math.round(value * 100)}%`;
}

export function formatDurationMs(value: number | null): string {
  if (value === null || !Number.isFinite(value)) {
    return "";
  }
  if (value < 1000) {
    return `${Math.round(value)}ms`;
  }
  return `${(value / 1000).toFixed(1)}s`;
}

export function topStatRows(rows: RuntimeSkillStatRow[], limit = 8): RuntimeSkillStatRow[] {
  return [...rows]
    .sort((left, right) => {
      if (right.callCount !== left.callCount) {
        return right.callCount - left.callCount;
      }
      return left.name.localeCompare(right.name);
    })
    .slice(0, Math.max(0, limit));
}

export type PolicyFlag = {
  key: keyof RuntimeSkillMutationPolicy;
  /** null = 后端未上报该标志（UI 显示未知，不按 false 渲染）。 */
  value: boolean | null;
  /** 该标志为 true 时表示「被禁用」。 */
  disables: boolean;
};

export function policyFlags(policy: RuntimeSkillMutationPolicy | null): PolicyFlag[] {
  if (!policy) {
    return [];
  }
  return [
    { key: "readOnly", value: policy.readOnly, disables: true },
    { key: "disableImport", value: policy.disableImport, disables: true },
    { key: "disablePersist", value: policy.disablePersist, disables: true },
    { key: "disableReloadOps", value: policy.disableReloadOps, disables: true },
    { key: "disableHotReload", value: policy.disableHotReload, disables: true },
  ];
}

const structuralHotReloadKeys = new Set([
  "enabled",
  "watching",
  "skillDir",
  "skillDirs",
  "skillCount",
  "callbackCount",
  "debounceTime",
]);

function isScalar(value: unknown): value is string | number | boolean {
  return (
    typeof value === "string" || typeof value === "number" || typeof value === "boolean"
  );
}

/**
 * 热重载统计里未结构化的标量键，按 key 排序稳定输出。
 * 非标量（嵌套对象/数组）不在此列——避免把 JSON 树硬塞进一行。
 */
export function hotReloadExtraEntries(
  stats: RuntimeHotReloadStats,
): Array<{ key: string; value: string }> {
  return Object.entries(stats.raw)
    .filter(([key, value]) => !structuralHotReloadKeys.has(key) && isScalar(value))
    .map(([key, value]) => ({ key, value: String(value) }))
    .sort((left, right) => left.key.localeCompare(right.key));
}

/** 启动热重载前的目录清单校验：去空、去重，保持输入顺序。 */
export function normalizeSkillDirs(input: string): string[] {
  const seen = new Set<string>();
  const dirs: string[] = [];
  for (const entry of input.split(/[\n,;，；]/)) {
    const trimmed = entry.trim();
    if (!trimmed || seen.has(trimmed)) {
      continue;
    }
    seen.add(trimmed);
    dirs.push(trimmed);
  }
  return dirs;
}

export const skillSearchModes: RuntimeSkillSearchMode[] = ["auto", "lexical", "semantic"];

export function isSearchMode(value: string): value is RuntimeSkillSearchMode {
  return (skillSearchModes as string[]).includes(value);
}

/** 结果行描述：优先后端 `matched_by`，其次高亮 details，再退化为描述。 */
export function matchSummary(skill: RuntimeSkill): string {
  return skill.description || skill.userPrompt || "";
}
