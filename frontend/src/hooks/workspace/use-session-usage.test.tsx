// @vitest-environment jsdom

// 新建会话时的会话用量语义：
//   * 切换会话不残留上一份快照（新建会话后环上/面板不得显示上个会话的 token）；
//   * 分析库还没有该会话（404 API_NOT_FOUND）按空态处理，不渲染成读失败；
//   * 真正的读失败（500 等）仍然照实冒泡。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { RuntimeApiError } from "@/api/runtime/shared";
import type { AnalyticsSessionUsageDetail } from "@/types/runtime";

const { getAnalyticsSessionUsageMock } = vi.hoisted(() => ({
  getAnalyticsSessionUsageMock: vi.fn(),
}));

vi.mock("@/lib/runtime-api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/runtime-api")>();
  return { ...actual, getAnalyticsSessionUsage: getAnalyticsSessionUsageMock };
});

import { useSessionUsage } from "./use-session-usage";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

type Snapshot = ReturnType<typeof useSessionUsage>;

function detail(sessionId: string, usedTokens: number): AnalyticsSessionUsageDetail {
  return {
    session: { session_id: sessionId },
    steps: [
      {
        success: true,
        usage_available: true,
        context_prompt_tokens: usedTokens,
        context_window_tokens: 1_000_000,
      },
    ],
    step_count: 1,
    turns: [],
    diagnostics: [],
    error_categories: {},
  } as unknown as AnalyticsSessionUsageDetail;
}

function Harness({
  onSnapshot,
  sessionId,
}: {
  onSnapshot: (snapshot: Snapshot) => void;
  sessionId: string;
}) {
  onSnapshot(useSessionUsage({ sessionId }));
  return null;
}

function notFoundError(): RuntimeApiError {
  return new RuntimeApiError(404, {
    error: "analytics session not found: session-new",
    code: "API_NOT_FOUND",
  });
}

describe("useSessionUsage", () => {
  let container: HTMLDivElement;
  let root: Root;
  let latest: Snapshot | null;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    latest = null;
    getAnalyticsSessionUsageMock.mockReset();
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  function render(sessionId: string) {
    act(() => {
      root.render(
        <Harness
          onSnapshot={(snapshot) => {
            latest = snapshot;
          }}
          sessionId={sessionId}
        />,
      );
    });
  }

  async function flush() {
    await act(async () => {
      await Promise.resolve();
    });
  }

  it("切换会话立即丢弃上一份快照，不让旧 token 盖在新会话上", async () => {
    getAnalyticsSessionUsageMock.mockImplementation(async (sessionId: string) =>
      detail(sessionId, 2_833),
    );
    render("session-old");
    await flush();
    expect(latest?.usage).not.toBeNull();

    // 新会话还没返回时，旧会话的用量必须已经失效。
    getAnalyticsSessionUsageMock.mockImplementation(() => new Promise(() => {}));
    render("session-new");
    expect(latest?.usage).toBeNull();
  });

  it("分析库还没有该会话（404）按空态处理，而不是读失败", async () => {
    getAnalyticsSessionUsageMock.mockRejectedValue(notFoundError());
    render("session-new");
    await flush();

    expect(latest?.error).toBeNull();
    expect(latest?.usage).toBeNull();
    expect(latest?.sessionMissing).toBe(true);
  });

  it("真正的读失败仍然照实冒泡", async () => {
    getAnalyticsSessionUsageMock.mockRejectedValue(
      new RuntimeApiError(500, { error: "usage store unavailable", code: "API_SERVER_ERROR" }),
    );
    render("session-x");
    await flush();

    expect(latest?.error).toBe("usage store unavailable");
    expect(latest?.sessionMissing).toBe(false);
  });

  it("会话已在分析库中但还没有上下文信号时，不算缺失", async () => {
    getAnalyticsSessionUsageMock.mockResolvedValue({
      session: { session_id: "session-y" },
      steps: [],
      step_count: 0,
      turns: [],
      diagnostics: [],
      error_categories: {},
    } as unknown as AnalyticsSessionUsageDetail);
    render("session-y");
    await flush();

    expect(latest?.sessionMissing).toBe(false);
    expect(latest?.error).toBeNull();
  });

  it("切到草稿会话（空 id）不发请求也不残留数据", async () => {
    getAnalyticsSessionUsageMock.mockImplementation(async (sessionId: string) =>
      detail(sessionId, 2_833),
    );
    render("session-old");
    await flush();

    render("");
    expect(getAnalyticsSessionUsageMock).toHaveBeenCalledTimes(1);
    expect(latest?.usage).toBeNull();
    expect(latest?.error).toBeNull();
  });
});