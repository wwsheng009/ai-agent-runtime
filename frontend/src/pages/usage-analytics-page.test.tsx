import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { buildAnalyticsGroupSelectionSearchParams } from "./usage-analytics-filters";
import { UsageAnalyticsPage } from "./usage-analytics-page";
import { emptyCoverage, emptyTotals } from "./usage-analytics/defaults";
import type { AnalyticsSessionUsageDetail } from "@/types/runtime";

// React 19 要求显式声明 act 测试环境，否则每次 act 都输出警告。
(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const {
  getAnalyticsSessionUsageMock,
  listAnalyticsSessionsMock,
  getAnalyticsSummaryMock,
  getAnalyticsDimensionsMock,
  getCacheCapabilitiesMock,
  getCacheOverviewMock,
  getCacheRequestsMock,
} = vi.hoisted(() => ({
  getAnalyticsSessionUsageMock: vi.fn(),
  listAnalyticsSessionsMock: vi.fn(),
  getAnalyticsSummaryMock: vi.fn(),
  getAnalyticsDimensionsMock: vi.fn(),
  getCacheCapabilitiesMock: vi.fn().mockResolvedValue({
    schema_version: "cache.analytics.v1",
    data_source: "session_runtime",
    max_requests_per_session: 2000,
    supports_sse: false,
  }),
  getCacheOverviewMock: vi.fn().mockResolvedValue({
    schema_version: "cache.analytics.v1",
    session_id: "session-null-arrays",
    generated_at: "2026-09-13T00:00:00Z",
    requests_total: 0,
    requests_with_usage: 0,
    requests_cache_reported: 0,
    tokens: {
      prompt_tokens: 0,
      completion_tokens: 0,
      total_tokens: 0,
      cache_read_tokens: 0,
      cache_creation_tokens: 0,
      reasoning_tokens: 0,
    },
    cache_status_distribution: {
      hit: 0,
      write: 0,
      reported_zero: 0,
      not_reported: 0,
      error: 0,
    },
    coverage: {},
  }),
  getCacheRequestsMock: vi.fn().mockResolvedValue({
    schema_version: "cache.analytics.v1",
    session_id: "session-null-arrays",
    total: 0,
    limit: 50,
    offset: 0,
    requests: [],
  }),
}));

vi.mock("@/lib/runtime-api", () => ({
  getAnalyticsDimensions: getAnalyticsDimensionsMock,
  getAnalyticsSessionUsage: getAnalyticsSessionUsageMock,
  getAnalyticsSummary: getAnalyticsSummaryMock,
  listAnalyticsSessions: listAnalyticsSessionsMock,
  getCacheCapabilities: getCacheCapabilitiesMock,
  getCacheOverview: getCacheOverviewMock,
  getCacheRequests: getCacheRequestsMock,
  getMessageTrace: vi.fn(),
}));

async function flush(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

describe("buildAnalyticsGroupSelectionSearchParams", () => {
  it("maps a day bucket to an exact date range instead of a log directory", () => {
    const current = new URLSearchParams("provider=openai&offset=50");

    const next = buildAnalyticsGroupSelectionSearchParams(
      current,
      "day",
      "2026-07-27",
    );

    expect(next.get("from")).toBe("2026-07-27");
    expect(next.get("to")).toBe("2026-07-27");
    expect(next.get("directory")).toBeNull();
    expect(next.get("provider")).toBe("openai");
    expect(next.get("offset")).toBeNull();
  });

  it("clears unknown dimension filters while preserving the remaining scope", () => {
    const current = new URLSearchParams("provider=openai&q=failed&offset=50");

    const next = buildAnalyticsGroupSelectionSearchParams(
      current,
      "provider",
      "(unknown)",
    );

    expect(next.get("provider")).toBeNull();
    expect(next.get("q")).toBe("failed");
    expect(next.get("offset")).toBeNull();
  });
});

describe("UsageAnalyticsPage", () => {
  it("renders the responsive analytics shell and accessible navigation", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/usage"]}>
        <Routes>
          <Route path="/usage" element={<UsageAnalyticsPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(markup).toContain("会话与总体用量");
    expect(markup).toContain("分析范围与筛选");
    expect(markup).toContain('aria-label="分析页面导航"');
    expect(markup).toContain('aria-label="日志"');
    expect(markup).toContain('aria-label="刷新"');
    expect(markup).toContain('href="/workspace/chats/new"');
    expect(markup).toContain("surface-panel");
    // 契约：/usage/cache 已删除，页面不再暴露缓存 tab/链接。
    expect(markup).not.toContain('href="/usage/cache"');
    expect(markup).not.toContain("分析视图");
  });
});

describe("UsageAnalyticsPage session detail", () => {
  it("renders when the backend serializes empty collections as null", async () => {
    // 回归：Go 后端空切片会序列化为 null（如 partial_reasons/turns/steps/
    // diagnostics），SessionDetail 必须归一化，否则 QualityNotice 取
    // reasons.length 时崩溃导致整页白屏。
    const detailWithNullCollections = {
      schema_version: "runtime.analytics.v1",
      generated_at: "2026-09-10T12:00:00Z",
      data_window: {},
      coverage: {
        sessions: 1,
        sessions_with_usage: 1,
        usage_session_rate: 1,
        llm_requests: 2,
        llm_requests_with_usage: 2,
        usage_request_rate: 1,
        tool_results_observed: 0,
        dropped_messages: 0,
      },
      partial: false,
      partial_reasons: null,
      session: {
        session_id: "session-null-arrays",
        title: "回归测试会话",
        status: "active",
        usage_quality: "provider_reported",
        usage_coverage: 1,
        partial: false,
        turn_count: 1,
        failed_turns: 0,
        llm_requests: 2,
        llm_requests_with_usage: 2,
        llm_errors: 0,
        tool_errors: 0,
        tool_results_observed: 0,
        total_duration_ms: 1000,
        total_tokens: 10,
        prompt_tokens: 8,
        completion_tokens: 2,
        reconciliation_status: "matched",
        reconciliation_delta: 0,
      },
      steps: null,
      step_count: 0,
      turns: null,
      diagnostics: null,
      error_categories: null,
    } as unknown as AnalyticsSessionUsageDetail;
    getAnalyticsSessionUsageMock.mockResolvedValue(detailWithNullCollections);

    const container = document.createElement("div");
    document.body.appendChild(container);
    let root: Root | undefined;
    try {
      await act(async () => {
        root = createRoot(container);
        root.render(
          <MemoryRouter initialEntries={["/usage/sessions/session-null-arrays"]}>
            <Routes>
              <Route path="/usage/sessions/:sessionId" element={<UsageAnalyticsPage />} />
            </Routes>
          </MemoryRouter>,
        );
      });
      // 等待 load() 的异步 setState 完成。
      await act(async () => {
        await Promise.resolve();
      });

      expect(getAnalyticsSessionUsageMock).toHaveBeenCalledWith(
        "session-null-arrays",
        expect.objectContaining({ adminToken: "" }),
      );
      expect(container.textContent).toContain("回归测试会话");
      expect(container.textContent).not.toContain("加载中");
      // 契约：缓存分析面板内嵌在会话详情页。
      expect(container.textContent).toContain("LLM 缓存分析");
      expect(container.textContent).toContain("缓存状态分布");
      expect(getCacheRequestsMock).toHaveBeenCalledWith("session-null-arrays", {
        limit: 50,
        offset: 0,
      });
    } finally {
      act(() => root?.unmount());
      container.remove();
    }
  });

  it("renders the tokens tab without the duplicated LLM request detail table", async () => {
    const detail = {
      schema_version: "runtime.analytics.v1",
      generated_at: "2026-09-10T12:00:00Z",
      data_window: {},
      coverage: {
        sessions: 1,
        sessions_with_usage: 1,
        usage_session_rate: 1,
        llm_requests: 1,
        llm_requests_with_usage: 1,
        usage_request_rate: 1,
        tool_results_observed: 0,
        dropped_messages: 0,
      },
      partial: false,
      partial_reasons: [],
      session: {
        session_id: "session-tokens-tab",
        title: "Token 视图会话",
        status: "active",
        usage_quality: "provider_reported",
        usage_coverage: 1,
        partial: false,
        turn_count: 0,
        failed_turns: 0,
        llm_requests: 2,
        llm_requests_with_usage: 2,
        llm_errors: 0,
        tool_errors: 0,
        tool_results_observed: 0,
        total_duration_ms: 1000,
        total_tokens: 10,
        prompt_tokens: 8,
        completion_tokens: 2,
        reconciliation_status: "matched",
        reconciliation_delta: 0,
      },
      steps: [
        { trace_id: "trace-dup-1", step: 1, duration_ms: 1200, success: true, total_tokens: 10, cached_tokens: 5, context_utilization: 0.5, usage_available: true },
      ],
      step_count: 1,
      turns: [],
      diagnostics: [],
      error_categories: {},
    } as unknown as AnalyticsSessionUsageDetail;
    getAnalyticsSessionUsageMock.mockResolvedValue(detail);

    const container = document.createElement("div");
    document.body.appendChild(container);
    let root: Root | undefined;
    try {
      await act(async () => {
        root = createRoot(container);
        root.render(
          <MemoryRouter initialEntries={["/usage/sessions/session-tokens-tab?tab=tokens"]}>
            <Routes>
              <Route path="/usage/sessions/:sessionId" element={<UsageAnalyticsPage />} />
            </Routes>
          </MemoryRouter>,
        );
      });
      await act(async () => {
        await Promise.resolve();
      });

      // Token tab 仍渲染轮次表，但不再重复渲染步骤级「LLM 请求明细」；
      // 逐请求事实只在缓存面板的「请求明细」表中呈现（已合并 Trace / 耗时 / 结果列）。
      expect(container.textContent).toContain("轮次 Token");
      expect(container.textContent).not.toContain("LLM 请求明细");
      expect(container.textContent).toContain("请求明细");
      expect(container.textContent).not.toContain("trace-dup-1");
    } finally {
      act(() => root?.unmount());
      container.remove();
    }
  });
});

describe("UsageAnalyticsPage session list pagination", () => {
  it("pages the session list with DB offset/limit instead of client-side slicing", async () => {
    listAnalyticsSessionsMock.mockImplementation((query: { offset?: number } = {}) =>
      Promise.resolve({
        schema_version: "runtime.analytics.v1",
        generated_at: "2026-09-13T00:00:00Z",
        sessions: [
          {
            session_id: `session-page-${query.offset ?? 0}`,
            title: "分页会话",
            status: "active",
            project: "",
            provider: "anthropic",
            model: "claude",
            title_source: "session_history",
            usage_quality: "provider_reported",
            usage_coverage: 1,
            partial: false,
            turn_count: 1,
            failed_turns: 0,
            llm_requests: 1,
            llm_errors: 0,
            total_tokens: 10,
            start_time: "2026-09-13T00:00:00Z",
          },
        ],
        totals: emptyTotals,
        coverage: emptyCoverage,
        partial: false,
        partial_reasons: [],
        total: 120,
        scanned: 120,
      }),
    );
    getAnalyticsSummaryMock.mockResolvedValue({
      schema_version: "runtime.analytics.v1",
      generated_at: "2026-09-13T00:00:00Z",
      group_by: "day",
      groups: [],
      totals: emptyTotals,
    });
    getAnalyticsDimensionsMock.mockResolvedValue({
      schema_version: "runtime.analytics.v1",
      generated_at: "2026-09-13T00:00:00Z",
      providers: [],
      models: [],
      directories: [],
      projects: [],
      statuses: [],
    });

    const container = document.createElement("div");
    document.body.appendChild(container);
    let root: Root | undefined;
    try {
      await act(async () => {
        root = createRoot(container);
        root.render(
          <MemoryRouter initialEntries={["/usage"]}>
            <Routes>
              <Route path="/usage" element={<UsageAnalyticsPage />} />
            </Routes>
          </MemoryRouter>,
        );
      });
      await flush();

      expect(listAnalyticsSessionsMock).toHaveBeenCalledWith(
        expect.objectContaining({ limit: 50, offset: 0 }),
      );
      expect(container.textContent).toContain("第 1-1 条，共 120 条");

      const next = container.querySelector('button[aria-label="下一页"]');
      expect(next).not.toBeNull();
      await act(async () => {
        next?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      });
      await flush();

      expect(listAnalyticsSessionsMock).toHaveBeenLastCalledWith(
        expect.objectContaining({ limit: 50, offset: 50 }),
      );
      expect(container.textContent).toContain("第 51-51 条，共 120 条");
    } finally {
      act(() => root?.unmount());
      container.remove();
    }
  });
});
