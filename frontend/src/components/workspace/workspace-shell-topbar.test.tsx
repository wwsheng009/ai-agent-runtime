// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Thread } from "@/data/mock";

import { WorkspaceShellTopbar } from "./workspace-shell-topbar";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const thread: Thread = {
  id: "thread-1",
  title: "Review runtime changes",
  summary: "",
  updatedAt: "2026-07-27T00:00:00Z",
  status: "active",
  tags: [],
  prompts: [],
  messages: [],
  artifacts: [],
};

describe("WorkspaceShellTopbar", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderTopbar(
    overrides: Partial<React.ComponentProps<typeof WorkspaceShellTopbar>> = {},
  ) {
    const props: React.ComponentProps<typeof WorkspaceShellTopbar> = {
      density: "comfortable",
      isNewThread: true,
      liveTeamCount: 0,
      onOpenSettings: vi.fn(),
      onOpenSidebar: vi.fn(),
      onToggleRightRail: vi.fn(),
      rightRailOpen: false,
      selectedThread: thread,
      threadStatusLabel: "新线程",
      threadSubtitle: "不应显示的副标题",
      transportLabel: "预置预览",
      ...overrides,
    };

    act(() => {
      root?.render(
        <MemoryRouter>
          <WorkspaceShellTopbar {...props} />
        </MemoryRouter>,
      );
    });
    return props;
  }

  it("keeps the new-thread title single-line and labels icon navigation", () => {
    const props = renderTopbar();

    expect(container.textContent).toContain("新建聊天");
    expect(container.textContent).not.toContain("不应显示的副标题");
    expect(container.textContent).not.toContain("预置预览");

    for (const label of [
      "打开聊天导航",
      "日志",
      "使用分析",
      "Runtime",
      "设置",
    ]) {
      expect(container.querySelector(`[aria-label="${label}"]`)).toBeInstanceOf(
        HTMLElement,
      );
    }

    const openSidebarButton = container.querySelector(
      'button[aria-label="打开聊天导航"]',
    );
    act(() => {
      openSidebarButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(props.onOpenSidebar).toHaveBeenCalledTimes(1);
  });

  it("keeps existing-thread new-chat and right rail actions accessible", () => {
    const props = renderTopbar({ isNewThread: false });

    expect(container.textContent).toContain(thread.title);
    expect(container.querySelector('[aria-label="新建聊天"]')).toBeInstanceOf(
      HTMLAnchorElement,
    );

    const railToggle = container.querySelector(
      '[data-testid="topbar-toggle-right-rail"]',
    );
    expect(railToggle).toBeInstanceOf(HTMLButtonElement);
    expect(railToggle?.getAttribute("aria-label")).toBe("展开右侧栏");
    expect(railToggle?.getAttribute("aria-pressed")).toBe("false");

    act(() => {
      railToggle?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(props.onToggleRightRail).toHaveBeenCalledTimes(1);
  });

  it("reflects the right rail open state and hides the toggle on new threads", () => {
    renderTopbar({ isNewThread: false, rightRailOpen: true });

    const railToggle = container.querySelector(
      '[data-testid="topbar-toggle-right-rail"]',
    );
    expect(railToggle).toBeInstanceOf(HTMLButtonElement);
    expect(railToggle?.getAttribute("aria-label")).toBe("收起右侧栏");
    expect(railToggle?.getAttribute("aria-pressed")).toBe("true");

    renderTopbar();
    expect(
      container.querySelector('[data-testid="topbar-toggle-right-rail"]'),
    ).toBeNull();
  });

  it("surfaces a non-online connection status with manual retry", () => {
    const onRetryConnection = vi.fn();
    renderTopbar({
      connectionStatus: "offline",
      isNewThread: false,
      onRetryConnection,
    });

    const badge = container.querySelector('[data-connection-status="offline"]');
    expect(badge).toBeInstanceOf(HTMLElement);
    expect(container.textContent).toContain("连接中断");

    const retryButton = badge?.querySelector("button");
    expect(retryButton?.textContent).toBe("重试");
    act(() => {
      retryButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onRetryConnection).toHaveBeenCalledTimes(1);
  });

  it("shows an online marker without retry and hides the idle state", () => {
    renderTopbar({ connectionStatus: "online", isNewThread: false });
    const online = container.querySelector('[data-connection-status="online"]');
    expect(online).toBeInstanceOf(HTMLElement);
    expect(online?.querySelector("button")).toBeNull();

    renderTopbar({ connectionStatus: "idle", isNewThread: false });
    expect(container.querySelector("[data-connection-status]")).toBeNull();
  });

  it("renders the transport state as an icon whose copy lives in the tooltip", () => {
    renderTopbar({
      isNewThread: false,
      selectedThread: { ...thread, transport: "live" },
      transportLabel: "在线运行时",
    });

    const marker = container.querySelector(
      '[data-testid="topbar-transport-status"]',
    );
    expect(marker).toBeInstanceOf(HTMLElement);
    expect(marker?.getAttribute("data-transport-kind")).toBe("live");
    expect(marker?.getAttribute("aria-label")).toContain("在线运行时");
    expect(marker?.getAttribute("title")).toContain("在线运行时");
    expect(marker?.querySelector("svg")).toBeInstanceOf(SVGElement);
    // 状态名只在 tooltip / 无障碍标签里，正文不再占文字宽度。
    expect(container.textContent).not.toContain("在线运行时");
  });

  it("distinguishes the degraded and seeded transport states", () => {
    renderTopbar({
      isNewThread: false,
      selectedThread: { ...thread, transport: "error" },
      transportLabel: "运行时降级",
    });
    expect(
      container
        .querySelector('[data-testid="topbar-transport-status"]')
        ?.getAttribute("data-transport-kind"),
    ).toBe("error");

    renderTopbar({
      isNewThread: false,
      selectedThread: { ...thread, transport: "mock" },
      transportLabel: "预置预览",
    });
    expect(
      container
        .querySelector('[data-testid="topbar-transport-status"]')
        ?.getAttribute("data-transport-kind"),
    ).toBe("seeded");
  });

  it("refreshes the current session from the topbar icon and disables while pending", () => {
    const onRefreshSession = vi.fn();
    renderTopbar({ isNewThread: false, onRefreshSession });

    const refreshButton = container.querySelector(
      '[data-testid="topbar-refresh-session"]',
    );
    expect(refreshButton).toBeInstanceOf(HTMLButtonElement);
    expect(refreshButton?.getAttribute("aria-label")).toBe("刷新当前会话");

    act(() => {
      refreshButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onRefreshSession).toHaveBeenCalledTimes(1);

    renderTopbar({ isNewThread: false, onRefreshSession, sessionRefreshing: true });
    const pendingButton = container.querySelector(
      '[data-testid="topbar-refresh-session"]',
    );
    expect(pendingButton?.hasAttribute("disabled")).toBe(true);
    expect(pendingButton?.getAttribute("aria-label")).toBe("正在刷新当前会话…");
  });

  it("hides the refresh entry without a handler and on new threads", () => {
    renderTopbar({ isNewThread: false });
    expect(
      container.querySelector('[data-testid="topbar-refresh-session"]'),
    ).toBeNull();

    renderTopbar({ isNewThread: true, onRefreshSession: vi.fn() });
    expect(
      container.querySelector('[data-testid="topbar-refresh-session"]'),
    ).toBeNull();
  });
});
