// @vitest-environment jsdom

// 左侧栏收起/展开（桌面 xl+ 的「图标列」形态）：
// - 头部提供向左收起的图标；收起后整列换成只显示图标的图标列；
// - 图标列的展开按钮回传 false；
// - 图标列里的分区图标在展开侧栏的同时把该段切到展开态。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SettingsProvider } from "@/core/settings";
import { APP_SETTINGS_STORAGE_KEY } from "@/core/settings/local";

import { WorkspaceSidebar } from "./workspace-sidebar";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

describe("WorkspaceSidebar collapse rail", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    window.localStorage.clear();
    // 与既有侧栏用例同口径：显式选中文，断言 zh-CN 文案。
    window.localStorage.setItem(
      APP_SETTINGS_STORAGE_KEY,
      JSON.stringify({ localization: { locale: "zh-CN" } }),
    );
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
      onCollapsedChange: vi.fn(),
      onOpenSettings: vi.fn(),
      onRefreshRuntimeTeams: vi.fn(),
      onSelectThread: vi.fn(),
      runtimeSessionUsers: [],
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
      workspaceDirectories: [],
      workspaceDirectoriesError: null,
      workspaceDirectoriesLoading: false,
      workspaceDirectoriesRefreshing: false,
      onAddWorkspaceDirectory: vi.fn(),
      onRenameWorkspaceDirectory: vi.fn(),
      onRemoveWorkspaceDirectory: vi.fn(),
      onCreateSessionInDirectory: vi.fn(),
      onRenameRuntimeSession: vi.fn(),
      ...overrides,
    };

    act(() => {
      root?.render(
        <SettingsProvider>
          <MemoryRouter>
            <WorkspaceSidebar {...props} />
          </MemoryRouter>
        </SettingsProvider>,
      );
    });
    return props;
  }

  it("头部渲染向左收起的图标，点击回传 true", () => {
    const props = renderSidebar();
    const collapseButton = container.querySelector(
      'button[aria-label="收起侧栏"]',
    );

    expect(collapseButton).toBeInstanceOf(HTMLButtonElement);
    // 只在桌面断点可用：移动抽屉保持整栏形态。
    expect(collapseButton?.className).toContain("xl:inline-flex");
    expect(container.querySelector('[data-testid="sidebar-collapsed-rail"]')).toBeNull();

    act(() => {
      (collapseButton as HTMLButtonElement).click();
    });
    expect(props.onCollapsedChange).toHaveBeenCalledWith(true);
  });

  it("收起态渲染图标列，展开按钮回传 false", () => {
    const props = renderSidebar({ collapsed: true });
    const rail = container.querySelector('[data-testid="sidebar-collapsed-rail"]');

    expect(rail).not.toBeNull();
    // 图标列只在 xl+ 出现（窄屏仍走整栏抽屉）。
    expect(rail?.className).toContain("xl:flex");
    expect(rail?.querySelector("button")).not.toBeNull();

    const expandButton = container.querySelector('button[aria-label="展开侧栏"]');
    expect(expandButton).toBeInstanceOf(HTMLButtonElement);

    act(() => {
      (expandButton as HTMLButtonElement).click();
    });
    expect(props.onCollapsedChange).toHaveBeenCalledWith(false);
  });

  it("收起态隐藏完整侧栏内容，分区图标展开侧栏并打开对应分区", () => {
    const props = renderSidebar({ collapsed: true });
    const fullContent = container.querySelector("aside > div");

    expect(fullContent?.className).toContain("xl:hidden");
    // 默认折叠的运行时段正文不存在，点击图标后应随展开一起出现。
    expect(container.querySelector('a[href="/runtime/config"]')).toBeNull();

    const runtimeButton = container.querySelector(
      'button[aria-label="运行时概览"]',
    );
    expect(runtimeButton).toBeInstanceOf(HTMLButtonElement);

    act(() => {
      (runtimeButton as HTMLButtonElement).click();
    });

    expect(props.onCollapsedChange).toHaveBeenCalledWith(false);
    expect(container.querySelector('a[href="/runtime/config"]')).not.toBeNull();
  });
});
