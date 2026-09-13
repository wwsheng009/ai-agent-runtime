// 由 pages/usage-analytics/primitives.tsx 抽出的空态常量（react-refresh/only-export-components）。
// primitives.tsx 只导出组件，常量集中在本模块供 overview 等消费方复用。

import type {
  AnalyticsCoverage,
  AnalyticsDimensionsResponse,
  AnalyticsGlobalTotals,
} from "@/types/runtime";

export const emptyTotals: AnalyticsGlobalTotals = {
  sessions: 0,
  total_requests: 0,
  total_responses: 0,
  total_tool_calls: 0,
  llm_requests: 0,
  llm_successes: 0,
  llm_errors: 0,
  turns: 0,
  failed_turns: 0,
  recovered_turns: 0,
  tool_results_observed: 0,
  tool_errors: 0,
  total_duration_ms: 0,
  total_tokens: 0,
  prompt_tokens: 0,
  completion_tokens: 0,
  cached_tokens: 0,
  reasoning_tokens: 0,
};

export const emptyCoverage: AnalyticsCoverage = {
  sessions: 0,
  sessions_with_usage: 0,
  usage_session_rate: 0,
  llm_requests: 0,
  llm_requests_with_usage: 0,
  usage_request_rate: 0,
  tool_results_observed: 0,
  dropped_messages: 0,
};

export const emptyDimensions: AnalyticsDimensionsResponse = {
  schema_version: "runtime.analytics.v1",
  generated_at: "",
  providers: [],
  models: [],
  directories: [],
  projects: [],
  statuses: [],
};
