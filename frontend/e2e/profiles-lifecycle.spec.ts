// Batch 13 slice 9：E2E-4 / E2E-5 的前端半程（跨目录分发与删除保护）。
//
// 覆盖（plan §23 剧本表）：
//   * E2E-4：`export` → 另一工作区 `import` → 使用；
//   * E2E-5：删除被 `default_profile` 引用的 profile（无 force 被阻止；force 后 default 清空）。
//
// 前端半程的口径（与 slice 9 的 mock 契约一一对应）：
//   * 导出 = 浏览器下载事件（产物出口），导入 = 先预演后落盘（D28-1：预演未通过确认按钮禁用）；
//   * 导入绝不自动激活（D28）：成功文案明示「not activated」，且 `default_profile` 不变；
//   * 「使用」= 会话内 builtin 命令 `/profile <ref>`（设置页 apply 无会话上下文，A12）。

import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedProfiles, seedSession } from "./support";

const PROFILES_URL = "/api/runtime/profiles";
const SESSION_ID = "e2e-profile-lifecycle";
const SHAREABLE_REF = "project:shareable";
const DEFAULTED_REF = "user:house-default";

/** 最小合法 zip（22 字节 EOCD）：与 mock 的导出体同形，`setInputFiles` 可选中。 */
const EMPTY_ZIP = Buffer.concat([Buffer.from([0x50, 0x4b, 0x05, 0x06]), Buffer.alloc(18)]);

const composer = (page: Page) => page.locator(".app-chat-input");
// builtin 命令回执：data-composer-command-result（属性值即 tone）。
const commandNotice = (page: Page) => page.locator("[data-composer-command-result]");
const dialog = (page: Page) => page.getByRole("dialog");

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
 * 单资源端点匹配：ref 含 `:` 与空格，编码形态由调用方决定，
 * 因此统一解码后再比对（不做路径拼接假设）。
 */
function isRefRequest(url: string, ref: string): boolean {
  return url.includes(`${PROFILES_URL}/`) && decodeURIComponent(url).includes(ref);
}

/** 会话内 `/profile <ref>` 切换：命令行提交走 Ctrl+Enter（纯 Enter 归候选菜单）。 */
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
  await seedSession(page.request, { id: SESSION_ID, title: "profile lifecycle" });
});

test("E2E-5: 删除被 default_profile 引用的 profile：先阻止，强制后放行且 default 清空", async ({
  page,
}) => {
  await seedProfiles(page.request, {
    profiles: [{ name: "house-default", layer: "user", description: "house default" }],
    defaultProfile: "house-default",
  });
  await openProfilesMode(page);

  const deleteAction = page.getByTestId(`profiles-action-delete-${DEFAULTED_REF}`);
  await expect(deleteAction).toBeVisible();
  await deleteAction.click();

  const panel = dialog(page);
  const confirm = page.getByTestId("profiles-dialog-confirm");
  await expect(panel).toContainText("Delete profile");

  // 第一次删除不带 force：后端 409（仍被 default_profile 引用）→ 对话框留下、确认禁用。
  const firstDelete = page.waitForRequest(
    (request) => request.method() === "DELETE" && isRefRequest(request.url(), DEFAULTED_REF),
  );
  await confirm.click();
  expect((await firstDelete).url()).not.toContain("force=true");
  await expect(confirm).toBeDisabled();
  await expect(panel).toContainText(/still point at this profile/i);

  // 勾选强制删除：确认解禁，第二次请求显式带 force=true。
  await page.getByLabel("Force delete").check();
  await expect(confirm).toBeEnabled();
  const forcedDelete = page.waitForRequest(
    (request) =>
      request.method() === "DELETE" &&
      isRefRequest(request.url(), DEFAULTED_REF) &&
      request.url().includes("force=true"),
  );
  await confirm.click();
  await forcedDelete;
  await expect(panel).toHaveCount(0);

  // 报告明示：条目消失 + 后端 `default_profile` 已清空（不是静默留一个悬空 default）。
  await expect(deleteAction).toHaveCount(0);
  await expect
    .poll(async () => {
      const response = await page.request.get(PROFILES_URL);
      const body = (await response.json()) as { default_profile?: string };
      return body.default_profile ?? "";
    })
    .toBe("");
});

test("E2E-4: 导出 → 导入（先预演后落盘、不自动激活）→ 会话内使用", async ({ page }) => {
  await seedProfiles(page.request, {
    profiles: [
      {
        name: "shareable",
        layer: "project",
        description: "cross-workspace bundle",
        spec: {
          profile: { name: "shareable", default_agent: "reviewer" },
          tools: { allowlist: ["read_file"], denylist: ["write_file"] },
        },
      },
    ],
  });
  await openProfilesMode(page);

  // 导出：下载事件即产物出口，建议文件名带 profile 名与 .zip 后缀。
  const download = page.waitForEvent("download");
  await page.getByTestId(`profiles-action-export-${SHAREABLE_REF}`).click();
  const artifact = await download;
  expect(artifact.suggestedFilename()).toContain("shareable");
  expect(artifact.suggestedFilename()).toMatch(/\.zip$/);

  // 导入：未选文件 → 预演与确认都禁用；预演通过前确认按钮保持禁用（D28-1）。
  await page.getByTestId("profiles-import-open").click();
  const panel = dialog(page);
  await expect(panel).toContainText("Import a profile bundle");
  const confirm = page.getByTestId("profiles-dialog-confirm");
  const preview = page.getByTestId("profiles-import-preview");
  await expect(preview).toBeDisabled();
  await expect(confirm).toBeDisabled();

  await page.getByTestId("profiles-import-file").setInputFiles({
    name: "shareable.zip",
    mimeType: "application/zip",
    buffer: EMPTY_ZIP,
  });
  await expect(preview).toBeEnabled();
  await preview.click();
  await expect(page.getByTestId("profiles-import-preview-result")).toContainText(/Dry run passed/);
  await expect(confirm).toBeEnabled();
  await confirm.click();
  await expect(panel).toHaveCount(0);

  // 导入绝不自动激活（D28）：成功文案明示，且 default_profile 不变、新条目不是 default。
  await expect(page.getByTestId("profiles-status")).toContainText(/not activated/i);
  const listed = (await (await page.request.get(PROFILES_URL)).json()) as {
    default_profile?: string;
    profiles: Array<{ ref: string; name: string; valid?: boolean; is_default?: boolean }>;
  };
  expect(listed.default_profile ?? "").toBe("");
  const imported = listed.profiles.find((entry) => entry.name !== "shareable");
  expect(imported).toBeTruthy();
  expect(imported?.valid).toBe(true);
  expect(imported?.is_default ?? false).toBe(false);

  // 使用：导入后 validate 通过，会话内可切换（切换回执即生效面）。
  await switchProfileInSession(page, imported!.ref);
});
