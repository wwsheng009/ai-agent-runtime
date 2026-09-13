// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AnalyticsSessionUsageDetail } from "@/types/runtime";

import { SessionUsagePanel } from "./session-usage-panel";

const { getAnalyticsSessionUsageMock } = vi.hoisted(() => ({
  getAnalyticsSessionUsageMock: vi.fn(),
}));

vi.mock("@/lib/runtime-api", () => ({
  getAnalyticsSessionUsage: getAnalyticsSessionUsageMock,
}));

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function buildDetail(
  sessionOverrides: Partial<AnalyticsSessionUsageDetail["session"]> = {},
): AnalyticsSessionUsageDetail {
  return {
    schema_version: "runtime.analytics.v1",
    generated_at: "2026-09-13T00:00:00Z",
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
    partial_reasons: [],
    session: {
      session_id: "session-usage-1",
      title: "面板会话",
      directory: "E:/projects/demo",
      rel_path: ".",
      total_requests: 2,
      total_responses: 2,
      total_tool_calls: 0,
      total_tokens: 12345,
      prompt_tokens: 10000,
      completion_tokens: 2345,
      cached_tokens: 2500,
      reasoning_tokens: 0,
      llm_requests: 2,
      llm_errors: 0,
      turn_count: 3,
      failed_turns: 0,
      recovered_turns: 0,
      tool_results_observed: 0,
      tool_errors: 0,
      average_response_time_ms: 1500,
      total_duration_ms: 60_000,
      usage_quality: "provider_reported",
      usage_complete: true,
      usage_coverage: 0.98,
      partial: false,
      partial_reasons: [],
      dropped_messages: 0,
      reconciliation_status: "matched",
      reconciliation_delta: 0,
      provider: "opencode.ai",
      model: "deepseek-v4.1-flash",
      status: "active",
      last_observed_at: "2026-09-13T01:00:00Z",
      ...sessionOverrides,
    },
    steps: [],
    step_count: 0,
    turns: [],
    diagnostics: [],
    error_categories: {},
  };
}

describe("SessionUsagePanel", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    getAnalyticsSessionUsageMock.mockReset();
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  async function renderPanel(sessionId = "session-usage-1") {
    await act(async () => {
      root?.render(
        <MemoryRouter>
          <SessionUsagePanel sessionId={sessionId} />
        </MemoryRouter>,
      );
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  it("renders the session usage summary and links to the usage detail page", async () => {
    getAnalyticsSessionUsageMock.mockResolvedValue(buildDetail());

    await renderPanel();

    expect(getAnalyticsSessionUsageMock).toHaveBeenCalledWith(
      "session-usage-1",
      expect.objectContaining({ adminToken: "" }),
    );
    expect(container.textContent).toContain("会话用量");
    expect(container.textContent).toContain("12.3k");
    expect(container.textContent).toContain("25.0%");
    expect(container.textContent).toContain("98.0%");

    const detailLink = container.querySelector(
      'a[href="/usage/sessions/session-usage-1"]',
    );
    expect(detailLink).not.toBeNull();
    expect(detailLink?.textContent).toContain("完整用量");
  });

  it("surfaces load failures with the backend message", async () => {
    getAnalyticsSessionUsageMock.mockRejectedValue(new Error("analytics unavailable"));

    await renderPanel();

    expect(container.textContent).toContain("会话用量加载失败");
    expect(container.textContent).toContain("analytics unavailable");
  });

  it("shows the empty state for a session without usage rows", async () => {
    getAnalyticsSessionUsageMock.mockResolvedValue(
      buildDetail({ total_tokens: 0, prompt_tokens: 0, completion_tokens: 0, llm_requests: 0, total_requests: 0, turn_count: 0 }),
    );

    await renderPanel();

    expect(container.textContent).toContain("本会话暂无用量记录");
    expect(container.textContent).toContain("完整用量");
  });

  it("refetches the summary after usage relevant runtime events", async () => {
    vi.useFakeTimers();
    try {
      getAnalyticsSessionUsageMock.mockResolvedValue(buildDetail());

      const renderWithEvent = async (
        lastRuntimeEventType: string,
        runtimeEventCount: number,
      ) => {
        await act(async () => {
          root?.render(
            <MemoryRouter>
              <SessionUsagePanel
                lastRuntimeEventType={lastRuntimeEventType}
                runtimeEventCount={runtimeEventCount}
                sessionId="session-usage-1"
              />
            </MemoryRouter>,
          );
        });
        await act(async () => {
          await Promise.resolve();
          await Promise.resolve();
        });
      };

      const flushDebounce = async () => {
        await act(async () => {
          await vi.advanceTimersByTimeAsync(1500);
        });
        await act(async () => {
          await Promise.resolve();
          await Promise.resolve();
        });
      };

      await renderWithEvent("session_start", 1);
      expect(getAnalyticsSessionUsageMock).toHaveBeenCalledTimes(1);

      // 与用量无关的事件不应触发重取。
      await renderWithEvent("checkpoint_created", 2);
      await flushDebounce();
      expect(getAnalyticsSessionUsageMock).toHaveBeenCalledTimes(1);

      // assistant_message = 一次 LLM 请求已完成，用量行已落库。
      await renderWithEvent("assistant_message", 3);
      await flushDebounce();
      expect(getAnalyticsSessionUsageMock).toHaveBeenCalledTimes(2);

      // 同一类型再次到达（计数变化）也应刷新。
      await renderWithEvent("assistant_message", 4);
      await flushDebounce();
      expect(getAnalyticsSessionUsageMock).toHaveBeenCalledTimes(3);
    } finally {
      vi.useRealTimers();
    }
  });

  it("polls usage while the session is responding and stays idle otherwise", async () => {
    vi.useFakeTimers();
    try {
      getAnalyticsSessionUsageMock.mockResolvedValue(buildDetail());

      const renderPanelWith = async (isResponding: boolean) => {
        await act(async () => {
          root?.render(
            <MemoryRouter>
              <SessionUsagePanel
                isResponding={isResponding}
                sessionId="session-usage-1"
              />
            </MemoryRouter>,
          );
        });
        await act(async () => {
          await Promise.resolve();
          await Promise.resolve();
        });
      };

      await renderPanelWith(true);
      expect(getAnalyticsSessionUsageMock).toHaveBeenCalledTimes(1);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(10_000);
      });
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(getAnalyticsSessionUsageMock).toHaveBeenCalledTimes(2);

      await renderPanelWith(false);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(getAnalyticsSessionUsageMock).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });
});
