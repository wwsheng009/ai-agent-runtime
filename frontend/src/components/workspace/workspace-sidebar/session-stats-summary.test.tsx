// @vitest-environment jsdom

// P2-1A：侧栏会话统计摘要组件单测（状态分列 / chip 渲染 / 刷新交互）。

import { type TFunction } from "i18next";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { RuntimeApiError } from "@/api/runtime/shared";
import type { RuntimeSessionStats } from "@/types/runtime";

import { WorkspaceSidebarSessionStatsSummary } from "./session-stats-summary";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const t = ((key: string, options?: { count?: number }) =>
  options?.count === undefined ? key : `${key}#${options.count}`) as unknown as TFunction<"workspace">;

function stats(overrides: Partial<RuntimeSessionStats> = {}): RuntimeSessionStats {
  return {
    total: 4,
    active: 1,
    idle: 0,
    closed: 0,
    archived: 2,
    totalMessages: 7,
    tags: { support: 1 },
    ...overrides,
  };
}

type Props = Parameters<typeof WorkspaceSidebarSessionStatsSummary>[0];

describe("WorkspaceSidebarSessionStatsSummary", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  function render(overrides: Partial<Props> = {}) {
    const props: Props = {
      error: null,
      onRefresh: vi.fn(),
      stats: stats(),
      status: "ready",
      t,
      unavailable: false,
      ...overrides,
    };
    act(() => root.render(<WorkspaceSidebarSessionStatsSummary {...props} />));
    return props;
  }

  function query(testId: string): HTMLElement | null {
    return container.querySelector<HTMLElement>(`[data-testid="${testId}"]`);
  }

  it("就绪态渲染 total 与非零计数 chip，零值计数不出现", () => {
    render();

    expect(query("session-stats-summary")).not.toBeNull();
    expect(query("session-stats-chip-total")?.textContent).toBe(
      "sidebar.sessionStats.total#4",
    );
    expect(query("session-stats-chip-active")?.textContent).toBe(
      "sidebar.sessionStats.active#1",
    );
    expect(query("session-stats-chip-archived")?.textContent).toBe(
      "sidebar.sessionStats.archived#2",
    );
    expect(query("session-stats-chip-totalMessages")?.textContent).toBe(
      "sidebar.sessionStats.totalMessages#7",
    );
    expect(query("session-stats-chip-idle")).toBeNull();
    expect(query("session-stats-chip-closed")).toBeNull();
    expect(query("session-stats-loading")).toBeNull();
    expect(query("session-stats-error")).toBeNull();
    expect(query("session-stats-unavailable")).toBeNull();
  });

  it("首次加载（无数据）只显示加载指示，不渲染摘要与刷新按钮", () => {
    render({ status: "loading", stats: null });

    expect(query("session-stats-loading")?.textContent).toContain(
      "sidebar.sessionStats.loading",
    );
    expect(query("session-stats-summary")).toBeNull();
    expect(query("session-stats-refresh")).toBeNull();
  });

  it("有数据时刷新：加载中摘要保留且按钮禁用，就绪后点击回调触发", () => {
    const props = render({ status: "loading", stats: stats() });

    const loadingButton = query("session-stats-refresh") as HTMLButtonElement | null;
    expect(loadingButton).not.toBeNull();
    expect(loadingButton?.disabled).toBe(true);
    expect(query("session-stats-summary")).not.toBeNull();
    expect(query("session-stats-loading")).toBeNull();

    act(() => loadingButton?.click());
    expect(props.onRefresh).not.toHaveBeenCalled();

    act(() =>
      root.render(<WorkspaceSidebarSessionStatsSummary {...props} status="ready" />),
    );

    const readyButton = query("session-stats-refresh") as HTMLButtonElement | null;
    expect(readyButton?.disabled).toBe(false);

    act(() => readyButton?.click());

    expect(props.onRefresh).toHaveBeenCalledTimes(1);
  });

  it("后端不可用（503）如实提示，不与真实失败混同；重试按钮可点击", () => {
    const props = render({
      error: new RuntimeApiError(503, null),
      stats: null,
      status: "error",
      unavailable: true,
    });

    expect(query("session-stats-unavailable")).not.toBeNull();
    expect(query("session-stats-error")).toBeNull();
    expect(query("session-stats-unavailable")?.textContent).toContain(
      "sidebar.sessionStats.unavailable",
    );

    act(() => query("session-stats-retry")?.click());

    expect(props.onRefresh).toHaveBeenCalledTimes(1);
  });

  it("真实失败展示错误标记与错误文案", () => {
    render({
      error: new Error("stats exploded"),
      stats: null,
      status: "error",
      unavailable: false,
    });

    const errorBox = query("session-stats-error");
    expect(errorBox).not.toBeNull();
    expect(errorBox?.textContent).toContain("sidebar.sessionStats.error");
    expect(errorBox?.textContent).toContain("stats exploded");
    expect(query("session-stats-unavailable")).toBeNull();
  });

  it("失败但保留旧统计时，摘要与重试并存", () => {
    render({
      error: new RuntimeApiError(503, null),
      stats: stats({ total: 9 }),
      status: "error",
      unavailable: true,
    });

    expect(query("session-stats-chip-total")?.textContent).toBe(
      "sidebar.sessionStats.total#9",
    );
    expect(query("session-stats-unavailable")).not.toBeNull();
  });
});
