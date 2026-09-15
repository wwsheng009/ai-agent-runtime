import type { Locator, Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedSession } from "./support";

// 2026-09-15 样式回归（第二轮）：目录组头是**第一层级元素**，必须顶到侧栏内容盒左侧。
//
// 口径（用户可见的几何不变式，不读实现类名，故对重构免疫）：
// - 目录组容器 `[data-testid="sidebar-session-group"]` 左缘 == 分区左缘；
// - 组头按钮 `[data-testid="sidebar-session-group-drop"]` 左缘 == 分区左缘 + 4px
//   （组头自身内衬 `px-1`，与分区标题行同一口径）——即组头不再是被缩进的子级；
// - 组内第一层会话行（`role="treeitem"` + `data-depth="0"`）左缘同样 == 分区左缘。
//
// 被锁死的回归：会话树曾整体包在 `ml-3`（12px）里，目录分组模式再叠加
// `border-l` + `pl-2`（8px），组头因此离侧栏内容盒左缘 20px。
//
// 说明：分组视图默认档即「按目录」（见 session-grouping.spec.ts 的默认断言），
// 目录组由会话元数据 `context.workspace_path` 派生，无需注册表端点。
// 分组容器用 `:has()` 反查所在分区，因此本用例不依赖任何 UI 文案（en-US 即可）。

/** 组容器左缘允许的误差（亚像素渲染）。 */
const EDGE_TOLERANCE = 1;
/** 组头按钮自身的内衬（`directory-group-header.tsx` 的 `px-1`）。 */
const HEADER_INSET = 4;

const ALPHA_RAW_PATH = "E:\\work\\alpha";
const ALPHA_GROUP_KEY = "E:/work/alpha";
const BETA_RAW_PATH = "E:\\work\\beta";
const BETA_GROUP_KEY = "E:/work/beta";

const ALPHA_SESSION_ID = "e2e-flush-1";
const BETA_SESSION_ID = "e2e-flush-2";

/** 含目录组的合并分区（工作目录 + 会话）。 */
function sessionsSection(page: Page): Locator {
  return page.locator('section:has([data-testid="sidebar-session-group"])');
}

function groupContainer(page: Page, key: string): Locator {
  return sessionsSection(page).locator(
    `[data-testid="sidebar-session-group"]:has([data-testid="sidebar-session-group-drop"][title="${key}"])`,
  );
}

function groupHeader(page: Page, key: string): Locator {
  return sessionsSection(page).locator(
    `[data-testid="sidebar-session-group-drop"][title="${key}"]`,
  );
}

/** 元素左缘（不可见即失败，避免 null 参与算术后静默通过）。 */
async function leftEdge(locator: Locator): Promise<number> {
  const box = await locator.boundingBox();
  if (!box) {
    throw new Error("元素不可见，量不到左缘");
  }
  return box.x;
}

/** 目录组默认折叠，但持有当前选中会话的组会自动展开：只在无行时点一次组头。
 *
 * 与 `session-grouping.spec.ts` 的 `expandGroup` 同口径——无条件 click 会把
 * 已展开的组**收起**，把「量不到行」伪装成产品缺陷。 */
async function ensureGroupExpanded(page: Page, key: string): Promise<Locator> {
  const rows = groupContainer(page, key).locator('[role="treeitem"]');
  if ((await rows.count()) === 0) {
    await groupHeader(page, key).click();
  }
  await expect(rows.first()).toBeVisible();
  return rows;
}

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  // 两个不同工作目录的会话：分组视图按元数据分桶，产生两个目录组。
  await seedSession(page.request, {
    id: ALPHA_SESSION_ID,
    title: "flush alpha",
    metadata: { context: { workspace_path: ALPHA_RAW_PATH } },
  });
  await seedSession(page.request, {
    id: BETA_SESSION_ID,
    title: "flush beta",
    metadata: { context: { workspace_path: BETA_RAW_PATH } },
  });
});

test("目录组头与组内会话行顶到侧栏内容盒左侧，没有嵌套缩进", async ({
  page,
}) => {
  await page.goto("/workspace");
  await expect(page.locator(".app-chat-input")).toBeVisible({ timeout: 30_000 });

  await expect(groupHeader(page, ALPHA_GROUP_KEY)).toBeVisible();
  await expect(groupHeader(page, BETA_GROUP_KEY)).toBeVisible();

  const sectionLeft = await leftEdge(sessionsSection(page));

  for (const key of [ALPHA_GROUP_KEY, BETA_GROUP_KEY]) {
    // 组容器：与分区同一条左基准线（回归前是 +20px）。
    const containerLeft = await leftEdge(groupContainer(page, key));
    expect(
      Math.abs(containerLeft - sectionLeft),
      `目录组 ${key} 的容器左缘应贴齐分区左缘`,
    ).toBeLessThanOrEqual(EDGE_TOLERANCE);

    // 组头：只剩自身的 4px 内衬，与分区标题行同口径。
    const headerLeft = await leftEdge(groupHeader(page, key));
    expect(
      Math.abs(headerLeft - (sectionLeft + HEADER_INSET)),
      `目录组 ${key} 的组头左缘应是「分区左缘 + px-1」`,
    ).toBeLessThanOrEqual(EDGE_TOLERANCE);
  }

  // 组内第一层会话行：与组头同一条左基准线（不继承任何嵌套缩进）。
  await ensureGroupExpanded(page, ALPHA_GROUP_KEY);
  const firstRow = groupContainer(page, ALPHA_GROUP_KEY)
    .locator('[role="treeitem"][data-depth="0"]')
    .first();
  const rowLeft = await leftEdge(firstRow);
  expect(
    Math.abs(rowLeft - sectionLeft),
    "组内第一层会话行左缘应贴齐分区左缘",
  ).toBeLessThanOrEqual(EDGE_TOLERANCE);
});
