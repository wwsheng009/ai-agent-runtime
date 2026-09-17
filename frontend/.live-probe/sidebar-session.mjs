// 临时探针 v6：打开指定会话页，观察其侧栏分组归属与行徽标（跑完即删）。
import { chromium } from "@playwright/test";

const BASE = process.env.LIVE_BASE_URL ?? "http://localhost:5193";
const SESSION = process.env.PROBE_SESSION ?? "";

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage();
await page.goto(`${BASE}/workspace/sessions/${SESSION}`, { waitUntil: "domcontentloaded" });
await page.waitForTimeout(6000);

const dump = await page.evaluate(() => {
  const groups = [];
  document.querySelectorAll('[data-testid="sidebar-session-group"]').forEach((group) => {
    const list = group.querySelector('[data-testid="sidebar-session-list"]');
    if (!list) return;
    const rows = Array.from(list.querySelectorAll('[role="treeitem"]')).map((row) => {
      const button = row.querySelector("button");
      const icon = row.querySelector("button span[aria-label]");
      return {
        titleAttr: button ? button.getAttribute("title") : null,
        status: icon ? icon.getAttribute("aria-label") : null,
        selected: Boolean(row.querySelector("button.border-accent-secondary-border")),
      };
    });
    groups.push({ key: group.getAttribute("data-group-key"), rows });
  });
  return { url: location.href, groups };
});

console.log(JSON.stringify(dump, null, 1));
await browser.close();
