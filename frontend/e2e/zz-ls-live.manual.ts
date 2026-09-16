// Manual live spec（一次性）：定位「实时新行到达瞬间整段工具行归零再恢复」的空白闪烁。
//   pnpm test:manual -- e2e/zz-ls-live.manual.ts
// 输出：%TEMP%/aicli-diag/ls-live3.json（+ 归零/恢复时截图 ls-live3-*.png）
// 触发：浏览器打开期间外部触发一次 `ls .../frontend/src/lib/tool-row`（见 MARKER）。
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";

import { expect, test } from "@playwright/test";

const SESSION_URL =
  "http://localhost:5193/workspace/sessions/session_20260916162023_dYM2gpu6";
const MARKER = "hooks/workspace";

type Sample = {
  rows: number;
  messages: number;
  lsRows: number;
  bodyText: string;
};

const norm = (text: string) => text.replace(/\\/g, "/").toLowerCase();

test.setTimeout(420_000);

test("ls live: blank-flash forensics", async ({ page }) => {
  const outDir = path.join(process.env.TEMP ?? "", "aicli-diag");
  mkdirSync(outDir, { recursive: true });
  const started = Date.now();
  const at = () => Date.now() - started;

  const requests: Array<{ at: number; method: string; url: string }> = [];
  const navs: Array<{ at: number; url: string }> = [];
  const consoleErrors: string[] = [];
  page.on("request", (req) => {
    const url = req.url();
    if (url.includes("/api/runtime/") || url.includes("/history")) {
      requests.push({ at: at(), method: req.method(), url: url.replace(/^https?:\/\/[^/]+/, "") });
    }
  });
  page.on("framenavigated", (frame) => {
    if (frame === page.mainFrame()) navs.push({ at: at(), url: frame.url() });
  });
  page.on("console", (msg) => {
    if (msg.type() === "error") consoleErrors.push(msg.text());
  });
  page.on("pageerror", (err) => consoleErrors.push(`pageerror: ${err.message}`));

  await page.goto(SESSION_URL, { timeout: 90_000 });
  await page.waitForSelector(".app-chat-input", { timeout: 90_000 });
  await page.waitForTimeout(3_000);

  const sample = (): Promise<Sample> =>
    page.evaluate(() => {
      const rows = document.querySelectorAll('[data-tool-row="true"]').length;
      const lsRows = Array.from(document.querySelectorAll('[data-tool-row-name="ls"]')).length;
      const messages = document.querySelectorAll("[data-message-id]").length;
      return {
        rows,
        lsRows,
        messages,
        bodyText: (document.body.innerText ?? "").replace(/\s+/g, " ").slice(0, 260),
      };
    });

  const initial = await sample();
  const events: Array<Record<string, unknown>> = [];
  let last = initial;
  let live: Record<string, unknown> | null = null;
  const deadline = started + 330_000;

  while (Date.now() < deadline) {
    const snap = await sample();
    if (snap.rows !== last.rows || snap.messages !== last.messages) {
      events.push({
        at: at(),
        kind: snap.rows < last.rows ? "shrink" : "grow",
        rows: [last.rows, snap.rows],
        messages: [last.messages, snap.messages],
        lsRows: snap.lsRows,
        bodyText: snap.bodyText,
      });
      const tag = snap.rows < last.rows ? `shrink-${snap.rows}` : `grow-${snap.rows}`;
      await page.screenshot({ path: path.join(outDir, `ls-live3-${tag}.png`) });
      last = snap;
    } else if (snap.lsRows > last.lsRows) {
      last = snap;
    }
    if (!live) {
      const fresh = await page.evaluate(
        (marker) =>
          Array.from(document.querySelectorAll('[data-tool-row-name="ls"]'))
            .map((row) => ({
              summary: (row.querySelector("[data-tool-row-summary]")?.textContent ?? "").replace(
                /\s+/g,
                " ",
              ),
              kind: row.getAttribute("data-tool-row-kind"),
              status: row.getAttribute("data-tool-row-status"),
              fileLink: row.querySelector('[data-tool-row-file-link="true"]')?.textContent ?? null,
            }))
            .filter((row) => row.summary.replace(/\\/g, "/").toLowerCase().includes(marker)),
        norm(MARKER),
      );
      if (fresh.length > 0) {
        live = fresh[0];
        // 继续观察 45s，覆盖「到达后是否再次归零」。
        events.push({ at: at(), kind: "live-detected", rows: [last.rows, last.rows], live });
      } else {
        await page.waitForTimeout(250);
        continue;
      }
    }
    if (live && at() > (events.find((it) => it.kind === "live-detected")?.at as number) + 45_000) {
      break;
    }
    await page.waitForTimeout(250);
  }

  const report = {
    session: SESSION_URL,
    initial,
    live,
    events,
    navs,
    requests: requests.slice(-40),
    consoleErrors: consoleErrors.slice(0, 10),
  };
  writeFileSync(path.join(outDir, "ls-live3.json"), JSON.stringify(report, null, 1), "utf8");
  console.log("[report]", JSON.stringify({ initial, live, events: events.length, navs: navs.length }));
  expect(initial.rows).toBeGreaterThan(-1);
});
