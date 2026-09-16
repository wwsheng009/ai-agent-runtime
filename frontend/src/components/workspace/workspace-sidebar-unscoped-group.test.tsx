// 「未绑定目录」组头的右缘口径（2026-09-16 样式优化）：
// 注册目录与派生目录的组头右侧都有动作槽（⋯ 菜单 / 注册并新建），未绑定目录两者皆无，
// 槽位整段缺位会让它的会话计数与折叠箭头比其它组头右移一段，行与行对不齐。
// 因此组头动作槽改为**恒定位**：宽度锁到一枚动作图标的宽度（12px 图标 + `p-1` = `min-w-5`）。
// 同时未绑定目录不属于「我的目录」清单，恒排在被注册目录之后（含 0 会话的补位组）。
// 事实源提示：`mergeDirectoryGroups` 给无工作路径会话的组键是内部哨兵值
// `__runtime-session-directory-unknown__`（未导出），测试按它定位未绑定组，不依赖文案语言。

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
const LOOSE_PATH = "E:\\work\\loose";
const UNKNOWN_DIRECTORY_KEY = "__runtime-session-directory-unknown__";

const alphaSession: RuntimeSessionRecord = {
  id: "session-alpha",
  metadata: {
    title: "Alpha 会话",
    context: { workspace_path: ALPHA_PATH },
  },
  createdAt: "2026-09-01T00:00:00Z",
  updatedAt: "2026-09-02T00:00:00Z",
};

/** 无工作路径 → 落入「未绑定目录」组。 */
const unscopedSession: RuntimeSessionRecord = {
  id: "session-unscoped",
  metadata: { title: "未绑定会话" },
  createdAt: "2026-09-01T00:00:00Z",
  updatedAt: "2026-09-03T00:00:00Z",
};

const workspaceDirectories = [
  { id: "dir-alpha", path: ALPHA_PATH, name: "alpha", exists: true },
  // 已注册但 0 会话：作为补位组常驻，用于验证未绑定目录仍排在它之后。
  { id: "dir-loose", path: LOOSE_PATH, name: "loose", exists: true },
];

describe("WorkspaceSidebar 未绑定目录组头", () => {
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

  function renderSidebar() {
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
      runtimeSessionUsers: [],
      runtimeSessions: [alphaSession, unscopedSession],
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

  function groupElements(): HTMLElement[] {
    return Array.from(
      container.querySelectorAll<HTMLElement>(
        '[data-testid="sidebar-session-group"]',
      ),
    );
  }

  it("未绑定目录排在最后一项（含 0 会话的注册目录补位组之后）", () => {
    renderSidebar();

    const keys = groupElements().map(
      (element) => element.getAttribute("data-group-key") ?? "",
    );
    expect(keys).toEqual([
      "dir-alpha",
      "dir-loose",
      UNKNOWN_DIRECTORY_KEY,
    ]);

    const last = groupElements().at(-1)!;
    const header = last.querySelector<HTMLElement>(
      '[data-testid="sidebar-session-group-drop"]',
    );
    expect(header?.getAttribute("title")).toMatch(/未绑定目录|Unscoped sessions/);
  });

  it("未绑定目录组头最右侧保留占位动作槽，宽度与其它组头一致", () => {
    renderSidebar();

    const groups = groupElements();
    const unscoped = groups.find(
      (element) =>
        element.getAttribute("data-group-key") === UNKNOWN_DIRECTORY_KEY,
    );
    expect(unscoped).not.toBeUndefined();

    const slots = groups.map((element) =>
      element.querySelector<HTMLElement>(
        '[data-testid="sidebar-directory-actions-slot"]',
      ),
    );
    // 每个组头都有动作槽（未绑定目录不再缺槽）。
    expect(slots.every((slot) => slot !== null)).toBe(true);
    for (const slot of slots) {
      expect(slot!.className).toContain("min-w-5");
    }

    const unscopedSlot = unscoped!.querySelector<HTMLElement>(
      '[data-testid="sidebar-directory-actions-slot"]',
    )!;
    // 占位槽位于组头行最右侧，且不承载任何动作（只占位，不伪造交互）。
    expect(unscopedSlot.parentElement?.lastElementChild).toBe(unscopedSlot);
    expect(unscopedSlot.children).toHaveLength(0);
  });
});
