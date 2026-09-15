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
  // Phase 2（合并方案 §3.2）：目录段与会话段已合并为单一「Directories」分区，
  // 同一会话只有一行（行菜单与行按钮同属一个行容器），不再按分区标题去重。
  const row = workspaceSection(page).getByRole("button", {
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

/** 合并后的单一分区容器：标题按钮形如「Directories 3」（段名沿用工作目录，见方案 §4 D1）。 */
function workspaceSection(page: Page) {
  return page.locator("section").filter({
    has: page.getByRole("button", { name: /^Directories \d+$/ }),
  });
}

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  await seedSession(page.request, { id: SOURCE_ID });
});

// Phase 2 回归断言（合并方案 §7-1）：目录段与会话段合并后，同一会话在侧栏只渲染一行。
// 合并前同一会话会在「工作目录 → Unscoped sessions」与「会话」两处各出现一次。
test("同一会话在侧栏只渲染一行", async ({ page }) => {
  await gotoWorkspace(page);

  await expect(
    workspaceSection(page).getByRole("button", {
      name: new RegExp(SOURCE_ID.slice(0, 10)),
    }),
  ).toHaveCount(1);
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

  // 分支不消耗源会话：合并后的分区里同时保留源行与新子行（深度 1）。
  const sessionsSection = workspaceSection(page);
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

test("归档会话后从默认列表隐藏，可从归档区恢复", async ({ page }) => {
  const archiveCalls: string[] = [];
  const restoreCalls: string[] = [];
  await page.route(`**/api/runtime/sessions/${SOURCE_ID}/archive`, async (route) => {
    if (route.request().method() === "POST") {
      archiveCalls.push(route.request().url());
    }
    await route.continue();
  });
  await page.route(`**/api/runtime/sessions/${SOURCE_ID}/activate`, async (route) => {
    if (route.request().method() === "POST") {
      restoreCalls.push(route.request().url());
    }
    await route.continue();
  });

  await gotoWorkspace(page);
  await openSessionRowMenu(page, SOURCE_ID);
  await page.getByRole("menuitem", { name: "Archive session" }).click();

  await expect.poll(() => archiveCalls.length).toBe(1);
  expect(decodeURIComponent(archiveCalls[0])).toContain(
    `/sessions/${SOURCE_ID}/archive`,
  );
  // 归档是非破坏操作：行从默认列表隐藏，但可通过「显示归档」入口找回。
  // 行名随分组/徽章变化，统一按会话 id 前缀匹配（与 openSessionRowMenu 同口径）。
  const sessionsSection = workspaceSection(page);
  const rowName = new RegExp(SOURCE_ID.slice(0, 10));
  await expect(
    sessionsSection.getByRole("button", { name: rowName }),
  ).toHaveCount(0);
  const showArchived = page.getByRole("button", { name: /^Show archived/ });
  await expect(showArchived).toBeVisible();

  // 展开归档区：行重新出现（带 Archived 徽章），菜单入口变为「恢复归档」。
  await showArchived.click();
  await expect(sessionsSection.getByRole("button", { name: rowName })).toBeVisible();
  await openSessionRowMenu(page, SOURCE_ID);
  await page
    .getByRole("menuitem", { name: "Restore archived session" })
    .click();

  await expect.poll(() => restoreCalls.length).toBe(1);
  expect(decodeURIComponent(restoreCalls[0])).toContain(
    `/sessions/${SOURCE_ID}/activate`,
  );
  // 恢复后回到默认列表；归档区计数归零，入口消失。
  await expect(
    sessionsSection.getByRole("button", { name: rowName }),
  ).toBeVisible();
  await expect(showArchived).toHaveCount(0);
});

// 回归用例（2026-09-14 的重复编辑器缺陷 → 2026-09-15 合并后口径）：
// 合并前同一会话在两个分区各渲染一行，重命名状态只按 id 记录时两行会同时挂载行内
// 输入框（都是 `autoFocus`）：后挂载者抢走焦点触发先挂载者的 `onBlur → onCancel`，
// 状态立刻清空、编辑器一帧内卸载 —— 用户看到的就是「点铅笔没反应」。
// 合并后（方案 §3.5-E）同一会话只有一行、重命名状态退化为 `string | null`，
// 该冲突在结构上消失；本用例守住「进入编辑态 → 跨帧存活 → 提交 / Esc 取消」链路。
// 2026-09-15 样式优化：行内铅笔并入行菜单，入口变为「hover 行 → 行菜单 → 重命名会话」，
// 断言口径（PATCH 落库 / 跨帧存活 / Esc 不写回）保持不变。
test("菜单重命名：单一入口进入编辑态、提交后再次进入并 Esc 取消", async ({
  page,
}) => {
  const patchBodies: Array<Record<string, unknown>> = [];
  await page.route(`**/api/runtime/sessions/${SOURCE_ID}`, async (route) => {
    if (route.request().method() === "PATCH") {
      patchBodies.push(
        (route.request().postDataJSON() ?? {}) as Record<string, unknown>,
      );
    }
    await route.continue();
  });

  await gotoWorkspace(page);

  const workspace = workspaceSection(page);
  await openSessionRowMenu(page, SOURCE_ID);
  await page.getByRole("menuitem", { name: "Rename session" }).click();

  const editor = page.getByRole("textbox", { name: "Rename session" });
  await expect(editor).toBeVisible();
  // 跨帧存活：编辑器不会被同帧的条件渲染 / 焦点转移卸载（缺陷期的表现就是这里挂掉）。
  await page.waitForTimeout(300);
  await expect(editor).toHaveCount(1);
  await editor.fill("Renamed from sidebar pencil");
  await editor.press("Enter");

  // 提交走 PATCH /sessions/{id}（{ title }），mock 落库后行标题同步刷新。
  await expect.poll(() => patchBodies.length).toBe(1);
  expect(patchBodies[0].title).toBe("Renamed from sidebar pencil");
  const renamedRow = workspace.getByRole("button", {
    name: /Renamed from sidebar pencil/,
  });
  await expect(renamedRow).toBeVisible();

  // 同一行再次进入编辑态：Esc 取消不写回，也不产生第二次 PATCH。
  await renamedRow.hover();
  await renamedRow
    .locator("xpath=..")
    .getByRole("button", { name: "Session actions" })
    .click();
  await page.getByRole("menuitem", { name: "Rename session" }).click();
  await expect(editor).toBeVisible();
  await page.waitForTimeout(300);
  await expect(editor).toHaveCount(1);
  await editor.press("Escape");
  await expect(editor).toHaveCount(0);
  expect(patchBodies).toHaveLength(1);
});
