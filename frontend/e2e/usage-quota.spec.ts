import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState } from "./support";

// P2-1A e2e：/analytics 页「用量与配额」面板 → GET /api/runtime/usage/{stats,policy,ledger}。
// 覆盖真实响应链路：全局聚合（无配额快照）→ 账本筛选重取 → 切具体作用域（余量 + 生效来源）
// → 未配置上限的作用域如实显示「无配额快照」→ 403 时提示补 admin token（不伪造数据）。

const ADMIN_TOKEN = "e2e-admin-token";
const ADMIN_TOKEN_KEY = "runtime.logs.adminToken";

// analytics 端点不在本用例范围内：用最小合法载荷桩掉，避免面板被邻域错误横幅干扰。
const emptyTotals = {
  sessions: 0,
  total_requests: 0,
  total_responses: 0,
  total_tool_calls: 0,
  llm_requests: 0,
  llm_successes: 0,
  llm_errors: 0,
  turns: 0,
  failed_turns: 0,
  recovered_turns: 0,
  tool_results_observed: 0,
  tool_errors: 0,
  total_duration_ms: 0,
  total_tokens: 0,
  prompt_tokens: 0,
  completion_tokens: 0,
  cached_tokens: 0,
  reasoning_tokens: 0,
};

const emptyCoverage = {
  sessions: 0,
  sessions_with_usage: 0,
  usage_session_rate: 0,
  llm_requests: 0,
  llm_requests_with_usage: 0,
  usage_request_rate: 0,
  tool_results_observed: 0,
  dropped_messages: 0,
};

const analyticsMeta = {
  schema_version: "runtime.analytics.v1",
  generated_at: "2026-09-13T10:00:00Z",
  data_window: {},
  coverage: emptyCoverage,
  partial: false,
  partial_reasons: [],
};

async function stubAnalytics(page: Page) {
  await page.route("**/api/runtime/analytics/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const body = path.endsWith("/sessions")
      ? {
          ...analyticsMeta,
          sessions: [],
          count: 0,
          total: 0,
          limit: 50,
          offset: 0,
          scanned: 0,
          totals: emptyTotals,
        }
      : path.endsWith("/overview")
        ? {
            ...analyticsMeta,
            group_by: "day",
            totals: emptyTotals,
            groups: [],
            scanned: 0,
            matched: 0,
          }
        : {
            schema_version: "runtime.analytics.v1",
            generated_at: "2026-09-13T10:00:00Z",
            providers: [],
            models: [],
            directories: [],
            projects: [],
            statuses: [],
          };
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(body),
    });
  });
}

async function gotoAnalytics(page: Page) {
  await stubAnalytics(page);
  await page.goto("/analytics");
  const panel = page.getByRole("region", { name: "Usage and quota" });
  await expect(panel).toBeVisible({ timeout: 30_000 });
  return panel;
}

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  await page.addInitScript(
    ([key, value]) => window.localStorage.setItem(key, value),
    [ADMIN_TOKEN_KEY, ADMIN_TOKEN],
  );
});

test("全局聚合→账本筛选→作用域余量：全部来自 /usage 真实响应", async ({ page }) => {
  const statsRequests: string[] = [];
  page.on("request", (request) => {
    const url = request.url();
    if (url.includes("/api/runtime/usage/stats")) {
      statsRequests.push(url);
    }
  });

  const panel = await gotoAnalytics(page);
  const ledgerRows = panel
    .locator("article")
    .filter({ hasText: "Token ledger" })
    .locator("tbody");

  // 策略摘要 + 全局用量（后端不提供货币成本字段，面板也不显示）。
  await expect(panel).toContainText("Usage policy");
  await expect(panel).toContainText("Usage tracking");
  await expect(panel).toContainText("100 requests / 50,000 tokens");
  await expect(panel).toContainText("2 recorded scopes · 1 recorded users");
  await expect(panel).toContainText("10,000");
  await expect(panel).not.toContainText("$");

  // 全局聚合不带配额快照：如实提示，而不是画一根假进度条。
  await expect(panel).toContainText("Pick a concrete scope to see remaining quota");
  await expect(
    statsRequests.some((url) => new URL(url).searchParams.get("user_id") === null),
  ).toBe(true);

  // 账本：默认全局 2 条，筛选后按入口重取。
  await expect(panel).toContainText("2 matched (limit 50)");
  await expect(ledgerRows).toContainText("ledger-skill");
  await expect(ledgerRows).toContainText("1,200");

  await panel.getByPlaceholder("e.g. execute / agent_chat").fill("agent_chat");
  await panel.getByRole("button", { name: "Apply filters" }).click();
  await expect(panel).toContainText("1 matched (limit 50)");
  await expect(ledgerRows).toContainText("agent_chat");
  await expect(ledgerRows).not.toContainText("ledger-skill");

  // 切到具体作用域：拿到余量与生效来源。
  await panel.getByRole("button", { name: "Scope" }).click();
  await page.getByRole("option", { name: "alice", exact: true }).click();
  await expect(panel).toContainText("91 / 100");
  await expect(panel).toContainText("41,500 / 50,000");
  await expect(panel).toContainText("Resolved from");
  await expect(panel).toContainText("User");
  await expect
    .poll(() => statsRequests.some((url) => url.includes("user_id=alice")))
    .toBe(true);

  // 未配置上限的作用域：如实显示「无配额快照」，不伪造 0 余量。
  await panel.getByRole("button", { name: "Scope" }).click();
  await page.getByRole("option", { name: "tenant-a", exact: true }).click();
  await expect(panel).toContainText("This endpoint returned no quota snapshot.");
  await expect(panel).not.toContainText("91 / 100");
});

test("用量接口 403 时提示补 admin token，不显示伪造数据", async ({ page }) => {
  await page.route("**/api/runtime/usage/**", async (route) => {
    await route.fulfill({
      status: 403,
      contentType: "application/json",
      body: JSON.stringify({ error: "forbidden" }),
    });
  });

  const panel = await gotoAnalytics(page);

  await expect(panel).toContainText("Provide an admin token to read usage and quota");
  await expect(panel).toContainText("Ledger data is unavailable.");
  await expect(panel).not.toContainText("91 / 100");
  await expect(panel).not.toContainText("1,200");
  await expect(panel).not.toContainText("$");
});
