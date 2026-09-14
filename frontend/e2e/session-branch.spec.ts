// 批次 4（§5.6 / §6.1）e2e：真实会话分支 —— 服务端按锚点把**历史前缀**复制进新会话，
// 源会话零改动。覆盖计划里的四条验收：
//   ① 只有「已完成轮次的最后一条消息」可用，其余分支按钮可见但 `aria-disabled` 且有原因；
//   ② 点击后发出 `POST /api/runtime/sessions/{id}/branch`，body 带 `anchor_message_id`；
//   ③ 跳转到新会话 canonical 路由，侧栏出现缩进子行（`aria-level=2` + 来源徽标）；
//   ④ 源会话历史不变（回源后消息数一致）。
//
// 数据源（与后端同语义的 mock）：
//   - `POST /api/_test/history` 覆盖 `GET /api/runtime/sessions/{id}/history`（history sync 投影）；
//   - `POST /api/runtime/sessions/{id}/branch` 见 `e2e/mock-server.mjs`，新会话 id 为确定性的
//     `e2e-branch-N`（`_test/reset` 会把序号清零）。
//
// 为什么先发一条消息再注入历史：历史 sync 以「本地已存在该 sessionId 的线程」为前置条件
// （与 `tool-row-history.spec.ts` 同一口径）；刷新后消息列表完全由权威历史投影而来。

import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedSessionHistory, waitForAssistantHistory } from "./support";

/** 首页首条消息在 mock 里固定落到该会话（`POST /sessions` 无 id 时的确定性回落）。 */
const SOURCE_ID = "e2e-session-1";
/** 源会话的线程短标题口径（`Runtime session ${id.slice(0, 10)}`）。 */
const SOURCE_ROW_TITLE = `Runtime session ${SOURCE_ID.slice(0, 10)}`;
const FIRST_TURN_TAIL = "msg-branch-answer-1";
const SECOND_TURN_TAIL = "msg-branch-answer-2";

/** 两轮已完成对话：锚点候选 = 每个 assistant 答案（轮末尾）。 */
const SOURCE_HISTORY: Array<Record<string, unknown>> = [
  {
    role: "user",
    content: "first question",
    metadata: { message_id: "msg-branch-question-1" },
  },
  {
    role: "assistant",
    content: "first answer",
    metadata: { message_id: FIRST_TURN_TAIL },
  },
  {
    role: "user",
    content: "second question",
    metadata: { message_id: "msg-branch-question-2" },
  },
  {
    role: "assistant",
    content: "second answer",
    metadata: { message_id: SECOND_TURN_TAIL },
  },
];

const composer = (page: Page) => page.locator(".app-chat-input");

/** 分支按钮（`turn-tail-row` 只在宿主提供 `onBranch` 时渲染，故按 data 属性定位）。 */
const branchButtons = (page: Page) => page.locator("button[data-branch-state]");

const sessionsSection = (page: Page) =>
  page.locator("section").filter({
    has: page.getByRole("button", { name: /^Sessions \d+$/ }),
  });

/** 打开一个「两轮已完成」的源会话：发一条消息拿到线程 → 注入历史 → 刷新。 */
async function openSeededSource(page: Page): Promise<void> {
  await resetMockState(page.request);
  await page.goto("/workspace");
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });

  await composer(page).fill("branch seed ping");
  await composer(page).press("Control+Enter");
  await waitForAssistantHistory(page.request, SOURCE_ID, "Paris");

  await seedSessionHistory(page.request, SOURCE_ID, SOURCE_HISTORY);
  await page.reload();
  await page.goto(`/workspace/sessions/${SOURCE_ID}`);
  await expect(page.getByText("second answer")).toBeVisible({ timeout: 20_000 });
}

/** 读源会话的权威历史（用于「源会话零改动」的逐字段断言）。 */
async function readSourceHistory(page: Page): Promise<unknown[]> {
  const response = await page.request.get(
    `/api/runtime/sessions/${encodeURIComponent(SOURCE_ID)}/history`,
  );
  expect(response.ok()).toBe(true);
  const body = (await response.json()) as { history?: unknown[] };
  return body.history ?? [];
}

test("① 只有已完成轮次的最后一条消息可分支，其余可见但不可用", async ({ page }) => {
  await openSeededSource(page);

  const buttons = branchButtons(page);
  await expect(buttons).toHaveCount(2);

  // 第 1 轮之后还有第 2 轮 ⇒ 不是 transcript 末尾：禁用但保留聚焦 + 原因。
  const stale = buttons.nth(0);
  await expect(stale).toHaveAttribute("data-branch-state", "unavailable");
  await expect(stale).toHaveAttribute("aria-disabled", "true");
  await expect(stale).toHaveAttribute(
    "title",
    "Available only on the last message of a completed turn",
  );
  const reasonId = await stale.getAttribute("aria-describedby");
  expect(reasonId).toBeTruthy();
  await expect(page.locator(`[id="${reasonId}"]`)).toHaveText(
    "Available only on the last message of a completed turn",
  );

  // 末尾消息 = 唯一锚点：可用（不占用原生 disabled，且不下发 aria-disabled）。
  const tail = buttons.nth(1);
  await expect(tail).toHaveAttribute("data-branch-state", "available");
  await expect(tail).not.toHaveAttribute("aria-disabled");
  await expect(tail).toHaveAttribute("aria-label", "Branch into a new conversation");
});

test("② 点击分支发出 POST /branch 且 body 带锚点，URL 落到新会话", async ({ page }) => {
  const bodies: Array<Record<string, unknown>> = [];
  await page.route("**/api/runtime/sessions/*/branch", async (route) => {
    bodies.push((route.request().postDataJSON() ?? {}) as Record<string, unknown>);
    await route.continue();
  });

  await openSeededSource(page);
  await branchButtons(page).nth(1).click();

  await expect.poll(() => bodies.length).toBe(1);
  expect(bodies[0]).toMatchObject({
    anchor_message_id: SECOND_TURN_TAIL,
    include_anchor: true,
  });
  expect(String(bodies[0].title)).toContain("(branch)");

  // 新会话 id 由 mock 确定性分配（reset 后从 1 起），并跳 canonical 会话路由。
  await expect(page).toHaveURL(/\/workspace\/sessions\/e2e-branch-1$/);
});

test("③④ 分支后侧栏出现缩进子行，且源会话历史零改动", async ({ page }) => {
  await openSeededSource(page);
  const historyBefore = await readSourceHistory(page);
  expect(historyBefore).toHaveLength(SOURCE_HISTORY.length);

  await branchButtons(page).nth(1).click();
  await expect(page).toHaveURL(/\/workspace\/sessions\/e2e-branch-1$/);
  // 新会话内容 = 锚点处的前缀（两轮都在，末尾即所点的那条消息）。
  await expect(page.getByText("second answer")).toBeVisible({ timeout: 20_000 });

  // ③ 侧栏：子行紧随父行、缩进 1 级、带来源徽标；父行仍在原处（分支不移动源行）。
  // 注意父行与子行标题同前缀（子行 = `${源标题} (branch)`），故必须按深度定位。
  const parentRow = sessionsSection(page)
    .locator('[role="treeitem"][data-depth="0"]')
    .filter({ hasText: SOURCE_ROW_TITLE });
  await expect(parentRow).toHaveCount(1);
  await expect(parentRow).toHaveAttribute("aria-level", "1");
  // 来源徽标只挂在子行上。
  await expect(parentRow.getByTestId("session-fork-badge")).toHaveCount(0);

  const childRow = sessionsSection(page).locator('[role="treeitem"][data-depth="1"]');
  await expect(childRow).toHaveCount(1);
  await expect(childRow).toHaveAttribute("aria-level", "2");
  await expect(childRow.getByTestId("session-fork-badge")).toHaveText("Branch");

  // 子行渲染在父行下方（同一棵树内的视觉顺序）。
  const parentBox = await parentRow.boundingBox();
  const childBox = await childRow.boundingBox();
  expect(childBox?.y ?? -1).toBeGreaterThan(parentBox?.y ?? -1);

  // ④ 源会话零改动：历史逐字段不变（mock 用结构化深拷贝，改源即会在此暴露）。
  expect(await readSourceHistory(page)).toEqual(historyBefore);

  // 回源：两轮答案仍在，且仍只有末尾可分支（分支不消耗源会话）。
  await page.goto(`/workspace/sessions/${SOURCE_ID}`);
  await expect(page.getByText("second answer")).toBeVisible({ timeout: 20_000 });
  await expect(branchButtons(page)).toHaveCount(2);
  await expect(branchButtons(page).nth(1)).toHaveAttribute(
    "data-branch-state",
    "available",
  );
});
