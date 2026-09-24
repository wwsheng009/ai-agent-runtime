// Batch 13 slice 9：E2E-1 / E2E-3 的前端半程（profiles 创建与修改闭环）。
//
// 覆盖（plan §23 剧本表）：
//   * E2E-1：新建向导（模板 review）→ 编辑器校验 → 会话内切换；
//   * E2E-3：改 deny 列表 → Preview impact → 保存 → 会话内切换。
//
// 「立即切换」**不在设置页**：A12 明确设置页没有会话上下文，`/apply` 必须显式带
// session_id，因此列表行的 apply 按钮保持「可见但禁用」（本文件显式钉住这个禁用态），
// 真实切换走会话 composer 的 builtin 命令 `/profile <ref>`——回执即 Switch Report
// 的前端面（`composer.builtin.profile.applied*`）。
//
// 断言只依赖 `data-*` 稳定钩子、testid 与下载事件，不绑定内部 DOM 结构。

import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedProfiles, seedSession } from "./support";

const PROFILES_URL = "/api/runtime/profiles";
const SESSION_ID = "e2e-profile-editor";

const composer = (page: Page) => page.locator(".app-chat-input");
// 命令回执（builtin 执行结果）挂 data-composer-command-result，属性值即 tone；
// data-composer-command-notice 是「未知命令」等菜单提示，两者不是同一个面。
const commandNotice = (page: Page) => page.locator("[data-composer-command-result]");
const editor = (page: Page) => page.locator("[data-profile-editor]");
const dialog = (page: Page) => page.getByRole("dialog");
/** 编辑器卡片导航：按钮顺序即 profileCardIds（basic/tools/…/validation）。 */
const editorNav = (page: Page) => editor(page).locator("nav button");
/** tools 卡：allow / deny 两个文本域（顺序即字段顺序）。 */
const toolsCard = (page: Page) => editorNav(page).nth(1);
const validationCard = (page: Page) => editorNav(page).nth(8);

/** 设置页 → Profiles 模式（模式入口是按钮列表；URL 上没有 mode 参数）。 */
async function openProfilesMode(page: Page): Promise<void> {
  await page.goto("/runtime/config");
  // 标题与导航项同名，按 role 取 h1，避免 strict mode 撞名。
  await expect(
    page.getByRole("heading", { name: "Backend config workspace" }),
  ).toBeVisible({ timeout: 30_000 });
  await page.getByRole("button", { name: "Profiles" }).click();
  await expect(page.getByTestId("profiles-list")).toBeVisible();
}

/**
 * 三步向导：命名 → 模板 → 层级。
 *
 * 模板/层级是自定义 Select（`button[aria-label=<字段标签>]` + portal `role="option"`），
 * 不是原生 `<select>`：必须先点触发器再点选项。选项按**文案**选（`review` / `project`），
 * 与 UI 语言无关（模板选项本身就是枚举原文）。
 */
async function pickSelectOption(
  page: Page,
  fieldLabel: string,
  option: string | RegExp,
): Promise<void> {
  await page.getByRole("button", { name: fieldLabel, exact: true }).click();
  await page.getByRole("option", { name: option, exact: true }).click();
}

async function createProfileViaWizard(
  page: Page,
  name: string,
  template: string,
  layer = "project",
): Promise<void> {
  await page.getByTestId("profiles-create-open").click();
  const panel = dialog(page);
  await expect(panel).toContainText("New profile");
  await panel.locator('input[type="text"]').fill(name);
  await page.getByTestId("profiles-create-next").click();
  await pickSelectOption(page, "Template", template);
  await page.getByTestId("profiles-create-next").click();
  // 层级选项文案随语言；用大小写不敏感的正则钉住枚举本身。
  await pickSelectOption(page, "Layer", new RegExp(`^${layer}$`, "i"));
  await page.getByTestId("profiles-create-submit").click();
  await expect(panel).toHaveCount(0);
}

/**
 * 会话内 `/profile <ref>` 切换。
 *
 * 提交口径与 P1-4 一致：命令行有候选菜单时纯 Enter 归菜单所有，命令行走 Ctrl+Enter。
 * 回执面同时钉两件事：请求体是 `set_profile` + 原文 ref（契约），通知条给出成功回执（UI）。
 */
async function switchProfileInSession(page: Page, ref: string): Promise<void> {
  await page.goto(`/workspace/chats/${SESSION_ID}`);
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });
  await composer(page).fill(`/profile ${ref}`);
  const pending = page.waitForRequest(
    (request) =>
      request.method() === "POST" &&
      request.url().includes(`/api/runtime/sessions/${SESSION_ID}/runtime/commands`),
  );
  await composer(page).press("Control+Enter");
  const body = (await pending).postDataJSON() as { type?: string; profile?: string };
  expect(body.type).toBe("set_profile");
  expect(body.profile).toBe(ref);
  await expect(commandNotice(page)).toBeVisible();
  await expect(commandNotice(page)).toHaveAttribute("data-composer-command-result", "success");
  await expect(commandNotice(page)).toContainText(/profile switched to/i);
}

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  // 会话先 seed：切换端点按 session_id 落绑定，未 seed 的会话 id 不会出现在任何夹具里。
  await seedSession(page.request, { id: SESSION_ID, title: "profile editor" });
});

test("E2E-1: 新建向导（模板 review）→ 校验 → 会话内切换", async ({ page }) => {
  await seedProfiles(page.request, { profiles: [{ name: "baseline", layer: "user" }] });
  await openProfilesMode(page);

  await createProfileViaWizard(page, "reviewer-plan", "review");

  // 新条目落在 project 层（ref 口径与后端一致：`${layer}:${name}`）。
  const editAction = page.getByTestId("profiles-action-edit-project:reviewer-plan");
  await expect(editAction).toBeVisible();
  // A12：设置页没有会话上下文，apply 保持禁用（真实切换在会话内）。
  await expect(page.getByTestId("profiles-action-apply-project:reviewer-plan")).toBeDisabled();

  // 编辑器：模板声明面落到草稿上（review 模板 = allow read_file / deny write_file+shell）。
  await editAction.click();
  const root = editor(page);
  await expect(root).toHaveAttribute("data-profile-editor", "project:reviewer-plan");
  await toolsCard(page).click();
  await expect(root.locator("textarea")).toHaveCount(2);
  await expect(root.locator("textarea").first()).toHaveValue(/read_file/);
  await expect(root.locator("textarea").nth(1)).toHaveValue(/write_file/);
  await expect(root.locator("textarea").nth(1)).toHaveValue(/shell/);

  // 校验：draft 合法 → 报告面 valid（校验结论来自同一实现，D28-1）。
  await validationCard(page).click();
  await page.getByRole("button", { name: "Validate draft" }).click();
  // 校验报告面在 validation 卡片内部（只有 report 非空才渲染），不在编辑器根上。
  await expect(page.locator('[data-profile-report="valid"]')).toBeVisible();

  // 切换（前端半程的「立即切换」）：会话内 builtin 命令 + 成功回执。
  await switchProfileInSession(page, "project:reviewer-plan");
});

test("E2E-3: 改 deny 列表 → Preview impact → 保存 → 会话内切换", async ({ page }) => {
  await seedProfiles(page.request, {
    profiles: [
      {
        name: "coder",
        layer: "project",
        description: "coding profile",
        spec: {
          profile: { name: "coder", default_agent: "coder" },
          tools: { allowlist: ["read_file", "write_file"], denylist: ["shell"] },
        },
      },
    ],
  });
  await openProfilesMode(page);

  await page.getByTestId("profiles-action-edit-project:coder").click();
  const root = editor(page);
  await expect(root).toHaveAttribute("data-profile-editor", "project:coder");

  await toolsCard(page).click();
  const deny = root.locator("textarea").nth(1);
  await expect(deny).toHaveValue(/shell/);
  await deny.fill("shell\nnetwork");

  // 预演：草稿与已存文件的差异面（changed_paths 计数挂在 data-profile-changes 上）。
  await validationCard(page).click();
  await page.getByRole("button", { name: "Preview impact" }).click();
  // 变更面同样在 validation 卡片内部：值即变更分组数，分组名以 Badge 呈现。
  const changedPaths = page.locator("[data-profile-changes]");
  await expect(changedPaths).toBeVisible();
  await expect(changedPaths).toHaveAttribute("data-profile-changes", /^[1-9]\d*$/);
  await expect(changedPaths).toContainText("tools");

  // 保存：落盘后草稿不再 dirty（Discard changes 只在 dirty 时可用）。
  await page.getByRole("button", { name: "Save profile" }).click();
  await expect(page.getByRole("button", { name: "Discard changes" })).toBeDisabled();

  // diff 与实际文件一致：视图接口读回的就是刚保存的 deny 面。
  await expect
    .poll(
      async () => {
        const response = await page.request.get(
          `${PROFILES_URL}/${encodeURIComponent("project:coder")}`,
        );
        return await response.text();
      },
      { timeout: 10_000 },
    )
    .toContain("network");

  // 切换后断言生效（前端半程 = 切换回执；下一轮工具面由 server 半程覆盖）。
  await switchProfileInSession(page, "project:coder");
});
