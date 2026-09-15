// @vitest-environment jsdom
//
// P2-6 子片 2：跨组移动的**接线级**验收（真实渲染 WorkspaceSidebar，不 mock 内部模块）。
// 覆盖两条口径：
// 1) 成功：拖到目标目录组头 → Host 写回用注册表里的原始路径，且分组视图立即乐观归属；
// 2) 失败：Host 拒绝 → 撤销乐观覆盖（回到原组）并就地提示，不做假落点。
//
// 事实源提示（实证结论，改动本文件前请先确认）：
// - 组头 title 是**规范化路径**（`\` → `/`），与写回用的注册表原始路径不同；
// - 目录组默认折叠，未展开的组不渲染会话行，需要先点组头展开才能观察归属。

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

/** 注册表里的原始路径（写回 Host 时必须是这一份）。 */
const ALPHA_PATH = "E:\\work\\alpha";
const BETA_PATH = "E:\\work\\beta";
/** 展示用的规范化路径（组头 title / 分组键口径）。 */
const ALPHA_KEY = "E:/work/alpha";
const BETA_KEY = "E:/work/beta";

const alphaSession: RuntimeSessionRecord = {
  id: "session-alpha",
  metadata: {
    title: "Alpha 会话",
    context: { workspace_path: ALPHA_PATH },
  },
  createdAt: "2026-09-01T00:00:00Z",
  updatedAt: "2026-09-02T00:00:00Z",
};

const betaSession: RuntimeSessionRecord = {
  id: "session-beta",
  metadata: {
    title: "Beta 会话",
    context: { workspace_path: BETA_PATH },
  },
  createdAt: "2026-09-01T00:00:00Z",
  updatedAt: "2026-09-01T00:00:00Z",
};

const workspaceDirectories = [
  { id: "dir-alpha", path: ALPHA_PATH, name: "alpha", exists: true },
  { id: "dir-beta", path: BETA_PATH, name: "beta", exists: true },
];

describe("WorkspaceSidebar 跨组移动（P2-6 子片 2）", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    window.localStorage.clear();
    // 手动排序模式才可拖拽；分组视图必须是「按目录」才有组头落点。
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
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderSidebar(
    onMoveRuntimeSession: React.ComponentProps<
      typeof WorkspaceSidebar
    >["onMoveRuntimeSession"],
  ) {
    const props: React.ComponentProps<typeof WorkspaceSidebar> = {
      density: "comfortable",
      onAddWorkspaceDirectory: vi.fn(),
      onArchiveRuntimeSession: vi.fn(),
      onCreateSessionInDirectory: vi.fn(),
      onDeleteRuntimeSession: vi.fn(),
      onForkRuntimeSession: vi.fn(),
      onMoveRuntimeSession,
      onRefreshRuntimeTeams: vi.fn(),
      onRemoveWorkspaceDirectory: vi.fn(),
      onRenameRuntimeSession: vi.fn(),
      onRenameWorkspaceDirectory: vi.fn(),
      onSelectRuntimeSessionUser: vi.fn(),
      onSelectThread: vi.fn(),
      runtimeSessionDefaultUserId: "anonymous",
      runtimeSessionUsers: [
        { user_id: "anonymous", display_name: "匿名", session_count: 2 },
      ],
      runtimeSessionUsersError: null,
      runtimeSessionUsersLoading: false,
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
   * 组头定位口径：组头 title 是规范化路径（`\` → `/`），与写回用的注册表原始路径不同；
   * 组容器身份键（data-group-key）在注册目录组里是 directory.id，不是路径，所以这里按 title 找组头，
   * 再向上取组容器。Phase 1 抽件后组头外面多了一层 hover/动作行，不能再用「最近的 div」。
   */
  function findGroupHeader(key: string): HTMLElement | null {
    return container.querySelector<HTMLElement>(
      `[data-testid="sidebar-session-group"] [data-testid="sidebar-session-group-drop"][title="${key}"]`,
    );
  }

  function groupHeader(key: string): HTMLElement {
    const header = findGroupHeader(key);
    if (!header) {
      throw new Error(`未找到目录组头：${key}`);
    }
    return header;
  }

  function groupContainer(key: string): HTMLElement {
    const group = groupHeader(key).closest<HTMLElement>(
      '[data-testid="sidebar-session-group"]',
    );
    if (!group) {
      throw new Error(`未找到目录分组容器：${key}`);
    }
    return group;
  }

  /** 目录组默认折叠：欠展开时点一次组头，行渲染出来才能观察归属。 */
  function expandGroup(key: string) {
    const group = groupContainer(key);
    if (group.querySelector('[draggable="true"]')) {
      return;
    }
    act(() => {
      groupHeader(key).dispatchEvent(
        new MouseEvent("click", { bubbles: true, cancelable: true }),
      );
    });
  }

  function rowTitle(scope: HTMLElement, title: string): string | null {
    const row = Array.from(scope.querySelectorAll("button")).find((button) =>
      (button.getAttribute("title") ?? "").startsWith(`${title} ·`),
    );
    return row?.getAttribute("title") ?? null;
  }

  function draggableRow(scope: HTMLElement): Element {
    const row = scope.querySelector('[draggable="true"]');
    if (!row) {
      throw new Error("该分组内没有可拖拽的会话行");
    }
    return row;
  }

  function dispatchDrag(target: Element, type: string) {
    const event = new Event(type, { bubbles: true, cancelable: true });
    Object.defineProperty(event, "dataTransfer", {
      value: {
        dropEffect: "",
        effectAllowed: "",
        setData: vi.fn(),
      },
    });
    Object.defineProperty(event, "clientY", { value: 0 });
    Object.defineProperty(event, "relatedTarget", { value: null });
    act(() => {
      target.dispatchEvent(event);
    });
  }

  /** 把 alpha 组的会话行拖到 beta 组头（跨组落点）。两次微任务让步让写回收尾。 */
  async function dropAlphaRowOnBetaGroup() {
    dispatchDrag(
      draggableRow(groupContainer(ALPHA_KEY)),
      "dragstart",
    );
    dispatchDrag(groupHeader(BETA_KEY), "drop");
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  it("把会话拖到目标目录组头：写回注册表原始路径并立即乐观归属", async () => {
    const onMoveRuntimeSession = vi.fn().mockResolvedValue(undefined);
    renderSidebar(onMoveRuntimeSession);

    expandGroup(ALPHA_KEY);
    expandGroup(BETA_KEY);
    expect(rowTitle(groupContainer(ALPHA_KEY), "Alpha 会话")).not.toBeNull();
    expect(rowTitle(groupContainer(BETA_KEY), "Alpha 会话")).toBeNull();

    await dropAlphaRowOnBetaGroup();

    // 写回的是注册表原始路径（反斜杠），不是展示用的规范化路径。
    expect(onMoveRuntimeSession).toHaveBeenCalledWith(
      "session-alpha",
      BETA_PATH,
    );
    expect(rowTitle(groupContainer(BETA_KEY), "Alpha 会话")).not.toBeNull();
    // Phase 2（合并方案 §3.5-F）：注册目录即使被移空也常驻成 0 会话组，
    // 组头保留「在目录中新建会话」的入口，只在组内显示空提示。
    expect(findGroupHeader(ALPHA_KEY)).not.toBeNull();
    expect(rowTitle(groupContainer(ALPHA_KEY), "Alpha 会话")).toBeNull();
    expect(container.textContent).toContain(
      "可恢复的运行时会话会在加载后显示在这里。",
    );
    expect(container.textContent).not.toContain("移动会话失败");
  });

  it("Host 写回失败即回滚：会话回到原组并就地提示", async () => {
    const onMoveRuntimeSession = vi
      .fn()
      .mockRejectedValue(new Error("host rejected"));
    renderSidebar(onMoveRuntimeSession);

    expandGroup(ALPHA_KEY);
    expandGroup(BETA_KEY);

    await dropAlphaRowOnBetaGroup();

    expect(onMoveRuntimeSession).toHaveBeenCalledWith(
      "session-alpha",
      BETA_PATH,
    );
    expect(rowTitle(groupContainer(BETA_KEY), "Alpha 会话")).toBeNull();
    expect(rowTitle(groupContainer(ALPHA_KEY), "Alpha 会话")).not.toBeNull();
    expect(container.textContent).toContain("移动会话失败：host rejected");
  });
});
