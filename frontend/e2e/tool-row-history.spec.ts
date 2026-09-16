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
/** 不用 \n 转义，避免多行字符串在补丁/快照工具链里被当成结构标记。 */
const NL = String.fromCharCode(10);

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

test("apply_patch 历史回执：展开是行级 diff 浏览器，不再直出原始 diff 文本", async ({ page }) => {
  await composer(page).fill("capital check");
  await composer(page).press("Control+Enter");
  await expect(page.getByText("The capital of France is Paris.").first()).toBeVisible({
    timeout: 20_000,
  });

  const marker = "***";
  // render_output 形态：说明行 + 带真实行号的 diff 围栏（行级视图的数据源）。
  const toolContent = [
    "补丁已应用：修改 1；影响 1 个路径",
    "",
    "文件差异:",
    "```diff",
    "--- a/src/a.ts",
    "+++ b/src/a.ts",
    "@@ -1,2 +1,2 @@",
    ' const keep = 1;',
    '-const value = "old";',
    '+const value = "new";',
    "```",
  ].join(NL);

  await seedSessionHistory(page.request, "e2e-session-1", [
    { role: "user", content: "把常量改个名" },
    {
      role: "assistant",
      content: "",
      metadata: { message_id: "msg-tool-patch" },
      tool_calls: [
        {
          id: "call-patch",
          name: "apply_patch",
          arguments: {
            patch: [
              `${marker} Begin Patch`,
              `${marker} Update File: src/a.ts`,
              "@@",
              '-const value = "old";',
              '+const value = "new";',
              `${marker} End Patch`,
            ].join(NL),
          },
        },
      ],
    },
    { role: "tool", content: toolContent, tool_call_id: "call-patch" },
    { role: "assistant", content: "改好了。" },
  ]);

  await page.reload();
  await waitForPromptVisible(page);

  const row = toolRows(page).first();
  await expect(toolRows(page)).toHaveCount(1);
  await expect(row).toContainText("apply_patch");
  await row.locator('[data-chat-row-toggle="chevron"]').click();

  const panel = row.locator('[data-testid="tool-row-diff-panel"]');
  await expect(panel).toBeVisible();
  // 行级视图取代原始入参面板：补丁正文不再以纯文本重复一遍。
  await expect(row.locator('[data-tool-row-input-panel="true"]')).toHaveCount(0);
  await expect(panel.locator('[data-testid="tool-row-diff-path"]')).toHaveText("src/a.ts");
  await expect(panel.locator('[data-diff-hunk-header="expanded"]')).toContainText("@@ -1,2 +1,2 @@");
  await expect(panel.locator('[data-diff-cell="del"]').first()).toContainText('const value = "old";');
  await expect(panel.locator('[data-diff-cell="add"]').first()).toContainText('const value = "new";');

  // 输出面板保留工具说明行，但围栏里的原始 diff 文本不再直出。
  const output = row.locator('[data-tool-row-output="result"]');
  await expect(output).toContainText("补丁已应用：修改 1；影响 1 个路径");
  await expect(output).not.toContainText("@@ -1,2 +1,2 @@");

  // 并排（split）模式：同一个补丁的另一种读法，切换后行号与增删行仍来自真实补丁。
  const modeGroup = panel.locator('[role="group"]').first();
  await modeGroup.locator("button").nth(1).click();
  await expect(modeGroup.locator("button").nth(1)).toHaveAttribute("aria-pressed", "true");
  await expect(panel.locator('[data-diff-cell="add"]').first()).toBeVisible();
});

test("ls 历史回执：折叠行显示目录且不挂文件链接，展开输入为键值文本（与实时帧同口径）", async ({
  page,
}) => {
  await composer(page).fill("capital check");
  await composer(page).press("Control+Enter");
  await expect(page.getByText("The capital of France is Paris.").first()).toBeVisible({
    timeout: 20_000,
  });

  await seedSessionHistory(page.request, "e2e-session-1", [
    { role: "user", content: "看看 e2e 目录里有什么" },
    {
      role: "assistant",
      content: "",
      metadata: { message_id: "msg-tool-ls" },
      tool_calls: [{ id: "call-ls", name: "ls", arguments: { path: "frontend/e2e", depth: 2 } }],
    },
    { role: "tool", content: "diag.manual.ts\nfixtures.ts", tool_call_id: "call-ls" },
    { role: "assistant", content: "目录里是 e2e 用例。" },
  ]);

  await page.reload();
  await waitForPromptVisible(page);

  const row = toolRows(page).first();
  await expect(toolRows(page)).toHaveCount(1);
  await expect(row).toHaveAttribute("data-tool-row-kind", "list");
  await expect(row).toContainText("ls");
  await expect(row).toContainText("frontend/e2e");
  // 目录不是可打开的文件：折叠行不得出现「打开文件」死链接。
  await expect(row.locator('[data-tool-row-file-link="true"]')).toHaveCount(0);

  await row.locator('[data-chat-row-toggle="chevron"]').click();
  const panel = row.locator('[data-tool-row-input-panel="true"]');
  // 回放链路存的是 JSON 入参，但面板展示必须与实时帧（后端 arg_preview 键值文本）一致。
  await expect(panel).toContainText("depth=2 path=frontend/e2e");
  await expect(panel).not.toContainText('{"path"');
});
