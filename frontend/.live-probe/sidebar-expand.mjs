// 临时探针 v7：展开“未绑定目录”全量行，搜索 A 会话行（跑完即删）。
import { chromium } from "@playwright/test";

const BASE = process.env.LIVE_BASE_URL ?? "http://localhost:5193";
const SESSION = process.env.PROBE_SESSION ?? "";
const NEEDLE = process.env.PROBE_NEEDLE ?? "";

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage();
await page.goto(`${BASE}/workspace/sessions/${SESSION}`, { waitUntil: "domcontentloaded" });
await page.waitForTimeout(6000);

const keys = await page.evaluate(() =>
  Array.from(document.querySelectorAll('[data-testid="sidebar-session-group"]')).map(
    (g) => g.getAttribute("data-group-key") ?? "",
  ),
);
for (const key of [...keys, "__runtime-session-directory-unknown__"]) {
  const group = page.locator(`[data-testid="sidebar-session-group"][data-group-key="${key}"]`).first();
  if ((await group.count()) === 0) continue;
  const header = group.locator("button[aria-expanded]").first();
  if ((await header.getAttribute("aria-expanded").catch(() => null)) === "false") {
    await header.click({ timeout: 6000 }).catch(() => undefined);
    await page.waitForTimeout(400);
  }
  const toggle = group.locator('[data-testid="sidebar-session-group-toggle"]');
  if ((await toggle.count()) > 0) {
    await toggle.first().click({ timeout: 6000 }).catch(() => undefined);
    await page.waitForTimeout(600);
  }
}
await page.waitForTimeout(1500);

const scan = (needle) =>
  page.evaluate((needleText) => {
    const hits = [];
    let total = 0;
    document.querySelectorAll('[data-testid="sidebar-session-group"]').forEach((group) => {
      const key = group.getAttribute("data-group-key") ?? "";
      const rows = group.querySelectorAll('[role="treeitem"]');
      total += rows.length;
      rows.forEach((row) => {
        const button = row.querySelector("button");
        const icon = row.querySelector("button span[aria-label]");
        const attr = button ? button.getAttribute("title") ?? "" : "";
        if (attr.includes(needleText)) {
          hits.push({
            key,
            titleAttr: attr,
            status: icon ? icon.getAttribute("aria-label") : null,
          });
        }
      });
    });
    return { total, hits };
  }, needle);

console.log("scan createdAt:", JSON.stringify(await scan(NEEDLE), null, 1));
await browser.close();
