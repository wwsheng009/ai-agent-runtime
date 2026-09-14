import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedSession } from "./support";

// P1-9 e2e：侧栏会话行的 Fork 与非破坏删除。
// Fork 语义（2026-09 分支能力升级，方案 §5.1 入口 2 / §5.3）：侧栏入口 =
// **整会话分支** —— `POST /sessions/{id}/branch` 且不带锚点（锚点缺省 = 会话末尾），
// 历史前缀由服务端复制进新会话、源会话零改动；标题带本地化后缀 + 继承工作目录。
// 删除仅移除会话引用，列表随快照刷新回落，不连带目录数据。

const SOURCE_ID = "e2e-p1-9-src";

// 侧栏标题优先级：thread 标题 → metadata.title → id。mock 的会话记录只有扁平
// title 字段，线程标题因此回落到 `Runtime session ${id.slice(0, 10)}`
// （见 lib/thread-state/sessions.ts），定位时按同一口径取短标题。
const SOURCE_ROW_TITLE = `Runtime session ${SOURCE_ID.slice(0, 10)}`;

async function gotoWorkspace(page: Page) {
  await page.goto("/workspace");
  await expect(page.locator(".app-chat-input")).toBeVisible({ timeout: 30_000 });
}

async function openSessionRowMenu(page: Page, sessionId: string) {
  // 同一会话同时出现在「Directories → Unscoped sessions」与「Sessions」两个分区，
  // 操作菜单只在 Sessions 分区渲染，因此先按分区标题收窄再点行。
  const sessionsSection = page.locator("section").filter({
    has: page.getByRole("button", { name: /^Sessions \d+$/ }),
  });
  const row = sessionsSection.getByRole("button", {
    name: new RegExp(sessionId.slice(0, 10)),
  });
  await expect(row).toBeVisible({ timeout: 15_000 });
  // 操作菜单与行按钮是兄弟节点（同属 div.group/session），且默认 opacity-0，
  // 需要 hover 行所在容器后才点击。
  await row.hover();
  await row
    .locator("xpath=..")
    .getByRole("button", { name: "Session actions" })
    .click();
}

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  await seedSession(page.request, { id: SOURCE_ID });
});

test("Fork 生成带分支后缀的独立新会话", async ({ page }) => {
  const branchBodies: Array<Record<string, unknown>> = [];
  // Playwright glob 的 `*` 不跨 `/`：必须显式写到 `/branch` 段，否则拦不到请求。
  await page.route("**/api/runtime/sessions/*/branch", async (route) => {
    if (route.request().method() === "POST") {
      branchBodies.push(
        (route.request().postDataJSON() ?? {}) as Record<string, unknown>,
      );
    }
    await route.continue();
  });

  await gotoWorkspace(page);
  await openSessionRowMenu(page, SOURCE_ID);
  await page.getByRole("menuitem", { name: "Fork session" }).click();

  await expect.poll(() => branchBodies.length).toBe(1);
  expect(String(branchBodies[0].title)).toContain("(branch)");
  // 侧栏入口是整会话分支：不带锚点（锚点缺省 = 会话末尾，前缀即整会话）。
  expect(branchBodies[0]).not.toHaveProperty("anchor_message_id");
  // mock 的 /branch 为每个分支分配确定性的 e2e-branch-N，并跳转到 canonical 路由。
  await expect(page).toHaveURL(/\/workspace\/sessions\/e2e-branch-\d+$/);

  // 分支不消耗源会话：侧栏同时保留源行与新子行（深度 1）。
  const sessionsSection = page.locator("section").filter({
    has: page.getByRole("button", { name: /^Sessions \d+$/ }),
  });
  await expect(sessionsSection.locator('[role="treeitem"]')).toHaveCount(2);
  await expect(
    sessionsSection.locator('[role="treeitem"][data-depth="1"]'),
  ).toHaveCount(1);
});

test("删除会话后该行从侧栏列表消失", async ({ page }) => {
  const deleteCalls: string[] = [];
  // 精确匹配单会话端点：Playwright glob 的 `*` 不跨 `/`，用 `**/sessions*` 会漏掉
  // `/sessions/{id}` 形态的 DELETE。
  await page.route(`**/api/runtime/sessions/${SOURCE_ID}`, async (route) => {
    if (route.request().method() === "DELETE") {
      deleteCalls.push(route.request().url());
    }
    await route.continue();
  });

  await gotoWorkspace(page);
  await openSessionRowMenu(page, SOURCE_ID);
  await page.getByRole("menuitem", { name: "Delete session" }).click();

  await expect.poll(() => deleteCalls.length).toBe(1);
  expect(decodeURIComponent(deleteCalls[0])).toContain(`/sessions/${SOURCE_ID}`);
  await expect(
    page.getByRole("button", { name: new RegExp(SOURCE_ROW_TITLE) }),
  ).toHaveCount(0);
});
