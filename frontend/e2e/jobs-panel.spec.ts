import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedJobs, seedSession } from "./support";

// P2-1A e2e：会话头「后台任务」入口 → Jobs 面板。
// 覆盖链路：topbar 入口（仅会话线程渲染）→ GET /api/runtime/background/jobs?session_id=
// → live/settled 分区 → 按需读取 /output → 取消 live 任务后转终态。

const SESSION_ID = "e2e-p2-1a-jobs";
const LIVE_JOB_ID = "job-e2e-live-1";
const SETTLED_JOB_ID = "job-e2e-settled-1";

function minutesAgo(minutes: number): string {
  return new Date(Date.now() - minutes * 60_000).toISOString();
}

async function gotoSession(page: Page) {
  await page.goto(`/workspace/sessions/${SESSION_ID}`);
  await expect(page.locator(".app-chat-input")).toBeVisible({ timeout: 30_000 });
  await expect(
    page.getByRole("button", { name: "Background jobs", exact: true }),
  ).toBeVisible({ timeout: 15_000 });
}

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  await seedSession(page.request, { id: SESSION_ID });
});

test("会话头入口打开面板：按会话拉取、live/settled 分区与退出码", async ({
  page,
}) => {
  const listRequests: string[] = [];
  page.on("request", (request) => {
    const url = request.url();
    if (url.includes("/api/runtime/background/jobs") && !url.includes("/output")) {
      listRequests.push(url);
    }
  });
  await seedJobs(page.request, SESSION_ID, [
    {
      ID: LIVE_JOB_ID,
      SessionID: SESSION_ID,
      Status: "running",
      Command: "sleep 300",
      CreatedAt: minutesAgo(2),
      StartedAt: minutesAgo(1),
    },
    {
      ID: SETTLED_JOB_ID,
      SessionID: SESSION_ID,
      Status: "completed",
      Command: "go test ./...",
      CreatedAt: minutesAgo(20),
      StartedAt: minutesAgo(19),
      FinishedAt: minutesAgo(15),
      ExitCode: 0,
    },
  ]);

  await gotoSession(page);

  // P2-9：常驻状态条不依赖弹层，打开前即反映 live 计数；点击即打开弹层（计数与分区同源）。
  const statusBar = page.getByTestId("topbar-jobs-running");
  await expect(statusBar).toHaveText("1 background job(s) running");
  await statusBar.click();

  const panel = page.getByTestId("jobs-panel");
  await expect(panel).toBeVisible();
  await expect(panel).toContainText("Live (1)");
  await expect(panel).toContainText("Settled (1)");
  await expect(panel).toContainText("sleep 300");
  await expect(panel).toContainText("go test ./...");
  await expect(panel).toContainText("Exit code 0");

  // 列表请求带 session_id 且只由本会话触发，防止跨会话串台回归。
  await expect.poll(() => listRequests.length).toBeGreaterThan(0);
  expect(listRequests[0]).toContain(`session_id=${SESSION_ID}`);
});

test("展开输出按 offset 读取，取消 live 任务后转为已结束", async ({ page }) => {
  await seedJobs(page.request, SESSION_ID, [
    {
      ID: LIVE_JOB_ID,
      SessionID: SESSION_ID,
      Status: "running",
      Command: "sleep 300",
      CreatedAt: minutesAgo(2),
      StartedAt: minutesAgo(1),
      Output: "booting worker\n",
    },
  ]);

  await gotoSession(page);
  await page.getByRole("button", { name: "Background jobs", exact: true }).click();

  const panel = page.getByTestId("jobs-panel");
  await expect(panel).toContainText("Live (1)");

  await page.getByRole("button", { name: "View output of sleep 300" }).click();
  await expect(panel.getByTestId("job-output")).toContainText("booting worker");

  await panel.getByRole("button", { name: "Cancel", exact: true }).click();

  // mock 的 cancel 将任务置为 cancelled；面板随之刷新，live 分区清空。
  await expect(panel).toContainText("Cancelled");
  await expect(panel).not.toContainText("Live (1)");
});
