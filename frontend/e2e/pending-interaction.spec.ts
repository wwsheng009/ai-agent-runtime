import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedSession } from "./support";

// P1-7 e2e：审批 / 提问 / 计划评审共用的「待交互」呈现位（composer 上沿单卡片）。
//
// 链路模拟（与 live-delta.spec.ts 同口径）：
// - `/runtime/stream` 由 route 拦截，脚本事件按 seq 递增投递；用例向数组 push
//   即等价于「后端随后产出该事件」，由前端长轮询循环自然取走；
// - `/runtime/commands` 由 route 拦截并记录请求体：验证决定投递契约，以及
//   「提交成功即乐观收敛（不等待 approval_resolved）」的呈现语义。

const SESSION_ID = "e2e-session-1";

type ScriptedEvent = {
  type: string;
  payload: Record<string, unknown>;
};

const composer = (page: Page) => page.locator(".app-chat-input");

const pendingBar = (page: Page) => page.getByTestId("pending-interaction");

/** 脚本化 runtime/stream：把已 push 且未投递的事件按 seq 一次性送出。 */
async function installScriptedRuntimeStream(
  page: Page,
  events: ScriptedEvent[],
): Promise<void> {
  let delivered = 0;
  await page.route(
    `**/api/runtime/sessions/${SESSION_ID}/runtime/stream*`,
    async (route) => {
      const frames = events.slice(delivered).map((event, index) => {
        const seq = delivered + index + 1;
        return [
          "event: runtime_event",
          `data: ${JSON.stringify({
            type: event.type,
            session_id: SESSION_ID,
            payload: { ...event.payload, seq },
            timestamp: "2026-09-11T00:00:00Z",
          })}`,
          "",
          "",
        ].join("\n");
      });
      delivered = events.length;
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: frames.join(""),
      });
    },
  );
}

/** 记录 runtime/commands 请求体并返回成功，供「决定投递」断言。 */
async function recordRuntimeCommands(
  page: Page,
  commands: Array<Record<string, unknown>>,
): Promise<void> {
  await page.route(
    `**/api/runtime/sessions/${SESSION_ID}/runtime/commands*`,
    async (route) => {
      commands.push(
        JSON.parse(route.request().postData() ?? "{}") as Record<string, unknown>,
      );
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: "{}",
      });
    },
  );
}

test.beforeEach(async ({ page }) => {
  // mock server 跨 spec 共享：清空上一个用例残留的会话历史/事件。
  await resetMockState(page.request);
});

test("P1-7a：审批卡进入统一呈现位，deny 投递 approve_tool 后卡片消失", async ({
  page,
}) => {
  await seedSession(page.request);
  const events: ScriptedEvent[] = [];
  const commands: Array<Record<string, unknown>> = [];
  await installScriptedRuntimeStream(page, events);
  await recordRuntimeCommands(page, commands);

  await page.goto(`/workspace/chats/${SESSION_ID}`);
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });

  events.push({
    type: "approval_requested",
    payload: {
      request_id: "req-e2e-p1-7",
      tool_name: "shell",
      reason: "run rm -rf build",
      risk_level: "high",
    },
  });

  const bar = pendingBar(page);
  await expect(bar).toBeVisible({ timeout: 15_000 });
  await expect(bar).toHaveAttribute("data-kind", "approval");
  await expect(bar).toHaveAttribute("data-status", "pending");
  await expect(bar.getByText("shell")).toBeVisible();
  await expect(bar.getByText("run rm -rf build")).toBeVisible();

  await bar.getByRole("button", { name: "Deny" }).click();

  await expect.poll(() => commands.length).toBe(1);
  expect(commands[0]).toMatchObject({
    type: "approve_tool",
    request_id: "req-e2e-p1-7",
    allow: false,
  });
  // 乐观收敛：提交成功即离开呈现位（approval_resolved 事件缺失也不悬挂）。
  await expect(bar).toHaveCount(0);
});

test("P1-7b：提问卡在同一呈现位收集回答并投递 answer_question", async ({
  page,
}) => {
  await seedSession(page.request);
  const events: ScriptedEvent[] = [];
  const commands: Array<Record<string, unknown>> = [];
  await installScriptedRuntimeStream(page, events);
  await recordRuntimeCommands(page, commands);

  await page.goto(`/workspace/chats/${SESSION_ID}`);
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });

  events.push({
    type: "question_asked",
    payload: {
      question_id: "q-e2e-p1-7",
      prompt: "Which color?",
      required: true,
      suggestions: ["red", "blue"],
    },
  });

  const bar = pendingBar(page);
  await expect(bar).toBeVisible({ timeout: 15_000 });
  await expect(bar).toHaveAttribute("data-kind", "question");
  await expect(bar.getByText("Which color?")).toBeVisible();
  await expect(bar.getByText("Required")).toBeVisible();

  await bar.locator("input").fill("blue");
  await bar.getByRole("button", { name: "Submit answer" }).click();

  await expect.poll(() => commands.length).toBe(1);
  expect(commands[0]).toMatchObject({
    type: "answer_question",
    question_id: "q-e2e-p1-7",
    answer: "blue",
  });
  await expect(bar).toHaveCount(0);
});

test("P1-7c：会话终止事件把未决审批收敛，卡片不悬挂", async ({ page }) => {
  await seedSession(page.request);
  const events: ScriptedEvent[] = [];
  await installScriptedRuntimeStream(page, events);

  await page.goto(`/workspace/chats/${SESSION_ID}`);
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });

  events.push({
    type: "approval_requested",
    payload: {
      request_id: "req-e2e-converge",
      tool_name: "shell",
      reason: "needs approval",
    },
  });

  const bar = pendingBar(page);
  await expect(bar).toBeVisible({ timeout: 15_000 });
  await expect(bar).toHaveAttribute("data-status", "pending");

  // 会话终止（停止/断开）→ 未决条目收敛，无需用户点击。
  events.push({ type: "session_interrupted", payload: { reason: "stopped" } });
  await expect(bar).toHaveCount(0, { timeout: 15_000 });
});
