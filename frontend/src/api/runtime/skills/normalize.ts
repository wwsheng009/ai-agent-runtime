// P0-2 拆分（原 skills.ts L41-L328）：REST 载荷 → 领域对象的归一化。
// 纪律见 index.ts 顶部注释：结构缺失抛错、单条坏数据只丢单条、后端上报值不改写。

import type {
  RuntimeHotReloadStats,
  RuntimeSkill,
  RuntimeSkillCatalog,
  RuntimeSkillMutationPolicy,
  RuntimeSkillSearchMatch,
  RuntimeSkillSearchResult,
  RuntimeSkillStatRow,
  RuntimeSkillStats,
  RuntimeSkillSource,
  RuntimeSkillTrigger,
  RuntimeSkillWorkflowStep,
} from "@/types/runtime";

import { DEFAULT_SKILL_SEARCH_LIMIT } from "./constants";

type RawRecord = Record<string, unknown>;

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
