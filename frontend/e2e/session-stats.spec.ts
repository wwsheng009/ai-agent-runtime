import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedRuntimeEvents, seedSession } from "./support";

// P2-1A e2e：侧栏会话统计（GET /api/runtime/sessions/stats）。
// 覆盖链路：默认用户口径聚合（含 camelCase 的 totalMessages）→ 既有持久化用户口径
// → 端点不可用（503）如实提示且不渲染任何伪造计数 → 恢复后重试可见真实计数；
// 以及 500 按真实失败呈现、不与降级混同。
//
// 2026-09-15：侧栏「会话用户」卡片下线（用户维度不再有可切换的浏览入口），
// 故用户口径用例改走**仅存入口**——localStorage 的已选用户
// （`runtime-sessions-data/storage.ts`），并顺带守住「侧栏不再渲染用户卡片」。

const ALICE_ACTIVE = "e2e-p2-stats-alice-active";
const ALICE_ARCHIVED = "e2e-p2-stats-alice-archived";
const BOB_ACTIVE = "e2e-p2-stats-bob-active";

const STATS_ROUTE = /\/api\/runtime\/sessions\/stats(?:\?|$)/;

async function gotoWorkspace(page: Page) {
  await page.goto("/workspace");
  await expect(page.locator(".app-chat-input")).toBeVisible({ timeout: 30_000 });
}

/**
 * 段头 ⋯ 面板（2026-09-15 布局优化）：统计摘要与刷新按钮从段内常驻工具条收敛进该面板，
 * 因此断言前必须先开面板。幂等：面板已开时不再点触发键（再点一次会把面板关掉）。
 */
async function openSectionMenu(page: Page) {
  const panel = page.getByTestId("sidebar-directories-menu-panel");
  if (!(await panel.isVisible())) {
    await page.getByTestId("sidebar-directories-menu-trigger").click();
  }
  await expect(panel).toBeVisible();
}

/** 会话用户的既有持久化入口（键与 `runtime-sessions-data/storage.ts` 对齐）。 */
const STORED_SESSION_USER_KEY = "workspace.runtime.sessions.selectedUser";

async function seedStoredSessionUser(page: Page, userId: string) {
  await page.addInitScript(
    ([key, value]) => window.localStorage.setItem(key, value),
    [STORED_SESSION_USER_KEY, userId] as const,
  );
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

  await openSectionMenu(page);
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

test("已存用户决定统计口径，且侧栏不再渲染用户卡片", async ({ page }) => {
  const requestedUserIds: string[] = [];
  await page.route(STATS_ROUTE, async (route) => {
    requestedUserIds.push(
      new URL(route.request().url()).searchParams.get("user_id") ?? "",
    );
    await route.continue();
  });
  await seedStoredSessionUser(page, "bob");

  await gotoWorkspace(page);
  await openSectionMenu(page);

  await expect(page.getByTestId("session-stats-chip-total")).toHaveText("1 sessions");
  await expect(page.getByTestId("session-stats-chip-active")).toHaveText("Active 1");
  await expect(page.getByTestId("session-stats-chip-archived")).toHaveCount(0);
  await expect(page.getByTestId("session-stats-chip-totalMessages")).toHaveCount(0);
  await expect.poll(() => requestedUserIds.at(-1)).toBe("bob");

  // 旧版用户卡片以 `title=<userId>` 暴露点击入口（alice / bob 两张）：现已下线。
  await expect(page.getByTitle("alice", { exact: true })).toHaveCount(0);
  await expect(page.getByTitle("bob", { exact: true })).toHaveCount(0);
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

  await openSectionMenu(page);
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

  await openSectionMenu(page);
  await expect(page.getByTestId("session-stats-error")).toBeVisible({
    timeout: 15_000,
  });
  await expect(page.getByTestId("session-stats-error")).toContainText(
    "Failed to load session stats",
  );
  await expect(page.getByTestId("session-stats-unavailable")).toHaveCount(0);
  await expect(page.getByTestId("session-stats-chip-total")).toHaveCount(0);
});
