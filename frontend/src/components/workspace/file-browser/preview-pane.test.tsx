// @vitest-environment jsdom

// 文件预览面板：小窗口右上角扩展图标 → 放大面板。
//
// 断言口径：
//   * 放大面板必须复用同一份取数结果——展开不得再打一次 `/fs/preview`（否则两份预览可能不一致）；
//   * 没有可预览目标（目录 / 无权限项）时不提供放大入口，避免点开一个必然为空的弹层；
//   * 关闭/焦点路径由 `expanded-preview.test.tsx` 在外壳层钉住。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { i18n } from "@/i18n";
import { zhWorkspacePanelsFileBrowser } from "@/i18n/resources/zh-CN/workspace/panels-file-browser";
import { zhWorkspacePanelsPreview } from "@/i18n/resources/zh-CN/workspace/panels-preview";
import type { FsEntry, FsPreview } from "@/types/runtime/fs-browser";

import { PreviewPane } from "./preview-pane";

const { fetchFsPreviewMock } = vi.hoisted(() => ({ fetchFsPreviewMock: vi.fn() }));

// 只替换请求函数，保留真实的 `isFsPreviewUnavailable`（降级判据必须走真实实现）。
vi.mock("@/api/runtime/fs-preview", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/runtime/fs-preview")>();
  return { ...actual, fetchFsPreview: fetchFsPreviewMock };
});

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const TEXT = "const a = 1;\nconst b = 2;\n";

function entry(overrides: Partial<FsEntry> = {}): FsEntry {
  return { name: "a.ts", path: "src/a.ts", type: "file", size: TEXT.length, mtime: 0, ...overrides };
}

function textPreview(): FsPreview {
  return { kind: "text", path: "src/a.ts", size: TEXT.length, truncated: false, text: TEXT };
}

describe("PreviewPane 放大文件预览", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    i18n.addResourceBundle(
      "zh-CN",
      "workspace",
      { panels: { fileBrowser: zhWorkspacePanelsFileBrowser, preview: zhWorkspacePanelsPreview } },
      true,
      false,
    );
    if (i18n.language !== "zh-CN") {
      void i18n.changeLanguage("zh-CN");
    }
    fetchFsPreviewMock.mockReset();
    fetchFsPreviewMock.mockResolvedValue(textPreview());

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

  async function renderPane(target: FsEntry | null) {
    await act(async () => {
      root.render(
        <PreviewPane entry={target} onDownload={vi.fn()} scope="session:s1" />,
      );
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  function dialog() {
    return document.body.querySelector<HTMLElement>('[data-testid="file-preview-expanded"]');
  }

  function click(node: Element | null) {
    act(() => {
      node?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
  }

  it("预览就绪后点扩展图标：放大面板显示同一份内容，且不重复请求", async () => {
    await renderPane(entry());

    const expand = container.querySelector('[data-testid="file-preview-expand"]');
    expect(expand).not.toBeNull();
    expect(dialog()).toBeNull();

    click(expand);

    const opened = dialog();
    expect(opened).not.toBeNull();
    expect(opened?.textContent).toContain("src/a.ts");
    expect(opened?.textContent).toContain("const a = 1;");
    expect(fetchFsPreviewMock).toHaveBeenCalledTimes(1);

    click(document.body.querySelector('[data-testid="file-preview-expanded-close"]'));

    expect(dialog()).toBeNull();
    // 小窗口没有跟着消失：放大只是「再挂一份」，关掉弹层回到原视口。
    expect(container.querySelector('[data-testid="file-browser-preview"]')).not.toBeNull();
  });

  it("目录 / 无权限项：没有可预览目标，不提供放大入口", async () => {
    await renderPane(entry({ type: "dir" }));

    expect(container.querySelector('[data-testid="file-preview-expand"]')).toBeNull();
    expect(fetchFsPreviewMock).not.toHaveBeenCalled();
  });
});
