import { chromium } from "@playwright/test";

import { DEFAULT_LOCALE, DEFAULT_TIMEZONE, DEFAULT_VIEWPORT } from "./support";

// Warms up the preview server before the suite starts:
//  - compiles nothing (dist is prebuilt) but lets the app mount and the composer appear,
//  - so the first real test does not pay the cold-start render cost.
//
// baseURL 由 playwright.config.ts 以空闲端口探测后写入 E2E_BASE_URL（P0-7）。
// locale / 时区 / 视口与 playwright.config.ts 的 use 段同源，避免预热上下文
// 与实际用例上下文漂移（日期文案、三栏布局）。

export default async function globalSetup() {
  const baseURL = process.env.E2E_BASE_URL ?? "http://127.0.0.1:5193";
  const browser = await chromium.launch({ channel: "chrome", headless: true });
  const page = await browser.newPage({
    locale: DEFAULT_LOCALE,
    timezoneId: DEFAULT_TIMEZONE,
    viewport: DEFAULT_VIEWPORT,
  });
  try {
    page.on("response", (r) => {
      const url = r.url();
      if (url.includes("/api/")) {
        process.stdout.write(
          `[warm] ${r.status()} ${r.request().method()} ${url.replace(baseURL, "")}\n`,
        );
      }
    });
    page.on("pageerror", (e) => {
      process.stdout.write(`[warm:pageerror] ${(e.stack ?? String(e)).slice(0, 1200)}\n`);
    });
    await page.goto(`${baseURL}/workspace`, {
      waitUntil: "domcontentloaded",
      timeout: 60_000,
    });
    await page.waitForSelector(".app-chat-input", { timeout: 60_000 });
    // give React a moment to settle before the tests start
    await page.waitForTimeout(1000);
  } finally {
    await browser.close();
  }
}
