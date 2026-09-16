// @vitest-environment jsdom

// 放大预览外壳单测：三个宿主面（Git diff 浏览 / 文件预览 / 工具行差异）共用本件，
// 因此这里只钉**外壳契约**，不重复各自的正文渲染（正文由各面的既有测试覆盖）。
//
// 断言纪律：
//   * 关闭路径三条必须都真的能关（面板按钮 / 遮罩点击 / Esc）——少一条就是「关不掉」的死角；
//   * 焦点纪律：打开后焦点必须在面板内（触发元素在工具行里会吞 keydown，焦点留在外面 Esc 失效），
//     关闭后必须回到触发元素（否则键盘用户的上下文丢失）；
//   * 关闭态不得残留 DOM（弹层走 portal，断言 document.body 而不是容器）。

import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { ExpandedPreviewDialog, ExpandPreviewButton } from "./expanded-preview";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

let container: HTMLDivElement;
let root: Root;

function Host() {
  const [open, setOpen] = useState(false);

  return (
    <div>
      <ExpandPreviewButton label="放大文件预览" onClick={() => setOpen(true)} testId="host-expand" />
      <ExpandedPreviewDialog
        ariaLabel="放大视图"
        closeLabel="关闭放大面板"
        eyebrow="放大视图"
        hint="Esc 或点击遮罩关闭"
        onClose={() => setOpen(false)}
        open={open}
        subtitle="1.2 KB"
        testId="host-expanded"
        title="src/a.ts"
      >
        <div data-testid="host-content">正文内容</div>
      </ExpandedPreviewDialog>
    </div>
  );
}

function panel() {
  return document.body.querySelector<HTMLElement>('[data-testid="host-expanded"]');
}

function trigger() {
  return container.querySelector<HTMLButtonElement>('[data-testid="host-expand"]');
}

function click(node: Element | null) {
  act(() => {
    node?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

// 遮罩关闭走 `onMouseDown`（按下即关，不等抬起）：断言时不能用 click 糊弄过去。
function mouseDown(node: Element | null) {
  act(() => {
    node?.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
  });
}

function pressEscape() {
  act(() => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  });
}

describe("ExpandedPreviewDialog", () => {
  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    act(() => root.render(<Host />));
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("关闭态不渲染面板；点扩展图标后弹出面板并显示标题 / 副标题 / 正文", () => {
    expect(panel()).toBeNull();

    click(trigger());

    const opened = panel();
    expect(opened).not.toBeNull();
    expect(opened?.getAttribute("aria-modal")).toBe("true");
    expect(opened?.textContent).toContain("src/a.ts");
    expect(opened?.textContent).toContain("1.2 KB");
    expect(opened?.querySelector('[data-testid="host-content"]')?.textContent).toBe("正文内容");
    // 面板挂在 body 上（portal），不挤在宿主容器里。
    expect(container.querySelector('[data-testid="host-expanded"]')).toBeNull();
  });

  it("关闭路径一：面板右上角关闭按钮", () => {
    click(trigger());
    click(document.body.querySelector('[data-testid="host-expanded-close"]'));

    expect(panel()).toBeNull();
  });

  it("关闭路径二：点击遮罩", () => {
    click(trigger());
    mouseDown(panel()?.parentElement ?? null);

    expect(panel()).toBeNull();
  });

  it("关闭路径三：Esc", () => {
    click(trigger());
    pressEscape();

    expect(panel()).toBeNull();
  });

  it("焦点纪律：打开时焦点移入面板，关闭后回到触发元素", () => {
    const button = trigger();
    act(() => button?.focus());

    click(button);
    expect(document.activeElement).toBe(panel());

    click(document.body.querySelector('[data-testid="host-expanded-close"]'));
    expect(document.activeElement).toBe(button);
  });
});
