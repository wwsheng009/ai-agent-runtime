import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedSession } from "./support";

// 后台连接链路 e2e（thread 页面 → 运行时事件 / 实时流两条通道）：
// - 首屏恢复读：`GET /runtime/events?...` 走正确路径且 200。同一端点有两种语义，
//   首屏是 P3-1 尾部优先窗口读（`?limit=800&tail=1`，只回放最近消息），增量续拉
//   仍是 `?after=` 游标分页；防前端路径回退回归——写成 `/events` 时 mock 不识别
//   该路径，轨迹恢复必然失败。
// - 实时通道：`GET /runtime/stream?after=…&live=1&poll_ms=…` 并行建连且 200。
// - 后端连接失败（mock 故障开关让 events 端点 500）时页面给出**正文可见**降级提示：
//   顶栏副标题「会话 xxx 需要恢复关注」+ composer 状态条「运行时错误」
//   （`composer.transport.error`）。注意「运行时降级」是 `topbar.threadTransport.error`
//   的文案，现只出现在顶栏 tooltip 与会话详情面板，不在正文——正文断言勿再引用它。
//   归属实测（本用例内断言固化）：events 端点 500 期间实时通道 `/runtime/stream`
//   仍是 200，降级由 **events 读取链路**触发，不是 stream 连带失败。
// 注意：mock server 由 playwright webServer 跨整个 run 共享，故障开关
// 必须在用例内恢复，避免污染后续 spec（trajectory.spec 的 reload 恢复
// 用例依赖 events 接口 200）。

const composer = (page: Page) => page.locator(".app-chat-input");
const runtimeEventsUrl = (sessionId: string) =>
  new RegExp(`/api/runtime/sessions/${sessionId}/runtime/events\\?`);
const runtimeStreamUrl = (sessionId: string) =>
  new RegExp(`/api/runtime/sessions/${sessionId}/runtime/stream\\?`);
const legacyEventsUrl = (sessionId: string) =>
  new RegExp(`/api/runtime/sessions/${sessionId}/events\\?`);

async function waitForPromptVisible(page: Page) {
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });
}

test.beforeEach(async ({ page }) => {
  // mock server 跨 spec 共享：清空上一个用例残留的会话历史/事件。
  await resetMockState(page.request);
});

test("thread 链接：首屏事件窗口读走 /runtime/events 且 200，无旧路径请求", async ({
  page,
}) => {
  // mock 的会话存储初始为空；前端不会自动建会话，打开 thread URL 前
  // 先创建一个（与真实后端的"最新会话兜底"行为对齐）。
  await seedSession(page.request);
  const eventsRequests: string[] = [];
  const eventsStatuses: number[] = [];
  const streamStatuses: number[] = [];
  const legacyPath: string[] = [];
  page.on("request", (req) => {
    const u = req.url();
    if (runtimeEventsUrl("e2e-session-1").test(u)) eventsRequests.push(u);
    else if (legacyEventsUrl("e2e-session-1").test(u)) legacyPath.push(u);
  });
  page.on("response", (resp) => {
    const u = resp.url();
    if (runtimeEventsUrl("e2e-session-1").test(u)) {
      eventsStatuses.push(resp.status());
    } else if (runtimeStreamUrl("e2e-session-1").test(u)) {
      streamStatuses.push(resp.status());
    }
  });

  await page.goto("/workspace/chats/e2e-session-1");
  await waitForPromptVisible(page);

  // 页面选中会话后应发生一次事件窗口读（正确路径）。
  await expect
    .poll(() => eventsRequests.length, { timeout: 15_000 })
    .toBeGreaterThan(0);
  // 旧路径（无 /runtime 段）绝不能出现——它是真实后端 404 的回归源头。
  expect(legacyPath).toHaveLength(0);

  // 页面自己发出的窗口读必须 200（mock 只注册正确路径；旧路径会 404）。
  await expect
    .poll(() => eventsStatuses.length, { timeout: 15_000 })
    .toBeGreaterThan(0);
  expect(eventsStatuses.every((status) => status === 200)).toBe(true);

  // 实时通道并行建连（/runtime/stream）同样 200：两条通道任一 5xx 都会让 thread 降级。
  await expect
    .poll(() => streamStatuses.length, { timeout: 15_000 })
    .toBeGreaterThan(0);
  expect(streamStatuses.every((status) => status === 200)).toBe(true);

  // 游标续拉契约仍在（?after= 分页是轨迹恢复 / 导出的数据源）。
  let status = -1;
  await expect
    .poll(
      async () => {
        const resp = await page.request.get(
          "/api/runtime/sessions/e2e-session-1/runtime/events?after=0&limit=500",
        );
        status = resp.status();
        return status;
      },
      { timeout: 15_000 },
    )
    .toBe(200);

  // 连接链路正常时不允许出现降级提示（顶栏副标题 + composer 状态条两处断言）。
  await expect(
    page.getByText(/需要恢复关注|needs restore attention/i),
  ).not.toBeVisible();
  await expect(page.getByText(/运行时错误|runtime error/i)).not.toBeVisible();
});

test("后端连接失败（events 500）时页面显示可见错误提示", async ({ page }) => {
  await seedSession(page.request);
  // 归属取证：分别记录 events 读与实时通道的响应码，用于证明降级由 events
  // 端点 500 触发，而不是实时通道连带失败（见用例末尾断言）。
  const eventsStatuses: number[] = [];
  const streamStatuses: number[] = [];
  page.on("response", (resp) => {
    const u = resp.url();
    if (runtimeEventsUrl("e2e-session-1").test(u)) {
      eventsStatuses.push(resp.status());
    } else if (runtimeStreamUrl("e2e-session-1").test(u)) {
      streamStatuses.push(resp.status());
    }
  });

  await page.goto("/workspace/chats/e2e-session-1");
  await waitForPromptVisible(page);

  // 先确认连接链路正常（200），再注入故障模拟"后台服务断开"。
  await expect
    .poll(
      async () =>
        (
          await page.request.get(
            "/api/runtime/sessions/e2e-session-1/runtime/events?after=0&limit=500",
          )
        ).status(),
      { timeout: 15_000 },
    )
    .toBe(200);

  const broken = await page.request.post("/api/_mock/break-events", {
    data: { session_id: "e2e-session-1" },
  });
  expect(broken.ok()).toBe(true);

  // 重启页面：事件端点 500 → thread 必须降级为**正文可见**的错误状态，不再静默。
  await page.reload();
  await waitForPromptVisible(page);
  // 顶栏副标题（正文，非 tooltip）：「会话 xxx 需要恢复关注」。
  await expect(
    page.getByText(/需要恢复关注|needs restore attention/i).first(),
  ).toBeVisible({ timeout: 20_000 });
  // composer 状态条同步降级：`composer.transport.error`（zh「运行时错误」）。
  await expect(
    page.getByText(/运行时错误|runtime error/i).first(),
  ).toBeVisible({ timeout: 10_000 });

  // 归属证据：events 确实以 500 失败（触发降级），同窗口内 stream 全部 200。
  await expect
    .poll(
      () => eventsStatuses.filter((status) => status === 500).length,
      { timeout: 5_000 },
    )
    .toBeGreaterThan(0);
  await expect
    .poll(() => streamStatuses.length, { timeout: 10_000 })
    .toBeGreaterThan(0);
  expect(streamStatuses.every((status) => status === 200)).toBe(true);
});

// 故障开关恢复：不影响后续 spec（mock 跨 run 共享）。
test.afterEach(async ({ page }) => {
  await page.request.delete("/api/_mock/break-events", {
    data: { session_id: "e2e-session-1" },
  });
});
