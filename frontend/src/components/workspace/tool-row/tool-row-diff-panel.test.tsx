// @vitest-environment jsdom

// 信息流 apply_patch 差异面板：小窗口右上角扩展图标 → 放大面板。
//
// 断言口径：
//   * 放大面板与右侧栏小窗口共用同一份解析结果与渲染栈（同一份诚实提示一起出现，不另建数据源）；
//   * 放大面板不得回退到整层灰底（历史缺陷：本面板漏改 `bg-black/*`，正文整体发灰）；
//   * 关闭/焦点路径由 `expanded-preview.test.tsx` 在外壳层钉住，这里不重复。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { i18n } from "@/i18n";
import { zhWorkspacePanelsGit } from "@/i18n/resources/zh-CN/workspace/panels-git";
import { zhWorkspacePanelsMessages } from "@/i18n/resources/zh-CN/workspace/panels-messages";
import { zhWorkspacePanelsPreview } from "@/i18n/resources/zh-CN/workspace/panels-preview";

import { ToolRowDiffPanel } from "./tool-row-diff-panel";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const PATCH = [
  "--- a/src/a.ts",
  "+++ b/src/a.ts",
  "@@ -1,2 +1,2 @@",
  " const a = 1;",
  "-old",
  "+new",
].join("\n");

const TRUNCATED_PATCH = `${PATCH}\n`;

describe("ToolRowDiffPanel 放大差异视图", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    i18n.addResourceBundle("zh-CN", "workspace", { panels: { git: zhWorkspacePanelsGit } }, true, false);
    i18n.addResourceBundle("zh-CN", "workspace", { panels: { messages: zhWorkspacePanelsMessages } }, true, false);
    i18n.addResourceBundle("zh-CN", "workspace", { panels: { preview: zhWorkspacePanelsPreview } }, true, false);
    if (i18n.language !== "zh-CN") {
      void i18n.changeLanguage("zh-CN");
    }

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

  function renderPanel(truncated = false) {
    act(() => {
      root.render(
        <ToolRowDiffPanel
          filePathHint="src/a.ts"
          patchText={truncated ? TRUNCATED_PATCH : PATCH}
          truncated={truncated}
        />,
      );
    });
  }

  function smallRows() {
    return container.querySelector('[data-testid="tool-row-diff-rows"]');
  }

  function dialog() {
    return document.body.querySelector<HTMLElement>('[data-testid="tool-row-diff-expanded"]');
  }

  function click(node: Element | null) {
    act(() => {
      node?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
  }

  it("小窗口保留原样；点扩展图标后放大面板渲染同一份行模型", () => {
    renderPanel();

    expect(smallRows()).not.toBeNull();
    expect(dialog()).toBeNull();

    click(container.querySelector('[data-testid="tool-row-diff-expand"]'));

    const opened = dialog();
    expect(opened).not.toBeNull();
    // 小窗口没有被搬走：放大是「再挂一份」，不是把原视口挪进弹层。
    expect(smallRows()).not.toBeNull();
    expect(opened?.querySelector('[data-testid="tool-row-diff-rows-expanded"]')).not.toBeNull();
    expect(opened?.textContent).toContain("src/a.ts");
  });

  it("诚实提示在放大面板里同样出现（同一份正文，不是第二份实现）", () => {
    renderPanel(true);

    expect(document.querySelectorAll('[data-testid="tool-row-diff-truncated"]')).toHaveLength(1);

    click(container.querySelector('[data-testid="tool-row-diff-expand"]'));

    // 小窗口 1 + 放大面板 1：提示来自同一份 renderDiffBody。
    expect(document.querySelectorAll('[data-testid="tool-row-diff-truncated"]')).toHaveLength(2);
  });

  it("放大面板不铺整层灰底（bg-black/* 回归）", () => {
    renderPanel();
    click(container.querySelector('[data-testid="tool-row-diff-expand"]'));

    const opened = dialog();
    expect(opened?.innerHTML).not.toContain("bg-black");
    expect(opened?.querySelector('[data-testid="tool-row-diff-rows-expanded"]')?.className).not.toContain(
      "bg-black",
    );
  });

  it("关闭按钮关掉放大面板，小窗口继续留在原位", () => {
    renderPanel();
    click(container.querySelector('[data-testid="tool-row-diff-expand"]'));
    click(document.body.querySelector('[data-testid="tool-row-diff-expanded-close"]'));

    expect(dialog()).toBeNull();
    expect(smallRows()).not.toBeNull();
  });
});
