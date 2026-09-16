import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedSession, seedSessionHistory } from "./support";

// 方案B e2e：runtime/stream 增量在请求中被实时渲染（打字机），最终 agent/chat
// result 定型（替换而非叠加，文本不翻倍）。
// 契约与真实后端一致：assistant_delta 是最终 LLM 文本的前缀分片（"final-"），
// result chunk 携带完整权威文本（"final-answer"）；打字机阶段显示前缀，
// result 到达后整体替换为权威全文，绝不叠加。
//
// 链路模拟：
// - /api/agent/chat 由 route 拦截：发送后延迟 1.5s 才返回 meta→chunk→done；
//   拦截绕过了 mock server 自身的回合落库，因此 route 侧必须补写同样的权威历史
//   （真实后端由 `agentChatHistoryCheckpointer` 中途提交 + 收尾落库保证「done 可见
//   时历史已包含本轮」）——否则回合结束时的一次历史重同步会按「服务端确认无消息」
//   的语义把刚渲染出的整轮对话抹掉，那不是真实后端会出现的状态；
// - /api/runtime/sessions/e2e-session-1/runtime/stream 由 route 拦截：
//   prompt 发出之前到达的请求**挂起不回应**（不能回空流：客户端对正常空流按 2s
//   退避重连，增量帧会落在请求结束之后，打字机窗口随之关闭）；发出之后放行，
//   投递一条 assistant_delta（带 payload.seq=1，与真实持久化事件一致），落点仍是
//   进行中的回合；随后重连一律空流——帧只投递一次，不重复落点。

const composer = (page: Page) => page.locator(".app-chat-input");

test.beforeEach(async ({ page }) => {
  // mock server 跨 spec 共享：清空上一个用例残留的会话历史/事件。
  await resetMockState(page.request);
});

test("打字机：请求期间 assistant_delta 实时渲染，result 定型且不翻倍", async ({
  page,
}) => {
  // 会话（mock 存储初始为空，前端不自动创建）。
  await seedSession(page.request);

  let allowStream = false;
  /** 增量放行闸门：放行前到达的流请求挂起（见文件头注释），避免迟到帧。 */
  let releaseDelta: (() => void) | null = null;
  const deltaGate = new Promise<void>((resolve) => {
    releaseDelta = resolve;
  });
  /** 帧只投递一次：投递成功后的重连按空流返回，避免增量重复落点。 */
  let deltaDelivered = false;
  await page.route("**/api/runtime/sessions/e2e-session-1/runtime/stream*", async (route) => {
    // 判据不取 URL 的 after：建连游标会随轨迹回放推进（历史见
    // docs/plan/workspace-chat-realtime-streaming.md「遗留红项」一节：首连 after
    // 一旦非 0，按 after 分流会让增量永远投不出去），改按「是否已投递」分流。
    if (!allowStream) {
      await deltaGate;
    }
    if (deltaDelivered) {
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: "",
      });
      return;
    }
    try {
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: [
          "event: runtime_event",
          'data: {"type":"assistant_delta","session_id":"e2e-session-1","payload":{"delta":"final-","stream_id":"s1","sequence":1,"seq":1},"timestamp":"2026-08-30T00:00:00Z"}',
          "",
          "",
        ].join("\n"),
      });
      deltaDelivered = true;
    } catch {
      // 该连接在放行前已被客户端 abort（重建连等）：不消耗唯一一次投递额度。
    }
  });

  await page.route("**/api/agent/chat*", async (route) => {
    // 模拟 LLM 生成耗时：给打字机窗口留出断言时间。
    await new Promise((resolve) => setTimeout(resolve, 1500));
    // 补写被拦截绕过的回合落库（见文件头注释）：历史必须覆盖本轮已渲染的用户
    // 消息与最终回答，收尾的历史重同步才不会把这一轮整体替换掉。
    await seedSessionHistory(page.request, "e2e-session-1", [
      { role: "user", content: "show streaming" },
      { role: "assistant", content: "final-answer" },
    ]);
    await route.fulfill({
      status: 200,
      contentType: "text/event-stream",
      body: [
        'event: meta',
        'data: {"session_id":"e2e-session-1","status":"streaming","kind":"llm","source":"agent_react"}',
        "",
        'event: chunk',
        'data: {"type":"text","content":"final-answer","total_chars":12}',
        "",
        'event: done',
        'data: {"session_id":"e2e-session-1","status":"completed","content":"final-answer"}',
        "",
      ].join("\n"),
    });
  });

  await page.goto("/workspace/chats/e2e-session-1");
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });

  // 发送 prompt：agent/chat 开始挂起 1.5s → 请求进行中。
  await composer(page).fill("show streaming");
  await composer(page).press("Control+Enter");
  allowStream = true;
  // 放行挂起的流连接：增量帧此刻投递，POST 仍在挂起（1.5s）→ 打字机窗口内。
  releaseDelta?.();

  // 打字机：请求进行中，增量前缀文本实时出现。判据必须抓住「尚未定型」的形态：
  // 定型文本是 "final-answer"，增量帧若没落到在途消息上，流式行只会等 result
  // 到达后一次性定型——这条断言必须失败，否则本用例退化成结果断言。
  await expect
    .poll(
      async () => {
        const streamingRow = page.locator('article[aria-busy="true"]').first();
        if ((await streamingRow.count()) === 0) {
          return false;
        }
        const text = (await streamingRow.innerText()).trim();
        return text.includes("final-") && !text.includes("answer");
      },
      { timeout: 5_000 },
    )
    .toBe(true);

  // result 定型：最终文本 = 权威全文（替换语义：前缀被替换为完整文本，不叠加）。
  await expect(page.getByText(/final-answer/).first()).toBeVisible({
    timeout: 5_000,
  });
  const text = await page.evaluate(() => document.body.innerText);
  const finalCount = (text.match(/final-answer/g) ?? []).length;
  expect(finalCount).toBeLessThanOrEqual(2); // 触发区 + 消息区，不重复
  // 前缀与最终文本不得叠加成 “final-final-answer” 之类形态。
  expect(text).not.toContain("final-final");
});