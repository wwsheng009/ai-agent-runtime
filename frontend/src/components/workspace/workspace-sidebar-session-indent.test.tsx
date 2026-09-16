// 会话列表缩进口径（2026-09-16 样式优化）：
// 「按目录」视图下，组内会话列表缩进**一枚小文件图标的宽度**——全库小图标口径是
// `size-3.5`（0.875rem = 14px，见文件树 `tree-list.tsx` 行图标、附件条 `FileIcon size={14}`），
// 缩进步长因此取同刻度的 `pl-3.5`，字号缩放时图标与缩进不会失配。
// 平铺视图没有目录组头（会话行是段内顶层条目），保持贴齐、不出现悬空缩进。
// 事实源提示：目录组的默认开合由 `openSessionDirectories` 决定，不同夹具可能不同，
// 因此「按目录」用例先按需展开（已展开时再点一次会收起，不能无条件点）。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SettingsProvider } from "@/core/settings";
import { APP_SETTINGS_STORAGE_KEY } from "@/core/settings/local";
import { type RuntimeSessionRecord } from "@/types/runtime";

import { WorkspaceSidebar } from "./workspace-sidebar";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const ALPHA_PATH = "E:\\work\\alpha";

const alphaSession: RuntimeSessionRecord = {
  id: "session-alpha",
  metadata: {
    title: "Alpha 会话",
    context: { workspace_path: ALPHA_PATH },
  },
  createdAt: "2026-09-01T00:00:00Z",
  updatedAt: "2026-09-02T00:00:00Z",
};

const workspaceDirectories = [
  { id: "dir-alpha", path: ALPHA_PATH, name: "alpha", exists: true },
];

describe("WorkspaceSidebar 会话列表缩进", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    window.localStorage.clear();
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

  function renderSidebar(grouping: "directory" | "flat") {
    window.localStorage.setItem(
      APP_SETTINGS_STORAGE_KEY,
      JSON.stringify({
        localization: { locale: "zh-CN" },
        workspace: { sessionGrouping: grouping },
      }),
    );

    const props: React.ComponentProps<typeof WorkspaceSidebar> = {
      density: "comfortable",
      onAddWorkspaceDirectory: vi.fn(),
      onArchiveRuntimeSession: vi.fn(),
      onCreateSessionInDirectory: vi.fn(),
      onDeleteRuntimeSession: vi.fn(),
      onForkRuntimeSession: vi.fn(),
      onMoveRuntimeSession: vi.fn(),
      onRefreshRuntimeTeams: vi.fn(),
      onRemoveWorkspaceDirectory: vi.fn(),
      onRenameRuntimeSession: vi.fn(),
      onRenameWorkspaceDirectory: vi.fn(),
      onSelectThread: vi.fn(),
      runtimeSessionUsers: [],
      runtimeSessions: [alphaSession],
      runtimeSessionsError: null,
      runtimeSessionsLoading: false,
      runtimeSessionsRefreshing: false,
      runtimeSessionsSummary: {
        activeCount: 1,
        archivedCount: 0,
        recoverableCount: 1,
        totalCount: 1,
      },
      runtimeTeamSummaries: [],
      runtimeTeams: [],
      runtimeTeamsError: null,
      runtimeTeamsLoading: false,
      runtimeTeamsRefreshing: false,
      selectedRuntimeSessionUserId: "anonymous",
      selectedThreadId: "new",
      threads: [],
      workspaceDirectories,
      workspaceDirectoriesError: null,
      workspaceDirectoriesLoading: false,
      workspaceDirectoriesRefreshing: false,
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
  }

  function sessionLists(): HTMLElement[] {
    return Array.from(
      container.querySelectorAll<HTMLElement>(
        '[data-testid="sidebar-session-list"]',
      ),
    );
  }

  /** 目录组折叠时不渲染会话列表：按需点一次组头，已展开则保持原状。 */
  function ensureSessionList() {
    if (sessionLists().length > 0) {
      return;
    }
    const header = container.querySelector<HTMLElement>(
      '[data-testid="sidebar-session-group-drop"]',
    );
    if (!header) {
      throw new Error("未找到目录组头");
    }
    act(() => {
      header.dispatchEvent(
        new MouseEvent("click", { bubbles: true, cancelable: true }),
      );
    });
  }

  it("按目录视图：组内会话列表缩进一枚小文件图标的宽度（pl-3.5 = 0.875rem）", () => {
    renderSidebar("directory");
    ensureSessionList();

    const lists = sessionLists();
    expect(lists).toHaveLength(1);
    const list = lists[0]!;
    expect(list.className).toContain("pl-3.5");
    // 缩进挂在列表容器上，会话行仍落在其中（缩进表达「目录头 → 会话行」的层级）。
    expect(list.querySelector('[role="treeitem"]')).not.toBeNull();
  });

  it("平铺视图：没有目录组头，会话列表不加缩进", () => {
    renderSidebar("flat");

    const lists = sessionLists();
    expect(lists.length).toBeGreaterThan(0);
    for (const list of lists) {
      expect(list.className).not.toContain("pl-3.5");
      expect(list.querySelector('[role="treeitem"]')).not.toBeNull();
    }
  });
});
