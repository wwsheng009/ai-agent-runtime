import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedLanguage } from "./support";

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
  min_duration_ms: 0,
  max_duration_ms: 0,
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
  /** 按 /errors 的请求 query 回放不同失败模式（用于断言「失败分类分布跟随筛选」）。 */
  errorPatternsForQuery?: (
    params: URLSearchParams,
  ) => { error_code: string; failure_category: string; source: string; count: number }[] | undefined;
  /** 收集 /errors 请求的 query 字符串（断言参数透传）。 */
  errorQueries?: string[];
};

async function stubAnalytics(page: Page, options: StubOptions = {}) {
  const meta = { ...ANALYTICS_META, coverage: EMPTY_COVERAGE, partial: false, partial_reasons: [] };
  const sessions = {
    ...meta, sessions: [], count: 0, total: 0, limit: 50, offset: 0, scanned: 0, totals: EMPTY_TOTALS,
  };
  const summary = {
    ...meta, group_by: "day", totals: EMPTY_TOTALS, groups: [], scanned: 0, matched: 0,
  };
  const dimensions = {
    ...ANALYTICS_META, providers: [], models: [], directories: [], projects: [], statuses: [],
  };
  await page.route("**/api/runtime/analytics/**", async (route) => {
    const requestUrl = new URL(route.request().url());
    const path = requestUrl.pathname;
    if (options.toolsForbidden && path.endsWith("/tools")) {
      await route.fulfill({
        status: 403,
        contentType: "application/json",
        body: JSON.stringify({ error: "admin token required", code: "forbidden" }),
      });
      return;
    }
    let errorPatterns = options.errorPatterns ?? [];
    if (path.endsWith("/errors")) {
      options.errorQueries?.push(requestUrl.searchParams.toString());
      errorPatterns = options.errorPatternsForQuery?.(requestUrl.searchParams) ?? errorPatterns;
    }
    const body = path.endsWith("/tools")
      ? { ...ANALYTICS_META, tools: [], totals: EMPTY_TOOL_STAT }
      : path.endsWith("/subagents")
        ? { ...ANALYTICS_META, summary: EMPTY_SUBAGENT_SUMMARY, subagents: [] }
        : path.endsWith("/errors")
          ? { ...ANALYTICS_META, patterns: errorPatterns }
          : path.endsWith("/sessions")
            ? sessions
            : path.endsWith("/overview")
              // 首屏端点已合并为 sessions / summary / dimensions，不再是 summary 本身。
              ? { ...ANALYTICS_META, sessions, summary, dimensions, matched: 0 }
              : path.endsWith("/summary")
                ? summary
                : path.includes("/sessions/")
                  ? sessionDetail()
                  : dimensions;
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

  const response = page.waitForResponse((result) =>
    new URL(result.url()).pathname === "/api/runtime/analytics/tools",
  );
  await page.getByRole("tab", { name: /^(Tools|工具)$/ }).click();
  expect((await response).status()).toBe(403);
  const alert = page.getByRole("alert").first();
  await expect(alert).toBeVisible();
  await expect(alert).toContainText("admin token required");
  await expect(page.getByTestId("tool-stats-empty")).toHaveCount(0);
});

for (const locale of ["zh-CN", "en-US"]) {
  test(`路由事件明细：长护栏保持紧凑，完整内容在右侧面板展示（${locale}）`, async ({ page }) => {
    await stubAnalytics(page);
    await seedLanguage(page, locale);
    const warnings = [
      `difficulty_promoted_by_keyword:${"long-keyword-".repeat(40)}`,
      "difficulty_floor_by_task_type:security",
      `unknown_guardrail:${"x".repeat(800)}`,
    ];
    const longEvent = {
      recorded_at: "2026-09-27T08:00:00Z",
      session_id: SESSION_ID,
      parent_session_id: "parent-session-full-id",
      child_session_id: "child-session-full-id",
      agent_id: `agent-${"long-identifier-".repeat(10)}`,
      scope: "subagent",
      kind: "warning",
      role: "verifier",
      task_type: "verify",
      task_subject: "Task subject that belongs in the details panel",
      goal: "Full task goal: ".repeat(60),
      step: 1,
      reason: "resolved",
      source: "explicit_promoted",
      difficulty: "hard",
      difficulty_source: "explicit_promoted",
      provider: "mock-provider",
      model: `model-${"long-name-".repeat(20)}`,
      reasoning_effort: "high",
      route_changed: true,
      fallback_used: true,
      fallback_reason: "Full fallback reason: ".repeat(60),
      candidate_count: 2,
      warnings,
      attempt: 1,
      max_attempts: 2,
    };
    let routingReads = 0;
    await page.route("**/api/runtime/analytics/routing**", async (route) => {
      routingReads += 1;
      const body = new URL(route.request().url()).pathname.endsWith("/events")
        ? {
            ...ANALYTICS_META,
            events: [
              { ...longEvent, agent_id: "short-agent", kind: "applied", model: "short-model", warnings: [], fallback_used: false },
              longEvent,
            ],
            count: 2,
            limit: 50,
            offset: 0,
          }
        : {
            ...ANALYTICS_META,
            totals: { total: 2, main_agent: 0, subagent: 2, applied: 1, cleared: 0, warnings: 1, route_changed: 2, fallback_used: 1, candidate_total: 4, distinct_sessions: 1, distinct_models: 2 },
            by_scope: [], by_kind: [], by_reason: [], by_source: [], by_provider: [], by_model: [],
            by_difficulty: [], by_difficulty_source: [], by_task_type: [], by_role: [], warnings: [],
            sampled: false, sample_size: 2,
          };
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
    });
    await page.goto("/usage");
    await expect(page.getByTestId("usage-analytics-health-banner")).toBeVisible();
    expect(routingReads).toBe(0);
    await page.getByRole("tab", { name: /^(Routing|路由观测)$/ }).click();
    await expect(page).toHaveURL(/[?&]view=routing/);
    const panel = page.getByTestId("routing-observability-panel");
    const rows = panel.locator("tbody tr");
    await expect(rows).toHaveCount(2);
    await rows.last().scrollIntoViewIfNeeded();

    const heights = await rows.evaluateAll((elements) => elements.map((element) => element.getBoundingClientRect().height));
    expect(heights[1]).toBeGreaterThan(0);
    expect(heights[1]).toBeLessThanOrEqual(52);
    expect(Math.abs(heights[1]! - heights[0]!)).toBeLessThanOrEqual(1);
    const longRow = rows.last();
    await expect(longRow.locator("td").nth(8)).toHaveText(locale === "zh-CN" ? "3 条" : "3");
    for (const warning of warnings) await expect(longRow).not.toContainText(warning);
    await expect(longRow).not.toContainText(longEvent.task_subject);
    await expect(longRow).not.toContainText(longEvent.goal.trim());
    await expect(longRow).not.toContainText(longEvent.fallback_reason.trim());

    const details = longRow.getByRole("button", { name: /View event details|查看事件明细/ });
    await details.focus();
    await details.press("Enter");
    const dialog = page.getByRole("dialog", { name: /Route event details|路由事件明细/ });
    await expect(dialog).toBeVisible();
    await expect(dialog.locator("li")).toHaveCount(warnings.length);
    for (const warning of warnings) await expect(dialog).toContainText(warning);
    for (const field of [longEvent.agent_id, longEvent.task_subject, longEvent.goal.trim(), longEvent.fallback_reason.trim(), longEvent.model, longEvent.child_session_id]) {
      await expect(dialog).toContainText(field);
    }
    expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth + 1)).toBe(true);
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
    await expect(details).toBeFocused();

    // Clicking the compact warning count opens the same event without needing the action column.
    await longRow.locator("td").nth(8).click();
    await expect(dialog).toBeVisible();
    await dialog.getByRole("button", { name: /Close details|关闭明细/ }).click();
    await expect(dialog).toHaveCount(0);

    await page.setViewportSize({ width: 390, height: 844 });
    await details.click();
    await expect(dialog).toBeVisible();
    const bounds = await dialog.boundingBox();
    expect(bounds?.width).toBeLessThanOrEqual(390);
    expect(bounds?.height).toBeLessThanOrEqual(844);
    expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth + 1)).toBe(true);
    const content = dialog.locator('[tabindex="0"]');
    expect(await content.evaluate((element) => element.scrollWidth <= element.clientWidth + 1)).toBe(true);
    expect(await content.evaluate((element) => element.scrollHeight > element.clientHeight)).toBe(true);
    await dialog.getByRole("button", { name: /Close details|关闭明细/ }).click();
    await expect(dialog).toHaveCount(0);
  });
}

for (const locale of ["zh-CN", "en-US"]) {
  test(`用量分类 Tab：过滤器外置、URL 恢复与窄屏布局（${locale}）`, async ({ page }) => {
    await stubAnalytics(page);
    await seedLanguage(page, locale);
    let overviewReads = 0;
    page.on("request", (request) => {
      if (new URL(request.url()).pathname === "/api/runtime/analytics/overview") overviewReads += 1;
    });
    await page.goto("/usage?provider=openai&q=sample&from=2026-09-01&offset=50");
    await expect(page.getByTestId("usage-analytics-health-banner")).toBeVisible();
    const filters = page.getByRole("form", { name: /Analytics scope and filters|分析范围与筛选/ });
    const tabs = page.getByRole("tablist", { name: /Usage categories|用量信息分类/ });
    await expect(tabs.getByRole("tab")).toHaveCount(6);
    expect(await filters.evaluate((element) => element.closest('[role="tabpanel"]') === null)).toBe(true);

    for (const view of ["overview", "models", "sessions", "quota", "routing", "artifacts"]) {
      const tab = page.locator(`#usage-view-${view}-tab`);
      await tab.click();
      await expect(tab).toHaveAttribute("aria-selected", "true");
      await expect(page.getByRole("tabpanel")).toHaveCount(1);
      await expect(page.getByRole("tabpanel")).toHaveAttribute("id", `usage-view-${view}-panel`);
      await expect(filters).toBeVisible();
      await expect(filters.getByRole("textbox", { name: /^(Search|搜索)$/ })).toHaveValue("sample");
      const query = new URL(page.url()).searchParams;
      expect(query.get("provider")).toBe("openai");
      expect(query.get("from")).toBe("2026-09-01");
      expect(query.get("offset")).toBe("50");
      expect(overviewReads).toBe(1);
    }
    await expect(page.getByRole("tabpanel")).toContainText(/snapshot is unavailable|暂时无法读取工件流快照/);

    await page.locator("#usage-view-models-tab").click();
    await filters.getByRole("button", { name: /Reset filters|重置筛选/ }).click();
    await expect(page).toHaveURL(/\/usage\?view=models$/);
    await expect(filters.getByRole("textbox", { name: /^(Search|搜索)$/ })).toHaveValue("");
    await expect.poll(() => overviewReads).toBe(2);
    await page.locator("#usage-view-routing-tab").click();
    await page.goBack();
    await expect(page.locator("#usage-view-models-tab")).toHaveAttribute("aria-selected", "true");

    await page.reload();
    await expect(page.locator("#usage-view-models-tab")).toHaveAttribute("aria-selected", "true");
    await expect(filters).toBeVisible();
    const filterBounds = await filters.boundingBox();
    const tabBounds = await tabs.boundingBox();
    expect(filterBounds!.y + filterBounds!.height).toBeLessThanOrEqual(tabBounds!.y);

    await page.setViewportSize({ width: 390, height: 844 });
    await page.locator("#usage-view-artifacts-tab").click();
    await expect(page.getByRole("tabpanel")).toHaveAttribute("id", "usage-view-artifacts-panel");
    expect(await tabs.evaluate((element) => getComputedStyle(element).overflowX)).toBe("auto");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true);
    await expect(filters).toBeVisible();
  });
}

for (const locale of ["zh-CN", "en-US"]) {
  test(`紧凑过滤器：高度预算、日期范围与筛选操作（${locale}）`, async ({ page }) => {
    await stubAnalytics(page);
    await seedLanguage(page, locale);
    await page.goto("/usage?view=models&provider=openai&model=long-model-name-for-layout&q=needle&from=2026-09-01&to=2026-09-30&offset=50");
    await expect(page.getByTestId("usage-analytics-health-banner")).toBeVisible();
    const filters = page.getByRole("form", { name: /Analytics scope and filters|分析范围与筛选/ });
    const measurements: { width: number; height: number; budget: number }[] = [];
    for (const [width, budget] of [[1440, 128], [1024, 128], [768, 166], [390, 280], [320, 280]]) {
      await page.setViewportSize({ width, height: 900 });
      const bounds = await filters.boundingBox();
      measurements.push({ width, height: bounds!.height, budget });
      expect(await filters.evaluate((element) => element.scrollWidth <= element.clientWidth + 1)).toBe(true);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true);
      const dateWidths = await filters.locator('input[type="date"]').evaluateAll((inputs) => inputs.map((input) => input.getBoundingClientRect().width));
      expect(dateWidths.every((value) => value >= 110)).toBe(true);
    }
    console.info(`[compact-filters/${locale}] ${JSON.stringify(measurements)}`);
    expect(measurements.filter(({ height, budget }) => height > budget)).toEqual([]);
    await expect(filters.locator('input, button[aria-haspopup="listbox"]')).toHaveCount(10);

    // 右列的长选项菜单也必须留在窄视口内，不能因触发器变窄而溢出。
    await filters.getByRole("button", { name: "Model", exact: true }).click();
    const modelMenu = page.getByRole("listbox", { name: "Model", exact: true });
    await expect(modelMenu).toBeVisible();
    const menuBounds = await modelMenu.boundingBox();
    expect(menuBounds!.x).toBeGreaterThanOrEqual(0);
    expect(menuBounds!.x + menuBounds!.width).toBeLessThanOrEqual(320);
    await page.keyboard.press("Escape");
    await filters.getByRole("button", { name: "Provider", exact: true }).click();
    await page.getByRole("option", { name: /^(All|全部)$/ }).click();
    await expect.poll(() => new URL(page.url()).searchParams.has("provider")).toBe(false);
    await filters.getByLabel(/^(Start date|开始日期)$/).fill("2026-09-02");
    await filters.getByLabel(/^(End date|结束日期)$/).fill("2026-09-28");
    await expect.poll(() => new URL(page.url()).searchParams.get("from")).toBe("2026-09-02");
    await expect.poll(() => new URL(page.url()).searchParams.get("to")).toBe("2026-09-28");
    await expect.poll(() => new URL(page.url()).searchParams.has("offset")).toBe(false);
    await filters.getByRole("button", { name: /^(Group by|分组)$/ }).click();
    await page.getByRole("option", { name: /^(Model|模型)$/ }).click();
    await expect.poll(() => new URL(page.url()).searchParams.get("group_by")).toBe("model");
    const token = filters.getByLabel("Admin token", { exact: true });
    await expect(token).toHaveAttribute("type", "password");
    await token.fill("e2e-layout-token");
    await filters.getByRole("button", { name: /Reset filters|重置筛选/ }).click();
    await expect(page).toHaveURL(/\/usage\?view=models$/);
    await expect(filters.getByRole("textbox", { name: /^(Search|搜索)$/ })).toHaveValue("");
    await expect(token).toHaveValue("e2e-layout-token");
  });
}

test("失败分类分布跟随筛选：参数透传且数据随筛选变化", async ({ page }) => {
  const errorQueries: string[] = [];
  await stubAnalytics(page, {
    errorQueries,
    // 按 provider 回放不同失败模式：只有带上筛选才会出现 openai 会话的分类。
    errorPatternsForQuery: (params) =>
      params.get("provider") === "openai"
        ? [{ error_code: "UPSTREAM_TIMEOUT", failure_category: "timeout", source: "requests", count: 7 }]
        : [{ error_code: "TOOL_DENIED", failure_category: "tool_error", source: "tools", count: 3 }],
  });
  await page.goto("/usage?provider=openai&from=2026-09-01&to=2026-09-30");
  await expect(page.getByTestId("usage-analytics-health-banner")).toBeVisible();
  const filters = page.getByRole("form", { name: /Analytics scope and filters|分析范围与筛选/ });
  const failureChart = page.getByTestId("analytics-failure-chart");

  // 概览的失败分类分布来自 /errors，且必须带上页面筛选（会话级口径）。
  await expect(failureChart).toBeVisible();
  // 用图表柱条的 aria-label（role=button）定位：Y 轴刻度里的同名 <title> 是隐藏节点。
  await expect(page.getByRole("button", { name: /UPSTREAM_TIMEOUT/ })).toBeVisible();
  await expect.poll(() => errorQueries.length).toBeGreaterThan(0);
  const first = new URLSearchParams(errorQueries[0]);
  expect(first.get("provider")).toBe("openai");
  expect(first.get("from")).toBe("2026-09-01");
  expect(first.get("to")).toBe("2026-09-30");
  expect(first.get("top")).toBe("10");

  // 清掉 provider：图表必须重取，且不再包含 openai 会话的失败分类。
  const before = errorQueries.length;
  await filters.getByRole("button", { name: "Provider", exact: true }).click();
  await page.getByRole("option", { name: /^(All|全部)$/ }).click();
  await expect(page.getByRole("button", { name: /TOOL_DENIED/ })).toBeVisible();
  await expect(page.getByRole("button", { name: /UPSTREAM_TIMEOUT/ })).toHaveCount(0);
  await expect.poll(() => errorQueries.length).toBeGreaterThan(before);
  const last = new URLSearchParams(errorQueries[errorQueries.length - 1]);
  expect(last.has("provider")).toBe(false);
  expect(last.get("from")).toBe("2026-09-01");
});
