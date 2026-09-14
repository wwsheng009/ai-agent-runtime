import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState } from "./support";

// P2-7（composer 内置命令执行器）：
// - `/` 菜单列出**真实可执行**的内置命令（export / rename），菜单点选与命令行提交同源；
// - `/rename`：缺标题前置失败（不产生空标题），错误以 alert 透出且可关闭；
// - `/export`：走与轨迹页导出同一实现（分页拉取 → JSONL → 下载），成功通知回报条数与文件名；
//   非法参数前置失败，不发起导出；
// - 已认领的命令执行后清空命令行（未认领/被阻塞时保留草稿供修正）。
//
// 提交口径与 P1-4 一致：菜单打开时纯 Enter 归菜单所有，命令行提交走 Ctrl/Cmd+Enter（或发送按钮）。
// 断言只依赖 `data-composer-*` 稳定钩子与下载事件，不绑定内部 DOM。

const composer = (page: Page) => page.locator(".app-chat-input");
const commandResult = (page: Page) => page.locator("[data-composer-command-result]");

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  await page.goto("/workspace");
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });
});

test("P2-7: the slash menu lists builtin commands and /rename without a title fails loudly", async ({
  page,
}) => {
  // `/` 菜单与 `+` 按钮同源：这里只验证内置命令清单确实进入菜单（此前为空）。
  await composer(page).fill("/");
  await expect(page.locator("[data-composer-menu]")).toBeVisible();
  await expect(page.locator('[data-composer-command-option="export"]')).toBeVisible();
  await expect(page.locator('[data-composer-command-option="rename"]')).toBeVisible();

  // 命令行提交仍走宿主执行，不降级为 prompt。
  await composer(page).fill("/rename");
  await composer(page).press("Control+Enter");

  const notice = commandResult(page);
  await expect(notice).toHaveAttribute("data-composer-command-result", "error");
  await expect(notice).toHaveAttribute("role", "alert");
  await expect(notice).toContainText(/provide a new title/i);
  // 已认领：命令行清空，避免把命令当草稿留在输入框里。
  await expect(composer(page)).toHaveValue("");

  // 回执可关闭，且不残留。
  await page.locator("[data-composer-command-result-dismiss]").click();
  await expect(notice).toHaveCount(0);
});

test("P2-7: /export downloads the session trajectory and rejects unknown flags first", async ({
  page,
}) => {
  await composer(page).fill("use the tool to look it up");
  await composer(page).press("Control+Enter");
  await expect(page.getByText("Paris is the capital of France.").first()).toBeVisible({
    timeout: 20_000,
  });

  // 非法参数：前置失败，不发起导出（也不产生下载）。
  await composer(page).fill("/export --json");
  await composer(page).press("Control+Enter");
  await expect(commandResult(page)).toHaveAttribute(
    "data-composer-command-result",
    "error",
  );
  await expect(commandResult(page)).toContainText("--json");

  // 合法路径：下载 JSONL 并以成功通知回报条数与文件名。
  const downloadPromise = page.waitForEvent("download");
  await composer(page).fill("/export");
  await composer(page).press("Control+Enter");
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toContain(".jsonl");

  await expect(commandResult(page)).toHaveAttribute(
    "data-composer-command-result",
    "success",
  );
  await expect(commandResult(page)).toHaveAttribute("role", "status");
  await expect(commandResult(page)).toContainText(/exported \d+ event/);
  await expect(composer(page)).toHaveValue("");
});
