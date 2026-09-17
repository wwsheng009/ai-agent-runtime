// 临时探针 v4：展开目标分组，dump 该组可见行的 title/status/停止按钮（跑完即删）。
import { chromium } from "@playwright/test";

const BASE = process.env.LIVE_BASE_URL ?? "http://localhost:5193";
const GROUP_TEXT = process.env.PROBE_GROUP ?? "ai-agent-runtime";

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage();
await page.goto(`${BASE}/workspace/chats/new`, { waitUntil: "domcontentloaded" });
await page.locator('[data-testid="sidebar-session-list"]').first().waitFor({ timeout: 30_000 });
await page.waitForTimeout(4000);

const dumpRows = () =>
  page.evaluate(() => {
    const out = [];
    document.querySelectorAll('[data-testid="sidebar-session-group"]').forEach((group) => {
      const list = group.querySelector('[data-testid="sidebar-session-list"]');
      if (!list) return;
      const rows = Array.from(list.querySelectorAll('[role="treeitem"]')).map((row) => {
        const button = row.querySelector("button");
        const icon = row.querySelector("button span[aria-label]");
        const stop = row.querySelector(
          '[data-testid="session-row-actions-slot"] [aria-label*="停止"]',
        );
        return {
          titleAttr: button ? button.getAttribute("title") : null,
          visibleTitle: button ? (button.textContent ?? "").trim().slice(0, 40) : null,
          status: icon ? icon.getAttribute("aria-label") : null,
          hasStop: Boolean(stop),
        };
      });
      out.push({ key: group.getAttribute("data-group-key"), rows });
    });
    return out;
  });

const target = page
  .locator('[data-testid="sidebar-session-group"]')
  .filter({ hasText: GROUP_TEXT })
  .first();
console.log("target count:", await target.count());
const header = target.locator("button[aria-expanded]").first();
if ((await header.getAttribute("aria-expanded")) === "false") {
  await header.click({ timeout: 8000 }).catch((e) => console.log("click err", String(e).slice(0, 120)));
  await page.waitForTimeout(2500);
}
console.log(JSON.stringify(await dumpRows(), null, 1));
await browser.close();
