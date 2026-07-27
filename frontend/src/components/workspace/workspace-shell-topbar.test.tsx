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
      artifactRailOpen: false,
      density: "comfortable",
      isNewThread: true,
      liveTeamCount: 0,
      onOpenSettings: vi.fn(),
      onOpenSidebar: vi.fn(),
      onToggleArtifactRail: vi.fn(),
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

  it("keeps existing-thread new-chat and artifact actions accessible", () => {
    const props = renderTopbar({ isNewThread: false });

    expect(container.textContent).toContain(thread.title);
    expect(container.querySelector('[aria-label="新建聊天"]')).toBeInstanceOf(
      HTMLAnchorElement,
    );
    expect(container.querySelector('[aria-label="显示文件"]')).toBeInstanceOf(
      HTMLButtonElement,
    );

    const artifactButton = container.querySelector(
      'button[aria-label="显示文件"]',
    );
    act(() => {
      artifactButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(props.onToggleArtifactRail).toHaveBeenCalledTimes(1);
  });
});
