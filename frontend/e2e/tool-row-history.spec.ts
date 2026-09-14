import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedSessionHistory } from "./support";

// B4/B5 验收：首页消息列表里的「历史工具回执」必须还原成具体的工具行
// （读的哪个文件 / 跑的是什么命令 + 状态），可点击展开结果与错误，
// 而不是一律显示通用「上下文注入」行。
//
// 数据来源：mock 的 `POST /api/_test/history` 覆盖
// `GET /api/runtime/sessions/:id/history`；页面刷新走 history sync 投影。

const composer = (page: Page) => page.locator(".app-chat-input");

async function waitForPromptVisible(page: Page) {
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });
}

/**
 * 工具行外层（`presentation.attributes` 的宿主）：含 24px 单行本身 + 展开面板，
 * 面板是行的兄弟节点而不是子节点，因此断言必须框在这个外层上。
 */
const toolRows = (page: Page) => page.locator('[data-tool-row="true"]');

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  await page.goto("/workspace");
  await waitForPromptVisible(page);
});

test("B4/B5: 历史工具回执渲染为具体工具行（文件 / 命令）并可展开", async ({ page }) => {
  // 先发一条普通消息让首页线程拿到 sessionId（历史 sync 的前置条件），
  // 再注入历史并刷新：刷新后消息列表完全由历史投影而来。
  await composer(page).fill("capital check");
  await composer(page).press("Control+Enter");
  await expect(page.getByText("The capital of France is Paris.").first()).toBeVisible({
    timeout: 20_000,
  });

  await seedSessionHistory(page.request, "e2e-session-1", [
    { role: "user", content: "读取 e2e 笔记并跑一次测试" },
    {
      role: "assistant",
      content: "",
      metadata: { message_id: "msg-tool-read" },
      tool_calls: [
        {
          id: "call-read",
          name: "read_file",
          arguments: { file_path: "/workspace/e2e/notes.txt" },
        },
      ],
    },
    { role: "tool", content: "hello from notes", tool_call_id: "call-read" },
    {
      role: "assistant",
      content: "",
      metadata: { message_id: "msg-tool-shell" },
      tool_calls: [
        { id: "call-shell", name: "shell", arguments: { command: "npm test" } },
      ],
    },
    {
      role: "tool",
      content: "exit status 1",
      tool_call_id: "call-shell",
      metadata: { error: "exit status 1" },
    },
    { role: "assistant", content: "读取完成。" },
  ]);

  await page.reload();
  await waitForPromptVisible(page);

  const rows = toolRows(page);
  await expect(rows).toHaveCount(2, { timeout: 20_000 });

  // 读文件：折叠态就能看出读的是哪个文件（工具名 + 路径 + 状态词缀）。
  const readRow = rows.first();
  await expect(readRow).toContainText("read_file");
  await expect(readRow).toContainText("notes.txt");
  await expect(readRow).toHaveAttribute("data-tool-row-status", "finished");

  // 执行命令失败：折叠态仍是一行，失败原因作行内词缀（不整行变红、不撑高）。
  const shellRow = rows.nth(1);
  await expect(shellRow).toContainText("shell");
  await expect(shellRow).toContainText("exit status 1");
  await expect(shellRow).toHaveAttribute("data-tool-row-status", "error");

  // 折叠态 24px 单行（不随结果/错误内容增长）。
  const collapsed = await shellRow.boundingBox();
  expect(collapsed?.height ?? 0).toBeLessThanOrEqual(28);

  // 展开：命令与错误明细可见，行状态切到 open。
  // 注：摘要含交互元素（路径链接）时整行不是 button，有两个同义入口：
  // 前导图标（本用例点击它，指针友好）与右侧 chevron（键盘停靠点）。
  const rowBar = shellRow.locator("[data-chat-row-state]");
  const iconToggle = shellRow.locator('[data-chat-row-toggle="icon"]');
  const chevronToggle = shellRow.locator('[data-chat-row-toggle="chevron"]');
  await expect(iconToggle).toHaveAttribute("aria-expanded", "false");
  await expect(chevronToggle).toHaveAttribute("aria-expanded", "false");

  await iconToggle.click();
  await expect(rowBar).toHaveAttribute("data-chat-row-state", "open");
  await expect(iconToggle).toHaveAttribute("aria-expanded", "true");
  await expect(chevronToggle).toHaveAttribute("aria-expanded", "true");
  await expect(shellRow.locator('[data-tool-row-input-panel="true"]')).toContainText("npm test");
  await expect(shellRow).toContainText("npm test");

  // 换右侧 chevron 收拢：两个入口同义，行状态回到 closed。
  await chevronToggle.click();
  await expect(rowBar).toHaveAttribute("data-chat-row-state", "closed");
  await expect(iconToggle).toHaveAttribute("aria-expanded", "false");
  await expect(chevronToggle).toHaveAttribute("aria-expanded", "false");
  // 面板收起后仍挂载（React 常驻 + hidden），因此断言可见性而不是文本缺失。
  await expect(shellRow.locator('[data-tool-row-input-panel="true"]')).toBeHidden();
});
