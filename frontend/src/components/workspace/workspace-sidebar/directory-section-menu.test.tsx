// @vitest-environment jsdom

// 2026-09-15 布局优化：工作目录段头 ⋯ 面板的单测。
// 覆盖：默认收起（段内不再常驻统计/排序/分组/管理目录）→ 展开后的组成 →
// 统计降级在入口上的告警点 → 排序/分组回调 → 管理目录回调并收起 → 归档开关显隐与回调 →
// Escape 收面板并还焦入口 → 无会话组时不渲染排序/分组。

import { type TFunction } from "i18next";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { RuntimeSessionStats } from "@/types/runtime";

import { WorkspaceSidebarDirectorySectionMenu } from "./directory-section-menu";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const t = ((key: string, options?: { count?: number }) =>
  options?.count === undefined
    ? key
    : `${key}#${options.count}`) as unknown as TFunction<"workspace">;

function stats(overrides: Partial<RuntimeSessionStats> = {}): RuntimeSessionStats {
  return {
    total: 4,
    active: 1,
    idle: 0,
    closed: 0,
    archived: 2,
    totalMessages: 7,
    tags: {},
    ...overrides,
  };
}

type Props = Parameters<typeof WorkspaceSidebarDirectorySectionMenu>[0];

describe("WorkspaceSidebarDirectorySectionMenu", () => {
  let container: HTMLDivElement;
  let root: Root;
  const onManageDirectories = vi.fn();
  const onRefreshSessionStats = vi.fn();
  const onSelectSessionGroupingMode = vi.fn();
  const onSelectSessionOrderMode = vi.fn();
  const onToggleArchivedSessions = vi.fn();

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    onManageDirectories.mockReset();
    onRefreshSessionStats.mockReset();
    onSelectSessionGroupingMode.mockReset();
    onSelectSessionOrderMode.mockReset();
    onToggleArchivedSessions.mockReset();
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  function render(overrides: Partial<Props> = {}) {
    const props: Props = {
      hasGroups: true,
      hiddenArchivedCount: 0,
      onManageDirectories,
      onRefreshSessionStats,
      onSelectSessionGroupingMode,
      onSelectSessionOrderMode,
      onToggleArchivedSessions,
      sessionGroupingMode: "directory",
      sessionOrderMode: "updated",
      sessionStats: stats(),
      sessionStatsError: null,
      sessionStatsStatus: "ready",
      sessionStatsUnavailable: false,
      showArchivedSessions: false,
      t,
      ...overrides,
    };
    act(() => root.render(<WorkspaceSidebarDirectorySectionMenu {...props} />));
    return props;
  }

  function query(selector: string): HTMLElement | null {
    return container.querySelector<HTMLElement>(selector);
  }

  function testId(id: string): HTMLElement | null {
    return query(`[data-testid="${id}"]`);
  }

  function trigger(): HTMLButtonElement {
    const element = container.querySelector<HTMLButtonElement>(
      '[data-testid="sidebar-directories-menu-trigger"]',
    );
    if (!element) {
      throw new Error("未找到 ⋯ 入口");
    }
    return element;
  }

  function click(element: HTMLElement | null) {
    act(() => {
      element?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
  }

  function keyDown(element: HTMLElement | null, key: string) {
    act(() => {
      element?.dispatchEvent(
        new KeyboardEvent("keydown", { key, bubbles: true }),
      );
    });
  }

  function openPanel() {
    click(trigger());
  }

  it("默认收起：段内不再常驻统计 / 排序 / 分组 / 管理目录", () => {
    render();

    expect(trigger().getAttribute("aria-haspopup")).toBe("dialog");
    expect(trigger().getAttribute("aria-expanded")).toBe("false");
    expect(testId("sidebar-directories-menu-panel")).toBeNull();
    // 迁入弹层的四种控件在收起态一律不在 DOM 中（这正是本次布局优化的账）。
    expect(testId("session-stats-summary")).toBeNull();
    expect(testId("sidebar-session-order-control")).toBeNull();
    expect(testId("sidebar-session-grouping-control")).toBeNull();
    expect(testId("sidebar-directories-manage-entry")).toBeNull();
  });

  it("展开后一个面板内提供统计 / 排序 / 分组 / 管理目录 / 归档", () => {
    render({ hiddenArchivedCount: 3 });

    openPanel();

    expect(trigger().getAttribute("aria-expanded")).toBe("true");
    expect(testId("sidebar-directories-menu-panel")).not.toBeNull();
    expect(testId("session-stats-chip-total")?.textContent).toBe(
      "sidebar.sessionStats.total#4",
    );
    expect(testId("session-stats-refresh")).not.toBeNull();
    expect(testId("sidebar-session-order-control")).not.toBeNull();
    expect(testId("sidebar-session-grouping-control")).not.toBeNull();
    expect(testId("sidebar-directories-manage-entry")?.textContent).toContain(
      "sidebar.directories.manage",
    );
    expect(testId("sidebar-session-archived-toggle")?.textContent).toContain(
      "sidebar.session.showArchived#3",
    );
  });

  it("无会话组时不给排序 / 分组空控件，统计与管理目录仍在", () => {
    render({ hasGroups: false });

    openPanel();

    expect(testId("sidebar-session-order-control")).toBeNull();
    expect(testId("sidebar-session-grouping-control")).toBeNull();
    expect(testId("session-stats-summary")).not.toBeNull();
    expect(testId("sidebar-directories-manage-entry")).not.toBeNull();
  });

  it("统计降级时入口上留告警点，面板收起也可见", () => {
    render({ sessionStats: null, sessionStatsStatus: "error" });

    expect(testId("sidebar-directories-menu-alert")).not.toBeNull();

    click(trigger());
    expect(testId("session-stats-error")).not.toBeNull();
  });

  it("统计就绪时入口不带告警点", () => {
    render();

    expect(testId("sidebar-directories-menu-alert")).toBeNull();
  });

  it("排序 / 分组选择各自回调，面板保持展开", () => {
    render();

    openPanel();
    const orderControl = testId("sidebar-session-order-control");
    const manual = Array.from(
      orderControl?.querySelectorAll<HTMLButtonElement>("button") ?? [],
    ).find((button) => button.textContent === "sidebar.sessionOrder.manual");
    click(manual ?? null);

    expect(onSelectSessionOrderMode).toHaveBeenCalledWith("manual");
    expect(testId("sidebar-directories-menu-panel")).not.toBeNull();

    const groupingControl = testId("sidebar-session-grouping-control");
    const flat = Array.from(
      groupingControl?.querySelectorAll<HTMLButtonElement>("button") ?? [],
    ).find((button) => button.textContent === "sidebar.sessionGrouping.flat");
    click(flat ?? null);

    expect(onSelectSessionGroupingMode).toHaveBeenCalledWith("flat");
    expect(testId("sidebar-directories-menu-panel")).not.toBeNull();
  });

  it("管理目录：回调并收起面板", () => {
    render();

    openPanel();
    click(testId("sidebar-directories-manage-entry"));

    expect(onManageDirectories).toHaveBeenCalledTimes(1);
    expect(testId("sidebar-directories-menu-panel")).toBeNull();
  });

  it("归档开关：无隐藏归档且未展开时不出现，展开后回调", () => {
    render({ hiddenArchivedCount: 0, showArchivedSessions: false });
    openPanel();
    expect(testId("sidebar-session-archived-toggle")).toBeNull();

    act(() => root.unmount());
    container.remove();
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);

    render({ showArchivedSessions: true });
    openPanel();
    click(testId("sidebar-session-archived-toggle"));
    expect(onToggleArchivedSessions).toHaveBeenCalledTimes(1);
  });

  it("Escape 收起面板并把焦点还给入口", () => {
    render();

    openPanel();
    expect(document.activeElement).toBe(testId("sidebar-directories-menu-panel"));

    keyDown(testId("sidebar-directories-menu-panel"), "Escape");

    expect(testId("sidebar-directories-menu-panel")).toBeNull();
    expect(document.activeElement).toBe(trigger());
  });
});
