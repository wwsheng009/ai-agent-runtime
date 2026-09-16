import type { Locator, Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedSession } from "./support";

// P0-P4 e2e：右侧栏「文件浏览器 / Git 变更面」的可见性契约。
//
// 四个用户可见症状同源，且都只在真实浏览器里显形（jsdom 不计算布局）：
//  - 目录树没有滚动条 / 预览区看不到 / git 变更列表没有滚动条：高度链断开时容器高度
//    退化为内容高度，`overflow-auto` 类名照旧在，但滚动永远不会发生；
//  - 最后更新时间恒为 1970-01-21：Unix 秒被当成毫秒解释。
// 因此这里用「必然溢出」的夹具（根层 83 项 / 61 个变更）断言滚动真的发生，
// 并断言预览区在可视范围内真的有高度。

const SESSION_ID = "e2e-files-session";
// 与 mock-server.mjs 的 E2E_MTIME_SECONDS 同一口径（后端契约：Unix 秒）。
const FIXTURE_MTIME_SECONDS = 1758000000;
const PREVIEW_MARKER = "E2E_PREVIEW_TEXT_OK";

const composer = (page: Page) => page.locator(".app-chat-input");
const filesTab = (page: Page) => page.getByTestId("artifact-panel-tab-files");
const gitTab = (page: Page) => page.getByTestId("artifact-panel-tab-git");

/** 选中真实会话：新建线程态下右侧栏整体不渲染（`rightRailOpen = !isNewThread && ...`）。 */
async function openSession(page: Page): Promise<void> {
  await page.goto(`/workspace/chats/${SESSION_ID}`);
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });
}

/**
 * 展开右侧栏。
 *
 * 开关状态只认 `aria-pressed`：面板是懒加载面，用「页签当前是否可见」判断会把
 * 尚未挂载的已展开右栏当成关闭，一点之下反而关掉它（本用例第一版即因此失败）。
 */
async function openRightRail(page: Page): Promise<void> {
  const toggle = page.getByTestId("topbar-toggle-right-rail");
  await expect(toggle).toBeVisible({ timeout: 30_000 });
  if ((await toggle.getAttribute("aria-pressed")) !== "true") {
    await toggle.click();
  }
  await expect(toggle).toHaveAttribute("aria-pressed", "true", { timeout: 15_000 });
  await expect(filesTab(page)).toBeVisible({ timeout: 15_000 });
}

/**
 * 断言元素真的可滚动：内容高于可视区，且 scrollTop 能被改写。
 * 只断言 `overflow-auto` 类名会漏掉「高度链断开、容器高度=内容高度」这一整类缺陷。
 */
async function expectScrollable(target: Locator): Promise<void> {
  await expect(target).toBeVisible();
  const metrics = await target.evaluate((node) => ({
    clientHeight: node.clientHeight,
    scrollHeight: node.scrollHeight,
  }));
  expect(
    metrics.scrollHeight,
    `内容高度 ${metrics.scrollHeight} 未超过可视高度 ${metrics.clientHeight}，不会出现滚动条`,
  ).toBeGreaterThan(metrics.clientHeight);
  const scrollTop = await target.evaluate((node) => {
    node.scrollTop = node.scrollHeight;
    return node.scrollTop;
  });
  expect(scrollTop, "scrollTop 无法改写，内容被容器裁掉而不是滚动").toBeGreaterThan(0);
}

/**
 * 断言容器被显式样式化为「始终可见的滚动条」。
 *
 * `expectScrollable` 只证明「能滚」；用户报的「缺滚动条」是视觉问题：容器若没有
 * `app-scrollbar`（thin + 稳定 gutter + 自带 thumb 配色），滚动条会退化成 overlay 形态
 * ——不悬停、不滚动时完全不可见，用户看到的就是「没有滚动条」。
 */
async function expectStyledScrollbar(target: Locator, label: string): Promise<void> {
  const style = await target.evaluate((node) => {
    const computed = getComputedStyle(node);
    return {
      overflowY: computed.overflowY,
      scrollbarWidth: computed.getPropertyValue("scrollbar-width").trim(),
      scrollbarColor: computed.getPropertyValue("scrollbar-color").trim(),
      hasClass: node.classList.contains("app-scrollbar"),
    };
  });
  expect(
    ["auto", "scroll"],
    `${label} 的 overflow-y=${style.overflowY}，不是可滚动容器`,
  ).toContain(style.overflowY);
  expect(style.hasClass, `${label} 缺少 app-scrollbar：overlay 滚动条不悬停即不可见`).toBe(true);
  expect(style.scrollbarWidth, `${label} 未声明 scrollbar-width: thin`).toBe("thin");
  expect(style.scrollbarColor, `${label} 未声明 scrollbar-color（thumb 无固定配色）`).not.toBe(
    "auto",
  );
}

/**
 * 断言容器没有横向裁切：`overflow-hidden` 的容器里 `scrollWidth > clientWidth`，
 * 意味着右侧内容被切掉且永远滚不到（尾部控件点不到、时间列看不见）。
 * 隐式 `auto` 栅格列是这类溢出的经典来源——轨道会吃内容最小宽度而不收缩。
 */
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

/**
 * 断言元素的可视盒落在视口之内。
 *
 * `boundingBox()` 只证明元素有盒子；父级是 `overflow-hidden` 时，被顶到视口外的元素
 * 依然有盒子、依然「visible」——用户看到的却是空白。高度链断开时预览区正是这样消失的。
 */
async function expectInsideViewport(
  target: Locator,
  page: Page,
  label: string,
): Promise<void> {
  const box = await target.boundingBox();
  expect(box, `${label} 没有布局盒`).not.toBeNull();
  const viewport = page.viewportSize();
  const width = viewport?.width ?? 0;
  const height = viewport?.height ?? 0;
  const bounds = (value: number) => Math.round(value);
  // 1px 容差：亚像素布局下边界可能落在 1439.5 这类位置。
  expect(box!.y, `${label} 被顶到视口上方之外（top=${bounds(box!.y)}）`).toBeGreaterThanOrEqual(
    -1,
  );
  expect(
    box!.y + box!.height,
    `${label} 被顶到视口下方之外（bottom=${bounds(box!.y + box!.height)} > ${height}）`,
  ).toBeLessThanOrEqual(height + 1);
  expect(box!.x, `${label} 被推出视口左侧之外（left=${bounds(box!.x)}）`).toBeGreaterThanOrEqual(
    -1,
  );
  expect(
    box!.x + box!.width,
    `${label} 被推出视口右侧之外（right=${bounds(box!.x + box!.width)} > ${width}）`,
  ).toBeLessThanOrEqual(width + 1);
}

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  await seedSession(page.request, { id: SESSION_ID, title: "e2e files" });
});

test("文件浏览器：目录树可滚动、文本可预览、mtime 不是 1970-01-21", async ({ page }) => {
  await openSession(page);
  await openRightRail(page);
  await filesTab(page).click();

  // 根层 83 项：树必须在面板内滚动，而不是把面板内容顶高。
  const tree = page.getByTestId("file-browser-tree");
  await expectScrollable(tree);
  await expectStyledScrollbar(tree, "文件目录树");

  await expectNoHorizontalClip(page.getByTestId("file-browser-surface"), "文件浏览器面板");
  await expectInsideViewport(tree, page, "文件目录树");

  const notesRow = tree.locator('[role="treeitem"]').filter({ hasText: "notes.txt" });
  await expect(notesRow).toBeVisible();

  // 期望值由同一个 Unix 秒推导（本地时区固定 Asia/Shanghai，见 playwright.config.ts），
  // 不把「1970-01-21」这类错误结果写进期望里。
  const expectedMtime = new Date(FIXTURE_MTIME_SECONDS * 1000)
    .toLocaleString("sv-SE", { timeZone: "Asia/Shanghai" })
    .slice(0, 16);
  const mtime = notesRow.getByTestId("file-tree-mtime");
  await expect(mtime).toHaveText(expectedMtime);
  await expect(mtime).not.toContainText("1970");

  await notesRow.click();

  // 文本预览：内容可见 = 预览区没有被内容高度顶出可视范围（此前高度退化为 0）。
  const preview = page.getByTestId("file-browser-preview");
  await expect(preview).toBeVisible();
  const viewer = page.getByTestId("text-viewer");
  await expect(viewer).toBeVisible({ timeout: 15_000 });
  await expect(viewer).toContainText(PREVIEW_MARKER);
  const box = await viewer.boundingBox();
  expect(box?.height ?? 0, "预览区高度塌陷到不可读").toBeGreaterThan(40);
  await expectInsideViewport(viewer, page, "文本预览正文");
  await expectInsideViewport(preview, page, "文件预览区");
});

test("Git 变更面：变更列表可滚动", async ({ page }) => {
  await openSession(page);
  await openRightRail(page);
  await gitTab(page).click();

  // 61 个变更：列表必须在自身容器内滚动。
  await expect(page.getByText("src/module-001.ts")).toBeVisible({ timeout: 15_000 });
  const rows = page.getByTestId("git-change-list-rows");
  await expectScrollable(rows);
  await expectStyledScrollbar(rows, "Git 变更列表");
  await expectInsideViewport(rows, page, "Git 变更列表");

  // 同一条不变量再压到 Git 面：面板内任何元素都不得越过 `overflow-hidden` 被裁掉，
  // 否则行尾的状态标记/统计数字会「看起来不存在」。
  await expectNoHorizontalClip(page.getByTestId("git-surface"), "Git 面板");
  await expectNoHorizontalClip(page.getByTestId("git-change-list"), "Git 变更列表容器");
});
