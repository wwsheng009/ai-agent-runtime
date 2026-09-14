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
});
