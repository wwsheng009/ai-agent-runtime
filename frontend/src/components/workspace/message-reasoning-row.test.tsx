// @vitest-environment jsdom
// §12.1.4 验收：推理行的两条硬约束
// 1) 无推理正文（空 / 纯空白）时不渲染行 —— 不用占位文案顶上屏，也不留 24px 空行；
// 2) 有正文时前导图标与右侧 chevron 同义展开（指针用户不必瞄准最右侧）。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { MessageReasoningRow } from "./message-reasoning-row";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

describe("MessageReasoningRow", () => {
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

  function renderRow(content: string, streaming = false) {
    act(() => {
      root?.render(
        <MessageReasoningRow
          flowKey="assistant-step:reasoning-1"
          segment={{ type: "reasoning", content }}
          streaming={streaming}
        />,
      );
    });
  }

  it("空正文：不渲染行（无占位文案、无空行）", () => {
    renderRow("");
    expect(container.innerHTML).toBe("");

    renderRow("   \n\t ");
    expect(container.innerHTML).toBe("");
  });

  it("流式窗口内空正文：同样不渲染空壳行", () => {
    renderRow("", true);
    expect(container.innerHTML).toBe("");
  });

  it("有正文：渲染 24px 单行 + 单行摘要", () => {
    renderRow("先盘点入口文件，再确认调用链。");
    const row = container.querySelector<HTMLElement>('[data-chat-row="reasoning"]');
    expect(row).not.toBeNull();
    expect(row?.getAttribute("data-chat-row-state")).toBe("closed");
    expect(
      container.querySelector('[data-chat-row-summary="true"]')?.textContent,
    ).toContain("先盘点入口文件");
  });

  it("展开入口：点前导图标与点右侧 chevron 同义，aria-expanded 同步", () => {
    renderRow("先盘点入口文件，再确认调用链。");

    const icon = container.querySelector<HTMLElement>(
      '[data-chat-row-leading="icon"]',
    );
    expect(icon).not.toBeNull();

    act(() => icon?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(
      container
        .querySelector('[data-chat-row="reasoning"]')
        ?.getAttribute("data-chat-row-state"),
    ).toBe("open");
    expect(
      container.querySelector<HTMLElement>('[data-chat-row-panel="reasoning"]'),
    ).not.toBeNull();

    act(() => icon?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(
      container
        .querySelector('[data-chat-row="reasoning"]')
        ?.getAttribute("data-chat-row-state"),
    ).toBe("closed");
  });
});
