import { chromium } from "@playwright/test";

// Warms up the vite dev server before the suite starts:
//  - triggers vite's first-run dependency re-optimization,
//  - compiles the workspace page module graph,
//  - lets the app mount and the composer appear,
// so the first real test does not time out waiting for the first compile.

export default async function globalSetup() {
  const browser = await chromium.launch({ channel: "chrome", headless: true });
  const page = await browser.newPage();
  try {
    page.on("response", (r) => {
      const url = r.url();
      if (url.includes("/api/")) {
        process.stdout.write(`[warm] ${r.status()} ${r.request().method()} ${url.replace("http://127.0.0.1:5193", "")}\n`);
      }
    });
    page.on("pageerror", (e) => {
      process.stdout.write(`[warm:pageerror] ${(e.stack ?? String(e)).slice(0, 1200)}\n`);
    });
    await page.goto("http://127.0.0.1:5193/workspace", {
      waitUntil: "domcontentloaded",
      timeout: 60_000,
    });
    await page.waitForSelector(".app-chat-input", { timeout: 60_000 });
    // give React a moment to settle before the tests start
    await page.waitForTimeout(1500);
  } finally {
    await browser.close();
  }
}
