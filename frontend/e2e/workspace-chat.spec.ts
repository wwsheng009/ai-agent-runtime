import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedSession, seedSessionHistory } from "./support";

// e2e acceptance coverage for the workspace streaming chat surface
// (Phase 1: G1 reasoning-first, G2 tool card lifecycle, G5 scroll-follow,
// G6 phase status strip, G8 stopped markers / interruption).
//
// The backend is the scripted mock server in ./mock-server.mjs; scripts are
// selected by keywords in the prompt text.

const composer = (page: Page) => page.locator(".app-chat-input");

async function sendPrompt(page: Page, text: string) {
  await composer(page).fill(text);
  await composer(page).press("Control+Enter");
}

async function waitForPromptVisible(page: Page) {
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });
}

async function scrollMetrics(page: Page) {
  return page.evaluate(() => {
    const log = document.querySelector('[role="log"]');
    const candidates = [
      ...(log?.parentElement ? [log.parentElement] : []),
      ...Array.from(
        document.querySelectorAll(
          "main, [class*='scroll'], [class*='list'], [class*='overflow']",
        ),
      ),
      document.scrollingElement,
    ];
    for (const el of candidates) {
      if (!el) continue;
      const style = getComputedStyle(el);
      if (style.overflowY === "hidden" || style.overflow === "hidden") {
        continue;
      }
      if (el.scrollHeight > el.clientHeight + 120) {
        return {
          top: Math.round(el.scrollTop),
          max: Math.round(el.scrollHeight - el.clientHeight),
          height: el.scrollHeight,
          client: el.clientHeight,
        };
      }
    }
    return { top: 0, max: 0, height: 0, client: 0 };
  });
}

/**
 * P1-3：reading-line（视口顶部往下 1/3）命中的语义行 + 行内 active Turn 标记。
 *
 * 断言只看「消息 id / 行相对偏移 / data-active-turn」，不看 DOM 结构或行数，
 * 因此对虚拟化与渲染实现保持中立（±2px 偏移容差）。
 */
async function readingState(page: Page) {
  return page.evaluate(() => {
    const log = document.querySelector('[role="log"]');
    const host = log?.parentElement ?? null;
    if (!log || !host) {
      return null;
    }
    const hostTop = host.getBoundingClientRect().top;
    const line = hostTop + host.clientHeight / 3;
    const rows = Array.from(log.querySelectorAll<HTMLElement>("[data-message-id]"));
    let picked: HTMLElement | null = null;
    for (const row of rows) {
      if (row.getBoundingClientRect().top <= line) {
        picked = row;
      }
    }
    const active = log.querySelector<HTMLElement>('[data-active-turn="true"]');
    return {
      activeId: active?.dataset.messageId ?? null,
      id: picked?.dataset.messageId ?? null,
      top: picked ? Math.round(picked.getBoundingClientRect().top - hostTop) : null,
    };
  });
}

test.beforeEach(async ({ page }) => {
  // mock server 跨 spec 共享：每个用例前清空会话历史/事件/故障开关，
  // 否则 e2e-session-1 会累积上一次用例的问答。
  await resetMockState(page.request);
  await page.goto("/workspace");
  await waitForPromptVisible(page);
});

test("G1: reasoning renders live before the answer chunk, then completes", async ({
  page,
}) => {
  // 关键字 "hold" 选中间隔放大的推理脚本：展开断言必须落在 done 之前的流式窗口内
  // （done 之后本回合过程行随历史刷新收敛，推理行不再驻留页面）。
  await sendPrompt(page, "capital of france (reasoning hold)");

  // reasoning row appears while the answer is still pending
  // 批次 B2：推理行收敛为 24px 单行（标题 + 单行摘要），断言走语义锚点。
  const reasoningRow = page.locator('[data-chat-row="reasoning"]').first();
  await expect(reasoningRow).toBeVisible({ timeout: 15_000 });
  await expect(reasoningRow).toHaveAttribute("data-chat-row-state", "closed");
  await expect(reasoningRow.locator('[data-chat-row-summary="true"]')).toContainText(
    "Checking whether the user request needs a tool",
  );

  // §13 C2：流式期恒不折叠，本回合不产出 turn-process 统计行
  await expect(page.locator('[data-chat-flow-kind="turn-process"]')).toHaveCount(0);

  // reasoning is still not followed by the answer yet
  await expect(page.getByText("The capital of France is Paris.")).not.toBeVisible();

  // expand reasoning to reveal the full transcript
  // 批次 B2：展开区改 Markdown 排版（不再有独立 <pre>），内容容器锚点
  // `data-chat-row-panel`，不依赖 class 断言。
  await reasoningRow.locator("button").first().click();
  await expect(reasoningRow).toHaveAttribute("data-chat-row-state", "open");
  const reasoningPanel = reasoningRow.locator('[data-chat-row-panel="reasoning"]');
  await expect(reasoningPanel).toContainText("No tool needed, drafting the answer");
  await expect(reasoningPanel).toContainText("Writing the final answer now");

  // answer chunk lands afterwards
  await expect(page.getByText("The capital of France is Paris.")).toBeVisible({
    timeout: 15_000,
  });
});

test("G2: tool card walks Started -> Running -> Finished；折叠态单行，展开后可见结果", async ({
  page,
}) => {
  await sendPrompt(page, "use the tool to look it up");

  // phase strip reports the tool phase
  await expect(page.getByText("Calling tools…")).toBeVisible({ timeout: 15_000 });

  // E1（§8.4）：工具行可用 flow 锚点定位；
  // tool identity is shown (exact match: the sr-only status live region also contains the name)
  const toolRow = page.locator('[data-chat-flow-kind="tool-call"]').first();
  await expect(toolRow).toBeVisible({ timeout: 15_000 });
  await expect(toolRow.getByText("web_search", { exact: true })).toBeVisible();

  // badge lifecycle
  const startedBadge = page.getByText("Started", { exact: true }).first();
  await expect(startedBadge).toBeVisible({ timeout: 10_000 });
  const runningBadge = page.getByText("Running", { exact: true }).first();
  await expect(runningBadge).toBeVisible({ timeout: 10_000 });
  await expect(page.getByText("Finished", { exact: true }).first()).toBeVisible({
    timeout: 10_000,
  });

  // B4（§5.5）：折叠态只有 24px 单行摘要（参数/目标可见），结果收进展开面板
  await expect(page.getByText(/capital of France/).first()).toBeVisible();
  const resultText = page.getByText("Paris", { exact: true }).first();
  await expect(resultText).toBeHidden();

  // 展开入口有两个（前导图标 / 右侧 chevron）；这里走键盘可达的那个。
  await toolRow.locator('[data-chat-row-toggle="chevron"]').click();
  await expect(resultText).toBeVisible();

  // final assistant text arrives
  await expect(page.getByText("Paris is the capital of France.")).toBeVisible({
    timeout: 15_000,
  });

  // P1-1: 完整 Turn token 用量在完成后显示（输入 1234 + 输出 567 = 合计 1801）
  await expect(page.getByText("1,801")).toBeVisible({ timeout: 5_000 });
});

test("G5: list auto-follows the stream, pauses while scrolled up, resumes at bottom", async ({
  page,
}) => {
  await sendPrompt(page, "scroll long answer");

  // stream starts; the list should follow so the newest chunk stays near the bottom
  await expect(page.getByText(/part 1/)).toBeVisible({ timeout: 15_000 });
  await expect(page.getByText(/part 8/)).toBeVisible({ timeout: 15_000 });

  let metrics = await scrollMetrics(page);
  expect(metrics.max).toBeGreaterThan(200); // content overflowed the viewport
  expect(Math.abs(metrics.max - metrics.top)).toBeLessThanOrEqual(120); // following

  // user scrolls up: follow must pause
  const list = page.locator('[role="log"]').locator("..");
  await list.hover();
  await page.mouse.wheel(0, -1600);
  await page.waitForTimeout(300);
  const scrolledTop = (await scrollMetrics(page)).top;
  expect(scrolledTop).toBeLessThan((await scrollMetrics(page)).max - 150);

  await expect(page.getByText(/part 16/)).toBeVisible({ timeout: 15_000 });
  const paused = await scrollMetrics(page);
  expect(Math.abs(paused.top - scrolledTop)).toBeLessThanOrEqual(60); // did not follow

  // user returns to the bottom: follow resumes
  await page.mouse.wheel(0, 4000);
  await page.waitForTimeout(300);
  await expect(page.getByText(/part 24/)).toBeVisible({ timeout: 15_000 });
  metrics = await scrollMetrics(page);
  expect(Math.abs(metrics.max - metrics.top)).toBeLessThanOrEqual(120); // following again
});

test("P1-3a: reading position holds while the answer keeps streaming", async ({
  page,
}) => {
  await sendPrompt(page, "scroll long answer");
  await expect(page.getByText(/part 4/)).toBeVisible({ timeout: 15_000 });

  // 上滚离开底部：语义锚点定在 reading-line 命中的那一行。
  const list = page.locator('[role="log"]').locator("..");
  await list.hover();
  await page.mouse.wheel(0, -2400);
  await page.waitForTimeout(300);
  const before = await readingState(page);
  expect(before?.id).toBeTruthy();
  expect(before?.top).not.toBeNull();
  // active Turn 是几何命中：reading-line 命中的行就是 active 行。
  expect(before?.activeId).toBe(before?.id);

  // 后续 chunk 只在视口下方增长：锚点行的视口偏移必须原地不动（±2px）。
  await expect(page.getByText(/part 24/)).toBeVisible({ timeout: 15_000 });
  const after = await readingState(page);
  expect(after?.id).toBe(before?.id);
  expect(Math.abs((after?.top ?? 0) - (before?.top ?? 0))).toBeLessThanOrEqual(2);
  expect(after?.activeId).toBe(before?.id);
});

test("P1-3b: prepending older history keeps the viewport anchored", async ({ page }) => {
  await sendPrompt(page, "scroll long answer");
  await expect(page.getByText(/part 36/)).toBeVisible({ timeout: 20_000 });
  await page.waitForTimeout(600); // 等 done/finalize 落地，注入期间不再有流式提交

  const list = page.locator('[role="log"]').locator("..");
  await list.hover();
  await page.mouse.wheel(0, -4000);
  await page.waitForTimeout(300);
  const before = await readingState(page);
  const metricsBefore = await scrollMetrics(page);
  expect(before?.id).toBeTruthy();
  expect(metricsBefore.max - metricsBefore.top).toBeGreaterThan(150); // 已离开底部

  // 在语义行容器顶部插入一条「更早的历史」行：等价于历史前插（虚拟化中立）。
  await page.evaluate(() => {
    const log = document.querySelector('[role="log"]');
    if (!log) {
      return;
    }
    const row = document.createElement("article");
    row.dataset.messageId = "history-prepended-0";
    row.style.display = "block";
    row.style.height = "900px";
    log.prepend(row);
  });
  await page.waitForTimeout(400);

  const after = await readingState(page);
  const metricsAfter = await scrollMetrics(page);
  // 阅读保顶：语义行位置不变（±2px），scrollTop 增大以抵消前插。
  expect(after?.id).toBe(before?.id);
  expect(Math.abs((after?.top ?? 0) - (before?.top ?? 0))).toBeLessThanOrEqual(2);
  expect(metricsAfter.top).toBeGreaterThan(metricsBefore.top);
  // 保顶期间不抢贴底：仍然是离开底部的状态。
  expect(metricsAfter.max - metricsAfter.top).toBeGreaterThan(150);
});

test("P1-3c: bottom ownership holds while a tool card streams in", async ({ page }) => {
  // 先制造一屏以上的内容，再在底部叠加工具回合：工具卡出现/增长期间必须保持贴底。
  await sendPrompt(page, "scroll long answer");
  await expect(page.getByText(/part 36/)).toBeVisible({ timeout: 20_000 });
  await page.waitForTimeout(400);
  let metrics = await scrollMetrics(page);
  expect(metrics.max).toBeGreaterThan(200);
  expect(metrics.max - metrics.top).toBeLessThanOrEqual(32); // ±32px 底部归属

  await sendPrompt(page, "use the tool to look it up");
  await expect(page.getByText("web_search", { exact: true })).toBeVisible({ timeout: 15_000 });
  await expect(page.getByText("Running", { exact: true }).first()).toBeVisible({
    timeout: 10_000,
  });
  metrics = await scrollMetrics(page);
  expect(metrics.max - metrics.top).toBeLessThanOrEqual(32); // 工具卡增长时仍贴底

  await expect(page.getByText("Paris is the capital of France.")).toBeVisible({
    timeout: 15_000,
  });
  await page.waitForTimeout(200);
  metrics = await scrollMetrics(page);
  expect(metrics.max - metrics.top).toBeLessThanOrEqual(32); // 正文收尾后仍贴底
});

test("G6: phase strip reflects the stream lifecycle", async ({ page }) => {
  await sendPrompt(page, "capital of france (phase strip)");

  // an explicit phase label appears while the turn is active
  await expect(page.getByText(/Waiting for first output|Streaming output|Calling tools|Finalizing turn|Connecting to runtime/)).toBeVisible({
    timeout: 15_000,
  });

  // the strip disappears once the turn completes
  await expect(page.getByText("The capital of France is Paris.")).toBeVisible({
    timeout: 15_000,
  });
  await expect(page.getByText("Streaming output…")).not.toBeVisible({ timeout: 15_000 });
});

test("G8a: server-side interruption surfaces a stopped marker", async ({ page }) => {
  await sendPrompt(page, "trigger the error case");

  // partial text arrives before the interruption
  await expect(page.getByText("The capital of France is ")).toBeVisible({
    timeout: 15_000,
  });

  // the stream error is surfaced in the message surface
  await expect(page.getByText("stream interrupted by test")).toBeVisible({
    timeout: 15_000,
  });
});

test("G8b: the Stop shortcut aborts a live stream and cancels the server turn", async ({ page }) => {
  // 建议 3（后端 cancel 契约）：停止不只要 abort 本地 SSE —— resume_on_disconnect
  // 之后服务端回合仍在跑，必须把 interrupt 命令投到 /runtime/commands 且带上
  // 正在跑的那个回合身份。mock server 未实现该端点，这里拦截并记录请求体。
  const commandBodies: Array<Record<string, unknown>> = [];
  await page.route(
    /\/api\/runtime\/sessions\/[^/]+\/runtime\/commands$/,
    async (route) => {
      commandBodies.push(JSON.parse(route.request().postData() ?? "{}"));
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          ok: true,
          cancelled: true,
          reason: "cancelled",
          channel: "active_turn",
        }),
      });
    },
  );

  await sendPrompt(page, "interrupt this stream");

  // chunks are flowing
  await expect(page.getByText(/Interruptible chunk 1/)).toBeVisible({ timeout: 15_000 });
  await expect(page.getByText(/Interruptible chunk 3/)).toBeVisible({ timeout: 15_000 });

  // Ctrl+Enter while responding stops the turn (same shortcut as submit)
  await composer(page).press("Control+Enter");

  // the assistant message is marked stopped instead of completed
  await expect(page.getByText("Stopped", { exact: true }).first()).toBeVisible();

  // 服务端一侧真的收到了中断，而不是只停了本地渲染
  await expect.poll(() => commandBodies.length).toBeGreaterThan(0);
  expect(commandBodies[0]?.type).toBe("interrupt");
  expect(String(commandBodies[0]?.turn_id ?? "")).not.toBe("");
});

test("G8c: 刷新后的续传回合仍能停止（无本地请求可 abort → 显式投递 interrupt）", async ({
  page,
}) => {
  // P4-刷新续传 + 建议 3（后端 cancel 契约）：刷新后的页面本地没有回合，停止按钮
  // 由会话级 currentSessionResponding 驱动——它必须出现，且点击后把 interrupt 投给
  // 服务端（带服务端仍在跑的那个回合身份），否则停止只是本地幻觉。
  // mock 静态服务不报告在途回合，这里按真实后端契约构造快照与命令响应。
  await seedSession(page.request, { id: "e2e-session-1", title: "Resumed turn" });
  await seedSessionHistory(page.request, "e2e-session-1", [
    { role: "user", content: "继续写完" },
    { role: "assistant", content: "前半截答案" },
  ]);

  let activeTurn: Record<string, unknown> | null = {
    session_id: "e2e-session-1",
    turn_id: "e2e-turn-9",
    source: "agent_chat_stream",
    detached: true,
  };
  const commandBodies: Array<Record<string, unknown>> = [];

  // GET /runtime 快照：本会话此刻仍在服务端执行 turn-9（续传身份的唯一来源）。
  await page.route(/\/api\/runtime\/sessions\/[^/]+\/runtime$/, async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        session_id: "e2e-session-1",
        state: null,
        active_turn: activeTurn,
      }),
    });
  });
  await page.route(
    "**/api/runtime/sessions/e2e-session-1/runtime/stream*",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: "",
      });
    },
  );
  await page.route(
    /\/api\/runtime\/sessions\/[^/]+\/runtime\/commands$/,
    async (route) => {
      commandBodies.push(JSON.parse(route.request().postData() ?? "{}"));
      // 取消成功后：服务端不再报告在途回合 → 快照收敛，停止态回落。
      activeTurn = null;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          ok: true,
          cancelled: true,
          reason: "cancelled",
          channel: "active_turn",
          turn_id: "e2e-turn-9",
        }),
      });
    },
  );

  await page.goto("/workspace/chats/e2e-session-1");

  // 半截答复被认领为在途回合 → composer 出现停止按钮（本地 isResponding 为 false）。
  const stop = page.getByRole("button", { name: "Stop response" });
  await expect(stop).toBeVisible({ timeout: 30_000 });

  await stop.click();

  await expect.poll(() => commandBodies.length).toBeGreaterThan(0);
  expect(commandBodies[0]?.type).toBe("interrupt");
  // 带上续传回合身份：错代的 stop 不得误伤新回合（后端 409 turn_mismatch）。
  expect(commandBodies[0]?.turn_id).toBe("e2e-turn-9");

  // 快照收敛后按钮回到发送态（停止不是一次性假动作）。
  await expect(stop).toBeHidden({ timeout: 15_000 });
});
