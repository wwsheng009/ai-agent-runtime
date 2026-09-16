// Manual spec（一次性）：抓页面自己的 runtime 订阅 URL（after 游标）与连接徽标状态，
// 用于判定「页面是否真的在收实时帧」以及游标取值域。
//   pnpm test:manual -- e2e/zz-stream-url.manual.ts
// 输出：%TEMP%/aicli-diag/stream-url.json
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";

import { expect, test } from "@playwright/test";

const SESSION_URL =
  "http://localhost:5193/workspace/sessions/session_20260916162023_dYM2gpu6";

test.setTimeout(180_000);

test("page runtime subscription forensics", async ({ page }) => {
  const outDir = path.join(process.env.TEMP ?? "", "aicli-diag");
  mkdirSync(outDir, { recursive: true });

  await page.addInitScript(() => {
    const w = window as unknown as { __reqs?: Array<{ url: string; method: string }> };
    w.__reqs = [];
    const orig = window.fetch;
    window.fetch = function (
      this: unknown,
      ...args: Parameters<typeof fetch>
    ): ReturnType<typeof fetch> {
      try {
        const input = args[0];
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.toString()
              : (input as Request).url;
        const init = args[1] as RequestInit | undefined;
        w.__reqs?.push({ url, method: (init?.method ?? "GET").toUpperCase() });
      } catch {
        /* ignore */
      }
      return orig.apply(this as never, args);
    };
  });

  await page.goto(SESSION_URL, { timeout: 90_000 });
  await page.waitForSelector(".app-chat-input", { timeout: 90_000 });
  await page.waitForTimeout(12_000);

  const read = () =>
    page.evaluate(() => {
      const w = window as unknown as { __reqs?: Array<{ url: string; method: string }> };
      return {
        reqs: (w.__reqs ?? []).map((item) => item.url),
        badge: document
          .querySelector("[data-connection-status]")
          ?.getAttribute("data-connection-status"),
        rows: document.querySelectorAll('[data-tool-row="true"]').length,
      };
    });

  const first = await read();
  await page.waitForTimeout(25_000);
  const second = await read();

  const streamReqs = [...first.reqs, ...second.reqs].filter((url) => url.includes("runtime/stream"));
  const report = {
    session: SESSION_URL,
    badge: [first.badge, second.badge],
    rows: [first.rows, second.rows],
    streamReqs,
    otherRuntimeReqs: [...new Set([...first.reqs, ...second.reqs])].filter(
      (url) => url.includes("/api/runtime/") && !url.includes("runtime/stream"),
    ),
  };
  writeFileSync(path.join(outDir, "stream-url.json"), JSON.stringify(report, null, 1), "utf8");
  console.log("[report]", JSON.stringify(report));
  expect(first.rows).toBeGreaterThan(-1);
});
