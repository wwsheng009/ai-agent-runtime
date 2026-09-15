import type { Locator, Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedLanguage } from "./support";

// P2-6 子片 3 e2e：组内展开 / 折叠（Show N more）与空白新会话提升。
//
// 口径（用户可见行为，不从实现反推）：
// - 目录组默认折叠；展开组后，超过 5 个会话的组只呈现前 5 个；
// - 折叠控件是真实按钮：`aria-expanded=false` +「展开其余 N 个会话」，展开后变「收起」；
// - 折叠只影响呈现：展开后行数恢复到全量，再点回到折叠态；
// - 空白会话（`metadata.totalTurns === 0`）钉在组顶——即使它最近更新时间最旧（按
//   常规最近更新排序会沉底，因此「可见 + 在最新会话之上」只能来自提升）。
//
// Host 会话快照由本文件 stub（不依赖 mock 的会话夹具）：全部会话同属一个注册目录。

/** 注册表里的原始路径（组头 title 是规范化后的 `E:/work/collapse`）。 */
const RAW_PATH = "E:\\work\\collapse";
const GROUP_KEY = "E:/work/collapse";

/** 行定位按 id 前 10 位做子串匹配（与 session-order.spec.ts 同口径）。 */
const BLANK_SESSION_ID = "e2e-collapse-blank";
/** 5 个「已有消息」的会话：更新时间从新到旧 b5 → b1。 */
const SESSION_IDS = [
  "e2e-col-b1",
  "e2e-col-b2",
  "e2e-col-b3",
  "e2e-col-b4",
  "e2e-col-b5",
];

type HostSession = Record<string, unknown>;

/** 会话快照：5 个有消息的会话 + 1 个空白会话（空白会话时间最旧）。 */
function hostSessions(): HostSession[] {
  const withMessages = SESSION_IDS.map((id, index) => ({
    id,
    userId: "default",
    state: "idle",
    metadata: {
      title: id,
      totalTurns: 2,
      context: { workspace_path: RAW_PATH },
    },
    createdAt: "2026-01-01T00:00:00Z",
    updatedAt: `2026-01-0${index + 4}T00:00:00Z`,
  }));

  return [
    ...withMessages,
    {
      id: BLANK_SESSION_ID,
      userId: "default",
      state: "idle",
      metadata: {
        title: BLANK_SESSION_ID,
        totalTurns: 0,
        context: { workspace_path: RAW_PATH },
      },
      createdAt: "2026-01-01T00:00:00Z",
      updatedAt: "2026-01-01T00:00:00Z",
    },
  ];
}

async function installHostStub(page: Page): Promise<void> {
  await page.route(
    (url) => url.pathname === "/api/runtime/workspace-directories",
    async (route) => {
      if (route.request().method() !== "GET") {
        await route.continue();
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        json: {
          directories: [
            { id: "e2e-dir-collapse", path: RAW_PATH, name: "collapse", exists: true },
          ],
          count: 1,
        },
      });
    },
  );

  await page.route(
    (url) => url.pathname === "/api/runtime/sessions",
    async (route) => {
      if (route.request().method() !== "GET") {
        await route.continue();
        return;
      }
      const sessions = hostSessions();
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        json: { sessions, count: sessions.length, user_id: "default" },
      });
    },
  );
}

// Phase 2（合并方案 §3.2）：目录段与会话段已合并为单一分区，段名沿用工作目录（决策 D1），
// 徽标是会话数（决策 D5）；zh-CN 下标题按钮形如「工作目录 6」。
function sessionsSection(page: Page) {
  return page.locator("section").filter({
    has: page.getByRole("button", { name: /^工作目录 \d+$/ }),
  });
}

/** 目录组头：title 是规范化路径（`\` → `/`）。 */
function groupHeader(page: Page): Locator {
  return sessionsSection(page).locator(
    `[data-testid="sidebar-session-group-drop"][title="${GROUP_KEY}"]`,
  );
}

/** 折叠控件（P2-6 子片 3 新增）。 */
function groupToggle(page: Page): Locator {
  return sessionsSection(page).getByTestId("sidebar-session-group-toggle");
}

/** 行容器（拖拽属性宿主）是行按钮的父节点。 */
function sessionRow(page: Page, id: string): Locator {
  return sessionsSection(page)
    .getByRole("button", { name: new RegExp(id.slice(0, 10)) })
    .locator("xpath=..");
}

test.describe("P2-6 子片 3：组内展开 / 折叠与空白会话提升", () => {
  test.beforeEach(async ({ page, request }) => {
    await resetMockState(request);
    await seedLanguage(page, "zh-CN");
    await installHostStub(page);
    await page.goto("/workspace");
    await expect(page.locator(".app-chat-input")).toBeVisible({ timeout: 30_000 });
    // 目标组的默认开合不属于本用例的断言面（默认折叠与默认展开都出现过）：
    // 只在组内会话不可见时点开，避免把「已展开」点成折叠；会话快照到达后侧栏
    // 会重渲染，点击可能被替换掉的节点吞掉，故整体重试（toPass）。
    await expect(async () => {
      if (!(await sessionRow(page, BLANK_SESSION_ID).isVisible())) {
        await groupHeader(page).click();
      }
      await expect(sessionRow(page, BLANK_SESSION_ID)).toBeVisible({
        timeout: 2_000,
      });
    }).toPass({ timeout: 20_000 });
  });

  test("超过上限的组折叠为 5 个，折叠控件可展开并收起", async ({ page }) => {
    await expect(groupToggle(page)).toHaveAttribute("aria-expanded", "false");
    await expect(groupToggle(page)).toContainText("展开其余 1 个会话");

    // 折叠态：只呈现 5 个，沉底的 b1 不渲染（也不可拖拽）。
    await expect(sessionRow(page, BLANK_SESSION_ID)).toBeVisible();
    for (const id of ["e2e-col-b2", "e2e-col-b3", "e2e-col-b4", "e2e-col-b5"]) {
      await expect(sessionRow(page, id)).toBeVisible();
    }
    await expect(sessionRow(page, "e2e-col-b1")).toHaveCount(0);

    await groupToggle(page).click();
    await expect(groupToggle(page)).toHaveAttribute("aria-expanded", "true");
    await expect(groupToggle(page)).toContainText("收起");
    await expect(sessionRow(page, "e2e-col-b1")).toBeVisible();

    await groupToggle(page).click();
    await expect(groupToggle(page)).toHaveAttribute("aria-expanded", "false");
    await expect(sessionRow(page, "e2e-col-b1")).toHaveCount(0);
  });

  test("空白新会话钉在组顶：比最近更新的会话更靠上", async ({ page }) => {
    const blankRow = await sessionRow(page, BLANK_SESSION_ID).boundingBox();
    const newestRow = await sessionRow(page, "e2e-col-b5").boundingBox();

    expect(blankRow).not.toBeNull();
    expect(newestRow).not.toBeNull();
    expect(blankRow!.y).toBeLessThan(newestRow!.y);
  });
});
