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
