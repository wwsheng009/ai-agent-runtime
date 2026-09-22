export type AnalyticsGroupBy =
  | "day"
  | "provider"
  | "model"
  | "directory"
  | "project"
  | "status";

export type AnalyticsTokenTotals = {
  total_tokens: number;
  prompt_tokens: number;
  completion_tokens: number;
  cached_tokens: number;
  reasoning_tokens: number;
};

export type AnalyticsGlobalTotals = AnalyticsTokenTotals & {
  sessions: number;
  total_requests: number;
  total_responses: number;
  total_tool_calls: number;
  llm_requests: number;
  llm_successes: number;
  llm_errors: number;
  turns: number;
  failed_turns: number;
  recovered_turns: number;
  tool_results_observed: number;
  tool_errors: number;
  total_duration_ms: number;
  average_response_time_ms?: number;
  /** 已观测首字时间（TTFT）的均值与样本数；样本数为 0/缺省 = 未采集。 */
  average_first_token_ms?: number;
  first_token_samples?: number;
};

export type AnalyticsSessionRollup = {
  session_id: string;
  runtime_session_id?: string;
  title?: string;
  title_source?: string;
  directory: string;
  project?: string;
  rel_path: string;
  start_time?: string;
  end_time?: string;
  last_observed_at?: string;
  status?: string;
  provider?: string;
  protocol?: string;
  model?: string;
  base_url?: string;
  stream?: boolean;
  total_requests: number;
  total_responses: number;
  total_tool_calls: number;
  total_tokens: number;
  prompt_tokens?: number;
  completion_tokens?: number;
  cached_tokens?: number;
  reasoning_tokens?: number;
  llm_requests?: number;
  llm_requests_with_usage?: number;
  llm_successes?: number;
  llm_errors?: number;
  turn_count: number;
  failed_turns: number;
  recovered_turns: number;
  tool_results_observed: number;
  tool_errors: number;
  average_response_time_ms?: number;
  total_duration_ms?: number;
  /** 会话内已观测请求的首字时间均值（样本加权）；缺省 = 未采集。 */
  average_first_token_ms?: number;
  first_token_samples?: number;
  has_debug_usage?: boolean;
  source?: string;
  usage_quality: string;
  usage_complete: boolean;
  usage_coverage: number;
  partial: boolean;
  partial_reasons: string[];
  dropped_messages: number;
  reconciliation_status: string;
  reconciliation_delta: number;
};

export type AnalyticsGroupBucket = AnalyticsTokenTotals & {
  key: string;
  sessions: number;
  total_requests: number;
  total_responses: number;
  total_tool_calls: number;
  llm_requests: number;
  llm_successes: number;
  llm_errors: number;
  turns: number;
  failed_turns: number;
  recovered_turns: number;
  tool_results_observed: number;
  tool_errors: number;
  total_duration_ms: number;
  average_response_time_ms?: number;
  average_first_token_ms?: number;
  first_token_samples?: number;
};

export type AnalyticsSessionsQuery = {
  from?: string;
  to?: string;
  provider?: string;
  model?: string;
  directory?: string;
  project?: string;
  status?: string;
  q?: string;
  limit?: number;
  offset?: number;
  max_scan?: number;
};

export type AnalyticsSummaryQuery = AnalyticsSessionsQuery & {
  group_by?: AnalyticsGroupBy;
};

export type AnalyticsCoverage = {
  sessions: number;
  sessions_with_usage: number;
  usage_session_rate: number;
  llm_requests: number;
  llm_requests_with_usage: number;
  usage_request_rate: number;
  tool_results_observed: number;
  dropped_messages: number;
};

export type AnalyticsDataWindow = {
  from?: string;
  to?: string;
};

export type AnalyticsResponseMeta = {
  schema_version: string;
  generated_at: string;
  data_window: AnalyticsDataWindow;
  coverage: AnalyticsCoverage;
  partial: boolean;
  partial_reasons: string[];
};

export type AnalyticsSessionsResponse = AnalyticsResponseMeta & {
  sessions: AnalyticsSessionRollup[];
  count: number;
  total: number;
  limit: number;
  offset: number;
  scanned: number;
  totals: AnalyticsGlobalTotals;
};

export type AnalyticsSummaryResponse = AnalyticsResponseMeta & {
  group_by: AnalyticsGroupBy | string;
  totals: AnalyticsGlobalTotals;
  groups: AnalyticsGroupBucket[];
  scanned: number;
  matched: number;
};

export type AnalyticsDimensionsResponse = {
  schema_version: string;
  generated_at: string;
  providers: string[];
  models: string[];
  directories: string[];
  projects: string[];
  statuses: string[];
};

/**
 * 首屏引导载荷（后端 /api/runtime/analytics/overview，方案 Phase 2）：
 * sessions + summary + dimensions 一次取齐，减少首屏 HTTP 往返。
 * matched 为顶层兼容字段（等于 sessions.total）。
 */
export type AnalyticsOverviewResponse = {
  schema_version: string;
  generated_at: string;
  sessions: AnalyticsSessionsResponse;
  summary: AnalyticsSummaryResponse;
  dimensions: AnalyticsDimensionsResponse;
  matched: number;
};

export type AnalyticsStepUsage = {
  started_at?: string;
  timestamp?: string;
  trace_id?: string;
  step?: number;
  success: boolean;
  prompt_tokens?: number;
  completion_tokens?: number;
  total_tokens?: number;
  cached_tokens?: number;
  cache_read_tokens?: number;
  cache_read_reported?: boolean;
  cache_hit_ratio?: number;
  cache_status?: string;
  reasoning_tokens?: number;
  usage_source?: string;
  usage_available: boolean;
  error_category?: string;
  duration_ms?: number;
  /** 首字时间（TTFT，毫秒）。0/缺省 = 未采集（非流式/历史/首字前失败）。 */
  first_token_ms?: number;
  context_prompt_tokens?: number;
  context_window_tokens?: number;
  prompt_budget?: number;
  context_utilization?: number;
};

export type AnalyticsTurnUsage = {
  turn_id: string;
  trace_id: string;
  ordinal: number;
  started_at?: string;
  ended_at?: string;
  duration_ms: number;
  /** 该 turn 内已观测请求的首字时间均值与样本数；缺省 = 该轮次无首字观测。 */
  first_token_ms?: number;
  first_token_samples?: number;
  outcome: "success" | "recovered" | "failed" | "cancelled" | string;
  error_category?: string;
  llm_requests: number;
  llm_successes: number;
  llm_errors: number;
  tool_results_observed: number;
  tool_errors: number;
  usage: AnalyticsTokenTotals;
  usage_quality: string;
  usage_coverage: number;
  max_context_utilization: number;
};

export type AnalyticsDiagnostic = {
  code: string;
  severity: "info" | "warning" | "error" | string;
  count: number;
  rate?: number;
  turn_id?: string;
  error_category?: string;
};

export type AnalyticsSessionUsageDetail = Omit<AnalyticsResponseMeta, "data_window"> & {
  session: AnalyticsSessionRollup;
  steps: AnalyticsStepUsage[];
  step_count: number;
  turns: AnalyticsTurnUsage[];
  diagnostics: AnalyticsDiagnostic[];
  error_categories: Record<string, number>;
};

// ============================================================================
// 批次 7.2：会话观测（工具 / 子代理 / 失败模式）契约。
// 字段与 backend/internal/usageanalytics/query_v2.go 的 JSON tag 一一对应；
// 空库语义：数组/map 一律为空（不是 null、不是 500），由面板渲染「暂无数据」。
// ============================================================================

export type AnalyticsErrorPattern = {
  error_code?: string;
  failure_category?: string;
  source: string;
  count: number;
};

export type AnalyticsToolStat = {
  tool_name: string;
  calls: number;
  failures: number;
  failure_rate: number;
  empty_results: number;
  retried_calls: number;
  average_duration_ms: number;
  /** 过滤窗内耗时样本（自报 duration_ms，缺失时回退 requested→completed 事件时间差）的最小/最大值。 */
  min_duration_ms: number;
  max_duration_ms: number;
  p50_duration_ms: number;
  p95_duration_ms: number;
  error_top?: AnalyticsErrorPattern[];
};

export type AnalyticsToolStatsQuery = {
  session?: string;
  tool?: string;
  outcome?: string;
  from?: string;
  to?: string;
  limit?: number;
};

export type AnalyticsToolStatsResponse = {
  schema_version: string;
  generated_at: string;
  tools: AnalyticsToolStat[];
  totals: AnalyticsToolStat;
};

export type AnalyticsSubagentStat = {
  subagent_id: string;
  parent_session_id: string;
  child_session_id?: string;
  role?: string;
  /** 任务类型（12 类封闭枚举；未记录 task_type 的历史行为空）。 */
  task_type?: string;
  /** 任务主体（发射端截断；未记录的历史行为空）。 */
  task_subject?: string;
  source?: string;
  success: boolean | null;
  completion_reason: string;
  failure_category?: string;
  error_code?: string;
  attempt: number;
  max_attempts: number;
  retry_reason?: string;
  duration_ms: number;
  usage_total_tokens: number;
  conflict_count: number;
  completed_at?: string;
};

export type AnalyticsSubagentStatsSummary = {
  total: number;
  succeeded: number;
  failed: number;
  unknown: number;
  failure_rate: number;
  timeouts: number;
  retried: number;
  failure_categories: Record<string, number>;
  sources: Record<string, number>;
};

export type AnalyticsSubagentStatsQuery = {
  session?: string;
  failure_category?: string;
  failed_only?: boolean;
  from?: string;
  to?: string;
  limit?: number;
};

export type AnalyticsSubagentStatsResponse = {
  schema_version: string;
  generated_at: string;
  summary: AnalyticsSubagentStatsSummary;
  subagents: AnalyticsSubagentStat[];
};

export type AnalyticsErrorPatternsQuery = {
  session?: string;
  source?: string;
  top?: number;
};

export type AnalyticsErrorPatternsResponse = {
  schema_version: string;
  generated_at: string;
  patterns: AnalyticsErrorPattern[];
};

// 批次 3.2：runtime status 快照 usage_analytics 健康块。
// attached=false 时字段仍然齐全（db_path 为解析后的目标路径、计数为 0、
// last_ingest_at=null、degraded=true），前端只据此显示横幅，不伪造数据。
export type AnalyticsUsageHealth = {
  attached: boolean;
  db_path?: string;
  ingested_total?: number;
  conflict_total?: number;
  last_ingest_at?: string | null;
  table_counts?: Record<string, number>;
  degraded?: boolean;
};

// ============================================================================
// F-4a：runtime status 快照 tool_efficiency 观测契约。
// 字段与 backend/internal/observability/tool_efficiency_snapshot.go 的 JSON tag
// 一一对应；runtime status 未填块 / 403 / 网络失败时由 API 层静默降级为 null。
// ============================================================================

export type AnalyticsLabeledCount = {
  labels: Record<string, string>;
  count: number;
};

export type AnalyticsArtifactArchives = {
  total: number;
  by_layer: Record<string, number>;
  by_disposition: Record<string, number>;
  series?: AnalyticsLabeledCount[];
};

export type AnalyticsArtifactTruncations = {
  total: number;
  by_layer: Record<string, number>;
  by_truncated_by: Record<string, number>;
  series?: AnalyticsLabeledCount[];
};

export type AnalyticsArtifactDeref = {
  total: number;
  followup_ratio: number;
  miss_by_reason: Record<string, number>;
};

export type AnalyticsArtifactFlow = {
  archives: AnalyticsArtifactArchives;
  truncations: AnalyticsArtifactTruncations;
  pointer_notice: Record<string, number>;
  deref: AnalyticsArtifactDeref;
  l1_l4_gap_ratio: number;
};

export type AnalyticsToolEfficiencySnapshot = {
  captured_at: string;
  preflight: {
    total: number;
    allow: number;
    deny: number;
    allow_rate: number;
    by_reason: Record<string, number>;
    by_decision: Record<string, number>;
  };
  outcomes: {
    total: number;
    by_outcome: Record<string, number>;
    by_error_code: Record<string, number>;
    success_rate: number;
    non_fail_rate: number;
  };
  disposition_replays: {
    total: number;
    by_outcome: Record<string, number>;
    by_repeat: Record<string, number>;
  };
  artifact_flow: AnalyticsArtifactFlow;
  fail_categories: Record<string, number>;
  inefficiency_flags: string[];
};

export type AnalyticsRuntimeStatusEnvelope = {
  runtime?: {
    usage_analytics?: AnalyticsUsageHealth;
    tool_efficiency?: AnalyticsToolEfficiencySnapshot;
  };
};
