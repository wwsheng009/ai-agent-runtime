// 临时探针 v3：单点实验 —— 点击一个折叠组头，观察 URL 与 aria-expanded 变化（跑完即删）。
import { chromium } from "@playwright/test";

const BASE = process.env.LIVE_BASE_URL ?? "http://localhost:5193";

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage();
await page.goto(`${BASE}/workspace/chats/new`, { waitUntil: "domcontentloaded" });
await page.locator('[data-testid="sidebar-session-list"]').first().waitFor({ timeout: 30_000 });
await page.waitForTimeout(4000);

const snapshot = () =>
  page.evaluate(() => ({
    url: location.href,
    groups: Array.from(document.querySelectorAll('[data-testid="sidebar-session-group"]')).map((g) => ({
      key: g.getAttribute("data-group-key"),
      label: g.querySelector("button[aria-expanded]")?.textContent?.trim().slice(0, 24) ?? "",
      expanded: g.querySelector("button[aria-expanded]")?.getAttribute("aria-expanded") ?? null,
      rows: g.querySelectorAll('[role="treeitem"]').length,
    })),
  }));

console.log("BEFORE:", JSON.stringify(await snapshot()));

const target = page
  .locator('[data-testid="sidebar-session-group"]')
  .filter({ hasText: "ai-agent-runtime" })
  .first();
console.log("target count:", await target.count());
const header = target.locator("button[aria-expanded]").first();
console.log("header count:", await header.count(), "expanded:", await header.getAttribute("aria-expanded"));
await header.click({ timeout: 8000 }).catch((e) => console.log("click error:", String(e).slice(0, 160)));
await page.waitForTimeout(2500);
console.log("AFTER 1 click:", JSON.stringify(await snapshot()));

await page.waitForTimeout(1500);
console.log("url now:", page.url());
await browser.close();
