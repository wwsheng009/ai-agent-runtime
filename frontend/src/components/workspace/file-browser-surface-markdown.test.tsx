// @vitest-environment jsdom

// 文件页签 Markdown 预览：md 文本用与聊天 / 预览弹层共享的 MessageMarkdown 渲染
// （标题、列表、表格等真实结构，不再是裸文本），并提供「Markdown / 文本」切换开关；
// 非 Markdown 文本不出现开关，仍走带行号的 TextViewer。

import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  clickRow,
  entry,
  flush,
  mountSurface,
  page,
  rootFixture,
  setupSurfaceDom,
  teardownSurfaceDom,
} from "@/test/file-browser-surface-harness";

const { fetchFsListingMock, fetchFsPreviewMock, fetchFsRootsMock } = vi.hoisted(() => ({
  fetchFsListingMock: vi.fn(),
  fetchFsPreviewMock: vi.fn(),
  fetchFsRootsMock: vi.fn(),
}));

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

const MD_SOURCE = [
  "# 预览标题",
  "",
  "- 列表项 A",
  "- 列表项 B",
  "",
  "| 列1 | 列2 |",
  "| --- | --- |",
  "| a | b |",
  "",
].join("\n");

async function settleUi() {
  await act(async () => {
    await flush();
  });
}

function query<T extends Element = Element>(selector: string): T | null {
  return document.body.querySelector<T>(selector);
}

function ariaPressed(testId: string): string | null {
  return query(`[data-testid="${testId}"]`)?.getAttribute("aria-pressed") ?? null;
}

function clickTestId(testId: string) {
  act(() => {
    query<HTMLButtonElement>(`[data-testid="${testId}"]`)?.dispatchEvent(
      new MouseEvent("click", { bubbles: true }),
    );
  });
}

async function mountWithEntries() {
  fetchFsRootsMock.mockResolvedValue({ roots: [rootFixture()] });
  fetchFsListingMock.mockResolvedValue(
    page({
      entries: [entry({ name: "guide.md", path: "guide.md" }), entry({ name: "a.ts", path: "a.ts" })],
    }),
  );
  await mountSurface();
}

/** 打开 md 文件页签并落定预览请求。 */
async function openMarkdownFile() {
  await mountWithEntries();
  clickRow("guide.md");
  await settleUi();
}

describe("FileBrowserSurface：Markdown 预览与 md/文本 切换", () => {
  beforeEach(() => {
    setupSurfaceDom();
    fetchFsListingMock.mockReset();
    fetchFsPreviewMock.mockReset();
    fetchFsRootsMock.mockReset();
    fetchFsPreviewMock.mockResolvedValue({
      kind: "text",
      path: "guide.md",
      size: 128,
      truncated: false,
      text: MD_SOURCE,
    });
  });

  afterEach(() => {
    teardownSurfaceDom();
  });

  it("md 文件默认渲染 Markdown：标题/列表/表格来自共享渲染器，并给出切换开关", async () => {
    await openMarkdownFile();

    const rendered = query('[data-testid="file-browser-preview-markdown"]');
    expect(rendered).not.toBeNull();
    expect(rendered?.querySelector("h1")?.textContent).toBe("预览标题");
    expect(rendered?.querySelectorAll("ul li")).toHaveLength(2);
    expect(rendered?.querySelector("table")).not.toBeNull();
    // 渲染后不再出现 Markdown 记号本身，也不在文本视图里。
    expect(rendered?.textContent).not.toContain("# 预览标题");
    expect(query('[data-testid="text-viewer"]')).toBeNull();

    expect(query('[data-testid="file-browser-preview-view"]')).not.toBeNull();
    expect(ariaPressed("file-browser-preview-view-markdown")).toBe("true");
    expect(ariaPressed("file-browser-preview-view-text")).toBe("false");
  });

  it("切到「文本」显示原始 Markdown 源码，切回「Markdown」恢复渲染", async () => {
    await openMarkdownFile();

    clickTestId("file-browser-preview-view-text");
    await settleUi();

    const viewer = query('[data-testid="text-viewer"]');
    expect(viewer).not.toBeNull();
    expect(viewer?.textContent).toContain("# 预览标题");
    expect(query('[data-testid="file-browser-preview-markdown"]')).toBeNull();
    expect(ariaPressed("file-browser-preview-view-text")).toBe("true");
    expect(ariaPressed("file-browser-preview-view-markdown")).toBe("false");

    clickTestId("file-browser-preview-view-markdown");
    await settleUi();

    expect(query('[data-testid="file-browser-preview-markdown"]')?.querySelector("h1")?.textContent).toBe(
      "预览标题",
    );
    expect(query('[data-testid="text-viewer"]')).toBeNull();
    expect(ariaPressed("file-browser-preview-view-markdown")).toBe("true");
  });

  it("非 Markdown 文本不出现切换开关：仍是带行号的文本视图", async () => {
    fetchFsPreviewMock.mockResolvedValue({
      kind: "text",
      path: "a.ts",
      size: 12,
      truncated: false,
      text: "const a = 1;\n",
    });
    await mountWithEntries();
    clickRow("a.ts");
    await settleUi();

    expect(query('[data-testid="file-browser-preview-view"]')).toBeNull();
    expect(query('[data-testid="text-viewer"]')?.textContent).toContain("const a = 1;");
    expect(query('[data-testid="file-browser-preview-markdown"]')).toBeNull();
  });

  it("放大面板头部挂同一份开关：在弹层里切换，内联视图同步", async () => {
    await openMarkdownFile();

    clickTestId("file-preview-expand");
    await settleUi();

    expect(query('[data-testid="file-preview-expanded"]')).not.toBeNull();
    expect(ariaPressed("file-preview-expanded-view-markdown")).toBe("true");

    clickTestId("file-preview-expanded-view-text");
    await settleUi();

    expect(ariaPressed("file-preview-expanded-view-text")).toBe("true");
    expect(ariaPressed("file-browser-preview-view-text")).toBe("true");
    // 内联与弹层各挂一份正文：两份都切到文本视图。
    expect(document.body.querySelectorAll('[data-testid="text-viewer"]').length).toBeGreaterThan(1);
    expect(query('[data-testid="file-browser-preview-markdown"]')).toBeNull();
  });
});
