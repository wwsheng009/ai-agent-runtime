// @vitest-environment jsdom

// 批次 7.2：会话观测面板（工具 / 子代理 / 失败模式）渲染与降级分支。
// 空库语义（空数组）→ 「暂无数据」；加载失败 → role="alert"；下钻 → 回调带出过滤条件。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  AnalyticsErrorPattern,
  AnalyticsRouteEventsResponse,
  AnalyticsRouteStatsResponse,
  AnalyticsSubagentStatsResponse,
  AnalyticsToolStatsResponse,
} from "@/types/runtime";

const {
  listAnalyticsToolsMock,
  getAnalyticsSubagentsMock,
  listAnalyticsErrorsMock,
  getAnalyticsRoutingStatsMock,
  listAnalyticsRoutingEventsMock,
} = vi.hoisted(() => ({
  listAnalyticsToolsMock: vi.fn(),
  getAnalyticsSubagentsMock: vi.fn(),
  listAnalyticsErrorsMock: vi.fn(),
  getAnalyticsRoutingStatsMock: vi.fn(),
  listAnalyticsRoutingEventsMock: vi.fn(),
}));

vi.mock("@/api/runtime/analytics", () => ({
  listAnalyticsTools: listAnalyticsToolsMock,
  getAnalyticsSubagents: getAnalyticsSubagentsMock,
  listAnalyticsErrors: listAnalyticsErrorsMock,
  getAnalyticsRoutingStats: getAnalyticsRoutingStatsMock,
  listAnalyticsRoutingEvents: listAnalyticsRoutingEventsMock,
}));

import { ErrorPatternsPanel } from "./error-patterns-panel";
import { RoutingObservabilityPanel } from "./routing-observability-panel";
import { SubagentStatsPanel } from "./subagent-stats-panel";
import { ToolStatsPanel } from "./tool-stats-panel";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function toolResponse(): AnalyticsToolStatsResponse {
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

function emptyToolResponse(): AnalyticsToolStatsResponse {
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

function subagentResponse(): AnalyticsSubagentStatsResponse {
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

function emptySubagentResponse(): AnalyticsSubagentStatsResponse {
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

const timeoutPattern: AnalyticsErrorPattern = {
  error_code: "deadline_exceeded",
  failure_category: "timeout",
  source: "subagents",
  count: 2,
};

function routeStatsResponse(): AnalyticsRouteStatsResponse {
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

function emptyRouteStatsResponse(): AnalyticsRouteStatsResponse {
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

function routeEventsResponse(): AnalyticsRouteEventsResponse {
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

function emptyRouteEventsResponse(): AnalyticsRouteEventsResponse {
  return {
    schema_version: "usage.analytics.v2",
    generated_at: "2026-09-22T00:00:00Z",
    events: [],
    count: 0,
    limit: 50,
    offset: 0,
  };
}

describe("usage analytics observability panels", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    listAnalyticsToolsMock.mockReset();
    getAnalyticsSubagentsMock.mockReset();
    listAnalyticsErrorsMock.mockReset();
    getAnalyticsRoutingStatsMock.mockReset();
    listAnalyticsRoutingEventsMock.mockReset();
    Element.prototype.scrollIntoView = vi.fn();
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  async function flush() {
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  it("工具面板：空数组渲染「暂无数据」", async () => {
    listAnalyticsToolsMock.mockResolvedValue(emptyToolResponse());
    act(() => {
      root.render(<ToolStatsPanel sessionId="session-1" />);
    });
    await flush();

    expect(container.querySelector('[data-testid="tool-stats-empty"]')).not.toBeNull();
    expect(container.textContent).toContain("暂无数据");
    // 无耗时样本 → 不伪造成 0ms（与 micro web / TUI 的「未上报 → --」同口径）。
    expect(container.textContent).toContain("最小 -- · 最大 --");
    expect(listAnalyticsToolsMock).toHaveBeenCalledWith({
      session: "session-1",
      outcome: undefined,
      adminToken: undefined,
    });
  });

  it("工具面板：渲染失败率与可展开的失败样本", async () => {
    listAnalyticsToolsMock.mockResolvedValue(toolResponse());
    act(() => {
      root.render(<ToolStatsPanel sessionId="session-1" />);
    });
    await flush();

    expect(container.textContent).toContain("shell");
    expect(container.textContent).toContain("25.0%");
    expect(container.textContent).toContain("5.0 s");
    // 页头耗时指标：平均 + 最小/最大区间；行内最小列 300 ms。
    expect(container.textContent).toContain("1.2 s");
    expect(container.textContent).toContain("最小 300 ms · 最大 5.0 s");

    // 限定在表格内：页头的 Select 触发器同样带 aria-expanded。
    const toggle = container.querySelector<HTMLButtonElement>(
      'table button[aria-expanded="false"]',
    );
    expect(toggle).not.toBeNull();
    act(() => {
      toggle?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(container.textContent).toContain("tool_timeout");
    expect(container.textContent).toContain("超时");
  });

  it("工具面板：加载失败渲染 role=alert", async () => {
    listAnalyticsToolsMock.mockRejectedValue(new Error("403 forbidden"));
    act(() => {
      root.render(<ToolStatsPanel sessionId="session-1" />);
    });
    await flush();

    const alert = container.querySelector('[role="alert"]');
    expect(alert?.textContent).toContain("403 forbidden");
  });

  it("子代理面板：空数组渲染「暂无数据」并展示零值摘要", async () => {
    getAnalyticsSubagentsMock.mockResolvedValue(emptySubagentResponse());
    act(() => {
      root.render(<SubagentStatsPanel sessionId="session-1" />);
    });
    await flush();

    expect(container.querySelector('[data-testid="subagent-stats-empty"]')).not.toBeNull();
    expect(container.textContent).toContain("暂无数据");
    expect(getAnalyticsSubagentsMock).toHaveBeenCalledWith({
      session: "session-1",
      failed_only: undefined,
      failure_category: undefined,
      limit: 200,
      adminToken: undefined,
    });
  });

  it("子代理面板：渲染完成/失败、失败分类与来源", async () => {
    getAnalyticsSubagentsMock.mockResolvedValue(subagentResponse());
    act(() => {
      root.render(<SubagentStatsPanel sessionId="session-1" />);
    });
    await flush();

    expect(container.textContent).toContain("50.0%");
    expect(container.textContent).toContain("subagent-ok-1");
    expect(container.textContent).toContain("超时");
    expect(container.textContent).toContain("调度器");
    expect(container.textContent).toContain("团队");
    expect(container.textContent).toContain("第 2/2 次");
    // P4：任务类型列（12 类枚举归一为标签）与 task_subject 次行，role 列保留。
    expect(container.textContent).toContain("任务类型");
    expect(container.textContent).toContain("探索");
    expect(container.textContent).toContain("排查路由面板");
    expect(container.textContent).toContain("角色");
  });

  it("失败模式面板：空数组渲染「暂无数据」", async () => {
    listAnalyticsErrorsMock.mockResolvedValue({
      schema_version: "usage.analytics.v2",
      generated_at: "2026-09-17T00:00:00Z",
      patterns: [],
    });
    act(() => {
      root.render(
        <ErrorPatternsPanel
          sessionId="session-1"
          onDrilldown={() => {}}
        />,
      );
    });
    await flush();

    expect(container.querySelector('[data-testid="error-patterns-empty"]')).not.toBeNull();
    expect(container.textContent).toContain("暂无数据");
  });

  it("失败模式面板：点击下钻回传失败分类", async () => {
    listAnalyticsErrorsMock.mockResolvedValue({
      schema_version: "usage.analytics.v2",
      generated_at: "2026-09-17T00:00:00Z",
      patterns: [timeoutPattern],
    });
    const onDrilldown = vi.fn();
    act(() => {
      root.render(
        <ErrorPatternsPanel
          sessionId="session-1"
          onDrilldown={onDrilldown}
        />,
      );
    });
    await flush();

    expect(container.textContent).toContain("deadline_exceeded");
    const drilldown = container.querySelector<HTMLButtonElement>(
      'button[aria-label*="deadline_exceeded"]',
    );
    expect(drilldown).not.toBeNull();
    act(() => {
      drilldown?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onDrilldown).toHaveBeenCalledWith(timeoutPattern);
  });

  it("路由面板：空数组渲染「暂无路由事件」并传会话过滤", async () => {
    getAnalyticsRoutingStatsMock.mockResolvedValue(emptyRouteStatsResponse());
    listAnalyticsRoutingEventsMock.mockResolvedValue(emptyRouteEventsResponse());
    act(() => {
      root.render(<RoutingObservabilityPanel sessionId="session-1" />);
    });
    await flush();

    expect(container.querySelector('[data-testid="routing-observability-empty"]')).not.toBeNull();
    expect(container.textContent).toContain("暂无路由事件");
    expect(getAnalyticsRoutingStatsMock).toHaveBeenCalledWith({
      session: "session-1",
      scope: undefined,
      warnings_only: undefined,
      adminToken: undefined,
    });
    expect(listAnalyticsRoutingEventsMock).toHaveBeenCalledWith({
      session: "session-1",
      scope: undefined,
      warnings_only: undefined,
      adminToken: undefined,
      limit: 50,
      offset: 0,
    });
  });

  it("路由面板：渲染主/子切换指标、分布与三态明细", async () => {
    getAnalyticsRoutingStatsMock.mockResolvedValue(routeStatsResponse());
    listAnalyticsRoutingEventsMock.mockResolvedValue(routeEventsResponse());
    act(() => {
      root.render(<RoutingObservabilityPanel sessionId="session-1" />);
    });
    await flush();

    // 指标卡：主 Agent / 子 Agent / 改道 / 回退。
    expect(container.textContent).toContain("主 Agent");
    expect(container.textContent).toContain("子 Agent");
    expect(container.textContent).toContain("改道");
    expect(container.textContent).toContain("回退");
    // 分布桶标签归一：显式+提升 / 难度档位 hard。
    expect(container.textContent).toContain("显式+提升");
    expect(container.textContent).toContain("hard");
    // 难度来源维度：区分「模型显式声明」与「本地推断」，是本地网是否过火的判据。
    expect(container.textContent).toContain("推断");
    // 明细行：路由组合、三态布尔与护栏告警。
    expect(container.textContent).toContain("subagent-route-1");
    expect(container.textContent).toContain("ds2api / deepseek-v4-flash");
    // P4：by_task_type 分布卡与 by_role 分布卡并存渲染（role 卡兼容保留）。
    expect(container.textContent).toContain("安全");
    expect(container.textContent).toContain("researcher");
    expect(container.textContent).toContain("任务类型");
    // 明细行：task_type 归一为标签，task_subject 落到次行。
    expect(container.textContent).toContain("核对 P4 契约");
    // 新 warning token 前缀（prefix:value）按 {{value}} 插值出标签。
    expect(container.textContent).toContain("任务类型下限：security");
    // 子代理任务目标（goal）落到明细列。
    expect(container.textContent).toContain("改一个文件");
    expect(container.textContent).toContain("已显示 1 / 1");
    expect(container.textContent).toContain("1 条");
  });

  it("路由面板：加载失败渲染 role=alert", async () => {
    getAnalyticsRoutingStatsMock.mockRejectedValue(new Error("403 forbidden"));
    listAnalyticsRoutingEventsMock.mockRejectedValue(new Error("403 forbidden"));
    act(() => {
      root.render(<RoutingObservabilityPanel sessionId="session-1" />);
    });
    await flush();

    const alert = container.querySelector('[role="alert"]');
    expect(alert?.textContent).toContain("403 forbidden");
  });
});
