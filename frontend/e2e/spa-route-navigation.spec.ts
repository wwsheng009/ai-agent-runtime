import { expect, test } from "./fixtures";
import { resetMockState } from "./support";

// 站内导航回归：点击工作台顶栏入口必须真正切页 —— URL 变、目标页内容挂载、
// 工作台外壳卸载，且不能退化成整页刷新。
//
// 修复前的失败形态：<Routes> 在同一位置复用 RetryableLazyRoute 实例（props 变、
// state 不变），lazy 组件身份仍来自上一条路由 → URL 变了但页面没变（假跳转），
// 目标页 chunk 也永远不会被请求。
//
// 现有 spec 全部用 page.goto 直达，覆盖不到「站内点击」这条路径，所以单独补一份。

/** 文档加载计数：SPA 跳转不增加，整页刷新（新 document）会 +1。 */
const SPA_LOADS_KEY = "e2e.spaDocumentLoads";

const TARGETS = [
  {
    label: "Logs",
    href: "/logs",
    expectText: "runtime-server listening on 0.0.0.0:8101",
  },
  { label: "Usage", href: "/usage", expectText: "Session & global usage" },
  {
    label: "Runtime",
    href: "/runtime/config",
    expectText: "Backend config workspace",
  },
] as const;

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  await page.addInitScript((key) => {
    const loads = Number(window.sessionStorage.getItem(key) ?? "0") + 1;
    window.sessionStorage.setItem(key, String(loads));
  }, SPA_LOADS_KEY);
});

for (const target of TARGETS) {
  test(`点击顶栏 ${target.label} 入口 → ${target.href} 真实切页`, async ({
    page,
  }) => {
    await page.goto("/workspace");

    const entry = page.locator(`a[href="${target.href}"]`).first();
    await expect(entry).toBeVisible({ timeout: 30_000 });
    await entry.click();

    // 只看 pathname：/logs 等页面会自行追加 query（如 ?cursor=42）。
    await expect(page).toHaveURL((url) => url.pathname === target.href);
    // 目标页自带的「返回工作台」入口 = 内容确实换了，而不是 URL 变了页面没变。
    await expect(
      page.getByRole("link", { name: "Back to workspace" }).first(),
    ).toBeVisible({ timeout: 30_000 });
    // 页面正文（真数据 / 真标题），比「外壳换没换」更硬的判据。
    await expect(page.getByText(target.expectText).first()).toBeVisible({
      timeout: 30_000,
    });
    // 工作台外壳已卸载。
    await expect(page.getByTestId("workspace-view-tab-chat")).toHaveCount(0);
    // 没有退化成整页刷新。
    expect(
      await page.evaluate(
        (key) => window.sessionStorage.getItem(key),
        SPA_LOADS_KEY,
      ),
    ).toBe("1");
  });
}
