// P2-1A：运行时 Skills REST 客户端（技能市场 / 热重载面板）。
//
// 端点（backend/internal/api/skills/handler.go:654-671、852-863）：
//   GET  /api/runtime/skills?source_layer=&source_dir=   → {skills, count}
//   GET  /api/runtime/skills/{name}                      → Skill（404 skill not found）
//   GET  /api/runtime/skills/search?q=&limit=&category=&mode=&source_layer=&source_dir=
//   GET  /api/runtime/skills/stats?source_layer=&source_dir=
//   GET  /api/runtime/skills/hot-reload/stats            → {stats}（未配置时 503）
//   POST /api/runtime/skills/hot-reload/start|stop|reload（写操作）
//
// 归一化纪律：
//   * 核心结构缺失（`skills` 非数组、`stats` 非数组/对象、技能缺 `name`）→ 抛错，
//     不把结构异常当作「空市场」；
//   * 单条技能缺 `name` 只丢该条（其余照常呈现），不做整页失败；
//   * `count` / `total_skills` 保留后端上报值，前端不按数组长度改写；
//   * `mutation_policy` / `embedding` 缺失 → 置 null（UI 显示未知），不猜默认值；
//   * 写操作是受策略约束的端点：403（未授权 / 只读策略 / 策略禁用目录）与 503
//     （热重载未配置）按真实状态码向上抛，UI 分类展示，绝不假装成功。

import type {
  RuntimeHotReloadStats,
  RuntimeSkill,
  RuntimeSkillCatalog,
  RuntimeSkillMutationPolicy,
  RuntimeSkillSearchMatch,
  RuntimeSkillSearchResult,
  RuntimeSkillSource,
  RuntimeSkillStatRow,
  RuntimeSkillStats,
  RuntimeSkillTrigger,
  RuntimeSkillWorkflowStep,
} from "@/types/runtime";

import {
  RuntimeApiError,
  buildRuntimeUrl,
  buildRuntimeUrlWithQuery,
  fetchRuntimeJson,
} from "./shared";

type RawRecord = Record<string, unknown>;

export const SKILLS_PATH = "/api/runtime/skills";
export const SKILLS_SEARCH_PATH = "/api/runtime/skills/search";
export const SKILLS_STATS_PATH = "/api/runtime/skills/stats";
export const SKILLS_HOT_RELOAD_PATH = "/api/runtime/skills/hot-reload";
export const SKILLS_HOT_RELOAD_STATS_PATH = `${SKILLS_HOT_RELOAD_PATH}/stats`;

/** 写操作鉴权头（handler.go:7069-7079 同时接受 Bearer admin token）。 */
export const SKILLS_ADMIN_TOKEN_HEADER = "X-Skills-Admin-Token";

export const DEFAULT_SKILL_SEARCH_LIMIT = 20;
export const MAX_SKILL_SEARCH_LIMIT = 200;

function asRecord(value: unknown): RawRecord | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as RawRecord)
    : null;
}

function pickString(record: RawRecord, key: string): string {
  const value = record[key];
  return typeof value === "string" ? value.trim() : "";
}

function pickBoolean(record: RawRecord, key: string): boolean | null {
  const value = record[key];
  return typeof value === "boolean" ? value : null;
}

function pickNumber(record: RawRecord, key: string): number | null {
  const value = record[key];
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function pickStringArray(record: RawRecord, key: string): string[] {
  const value = record[key];
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .filter((entry): entry is string => typeof entry === "string")
    .map((entry) => entry.trim())
    .filter((entry) => entry.length > 0);
}

function pickRecord(record: RawRecord, key: string): RawRecord | null {
  return asRecord(record[key]);
}

function normalizeSource(value: unknown): RuntimeSkillSource | null {
  const record = asRecord(value);
  if (!record) {
    return null;
  }
  return {
    path: pickString(record, "path"),
    dir: pickString(record, "dir"),
    layer: pickString(record, "layer"),
    promptPath: pickString(record, "prompt_path"),
  };
}

function normalizeTrigger(value: unknown): RuntimeSkillTrigger | null {
  const record = asRecord(value);
  if (!record) {
    return null;
  }
  const type = pickString(record, "type");
  const values = pickStringArray(record, "values");
  if (!type && values.length === 0) {
    return null;
  }
  return { type, values, weight: pickNumber(record, "weight") };
}

function normalizeWorkflowStep(value: unknown): RuntimeSkillWorkflowStep | null {
  const record = asRecord(value);
  if (!record) {
    return null;
  }
  return {
    id: pickString(record, "id"),
    name: pickString(record, "name"),
    tool: pickString(record, "tool"),
    args: asRecord(record.args) ?? {},
    dependsOn: pickStringArray(record, "dependsOn"),
    condition: pickString(record, "condition"),
  };
}

/** 单条技能归一化；缺 `name` 视为无效条目（调用方按需丢弃或抛错）。 */
export function normalizeRuntimeSkill(value: unknown): RuntimeSkill | null {
  const record = asRecord(value);
  if (!record) {
    return null;
  }
  const name = pickString(record, "name");
  if (!name) {
    return null;
  }

  const workflow = pickRecord(record, "workflow");
  const context = pickRecord(record, "context");
  const rawTriggers = Array.isArray(record.triggers) ? record.triggers : [];
  const rawSteps = workflow && Array.isArray(workflow.steps) ? workflow.steps : [];

  return {
    name,
    description: pickString(record, "description"),
    version: pickString(record, "version"),
    category: pickString(record, "category"),
    capabilities: pickStringArray(record, "capabilities"),
    tags: pickStringArray(record, "tags"),
    triggers: rawTriggers
      .map(normalizeTrigger)
      .filter((trigger): trigger is RuntimeSkillTrigger => trigger !== null),
    tools: pickStringArray(record, "tools"),
    systemPrompt: pickString(record, "systemPrompt"),
    userPrompt: pickString(record, "userPrompt"),
    workflowSteps: rawSteps
      .map(normalizeWorkflowStep)
      .filter((step): step is RuntimeSkillWorkflowStep => step !== null),
    contextFiles: context ? pickStringArray(context, "files") : [],
    contextEnvironment: context ? pickStringArray(context, "environment") : [],
    contextSymbols: context ? pickStringArray(context, "symbols") : [],
    permissions: pickStringArray(record, "permissions"),
    source: normalizeSource(record.source),
  };
}

function normalizeSkillArray(value: unknown): RuntimeSkill[] {
  if (!Array.isArray(value)) {
    throw new Error("runtime skills payload is missing a `skills` array");
  }
  return value
    .map(normalizeRuntimeSkill)
    .filter((skill): skill is RuntimeSkill => skill !== null);
}

/** `GET /skills` 归一化：`skills` 非数组即抛错（不伪装空市场）。 */
export function normalizeSkillCatalog(payload: unknown): RuntimeSkillCatalog {
  const record = asRecord(payload);
  if (!record) {
    throw new Error("runtime skills payload is not an object");
  }
  const skills = normalizeSkillArray(record.skills);
  const reported = pickNumber(record, "count");
  return { skills, count: reported ?? skills.length };
}

export function normalizeSkillSearchResult(payload: unknown): RuntimeSkillSearchResult {
  const record = asRecord(payload);
  if (!record) {
    throw new Error("runtime skill search payload is not an object");
  }
  const results = normalizeSkillArray(record.results);
  const rawMatches = Array.isArray(record.matches) ? record.matches : [];

  const matches: RuntimeSkillSearchMatch[] = [];
  for (const entry of rawMatches) {
    const matchRecord = asRecord(entry);
    if (!matchRecord) {
      continue;
    }
    const skill = normalizeRuntimeSkill(matchRecord.skill);
    if (!skill) {
      continue;
    }
    matches.push({
      skill,
      score: pickNumber(matchRecord, "score"),
      matchedBy: pickString(matchRecord, "matched_by"),
      details: pickString(matchRecord, "details"),
    });
  }

  return {
    query: pickString(record, "query"),
    results,
    matches,
    count: pickNumber(record, "count") ?? results.length,
    limit: pickNumber(record, "limit") ?? DEFAULT_SKILL_SEARCH_LIMIT,
    requestedMode: pickString(record, "requested_mode"),
    resolvedMode: pickString(record, "resolved_mode"),
    usedEmbedding: pickBoolean(record, "used_embedding") ?? false,
  };
}

function normalizeMutationPolicy(value: unknown): RuntimeSkillMutationPolicy | null {
  const record = asRecord(value);
  if (!record) {
    return null;
  }
  return {
    readOnly: pickBoolean(record, "read_only"),
    disableImport: pickBoolean(record, "disable_import"),
    disablePersist: pickBoolean(record, "disable_persist"),
    disableReloadOps: pickBoolean(record, "disable_reload_ops"),
    disableHotReload: pickBoolean(record, "disable_hot_reload"),
  };
}

function normalizeStatRow(value: unknown): RuntimeSkillStatRow | null {
  const record = asRecord(value);
  if (!record) {
    return null;
  }
  const name = pickString(record, "name");
  if (!name) {
    return null;
  }
  return {
    name,
    category: pickString(record, "category"),
    callCount: pickNumber(record, "call_count") ?? 0,
    successRate: pickNumber(record, "success_rate") ?? 0,
    avgDurationMs: pickNumber(record, "avg_duration_ms"),
    sourceDir: pickString(record, "source_dir"),
    sourcePath: pickString(record, "source_path"),
    sourceLayer: pickString(record, "source_layer"),
  };
}

/** `GET /skills/stats` 归一化：`stats` 非数组即抛错（不伪装「零技能」）。 */
export function normalizeSkillStats(payload: unknown): RuntimeSkillStats {
  const record = asRecord(payload);
  if (!record) {
    throw new Error("runtime skill stats payload is not an object");
  }
  if (!Array.isArray(record.stats)) {
    throw new Error("runtime skill stats payload is missing a `stats` array");
  }

  const rows = record.stats
    .map(normalizeStatRow)
    .filter((row): row is RuntimeSkillStatRow => row !== null);

  const sourceSummaryRecord = asRecord(record.source_summary) ?? {};
  const sourceSummary: Record<string, number> = {};
  for (const [key, value] of Object.entries(sourceSummaryRecord)) {
    const numeric = typeof value === "number" && Number.isFinite(value) ? value : null;
    if (numeric !== null) {
      sourceSummary[key] = numeric;
    }
  }

  const embedding = asRecord(record.embedding);

  return {
    rows,
    totalSkills: pickNumber(record, "total_skills") ?? rows.length,
    skillDirs: pickStringArray(record, "skill_dirs"),
    sourceSummary,
    policy: normalizeMutationPolicy(record.mutation_policy),
    embeddingEnabled: embedding ? pickBoolean(embedding, "enabled") : null,
    raw: record,
  };
}

/** 热重载统计归一化：`stats` 非对象即抛错（不把 503 之外的坏载荷当空态）。 */
export function normalizeHotReloadStats(payload: unknown): RuntimeHotReloadStats {
  const record = asRecord(payload);
  if (!record) {
    throw new Error("runtime hot reload payload is not an object");
  }
  const stats = asRecord(record.stats);
  if (!stats) {
    throw new Error("runtime hot reload payload is missing `stats`");
  }

  const dirs = pickStringArray(stats, "skillDirs");
  const singleDir = pickString(stats, "skillDir");
  if (dirs.length === 0 && singleDir) {
    dirs.push(singleDir);
  }

  return {
    enabled: pickBoolean(stats, "enabled"),
    watching: pickBoolean(stats, "watching"),
    skillDir: singleDir,
    skillDirs: dirs,
    skillCount: pickNumber(stats, "skillCount"),
    callbackCount: pickNumber(stats, "callbackCount"),
    debounceTime: pickString(stats, "debounceTime"),
    raw: stats,
  };
}

export type SkillsRequestOptions = {
  signal?: AbortSignal;
  /** 来源层级过滤（后端 `source_layer`）。 */
  layer?: string;
  /** 来源目录过滤（后端 `source_dir`）。 */
  dir?: string;
};

export type SkillSearchQuery = {
  query: string;
  limit?: number;
  category?: string;
  mode?: RuntimeSkillSearchModeInput;
};

export type RuntimeSkillSearchModeInput = "auto" | "lexical" | "semantic";

export type SkillMutationRequestOptions = {
  signal?: AbortSignal;
  /** 管理令牌；空串即不发送鉴权头（后端按 loopback/role 判定）。 */
  adminToken?: string;
};

function withAdminToken(
  adminToken: string | undefined,
  extra: Record<string, string>,
): Record<string, string> {
  const headers: Record<string, string> = { ...extra };
  const token = adminToken?.trim();
  if (token) {
    headers[SKILLS_ADMIN_TOKEN_HEADER] = token;
  }
  return headers;
}

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

async function postHotReload(
  action: "start" | "stop" | "reload",
  body: Record<string, unknown> | null,
  options: SkillMutationRequestOptions,
): Promise<RuntimeHotReloadStats> {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(`${SKILLS_HOT_RELOAD_PATH}/${action}`),
    {
      method: "POST",
      headers: withAdminToken(options.adminToken, { "Content-Type": "application/json" }),
      ...(body ? { body: JSON.stringify(body) } : {}),
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
  return normalizeHotReloadStats(payload);
}

/** 启动热重载；`dirs` 为空直接抛错（后端也会 400）。 */
export async function startHotReload(
  dirs: string[],
  options: SkillMutationRequestOptions & { debounceMs?: number } = {},
): Promise<RuntimeHotReloadStats> {
  const cleaned = dirs.map((dir) => dir.trim()).filter((dir) => dir.length > 0);
  if (cleaned.length === 0) {
    throw new Error("hot reload requires at least one skill directory");
  }
  const body: Record<string, unknown> = { dirs: cleaned };
  if (typeof options.debounceMs === "number" && Number.isFinite(options.debounceMs)) {
    body.debounce_ms = Math.max(0, Math.floor(options.debounceMs));
  }
  return postHotReload("start", body, options);
}

export async function stopHotReload(
  options: SkillMutationRequestOptions = {},
): Promise<RuntimeHotReloadStats> {
  return postHotReload("stop", null, options);
}

export async function reloadHotReload(
  options: SkillMutationRequestOptions = {},
): Promise<RuntimeHotReloadStats> {
  return postHotReload("reload", null, options);
}

/**
 * 端点不可用的降级判据：路由缺失（404/405）、未实现（501）、服务未配置（503）。
 * 403 属于「策略拒绝」而非不可用，由 UI 单独呈现（绝不与未配置混为一谈）。
 */
export function isSkillsUnavailable(error: unknown): boolean {
  if (!(error instanceof RuntimeApiError)) {
    return false;
  }
  return (
    error.status === 404 ||
    error.status === 405 ||
    error.status === 501 ||
    error.status === 503
  );
}

/** 写操作被策略 / 鉴权拒绝（403）。 */
export function isSkillsForbidden(error: unknown): boolean {
  return error instanceof RuntimeApiError && error.status === 403;
}


/**
 * 执行指定的 skill。
 * 
 * POST /api/runtime/skills/{name}/execute
 * 
 * @param name - skill 名称
 * @param params - 执行参数（可选）
 * @returns 执行结果
 */
export async function executeSkill(
  name: string,
  params?: {
    prompt?: string;
    sessionId?: string;
    params?: Record<string, unknown>;
    context?: Record<string, unknown>;
    options?: Record<string, unknown>;
  },
): Promise<unknown> {
  const url = buildRuntimeUrl(`/api/runtime/skills/${encodeURIComponent(name)}/execute`);
  const response = await fetchRuntimeJson<unknown>(url, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: params ? JSON.stringify(params) : JSON.stringify({}),
  });
  return response;
}
