// @vitest-environment jsdom

// P5 面级单测：文件浏览器升级为「多页签文件管理器」。
// 断言用户可见契约：点击文件名建页签并激活、重复点击只聚焦不重复、根页签常驻且不可关、
// 切回根页签恢复树视图、关闭活动页签按「右邻 → 左邻 → 根」回落、预览请求用打开时的快照。
// 纯函数层（去重 / 回落 / 身份）另见 `file-browser/file-manager-tabs.test.ts`。

import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  activeTabLabel,
  browserTreeVisible,
  clickRow,
  clickTab,
  closeTab,
  entry,
  filePaneShowsPath,
  flush,
  mountSurface,
  page,
  rootFixture,
  setupSurfaceDom,
  tabCloseButton,
  tabLabels,
  teardownSurfaceDom,
} from "@/test/file-browser-surface-harness";

const { fetchFsListingMock, fetchFsPreviewMock, fetchFsRootsMock } = vi.hoisted(() => ({
  fetchFsListingMock: vi.fn(),
  fetchFsPreviewMock: vi.fn(),
  fetchFsRootsMock: vi.fn(),
}));

// fs-roots 只替换请求函数，保留真实的降级判据实现（与既有面级用例同口径）。
vi.mock("@/api/runtime/fs-roots", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/runtime/fs-roots")>();
  return { ...actual, fetchFsRoots: fetchFsRootsMock };
});

vi.mock("@/api/runtime/fs-list", () => ({
  fetchFsListing: fetchFsListingMock,
  isFsListingCursorError: () => false,
}));

vi.mock("@/api/runtime/fs-preview", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/runtime/fs-preview")>();
  return { ...actual, fetchFsPreview: fetchFsPreviewMock };
});

/** 点击后的落定：React 调度器提交 + 页签正文的预览 Promise 链。 */
async function settleUi() {
  await act(async () => {
    await flush();
  });
}

describe("FileBrowserSurface P5：多页签文件管理器", () => {
  beforeEach(() => {
    setupSurfaceDom();
    fetchFsListingMock.mockReset();
    fetchFsPreviewMock.mockReset();
    fetchFsRootsMock.mockReset();
    fetchFsPreviewMock.mockResolvedValue({
      kind: "text",
      path: "",
      size: 0,
      truncated: false,
      text: "const a = 1;\n",
    });
  });

  afterEach(() => {
    teardownSurfaceDom();
  });

  async function mountWithFiles() {
    fetchFsRootsMock.mockResolvedValue({ roots: [rootFixture()] });
    fetchFsListingMock.mockResolvedValue(
      page({
        entries: [
          entry({ name: "a.ts", path: "a.ts" }),
          entry({ name: "b.ts", path: "b.ts" }),
          entry({ name: "c.ts", path: "c.ts" }),
          entry({ name: "src", path: "src", type: "dir" }),
        ],
      }),
    );
    await mountSurface();
  }

  it("初始只有根页签「文件浏览器」：树视图在场，根页签没有关闭入口", async () => {
    await mountWithFiles();

    expect(tabLabels()).toEqual(["文件浏览器"]);
    expect(activeTabLabel()).toBe("文件浏览器");
    expect(browserTreeVisible()).toBe(true);
    expect(tabCloseButton("文件浏览器")).toBeNull();
    expect(fetchFsPreviewMock).not.toHaveBeenCalled();
  });

  it("点击文件名建页签并激活：正文接管、按打开时的 scope+path 取数", async () => {
    await mountWithFiles();

    clickRow("a.ts");
    await settleUi();

    expect(tabLabels()).toEqual(["文件浏览器", "a.ts"]);
    expect(activeTabLabel()).toBe("a.ts");
    // 文件页签接管正文：树让位，正文显示路径。
    expect(browserTreeVisible()).toBe(false);
    expect(filePaneShowsPath("a.ts")).toBe(true);
    expect(tabCloseButton("a.ts")).not.toBeNull();
    const call = fetchFsPreviewMock.mock.calls.at(-1);
    expect(call?.[0]).toEqual({ scope: "session:s1", path: "a.ts" });

    // 切回根页签：树视图回来，正文让位。
    clickTab("文件浏览器");
    expect(activeTabLabel()).toBe("文件浏览器");
    expect(browserTreeVisible()).toBe(true);
  });

  it("多个文件按打开顺序追加页签；重复点击同一文件只聚焦不重复", async () => {
    await mountWithFiles();

    clickRow("a.ts");
    await settleUi();
    // 文件页签激活时目录树让位：继续开下一个文件前先切回根页签。
    clickTab("文件浏览器");
    clickRow("b.ts");
    await settleUi();
    expect(tabLabels()).toEqual(["文件浏览器", "a.ts", "b.ts"]);

    // 重复点击 a.ts：页签数不变，激活态回到 a.ts（不产生第二个 a.ts）。
    clickTab("文件浏览器");
    clickRow("a.ts");
    await settleUi();
    expect(tabLabels()).toEqual(["文件浏览器", "a.ts", "b.ts"]);
    expect(activeTabLabel()).toBe("a.ts");
  });

  it("关闭活动页签依次回落到右邻、左邻，最后回根页签", async () => {
    await mountWithFiles();

    clickRow("a.ts");
    await settleUi();
    clickTab("文件浏览器");
    clickRow("b.ts");
    await settleUi();
    clickTab("文件浏览器");
    clickRow("c.ts");
    await settleUi();
    expect(tabLabels()).toEqual(["文件浏览器", "a.ts", "b.ts", "c.ts"]);

    // 关中间的活动页签 b → 回落到右邻 c。
    clickTab("b.ts");
    closeTab("b.ts");
    expect(tabLabels()).toEqual(["文件浏览器", "a.ts", "c.ts"]);
    expect(activeTabLabel()).toBe("c.ts");

    // 关末位活动页签 c → 无右邻，回落左邻 a。
    closeTab("c.ts");
    expect(activeTabLabel()).toBe("a.ts");

    // 关最后一个 → 回根页签，树视图回来。
    closeTab("a.ts");
    expect(tabLabels()).toEqual(["文件浏览器"]);
    expect(activeTabLabel()).toBe("文件浏览器");
    expect(browserTreeVisible()).toBe(true);
  });

  it("文件页签保留放大弹层入口（P1 弹层口径）：点放大开覆盖面板，关闭按钮收回，内联正文不丢", async () => {
    await mountWithFiles();

    clickRow("a.ts");
    await settleUi();

    const expand = document.body.querySelector<HTMLButtonElement>('[data-testid="file-preview-expand"]');
    expect(expand).not.toBeNull();
    act(() => {
      expand?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await settleUi();

    // 放大面板在场：正文以同一份实现再挂一次（同名 testid），关闭按钮可点。
    expect(document.body.querySelector('[data-testid="file-preview-expanded"]')).not.toBeNull();
    expect(document.body.querySelectorAll('[data-testid="file-browser-preview"]').length).toBeGreaterThan(1);

    const close = document.body.querySelector<HTMLButtonElement>('[data-testid="file-preview-expanded-close"]');
    expect(close).not.toBeNull();
    act(() => {
      close?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await settleUi();

    expect(document.body.querySelector('[data-testid="file-preview-expanded"]')).toBeNull();
    // 页签内联正文仍在：放大只换容器尺寸，不夺走页签内容。
    expect(document.body.querySelector('[data-testid="file-browser-preview"]')).not.toBeNull();
  });
});
