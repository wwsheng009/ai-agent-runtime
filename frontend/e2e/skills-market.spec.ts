import type { Locator, Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState } from "./support";

// P2-1A e2e：/runtime/skills 页 → GET /api/runtime/skills{,/search,/stats,/hot-reload/*}。
// 覆盖真实响应链路：目录（含坏条目）→ 检索（含模式降级）→ 详情 → 统计 / policy；
// 热重载未配置 503 如实呈现 → 启动 / 停止 / 重载全部走真实 POST 带管理令牌 → 403 提示补令牌；
// 目录端点 404 时不伪装空市场，policy 禁用时按钮不可点。

const ADMIN_TOKEN = "e2e-skills-token";
const ADMIN_TOKEN_KEY = "runtime.logs.adminToken";

async function gotoSkills(page: Page) {
  await page.goto("/runtime/skills");
  await expect(page.getByRole("heading", { name: "Skills / hot reload" })).toBeVisible({
    timeout: 30_000,
  });
}

/** 读「标签 + 值」卡片的值：label 与 value 是相邻兄弟节点（skills/*.tsx 的 StatValue）。 */
function statValue(panel: Locator, label: string): Locator {
  return panel.getByText(label, { exact: true }).locator("xpath=following-sibling::div[1]");
}

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  await page.addInitScript(
    ([key, value]) => window.localStorage.setItem(key, value),
    [ADMIN_TOKEN_KEY, ADMIN_TOKEN] as const,
  );
});

test("目录 / 检索 / 详情 / 统计：全部来自 /api/runtime/skills 真实响应", async ({ page }) => {
  const skillRequests: string[] = [];
  page.on("request", (request) => {
    const url = request.url();
    if (url.includes("/api/runtime/skills")) {
      skillRequests.push(url);
    }
  });

  await gotoSkills(page);

  const catalog = page.getByRole("region", { name: "Skill catalog" });
  await expect(catalog).toBeVisible();

  // 后端上报 count=3（含一条缺 name 的坏条目）：只丢弃坏行，不按数组长度改写计数，
  // 也不因此显示「空市场」。
  await expect(catalog.getByTestId("skills-catalog-count")).toHaveText("3 skills");
  await expect(catalog.getByTestId("skills-row")).toHaveCount(2);
  await expect(catalog.getByTestId("skills-list")).toContainText("code-review");
  await expect(catalog.getByTestId("skills-list")).toContainText("quality · v1.2.0");
  await expect(catalog.getByTestId("skills-row-source").first()).toHaveText("project · D:/skills");
  await expect(catalog.getByTestId("skills-catalog-empty")).toHaveCount(0);

  // 统计面板：数值来自 /skills/stats，embedding 关闭如实显示 Disabled。
  const stats = page.getByRole("region", { name: "Skill stats" });
  await expect(stats.getByTestId("skills-stats-total")).toHaveText("3");
  await expect(stats.getByTestId("skills-stats-dirs")).toHaveText("D:/skills, E:/shared/skills");
  await expect(
    stats.getByText("Embedding search", { exact: true }).locator("xpath=following-sibling::div[1]"),
  ).toHaveText("Disabled");
  await expect(stats.getByTestId("skills-stats-policy")).toContainText("Read only: off");
  await expect(stats.getByTestId("skills-stats-policy")).toContainText("Hot reload disabled: off");
  await expect(stats).toContainText("project: 2");
  await expect(stats.getByTestId("skills-stats-rows")).toContainText("code-review");
  await expect(stats.getByTestId("skills-stats-rows")).toContainText("12");
  await expect(stats.getByTestId("skills-stats-rows")).toContainText("92%");
  await expect(stats.getByTestId("skills-stats-rows")).toContainText("1.2s");

  // 选中目录行 → 详情面板重新拉取 GET /skills/{name}，工作流 / 触发器 / 提示词原样呈现。
  await catalog.getByTestId("skills-row").filter({ hasText: "code-review" }).click();
  const detail = page.getByTestId("skills-detail");
  await expect(detail.getByTestId("skills-detail-name")).toHaveText("code-review");
  await expect(detail.getByTestId("skills-detail-workflow")).toContainText("scan · read_file");
  await expect(detail.getByTestId("skills-detail-workflow")).toContainText("Depends on scan");
  await expect(detail).toContainText("Weight 0.9");
  await expect(detail).toContainText("You review code changes and report risks.");
  await expect(detail).not.toContainText("Loading skill detail");
  await expect
    .poll(() => skillRequests.some((url) => url.endsWith("/api/runtime/skills/code-review")))
    .toBe(true);

  // 检索：命中数 / 解析模式来自响应本体。
  await catalog.getByTestId("skills-search-input").fill("review");
  await catalog.getByRole("button", { name: "Search" }).click();
  await expect(catalog.getByTestId("skills-search-summary")).toHaveText(
    "“review” matched 1 results",
  );
  await expect(catalog).toContainText("Resolved mode: lexical");
  await expect(catalog).toContainText("Lexical only");
  await expect(catalog.getByTestId("skills-row")).toHaveCount(1);
  await expect
    .poll(() => skillRequests.some((url) => url.includes("q=review")))
    .toBe(true);

  // embedding 关闭时请求 semantic：后端 resolved_mode=lexical，UI 显示真实模式，
  // 不把降级结果假装成语义命中。
  await catalog.getByTestId("skills-mode-semantic").click();
  await catalog.getByRole("button", { name: "Search" }).click();
  await expect
    .poll(() => skillRequests.some((url) => url.includes("q=review") && url.includes("mode=semantic")))
    .toBe(true);
  await expect(catalog).toContainText("Resolved mode: lexical");
  await expect(catalog).not.toContainText("Embedding search used");

  // 回到目录：检索态清空，目录行恢复。
  await catalog.getByRole("button", { name: "Back to catalog" }).click();
  await expect(catalog.getByTestId("skills-row")).toHaveCount(2);
});

test("热重载：未配置 503 如实呈现 → 启动 / 停止 / 重载走真实 POST", async ({ page }) => {
  const posts: Array<{ path: string; token: string | undefined; body: unknown }> = [];
  page.on("request", (request) => {
    if (request.method() !== "POST" || !request.url().includes("/api/runtime/skills/hot-reload/")) {
      return;
    }
    posts.push({
      path: new URL(request.url()).pathname,
      token: request.headers()["x-skills-admin-token"],
      body: request.postDataJSON(),
    });
  });

  await gotoSkills(page);

  const panel = page.getByTestId("skills-hot-reload");
  // 未配置热重载：如实报「端点不可用」，且不出现任何 watching 卡片（不伪造 false）。
  await expect(panel.getByTestId("skills-hot-reload-error")).toContainText(
    "This capability is not enabled in the current runtime",
  );
  await expect(panel.getByText("Watching", { exact: true })).toHaveCount(0);

  await panel.getByTestId("skills-hot-reload-dirs").fill("D:/skills\nE:/shared/skills");
  await panel.getByTestId("skills-hot-reload-debounce").fill("250");
  await panel.getByTestId("skills-hot-reload-start").click();

  await expect(statValue(panel, "Watching")).toHaveText("Yes");
  await expect(statValue(panel, "Loaded skills")).toHaveText("2");
  await expect(statValue(panel, "Watched directories")).toHaveText("D:/skills, E:/shared/skills");
  await expect(statValue(panel, "Debounce")).toHaveText("250ms");
  await expect(panel.getByTestId("skills-hot-reload-error")).toHaveCount(0);

  await panel.getByTestId("skills-hot-reload-stop").click();
  await expect(statValue(panel, "Watching")).toHaveText("No");

  await panel.getByTestId("skills-hot-reload-reload").click();
  await expect(statValue(panel, "Callbacks")).toHaveText("1");

  expect(posts.map((post) => post.path)).toEqual([
    "/api/runtime/skills/hot-reload/start",
    "/api/runtime/skills/hot-reload/stop",
    "/api/runtime/skills/hot-reload/reload",
  ]);
  expect(posts[0]?.token).toBe(ADMIN_TOKEN);
  expect(posts[0]?.body).toEqual({
    dirs: ["D:/skills", "E:/shared/skills"],
    debounce_ms: 250,
  });
});

test("写操作 403：提示补管理令牌，不显示伪状态", async ({ page }) => {
  // 覆盖 beforeEach 的种子：令牌错误时后端拒绝写操作。
  await page.addInitScript(
    ([key, value]) => window.localStorage.setItem(key, value),
    [ADMIN_TOKEN_KEY, "wrong-token"] as const,
  );

  await gotoSkills(page);

  const panel = page.getByTestId("skills-hot-reload");
  await panel.getByTestId("skills-hot-reload-dirs").fill("D:/skills");
  await panel.getByTestId("skills-hot-reload-start").click();

  const actionError = panel.getByTestId("skills-hot-reload-action-error");
  await expect(actionError).toContainText("Request rejected (403)");
  await expect(actionError).toContainText("Set the admin token on the logs page and retry.");
  await expect(
    actionError.getByRole("link", { name: "Open logs page to set the token" }),
  ).toHaveAttribute("href", "/logs");

  // 被拒绝后依然是「未配置」态：没有 watching 卡片，也没有假的「已启动」。
  await expect(panel.getByText("Watching", { exact: true })).toHaveCount(0);
  await expect(panel.getByTestId("skills-hot-reload-error")).toContainText(
    "This capability is not enabled in the current runtime",
  );
});

test("端点不可用 / 策略禁用：不伪装空市场，热重载按钮不可点", async ({ page }) => {
  // 目录端点 404：如实报「未启用」，且不显示空态文案（0 行 ≠ 空市场）。
  await page.route("**/api/runtime/skills", async (route) => {
    await route.fulfill({
      status: 404,
      contentType: "application/json",
      body: JSON.stringify({ error: "not found" }),
    });
  });
  // stats 端点：策略明确禁用热重载；embedding 缺席 → 显示 Unknown（不按 false 渲染）。
  await page.route("**/api/runtime/skills/stats**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        stats: [],
        total_skills: 0,
        skill_dirs: [],
        source_summary: {},
        mutation_policy: {
          read_only: false,
          disable_import: false,
          disable_persist: false,
          disable_reload_ops: false,
          disable_hot_reload: true,
        },
        embedding: null,
      }),
    });
  });

  await gotoSkills(page);

  const catalog = page.getByRole("region", { name: "Skill catalog" });
  await expect(catalog.getByTestId("skills-catalog-error")).toContainText(
    "This capability is not enabled in the current runtime",
  );
  await expect(catalog.getByTestId("skills-catalog-empty")).toHaveCount(0);
  await expect(catalog.getByTestId("skills-list")).toHaveCount(0);

  const stats = page.getByRole("region", { name: "Skill stats" });
  await expect(stats.getByTestId("skills-stats-policy")).toContainText("Hot reload disabled: on");
  await expect(
    stats.getByText("Embedding search", { exact: true }).locator("xpath=following-sibling::div[1]"),
  ).toHaveText("Unknown");

  const panel = page.getByTestId("skills-hot-reload");
  await expect(panel.getByTestId("skills-hot-reload-policy")).toContainText(
    "The mutation policy disables hot reload",
  );
  await expect(panel.getByTestId("skills-hot-reload-start")).toBeDisabled();
  await expect(panel.getByTestId("skills-hot-reload-stop")).toBeDisabled();
  await expect(panel.getByTestId("skills-hot-reload-reload")).toBeDisabled();
});
