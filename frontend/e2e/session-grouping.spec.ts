import type { Locator, Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedLanguage, seedSession } from "./support";

// P2-6 子片 2 e2e：侧栏会话分组视图 + 跨组移动。
//
// 口径（已核实的权威事实，不从实现反推）：
// - 分组控件与排序控件同形：role="group" + aria-pressed 双档（按目录 / 平铺）；
// - 会话行只在「手动」排序模式下才 draggable；跨组落点 = 目标目录组头；
// - 目录组头 data-testid="sidebar-session-group-drop"，title 是**规范化路径**
//   （`\` → `/`，如 E:/work/alpha），目录组默认折叠——未展开的组不渲染会话行；
// - Host 写回走 PATCH /api/runtime/sessions/:id，body 用**注册表原始路径**
//   （E:\work\beta，camelCase context），后端按 context 键逐键合并；
// - Host 拒绝写回即回滚到原组 + 就地提示「移动会话失败：<message>」，成功路径不得出现；
// - 平铺视图不渲染目录组头，跨组移动不可表达（切回按目录恢复）。
//
// 用例统一 zh-CN：失败文案的权威口径是中文（`移动会话失败：<message>`），
// 用既有 helper `seedLanguage` 显式覆盖系统语言（见 e2e/support.ts 的说明）。
//
// Host 侧状态由本文件的 page.route stub 提供（installHostStub 有逐条说明）：
// e2e mock 既没有 /api/runtime/workspace-directories 端点，也不做 PATCH body 的
// context 合并；因此「注册目录」与「写回后的会话快照」在这里被显式建模成 Host 状态。
// 断言仍只读用户可见行为（分组归属、就地提示）与真实请求体，不 mock 前端模块。

/** 注册表里的原始路径（Host 写回必须是这一份，反斜杠原样）。 */
const ALPHA_RAW_PATH = "E:\\work\\alpha";
const BETA_RAW_PATH = "E:\\work\\beta";
/** 组头 title 用的规范化路径（`\` → `/`）。 */
const ALPHA_GROUP_KEY = "E:/work/alpha";
const BETA_GROUP_KEY = "E:/work/beta";

// 行定位按 id 前 10 位做子串匹配（与 session-order.spec.ts 同口径），
// 两个 id 必须在前 10 位内彼此可区分。
const ALPHA_SESSION_ID = "e2e-alpha-1";
const BETA_SESSION_ID = "e2e-beta-1";

const MOVE_FAILED_TEXT = "移动会话失败：";
/** Host 拒绝写回时的响应错误文案（进到就地提示的 <message> 位）。 */
const HOST_REJECTED_MESSAGE = "e2e host rejected move";
const HOST_REJECTED_TEXT = `${MOVE_FAILED_TEXT}${HOST_REJECTED_MESSAGE}`;

/** Host 注册表（两个目录各自已注册，exists=true 才可作为跨组落点）。 */
const WORKSPACE_DIRECTORIES = [
  { id: "e2e-dir-alpha", path: ALPHA_RAW_PATH, name: "alpha", exists: true },
  { id: "e2e-dir-beta", path: BETA_RAW_PATH, name: "beta", exists: true },
];

type HostWriteBack = {
  body: Record<string, unknown>;
  sessionId: string;
};

type HostStub = {
  /** 收到的 PATCH（按发生顺序）。 */
  writes: HostWriteBack[];
  /** 已落库的归属（sessionId → 注册表原始路径）；会话快照按它合并回去。 */
  persisted: Map<string, string>;
};

/**
 * Host 侧状态的最小子集（注册目录 + 会话写回落库）。
 *
 * - 目录注册表：mock 无该端点（会落到「未知 API 返回 {}」的兜底），注册目录恒为空，
 *   目录组会退化成「从会话路径派生」的组，落点路径也就变成规范化路径而非注册表原始路径；
 * - 会话写回：mock 的 `/api/runtime/sessions/:id` 处理器不区分方法、忽略 body，
 *   PATCH 看似 200 但不落库（刷新即回原组），因此这里把写回结果合并回 GET 快照，
 *   等价于真实后端「按 context 键逐键合并」的持久化。
 */
async function installHostStub(
  page: Page,
  options: { rejectWrites?: boolean } = {},
): Promise<HostStub> {
  const stub: HostStub = { persisted: new Map(), writes: [] };

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
          directories: WORKSPACE_DIRECTORIES,
          count: WORKSPACE_DIRECTORIES.length,
        },
      });
    },
  );

  await page.route(
    (url) => /^\/api\/runtime\/sessions\/[^/]+$/.test(url.pathname),
    async (route) => {
      const pathname = new URL(route.request().url()).pathname;
      if (route.request().method() !== "PATCH") {
        await route.continue();
        return;
      }
      const sessionId = decodeURIComponent(pathname.split("/").pop() ?? "");
      const body = (route.request().postDataJSON() ?? {}) as Record<string, unknown>;
      stub.writes.push({ body, sessionId });
      if (options.rejectWrites) {
        await route.fulfill({
          status: 503,
          contentType: "application/json",
          json: { error: HOST_REJECTED_MESSAGE },
        });
        return;
      }
      const context = (body.context ?? {}) as Record<string, unknown>;
      const workspacePath =
        typeof context.workspace_path === "string" ? context.workspace_path : "";
      if (workspacePath) {
        stub.persisted.set(sessionId, workspacePath);
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        json: {
          session: {
            id: sessionId,
            session_id: sessionId,
            metadata: { context: { workspace_path: workspacePath } },
          },
        },
      });
    },
  );

  // 会话快照：把已落库的归属合并回 Host 结果（等价真实后端 PATCH 成功后的下一次 GET）。
  await page.route(
    (url) => url.pathname === "/api/runtime/sessions",
    async (route) => {
      if (route.request().method() !== "GET") {
        await route.continue();
        return;
      }
      const response = await route.fetch();
      const payload = (await response.json()) as {
        sessions?: Array<Record<string, unknown>>;
      };
      const sessions = (payload.sessions ?? []).map((session) => {
        const sessionId = String(session.session_id ?? session.id ?? "");
        const workspacePath = stub.persisted.get(sessionId);
        if (!workspacePath) {
          return session;
        }
        const metadata = (session.metadata ?? {}) as Record<string, unknown>;
        const context = (metadata.context ?? {}) as Record<string, unknown>;
        return {
          ...session,
          metadata: {
            ...metadata,
            context: { ...context, workspace_path: workspacePath },
          },
        };
      });
      await route.fulfill({ json: { ...payload, sessions }, response });
    },
  );

  return stub;
}

async function gotoWorkspace(page: Page) {
  await page.goto("/workspace");
  await expect(page.locator(".app-chat-input")).toBeVisible({ timeout: 30_000 });
}

/** 侧栏「会话」段（section 名随 zh-CN 本地化：`会话 <count>`）。 */
function sessionsSection(page: Page) {
  return page.locator("section").filter({
    has: page.getByRole("button", { name: /^会话 \d+$/ }),
  });
}

/** 排序控件：role=group + aria-pressed 双档。 */
function orderControl(page: Page) {
  return page.getByRole("group", { name: "会话排序方式" });
}

/** 分组视图控件：role=group + aria-pressed 双档（按目录 / 平铺）。 */
function groupingControl(page: Page) {
  return page.getByRole("group", { name: "会话分组视图" });
}

/** 行容器（拖拽属性宿主）是行按钮的父节点（与 session-order.spec.ts 同口径）。 */
function sessionRow(page: Page, id: string): Locator {
  return sessionsSection(page)
    .getByRole("button", { name: new RegExp(id.slice(0, 10)) })
    .locator("xpath=..");
}

/** 目录组头：title 是规范化路径（`\` → `/`）。 */
function groupHeader(page: Page, key: string): Locator {
  return sessionsSection(page).locator(
    `[data-testid="sidebar-session-group-drop"][title="${key}"]`,
  );
}

/** 组容器：组头 + 该组会话行列表的共同父节点（归属断言的观察范围）。 */
function groupContainer(page: Page, key: string): Locator {
  return groupHeader(page, key).locator("xpath=..");
}

/** 指定分组内某会话的行容器（可见即「归属该组」）。 */
function rowInGroup(page: Page, key: string, sessionId: string): Locator {
  return groupContainer(page, key)
    .getByRole("button", { name: new RegExp(sessionId.slice(0, 10)) })
    .locator("xpath=..");
}

/** 目录组默认折叠：组内看不到会话行时点一次组头展开。 */
async function expandGroup(page: Page, key: string): Promise<void> {
  const rows = groupContainer(page, key).locator('[draggable="true"]');
  if ((await rows.count()) === 0) {
    await groupHeader(page, key).click();
  }
  await expect(rows.first()).toBeVisible();
}

/** 进入「手动排序 + 按目录」的分组视图，并展开两个目录组。 */
async function openGroupedManualSidebar(page: Page): Promise<void> {
  await gotoWorkspace(page);

  const grouping = groupingControl(page);
  await expect(grouping.getByRole("button", { name: "按目录" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await orderControl(page).getByRole("button", { name: "手动" }).click();
  await expect(orderControl(page).getByRole("button", { name: "手动" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );

  await expect(groupHeader(page, ALPHA_GROUP_KEY)).toBeVisible();
  await expect(groupHeader(page, BETA_GROUP_KEY)).toBeVisible();
  await expandGroup(page, ALPHA_GROUP_KEY);
  await expandGroup(page, BETA_GROUP_KEY);
}

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  // 两个不同工作目录的会话：归属来自会话元数据，分组视图按它分桶。
  await seedSession(page.request, {
    id: ALPHA_SESSION_ID,
    metadata: { context: { workspace_path: ALPHA_RAW_PATH } },
  });
  await seedSession(page.request, {
    id: BETA_SESSION_ID,
    metadata: { context: { workspace_path: BETA_RAW_PATH } },
  });
  await seedLanguage(page, "zh-CN");
});

test("拖到目标目录组头即乐观归属并写回 Host，刷新后仍在目标组", async ({ page }) => {
  const host = await installHostStub(page);
  await openGroupedManualSidebar(page);

  await expect(rowInGroup(page, ALPHA_GROUP_KEY, ALPHA_SESSION_ID)).toHaveAttribute(
    "draggable",
    "true",
  );
  await expect(rowInGroup(page, BETA_GROUP_KEY, BETA_SESSION_ID)).toBeVisible();
  await expect(rowInGroup(page, BETA_GROUP_KEY, ALPHA_SESSION_ID)).toHaveCount(0);

  // 跨组落点 = 目标目录组头。
  await sessionRow(page, ALPHA_SESSION_ID).dragTo(groupHeader(page, BETA_GROUP_KEY));

  // 乐观归属：会话立即出现在目标组；源组已无会话（空组不留在会话段）。
  await expect(rowInGroup(page, BETA_GROUP_KEY, ALPHA_SESSION_ID)).toBeVisible();
  await expect(groupHeader(page, ALPHA_GROUP_KEY)).toHaveCount(0);
  await expect(sessionsSection(page).getByText(MOVE_FAILED_TEXT)).toHaveCount(0);

  // Host 写回：body 用注册表原始路径（反斜杠），不是组头显示的规范化路径。
  await expect.poll(() => host.writes).toEqual([
    {
      body: { context: { workspace_path: BETA_RAW_PATH } },
      sessionId: ALPHA_SESSION_ID,
    },
  ]);

  // 刷新页面：归属由 Host 快照裁决，仍显示在目标组（源组已空，不再渲染组头）。
  await page.reload();
  await expect(page.locator(".app-chat-input")).toBeVisible({ timeout: 30_000 });
  await expect(groupHeader(page, ALPHA_GROUP_KEY)).toHaveCount(0);
  await expandGroup(page, BETA_GROUP_KEY);
  await expect(rowInGroup(page, BETA_GROUP_KEY, ALPHA_SESSION_ID)).toBeVisible();
  await expect(rowInGroup(page, ALPHA_GROUP_KEY, ALPHA_SESSION_ID)).toHaveCount(0);
});

test("Host 拒绝写回即回滚到原组并就地提示，刷新后不产生假落点", async ({ page }) => {
  const host = await installHostStub(page, { rejectWrites: true });
  await openGroupedManualSidebar(page);

  await sessionRow(page, ALPHA_SESSION_ID).dragTo(groupHeader(page, BETA_GROUP_KEY));

  // 请求确实发出（注册表原始路径），失败后撤销乐观归属。
  await expect.poll(() => host.writes).toEqual([
    {
      body: { context: { workspace_path: BETA_RAW_PATH } },
      sessionId: ALPHA_SESSION_ID,
    },
  ]);
  await expect(rowInGroup(page, ALPHA_GROUP_KEY, ALPHA_SESSION_ID)).toBeVisible();
  await expect(rowInGroup(page, BETA_GROUP_KEY, ALPHA_SESSION_ID)).toHaveCount(0);
  await expect(sessionsSection(page).getByText(HOST_REJECTED_TEXT)).toBeVisible();

  // 刷新：Host 快照仍是原归属，没有假落点。
  await page.reload();
  await expect(page.locator(".app-chat-input")).toBeVisible({ timeout: 30_000 });
  await expandGroup(page, ALPHA_GROUP_KEY);
  await expandGroup(page, BETA_GROUP_KEY);
  await expect(rowInGroup(page, ALPHA_GROUP_KEY, ALPHA_SESSION_ID)).toBeVisible();
  await expect(rowInGroup(page, BETA_GROUP_KEY, ALPHA_SESSION_ID)).toHaveCount(0);
  await expect(sessionsSection(page).getByText(MOVE_FAILED_TEXT)).toHaveCount(0);
});

test("切到平铺不渲染目录组头，切回按目录组头回来", async ({ page }) => {
  await installHostStub(page);
  await gotoWorkspace(page);

  // 按目录（默认档）：两个注册目录各有组头，跨组落点可表达。
  await expect(groupHeader(page, ALPHA_GROUP_KEY)).toBeVisible();
  await expect(groupHeader(page, BETA_GROUP_KEY)).toBeVisible();

  const grouping = groupingControl(page);
  await grouping.getByRole("button", { name: "平铺" }).click();
  await expect(grouping.getByRole("button", { name: "平铺" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  // 平铺没有组边界：不渲染任何目录组头（跨组移动不可表达）。
  await expect(page.getByTestId("sidebar-session-group-drop")).toHaveCount(0);
  // 视图切换不丢会话：两行仍在，只是回到单列平铺。
  await expect(sessionRow(page, ALPHA_SESSION_ID)).toBeVisible();
  await expect(sessionRow(page, BETA_SESSION_ID)).toBeVisible();

  await grouping.getByRole("button", { name: "按目录" }).click();
  await expect(grouping.getByRole("button", { name: "按目录" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await expect(groupHeader(page, ALPHA_GROUP_KEY)).toBeVisible();
  await expect(groupHeader(page, BETA_GROUP_KEY)).toBeVisible();
});
