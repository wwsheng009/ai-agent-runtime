// 临时探针 v2：展开全部分组，抓取真实行与状态徽标（跑完即删）。纯 JS，勿加 TS 注解。
import { chromium } from "@playwright/test";

const BASE = process.env.LIVE_BASE_URL ?? "http://localhost:5193";
const NEEDLE = process.env.PROBE_NEEDLE ?? "";

const browser = await chromium.launch({ channel: "chrome" });
const page = await browser.newPage();
await page.goto(`${BASE}/workspace/chats/new`, { waitUntil: "domcontentloaded" });
await page.locator('[data-testid="sidebar-session-list"]').first().waitFor({ timeout: 30_000 });
await page.waitForTimeout(4000);

const readGroupState = () =>
  page.evaluate(() => {
    const out = [];
    document.querySelectorAll('[data-testid="sidebar-session-group"]').forEach((group) => {
      const header = group.querySelector("button[aria-expanded]");
      const list = group.querySelector('[data-testid="sidebar-session-list"]');
      out.push({
        key: group.getAttribute("data-group-key"),
        expanded: header ? header.getAttribute("aria-expanded") : null,
        rows: list ? list.querySelectorAll('[role="treeitem"]').length : -1,
      });
    });
    return out;
  });

const collapsed = await page.evaluate(
  () =>
    document.querySelectorAll('[data-testid="sidebar-session-group"] button[aria-expanded="false"]')
      .length,
);
console.log("collapsed groups before expand:", collapsed);

for (let i = 0; i < 40; i += 1) {
  const target = page
    .locator('[data-testid="sidebar-session-group"] button[aria-expanded="false"]')
    .first();
  if ((await target.count()) === 0) break;
  await target.click({ timeout: 5000 }).catch(() => undefined);
  await page.waitForTimeout(400);
}

await page.waitForTimeout(1500);

const collect = (needle) =>
  page.evaluate((needleText) => {
    const groups = [];
    document.querySelectorAll('[data-testid="sidebar-session-group"]').forEach((group) => {
      const key = group.getAttribute("data-group-key") ?? "";
      const rowEls = Array.from(group.querySelectorAll('[role="treeitem"]'));
      const map = (row) => {
        const button = row.querySelector("button");
        const icon = row.querySelector("button span[aria-label]");
        const stop = row.querySelector(
          '[data-testid="session-row-actions-slot"] [aria-label*="停止"]',
        );
        return {
          titleAttr: button ? button.getAttribute("title") : null,
          status: icon ? icon.getAttribute("aria-label") ?? "" : "",
          hasStop: Boolean(stop),
        };
      };
      const rows = rowEls.map(map);
      const matched = needleText
        ? rows.filter((row) => (row.titleAttr ?? "").includes(needleText))
        : [];
      groups.push({ key, rowCount: rows.length, rows: rows.slice(0, 2), matched });
    });
    return {
      groupCount: groups.length,
      groups: groups.filter((g) => g.rowCount !== 0),
    };
  }, needle);

const dump = await collect(NEEDLE);
console.log(JSON.stringify({ groupState: await readGroupState(), dump }, null, 1));
await browser.close();
