import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedSession } from "./support";

// P1-7（composer `@` 工作区文件引用 + 全库模糊搜索）端到端验收：
// - `@` 打开引用菜单时首屏来自 `fs/list`（P0 链路），输入关键字后改由 `fs/search` 远端检索；
// - mock 夹具把命中刻意放在**子目录**（`src/lib/composer-menu.ts`），且根层首屏列表里没有它，
//   因此「输入关键字后出现」只能来自跨目录搜索，而不是首屏客户端过滤的残留；
// - 点选后草稿补全为 `@<相对路径>`：引用 token 原样插入（不追加空格、不被改写）。
//
// 断言只依赖 `data-composer-menu*` 稳定钩子与请求参数，不绑定内部 DOM 与实现细节。
// 文案断言依赖默认语言为 en-US（EMPTY_QUERY_COPY / "Workspace files"）；若以后在 fixtures
// 里切换语言，需同步改为从 i18n 资源取常量。

const SESSION_ID = "e2e-composer-reference";
const SEARCH_HIT_QUERY = "composer-menu.ts";
const SEARCH_HIT_PATH = "src/lib/composer-menu.ts";
const EMPTY_QUERY_COPY = "No files match; keep typing to narrow down";

const composer = (page: Page) => page.locator(".app-chat-input");
const menu = (page: Page) => page.locator("[data-composer-menu]");
const workspaceGroup = (page: Page) =>
  page.locator('[data-composer-menu-group="workspace-files"]');

/** 记录 `/fs/search` 的查询参数：用网络证据区分「远端检索」与「首屏客户端过滤」。 */
function trackFsSearch(page: Page): URLSearchParams[] {
  const calls: URLSearchParams[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname === "/api/runtime/fs/search") {
      calls.push(url.searchParams);
    }
  });
  return calls;
}

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  await seedSession(page.request, { id: SESSION_ID, title: "composer reference" });
  await page.goto(`/workspace/chats/${SESSION_ID}`);
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });
});

test("P1-7: `@` 首屏给工作区分组，输入关键字走远端搜索并插入子目录文件引用", async ({
  page,
}) => {
  const searchCalls = trackFsSearch(page);

  // 1) `@` 打开引用菜单：工作区分组（首屏来自 fs/list）必须就位。
  await composer(page).fill("@");
  await expect(menu(page)).toBeVisible();
  await expect(workspaceGroup(page)).toBeVisible();
  await expect(workspaceGroup(page)).toContainText("Workspace files");

  // 2) 关键字只在子目录命中：必须真正打搜索端点（作用域由 fs/roots 解析）。
  await composer(page).fill(`@${SEARCH_HIT_QUERY}`);
  const hit = menu(page).locator("[data-composer-menu-item]", { hasText: SEARCH_HIT_PATH });
  await expect(hit.first()).toBeVisible();

  await expect
    .poll(() => searchCalls.filter((params) => params.get("q") === SEARCH_HIT_QUERY).length)
    .toBeGreaterThan(0);
  const searchParams = searchCalls.filter((call) => call.get("q") === SEARCH_HIT_QUERY).at(-1);
  expect(searchParams?.get("scope")).toBe("session:e2e-workspace");
  expect(searchParams?.get("kinds")).toBe("file");
  // 空 query 不打搜索端点（空 `@` 仍走首屏列表）。
  expect(searchCalls.every((call) => (call.get("q") ?? "").length > 0)).toBe(true);

  // 未命中的根层文件必须从结果里消失：证明当前候选来自远端过滤，而不是首屏残留。
  await expect(menu(page)).not.toContainText("notes.txt");

  // 3) 点选：草稿补全为 `@src/lib/composer-menu.ts`，菜单收起。
  await hit.first().click();
  await expect(composer(page)).toHaveValue(`@${SEARCH_HIT_PATH}`);
  await expect(menu(page)).toHaveCount(0);
});

test("P1-7: 搜索无命中时如实给空态文案，不伪造候选", async ({ page }) => {
  await composer(page).fill("@");
  await expect(workspaceGroup(page)).toBeVisible();

  await composer(page).fill("@zzz-no-such-file");
  const note = workspaceGroup(page).locator('[data-composer-menu-group-note="workspace-files"]');
  await expect(note).toContainText(EMPTY_QUERY_COPY);
  await expect(menu(page).locator("[data-composer-menu-item]")).toHaveCount(0);
});
