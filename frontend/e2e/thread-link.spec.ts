import { expect, type Page, test } from "@playwright/test";

// 后台连接链路 e2e（thread 页面 → runtime 事件增量接口）：
// - 断言事件增量走 /runtime/events 正确路径且 200（防前端路径回退回归：
//   前端若再次写成 /events，mock 不识别该路径，轨迹恢复将失败）。
// - 断言后端连接失败（mock 故障开关 500）时页面给出可见降级提示，
//   不再是"无法连接后台服务且无提示"的静默状态。
// 注意：mock server 由 playwright webServer 跨整个 run 共享，故障开关
// 必须在用例内恢复，避免污染后续 spec（trajectory.spec 的 reload 恢复
// 用例依赖 events 接口 200）。

const composer = (page: Page) => page.locator(".app-chat-input");
const runtimeEventsUrl = (sessionId: string) =>
  new RegExp(
    `/api/runtime/sessions/${sessionId}/runtime/events\\?after=`,
  );
const legacyEventsUrl = (sessionId: string) =>
  new RegExp(`/api/runtime/sessions/${sessionId}/events\\?after=`);

async function waitForPromptVisible(page: Page) {
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });
}

// mock 的会话存储初始为空；前端不会自动建会话，打开 thread URL 前
// 先创建一个（与真实后端的"最新会话兜底"行为对齐）。
async function ensureMockSession(page: Page) {
  const resp = await page.request.post("/api/runtime/sessions");
  expect(resp.status()).toBe(200);
}

test("thread 链接：事件增量请求 /runtime/events 且 200，无旧路径请求", async ({
  page,
}) => {
  await ensureMockSession(page);
  const goodPath: string[] = [];
  const legacyPath: string[] = [];
  page.on("request", (req) => {
    const u = req.url();
    if (runtimeEventsUrl("e2e-session-1").test(u)) goodPath.push(u);
    else if (legacyEventsUrl("e2e-session-1").test(u)) legacyPath.push(u);
  });

  await page.goto("/workspace/chats/e2e-session-1");
  await waitForPromptVisible(page);

  // 页面选中会话后应发生一次事件增量拉取（正确路径）。
  await expect
    .poll(() => goodPath.length, { timeout: 15_000 })
    .toBeGreaterThan(0);
  // 旧路径（无 /runtime 段）绝不能出现——它是真实后端 404 的回归源头。
  expect(legacyPath).toHaveLength(0);

  // 增量接口必须 200（mock 只注册正确路径；旧路径会 404）。
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

  // 连接链路正常时不允许出现连接失败/降级提示。
  await expect(page.getByText(/运行时降级|runtime degraded|degraded/i)).not.toBeVisible();
  await expect(page.getByText(/需要恢复关注|needs restore attention/i)).not.toBeVisible();
});

// 故障开关恢复：不影响后续 spec（mock 跨 run 共享）。
test.afterEach(async ({ page }) => {
  await page.request.delete("/api/_mock/break-events", {
    data: { session_id: "e2e-session-1" },
  });
});

test("后端连接失败（events 500）时页面显示可见错误提示", async ({ page }) => {
  await ensureMockSession(page);
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

  // 重启页面：增量拉取 500 → thread 必须降级为可见错误状态
  // （Topbar 显示"运行时降级" + "需要恢复关注"，不再静默）。
  await page.reload();
  await waitForPromptVisible(page);
  await expect(
    page.getByText(/需要恢复关注|needs restore attention/i).first(),
  ).toBeVisible({ timeout: 20_000 });
  // 连接状态标签同步降级（composer "运行时降级"/transport error）。
  await expect(
    page.getByText(/运行时降级|runtime degraded|degraded/i).first(),
  ).toBeVisible({ timeout: 10_000 });
});