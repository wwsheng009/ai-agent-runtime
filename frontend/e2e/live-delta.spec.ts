import { expect, type Page, test } from "@playwright/test";

// 方案B e2e：runtime/stream 增量在请求中被实时渲染（打字机），最终 agent/chat
// result 定型（替换而非叠加，文本不翻倍）。
// 契约与真实后端一致：assistant_delta 是最终 LLM 文本的前缀分片（"final-"），
// result chunk 携带完整权威文本（"final-answer"）；打字机阶段显示前缀，
// result 到达后整体替换为权威全文，绝不叠加。
//
// 链路模拟：
// - /api/agent/chat 由 route 拦截：发送后延迟 1.5s 才返回 meta→chunk→done；
// - /api/runtime/sessions/e2e-session-1/runtime/stream 由 route 拦截：
//   prompt 发出之前到达的请求返回空流；发出之后（allowStream=true）的
//   请求才返回一条 assistant_delta（带 payload.seq=1，与真实持久化事件一致）。

const composer = (page: Page) => page.locator(".app-chat-input");

test("打字机：请求期间 assistant_delta 实时渲染，result 定型且不翻倍", async ({
  page,
}) => {
  // 会话（mock 存储初始为空，前端不自动创建）。
  const resp = await page.request.post("/api/runtime/sessions");
  expect(resp.status()).toBe(200);

  let allowStream = false;
  await page.route("**/api/runtime/sessions/e2e-session-1/runtime/stream*", async (route) => {
    const after = Number(
      new URL(route.request().url()).searchParams.get("after") ?? "0",
    );
    if (!allowStream || after > 0) {
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: "",
      });
      return;
    }
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
  });

  await page.route("**/api/agent/chat*", async (route) => {
    // 模拟 LLM 生成耗时：给打字机窗口留出断言时间。
    await new Promise((resolve) => setTimeout(resolve, 1500));
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

  // 打字机：请求进行中，增量前缀文本实时出现。
  await expect(page.getByText(/final-/).first()).toBeVisible({
    timeout: 5_000,
  });

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