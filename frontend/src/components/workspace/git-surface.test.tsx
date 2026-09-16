// @vitest-environment jsdom

// 右侧栏 Git 变更面：小窗口 diff 工具栏右侧扩展图标 → 放大面板。
//
// 断言口径：放大面板复用同一份 `GitDiffView` props（同一份服务端 diff，放大不重新请求、不改对比目标）；
// 放大后的正文不得再挂一个扩展入口（否则会套娃出「放大的放大」）。
// 关闭/焦点路径由 `expanded-preview.test.tsx` 在外壳层钉住。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { i18n } from "@/i18n";
import { zhWorkspacePanelsGit } from "@/i18n/resources/zh-CN/workspace/panels-git";
import { zhWorkspacePanelsPreview } from "@/i18n/resources/zh-CN/workspace/panels-preview";
import type { UseGitChangesResult } from "@/hooks/workspace/use-git-changes";
import type {
  GitCommitsResult,
  GitStatusResult,
} from "@/types/runtime/git-browse";

import { GitSurface } from "./git-surface";
import { diffResult, snapshot as diffSnapshot } from "./git/diff-view.test-fixtures";

const { useGitChangesMock } = vi.hoisted(() => ({ useGitChangesMock: vi.fn() }));

// 只替换 hook：变更列表 / diff 视图的既有实现保持真实（这里验证的是面的编排，不是它们的渲染）。
vi.mock("@/hooks/workspace/use-git-changes", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/workspace/use-git-changes")>();
  return { ...actual, useGitChanges: useGitChangesMock };
});

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const SELECTED = "src/app.ts";

function statusResult(): GitStatusResult {
  return {
    repo: { root: "E:/ws", branch: "main", detached: false, head: "abc1234", ahead: 0, behind: 0, isBare: false },
    clean: false,
    staged: [],
    unstaged: [{ path: SELECTED, status: ".M", insertions: 1, deletions: 1, binary: false }],
    untracked: [],
    conflicts: [],
    warnings: [],
    generatedAt: 0,
  };
}

function commitsResult(): GitCommitsResult {
  return { commits: [], nextCursor: null, hasMore: false };
}

function gitResult(overrides: Partial<UseGitChangesResult> = {}): UseGitChangesResult {
  return {
    scope: "session:s1",
    status: { status: "ready", data: statusResult(), error: null, unavailable: false },
    entries: [],
    reloadStatus: vi.fn(),
    selectedPath: SELECTED,
    selectFile: vi.fn(),
    selectedStillChanged: true,
    diff: diffSnapshot({ data: diffResult({ file: { path: SELECTED, status: "M", isBinary: false, isSubmodule: false } }) }),
    target: "working",
    setTarget: vi.fn(),
    whitespace: "show",
    setWhitespace: vi.fn(),
    context: 3,
    expandContext: vi.fn(),
    canExpandContext: true,
    rowLimit: 2000,
    showMoreRows: vi.fn(),
    retryDiff: vi.fn(),
    commits: { status: "ready", data: commitsResult(), error: null, unavailable: false },
    commitsLoadingMore: false,
    loadMoreCommits: vi.fn(),
    reloadCommits: vi.fn(),
    stagePending: false,
    stagingPaths: [],
    stageError: null,
    clearStageError: vi.fn(),
    stageFiles: vi.fn(),
    ...overrides,
  };
}

describe("GitSurface 放大 diff 视图", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    i18n.addResourceBundle("zh-CN", "workspace", { panels: { git: zhWorkspacePanelsGit } }, true, false);
    i18n.addResourceBundle("zh-CN", "workspace", { panels: { preview: zhWorkspacePanelsPreview } }, true, false);
    if (i18n.language !== "zh-CN") {
      void i18n.changeLanguage("zh-CN");
    }

    useGitChangesMock.mockReset();
    useGitChangesMock.mockReturnValue(gitResult());

    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderSurface() {
    act(() => {
      root.render(<GitSurface sessionId="s1" workspacePath="E:/ws" />);
    });
  }

  function dialog() {
    return document.body.querySelector<HTMLElement>('[data-testid="git-diff-expanded"]');
  }

  function click(node: Element | null) {
    act(() => {
      node?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
  }

  it("小窗口保留原样；放大面板复用同一份 diff，且不再挂扩展入口（不套娃）", () => {
    renderSurface();

    expect(container.querySelector('[data-testid="git-diff-rows"]')).not.toBeNull();
    expect(dialog()).toBeNull();

    click(container.querySelector('[data-testid="git-diff-expand"]'));

    const opened = dialog();
    expect(opened).not.toBeNull();
    expect(opened?.textContent).toContain(SELECTED);
    expect(opened?.querySelector('[data-testid="git-diff-rows"]')).not.toBeNull();
    expect(opened?.querySelector('[data-testid="git-diff-expand"]')).toBeNull();
    // 小窗口仍在原位（放大是再挂一份，不是搬走原视口）。
    expect(container.querySelector('[data-testid="git-diff-rows"]')).not.toBeNull();
  });

  it("关闭按钮关掉放大面板", () => {
    renderSurface();
    click(container.querySelector('[data-testid="git-diff-expand"]'));

    click(document.body.querySelector('[data-testid="git-diff-expanded-close"]'));

    expect(dialog()).toBeNull();
    expect(container.querySelector('[data-testid="git-diff-rows"]')).not.toBeNull();
  });
});
