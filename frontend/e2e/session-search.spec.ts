import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedSession } from "./support";

// P2-1A e2e：会话元数据检索弹层（POST /api/runtime/sessions/search）。
// 覆盖链路：侧栏触发按钮 → 服务端过滤（user / tags AND / state）→ 结果行选择跳转，
// 以及后端不可用（503）时如实提示降级、不落地伪造结果。

const ALICE_SUPPORT = "e2e-p2-search-alice-support";
const ALICE_ARCHIVED = "e2e-p2-search-alice-archived";
const BOB_BILLING = "e2e-p2-search-bob-billing";

async function gotoWorkspace(page: Page) {
  await page.goto("/workspace");
  await expect(page.locator(".app-chat-input")).toBeVisible({ timeout: 30_000 });
}

async function openSearchDialog(page: Page) {
  await page.getByTestId("session-search-trigger").click();
  await expect(page.getByTestId("session-search-dialog")).toBeVisible({
    timeout: 15_000,
  });
}

async function seedSearchFixtures(page: Page) {
  await seedSession(page.request, {
    id: ALICE_SUPPORT,
    title: "Billing escalation",
    state: "active",
    userId: "alice",
    tags: ["support", "billing"],
  });
  await seedSession(page.request, {
    id: ALICE_ARCHIVED,
    title: "Archived support thread",
    state: "archived",
    userId: "alice",
    tags: ["support"],
  });
  await seedSession(page.request, {
    id: BOB_BILLING,
    title: "Invoices for Q3",
    state: "active",
    userId: "bob",
    tags: ["billing"],
  });
}

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  await seedSearchFixtures(page);
});

test("标签按 AND + 状态过滤由服务端执行，结果与计数一致", async ({ page }) => {
  const bodies: Array<Record<string, unknown>> = [];
  await page.route("**/api/runtime/sessions/search", async (route) => {
    bodies.push((route.request().postDataJSON() ?? {}) as Record<string, unknown>);
    await route.continue();
  });

  await gotoWorkspace(page);
  await openSearchDialog(page);

  await page.getByLabel("Tags").fill("support, billing");
  await page.getByRole("button", { name: "Search", exact: true }).click();

  const rows = page.getByTestId("session-search-row");
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("Billing escalation");
  await expect(rows.first()).toContainText("Tags: support, billing");
  await expect(page.getByText("1 session(s) matched")).toBeVisible();
  // 请求体走 snake_case，limit/offset 由前端收口（后端不设默认上限）。
  expect(bodies.at(-1)).toMatchObject({
    tags: ["support", "billing"],
    limit: 50,
    offset: 0,
  });

  // 只留 support：AND 放宽后命中的 archived 会话也如实返回（不做状态隐藏）。
  await page.getByLabel("Tags").fill("support");
  await page.getByRole("button", { name: "Search", exact: true }).click();
  await expect(rows).toHaveCount(2);
  await expect(page.getByText("2 session(s) matched")).toBeVisible();
  await expect(rows.filter({ hasText: "Archived support thread" })).toContainText(
    "Archived",
  );

  // 叠加状态过滤：只剩 active 的 support 会话。
  await page.getByLabel("State").selectOption("active");
  await page.getByRole("button", { name: "Search", exact: true }).click();
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("Billing escalation");
  expect(bodies.at(-1)).toMatchObject({ tags: ["support"], state: "active" });
});

test("按用户过滤后选择结果行会切换到该会话", async ({ page }) => {
  await gotoWorkspace(page);
  await openSearchDialog(page);

  // 用户清单来自 GET /api/runtime/sessions/users（mock 由 seed 的会话派生）。
  await page.getByLabel("User").selectOption("bob");
  await page.getByRole("button", { name: "Search", exact: true }).click();

  const rows = page.getByTestId("session-search-row");
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("Invoices for Q3");
  await expect(page.getByText("1 session(s) matched")).toBeVisible();

  await page.getByRole("button", { name: "Open session Invoices for Q3" }).click();
  await expect(page).toHaveURL(new RegExp(`/workspace/sessions/${BOB_BILLING}`));
  await expect(page.getByTestId("session-search-dialog")).toHaveCount(0);
});

test("服务端检索不可用（503）时如实提示且不返回结果", async ({ page }) => {
  await page.route("**/api/runtime/sessions/search", async (route) => {
    await route.fulfill({
      status: 503,
      contentType: "application/json",
      body: JSON.stringify({ error: "session search unavailable" }),
    });
  });

  await gotoWorkspace(page);
  await openSearchDialog(page);

  await page.getByRole("button", { name: "Search", exact: true }).click();

  const error = page.getByTestId("session-search-error");
  await expect(error).toBeVisible();
  await expect(error).toContainText("Server-side metadata search is unavailable");
  await expect(error).toContainText("503");
  // 降级不等于空结果：不渲染结果行，也不把失败伪装成「0 条命中」。
  await expect(page.getByTestId("session-search-row")).toHaveCount(0);
  await expect(page.getByTestId("session-search-empty")).toHaveCount(0);
});
