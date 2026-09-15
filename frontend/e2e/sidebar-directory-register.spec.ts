import type { Locator, Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedLanguage, seedSession } from "./support";

// 2026-09-15 方案 §13-A：「管理目录」弹层列出**仅由会话派生**的目录，并可一键注册。
//
// 用户可见口径（不读实现类名，故对重构免疫）：
// - 侧栏目录组 ≠ 注册表：注册表里只有 alpha，beta 组只由会话 `context.workspace_path` 派生；
// - 派生组**没有**组头 ⋯ 菜单（`button[aria-haspopup="menu"]`）——这就是「未注册」的肉眼判据；
// - 弹层「未注册（来自会话）」分区列出 beta 的**规范化路径**，点「注册」后：
//   ① POST body.path 是规范化路径（注册表与派生组同一口径）；
//   ② 刷新后该行移入已注册列表（同路径分组被合并去重）；
//   ③ 侧栏 beta 组头随之长出 ⋯ 菜单（已并入注册表）。
//
// mock 说明：e2e mock 没有 `/api/runtime/workspace-directories` 端点，注册表由本用例的
// stub 自持（GET 回模型 / POST 入库并幂等），因此断言点落在「请求形状 + 刷新后 UI」。

const ALPHA_RAW_PATH = "E:\\work\\alpha";
const ALPHA_GROUP_KEY = "E:/work/alpha";
const BETA_RAW_PATH = "E:\\work\\beta";
const BETA_GROUP_KEY = "E:/work/beta";

const ALPHA_SESSION_ID = "e2e-register-1";
const BETA_SESSION_ID = "e2e-register-2";

type RegisteredDirectory = { id: string; name: string; path: string };

type RegistryStub = {
  directories: RegisteredDirectory[];
  /** 收到的注册路径（POST body.path），按顺序。 */
  posts: string[];
};

/** 注册表 stub：GET 回当前模型，POST 把路径并入（重复注册幂等 → existing: true）。 */
async function installRegistryStub(page: Page): Promise<RegistryStub> {
  const stub: RegistryStub = {
    directories: [{ id: "dir-alpha", path: ALPHA_RAW_PATH, name: "alpha" }],
    posts: [],
  };

  await page.route(
    (url) => url.pathname === "/api/runtime/workspace-directories",
    async (route) => {
      const request = route.request();
      if (request.method() === "POST") {
        const body = (request.postDataJSON() ?? {}) as { path?: string };
        const path = body.path ?? "";
        stub.posts.push(path);
        const existing = stub.directories.find(
          (directory) => directory.path.toLowerCase() === path.toLowerCase(),
        );
        const directory =
          existing ?? ({ id: "dir-beta", path, name: "beta" } as RegisteredDirectory);
        if (!existing) {
          stub.directories.push(directory);
        }
        await route.fulfill({
          status: existing ? 200 : 201,
          contentType: "application/json",
          json: { directory, existing: Boolean(existing) },
        });
        return;
      }
      if (request.method() === "GET") {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          json: {
            directories: stub.directories,
            count: stub.directories.length,
          },
        });
        return;
      }
      await route.continue();
    },
  );

  return stub;
}

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

/** 「已注册」的肉眼判据：组头动作菜单入口（派生组没有）。 */
function groupActions(page: Page, key: string): Locator {
  return groupContainer(page, key).locator('button[aria-haspopup="menu"]');
}

/**
 * 「管理目录」入口：段内常驻工具条正在收敛进段头 ⋯ 面板（2026-09-15 布局优化，
 * 与本次 §15 改动同日），两种结构都可能渲染，因此按「面板优先、常驻按钮回落」定位：
 * 配置入口本身只有一处，断言不依赖它挂在段头还是段内。
 */
async function openManageDirectories(page: Page) {
  const menuTrigger = page.getByTestId("sidebar-directories-menu-trigger");
  if ((await menuTrigger.count()) > 0) {
    await menuTrigger.click();
    await page.getByTestId("sidebar-directories-manage-entry").click();
    return;
  }
  await page.getByRole("button", { name: "管理目录" }).click();
}

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  // 断言文案是 zh-CN 口径（「管理目录」/「管理工作目录」），必须显式 seed 语言：
  // e2e 默认语言不是 zh-CN（同 session-grouping.spec.ts）。
  await seedLanguage(page, "zh-CN");
  // 两个不同工作目录的会话 → 两个目录组；其中只有 alpha 在注册表里。
  await seedSession(page.request, {
    id: ALPHA_SESSION_ID,
    title: "register alpha",
    metadata: { context: { workspace_path: ALPHA_RAW_PATH } },
  });
  await seedSession(page.request, {
    id: BETA_SESSION_ID,
    title: "register beta",
    metadata: { context: { workspace_path: BETA_RAW_PATH } },
  });
});

test("管理目录列出会话派生目录，一键注册后并入注册表", async ({ page }) => {
  const registry = await installRegistryStub(page);

  await page.goto("/workspace");
  await expect(page.locator(".app-chat-input")).toBeVisible({ timeout: 30_000 });

  await expect(groupHeader(page, ALPHA_GROUP_KEY)).toBeVisible();
  await expect(groupHeader(page, BETA_GROUP_KEY)).toBeVisible();
  // 注册前：beta 是派生组，没有组头 ⋯ 菜单。
  await expect(groupActions(page, BETA_GROUP_KEY)).toHaveCount(0);

  await openManageDirectories(page);

  const dialog = page.getByRole("dialog", { name: "管理工作目录" });
  await expect(dialog).toBeVisible();
  await expect(
    dialog.locator('[data-testid="directory-manage-row"]'),
  ).toHaveCount(1);

  const unregistered = dialog.locator(
    '[data-testid="directory-manage-unregistered-row"]',
  );
  await expect(unregistered).toHaveCount(1);
  await expect(unregistered).toHaveAttribute(
    "data-directory-path",
    BETA_GROUP_KEY,
  );

  // §15 之后未注册行有两个动作（组合动作 / 纯注册），这里必须精确匹配：
  // 组合动作的无障碍名「注册并在该目录下新建会话」含「注册」子串。
  await unregistered
    .getByRole("button", { name: "注册", exact: true })
    .click();

  // ① 注册请求用的是规范化路径。
  await expect.poll(() => registry.posts).toEqual([BETA_GROUP_KEY]);
  // ② 刷新后该行移入已注册列表（同路径分组被合并去重）。
  await expect(
    dialog.locator('[data-testid="directory-manage-row"]'),
  ).toHaveCount(2);
  await expect(unregistered).toHaveCount(0);
  // ③ 侧栏 beta 组头长出 ⋯ 菜单：它已经进了注册表。
  await expect(groupActions(page, BETA_GROUP_KEY)).toHaveCount(1);
});

test("注册失败：弹层就地提示且行保留（可重试）", async ({ page }) => {
  await page.route(
    (url) => url.pathname === "/api/runtime/workspace-directories",
    async (route) => {
      const request = route.request();
      if (request.method() === "POST") {
        await route.fulfill({
          status: 400,
          contentType: "application/json",
          json: { error: "工作目录不存在" },
        });
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        json: { directories: [], count: 0 },
      });
    },
  );

  await page.goto("/workspace");
  await expect(page.locator(".app-chat-input")).toBeVisible({ timeout: 30_000 });

  await openManageDirectories(page);

  const dialog = page.getByRole("dialog", { name: "管理工作目录" });
  const unregistered = dialog.locator(
    '[data-testid="directory-manage-unregistered-row"]',
  );
  // 注册表为空：不再显示空态，而是直接列出派生目录。
  await expect(
    dialog.locator('[data-testid="directory-manage-empty"]'),
  ).toHaveCount(0);
  await expect(unregistered).toHaveCount(2);

  await unregistered
    .filter({ hasText: BETA_GROUP_KEY })
    .getByRole("button", { name: "注册", exact: true })
    .click();

  await expect(
    dialog.locator('[data-testid="directory-manage-register-error"]'),
  ).toHaveText("工作目录不存在");
  // 弹层不收起、行不消失：用户可以改走「添加目录」或重试。
  await expect(dialog).toBeVisible();
  await expect(unregistered).toHaveCount(2);
});

// 2026-09-15 方案 §15：「用即注册」——派生目录组头的「注册并新建会话」一次点击完成
// ① POST 注册表 ② 用注册响应里的 directory_id 在该目录下新建会话并跳转。
//
// 与上一条用例的差别：那条走弹层里的**纯注册**（无会话副作用），这条走组头 hover 的
// **组合动作**，断言点是「一次点击后同时发生登记与会话创建」以及新会话的绑定口径。
test("派生目录组头「注册并新建会话」：先登记再按目录建会话并跳转", async ({
  page,
}) => {
  const registry = await installRegistryStub(page);

  // 建会话请求由 mock-server 正常处理（continue），这里只旁听请求体。
  const sessionBodies: Array<Record<string, unknown>> = [];
  await page.route(
    (url) => url.pathname === "/api/runtime/sessions",
    async (route) => {
      if (route.request().method() === "POST") {
        sessionBodies.push(
          (route.request().postDataJSON() ?? {}) as Record<string, unknown>,
        );
      }
      await route.continue();
    },
  );

  await page.goto("/workspace");
  await expect(page.locator(".app-chat-input")).toBeVisible({ timeout: 30_000 });
  await expect(groupHeader(page, BETA_GROUP_KEY)).toBeVisible();

  const derivedAction = groupContainer(page, BETA_GROUP_KEY).getByRole("button", {
    name: "注册并在该目录下新建会话",
  });
  // 只有派生目录有这个动作；已注册的 alpha 走 ⋯ 菜单三件套。
  await expect(derivedAction).toHaveCount(1);
  await expect(
    groupContainer(page, ALPHA_GROUP_KEY).getByRole("button", {
      name: "注册并在该目录下新建会话",
    }),
  ).toHaveCount(0);

  await groupHeader(page, BETA_GROUP_KEY).hover();
  await derivedAction.click();

  // ① 先登记：POST 的是派生组的规范化路径。
  await expect.poll(() => registry.posts).toEqual([BETA_GROUP_KEY]);
  // ② 再建会话：路径用规范化路径，directory_id 直接取注册响应（不是二次推导）。
  await expect.poll(() => sessionBodies.length).toBe(1);
  expect(sessionBodies[0]).toMatchObject({
    workspace_path: BETA_GROUP_KEY,
    directory_id: "dir-beta",
  });
  // ③ 新会话被选中：跳到 canonical 会话路由（mock 生成 e2e-fork-N）。
  await expect(page).toHaveURL(/\/workspace\/sessions\/e2e-fork-\d+/);
  // ④ 侧栏 beta 组头已并入注册表：长出 ⋯ 菜单。
  await expect(groupActions(page, BETA_GROUP_KEY)).toHaveCount(1);
});
