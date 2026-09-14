// P2-1A：运行时 Skills 契约（技能市场 / 热重载面板消费）。
//
// 后端真源（backend/pkg/skillsapi/client.go 与 internal/api/skills/handler.go）：
//   * GET  /api/runtime/skills            → {skills: Skill[], count}
//   * GET  /api/runtime/skills/{name}     → Skill（404 未找到）
//   * GET  /api/runtime/skills/search     → {query, results, matches, count, limit,
//                                            requested_mode, resolved_mode, used_embedding}
//   * GET  /api/runtime/skills/stats      → {stats: SkillStats[], total_skills, skill_dirs,
//                                            source_summary, mutation_policy, ...}
//   * GET  /api/runtime/skills/hot-reload/stats → {stats: {...}}（未配置热重载时 503）
//   * POST /api/runtime/skills/hot-reload/{start,stop,reload}（写操作，受 mutation policy 约束）
//
// `Skill` 的 JSON 键由 Go struct tag 固定：`systemPrompt` / `userPrompt` / `dependsOn` /
// `prompt_path` 等大小写混合，是后端既有契约（不在前端改写）。

/** 技能来源（`SkillSource`）。`layer` 为来源层级，`dir` 为所在目录。 */
export type RuntimeSkillSource = {
  path: string;
  dir: string;
  layer: string;
  promptPath: string;
};

export type RuntimeSkillTrigger = {
  type: string;
  values: string[];
  /** 权重缺省即 0（后端 `omitempty` 不写出零值，这里不臆造非零权重）。 */
  weight: number | null;
};

export type RuntimeSkillWorkflowStep = {
  id: string;
  name: string;
  tool: string;
  args: Record<string, unknown>;
  dependsOn: string[];
  condition: string;
};

/** 归一化后的技能条目；空字符串 / 空数组代表后端未提供该字段。 */
export type RuntimeSkill = {
  name: string;
  description: string;
  version: string;
  category: string;
  capabilities: string[];
  tags: string[];
  triggers: RuntimeSkillTrigger[];
  tools: string[];
  systemPrompt: string;
  userPrompt: string;
  workflowSteps: RuntimeSkillWorkflowStep[];
  contextFiles: string[];
  contextEnvironment: string[];
  contextSymbols: string[];
  permissions: string[];
  source: RuntimeSkillSource | null;
};

/** `GET /skills` 的归一化结果：`count` 为后端上报值（与 `skills.length` 可能不同）。 */
export type RuntimeSkillCatalog = {
  skills: RuntimeSkill[];
  count: number;
};

export type RuntimeSkillSearchMatch = {
  skill: RuntimeSkill;
  score: number | null;
  matchedBy: string;
  details: string;
};

export type RuntimeSkillSearchMode = "auto" | "lexical" | "semantic";

export type RuntimeSkillSearchResult = {
  query: string;
  results: RuntimeSkill[];
  matches: RuntimeSkillSearchMatch[];
  count: number;
  limit: number;
  /** 请求模式与后端实际解析出的模式（语义检索不可用时后端会降级，如实展示）。 */
  requestedMode: string;
  resolvedMode: string;
  usedEmbedding: boolean;
};

export type RuntimeSkillStatRow = {
  name: string;
  category: string;
  callCount: number;
  successRate: number;
  /** 后端字段为 `avg_duration_ms`；缺席即 null（不补 0，避免把未知时长说成零）。 */
  avgDurationMs: number | null;
  sourceDir: string;
  sourcePath: string;
  sourceLayer: string;
};

/** `mutation_policy` 快照（handler.go:5926-5935）。字段缺席即 `null`（策略未知）。 */
export type RuntimeSkillMutationPolicy = {
  readOnly: boolean | null;
  disableImport: boolean | null;
  disablePersist: boolean | null;
  disableReloadOps: boolean | null;
  disableHotReload: boolean | null;
};

export type RuntimeSkillStats = {
  rows: RuntimeSkillStatRow[];
  totalSkills: number;
  skillDirs: string[];
  sourceSummary: Record<string, number>;
  /** 后端未给出 `mutation_policy` 时为 null——UI 显示「策略未知」而不是猜默认值。 */
  policy: RuntimeSkillMutationPolicy | null;
  embeddingEnabled: boolean | null;
  /** 原始快照，供面板如实呈现未建模字段。 */
  raw: Record<string, unknown>;
};

/**
 * 热重载统计（`internal/skill/hot_reload.go:692-711`）：
 * 已知键结构化，其余键保存在 `raw` 供原样呈现。
 */
export type RuntimeHotReloadStats = {
  enabled: boolean | null;
  watching: boolean | null;
  skillDir: string;
  skillDirs: string[];
  skillCount: number | null;
  callbackCount: number | null;
  debounceTime: string;
  raw: Record<string, unknown>;
};
