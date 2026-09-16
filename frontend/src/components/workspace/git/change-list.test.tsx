// @vitest-environment jsdom

// 变更文件列表单测：分组顺序 / 状态徽标映射 / 行数统计诚实口径 / 过滤 / ↑↓ 切文件 / 错误态重试。
//
// i18n 口径：测试里把 zh 模块直接并进 workspace 命名空间（父批次尚未注册时也能断言真实文案），
// 这样断言的仍是 `panels-git.ts` 真源，而不是 key 回显。

import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { i18n } from "@/i18n";
import { zhWorkspacePanelsGit } from "@/i18n/resources/zh-CN/workspace/panels-git";
import type { GitSnapshot } from "@/hooks/workspace/use-git-changes";
import { buildGitChangeSections, gitStatusBadge, moveGitSelection } from "@/lib/git/change-model";
import type { GitFileStatus, GitStatusResult } from "@/types/runtime/git-browse";

import { GitChangeList } from "./change-list";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function file(path: string, status: string, overrides: Partial<GitFileStatus> = {}): GitFileStatus {
  return { path, status, insertions: 1, deletions: 0, binary: false, ...overrides };
}

function statusResult(overrides: Partial<GitStatusResult> = {}): GitStatusResult {
  return {
    repo: null,
    clean: false,
    staged: [],
    unstaged: [],
    untracked: [],
    conflicts: [],
    warnings: [],
    generatedAt: 0,
    ...overrides,
  };
}

function snapshot(overrides: Partial<GitSnapshot<GitStatusResult>> = {}): GitSnapshot<GitStatusResult> {
  return { status: "ready", data: statusResult(), error: null, unavailable: false, ...overrides };
}

function render(node: ReactNode) {
  const container = document.createElement("div");
  document.body.appendChild(container);
  const root = createRoot(container);
  act(() => root.render(node));
  return { container, root };
}

function flush() {
  return Promise.resolve().then(() => Promise.resolve());
}

describe("gitStatusBadge", () => {
  it("按分组选取 porcelain XY 的对应侧字母", () => {
    expect(gitStatusBadge("M.", "staged")).toEqual({ letter: "M", tone: "modify" });
    expect(gitStatusBadge(".M", "unstaged")).toEqual({ letter: "M", tone: "modify" });
    expect(gitStatusBadge("R.", "staged")).toEqual({ letter: "R", tone: "rename" });
    expect(gitStatusBadge(".D", "unstaged")).toEqual({ letter: "D", tone: "delete" });
    expect(gitStatusBadge("A.", "staged")).toEqual({ letter: "A", tone: "add" });
    expect(gitStatusBadge("??", "untracked")).toEqual({ letter: "untracked", tone: "untracked" });
    expect(gitStatusBadge("UU", "conflicts")).toEqual({ letter: "U", tone: "unmerged" });
  });

  it("缺侧（`.` / `?`）回退另一侧；两侧都缺时给未知而不是猜 M", () => {
    expect(gitStatusBadge(".M", "staged")).toEqual({ letter: "M", tone: "modify" });
    expect(gitStatusBadge("..", "unstaged")).toEqual({ letter: "X", tone: "unknown" });
    expect(gitStatusBadge("", "staged")).toEqual({ letter: "X", tone: "unknown" });
  });
});

describe("buildGitChangeSections", () => {
  it("分组顺序固定（冲突 → 已暂存 → 未暂存 → 未跟踪），组内保持后端顺序", () => {
    const sections = buildGitChangeSections(
      statusResult({
        conflicts: [file("c.ts", "UU")],
        staged: [file("b.ts", "M."), file("a.ts", "A.")],
        untracked: [file("new.ts", "??")],
      }),
    );
    expect(sections.map((section) => section.group)).toEqual([
      "conflicts",
      "staged",
      "unstaged",
      "untracked",
    ]);
    expect(sections[1].files.map((item) => item.path)).toEqual(["b.ts", "a.ts"]);
    expect(sections[2].files).toEqual([]);
  });

  it("没有状态数据时不给任何分组（不伪造空分组）", () => {
    expect(buildGitChangeSections(null)).toEqual([]);
  });
});

describe("moveGitSelection", () => {
  const entries = [
    { path: "a.ts", group: "staged" as const, file: file("a.ts", "M.") },
    { path: "b.ts", group: "unstaged" as const, file: file("b.ts", ".M") },
  ];

  it("向下/向上移动且在边界循环", () => {
    expect(moveGitSelection(entries, "a.ts", 1)).toBe("b.ts");
    expect(moveGitSelection(entries, "b.ts", 1)).toBe("a.ts");
    expect(moveGitSelection(entries, "a.ts", -1)).toBe("b.ts");
  });

  it("空列表保持原选择；当前选择不在列表时 ↓ 落到第一项、↑ 落到最后一项", () => {
    expect(moveGitSelection([], "a.ts", 1)).toBe("a.ts");
    expect(moveGitSelection(entries, "gone.ts", 1)).toBe("a.ts");
    expect(moveGitSelection(entries, "gone.ts", -1)).toBe("b.ts");
    expect(moveGitSelection(entries, null, 1)).toBe("a.ts");
  });
});

describe("GitChangeList", () => {
  let roots: Root[] = [];
  const originalLanguage = i18n.language;

  beforeEach(() => {
    // 合并（不覆盖）zh 真源：注册表尚未接线时也能断言真实文案。
    i18n.addResourceBundle(
      "zh-CN",
      "workspace",
      { panels: { git: zhWorkspacePanelsGit } },
      true,
      false,
    );
    if (i18n.language !== "zh-CN") {
      void i18n.changeLanguage("zh-CN");
    }
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    for (const root of roots) {
      act(() => root.unmount());
    }
    roots = [];
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
    void originalLanguage;
  });

  function mount(node: ReactNode) {
    const rendered = render(node);
    roots.push(rendered.root);
    return rendered.container;
  }

  it("按分组渲染行、分组计数与状态徽标字母", () => {
    const container = mount(
      <GitChangeList
        onRetry={vi.fn()}
        onSelect={vi.fn()}
        selectedPath="mod.ts"
        status={snapshot({
          data: statusResult({
            conflicts: [file("conflict.ts", "UU")],
            staged: [file("mod.ts", "M.")],
            unstaged: [file("work.ts", ".D")],
            untracked: [file("new.ts", "??")],
          }),
        })}
      />,
    );

    expect(container.querySelectorAll("[data-git-group]")).toHaveLength(4);
    expect(container.querySelector('[data-git-group="conflicts"]')?.textContent).toContain("冲突");
    expect(container.querySelector('[data-git-group="untracked"]')?.textContent).toContain("未跟踪");

    const rows = [...container.querySelectorAll("[data-git-path]")];
    expect(rows.map((row) => row.getAttribute("data-git-path"))).toEqual([
      "conflict.ts",
      "mod.ts",
      "work.ts",
      "new.ts",
    ]);
    expect(rows[0].textContent).toContain("U");
    expect(rows[1].textContent).toContain("M");
    expect(rows[2].textContent).toContain("D");
    expect(rows[3].textContent).toContain("?");
    expect(rows[1].getAttribute("aria-selected")).toBe("true");
  });

  it("行数统计：正常显示 +/−，二进制显示「二进制文件」，-1 显示不可用而不是 0", () => {
    const container = mount(
      <GitChangeList
        onRetry={vi.fn()}
        onSelect={vi.fn()}
        selectedPath={null}
        status={snapshot({
          data: statusResult({
            unstaged: [
              file("text.ts", ".M", { insertions: 12, deletions: 3 }),
              file("blob.bin", ".M", { insertions: -1, deletions: -1, binary: true }),
              file("mystery.ts", ".M", { insertions: -1, deletions: -1 }),
            ],
          }),
        })}
      />,
    );

    const text = container.querySelector('[data-git-path="text.ts"]')?.textContent ?? "";
    expect(text).toContain("+12");
    expect(text).toContain("3");

    const binary = container.querySelector('[data-git-path="blob.bin"]')?.textContent ?? "";
    expect(binary).toContain("二进制文件");
    expect(binary).not.toMatch(/\+\d/);

    const unknown = container.querySelector('[data-git-path="mystery.ts"]')?.textContent ?? "";
    expect(unknown).toContain("行数统计不可用（二进制）");
    expect(unknown).not.toContain("+0");
  });

  it("重命名行展示来源路径；过滤只作用于已加载数据", async () => {
    const container = mount(
      <GitChangeList
        onRetry={vi.fn()}
        onSelect={vi.fn()}
        selectedPath={null}
        status={snapshot({
          data: statusResult({
            staged: [
              file("src/new-name.ts", "R.", { from: "src/old-name.ts" }),
              file("other.ts", "M."),
            ],
          }),
        })}
      />,
    );
    expect(container.textContent).toContain("来自 src/old-name.ts");

    const input = container.querySelector("input");
    expect(input).not.toBeNull();
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
    act(() => {
      setter?.call(input, "other");
      input?.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await flush();

    const rows = [...container.querySelectorAll("[data-git-path]")];
    expect(rows.map((row) => row.getAttribute("data-git-path"))).toEqual(["other.ts"]);
  });

  it("↑/↓ 在列表内移动选择并回调选中路径", () => {
    const onSelect = vi.fn();
    const container = mount(
      <GitChangeList
        onRetry={vi.fn()}
        onSelect={onSelect}
        selectedPath="a.ts"
        status={snapshot({
          data: statusResult({
            staged: [file("a.ts", "M.")],
            unstaged: [file("b.ts", ".M")],
          }),
        })}
      />,
    );
    const listbox = container.querySelector('[role="listbox"]');
    expect(listbox).not.toBeNull();
    act(() => {
      listbox?.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
    });
    expect(onSelect).toHaveBeenCalledWith("b.ts");

    act(() => {
      listbox?.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true }));
    });
    expect(onSelect).toHaveBeenLastCalledWith("b.ts");
  });

  it("状态请求失败时给错误态与重试入口；仓库不可用文案区别于普通失败", () => {
    const onRetry = vi.fn();
    const container = mount(
      <GitChangeList
        onRetry={onRetry}
        onSelect={vi.fn()}
        selectedPath={null}
        status={snapshot({
          status: "error",
          data: null,
          error: new Error("boom"),
          unavailable: true,
        })}
      />,
    );
    const errorBox = container.querySelector('[data-testid="git-change-list-error"]');
    expect(errorBox?.textContent).toContain("仓库或 git 不可用");
    expect(errorBox?.textContent).toContain("boom");
    act(() => {
      errorBox?.querySelector("button")?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("干净仓库显示「工作区干净」而不是空列表假象", () => {
    const container = mount(
      <GitChangeList
        onRetry={vi.fn()}
        onSelect={vi.fn()}
        selectedPath={null}
        status={snapshot({ data: statusResult({ clean: true }) })}
      />,
    );
    const empty = container.querySelector('[data-testid="git-change-list-empty"]');
    expect(empty?.textContent).toContain("没有变更文件");
    expect(empty?.textContent).toContain("工作区干净");
  });

  it("缺省不渲染任何写入口（只读模式）", () => {
    const container = mount(
      <GitChangeList
        onRetry={vi.fn()}
        onSelect={vi.fn()}
        selectedPath={null}
        status={snapshot({ data: statusResult({ unstaged: [file("work.ts", ".M")] }) })}
      />,
    );
    // 组名「未暂存」本身就含「暂存」，不能用子串判断；按写入口按钮断言（缺省无 onStage → 一个按钮都不渲染）。
    expect(container.querySelectorAll("button").length).toBe(0);
    expect(container.querySelector('[data-testid="git-change-list-rows"]')).not.toBeNull();
  });

  it("行内按钮按分组发意图：未暂存→暂存、未跟踪→暂存、已暂存→取消暂存", () => {
    const onStage = vi.fn();
    const container = mount(
      <GitChangeList
        onRetry={vi.fn()}
        onSelect={vi.fn()}
        onStage={onStage}
        selectedPath={null}
        status={snapshot({
          data: statusResult({
            staged: [file("mod.ts", "M.")],
            unstaged: [file("work.ts", ".M")],
            untracked: [file("new.ts", "??")],
          }),
        })}
      />,
    );

    const clickIn = (path: string, label: string) => {
      const row = container.querySelector(`[data-git-path="${path}"]`);
      const button = [...(row?.querySelectorAll("button") ?? [])].find((item) =>
        item.textContent?.includes(label),
      );
      expect(button).toBeTruthy();
      act(() => {
        button?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      });
    };

    clickIn("work.ts", "暂存");
    expect(onStage).toHaveBeenLastCalledWith(["work.ts"], "stage");
    clickIn("new.ts", "暂存");
    expect(onStage).toHaveBeenLastCalledWith(["new.ts"], "stage");
    clickIn("mod.ts", "取消暂存");
    expect(onStage).toHaveBeenLastCalledWith(["mod.ts"], "unstage");
  });

  it("点暂存不冒泡成选中（写操作不改变当前查看的文件）", () => {
    const onSelect = vi.fn();
    const onStage = vi.fn();
    const container = mount(
      <GitChangeList
        onRetry={vi.fn()}
        onSelect={onSelect}
        onStage={onStage}
        selectedPath={null}
        status={snapshot({ data: statusResult({ unstaged: [file("work.ts", ".M")] }) })}
      />,
    );
    const row = container.querySelector('[data-git-path="work.ts"]');
    const button = [...(row?.querySelectorAll("button") ?? [])].find((item) =>
      item.textContent?.includes("暂存"),
    );
    act(() => {
      button?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onStage).toHaveBeenCalledTimes(1);
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("组头按钮整组提交；冲突组没有写入口（不能宣称冲突已解决）", () => {
    const onStage = vi.fn();
    const container = mount(
      <GitChangeList
        onRetry={vi.fn()}
        onSelect={vi.fn()}
        onStage={onStage}
        selectedPath={null}
        status={snapshot({
          data: statusResult({
            conflicts: [file("conflict.ts", "UU")],
            unstaged: [file("a.ts", ".M"), file("b.ts", ".M")],
          }),
        })}
      />,
    );

    const conflictGroup = container.querySelector('[data-git-group="conflicts"]');
    expect(conflictGroup?.querySelectorAll("button")).toHaveLength(0);

    const groupButton = container.querySelector('[data-git-group="unstaged"] button');
    act(() => {
      groupButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onStage).toHaveBeenCalledWith(["a.ts", "b.ts"], "stage");
  });

  it("写操作在途时全组按钮禁用（不排队、不重放）", () => {
    const onStage = vi.fn();
    const container = mount(
      <GitChangeList
        onRetry={vi.fn()}
        onSelect={vi.fn()}
        onStage={onStage}
        selectedPath={null}
        stagePending
        stagingPaths={["work.ts"]}
        status={snapshot({ data: statusResult({ unstaged: [file("work.ts", ".M")] }) })}
      />,
    );
    const buttons = [...container.querySelectorAll("button")];
    expect(buttons.every((button) => button.disabled)).toBe(true);
    expect(container.textContent).toContain("暂存中…");
    act(() => {
      buttons[0]?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onStage).not.toHaveBeenCalled();
  });
});
