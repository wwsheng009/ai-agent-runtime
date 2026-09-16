// @vitest-environment jsdom
// P1-6 验收：成功 / 失败 / 流式中 / 长输出四态 + 键盘聚焦文件链接不触发行展开。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { type ToolMessageSegment } from "@/lib/thread-state/messages";

import { MessageToolRow } from "./message-tool-row";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

describe("MessageToolRow", () => {
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

  function renderRow(
    segment: ToolMessageSegment,
    resolveFilePathLink?: (path: string) => (() => void) | null,
  ) {
    act(() => {
      root?.render(
        <MessageToolRow resolveFilePathLink={resolveFilePathLink} segment={segment} />,
      );
    });
  }

  function toolSegment(
    partial: Partial<Omit<ToolMessageSegment, "type">> = {},
  ): ToolMessageSegment {
    return { type: "tool", name: "read_file", status: "finished", ...partial };
  }

  it("标题：始终渲染原始工具名（已注册 kind 也不替换为动作词）", () => {
    renderRow(toolSegment({ details: { filePath: "src/a.ts" } }));
    expect(
      container.querySelector('[data-chat-row-title="true"]')?.textContent,
    ).toBe("read_file");

    renderRow(toolSegment({ name: "mcp__server__custom_thing" }));
    expect(
      container.querySelector('[data-chat-row-title="true"]')?.textContent,
    ).toBe("mcp__server__custom_thing");
  });

  it("成功态：状态播报、可展开输入、长输出在滚动容器内", () => {
    const longOutput = Array.from({ length: 40 }, (_, index) => `line ${index + 1}`).join("\n");
    renderRow(
      toolSegment({
        argsSummary: '{"file_path":"src/a.ts"}',
        resultSummary: longOutput,
        details: { filePath: "src/a.ts" },
      }),
    );

    const row = container.querySelector('[data-tool-row="true"]');
    expect(row?.getAttribute("data-tool-row-status")).toBe("finished");
    expect(row?.getAttribute("data-tool-row-kind")).toBe("read");
    expect(container.querySelector('[role="status"]')?.textContent).toContain("read_file");
    expect(container.querySelector('[role="status"]')?.textContent).toContain("已完成");

    const toggle = container.querySelector<HTMLButtonElement>(
      '[data-chat-row-toggle="chevron"]',
    );
    expect(toggle?.getAttribute("aria-expanded")).toBe("false");
    // 折叠态 24px 单行：结果与输入都在 hidden 面板内（不占高度，不参与默认滚动噪声）。
    const panel = container.querySelector<HTMLElement>('[data-tool-row-detail-panel="true"]');
    expect(panel?.hidden).toBe(true);
    expect(panel?.querySelector('[data-tool-row-output="result"]')).toBeTruthy();
    expect(panel?.querySelector('[data-tool-row-input-panel="true"]')).toBeTruthy();

    act(() => toggle?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(toggle?.getAttribute("aria-expanded")).toBe("true");
    expect(
      container.querySelector<HTMLElement>('[data-tool-row-detail-panel="true"]')?.hidden,
    ).toBe(false);

    const output = container.querySelector('[data-tool-row-output="result"] pre');
    expect(output?.textContent).toBe(longOutput);
    expect(output?.className).toContain("max-h-48");
    expect(output?.className).toContain("overflow-y-auto");
  });

  it("展开入口：点前导图标与点右侧 chevron 同义，aria-expanded 同步", () => {
    renderRow(toolSegment({ argsSummary: "npm test", resultSummary: "ok" }));

    const iconToggle = container.querySelector<HTMLButtonElement>(
      '[data-chat-row-toggle="icon"]',
    );
    const chevronToggle = container.querySelector<HTMLButtonElement>(
      '[data-chat-row-toggle="chevron"]',
    );
    const panel = () =>
      container.querySelector<HTMLElement>('[data-tool-row-detail-panel="true"]');

    expect(iconToggle).toBeTruthy();
    expect(chevronToggle).toBeTruthy();
    expect(panel()?.hidden).toBe(true);

    act(() => iconToggle?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(panel()?.hidden).toBe(false);
    expect(iconToggle?.getAttribute("aria-expanded")).toBe("true");
    expect(chevronToggle?.getAttribute("aria-expanded")).toBe("true");
    // 指针可达但不在 Tab 顺序里：每行只留一个键盘停靠点。
    expect(iconToggle?.tabIndex).toBe(-1);
    expect(chevronToggle?.tabIndex).toBe(0);

    act(() => chevronToggle?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(panel()?.hidden).toBe(true);
    expect(iconToggle?.getAttribute("aria-expanded")).toBe("false");
  });

  it("失败态：错误块替换输出、文件链接禁用且不残留结果块", () => {
    const selectArtifact = vi.fn();
    renderRow(
      toolSegment({
        status: "error",
        errorMessage: "patch 校验失败",
        resultSummary: "旧的输出不应出现",
        argsSummary: "*** Begin Patch",
        details: { filePath: "src/a.ts", diff: { additions: 1, removals: 1 } },
      }),
      () => selectArtifact,
    );

    const row = container.querySelector('[data-tool-row="true"]');
    expect(row?.getAttribute("data-tool-row-status")).toBe("error");
    // 失败态禁用链接：不渲染可聚焦按钮，路径以禁用标记呈现。
    expect(container.querySelector('[data-tool-row-file-link="true"]')).toBeNull();
    expect(container.querySelector('[data-tool-row-link-disabled="true"]')?.textContent).toBe(
      "src/a.ts",
    );

    // 失败态行高不变：错误详情收进折叠面板，展开后才出现（错误块替换输出块）。
    const toggle = container.querySelector<HTMLButtonElement>(
      '[data-chat-row-toggle="chevron"]',
    );
    expect(toggle?.getAttribute("aria-expanded")).toBe("false");
    expect(
      container.querySelector<HTMLElement>('[data-tool-row-detail-panel="true"]')?.hidden,
    ).toBe(true);

    act(() => toggle?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(container.querySelector('[data-tool-row-output="error"]')?.textContent).toContain(
      "patch 校验失败",
    );
    expect(container.querySelector('[data-tool-row-output="result"]')).toBeNull();
    expect(container.textContent).not.toContain("旧的输出不应出现");
  });

  it("流式中：运行态图标与徽标，尚未产生输出块", () => {
    renderRow(
      toolSegment({ name: "shell", status: "running", argsSummary: "npm test" }),
    );

    const row = container.querySelector('[data-tool-row="true"]');
    expect(row?.getAttribute("data-tool-row-status")).toBe("running");
    expect(container.textContent).toContain("执行中");
    expect(container.querySelector(".animate-spin")).toBeTruthy();
    expect(
      container.querySelector<HTMLElement>('[data-tool-row-detail-panel="true"]')?.hidden,
    ).toBe(true);
    expect(container.querySelector('[data-tool-row-output="result"]')).toBeNull();
    expect(container.querySelector('[data-tool-row-output="error"]')).toBeNull();
  });

  it("已开始且无输入：不渲染展开按钮", () => {
    renderRow(toolSegment({ name: "my_custom_tool", status: "started" }));

    expect(container.textContent).toContain("已开始");
    expect(container.querySelector('[aria-controls$="-panel"]')).toBeNull();
    expect(
      container.querySelector('[data-tool-row="true"]')?.getAttribute("data-tool-row-expandable"),
    ).toBe("false");
  });

  it("diff 卡：折叠行携带 +A -R 与文件链接", () => {
    renderRow(
      toolSegment({
        name: "apply_patch",
        argsSummary: "*** Begin Patch",
        details: { filePath: "src/a.ts", diff: { additions: 4, removals: 2 } },
      }),
      () => vi.fn(),
    );

    expect(container.querySelector('[data-tool-row-kind="diff"]')).toBeTruthy();
    expect(
      container.querySelector('[data-tool-row-diff-additions="4"]')?.textContent?.trim(),
    ).toBe("+4");
    expect(
      container.querySelector('[data-tool-row-diff-removals="2"]')?.textContent?.trim(),
    ).toBe("−2");
  });

  it("键盘：聚焦文件链接并按 Enter 触发跳转，不展开行", () => {
    const openFile = vi.fn();
    renderRow(
      toolSegment({ argsSummary: "src/a.ts", details: { filePath: "src/a.ts" } }),
      () => openFile,
    );

    const link = container.querySelector<HTMLButtonElement>('[data-tool-row-file-link="true"]');
    expect(link).toBeTruthy();
    act(() => link?.focus());
    expect(document.activeElement).toBe(link);

    act(() => {
      link?.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Enter" }));
    });
    expect(openFile).not.toHaveBeenCalled(); // 键盘原生激活由 click 完成，keydown 只阻断冒泡

    act(() => link?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(openFile).toHaveBeenCalledTimes(1);

    const toggle = container.querySelector<HTMLButtonElement>(
      '[data-chat-row-toggle="chevron"]',
    );
    expect(toggle?.getAttribute("aria-expanded")).toBe("false");
  });

  it("键盘：无解析目标时路径为纯文本，不出现死链接", () => {
    renderRow(toolSegment({ argsSummary: "src/a.ts", details: { filePath: "src/a.ts" } }));

    expect(container.querySelector('[data-tool-row-file-link="true"]')).toBeNull();
    expect(container.querySelector('[data-tool-row-file-link="false"]')?.textContent).toBe(
      "src/a.ts",
    );
  });

  it("ls（目录列举）：折叠行显示目录，且目录不渲染为文件链接", () => {
    const openFile = vi.fn();
    // 宿主**提供了**文件链接解析：目录行仍不得出现链接（目录不是可打开的文件）。
    renderRow(
      toolSegment({ name: "ls", argsSummary: '{"path":"frontend/e2e","depth":2}' }),
      () => openFile,
    );

    const row = container.querySelector('[data-tool-row="true"]');
    expect(row?.getAttribute("data-tool-row-kind")).toBe("list");
    expect(row?.getAttribute("data-tool-row-has-summary")).toBe("true");
    expect(container.querySelector("[data-tool-row-summary]")?.textContent).toContain(
      "frontend/e2e",
    );
    expect(container.querySelector('[data-tool-row-file-link="true"]')).toBeNull();
    expect(container.querySelector('[data-tool-row-file-link="false"]')?.textContent).toBe(
      "frontend/e2e",
    );
    expect(openFile).not.toHaveBeenCalled();
  });

  const NL = String.fromCharCode(10);
  /** 后端 tool.completed 的真实形态：说明行 + diff 围栏（带行号 hunk）。 */
  const DIFF_FENCE = [
    "补丁已应用：修改 1；影响 1 个路径",
    "",
    "文件差异:",
    "```diff",
    "--- a/src/a.ts",
    "+++ b/src/a.ts",
    "@@ -1 +1 @@",
    "-old line",
    "+new line",
    "```",
  ].join(NL);
  const RENDERABLE_PATCH = ["--- a/src/a.ts", "+++ b/src/a.ts", "@@ -1 +1 @@", "-old line", "+new line"].join(
    NL,
  );

  function expandRow() {
    const toggle = container.querySelector<HTMLButtonElement>('[data-chat-row-toggle="chevron"]');
    act(() => toggle?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  }

  it("输入面板：同一 ls 调用在回放（JSON 入参）与实时（预览文本）下展开出同一展示", () => {
    function remount() {
      act(() => root?.unmount());
      root = createRoot(container);
    }

    // 回放链路：历史里存的是结构化 arguments。
    renderRow(toolSegment({ name: "ls", argsSummary: '{"path":"frontend/e2e","depth":2}' }));
    expandRow();
    const replayText = container.querySelector(
      '[data-tool-row-input-panel="true"] pre',
    )?.textContent;
    expect(replayText).toBe("depth=2 path=frontend/e2e");

    // 实时链路：SSE 帧只带后端预览文本，展开后必须与回放一致（不能一边 JSON 一边键值）。
    remount();
    renderRow(toolSegment({ name: "ls", argsSummary: "depth=2 path=frontend/e2e" }));
    expandRow();
    expect(
      container.querySelector('[data-tool-row-input-panel="true"] pre')?.textContent,
    ).toBe(replayText);
  });

  it("apply_patch 展开：行级 diff 替换原始文本，输出块不再重复围栏", () => {
    renderRow(
      toolSegment({
        name: "apply_patch",
        resultSummary: DIFF_FENCE,
        details: {
          filePath: "src/a.ts",
          diff: { additions: 1, removals: 1 },
          diffText: RENDERABLE_PATCH,
        },
      }),
    );
    expandRow();

    const panel = container.querySelector('[data-testid="tool-row-diff-panel"]');
    expect(panel).toBeTruthy();
    expect(container.querySelector('[data-tool-row-input-panel="true"]')).toBeNull();
    expect(panel?.querySelector('[data-testid="tool-row-diff-path"]')?.textContent).toBe("src/a.ts");
    expect(panel?.querySelector('[data-diff-hunk-header="expanded"]')).toBeTruthy();
    expect(panel?.textContent).toContain("@@ -1 +1 @@");
    expect(panel?.querySelectorAll('[data-diff-cell="add"]').length).toBe(1);
    expect(panel?.querySelectorAll('[data-diff-cell="del"]').length).toBe(1);
    expect(panel?.textContent).toContain("old line");
    expect(panel?.textContent).toContain("new line");

    // 样式口径（回归：正文整层灰化）：视口只留边框 / 圆角，不得再铺 `bg-black/*` 的整层灰底
    // （增删语义由行底色 `-bg` token 承载）；增行文本一律前景色，`-accent` 是半透明轨色不得当文字色。
    const viewport = panel?.querySelector<HTMLElement>('[data-testid="tool-row-diff-rows"]');
    expect(viewport?.className).toContain("rounded-panel");
    expect(viewport?.className).not.toMatch(/bg-black/);
    expect(panel?.innerHTML).not.toContain("bg-black");
    const addCell = panel?.querySelector('[data-diff-cell="add"]');
    expect(addCell?.className).toContain("text-foreground");
    expect(addCell?.className).not.toContain("text-code-line-inserted-accent");

    // 输出块保留工具说明行，但补丁正文不再以原始文本重复出现。
    const output = container.querySelector('[data-tool-row-output="result"]')?.textContent ?? "";
    expect(output).toContain("补丁已应用：修改 1；影响 1 个路径");
    expect(output).not.toContain("```diff");
    expect(output).not.toContain("+new line");
  });

  it("apply_patch 展开：多文件补丁按文件切换，只渲染选中文件的行", () => {
    const patch = [
      "diff --git a/a.ts b/a.ts",
      "--- a/a.ts",
      "+++ b/a.ts",
      "@@ -1 +1 @@",
      "-a old",
      "+a new",
      "diff --git a/b.ts b/b.ts",
      "--- a/b.ts",
      "+++ b/b.ts",
      "@@ -1 +1 @@",
      "-b old",
      "+b new",
    ].join(NL);
    renderRow(
      toolSegment({
        name: "apply_patch",
        resultSummary: "补丁已应用",
        details: { diff: { additions: 2, removals: 2 }, diffText: patch },
      }),
    );
    expandRow();

    const files = container.querySelectorAll('[data-testid="tool-row-diff-files"] button');
    expect(files.length).toBe(2);
    expect(container.textContent).toContain("a old");
    expect(container.textContent).not.toContain("b old");

    act(() => files[1]?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(container.textContent).toContain("b old");
    expect(container.textContent).not.toContain("a old");
  });

  it("apply_patch 展开：截断与末尾 hunk 不完整都明示，不假装完整", () => {
    renderRow(
      toolSegment({
        name: "apply_patch",
        resultSummary: "补丁已应用",
        details: {
          diff: { additions: 0, removals: 1 },
          diffText: ["--- a/x.ts", "+++ b/x.ts", "@@ -1,3 +1,3 @@", " one", " two"].join(NL),
          diffTextTruncated: true,
        },
      }),
    );
    expandRow();

    expect(container.querySelector('[data-testid="tool-row-diff-truncated"]')).toBeTruthy();
    expect(container.querySelector('[data-testid="tool-row-diff-partial"]')).toBeTruthy();
  });

  it("失败态：错误块优先，不从补丁文本渲染行级视图", () => {
    renderRow(
      toolSegment({
        name: "apply_patch",
        status: "error",
        errorMessage: "patch 校验失败",
        details: { diffText: RENDERABLE_PATCH },
      }),
    );
    expandRow();

    expect(container.querySelector('[data-testid="tool-row-diff-panel"]')).toBeNull();
    expect(container.querySelector('[data-tool-row-output="error"]')?.textContent).toContain(
      "patch 校验失败",
    );
  });

  it("补丁不可解析：保持原始输入面板（不渲染行级视图）", () => {
    const marker = "***";
    const codexPatch = [
      `${marker} Begin Patch`,
      `${marker} Update File: src/a.ts`,
      "@@",
      "-old",
      "+new",
      `${marker} End Patch`,
    ].join(NL);
    renderRow(
      toolSegment({
        name: "apply_patch",
        argsSummary: codexPatch,
        resultSummary: "补丁已应用：修改 1；影响 1 个路径",
        details: { filePath: "src/a.ts", diff: { additions: 1, removals: 1 } },
      }),
    );
    expandRow();

    expect(container.querySelector('[data-testid="tool-row-diff-panel"]')).toBeNull();
    expect(container.querySelector('[data-tool-row-input-panel="true"]')).toBeTruthy();
    expect(container.querySelector('[data-tool-row-output="result"]')?.textContent).toContain(
      "补丁已应用：修改 1；影响 1 个路径",
    );
  });
});
