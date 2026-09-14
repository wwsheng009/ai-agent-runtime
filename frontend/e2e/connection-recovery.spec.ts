import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedSession } from "./support";

// P1-8 e2e：直连 `/api/agent/chat` 流失败后，统一「连接状态」必须可见
// （顶栏 + 消息流尾），并给出与自动重连共用的手动重试入口；手动重试复用
// 会话运行时流入口（按本地 last seq 拉齐），不重发 chat 请求，因此不会
// 产生重复回合请求。

const composer = (page: Page) => page.locator(".app-chat-input");

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
});

test("直连 chat 流失败可见断线状态，手动重试不产生重复请求", async ({
  page,
}) => {
  await seedSession(page.request);

  let chatRequests = 0;
  let streamRequests = 0;

  // 直连 chat 流：立即失败（模拟通道断开）。
  await page.route("**/api/agent/chat*", async (route) => {
    chatRequests += 1;
    await route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({ error: "agent chat stream offline" }),
    });
  });

  // 会话运行时流：保持健康（空流 + 等待重连），用于证明断线状态并非来自会话流。
  await page.route(
    "**/api/runtime/sessions/e2e-session-1/runtime/stream*",
    async (route) => {
      streamRequests += 1;
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: "",
      });
    },
  );

  await page.goto("/workspace/chats/e2e-session-1");
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });

  await composer(page).fill("recover direct chat");
  await composer(page).press("Control+Enter");

  // 直连 chat 失败 → 线程 transport=error → 统一连接状态收敛为 offline。
  const badge = page.locator('[data-connection-status="offline"]').first();
  await expect(badge).toBeVisible({ timeout: 20_000 });

  const retry = badge.locator("button");
  await expect(retry).toBeVisible();

  const streamsBeforeRetry = streamRequests;
  await retry.click();

  // 手动重试走会话运行时流（本地 last seq 拉齐）→ 立即发起新的 stream 请求。
  await expect
    .poll(() => streamRequests, { timeout: 10_000 })
    .toBeGreaterThan(streamsBeforeRetry);
  // 且不重发直连 chat 请求：整场只应有一次 agent/chat 提交。
  expect(chatRequests).toBe(1);
});
