import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState } from "./support";

// 批次 7.2 e2e：/usage 三处观测视图（不新增路由，全部在既有页面内）。
//   1) 会话详情 tab：overview/tokens/tools/subagents/diagnostics —— 切 tab 只改 URL，
//      面板数据在 tab 激活时才拉取；空库（空数组）→「暂无数据」，不显示空图、不报 500。
//   2) 失败模式下钻：错误模式行 → ?tab=diagnostics&error_category=…（URL 可分享）。
//   3) Overview 采集健康横幅：runtime.usage_analytics.attached=false（批次 3.2 快照）。
//   4) 403：面板以 role="alert" 如实呈现鉴权失败，不伪造「空数据」。
//
// 所有 /api/runtime/analytics/** 请求在本用例内打桩（照 usage-quota.spec.ts 模式），
// 断言前端自身的 tab / 降级 / 下钻行为，不依赖真实分析库内容。

const SESSION_ID = "e2e-observability-session";
const ADMIN_TOKEN = "e2e-admin-token";
const ADMIN_TOKEN_KEY = "runtime.logs.adminToken";

const ANALYTICS_META = {
  schema_version: "usage.analytics.v2",
  generated_at: "2026-09-17T00:00:00Z",
};

const EMPTY_COVERAGE = {
  sessions: 0,
  sessions_with_usage: 0,
  usage_session_rate: 0,
  llm_requests: 0,
  llm_requests_with_usage: 0,
  usage_request_rate: 0,
  tool_results_observed: 0,
  dropped_messages: 0,
};

const EMPTY_TOTALS = {
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

const EMPTY_TOOL_STAT = {
  tool_name: "all",
  calls: 0,
  failures: 0,
  failure_rate: 0,
  empty_results: 0,
  retried_calls: 0,
  average_duration_ms: 0,
  p50_duration_ms: 0,
  p95_duration_ms: 0,
};

const EMPTY_SUBAGENT_SUMMARY = {
  total: 0,
  succeeded: 0,
  failed: 0,
  unknown: 0,
  failure_rate: 0,
  timeouts: 0,
  retried: 0,
  failure_categories: {},
  sources: {},
};

function sessionDetail() {
  return {
    ...ANALYTICS_META,
    coverage: EMPTY_COVERAGE,
    partial: false,
    partial_reasons: [],
    session: {
      session_id: SESSION_ID,
      title: "Observability session",
      directory: "e2e",
      rel_path: "e2e",
      status: "completed",
      provider: "e2e-provider",
      model: "e2e-model",
      total_requests: 0,
      total_responses: 0,
      total_tool_calls: 0,
      total_tokens: 120,
      prompt_tokens: 100,
      completion_tokens: 20,
      cached_tokens: 0,
      reasoning_tokens: 0,
      llm_requests: 2,
      llm_requests_with_usage: 2,
      llm_errors: 0,
      turn_count: 1,
      failed_turns: 0,
      recovered_turns: 0,
      tool_results_observed: 0,
      tool_errors: 0,
      total_duration_ms: 1000,
      usage_quality: "provider_reported",
      usage_complete: true,
      usage_coverage: 1,
      partial: false,
      partial_reasons: [],
      dropped_messages: 0,
      reconciliation_status: "matched",
      reconciliation_delta: 0,
      start_time: "2026-09-17T00:00:00Z",
    },
    steps: [],
    step_count: 0,
    turns: [],
    diagnostics: [],
    error_categories: {},
  };
}

type StubOptions = {
  toolsForbidden?: boolean;
  errorPatterns?: { error_code: string; failure_category: string; source: string; count: number }[];
};

async function stubAnalytics(page: Page, options: StubOptions = {}) {
  await page.route("**/api/runtime/analytics/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (options.toolsForbidden && path.endsWith("/tools")) {
      await route.fulfill({
        status: 403,
        contentType: "application/json",
        body: JSON.stringify({ error: "admin token required", code: "forbidden" }),
      });
      return;
    }
    const body = path.endsWith("/tools")
      ? { ...ANALYTICS_META, tools: [], totals: EMPTY_TOOL_STAT }
      : path.endsWith("/subagents")
        ? { ...ANALYTICS_META, summary: EMPTY_SUBAGENT_SUMMARY, subagents: [] }
        : path.endsWith("/errors")
          ? { ...ANALYTICS_META, patterns: options.errorPatterns ?? [] }
          : path.endsWith("/sessions")
            ? {
                ...ANALYTICS_META,
                coverage: EMPTY_COVERAGE,
                partial: false,
                partial_reasons: [],
                sessions: [],
                count: 0,
                total: 0,
                limit: 50,
                offset: 0,
                scanned: 0,
                totals: EMPTY_TOTALS,
              }
            : path.endsWith("/overview")
              ? {
                  ...ANALYTICS_META,
                  coverage: EMPTY_COVERAGE,
                  partial: false,
                  partial_reasons: [],
                  group_by: "day",
                  totals: EMPTY_TOTALS,
                  groups: [],
                  scanned: 0,
                  matched: 0,
                }
              : path.includes("/sessions/")
                ? sessionDetail()
                : {
                    ...ANALYTICS_META,
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

  await page.route("**/api/runtime/status**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        runtime: {
          usage_analytics: {
            attached: false,
            db_path: "C:/e2e/usage_analytics.sqlite",
            ingested_total: 0,
            conflict_total: 0,
            last_ingest_at: null,
            degraded: true,
          },
        },
      }),
    });
  });

  // 会话详情内嵌缓存面板：只求不干扰断言，给最小合法载荷。
  await page.route("**/api/runtime/sessions/**/cache/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const body = path.endsWith("/capabilities")
      ? {
          schema_version: "cache.analytics.v1",
          data_source: "session_runtime",
          max_requests_per_session: 2000,
          supports_sse: false,
        }
      : path.endsWith("/overview")
        ? {
            schema_version: "cache.analytics.v1",
            session_id: SESSION_ID,
            generated_at: ANALYTICS_META.generated_at,
            requests_total: 0,
            requests_with_usage: 0,
            requests_cache_reported: 0,
            tokens: {
              prompt_tokens: 0,
              completion_tokens: 0,
              total_tokens: 0,
              cache_read_tokens: 0,
              cache_creation_tokens: 0,
              reasoning_tokens: 0,
            },
            cache_status_distribution: {
              hit: 0,
              write: 0,
              reported_zero: 0,
              not_reported: 0,
              error: 0,
            },
            coverage: {},
          }
        : {
            schema_version: "cache.analytics.v1",
            session_id: SESSION_ID,
            total: 0,
            limit: 50,
            offset: 0,
            requests: [],
          };
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(body),
    });
  });

  await page.route("**/api/runtime/usage/**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        tracking_enabled: false,
        ledger_enabled: false,
        quota_enabled: false,
        policy: {},
        usage: {},
        records: [],
        count: 0,
      }),
    });
  });
}

async function gotoSession(page: Page, options: StubOptions = {}) {
  await stubAnalytics(page, options);
  await page.addInitScript(
    ([key, value]) => window.localStorage.setItem(key, value),
    [ADMIN_TOKEN_KEY, ADMIN_TOKEN],
  );
  await page.goto(`/usage/sessions/${SESSION_ID}`);
  await expect(page.getByRole("tab", { name: /^(Overview|概览)$/ })).toBeVisible({
    timeout: 30_000,
  });
}

async function gotoOverview(page: Page) {
  await stubAnalytics(page);
  await page.goto("/usage");
  await expect(page.getByTestId("usage-analytics-health-banner")).toBeVisible({
    timeout: 30_000,
  });
}

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
});

test("会话观测 tab：切 tab 只改 URL，空库面板渲染「暂无数据」", async ({ page }) => {
  const analyticsRequests: string[] = [];
  page.on("request", (request) => {
    const url = request.url();
    if (url.includes("/api/runtime/analytics/")) {
      analyticsRequests.push(new URL(url).pathname);
    }
  });

  await gotoSession(page);

  // 首屏（overview）不拉观测端点：懒加载在 tab 激活时才发生。
  expect(analyticsRequests.some((path) => path.endsWith("/tools"))).toBe(false);
  expect(analyticsRequests.some((path) => path.endsWith("/subagents"))).toBe(false);

  await page.getByRole("tab", { name: /^(Tools|工具)$/ }).click();
  await expect(page).toHaveURL(new RegExp("[?&]tab=tools"));
  const toolEmpty = page.getByTestId("tool-stats-empty");
  await expect(toolEmpty).toBeVisible();
  await expect(toolEmpty).toHaveText(/No data yet|暂无数据/);
  expect(analyticsRequests.some((path) => path.endsWith("/tools"))).toBe(true);

  await page.getByRole("tab", { name: /^(Subagents|子代理)$/ }).click();
  await expect(page).toHaveURL(new RegExp("[?&]tab=subagents"));
  const subagentEmpty = page.getByTestId("subagent-stats-empty");
  await expect(subagentEmpty).toBeVisible();
  await expect(subagentEmpty).toHaveText(/No data yet|暂无数据/);
  expect(analyticsRequests.some((path) => path.endsWith("/subagents"))).toBe(true);

  await page.getByRole("tab", { name: /^(Failures|失败诊断)$/ }).click();
  await expect(page).toHaveURL(new RegExp("[?&]tab=diagnostics"));
  const patternEmpty = page.getByTestId("error-patterns-empty");
  await expect(patternEmpty).toBeVisible();
  await expect(patternEmpty).toHaveText(/No data yet|暂无数据/);
  expect(analyticsRequests.some((path) => path.endsWith("/errors"))).toBe(true);

  // 刷新后 tab 由 URL 复现（不落 localStorage / 组件内部状态）。
  await page.reload();
  await expect(page.getByTestId("error-patterns-empty")).toBeVisible({ timeout: 30_000 });
});

test("失败模式下钻：错误模式行写回 ?tab=diagnostics&error_category=", async ({ page }) => {
  await gotoSession(page, {
    errorPatterns: [
      {
        error_code: "deadline_exceeded",
        failure_category: "timeout",
        source: "subagents",
        count: 2,
      },
    ],
  });

  await page.getByRole("tab", { name: /^(Failures|失败诊断)$/ }).click();
  const drilldown = page.locator('button[aria-label*="deadline_exceeded"]');
  await expect(drilldown).toBeVisible();
  await drilldown.click();

  await expect(page).toHaveURL(/tab=diagnostics/);
  await expect(page).toHaveURL(/error_category=timeout/);
  // 诊断 tab 出现失败分类过滤条（清除筛选按钮为该状态独有）。
  await expect(
    page.getByRole("button", { name: /Clear filter|清除筛选/ }),
  ).toBeVisible();
});

test("Overview：attached=false 显示采集健康横幅并链到 /logs", async ({ page }) => {
  await gotoOverview(page);

  const banner = page.getByTestId("usage-analytics-health-banner");
  await expect(banner).toBeVisible();
  await expect(banner).toContainText("C:/e2e/usage_analytics.sqlite");
  await expect(page.getByRole("link", { name: /Open logs|查看日志/ })).toHaveAttribute(
    "href",
    "/logs",
  );
});

test("鉴权失败：观测面板以 role=alert 呈现 403，不伪造空数据", async ({ page }) => {
  await gotoSession(page, { toolsForbidden: true });

  await page.getByRole("tab", { name: /^(Tools|工具)$/ }).click();
  const alert = page.getByRole("alert").first();
  await expect(alert).toBeVisible();
  await expect(alert).toContainText("403");
  await expect(page.getByTestId("tool-stats-empty")).toHaveCount(0);
});
