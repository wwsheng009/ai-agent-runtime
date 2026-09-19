import type { Locator, Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedSession } from "./support";

// P5 e2e：文件浏览器升级为「多页签文件管理器」（根页签常驻 + 点击文件追加页签）。
//
// 为什么必须在真实浏览器里跑：单测（jsdom）只证明页签模型与回调接线，证明不了
// 「页签条真的在面板顶部可见」「切到文件页签后预览真的填满剩余高度」——高度链
// 断掉时容器高度退化为内容高度，类名照旧、断言照旧过，用户看到的却是塌陷。
// 这里沿用 file-browser-panel.spec.ts 的同一口径：断言可视盒与滚动真实发生。
//
// 夹具（e2e/mock-server.mjs）：根层 83 项（entry-001.txt … + notes.txt），只有
// notes.txt 可预览（其余 404）；预览标记 E2E_PREVIEW_TEXT_OK。

const SESSION_ID = "e2e-files-session";
const PREVIEW_MARKER = "E2E_PREVIEW_TEXT_OK";

const composer = (page: Page) => page.locator(".app-chat-input");
const filesTab = (page: Page) => page.getByTestId("artifact-panel-tab-files");
const tabStrip = (page: Page) => page.getByTestId("file-manager-tab-strip");
const browserTab = (page: Page) => page.getByTestId("file-manager-tab-browser");
const tree = (page: Page) => page.getByTestId("file-browser-tree");

/**
 * 文件页签的 testid 含 `\u0000` 分隔符（`scope\0path`），拿它做选择器不划算；
 * 页签按钮的稳定可访问名就是文件名，因此按 role+name 定位。
 * 关闭按钮是 button（aria-label "Close <name>"），不会污染 tab 查询。
 */
const fileTab = (page: Page, name: string) => tabStrip(page).getByRole("tab", { name, exact: true });
const tabCloseButton = (page: Page, name: string) =>
  tabStrip(page).getByRole("button", { name: `Close ${name}`, exact: true });

async function openSession(page: Page): Promise<void> {
  await page.goto(`/workspace/chats/${SESSION_ID}`);
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });
}

/** 展开右侧栏（只认 aria-pressed，理由同 file-browser-panel.spec.ts）。 */
async function openRightRail(page: Page): Promise<void> {
  const toggle = page.getByTestId("topbar-toggle-right-rail");
  await expect(toggle).toBeVisible({ timeout: 30_000 });
  if ((await toggle.getAttribute("aria-pressed")) !== "true") {
    await toggle.click();
  }
  await expect(toggle).toHaveAttribute("aria-pressed", "true", { timeout: 15_000 });
  await expect(filesTab(page)).toBeVisible({ timeout: 15_000 });
}

async function openFilesSurface(page: Page): Promise<void> {
  await openSession(page);
  await openRightRail(page);
  await filesTab(page).click();
  await expect(tabStrip(page)).toBeVisible({ timeout: 15_000 });
}

/**
 * 点击树行。
 *
 * 树是虚拟滚动：目标行不在窗口内时**根本不在 DOM 里**，`filter({ hasText })` 找不到它，
 * Playwright 也不会为一个不存在的元素去滚动（`scrollIntoViewIfNeeded` 只对已渲染元素生效）。
 * 夹具里 notes.txt 排在 83 项之末，因此先把视口滚到底再点；否则会像本 spec 第一版那样
 * 以「element(s) not found」失败。
 */
async function clickTreeRow(page: Page, name: string): Promise<void> {
  const viewport = tree(page);
  await expect(viewport).toBeVisible({ timeout: 15_000 });
  const row = viewport.locator('[role="treeitem"]').filter({ hasText: name }).first();
  for (let attempt = 0; attempt < 12; attempt += 1) {
    if (await row.count()) {
      await row.click();
      return;
    }
    // 未渲染 → 向底部推进一屏（最后一屏之后收敛）；虚拟窗口随后会渲出目标行。
    await viewport.evaluate((node) => {
      node.scrollTop = node.scrollHeight;
    });
    await page.waitForTimeout(120);
  }
  await expect(row, `树中找不到行「${name}」（已滚到底）`).toHaveCount(1);
  await row.click();
}

/** 断言容器没有横向裁切（裁掉的部分永远滚不到）。 */
async function expectNoHorizontalClip(target: Locator, label: string): Promise<void> {
  const metrics = await target.evaluate((node) => ({
    clientWidth: node.clientWidth,
    scrollWidth: node.scrollWidth,
  }));
  expect(
    metrics.scrollWidth,
    `${label} 横向被裁：内容 ${metrics.scrollWidth}px > 容器 ${metrics.clientWidth}px`,
  ).toBeLessThanOrEqual(metrics.clientWidth + 1);
}

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  await seedSession(page.request, { id: SESSION_ID, title: "e2e file manager tabs" });
});

test("多页签文件管理器：打开 / 去重 / 切回根页签 / 关闭按邻近回落", async ({ page }) => {
  await openFilesSurface(page);

  // 初始：只有根页签，且根页签没有关闭入口（不可关）。
  await expect(browserTab(page)).toHaveAttribute("aria-selected", "true");
  await expect(tabStrip(page).getByRole("tab")).toHaveCount(1);
  await expect(tabCloseButton(page, "File browser")).toHaveCount(0);
  await expect(tree(page)).toBeVisible();

  // 点击文件 → 追加页签并激活；预览正文可见 = 页签体真的占到了高度。
  await clickTreeRow(page, "notes.txt");
  const notesTab = fileTab(page, "notes.txt");
  await expect(notesTab).toHaveAttribute("aria-selected", "true", { timeout: 15_000 });
  await expect(browserTab(page)).toHaveAttribute("aria-selected", "false");
  await expect(tabStrip(page).getByRole("tab")).toHaveCount(2);
  const viewer = page.getByTestId("text-viewer");
  await expect(viewer).toBeVisible({ timeout: 15_000 });
  await expect(viewer).toContainText(PREVIEW_MARKER);
  // 切到文件页签后树视图让位（同一时刻只有一个页签正文）。
  await expect(tree(page)).toHaveCount(0);

  // 切回根页签：树恢复、文件页签正文让位。
  await browserTab(page).click();
  await expect(browserTab(page)).toHaveAttribute("aria-selected", "true");
  await expect(tree(page)).toBeVisible();
  await expect(page.getByTestId("file-browser-preview")).toHaveCount(0);

  // 重复打开同一文件：只聚焦已有页签，不产生重复项。
  await clickTreeRow(page, "notes.txt");
  await expect(notesTab).toHaveAttribute("aria-selected", "true");
  await expect(tabStrip(page).getByRole("tab")).toHaveCount(2);

  // 再开第二个文件 → 3 个页签。
  await browserTab(page).click();
  await clickTreeRow(page, "entry-001.txt");
  const secondTab = fileTab(page, "entry-001.txt");
  await expect(secondTab).toHaveAttribute("aria-selected", "true", { timeout: 15_000 });
  await expect(tabStrip(page).getByRole("tab")).toHaveCount(3);

  // 关闭活动页签（末位，无右邻）→ 回落到左邻 notes.txt（而非根页签）。
  await tabCloseButton(page, "entry-001.txt").click();
  await expect(tabStrip(page).getByRole("tab")).toHaveCount(2);
  await expect(secondTab).toHaveCount(0);
  await expect(notesTab).toHaveAttribute("aria-selected", "true");
  await expect(viewer).toContainText(PREVIEW_MARKER);

  // 关闭最后一个文件页签 → 回到根页签，树视图恢复。
  await tabCloseButton(page, "notes.txt").click();
  await expect(tabStrip(page).getByRole("tab")).toHaveCount(1);
  await expect(browserTab(page)).toHaveAttribute("aria-selected", "true");
  await expect(tree(page)).toBeVisible();
});

test("页签条与预览区：高度链与横向裁切不变量", async ({ page }) => {
  await openFilesSurface(page);
  await clickTreeRow(page, "notes.txt");
  await expect(page.getByTestId("text-viewer")).toBeVisible({ timeout: 15_000 });

  // 页签条必须贴在面板内、不被裁切（多个页签时标签不压缩、整条横滚）。
  await expectNoHorizontalClip(tabStrip(page), "文件管理器页签条");
  const stripBox = await tabStrip(page).boundingBox();
  expect(stripBox?.height ?? 0, "页签条高度塌陷到不可见").toBeGreaterThan(20);
  const surfaceBox = await page.getByTestId("file-browser-surface").boundingBox();
  expect(surfaceBox, "面板没有布局盒").not.toBeNull();
  expect(
    (stripBox?.y ?? 0) - (surfaceBox?.y ?? 0),
    "页签条没有贴在面板顶部（被顶出可视区）",
  ).toBeLessThan(8);

  // 预览正文填满剩余高度：高度塌陷（页面报过的「预览看不到」）在这里失败。
  const viewerBox = await page.getByTestId("text-viewer").boundingBox();
  expect(viewerBox?.height ?? 0, "预览区高度塌陷到不可读").toBeGreaterThan(40);
  const viewport = page.viewportSize();
  expect(
    (viewerBox?.y ?? 0) + (viewerBox?.height ?? 0),
    "预览区被顶到视口下方之外",
  ).toBeLessThanOrEqual((viewport?.height ?? 0) + 1);
});
