// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AnalyticsRequestsResponse, CacheRequestRecord } from "@/types/runtime";

const { listAnalyticsRequestsMock } = vi.hoisted(() => ({
  listAnalyticsRequestsMock: vi.fn(),
}));

vi.mock("@/api/runtime/analytics", () => ({
  listAnalyticsRequests: listAnalyticsRequestsMock,
}));

import { RequestAnalysisPanel } from "./request-analysis";

function request(
  overrides: Partial<CacheRequestRecord> = {},
): CacheRequestRecord {
  return {
    schema_version: "cache.analytics.v1",
    llm_request_id: "req-success",
    session_id: "session-a",
    provider: "openai",
    model: "gpt-5",
    status: "success",
    started_at: "2026-09-13T10:00:00Z",
    cache_status: "hit",
    cache_hit_ratio: 0.2,
    usage: {
      prompt_tokens: 800,
      completion_tokens: 200,
      total_tokens: 1_000,
      cached_tokens: 160,
      cache_read_tokens: 160,
      cache_creation_tokens: 0,
      input_total_tokens: 800,
      cache_read_reported: true,
      cache_creation_reported: false,
      reasoning_tokens: 0,
    },
    ...overrides,
  };
}

function response(records: CacheRequestRecord[]): AnalyticsRequestsResponse {
  return {
    schema_version: "runtime.analytics.v1",
    generated_at: "2026-09-13T12:00:00Z",
    total: records.length,
    limit: 50,
    offset: 0,
    summary: {
      requests: records.length,
      successes: records.filter((item) => item.status === "success").length,
      failures: records.filter((item) => item.status !== "success").length,
      input_tokens: 800,
      output_tokens: 200,
      total_tokens: 1_000,
      cache_read_tokens: 160,
      cache_creation_tokens: 0,
      sessions: 1,
      providers: 1,
      models: 1,
      cache_reported: 1,
      average_cache_hit_ratio: 0.2,
      cache_status_distribution: {
        hit: 1,
        write: 0,
        confirmed_miss: 0,
        reported_zero: 0,
        not_reported: 0,
        error: 0,
      },
    },
    requests: records,
  };
}

describe("RequestAnalysisPanel", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    listAnalyticsRequestsMock.mockReset();
    listAnalyticsRequestsMock.mockResolvedValue(response([request()]));
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT;
  });

  async function render(entry: string) {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={[entry]}>
          <Routes>
            <Route
              path="/usage"
              element={<RequestAnalysisPanel adminToken="admin-secret" />}
            />
          </Routes>
        </MemoryRouter>,
      );
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  it("passes request and cache filters to the server-side request endpoint", async () => {
    listAnalyticsRequestsMock.mockResolvedValue(response([
      request({
        llm_request_id: "req-failure-low-cache",
        status: "error",
        cache_status: "reported_zero",
        cache_hit_ratio: 0,
      }),
    ]));
    await render(
      "/usage?from=2026-09-13&to=2026-09-13&provider=openai&request_outcome=failure&request_cache=low&request_limit=50",
    );

    expect(listAnalyticsRequestsMock).toHaveBeenCalledWith(
      expect.objectContaining({
        from: "2026-09-13",
        to: "2026-09-13",
        provider: "openai",
        success: false,
        cache_hit_ratio_lt: 0.5,
        limit: 50,
        adminToken: "admin-secret",
      }),
    );
    expect(container.textContent).toContain("请求级历史分析");
    expect(container.textContent).toContain("已加载 1 条 · 匹配 1 条");
    expect(container.textContent).toContain("失败 1 条");
    expect(container.textContent).toContain("req-failure-low-cache");
    expect(container.textContent).toContain("缓存率 < 50%");
  });

  it("passes an explicit inclusive cache-rate range to the backend", async () => {
    await render(
      "/usage?view=requests&request_cache_min=50&request_cache_max=80",
    );

    expect(listAnalyticsRequestsMock).toHaveBeenLastCalledWith(
      expect.objectContaining({
        cache_hit_ratio_min: 0.5,
        cache_hit_ratio_max: 0.8,
        cache_hit_ratio_lt: undefined,
        cache_hit_ratio_gte: undefined,
      }),
    );
    expect(container.textContent).toContain("缓存率 ≥ 50%");
    expect(container.textContent).toContain("缓存率 ≤ 80%");
  });

  it("uses the one-click today low-cache filter", async () => {
    await render("/usage?view=requests");

    const button = Array.from(container.querySelectorAll("button")).find((item) =>
      item.textContent?.includes("今天 · 低缓存"),
    );
    expect(button).toBeTruthy();
    await act(async () => {
      button?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(listAnalyticsRequestsMock).toHaveBeenLastCalledWith(
      expect.objectContaining({
        cache_hit_ratio_lt: 0.5,
      }),
    );
  });

  it("renders an honest empty state when the filtered request set is empty", async () => {
    listAnalyticsRequestsMock.mockResolvedValue(response([]));

    await render("/usage?view=requests");

    expect(container.textContent).toContain("当前筛选条件下没有请求记录");
    expect(container.textContent).toContain("已加载 0 条 · 匹配 0 条");
  });

  it("opens a full request detail panel from the table", async () => {
    const longRequestID = "req-1234567890-abcdefghijklmnopqrstuvwxyz-full-id";
    listAnalyticsRequestsMock.mockResolvedValue(response([
      request({
        llm_request_id: longRequestID,
        session_id: "session-1234567890-abcdefghijklmnopqrstuvwxyz",
        trace_id: "trace-full-value",
        turn_id: "turn-full-value",
      }),
    ]));
    await render("/usage?view=requests");

    const detailButton = Array.from(document.querySelectorAll("button")).find(
      (button) => button.textContent?.includes("详情"),
    );
    expect(detailButton).toBeTruthy();
    await act(async () => {
      detailButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(document.body.textContent).toContain("请求详情");
    expect(document.body.textContent).toContain(longRequestID);
    expect(document.body.textContent).toContain("session-1234567890-abcdefghijklmnopqrstuvwxyz");
    expect(document.body.textContent).toContain("trace-full-value");
    expect(document.body.textContent).toContain("turn-full-value");
  });

  it("does not disguise an unavailable analytics database as empty", async () => {
    listAnalyticsRequestsMock.mockRejectedValue(
      Object.assign(new Error("usage analytics not configured"), { status: 503 }),
    );

    await render("/usage?view=requests");

    expect(container.textContent).toContain("用量分析库暂不可用");
    expect(container.textContent).not.toContain("当前筛选条件下没有请求记录");
  });
});
