import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedSession } from "./support";

// P1-4 子片 1（Composer 输入面）：草稿按会话持久化、14 行封顶内部滚动、输入回焦。
//
// 断言口径：
// - 只依赖 `.app-chat-input` 这一既有稳定钩子与真实 localStorage，不绑定内部
//   DOM 结构或组件状态；
// - 封顶值在浏览器内用实测 line-height / padding 计算（14 行），因此跟随字号
//   与密度设置漂移，不做魔法数字。
//
// e2e 默认 locale 为 en-US（见 support.ts），故提交按钮的 aria-label 为
// "Start new thread"。

const composer = (page: Page) => page.locator(".app-chat-input");

const DRAFT_STORAGE_PREFIX = "aicli.workspace.composer-draft.v1:";

const DRAFT_SESSION_ID = "e2e-composer-draft";

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
