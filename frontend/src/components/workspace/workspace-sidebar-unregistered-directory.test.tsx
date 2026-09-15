// @vitest-environment jsdom
//
// 方案 §12-A（管理目录弹层）**接线级**验收：真实渲染 WorkspaceSidebar，
// 验证「仅由会话派生」的目录在管理弹层里可见，并可一键注册（走 onAddWorkspaceDirectory）。
//
// 事实源提示（改动本文件前请先确认）：
// - 派生组的 fullPath 是规范化路径（`\` → `/`），注册表 POST 用的就是这一份；
// - 同路径的注册目录与派生组会被合并去重（mergeDirectoryGroups），
//   所以用例里的派生路径必须与注册表路径不同，否则它不会出现在「未注册」分区；
// - 弹层走 createPortal 挂到 document.body，查询要用 document 而不是 container。

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

/** 已注册目录（注册表里那一份路径，保留反斜杠）。 */
const ALPHA_PATH = "E:\\work\\alpha";
/** 只有会话、没有注册表条目的目录。 */
const GAMMA_PATH = "E:\\work\\gamma";
/** 派生组的规范化路径：注册请求必须用这一份。 */
const GAMMA_KEY = "E:/work/gamma";

const alphaSession: RuntimeSessionRecord = {
  id: "session-alpha",
  metadata: {
    title: "Alpha 会话",
    context: { workspace_path: ALPHA_PATH },
  },
  createdAt: "2026-09-01T00:00:00Z",
  updatedAt: "2026-09-02T00:00:00Z",
};

const gammaSession: RuntimeSessionRecord = {
  id: "session-gamma",
  metadata: {
    title: "Gamma 会话",
    context: { workspace_path: GAMMA_PATH },
  },
  createdAt: "2026-09-01T00:00:00Z",
  updatedAt: "2026-09-01T00:00:00Z",
};

const workspaceDirectories = [
  { id: "dir-alpha", path: ALPHA_PATH, name: "alpha", exists: true },
];

function flush() {
  return Promise.resolve().then(() => Promise.resolve());
}

describe("WorkspaceSidebar 管理目录（§12-A 未注册目录）", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    window.localStorage.clear();
    window.localStorage.setItem(
      APP_SETTINGS_STORAGE_KEY,
      JSON.stringify({
        localization: { locale: "zh-CN" },
        workspace: { sessionOrder: "manual", sessionGrouping: "directory" },
      }),
    );
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
      root = null;
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderSidebar(
    onAddWorkspaceDirectory: React.ComponentProps<
      typeof WorkspaceSidebar
    >["onAddWorkspaceDirectory"],
    onCreateSessionInDirectory: React.ComponentProps<
      typeof WorkspaceSidebar
    >["onCreateSessionInDirectory"] = vi.fn(),
  ) {
    const props: React.ComponentProps<typeof WorkspaceSidebar> = {
      density: "comfortable",
      onAddWorkspaceDirectory,
      onArchiveRuntimeSession: vi.fn(),
      onCreateSessionInDirectory,
      onDeleteRuntimeSession: vi.fn(),
      onForkRuntimeSession: vi.fn(),
      onMoveRuntimeSession: vi.fn(),
      onRefreshRuntimeTeams: vi.fn(),
      onRemoveWorkspaceDirectory: vi.fn(),
      onRenameRuntimeSession: vi.fn(),
      onRenameWorkspaceDirectory: vi.fn(),
      onSelectThread: vi.fn(),
      runtimeSessionUsers: [
        { user_id: "anonymous", display_name: "匿名", session_count: 2 },
      ],
      runtimeSessions: [alphaSession, gammaSession],
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

  /**
   * 「管理目录」弹层入口（2026-09-15 布局优化后落位在段头 ⋯ 面板内）：
   * 先展开面板，再点面板里的条目。
   */
  async function openManagerDialog() {
    const trigger = document.querySelector<HTMLButtonElement>(
      '[data-testid="sidebar-directories-menu-trigger"]',
    );
    if (!trigger) {
      throw new Error("未找到段头 ⋯ 菜单入口");
    }
    await act(async () => {
      trigger.click();
      await flush();
    });
    const button = document.querySelector<HTMLButtonElement>(
      '[data-testid="sidebar-directories-manage-entry"]',
    );
    if (!button) {
      throw new Error("未找到「管理目录」入口");
    }
    await act(async () => {
      button.click();
      await flush();
    });
  }

  function unregisteredRows(): HTMLElement[] {
    return Array.from(
      document.querySelectorAll<HTMLElement>(
        '[data-testid="directory-manage-unregistered-row"]',
      ),
    );
  }

  function registerButton(row: HTMLElement): HTMLButtonElement {
    const button = row.querySelector<HTMLButtonElement>(
      '[data-testid="directory-manage-register"]',
    );
    if (!button) {
      throw new Error("未找到「注册」按钮");
    }
    return button;
  }

  function registerAndCreateButton(row: HTMLElement): HTMLButtonElement {
    const button = row.querySelector<HTMLButtonElement>(
      '[data-testid="directory-manage-register-and-create"]',
    );
    if (!button) {
      throw new Error("未找到「注册并新建会话」按钮");
    }
    return button;
  }

  /** 派生组头 hover 动作槽里的「注册并新建会话」（注册目录没有这个入口）。 */
  function derivedGroupAction(): HTMLButtonElement {
    const button = document.querySelector<HTMLButtonElement>(
      '[data-testid="sidebar-directory-register-and-create"]',
    );
    if (!button) {
      throw new Error("未找到派生组头的「注册并新建会话」入口");
    }
    return button;
  }

  it("会话派生的目录出现在「未注册」分区，注册目录照旧列出", async () => {
    renderSidebar(vi.fn());
    await openManagerDialog();

    expect(
      document.querySelector('[data-testid="directory-manage-empty"]'),
    ).toBeNull();
    expect(
      document.querySelectorAll('[data-testid="directory-manage-row"]'),
    ).toHaveLength(1);
    expect(unregisteredRows()).toHaveLength(1);
    expect(unregisteredRows()[0]?.dataset.directoryPath).toBe(GAMMA_KEY);
    expect(
      unregisteredRows()[0]?.querySelector(
        '[data-testid="directory-manage-unregistered-count"]',
      )?.textContent,
    ).toBe("1");
  });

  it("点击「注册」用规范化路径调用注册表接口（onAddWorkspaceDirectory）", async () => {
    const onAddWorkspaceDirectory = vi.fn().mockResolvedValue(undefined);
    renderSidebar(onAddWorkspaceDirectory);
    await openManagerDialog();

    await act(async () => {
      registerButton(unregisteredRows()[0]!).click();
      await flush();
    });

    expect(onAddWorkspaceDirectory).toHaveBeenCalledTimes(1);
    expect(onAddWorkspaceDirectory).toHaveBeenCalledWith(GAMMA_KEY);
  });

  it("注册失败：弹层就地提示且不收起（行仍在）", async () => {
    const onAddWorkspaceDirectory = vi
      .fn()
      .mockRejectedValue(new Error("目录不存在"));
    renderSidebar(onAddWorkspaceDirectory);
    await openManagerDialog();

    await act(async () => {
      registerButton(unregisteredRows()[0]!).click();
      await flush();
    });

    expect(
      document.querySelector('[data-testid="directory-manage-register-error"]')
        ?.textContent,
    ).toBe("目录不存在");
    expect(unregisteredRows()).toHaveLength(1);
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();
  });

  // 方案 §15：「用即注册」——一次点击 = 登记 + 在该目录下建会话，
  // 且新会话直接用注册响应里的 id 绑定（不再靠路径二次推导）。
  it("弹层「注册并新建会话」：登记后用注册记录的 id 在该目录建会话", async () => {
    const onAddWorkspaceDirectory = vi
      .fn()
      .mockResolvedValue({ id: "dir-gamma", path: GAMMA_KEY });
    const onCreateSessionInDirectory = vi.fn().mockResolvedValue(undefined);
    renderSidebar(onAddWorkspaceDirectory, onCreateSessionInDirectory);
    await openManagerDialog();

    await act(async () => {
      registerAndCreateButton(unregisteredRows()[0]!).click();
      await flush();
    });

    expect(onAddWorkspaceDirectory).toHaveBeenCalledWith(GAMMA_KEY);
    expect(onCreateSessionInDirectory).toHaveBeenCalledWith({
      path: GAMMA_KEY,
      directoryId: "dir-gamma",
      label: "gamma",
    });
    // 成功后收起弹层：新会话会被选中并跳到会话路由。
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("派生组头「注册并新建会话」：一次点击完成登记 + 建会话（注册组没有该入口）", async () => {
    const onAddWorkspaceDirectory = vi
      .fn()
      .mockResolvedValue({ id: "dir-gamma", path: GAMMA_KEY });
    const onCreateSessionInDirectory = vi.fn().mockResolvedValue(undefined);
    renderSidebar(onAddWorkspaceDirectory, onCreateSessionInDirectory);

    // 只有派生组（gamma）有这个入口；已注册的 alpha 仍是 ⋯ 菜单三件套。
    expect(
      document.querySelectorAll(
        '[data-testid="sidebar-directory-register-and-create"]',
      ),
    ).toHaveLength(1);

    await act(async () => {
      derivedGroupAction().click();
      await flush();
      await flush();
    });

    expect(onAddWorkspaceDirectory).toHaveBeenCalledWith(GAMMA_KEY);
    expect(onCreateSessionInDirectory).toHaveBeenCalledWith({
      path: GAMMA_KEY,
      directoryId: "dir-gamma",
      label: "gamma",
    });
  });
});
