import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedSession } from "./support";

// P1-4（Composer 输入面）：
//   子片 1：草稿按会话持久化、14 行封顶内部滚动、输入回焦；
//   子片 2：附件三段式（选择 / 粘贴 / 拖放）、待发送轨、提交闸门与拒收提示；
//   子片 3：`+` / `/` / `@` 同源触发菜单、键盘所有权、命令行不降级为 prompt。
//
// 断言口径：
// - 只依赖 `.app-chat-input` 与 `data-composer-*` 稳定钩子及真实 localStorage，
//   不绑定内部 DOM 结构或组件状态；
// - 封顶值在浏览器内用实测 line-height / padding 计算（14 行），因此跟随字号
//   与密度设置漂移，不做魔法数字。
//
// e2e 默认 locale 为 en-US（见 support.ts），故提交按钮的 aria-label 为
// "Start new thread"。

const composer = (page: Page) => page.locator(".app-chat-input");

const DRAFT_STORAGE_PREFIX = "aicli.workspace.composer-draft.v1:";

const DRAFT_SESSION_ID = "e2e-composer-draft";

type WindowWithDragData = Window & { __e2eDragData?: DataTransfer };

async function readComposerMetrics(page: Page) {
  return page.evaluate(() => {
    const element = document.querySelector<HTMLTextAreaElement>(".app-chat-input");
    if (!element) {
      throw new Error("composer textarea missing");
    }
    const style = window.getComputedStyle(element);
    const lineHeight =
      Number.parseFloat(style.lineHeight) ||
      Number.parseFloat(style.fontSize) * 1.5;
    const padding =
      (Number.parseFloat(style.paddingTop) || 0) +
      (Number.parseFloat(style.paddingBottom) || 0);
    return {
      clientHeight: element.clientHeight,
      scrollHeight: element.scrollHeight,
      overflowY: style.overflowY,
      maxHeight: Math.ceil(lineHeight * 14 + padding),
    };
  });
}

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  await page.goto("/workspace");
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });
});

test("P1-4a: drafts persist per session across reloads and session switches", async ({
  page,
}) => {
  await seedSession(page.request, { id: DRAFT_SESSION_ID });

  await page.goto("/workspace/chats/new");
  await expect(composer(page)).toHaveValue("");
  await composer(page).fill("draft beta");
  await expect
    .poll(() =>
      page.evaluate(
        (key) => window.localStorage.getItem(key),
        `${DRAFT_STORAGE_PREFIX}new`,
      ),
    )
    .toBe("draft beta");

  // 切到运行时会话：草稿按会话隔离，不应串到另一会话
  await page.goto(`/workspace/chats/${DRAFT_SESSION_ID}`);
  await expect(composer(page)).toHaveValue("");
  await composer(page).fill("draft alpha");
  await expect
    .poll(() =>
      page.evaluate(
        (key) => window.localStorage.getItem(key),
        `${DRAFT_STORAGE_PREFIX}${DRAFT_SESSION_ID}`,
      ),
    )
    .toBe("draft alpha");

  // 刷新后同一会话恢复草稿
  await page.reload();
  await expect(composer(page)).toHaveValue("draft alpha");

  // 切回新建会话：beta 仍在（未被 alpha 覆盖）
  await page.goto("/workspace/chats/new");
  await expect(composer(page)).toHaveValue("draft beta");
});

test("P1-4b: the composer caps at 14 lines and scrolls inside", async ({
  page,
}) => {
  const longDraft = Array.from(
    { length: 30 },
    (_, index) => `line ${index + 1}`,
  ).join("\n");

  await composer(page).fill("single line");
  const shortMetrics = await readComposerMetrics(page);
  expect(shortMetrics.overflowY).toBe("hidden");
  expect(shortMetrics.clientHeight).toBeLessThan(shortMetrics.maxHeight);

  await composer(page).fill(longDraft);
  const longMetrics = await readComposerMetrics(page);

  expect(longMetrics.overflowY).toBe("auto");
  expect(longMetrics.clientHeight).toBeLessThanOrEqual(
    longMetrics.maxHeight + 1,
  );
  expect(longMetrics.scrollHeight).toBeGreaterThan(
    longMetrics.clientHeight + 40,
  );
});

test("P1-4c: the composer refocuses on load, session switch and submit", async ({
  page,
}) => {
  await expect(composer(page)).toBeFocused();

  await page.goto("/workspace/chats/new");
  await expect(composer(page)).toBeFocused();

  await composer(page).fill("focus me");
  await page.evaluate(() => {
    (document.activeElement as HTMLElement | null)?.blur();
  });
  await expect(composer(page)).not.toBeFocused();

  await page.locator('button[aria-label="Start new thread"]').click();

  await expect(composer(page)).toBeFocused();
  await expect(composer(page)).toHaveValue("");
});

test("P1-4d/S5: picked attachments upload on add and block submit until settled", async ({
  page,
}) => {
  await composer(page).fill("ship the attachment");
  const submit = page.locator('button[aria-label="Start new thread"]');

  await page.locator("input[data-composer-file-input]").setInputFiles({
    name: "notes.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("hello"),
  });

  await expect(page.locator("[data-composer-attachment]")).toHaveCount(1);
  // mock 与后端同形：非图片被跳过 → 条目如实落在 error（不伪造「已上传」）。
  await expect(
    page.locator('[data-composer-attachment][data-attachment-status="error"]'),
  ).toHaveCount(1);
  await expect(page.locator("[data-composer-attachments-blocked]")).toContainText(
    "can't send yet",
  );
  await expect(submit).toBeDisabled();

  // Ctrl/Cmd+Enter 也不能绕过闸门：附件不会被静默丢弃。
  await composer(page).press("Control+Enter");
  await expect(composer(page)).toHaveValue("ship the attachment");

  await page.locator("[data-composer-attachment-remove]").click();
  await expect(page.locator("[data-composer-attachment-rail]")).toHaveCount(0);
  await expect(submit).toBeEnabled();
});

test("P1-4e: pasting a file attaches it to the draft rail", async ({ page }) => {
  await page.evaluate(() => {
    const textarea = document.querySelector(".app-chat-input");
    if (!(textarea instanceof HTMLTextAreaElement)) {
      throw new Error("composer textarea missing");
    }
    const dataTransfer = new DataTransfer();
    dataTransfer.items.add(
      new File(["pasted"], "pasted.txt", { type: "text/plain" }),
    );
    textarea.dispatchEvent(
      new ClipboardEvent("paste", {
        bubbles: true,
        cancelable: true,
        clipboardData: dataTransfer,
      }),
    );
  });

  await expect(page.locator("[data-composer-attachment]")).toHaveCount(1);
  await expect(page.locator("[data-composer-attachment]")).toContainText(
    "pasted.txt",
  );
});

test("P1-4f: dropping files anywhere invites, then attaches them", async ({
  page,
}) => {
  await page.evaluate(() => {
    const dragWindow = window as WindowWithDragData;
    const dataTransfer = new DataTransfer();
    dataTransfer.items.add(
      new File(["dropped"], "dropped.txt", { type: "text/plain" }),
    );
    dragWindow.__e2eDragData = dataTransfer;
    window.dispatchEvent(
      new DragEvent("dragenter", { bubbles: true, dataTransfer }),
    );
  });

  const invitation = page.locator("[data-composer-drop-invitation]");
  await expect(invitation).toBeVisible();
  await expect(invitation).toContainText("drop to attach files");

  await page.evaluate(() => {
    const dragWindow = window as WindowWithDragData;
    window.dispatchEvent(
      new DragEvent("drop", {
        bubbles: true,
        dataTransfer: dragWindow.__e2eDragData,
      }),
    );
    delete dragWindow.__e2eDragData;
  });

  await expect(page.locator("[data-composer-drop-invitation]")).toHaveCount(0);
  await expect(page.locator("[data-composer-attachment]")).toContainText(
    "dropped.txt",
  );
});

test("P1-4g: duplicate picks are ignored with an acknowledging notice", async ({
  page,
}) => {
  const fileInput = page.locator("input[data-composer-file-input]");
  const payload = {
    name: "dup.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("dup"),
  };

  await fileInput.setInputFiles(payload);
  await fileInput.setInputFiles(payload);

  await expect(page.locator("[data-composer-attachment]")).toHaveCount(1);
  const rejected = page.locator("[data-composer-attachments-rejected]");
  await expect(rejected).toContainText("ignored 1");

  await rejected.click();
  await expect(rejected).toHaveCount(0);
});

test("P1-4h: the trigger menu owns the keyboard and slash lines never fall back to a prompt", async ({
  page,
}) => {
  const trigger = page.locator("button[data-composer-menu-trigger]");
  const menu = page.locator("[data-composer-menu]");
  const submit = page.locator('button[aria-label="Start new thread"]');

  // `+` 与 `/`、`@` 同源：按钮打开同一份菜单，textarea 暴露 combobox 语义。
  await trigger.click();
  await expect(menu).toBeVisible();
  await expect(composer(page)).toHaveAttribute("aria-expanded", "true");
  await expect(trigger).toHaveAttribute("aria-expanded", "true");
  await expect(page.locator("[data-composer-attach]")).toHaveCount(1);

  // Esc 关闭菜单并把键盘所有权还给输入框（原生焦点遍历不被劫持）。
  await composer(page).press("Escape");
  await expect(menu).toHaveCount(0);
  await expect(composer(page)).toHaveAttribute("aria-expanded", "false");
  await expect(composer(page)).toBeFocused();

  // 行首 `/` 进入命令行：状态行显式提示，当前草稿不会作为普通消息发送。
  await composer(page).fill("/nope");
  await expect(composer(page)).toHaveAttribute("data-composer-command-line", "true");
  await expect(page.locator("span[data-composer-command-line]")).toBeVisible();

  await composer(page).press("Enter");
  await expect(page.locator('[data-composer-command-notice="unknown-command"]')).toBeVisible();
  await expect(composer(page)).toHaveValue("/nope");
  await expect(composer(page)).toBeFocused();

  // Ctrl/Cmd+Enter 同样不能绕开：命令行只有「被宿主执行」一种出口。
  await composer(page).press("Control+Enter");
  await expect(composer(page)).toHaveValue("/nope");

  // 提示可关闭，草稿保留，用户可继续编辑或清空。
  await page.locator("[data-composer-command-notice-dismiss]").click();
  await expect(page.locator("[data-composer-command-notice]")).toHaveCount(0);

  await composer(page).fill("plain prompt");
  await expect(composer(page)).not.toHaveAttribute("data-composer-command-line", "true");
  await expect(submit).toBeEnabled();
});
