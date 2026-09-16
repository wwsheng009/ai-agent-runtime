import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedRuntimeFiles } from "./support";

// P2-1A e2e：运行时文件读取（POST /api/runtime/fs/read-file）+ 工具行文件预览弹层。
// 覆盖链路：read_file 工具行的行内文件链接 → 预览弹层 → 字节级渲染，
// 以及「空文件 / 二进制 / 读取失败」三类如实呈现（不用本地兜底伪造内容）。

const READ_FILE_PATH = "/workspace/e2e/notes.txt";
const READ_FILE_PROMPT = "please read-file and tell me what it says";
// P2-1A 修正：工具行给出的「相对会话工作目录」路径，以及解析它的会话作用域根
// （mock `/fs/roots` 的 session 根，见 mock-server.mjs 的 E2E_FS_ROOT_PATH）。
const READ_FILE_RELATIVE_PATH = "notes/relative.txt";
const READ_FILE_RELATIVE_PROMPT = "please read-file-relative and tell me what it says";
const E2E_SESSION_ROOT = "E:/workspace/e2e";
const RESOLVED_RELATIVE_PATH = `${E2E_SESSION_ROOT}/${READ_FILE_RELATIVE_PATH}`;
// P2-1A 扩展：Markdown 文本文件的预览页签（原始 / Markdown 预览）。
const READ_FILE_MARKDOWN_PATH = "/workspace/e2e/readme.md";
const READ_FILE_MARKDOWN_PROMPT = "please read-file-md and tell me what it says";

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
async function clickFileLink(page: Page, expectedPath: string = READ_FILE_PATH) {
  const link = page.locator('[data-tool-row-file-link="true"]');
  await expect(link).toBeVisible({ timeout: 30_000 });
  await expect(link).toContainText(expectedPath);
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

test("Markdown 文件预览：原始 / 预览两个页签，切到预览按 Markdown 渲染", async ({ page }) => {
  await seedRuntimeFiles(page.request, [
    {
      path: READ_FILE_MARKDOWN_PATH,
      content: "# 预览标题\n\n正文 **加粗**\n\n| a | b |\n| - | - |\n| 1 | 2 |\n",
    },
  ]);

  await sendPrompt(page, READ_FILE_MARKDOWN_PROMPT);
  await clickFileLink(page, READ_FILE_MARKDOWN_PATH);

  // 页签只在 Markdown 文本上出现，默认停在「原始」；文案随 locale 变化，这里只断言结构与选中态。
  const tabs = page.getByTestId("file-preview-dialog").getByRole("tab");
  await expect(tabs).toHaveCount(2);
  const rawTab = page.getByTestId("file-preview-tab-raw");
  const previewTab = page.getByTestId("file-preview-tab-markdown");
  await expect(rawTab).toHaveAttribute("aria-selected", "true");
  await expect(previewTab).toHaveAttribute("aria-selected", "false");
  await expect(page.getByTestId("file-preview-text")).toContainText("# 预览标题");

  await previewTab.click();

  await expect(previewTab).toHaveAttribute("aria-selected", "true");
  await expect(rawTab).toHaveAttribute("aria-selected", "false");
  const rendered = page.getByTestId("file-preview-markdown");
  await expect(rendered.getByRole("heading", { level: 1 })).toHaveText("预览标题");
  await expect(rendered.locator("table")).toBeVisible();
  // 渲染后不应再出现 Markdown 记号本身；原始文本页被替换掉。
  await expect(rendered).not.toContainText("| a |");
  await expect(page.getByTestId("file-preview-text")).toHaveCount(0);
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

test("工具行相对路径按会话作用域根解析后再读：不再落到运行时进程 cwd 下", async ({ page }) => {
  // 背景（本机实测）：`POST /fs/read-file` 按**运行时进程工作目录**解析相对路径，而进程 cwd
  // 落在 `<repo>/backend`，工具行给的却是相对**会话工作目录**的写法——直接透传必然读错文件。
  const bodies: Array<Record<string, unknown>> = [];
  let rootsRequests = 0;
  await page.route("**/api/runtime/fs/read-file", async (route) => {
    bodies.push((route.request().postDataJSON() ?? {}) as Record<string, unknown>);
    await route.continue();
  });
  await page.route("**/api/runtime/fs/roots*", async (route) => {
    rootsRequests += 1;
    await route.continue();
  });
  // 文件按**解析后的绝对路径**登记：原样透传相对路径（旧行为）在 mock 与后端都读不到。
  await seedRuntimeFiles(page.request, [
    { path: RESOLVED_RELATIVE_PATH, content: "relative target\n" },
  ]);

  await sendPrompt(page, READ_FILE_RELATIVE_PROMPT);
  await clickFileLink(page, READ_FILE_RELATIVE_PATH);

  expect(bodies.at(-1)).toEqual({ path: RESOLVED_RELATIVE_PATH });
  expect(rootsRequests).toBeGreaterThan(0);
  await expect(page.getByTestId("file-preview-resolved-path")).toContainText(RESOLVED_RELATIVE_PATH);
  await expect(page.getByTestId("file-preview-text")).toContainText("relative target");
});

test("长文件预览：面板底边停在 composer 上沿之上，且不被输入框截获点击", async ({ page }) => {
  // 背景（本机实测 1440×900，修复前）：面板 75→825、composer 底栏 699→900，与输入框重叠 101px；
  // 面板底边中心命中的是 composer 的 textarea——遮罩没抬层级，输入框盖在弹层上并截获点击。
  await seedRuntimeFiles(page.request, [
    {
      path: READ_FILE_PATH,
      content: Array.from({ length: 200 }, (_, index) => `line ${index + 1}`).join("\n"),
    },
  ]);

  await sendPrompt(page, READ_FILE_PROMPT);
  await clickFileLink(page);
  await expect(page.getByTestId("file-preview-text")).toContainText("line 200", {
    timeout: 15_000,
  });

  const geometry = await page.evaluate(() => {
    const panel = document.querySelector('[data-testid="file-preview-dialog"]') as HTMLElement;
    const input = document.querySelector(".app-chat-input") as HTMLElement;
    // composer 底栏 = 承载输入卡的 z-30 绝对定位浮层（底部渐变与停靠卡同属这一层）。
    const composerBar = input.closest(".z-30") as HTMLElement;
    const panelRect = panel.getBoundingClientRect();
    const composerRect = composerBar.getBoundingClientRect();
    const hitPanelBottom = document.elementFromPoint(
      panelRect.left + panelRect.width / 2,
      panelRect.bottom - 4,
    );
    const hitComposer = document.elementFromPoint(
      composerRect.left + composerRect.width / 2,
      composerRect.top + composerRect.height / 2,
    );
    const insideDialog = (node: Element | null) =>
      Boolean(node?.closest('[data-testid="file-preview-dialog"]'));

    return {
      panelBottom: Math.round(panelRect.bottom),
      composerTop: Math.round(composerRect.top),
      panelBottomHitInDialog: insideDialog(hitPanelBottom),
      composerHitInInput: Boolean(hitComposer?.closest(".app-chat-input")),
      composerHitInDialog: insideDialog(hitComposer) || hitComposer === panel.parentElement,
    };
  });

  // 面板不再伸进 composer 底栏：底边停在它的上沿之上（实测留出 12px 间距）。
  expect(geometry.panelBottom).toBeLessThanOrEqual(geometry.composerTop);
  // 面板底部仍属于弹层本身：可读、可点，不被输入框截获。
  expect(geometry.panelBottomHitInDialog).toBe(true);
  // 遮罩抬到 composer 之上：模态期间输入框不再接收指针事件（也不是命中目标）。
  expect(geometry.composerHitInInput).toBe(false);
  expect(geometry.composerHitInDialog).toBe(true);
});
