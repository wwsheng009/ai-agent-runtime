import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedSession } from "./support";

// P2-6 子片 1 e2e：侧栏会话排序的双模式、手动顺序账目与组内拖拽重排。
//
// 口径（与实现一致）：
// - 「最近更新」恒按更新时间派生，不读也不写手动账目；
// - 「手动」有账目按账目，无账目按最近更新呈现（账目只在拖拽时建立）；
// - 排序模式走设置域持久化，手动账目走浏览器本地存储；跨组拖拽不接收落点。

const SESSION_ORDER_STORAGE_KEY = "ai-agent-runtime.workspace.session-order";

// 三个 id 必须在前 10 位内彼此可区分（行定位按 id 前 10 位做子串匹配）。
const IDS = ["e2e-order1", "e2e-order2", "e2e-order3"] as const;

async function gotoWorkspace(page: Page) {
  await page.goto("/workspace");
  await expect(page.locator(".app-chat-input")).toBeVisible({ timeout: 30_000 });
}

function sessionsSection(page: Page) {
  return page.locator("section").filter({
    has: page.getByRole("button", { name: /^Sessions \d+$/ }),
  });
}

/** 行容器（拖拽属性宿主）是行按钮的父节点。 */
function sessionRow(page: Page, id: string) {
  return sessionsSection(page)
    .getByRole("button", { name: new RegExp(id.slice(0, 10)) })
    .locator("xpath=..");
}

function orderControl(page: Page) {
  return page.getByRole("group", { name: "Session ordering" });
}

/** 按纵向位置读出可见顺序（与 DOM 结构解耦，只认真实呈现）。 */
async function visibleOrder(page: Page): Promise<string[]> {
  const measured = await Promise.all(
    IDS.map(async (id) => {
      const box = await sessionRow(page, id).boundingBox();
      return { id, y: box?.y ?? Number.POSITIVE_INFINITY };
    }),
  );
  return measured.sort((left, right) => left.y - right.y).map((item) => item.id);
}

async function storedAccounts(page: Page): Promise<unknown[]> {
  const raw = await page.evaluate(
    (key) => window.localStorage.getItem(key),
    SESSION_ORDER_STORAGE_KEY,
  );
  const parsed = (raw ? JSON.parse(raw) : {}) as {
    accounts?: Record<string, unknown>;
  };
  return Object.values(parsed.accounts ?? {});
}

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  for (const id of IDS) {
    await seedSession(page.request, { id });
  }
});

test("排序控件默认最近更新，切到手动后行可拖且偏好刷新后保留", async ({
  page,
}) => {
  await gotoWorkspace(page);

  const updated = orderControl(page).getByRole("button", {
    name: "Last updated",
  });
  const manual = orderControl(page).getByRole("button", { name: "Manual" });
  await expect(updated).toHaveAttribute("aria-pressed", "true");
  await expect(manual).toHaveAttribute("aria-pressed", "false");
  // 最近更新模式不渲染拖拽 affordance（避免呈现不存在的可拖语义）。
  await expect(sessionRow(page, IDS[0])).toHaveAttribute("draggable", "false");

  await manual.click();
  await expect(manual).toHaveAttribute("aria-pressed", "true");
  await expect(sessionRow(page, IDS[0])).toHaveAttribute("draggable", "true");

  await page.reload();
  await expect(page.locator(".app-chat-input")).toBeVisible({ timeout: 30_000 });
  await expect(
    orderControl(page).getByRole("button", { name: "Manual" }),
  ).toHaveAttribute("aria-pressed", "true");
  await expect(sessionRow(page, IDS[2])).toHaveAttribute("draggable", "true");
});

test("手动模式拖拽重排：顺序落到浏览器账目、播报结果、刷新后保持", async ({
  page,
}) => {
  await gotoWorkspace(page);
  await orderControl(page).getByRole("button", { name: "Manual" }).click();

  const baseline = await visibleOrder(page);
  expect(baseline).toHaveLength(IDS.length);
  const moved = baseline[baseline.length - 1];
  const head = baseline[0];
  const expected = [moved, ...baseline.slice(0, -1)];

  // 拖到首行上半段 = 落到该行之前。
  await sessionRow(page, moved).dragTo(sessionRow(page, head), {
    targetPosition: { x: 24, y: 4 },
  });

  await expect
    .poll(() => visibleOrder(page))
    .toEqual([moved, ...baseline.slice(0, -1)]);
  await expect(
    page.getByTestId("sidebar-session-order-announcement"),
    // mock 会话的标题即会话 id（`seedSession` 不传 title 时 mock 记录 title = id），
    // 因此播报里出现的是裸 id。
  ).toContainText(moved);
  expect(await storedAccounts(page)).toEqual([expected]);

  await page.reload();
  await expect(page.locator(".app-chat-input")).toBeVisible({ timeout: 30_000 });
  await expect.poll(() => visibleOrder(page)).toEqual(expected);
});

test("切回最近更新不读手动账目（账目保留，切回手动后恢复）", async ({
  page,
}) => {
  await gotoWorkspace(page);
  const orderControlLocator = orderControl(page);
  await orderControlLocator.getByRole("button", { name: "Manual" }).click();

  const baseline = await visibleOrder(page);
  const moved = baseline[baseline.length - 1];
  await sessionRow(page, moved).dragTo(sessionRow(page, baseline[0]), {
    targetPosition: { x: 24, y: 4 },
  });
  const manualOrder = [moved, ...baseline.slice(0, -1)];
  await expect.poll(() => visibleOrder(page)).toEqual(manualOrder);

  await orderControlLocator.getByRole("button", { name: "Last updated" }).click();
  await expect.poll(() => visibleOrder(page)).toEqual(baseline);
  await expect(sessionRow(page, moved)).toHaveAttribute("draggable", "false");
  // 账目未被删除：只是在「最近更新」模式下不参与呈现。
  expect(await storedAccounts(page)).toEqual([manualOrder]);

  await orderControlLocator.getByRole("button", { name: "Manual" }).click();
  await expect.poll(() => visibleOrder(page)).toEqual(manualOrder);
});
