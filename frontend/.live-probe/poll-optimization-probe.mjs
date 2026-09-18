/**
 * 临时验收探针：poll 优化后的实际网络行为。
 *
 * 观测四点：
 * 1. 轮询 URL 是否都带 `?view=light`；
 * 2. 空闲自适应：请求间隔是否按 1.5× 从 3s 退避（窗口内轮次递减）；
 * 3. 页面隐藏（切到另一个标签页）后是否 0 请求；
 * 4. 恢复可见后是否立即补一轮。
 */
import { chromium } from "@playwright/test";

const BASE = process.env.PROBE_BASE ?? "http://localhost:5193";
const WINDOW_MS = Number(process.env.PROBE_WINDOW_MS ?? 30000);
const HIDDEN_MS = Number(process.env.PROBE_HIDDEN_MS ?? 20000);

const browser = await chromium.launch({ channel: "chrome", headless: true });
const context = await browser.newContext();
const page = await context.newPage();

// headless Chromium 不会因切标签页可靠翻转 visibilityState，这里显式模拟：
// 覆盖 document.hidden/visibilityState 并派发 visibilitychange（注册表读 document.hidden）。
await page.addInitScript(() => {
  let hidden = false;
  Object.defineProperty(document, "visibilityState", {
    get: () => (hidden ? "hidden" : "visible"),
    configurable: true,
  });
  Object.defineProperty(document, "hidden", {
    get: () => hidden,
    configurable: true,
  });
  window.__probeSetHidden = (value) => {
    hidden = Boolean(value);
    document.dispatchEvent(new Event("visibilitychange"));
  };
});

const polls = [];
const consoleErrors = [];
const isPoll = (url) =>
  /\/api\/runtime\/sessions\/[^/]+\/runtime(\?|$)/.test(url) &&
  !url.includes("/runtime/stream");

page.on("request", (request) => {
  if (isPoll(request.url())) {
    polls.push({ t: Date.now(), url: request.url() });
  }
});
page.on("console", (message) => {
  if (message.type() === "error") {
    consoleErrors.push(message.text());
  }
});

const startedAt = Date.now();
await page.goto(`${BASE}/workspace/chats/new`, { waitUntil: "domcontentloaded" });
await page.waitForTimeout(WINDOW_MS);

const visiblePolls = polls.length;
const visibilityBefore = await page.evaluate(() => document.visibilityState);
await page.evaluate(() => window.__probeSetHidden(true));
const visibilityAfter = await page.evaluate(() => document.visibilityState);
const beforeHidden = polls.length;
await page.waitForTimeout(HIDDEN_MS);
const duringHidden = polls.length - beforeHidden;

const beforeResume = polls.length;
await page.evaluate(() => window.__probeSetHidden(false));
const visibilityResumed = await page.evaluate(() => document.visibilityState);
await page.waitForTimeout(3_000);
const afterResume = polls.length - beforeResume;

const bySession = new Map();
for (const item of polls) {
  const id = item.url.match(/sessions\/([^/]+)\/runtime/)?.[1] ?? "?";
  bySession.set(id, (bySession.get(id) ?? 0) + 1);
}
const lightCount = polls.filter((item) => item.url.includes("view=light")).length;

console.log(
  JSON.stringify(
    {
      windowMs: WINDOW_MS,
      totalPolls: polls.length,
      lightCount,
      allLight: lightCount === polls.length,
      visiblePolls,
      visibilityBefore,
      visibilityAfter,
      visibilityResumed,
      hiddenWindowMs: HIDDEN_MS,
      duringHidden,
      resumeWindowMs: 3000,
      afterResume,
      bySession: [...bySession].map(([id, count]) => ({ id, count })),
      offsets: polls.map((item) => item.t - startedAt),
      consoleErrors,
    },
    null,
    1,
  ),
);

await browser.close();
