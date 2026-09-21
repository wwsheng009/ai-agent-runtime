// ============================================================================
// LLM 缓存分析契约（cache.analytics.v1，docs/plan/llm-cache-analytics-unified-plan.md §4）。
// 字段与 backend/internal/cacheanalytics/types.go 的 JSON 标签一一对应。
// ============================================================================

export type CacheUsage = {
  usage_source?: string;
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  cached_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  cache_read_reported: boolean;
  cache_creation_reported: boolean;
  reasoning_tokens: number;
};

export type CacheRequestRecord = {
  schema_version: string;
  llm_request_id: string;
  session_id: string;
  trace_id?: string;
  turn_id?: string;
  step?: number;
  provider?: string;
  model?: string;
  stream?: boolean;
  status: string;
  attempt?: number;
  started_at: string;
  finished_at?: string;
  duration_ms?: number;
  /**
   * 首字时间（TTFT，毫秒）。0/缺省 = 未采集（非流式请求、历史记录或首个增量
   * 前就失败）；展示层必须显示「未采集」而不是 0ms（总耗时不可冒充首字）。
   */
  first_token_ms?: number;
  usage?: CacheUsage;
  cache_hit_ratio?: number;
  cache_write_ratio?: number;
  cache_status: string;
  cache_epoch?: number;
  prompt_cache_key?: string;
  prompt_fingerprint?: string;
  user_message_id?: string;
  assistant_message_id?: string;
  provider_request_id?: string;
  error_category?: string;
  correlation_source?: string;
};

export type CacheStatusDistribution = {
  hit: number;
  write: number;
  reported_zero: number;
  not_reported: number;
  error: number;
};

export type CacheCoverageInfo = {
  usage_request_rate?: number;
  cache_report_rate?: number;
  partial: boolean;
  partial_reasons?: string[];
};

export type CacheOverviewTokens = {
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  reasoning_tokens: number;
};

export type CacheOverview = {
  schema_version: string;
  session_id: string;
  generated_at: string;
  window_from?: string;
  window_to?: string;
  requests_total: number;
  requests_with_usage: number;
  requests_cache_reported: number;
  tokens: CacheOverviewTokens;
  cache_hit_ratio?: number;
  cache_write_ratio?: number;
  cache_status_distribution: CacheStatusDistribution;
  coverage: CacheCoverageInfo;
  /** 延迟事实：总耗时与首字时间的会话级均值（样本数为 0 时均值不可用）。 */
  duration_samples?: number;
  average_duration_ms?: number;
  first_token_samples?: number;
  average_first_token_ms?: number;
};

export type CacheRequestListResponse = {
  schema_version: string;
  session_id: string;
  total: number;
  limit: number;
  offset: number;
  requests: CacheRequestRecord[];
};

export type CacheCapabilities = {
  schema_version: string;
  data_source: string;
  max_requests_per_session: number;
  supports_sse: boolean;
  supported_events?: string[];
};

export type CacheProducedBy = {
  llm_request_id: string;
  usage?: CacheUsage;
  cache_hit_ratio?: number;
  cache_status?: string;
  trace_id?: string;
  turn_id?: string;
  started_at?: string;
};

export type CacheConsumedBy = {
  llm_request_id: string;
  step?: number;
  turn_id?: string;
  cache_hit_ratio?: number;
  cache_status?: string;
  started_at?: string;
};

export type CacheMessageNeighbors = {
  prev_message_id?: string;
  next_message_id?: string;
};

export type CacheMessageTrace = {
  schema_version: string;
  session_id: string;
  message_id: string;
  message_role?: string;
  turn_id?: string;
  produced_by?: CacheProducedBy;
  consumed_by?: CacheConsumedBy[];
  neighbors: CacheMessageNeighbors;
  history_available: boolean;
  correlation_source?: string;
};

export type CacheRequestsQuery = {
  trace_id?: string;
  turn_id?: string;
  message_id?: string;
  status?: string;
  cache_status?: string;
  from?: string;
  to?: string;
  limit?: number;
  offset?: number;
};
