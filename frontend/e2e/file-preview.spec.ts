import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedRuntimeFiles } from "./support";

// P2-1A e2e：运行时文件读取（POST /api/runtime/fs/read-file）+ 工具行文件预览弹层。
// 覆盖链路：read_file 工具行的行内文件链接 → 预览弹层 → 字节级渲染，
// 以及「空文件 / 二进制 / 读取失败」三类如实呈现（不用本地兜底伪造内容）。

const READ_FILE_PATH = "/workspace/e2e/notes.txt";
const READ_FILE_PROMPT = "please read-file and tell me what it says";

const composer = (page: Page) => page.locator(".app-chat-input");

async function gotoWorkspace(page: Page) {
  await page.goto("/workspace");
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });
}

async function sendPrompt(page: Page, text: string) {
  await composer(page).fill(text);
  await composer(page).press("Control+Enter");
}

/** 工具行内文件链接（本用例里是 read_file 的唯一路径入口）。 */
async function clickFileLink(page: Page) {
  const link = page.locator('[data-tool-row-file-link="true"]');
  await expect(link).toBeVisible({ timeout: 30_000 });
  await expect(link).toContainText(READ_FILE_PATH);
  await link.click();
  await expect(page.getByTestId("file-preview-dialog")).toBeVisible({ timeout: 15_000 });
}

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  await gotoWorkspace(page);
});

test("工具行文件链接打开运行时文件预览：内容/字节数/行数都来自后端", async ({ page }) => {
  const bodies: Array<Record<string, unknown>> = [];
  await page.route("**/api/runtime/fs/read-file", async (route) => {
    bodies.push((route.request().postDataJSON() ?? {}) as Record<string, unknown>);
    await route.continue();
  });
  await seedRuntimeFiles(page.request, [
    { path: READ_FILE_PATH, content: "first line\nsecond line\n" },
  ]);

  await sendPrompt(page, READ_FILE_PROMPT);
  await clickFileLink(page);

  // 请求体是后端契约的 {path}，不做客户端路径改写。
  expect(bodies.at(-1)).toEqual({ path: READ_FILE_PATH });

  await expect(page.getByTestId("file-preview-resolved-path")).toContainText(READ_FILE_PATH);
  const text = page.getByTestId("file-preview-text");
  await expect(text).toContainText("first line", { timeout: 15_000 });
  await expect(text).toContainText("second line");
  await expect(page.getByTestId("file-preview-bytes")).toContainText("23");
  await expect(page.getByTestId("file-preview-lines")).toContainText("2");

  await page.keyboard.press("Escape");
  await expect(page.getByTestId("file-preview-dialog")).toBeHidden();
});

test("空文件与二进制：只呈现真实状态，不渲染文本", async ({ page }) => {
  await seedRuntimeFiles(page.request, [
    { path: READ_FILE_PATH, content: "" },
  ]);

  await sendPrompt(page, READ_FILE_PROMPT);
  await clickFileLink(page);

  await expect(page.getByTestId("file-preview-empty")).toBeVisible({ timeout: 15_000 });
  await expect(page.getByTestId("file-preview-text")).toHaveCount(0);

  await page.keyboard.press("Escape");
  await expect(page.getByTestId("file-preview-dialog")).toBeHidden();

  // 含 NUL 字节 → 判定为二进制，只给判定原因与真实字节数。
  await resetMockState(page.request);
  await seedRuntimeFiles(page.request, [
    { path: READ_FILE_PATH, dataBase64: Buffer.from([0x41, 0x00, 0x42]).toString("base64") },
  ]);
  await page.reload();
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });

  await sendPrompt(page, READ_FILE_PROMPT);
  await clickFileLink(page);

  const binary = page.getByTestId("file-preview-binary");
  await expect(binary).toBeVisible({ timeout: 15_000 });
  await expect(binary).toContainText("NUL");
  await expect(page.getByTestId("file-preview-text")).toHaveCount(0);
});

test("读取失败如实提示后端错误并可重试，不落本地兜底内容", async ({ page }) => {
  // 不 seed 文件：mock 与后端一致按读盘失败返回 500 + 真实错误文本。
  await sendPrompt(page, READ_FILE_PROMPT);
  await clickFileLink(page);

  const alert = page.getByTestId("file-preview-error");
  await expect(alert).toBeVisible({ timeout: 15_000 });
  await expect(alert).toContainText(`no such file: ${READ_FILE_PATH}`);
  await expect(page.getByTestId("file-preview-text")).toHaveCount(0);

  // 重试仍然失败（文件从未登记），错误保持可见且刷新为新的失败结果。
  await alert.getByRole("button").click();
  await expect(alert).toBeVisible({ timeout: 15_000 });
});
