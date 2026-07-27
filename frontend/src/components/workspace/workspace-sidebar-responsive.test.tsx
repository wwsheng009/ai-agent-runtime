// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Thread } from "@/data/mock";

import { WorkspaceSidebar } from "./workspace-sidebar";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const thread: Thread = {
  id: "thread-1",
  title: "Implementation review",
  summary: "",
  updatedAt: "2026-07-27T00:00:00Z",
  status: "active",
  tags: [],
  prompts: [],
  messages: [],
  artifacts: [],
};

describe("WorkspaceSidebar responsive navigation", () => {
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

  function renderSidebar(
    overrides: Partial<React.ComponentProps<typeof WorkspaceSidebar>> = {},
  ) {
    const props: React.ComponentProps<typeof WorkspaceSidebar> = {
      density: "comfortable",
      mobileOpen: true,
      onCloseMobile: vi.fn(),
      onOpenSettings: vi.fn(),
      onRefreshRuntimeTeams: vi.fn(),
      onSelectRuntimeSessionUser: vi.fn(),
      onSelectThread: vi.fn(),
      runtimeSessionDefaultUserId: "anonymous",
      runtimeSessionUsers: [],
      runtimeSessionUsersError: null,
      runtimeSessionUsersLoading: false,
      runtimeSessions: [],
      runtimeSessionsError: null,
      runtimeSessionsLoading: false,
      runtimeSessionsRefreshing: false,
      runtimeSessionsSummary: {
        activeCount: 0,
        archivedCount: 0,
        recoverableCount: 0,
        totalCount: 0,
      },
      runtimeTeamSummaries: [],
      runtimeTeams: [],
      runtimeTeamsError: null,
      runtimeTeamsLoading: false,
      runtimeTeamsRefreshing: false,
      selectedRuntimeSessionUserId: "anonymous",
      selectedThreadId: "new",
      threads: [],
      ...overrides,
    };

    act(() => {
      root?.render(
        <MemoryRouter>
          <WorkspaceSidebar {...props} />
        </MemoryRouter>,
      );
    });
    return props;
  }

  it("exposes an accessible mobile dialog and closes it with Escape", () => {
    const props = renderSidebar();
    const sidebar = container.querySelector("aside");

    expect(sidebar?.getAttribute("role")).toBe("dialog");
    expect(sidebar?.getAttribute("aria-modal")).toBe("true");
    expect(sidebar?.getAttribute("aria-label")).toBe("聊天与会话导航");
    expect(container.querySelector('button[aria-label="关闭聊天导航"]')).toBeInstanceOf(
      HTMLButtonElement,
    );

    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    });
    expect(props.onCloseMobile).toHaveBeenCalledTimes(1);
  });

  it("keeps a zero-data workspace quiet", () => {
    renderSidebar();

    expect(container.querySelector('input[aria-label="搜索线程"]')).toBeNull();
    expect(container.textContent).not.toContain("本地聊天");
    expect(container.textContent).not.toContain("anonymous");
    expect(container.textContent).not.toContain("0 个会话");
    expect(container.textContent).not.toContain("暂无可用 runtime 团队");
    expect(container.textContent).toContain("运行时概览");
  });

  it("shows search and chat history only when history exists", () => {
    renderSidebar({ threads: [thread], selectedThreadId: thread.id });

    expect(container.querySelector('input[aria-label="搜索线程"]')).toBeInstanceOf(
      HTMLInputElement,
    );
    expect(container.textContent).toContain("本地聊天");
    expect(container.textContent).toContain(thread.title);
  });
});
