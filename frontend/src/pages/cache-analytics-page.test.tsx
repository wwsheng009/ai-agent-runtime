import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { CacheAnalyticsPanel } from "./cache-analytics-page";
import { UsageAnalyticsPage } from "./usage-analytics-page";
import type { CacheRequestRecord } from "@/types/runtime";

// React 19 要求显式声明 act 测试环境，否则每次 act 都输出警告。
(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const {
  getCacheCapabilitiesMock,
  getCacheOverviewMock,
  getCacheRequestsMock,
  getMessageTraceMock,
  getAnalyticsSessionUsageMock,
} = vi.hoisted(() => ({
  getCacheCapabilitiesMock: vi.fn(),
  getCacheOverviewMock: vi.fn(),
  getCacheRequestsMock: vi.fn(),
  getMessageTraceMock: vi.fn(),
  getAnalyticsSessionUsageMock: vi.fn(),
}));

vi.mock("@/lib/runtime-api", () => ({
  getAnalyticsDimensions: vi.fn(),
  getAnalyticsSessionUsage: getAnalyticsSessionUsageMock,
  getAnalyticsSummary: vi.fn(),
  listAnalyticsSessions: vi.fn(),
  getCacheCapabilities: getCacheCapabilitiesMock,
  getCacheOverview: getCacheOverviewMock,
  getCacheRequests: getCacheRequestsMock,
  getMessageTrace: getMessageTraceMock,
}));

function cacheRequest(id: string): CacheRequestRecord {
  return {
    llm_request_id: id,
    session_id: "sess-1",
    started_at: "2026-09-13T00:00:00Z",
    provider: "anthropic",
    model: "claude",
    cache_status: "hit",
    cache_hit_ratio: 0.5,
    user_message_id: "msg-user-1",
    assistant_message_id: "msg-assistant-1",
    usage: {
      prompt_tokens: 10,
      completion_tokens: 2,
      total_tokens: 12,
      cache_read_tokens: 5,
      cache_creation_tokens: 1,
      reasoning_tokens: 0,
      cache_read_reported: true,
      cache_creation_reported: true,
    },
  } as unknown as CacheRequestRecord;
}

function primeCacheMocks() {
  getCacheCapabilitiesMock.mockResolvedValue({
    schema_version: "cache.analytics.v1",
    data_source: "session_runtime",
    max_requests_per_session: 2000,
    supports_sse: false,
  });
  getCacheOverviewMock.mockResolvedValue({
    schema_version: "cache.analytics.v1",
    session_id: "sess-1",
    generated_at: "2026-09-13T00:00:00Z",
    requests_total: 120,
    requests_with_usage: 120,
    requests_cache_reported: 100,
    tokens: {
      prompt_tokens: 100,
      completion_tokens: 20,
      total_tokens: 120,
      cache_read_tokens: 40,
      cache_creation_tokens: 10,
      reasoning_tokens: 0,
    },
    cache_hit_ratio: 0.4,
    cache_write_ratio: 0.1,
    cache_status_distribution: {
      hit: 80,
      write: 10,
      reported_zero: 20,
      not_reported: 10,
      error: 0,
    },
    coverage: { usage_request_rate: 1, cache_report_rate: 0.8 },
  });
  getCacheRequestsMock.mockImplementation(
    (_sessionId: string, query: { offset?: number; limit?: number } = {}) => {
      const offset = query.offset ?? 0;
      return Promise.resolve({
        schema_version: "cache.analytics.v1",
        session_id: "sess-1",
        total: 120,
        limit: query.limit ?? 50,
        offset,
        requests: [cacheRequest(`req-${offset + 1}`)],
      });
    },
  );
  getMessageTraceMock.mockResolvedValue(null);
}

function findButton(container: HTMLElement, label: string): HTMLButtonElement | undefined {
  return Array.from(container.querySelectorAll("button")).find(
    (button) => button.textContent?.trim() === label,
  ) as HTMLButtonElement | undefined;
}

async function flush(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

describe("CacheAnalyticsPanel", () => {
  it("renders the embedded cache panel without the standalone page shell", () => {
    const markup = renderToStaticMarkup(
      <CacheAnalyticsPanel sessionId="sess-1" />,
    );

    expect(markup).toContain("LLM 缓存分析");
    expect(markup).toContain("缓存总览");
    expect(markup).toContain("缓存状态分布");
    expect(markup).toContain("请求明细");
    // 独立页面壳（返回链接 / 会话下拉）已删除，且不得再指向 /usage/cache。
    expect(markup).not.toContain('href="/usage/cache"');
    expect(markup).not.toContain("返回用量");
    expect(markup).not.toContain("选择会话");
  });

  it("renders nothing when no session id is provided", () => {
    const markup = renderToStaticMarkup(<CacheAnalyticsPanel sessionId="  " />);

    expect(markup).not.toContain("缓存总览");
  });

  it("paginates cache requests through the offset query parameter", async () => {
    primeCacheMocks();
    const container = document.createElement("div");
    document.body.appendChild(container);
    let root: Root | undefined;
    try {
      await act(async () => {
        root = createRoot(container);
        root.render(<CacheAnalyticsPanel sessionId="sess-1" />);
      });
      await flush();

      expect(getCacheRequestsMock).toHaveBeenCalledWith("sess-1", {
        limit: 50,
        offset: 0,
      });
      expect(container.textContent).toContain("第 1-50 条，共 120 条");

      const next = findButton(container, "下一页");
      expect(next).toBeDefined();
      await act(async () => {
        next?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      });
      await flush();

      expect(getCacheRequestsMock).toHaveBeenLastCalledWith("sess-1", {
        limit: 50,
        offset: 50,
      });
      expect(container.textContent).toContain("第 51-100 条，共 120 条");
    } finally {
      act(() => root?.unmount());
      container.remove();
    }
  });
});

describe("UsageAnalyticsPage cache integration", () => {
  it("keeps /usage on the single analytics view without the cache tab", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/usage"]}>
        <Routes>
          <Route path="/usage" element={<UsageAnalyticsPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(markup).toContain("会话与总体用量");
    expect(markup).toContain('aria-label="分析页面导航"');
    expect(markup).not.toContain('href="/usage/cache"');
    expect(markup).not.toContain("分析视图");
  });

  it("renders the cache panel inside the session detail page", async () => {
    primeCacheMocks();
    getAnalyticsSessionUsageMock.mockResolvedValue({
      schema_version: "runtime.analytics.v1",
      generated_at: "2026-09-13T00:00:00Z",
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
        session_id: "sess-1",
        title: "缓存面板会话",
        status: "active",
        usage_quality: "provider_reported",
        usage_coverage: 1,
        partial: false,
        turn_count: 1,
        failed_turns: 0,
        llm_requests: 1,
        llm_requests_with_usage: 1,
        llm_errors: 0,
        tool_errors: 0,
        tool_results_observed: 0,
        total_duration_ms: 100,
        total_tokens: 12,
        prompt_tokens: 10,
        completion_tokens: 2,
        reconciliation_status: "matched",
        reconciliation_delta: 0,
      },
      steps: [],
      step_count: 0,
      turns: [],
      diagnostics: [],
      error_categories: {},
    });

    const container = document.createElement("div");
    document.body.appendChild(container);
    let root: Root | undefined;
    try {
      await act(async () => {
        root = createRoot(container);
        root.render(
          <MemoryRouter initialEntries={["/usage/sessions/sess-1"]}>
            <Routes>
              <Route path="/usage/sessions/:sessionId" element={<UsageAnalyticsPage />} />
            </Routes>
          </MemoryRouter>,
        );
      });
      await flush();

      expect(container.textContent).toContain("缓存面板会话");
      expect(container.textContent).toContain("缓存状态分布");
      expect(container.textContent).toContain("请求明细");
      expect(getCacheRequestsMock).toHaveBeenCalledWith("sess-1", {
        limit: 50,
        offset: 0,
      });
    } finally {
      act(() => root?.unmount());
      container.remove();
    }
  });
});
