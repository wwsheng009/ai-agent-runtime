// Manual debug spec（一次性）：读取真实会话页里指定工具名的行呈现事实。
//   npm run test:manual -- e2e/zz-ls-rows.manual.ts
import { writeFileSync } from "node:fs";
import path from "node:path";

import { test } from "@playwright/test";

const SESSION_URL =
  "http://localhost:5193/workspace/sessions/session_20260916185246_qQwPvMU5";
const TARGET_TOOL = "ls";

test("dump tool rows of a real session", async ({ page }) => {
  await page.goto(SESSION_URL, { timeout: 60_000 });
  await page.waitForSelector(".app-chat-input", { timeout: 60_000 });
  await page.waitForTimeout(8000);

  const rows = await page.evaluate((target) => {
    const out: unknown[] = [];
    for (const row of document.querySelectorAll('[data-tool-row="true"]')) {
      if ((row.getAttribute("data-tool-row-name") ?? "") !== target) {
        continue;
      }
      const summary = row.querySelector("[data-tool-row-summary]");
      const panel = row.querySelector('[data-tool-row-input-panel="true"]');
      out.push({
        name: row.getAttribute("data-tool-row-name"),
        kind: row.getAttribute("data-tool-row-kind"),
        status: row.getAttribute("data-tool-row-status"),
        hasSummary: row.getAttribute("data-tool-row-has-summary"),
        summary: (summary?.textContent ?? "").replace(/\s+/g, " "),
        fileLink: row.querySelector('[data-tool-row-file-link="true"]')?.textContent ?? null,
        input: (panel?.textContent ?? "").replace(/\s+/g, " ").slice(0, 240),
      });
    }
    return out;
  }, TARGET_TOOL);

  const file = path.join(process.env.TEMP ?? "", "aicli-diag", "ls-rows.json");
  writeFileSync(file, JSON.stringify(rows, null, 1), "utf8");
  console.log("[dump]", file, "rows =", rows.length);
  for (const row of rows) {
    console.log(JSON.stringify(row));
  }
});
