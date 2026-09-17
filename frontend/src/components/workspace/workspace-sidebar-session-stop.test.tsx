// Batch 3（多会话并发运行时 §4.8）：侧栏会话行的「停止运行」入口。
//
// 口径：菜单项只在该会话的活动投影判定为「运行中 / 等待类」时出现（`shouldOfferSessionStop`），
// 点击后只把 sessionId 交给接线层——后台会话没有本地 controller，停止靠服务端 `interrupt`。
// 空闲会话不渲染该项（避免给「没在跑」的会话一个点了没反应的入口）。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SettingsProvider } from "@/core/settings";
import { APP_SETTINGS_STORAGE_KEY } from "@/core/settings/local";
import { type RuntimeSessionRecord } from "@/types/runtime";

import { WorkspaceSidebar } from "./workspace-sidebar";
import { type SidebarSessionActivity } from "./workspace-sidebar/session-row-status";

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
  updatedAt: "2026-09-03T00:00:00Z",
};

const betaSession: RuntimeSessionRecord = {
  id: "session-beta",
  metadata: {
    title: "Beta 会话",
    context: { workspace_path: ALPHA_PATH },
  },
  createdAt: "2026-09-01T00:00:00Z",
  updatedAt: "2026-09-02T00:00:00Z",
};

const workspaceDirectories = [
  { id: "dir-alpha", path: ALPHA_PATH, name: "alpha", exists: true },
];

describe("WorkspaceSidebar 会话行「停止运行」入口", () => {
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

  function renderSidebar(options: {
    activity?: Record<string, SidebarSessionActivity>;
    onStop?: (sessionId: string) => void;
  }) {
    window.localStorage.setItem(
      APP_SETTINGS_STORAGE_KEY,
      JSON.stringify({
        localization: { locale: "zh-CN" },
        workspace: { sessionGrouping: "directory" },
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
      onStopRuntimeSession: options.onStop,
      runtimeSessionUsers: [],
      runtimeSessions: [alphaSession, betaSession],
      runtimeSessionsError: null,
      runtimeSessionsLoading: false,
      runtimeSessionsRefreshing: false,
      runtimeSessionsSummary: {
        activeCount: 2,
        archivedCount: 0,
        recoverableCount: 2,
        totalCount: 2,
      },
      runtimeTeamSummaries: [],
      runtimeTeams: [],
      runtimeTeamsError: null,
      runtimeTeamsLoading: false,
      runtimeTeamsRefreshing: false,
      selectedRuntimeSessionUserId: "anonymous",
      selectedThreadId: "new",
      sessionActivity: options.activity,
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

  function rowByTitle(title: string): HTMLElement {
    const rows = Array.from(
      container.querySelectorAll<HTMLElement>('[role="treeitem"]'),
    );
    const row = rows.find((element) => element.textContent?.includes(title));
    if (!row) {
      throw new Error(`未找到会话行：${title}`);
    }
    return row;
  }

  function openRowMenu(row: HTMLElement) {
    const trigger = row.querySelector<HTMLButtonElement>(
      'button[aria-haspopup="menu"]',
    );
    if (!trigger) {
      throw new Error("未找到会话行操作菜单入口");
    }
    act(() => {
      trigger.click();
    });
  }

  function stopItem(sessionId: string): HTMLElement | null {
    return container.querySelector<HTMLElement>(
      `[data-testid="session-stop-${sessionId}"]`,
    );
  }

  it("运行中的会话：菜单出现「停止运行」并把 sessionId 交给接线层", () => {
    const onStop = vi.fn();
    renderSidebar({ activity: { "session-alpha": { running: true } }, onStop });

    openRowMenu(rowByTitle("Alpha 会话"));
    const item = stopItem("session-alpha");
    expect(item).not.toBeNull();

    act(() => {
      item?.click();
    });
    expect(onStop).toHaveBeenCalledTimes(1);
    expect(onStop).toHaveBeenCalledWith("session-alpha");
  });

  it("活动信号按会话隔离：只有运行中会话的行出现「停止运行」", () => {
    // 注意：菜单项同时依赖 `onStop` 接线（未接线时连入口都不渲染），
    // 所以这里必须把停止回调挂上，才能验证「信号隔离」本身。
    renderSidebar({
      activity: { "session-beta": { running: true } },
      onStop: vi.fn(),
    });

    openRowMenu(rowByTitle("Beta 会话"));
    expect(stopItem("session-beta")).not.toBeNull();

    openRowMenu(rowByTitle("Alpha 会话"));
    expect(stopItem("session-alpha")).toBeNull();
  });

  it("等待用户裁决的会话（没有 running 信号）同样可以就地停止", () => {
    const onStop = vi.fn();
    renderSidebar({
      activity: { "session-alpha": { pendingApprovals: 1 } },
      onStop,
    });

    expect(
      container.querySelector('[data-testid="session-attention-bar"]'),
    ).not.toBeNull();

    openRowMenu(rowByTitle("Alpha 会话"));
    const item = stopItem("session-alpha");
    expect(item).not.toBeNull();

    act(() => {
      item?.click();
    });
    expect(onStop).toHaveBeenCalledWith("session-alpha");
  });

  it("没有活动投影时不渲染停止项（回滚开关关闭时的既有外观）", () => {
    renderSidebar({});

    expect(
      container.querySelector('[data-testid="session-attention-bar"]'),
    ).toBeNull();

    openRowMenu(rowByTitle("Alpha 会话"));
    expect(stopItem("session-alpha")).toBeNull();
  });
});
