// Manual spec（一次性）：抓「实时更新瞬间整段工具行归零再恢复」那一刻的页面上下文。
//   pnpm test:manual -- e2e/zz-flash.manual.ts
// 输出：%TEMP%/aicli-diag/flash.json（含归零时刻的正文/请求/徽标）
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";

import { expect, test } from "@playwright/test";

const SESSION_URL =
  "http://localhost:5193/workspace/sessions/session_20260916162023_dYM2gpu6";

test.setTimeout(300_000);

test("blank-flash forensics", async ({ page }) => {
  const outDir = path.join(process.env.TEMP ?? "", "aicli-diag");
  mkdirSync(outDir, { recursive: true });
  const started = Date.now();
  const at = () => Date.now() - started;

  const reqs: Array<{ at: number; url: string }> = [];
  page.on("request", (req) => {
    const url = req.url().replace(/^https?:\/\/[^/]+/, "");
    if (url.includes("/api/runtime/")) reqs.push({ at: at(), url });
  });
  const consoleErrors: string[] = [];
  page.on("console", (msg) => {
    if (msg.type() === "error") consoleErrors.push(msg.text());
  });

  await page.goto(SESSION_URL, { timeout: 90_000 });
  await page.waitForSelector(".app-chat-input", { timeout: 90_000 });
  await page.waitForTimeout(10_000);

  const probe = () =>
    page.evaluate(() => {
      const rows = Array.from(document.querySelectorAll('[data-tool-row="true"]'));
      const lsRows = rows.filter((row) => row.getAttribute("data-tool-row-name") === "ls");
      const main = document.querySelector("main") ?? document.body;
      return {
        rows: rows.length,
        lsRows: lsRows.length,
        badge: document
          .querySelector("[data-connection-status]")
          ?.getAttribute("data-connection-status"),
        head: (main.innerText ?? "").replace(/[\s\u00a0]+/g, " ").slice(0, 220),
      };
    });

  const initial = await probe();
  const events: Array<Record<string, unknown>> = [];
  let last = initial;
  const deadline = started + 230_000;

  while (Date.now() < deadline) {
    const snap = await probe();
    if (snap.rows !== last.rows) {
      events.push({
        at: at(),
        dir: snap.rows < last.rows ? "shrink" : "grow",
        rows: [last.rows, snap.rows],
        lsRows: snap.lsRows,
        badge: snap.badge,
        head: snap.head,
      });
      await page.screenshot({
        path: path.join(outDir, `flash-${snap.rows < last.rows ? "shrink" : "grow"}-${snap.rows}.png`),
      });
      last = snap;
      // 归零后只再观察 25s：确认是否恢复即可。
      if (events.some((it) => it.dir === "shrink") && at() > 20_000) break;
    }
    await page.waitForTimeout(350);
  }

  const report = {
    initial,
    events,
    last: last.rows,
    requests: reqs.slice(-30),
    consoleErrors: consoleErrors.slice(0, 10),
  };
  writeFileSync(path.join(outDir, "flash.json"), JSON.stringify(report, null, 1), "utf8");
  console.log("[report]", JSON.stringify({ initial, events: events.map((it) => ({ at: it.at, dir: it.dir, rows: it.rows, head: String(it.head).slice(0, 80) })) }));
  expect(initial.rows).toBeGreaterThan(-1);
});
