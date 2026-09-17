// @vitest-environment jsdom

// 批次 7.2：会话观测面板（工具 / 子代理 / 失败模式）渲染与降级分支。
// 空库语义（空数组）→ 「暂无数据」；加载失败 → role="alert"；下钻 → 回调带出过滤条件。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  AnalyticsErrorPattern,
  AnalyticsSubagentStatsResponse,
  AnalyticsToolStatsResponse,
} from "@/types/runtime";

const { listAnalyticsToolsMock, getAnalyticsSubagentsMock, listAnalyticsErrorsMock } =
  vi.hoisted(() => ({
    listAnalyticsToolsMock: vi.fn(),
    getAnalyticsSubagentsMock: vi.fn(),
    listAnalyticsErrorsMock: vi.fn(),
  }));

vi.mock("@/api/runtime/analytics", () => ({
  listAnalyticsTools: listAnalyticsToolsMock,
  getAnalyticsSubagents: getAnalyticsSubagentsMock,
  listAnalyticsErrors: listAnalyticsErrorsMock,
}));

import { ErrorPatternsPanel } from "./error-patterns-panel";
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
});
