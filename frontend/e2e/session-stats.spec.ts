import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedRuntimeEvents, seedSession } from "./support";

// P2-1A e2e：侧栏会话统计（GET /api/runtime/sessions/stats）。
// 覆盖链路：默认用户口径聚合（含 camelCase 的 totalMessages）→ 切换侧栏用户重取
// → 端点不可用（503）如实提示且不渲染任何伪造计数 → 恢复后重试可见真实计数；
// 以及 500 按真实失败呈现、不与降级混同。

const ALICE_ACTIVE = "e2e-p2-stats-alice-active";
const ALICE_ARCHIVED = "e2e-p2-stats-alice-archived";
const BOB_ACTIVE = "e2e-p2-stats-bob-active";

const STATS_ROUTE = /\/api\/runtime\/sessions\/stats(?:\?|$)/;

async function gotoWorkspace(page: Page) {
  await page.goto("/workspace");
  await expect(page.locator(".app-chat-input")).toBeVisible({ timeout: 30_000 });
}

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  await seedSession(page.request, {
    id: ALICE_ACTIVE,
    title: "Alice support thread",
    state: "active",
    userId: "alice",
    tags: ["support", "billing"],
  });
  await seedSession(page.request, {
    id: ALICE_ARCHIVED,
    title: "Alice archived thread",
    state: "archived",
    userId: "alice",
    tags: ["support"],
  });
  await seedSession(page.request, {
    id: BOB_ACTIVE,
    title: "Bob billing thread",
    state: "active",
    userId: "bob",
    tags: ["billing"],
  });
  // mock 的 totalMessages 由事件表推导：给 alice 的三条事件，用于验证
  // camelCase 的 totalMessages 字段确实接线（而不是恒为 0 的假通过）。
  await seedRuntimeEvents(page.request, ALICE_ACTIVE, [
    { type: "chat.sse.message_delta", payload: { delta: "a" } },
    { type: "chat.sse.message_delta", payload: { delta: "b" } },
    { type: "chat.sse.message_completed", payload: { content: "ab" } },
  ]);
});

test("侧栏统计按默认用户聚合，计数与 seed 一致", async ({ page }) => {
  const requestedUserIds: string[] = [];
  await page.route(STATS_ROUTE, async (route) => {
    requestedUserIds.push(
      new URL(route.request().url()).searchParams.get("user_id") ?? "",
    );
    await route.continue();
  });

  await gotoWorkspace(page);

  await expect(page.getByTestId("session-stats-summary")).toBeVisible({
    timeout: 15_000,
  });
  // 用户清单就绪后自动落到有会话的默认用户（alice），统计与列表同口径。
  await expect(page.getByTestId("session-stats-chip-total")).toHaveText("2 sessions");
  await expect(page.getByTestId("session-stats-chip-active")).toHaveText("Active 1");
  await expect(page.getByTestId("session-stats-chip-archived")).toHaveText(
    "Archived 1",
  );
  await expect(page.getByTestId("session-stats-chip-totalMessages")).toHaveText(
    "3 messages",
  );
  // 0 计数计数不渲染 chip：不制造噪声，也不伪造不存在的数据。
  await expect(page.getByTestId("session-stats-chip-idle")).toHaveCount(0);
  await expect(page.getByTestId("session-stats-chip-closed")).toHaveCount(0);

  await expect.poll(() => requestedUserIds.at(-1)).toBe("alice");
});

test("切换侧栏用户后统计按新用户重取", async ({ page }) => {
  await gotoWorkspace(page);
  await expect(page.getByTestId("session-stats-chip-total")).toHaveText("2 sessions");

  await page.getByTitle("bob", { exact: true }).click();

  await expect(page.getByTestId("session-stats-chip-total")).toHaveText("1 sessions");
  await expect(page.getByTestId("session-stats-chip-active")).toHaveText("Active 1");
  await expect(page.getByTestId("session-stats-chip-archived")).toHaveCount(0);
  await expect(page.getByTestId("session-stats-chip-totalMessages")).toHaveCount(0);
});

test("统计端点 503 时如实提示且不伪造计数，恢复后重试可见真实计数", async ({
  page,
}) => {
  let unavailable = true;
  await page.route(STATS_ROUTE, async (route) => {
    if (!unavailable) {
      await route.continue();
      return;
    }
    await route.fulfill({
      status: 503,
      contentType: "application/json",
      body: JSON.stringify({ error: "session statistics unavailable" }),
    });
  });

  await gotoWorkspace(page);

  await expect(page.getByTestId("session-stats-unavailable")).toBeVisible({
    timeout: 15_000,
  });
  await expect(page.getByTestId("session-stats-unavailable")).toContainText(
    "Session stats unavailable",
  );
  // 降级不等于 0 个会话：不渲染摘要与任何 chip。
  await expect(page.getByTestId("session-stats-summary")).toHaveCount(0);
  await expect(page.getByTestId("session-stats-chip-total")).toHaveCount(0);

  unavailable = false;
  await page.getByTestId("session-stats-retry").click();

  await expect(page.getByTestId("session-stats-chip-total")).toHaveText("2 sessions");
  await expect(page.getByTestId("session-stats-unavailable")).toHaveCount(0);
});

test("统计端点 500 按真实失败呈现，不与不可用降级混同", async ({ page }) => {
  await page.route(STATS_ROUTE, async (route) => {
    await route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({ error: "stats exploded" }),
    });
  });

  await gotoWorkspace(page);

  await expect(page.getByTestId("session-stats-error")).toBeVisible({
    timeout: 15_000,
  });
  await expect(page.getByTestId("session-stats-error")).toContainText(
    "Failed to load session stats",
  );
  await expect(page.getByTestId("session-stats-unavailable")).toHaveCount(0);
  await expect(page.getByTestId("session-stats-chip-total")).toHaveCount(0);
});
