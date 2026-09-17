// 临时探针 v5：按 key 展开所有分组，搜索目标行（跑完即删）。
import { chromium } from "@playwright/test";

const BASE = process.env.LIVE_BASE_URL ?? "http://localhost:5193";
const NEEDLE = process.env.PROBE_NEEDLE ?? "";
const DUMP_KEY = process.env.PROBE_DUMP_KEY ?? "";

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage();
await page.goto(`${BASE}/workspace/chats/new`, { waitUntil: "domcontentloaded" });
await page.locator('[data-testid="sidebar-session-list"]').first().waitFor({ timeout: 30_000 });
await page.waitForTimeout(4000);

const keys = await page.evaluate(() =>
  Array.from(document.querySelectorAll('[data-testid="sidebar-session-group"]')).map(
    (g) => g.getAttribute("data-group-key") ?? "",
  ),
);
console.log("group keys:", keys.length);

for (const key of keys) {
  const group = page.locator(`[data-testid="sidebar-session-group"][data-group-key="${key}"]`).first();
  if ((await group.count()) === 0) continue;
  const header = group.locator("button[aria-expanded]").first();
  if ((await header.getAttribute("aria-expanded").catch(() => null)) === "false") {
    await header.click({ timeout: 6000 }).catch((e) => console.log("click err", key, String(e).slice(0, 90)));
    await page.waitForTimeout(500);
  }
}
await page.waitForTimeout(1500);

const result = await page.evaluate(({ needle, dumpKey }) => {
  const summary = [];
  const matched = [];
  let dumped = [];
  document.querySelectorAll('[data-testid="sidebar-session-group"]').forEach((group) => {
    const key = group.getAttribute("data-group-key") ?? "";
    const list = group.querySelector('[data-testid="sidebar-session-list"]');
    const rows = list ? Array.from(list.querySelectorAll('[role="treeitem"]')) : [];
    const capped = Boolean(group.querySelector('[data-testid="sidebar-session-group-toggle"]'));
    summary.push({ key, rows: rows.length, capped });
    if (dumpKey && key === dumpKey) {
      dumped = rows.map((row) => {
        const button = row.querySelector("button");
        return button ? button.getAttribute("title") : null;
      });
    }
    rows.forEach((row, index) => {
      const button = row.querySelector("button");
      const icon = row.querySelector("button span[aria-label]");
      const stop = row.querySelector('[data-testid="session-row-actions-slot"] [aria-label*="停止"]');
      const titleAttr = button ? button.getAttribute("title") ?? "" : "";
      if (needle && titleAttr.includes(needle)) {
        matched.push({
          key,
          index,
          titleAttr,
          status: icon ? icon.getAttribute("aria-label") : null,
          hasStop: Boolean(stop),
        });
      }
    });
  });
  return { summary, matched, dumped };
}, { needle: NEEDLE, dumpKey: DUMP_KEY });

console.log(JSON.stringify(result, null, 1));
await browser.close();
