// P0-2 拆分（原 skills.ts L330-L456 + L365-L374）：只读查询（列表/详情/检索/统计/热重载统计）。

import type {
  RuntimeHotReloadStats,
  RuntimeSkill,
  RuntimeSkillCatalog,
  RuntimeSkillSearchResult,
  RuntimeSkillStats,
} from "@/types/runtime";

import {
  buildRuntimeUrl,
  buildRuntimeUrlWithQuery,
  fetchRuntimeJson,
} from "../shared";

import {
  DEFAULT_SKILL_SEARCH_LIMIT,
  MAX_SKILL_SEARCH_LIMIT,
  SKILLS_HOT_RELOAD_STATS_PATH,
  SKILLS_PATH,
  SKILLS_SEARCH_PATH,
  SKILLS_STATS_PATH,
} from "./constants";
import {
  normalizeHotReloadStats,
  normalizeRuntimeSkill,
  normalizeSkillCatalog,
  normalizeSkillSearchResult,
  normalizeSkillStats,
} from "./normalize";
import type { SkillSearchQuery, SkillsRequestOptions } from "./types";

function clampSearchLimit(limit: number | undefined): number {
  if (typeof limit !== "number" || !Number.isFinite(limit) || limit <= 0) {
    return DEFAULT_SKILL_SEARCH_LIMIT;
  }
  return Math.min(Math.floor(limit), MAX_SKILL_SEARCH_LIMIT);
}

function skillDetailPath(name: string): string {
  return `${SKILLS_PATH}/${encodeURIComponent(name)}`;
}

/** 列出技能目录（可按来源层级 / 目录过滤）。 */
export async function listRuntimeSkills(
  options: SkillsRequestOptions = {},
): Promise<RuntimeSkillCatalog> {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrlWithQuery(SKILLS_PATH, {
      source_layer: options.layer,
      source_dir: options.dir,
    }),
    options.signal ? { signal: options.signal } : {},
  );
  return normalizeSkillCatalog(payload);
}

/** 读取单个技能；404 原样抛 `RuntimeApiError`（区分「不存在」与其他失败）。 */
export async function getRuntimeSkillDetail(
  name: string,
  options: Pick<SkillsRequestOptions, "signal"> = {},
): Promise<RuntimeSkill> {
  const trimmed = name.trim();
  if (!trimmed) {
    throw new Error("skill name is required");
  }
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(skillDetailPath(trimmed)),
    options.signal ? { signal: options.signal } : {},
  );
  const skill = normalizeRuntimeSkill(payload);
  if (!skill) {
    throw new Error("runtime skill payload is missing `name`");
  }
  return skill;
}

/** 关键词检索技能；空查询直接抛错（不发无效请求）。 */
export async function searchRuntimeSkills(
  query: SkillSearchQuery,
  options: SkillsRequestOptions = {},
): Promise<RuntimeSkillSearchResult> {
  const trimmed = query.query.trim();
  if (!trimmed) {
    throw new Error("skill search query is required");
  }
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrlWithQuery(SKILLS_SEARCH_PATH, {
      q: trimmed,
      limit: clampSearchLimit(query.limit),
      category: query.category?.trim() || undefined,
      mode: query.mode,
      source_layer: options.layer,
      source_dir: options.dir,
    }),
    options.signal ? { signal: options.signal } : {},
  );
  return normalizeSkillSearchResult(payload);
}

/** 技能运行统计（含 mutation policy 快照）。 */
export async function getRuntimeSkillsStats(
  options: SkillsRequestOptions = {},
): Promise<RuntimeSkillStats> {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrlWithQuery(SKILLS_STATS_PATH, {
      source_layer: options.layer,
      source_dir: options.dir,
    }),
    options.signal ? { signal: options.signal } : {},
  );
  return normalizeSkillStats(payload);
}

/** 热重载统计；未配置热重载时后端 503，按真实状态码抛错。 */
export async function getHotReloadStats(
  options: Pick<SkillsRequestOptions, "signal"> = {},
): Promise<RuntimeHotReloadStats> {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(SKILLS_HOT_RELOAD_STATS_PATH),
    options.signal ? { signal: options.signal } : {},
  );
  return normalizeHotReloadStats(payload);
}
