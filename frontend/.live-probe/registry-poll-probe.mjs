/**
 * 临时探针（收尾需删除）：观测后台注册表的轮询/建连是否持续。
 *
 * 背景：`use-session-stream-supervisor` 在 React unmount 清理里调用
 * `disposeSessionRuntimeRegistry()`；dev 下 <StrictMode> 会 mount→cleanup→mount，
 * 组件仍持有已 dispose 的实例（`ensure()` 直接 return），后台订阅会静默失效。
 *
 * 判据：/api/runtime/sessions/{id}/runtime 轮询在 45s 窗口内应**周期重复**
 *       （poll 模式），而不是只在页面加载后出现一轮。
 */
import { chromium } from "@playwright/test";

const BASE = process.env.PROBE_BASE ?? "http://localhost:5193";
const WINDOW_MS = Number(process.env.PROBE_WINDOW_MS ?? 45000);

const browser = await chromium.launch({ channel: "chrome", headless: true });
const context = await browser.newContext();
const page = await context.newPage();

const polls = [];
const consoleErrors = [];
page.on("request", (request) => {
  if (/\/api\/runtime\/sessions\/[^/]+\/runtime(\?|$)/.test(request.url())) {
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

const bySession = new Map();
for (const item of polls) {
  const id = item.url.match(/sessions\/([^/]+)\/runtime/)?.[1] ?? "?";
  bySession.set(id, (bySession.get(id) ?? 0) + 1);
}

console.log(
  JSON.stringify(
    {
      windowMs: WINDOW_MS,
      totalPolls: polls.length,
      bySession: [...bySession].map(([id, count]) => ({ id, count })),
      offsets: polls.map((item) => item.t - startedAt),
      consoleErrors,
    },
    null,
    1,
  ),
);

await browser.close();
