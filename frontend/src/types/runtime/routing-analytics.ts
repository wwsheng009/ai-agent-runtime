// 路由切换观测契约（后端 backend/internal/usageanalytics/contracts_routes.go）。
//
// 端点：
//   GET /api/runtime/analytics/routing         总览（totals + 维度分布桶）
//   GET /api/runtime/analytics/routing/events  明细（时间倒序分页）
//
// 语义约定：
//   - route_changed / fallback_used 是**三态**（undefined = 事件未携带该字段），
//     因此 totals 只计 true，未知不并入 false；
//   - 分布桶与 totals 同源同过滤集（后端全量 SQL 聚合），桶计数之和恒等于 totals。

/**
 * 任务类型封闭枚举（P4 收编，后端 NormalizeTaskType 的 12 类，已排序）。
 * 配置编辑器 task_types 的键必须取自该集合；观测面板按此表映射展示标签。
 */
export const agentRoutingTaskTypes = [
  "config",
  "explore",
  "generate",
  "implement",
  "integration",
  "migrate",
  "modify",
  "refactor",
  "security",
  "test",
  "understand",
  "verify",
] as const;

export type AgentRoutingTaskType = (typeof agentRoutingTaskTypes)[number];

export type AnalyticsRouteTotals = {
  total: number;
  main_agent: number;
  subagent: number;
  applied: number;
  cleared: number;
  warnings: number;
  route_changed: number;
  fallback_used: number;
  candidate_total: number;
  distinct_sessions: number;
  distinct_models: number;
};

export type AnalyticsRouteBucket = {
  key: string;
  count: number;
  route_changed: number;
  fallback_used: number;
};

export type AnalyticsRouteStatsResponse = {
  schema_version: string;
  generated_at: string;
  totals: AnalyticsRouteTotals;
  by_scope: AnalyticsRouteBucket[];
  by_kind: AnalyticsRouteBucket[];
  by_reason: AnalyticsRouteBucket[];
  by_source: AnalyticsRouteBucket[];
  by_provider: AnalyticsRouteBucket[];
  by_model: AnalyticsRouteBucket[];
  by_difficulty: AnalyticsRouteBucket[];
  /**
   * 难度来源分布：explicit（模型显式声明）/ explicit_promoted（显式被启发式提升覆盖）
   * / inferred（未声明且命中提升）/ default（未声明且未命中）。
   * 与 by_difficulty 对照即可判断本地提升是否过火。
   */
  by_difficulty_source: AnalyticsRouteBucket[];
  /**
   * 任务类型分布（P4 task_type 收编后的主维度）：键为 12 类封闭枚举
   * （config/explore/generate/implement/integration/migrate/modify/refactor/
   * security/test/understand/verify），历史缺列行为空串桶。
   * 与 by_role 并存：by_role 是编排角色维度，兼容保留不删。
   */
  by_task_type: AnalyticsRouteBucket[];
  by_role: AnalyticsRouteBucket[];
  warnings: AnalyticsRouteBucket[];
  /** 兼容保留字段：分布桶已是全量精确聚合，后端恒为 false。 */
  sampled: boolean;
  /** 分布桶覆盖的全量行数（等于 totals.total）。 */
  sample_size: number;
};

export type AnalyticsRouteEvent = {
  recorded_at: string;
  session_id: string;
  parent_session_id?: string;
  child_session_id?: string;
  trace_id?: string;
  scope: string;
  kind: string;
  agent_id?: string;
  role?: string;
  /** 任务类型（12 类封闭枚举；未记录 task_type 的历史行为空）。 */
  task_type?: string;
  /** 任务主体（发射端按 256 字符截断；未记录的历史行为空）。 */
  task_subject?: string;
  /** 子代理任务目标（发射端按 256 字符截断；主 Agent 行与缺列旧库为空）。 */
  goal?: string;
  step: number;
  reason: string;
  source?: string;
  difficulty?: string;
  difficulty_source?: string;
  provider?: string;
  model?: string;
  reasoning_effort?: string;
  route_changed?: boolean;
  fallback_used?: boolean;
  fallback_reason?: string;
  candidate_count?: number;
  warnings?: string[];
  attempt?: number;
  max_attempts?: number;
  batch_id?: string;
};

export type AnalyticsRouteEventsResponse = {
  schema_version: string;
  generated_at: string;
  events: AnalyticsRouteEvent[];
  count: number;
  limit: number;
  offset: number;
};

/**
 * 路由观测查询参数（stats / events 共用）。
 * session 同时兼容后端的 session / session_id 两个别名。
 */
export type AnalyticsRoutingQuery = {
  from?: string;
  to?: string;
  scope?: string;
  kind?: string;
  source?: string;
  provider?: string;
  model?: string;
  difficulty?: string;
  session?: string;
  /** warnings_only=true 等价于 kind=warning（显式 kind 优先）。 */
  warnings_only?: boolean;
  limit?: number;
  offset?: number;
};
