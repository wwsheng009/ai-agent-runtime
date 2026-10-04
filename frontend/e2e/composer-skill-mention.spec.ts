import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedSession } from "./support";

// 文本类 skill `$` 提及端到端验收（对齐 aicli TUI 已实现的 Codex 风格路径）：
// - `$` 打开技能目录候选：文本类（docs-writer）在列，带 workflow 步骤的 code-review 不出现；
// - 补全插入 `$name `（含尾随空格），一条草稿可完成多个提及；
// - 提交把 `$name` 原文带给运行时（注入由后端 mention 路径处理，前端不改写文本）。
//
// 断言只依赖 `data-composer-menu*` / `data-composer-skill-option` 稳定钩子与聊天请求载荷。
// 文案断言依赖默认语言为 en-US（"Skills"）；切换默认语言时需同步改为从 i18n 资源取常量。

const SESSION_ID = "e2e-composer-skill-mention";

const composer = (page: Page) => page.locator(".app-chat-input");
const menu = (page: Page) => page.locator("[data-composer-menu]");
const skillGroup = (page: Page) => page.locator('[data-composer-menu-group="skills"]');

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  await seedSession(page.request, { id: SESSION_ID, title: "composer skill mention" });
  await page.goto(`/workspace/chats/${SESSION_ID}`);
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });
});

test("P: `$` 列出文本类技能（workflow 技能被过滤）并补全为 `$name `", async ({ page }) => {
  await composer(page).fill("$");
  await expect(menu(page)).toBeVisible();
  await expect(skillGroup(page)).toBeVisible();
  await expect(skillGroup(page)).toContainText("Skills");
  await expect(skillGroup(page)).toContainText("docs-writer");
  // 展示优先用技能描述（不是分类）：docs-writer 的描述来自 mock 目录。
  await expect(skillGroup(page)).toContainText("Draft and update project documentation.");
  // code-review 带 workflow 步骤：`$` 补全只收文本类（对齐 TUI 候选口径）。
  await expect(skillGroup(page)).not.toContainText("code-review");

  await composer(page).fill("$doc");
  const option = menu(page).locator('[data-composer-skill-option="docs-writer"]');
  await expect(option).toBeVisible();
  await option.click();
  // 尾随空格是刻意的：便于继续输入下一个提及/提示词。
  await expect(composer(page)).toHaveValue("$docs-writer ");
  await expect(menu(page)).toHaveCount(0);
});

test("P: 长描述挤压面板时技能名仍完整展示（不省略号截断）", async ({ page }) => {
  // 把面板压窄到 240px，复现「长描述挤占技能名」的最坏布局（不依赖视口宽度）。
  await page.addStyleTag({ content: "[data-composer-menu]{max-width:240px!important}" });
  await composer(page).fill("$doc");
  const option = menu(page).locator('[data-composer-skill-option="docs-writer"]');
  await expect(option).toBeVisible();

  // 名称行不得出现横向溢出（省略号截断在布局上表现为 scrollWidth > clientWidth）。
  const label = option.locator("span").first();
  const overflow = await label.evaluate((element) => element.scrollWidth - element.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
  await expect(label).toHaveText("docs-writer");
});

test("P: `+` 菜单提供技能入口，下钻点选后在光标处插入 `$name `", async ({ page }) => {
  await page.locator("button[data-composer-menu-trigger]").click();
  await expect(menu(page)).toBeVisible();
  // 根层：技能 launcher（带候选计数），点击下钻展开技能列表。
  const launcher = menu(page).locator('[data-composer-menu-item="launcher:skills"]');
  await expect(launcher).toBeVisible();
  await expect(launcher).toContainText("Skills");
  await launcher.click();

  await menu(page).locator('[data-composer-skill-option="docs-writer"]').click();
  await expect(composer(page)).toHaveValue("$docs-writer ");
  await expect(menu(page)).toHaveCount(0);
});

test("P: 一条草稿可引用多个技能，提交载荷保留 `$name` 原文", async ({ page }) => {
  const requests: Array<{ messages?: Array<{ content?: string }> }> = [];
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === "/api/agent/chat") {
      try {
        requests.push(request.postDataJSON());
      } catch {
        // 非 JSON 载荷不参与断言。
      }
    }
  });

  await composer(page).fill("$doc");
  await menu(page).locator('[data-composer-skill-option="docs-writer"]').click();
  await expect(composer(page)).toHaveValue("$docs-writer ");

  // 第二个提及沿用同一条补全路径（多次提及 = 多个 `$name` token）。
  await composer(page).fill("$docs-writer 按照 $doc");
  await menu(page).locator('[data-composer-skill-option="docs-writer"]').click();
  await expect(composer(page)).toHaveValue("$docs-writer 按照 $docs-writer ");

  await composer(page).press("Control+Enter");
  await expect.poll(() => requests.length).toBeGreaterThan(0);
  const contents = requests
    .flatMap((payload) => payload.messages ?? [])
    .map((message) => message.content ?? "")
    .join("\n");
  expect(contents).toContain("$docs-writer 按照 $docs-writer");
});
