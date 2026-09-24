// P0-2 拆分：会话观测面板测试的共享夹具。
// 由 observability-panels.test.tsx（原 L45-L313）下沉而来：响应构造器 + act 环境类型，
// 供主测试文件复用，保持断言口径不变。

import type {
  AnalyticsErrorPattern,
  AnalyticsRouteEventsResponse,
  AnalyticsRouteStatsResponse,
  AnalyticsSubagentStatsResponse,
  AnalyticsToolStatsResponse,
} from "@/types/runtime";

export type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

export function toolResponse(): AnalyticsToolStatsResponse {
  return {
    schema_version: "usage.analytics.v2",
    generated_at: "2026-09-17T00:00:00Z",
    tools: [
      {
        tool_name: "shell",
        calls: 12,
        failures: 3,
        failure_rate: 0.25,
        empty_results: 1,
        retried_calls: 2,
        average_duration_ms: 1200,
        min_duration_ms: 300,
        max_duration_ms: 5000,
        p50_duration_ms: 900,
        p95_duration_ms: 5000,
        error_top: [
          {
            error_code: "tool_timeout",
            failure_category: "timeout",
            source: "tools",
            count: 2,
          },
        ],
      },
    ],
    totals: {
      tool_name: "all",
      calls: 12,
      failures: 3,
      failure_rate: 0.25,
      empty_results: 1,
      retried_calls: 2,
      average_duration_ms: 1200,
      min_duration_ms: 300,
      max_duration_ms: 5000,
      p50_duration_ms: 900,
      p95_duration_ms: 5000,
    },
  };
}

export function emptyToolResponse(): AnalyticsToolStatsResponse {
  return {
    schema_version: "usage.analytics.v2",
    generated_at: "2026-09-17T00:00:00Z",
    tools: [],
    totals: {
      tool_name: "all",
      calls: 0,
      failures: 0,
      failure_rate: 0,
      empty_results: 0,
      retried_calls: 0,
      average_duration_ms: 0,
      min_duration_ms: 0,
      max_duration_ms: 0,
      p50_duration_ms: 0,
      p95_duration_ms: 0,
    },
  };
}

export function subagentResponse(): AnalyticsSubagentStatsResponse {
  return {
    schema_version: "usage.analytics.v2",
    generated_at: "2026-09-17T00:00:00Z",
    summary: {
      total: 2,
      succeeded: 1,
      failed: 1,
      unknown: 0,
      failure_rate: 0.5,
      timeouts: 1,
      retried: 1,
      failure_categories: { timeout: 1 },
      sources: { scheduler: 1, spawn_team: 1 },
    },
    subagents: [
      {
        subagent_id: "subagent-ok-1",
        parent_session_id: "session-1",
        child_session_id: "child-1",
        role: "explore",
        task_type: "explore",
        task_subject: "排查路由面板",
        source: "scheduler",
        success: true,
        completion_reason: "completed",
        attempt: 1,
        max_attempts: 1,
        duration_ms: 800,
        usage_total_tokens: 1200,
        conflict_count: 0,
        completed_at: "2026-09-17T00:00:00Z",
      },
      {
        subagent_id: "subagent-failed-1",
        parent_session_id: "session-1",
        task_type: "verify",
        success: false,
        source: "spawn_team",
        completion_reason: "timeout",
        failure_category: "timeout",
        error_code: "deadline_exceeded",
        attempt: 2,
        max_attempts: 2,
        retry_reason: "transient",
        duration_ms: 30_000,
        usage_total_tokens: 400,
        conflict_count: 1,
        completed_at: "2026-09-17T00:10:00Z",
      },
    ],
  };
}

export function emptySubagentResponse(): AnalyticsSubagentStatsResponse {
  return {
    schema_version: "usage.analytics.v2",
    generated_at: "2026-09-17T00:00:00Z",
    summary: {
      total: 0,
      succeeded: 0,
      failed: 0,
      unknown: 0,
      failure_rate: 0,
      timeouts: 0,
      retried: 0,
      failure_categories: {},
      sources: {},
    },
    subagents: [],
  };
}

export const timeoutPattern: AnalyticsErrorPattern = {
  error_code: "deadline_exceeded",
  failure_category: "timeout",
  source: "subagents",
  count: 2,
};

export function routeStatsResponse(): AnalyticsRouteStatsResponse {
  return {
    schema_version: "usage.analytics.v2",
    generated_at: "2026-09-22T00:00:00Z",
    totals: {
      total: 3,
      main_agent: 1,
      subagent: 2,
      applied: 2,
      cleared: 1,
      warnings: 1,
      route_changed: 1,
      fallback_used: 1,
      candidate_total: 4,
      distinct_sessions: 2,
      distinct_models: 2,
    },
    by_scope: [
      { key: "subagent", count: 2, route_changed: 1, fallback_used: 1 },
      { key: "main_agent", count: 1, route_changed: 0, fallback_used: 0 },
    ],
    by_kind: [
      { key: "applied", count: 2, route_changed: 1, fallback_used: 1 },
      { key: "warning", count: 1, route_changed: 0, fallback_used: 0 },
    ],
    by_reason: [{ key: "resolved", count: 3, route_changed: 1, fallback_used: 1 }],
    by_source: [{ key: "explicit_promoted", count: 1, route_changed: 1, fallback_used: 0 }],
    by_provider: [{ key: "ds2api", count: 2, route_changed: 1, fallback_used: 1 }],
    by_model: [{ key: "deepseek-v4-flash", count: 2, route_changed: 1, fallback_used: 1 }],
    by_difficulty: [{ key: "hard", count: 2, route_changed: 1, fallback_used: 1 }],
    by_difficulty_source: [{ key: "inferred", count: 1, route_changed: 0, fallback_used: 0 }],
    // P4：任务类型分布（新主维度）与 role 分布（编排角色，兼容保留）并存。
    by_task_type: [
      { key: "security", count: 2, route_changed: 1, fallback_used: 1 },
      { key: "verify", count: 1, route_changed: 0, fallback_used: 0 },
    ],
    by_role: [{ key: "researcher", count: 1, route_changed: 1, fallback_used: 0 }],
    warnings: [{ key: "difficulty_floor_by_task_type:security", count: 1, route_changed: 0, fallback_used: 0 }],
    sampled: false,
    sample_size: 0,
  };
}

export function emptyRouteStatsResponse(): AnalyticsRouteStatsResponse {
  return {
    ...routeStatsResponse(),
    totals: {
      total: 0,
      main_agent: 0,
      subagent: 0,
      applied: 0,
      cleared: 0,
      warnings: 0,
      route_changed: 0,
      fallback_used: 0,
      candidate_total: 0,
      distinct_sessions: 0,
      distinct_models: 0,
    },
    by_scope: [],
    by_kind: [],
    by_reason: [],
    by_source: [],
    by_provider: [],
    by_model: [],
    by_difficulty: [],
    by_difficulty_source: [],
    by_task_type: [],
    by_role: [],
    warnings: [],
  };
}

export function routeEventsResponse(): AnalyticsRouteEventsResponse {
  return {
    schema_version: "usage.analytics.v2",
    generated_at: "2026-09-22T00:00:00Z",
    events: [
      {
        recorded_at: "2026-09-22T10:00:00Z",
        session_id: "session-1",
        child_session_id: "child-route-1",
        scope: "subagent",
        kind: "applied",
        agent_id: "subagent-route-1",
        role: "verifier",
        task_type: "verify",
        task_subject: "核对 P4 契约",
        goal: "改一个文件",
        step: 1,
        reason: "resolved",
        source: "explicit_promoted",
        difficulty: "hard",
        difficulty_source: "explicit_promoted",
        provider: "ds2api",
        model: "deepseek-v4-flash",
        reasoning_effort: "max",
        route_changed: true,
        fallback_used: true,
        candidate_count: 2,
        warnings: ["difficulty_floor_by_task_type:security"],
        attempt: 1,
        max_attempts: 2,
      },
    ],
    count: 1,
    limit: 50,
    offset: 0,
  };
}

export function emptyRouteEventsResponse(): AnalyticsRouteEventsResponse {
  return {
    schema_version: "usage.analytics.v2",
    generated_at: "2026-09-22T00:00:00Z",
    events: [],
    count: 0,
    limit: 50,
    offset: 0,
  };
}
