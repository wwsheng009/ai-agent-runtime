import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { buildAnalyticsGroupSelectionSearchParams } from "./usage-analytics-filters";
import { UsageAnalyticsPage } from "./usage-analytics-page";
import type { AnalyticsSessionUsageDetail } from "@/types/runtime";

// React 19 要求显式声明 act 测试环境，否则每次 act 都输出警告。
(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const { getAnalyticsSessionUsageMock } = vi.hoisted(() => ({
  getAnalyticsSessionUsageMock: vi.fn(),
}));

vi.mock("@/lib/runtime-api", () => ({
  getAnalyticsDimensions: vi.fn(),
  getAnalyticsSessionUsage: getAnalyticsSessionUsageMock,
  getAnalyticsSummary: vi.fn(),
  listAnalyticsSessions: vi.fn(),
}));

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
    } finally {
      act(() => root?.unmount());
      container.remove();
    }
  });
});
