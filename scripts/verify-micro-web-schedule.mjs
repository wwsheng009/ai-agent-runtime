// 运行：node scripts/verify-micro-web-schedule.mjs（无需浏览器；见 docs/aicli/web-testing.md）
// P4：自动化任务页签——结构静态契约 + js/schedule.js 行为（迷你 DOM + fetch stub）。
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const webRoot = path.join(root, "backend", "cmd", "aicli", "commands", "web");
let failures = 0;
function check(condition, message) {
  if (!condition) {
    failures++;
    console.error("FAIL " + message);
  }
}

// ---- 1. 静态契约：index.html 结构 ----
const html = readFileSync(path.join(webRoot, "index.html"), "utf8");
check(html.includes('id="tab-schedule-btn"'), "index.html 有 #tab-schedule-btn");
check(html.includes('id="tab-schedule"'), "index.html 有 #tab-schedule");
check(html.includes('id="schedule-list"'), "index.html 有 #schedule-list");
check(html.includes('id="schedule-refresh-btn"'), "index.html 有 #schedule-refresh-btn");

// ---- 2. 静态契约：style.css 变量已定义（无未定义的自定义属性——约束见 web-testing.md）----
const css = readFileSync(path.join(webRoot, "style.css"), "utf8");
const defined = new Set();
for (const match of css.matchAll(/(--[a-zA-Z0-9-]+)\s*:/g)) {
  defined.add(match[1]);
}
const scheduleBlock = css.slice(css.indexOf("自动化任务页签"));
for (const match of scheduleBlock.matchAll(/var\((--[a-zA-Z0-9-]+)/g)) {
  const token = match[1];
  check(
    defined.has(token) || token === "--danger" || token === "--ok",
    `schedule 样式使用的 ${token} 必须已在主题定义（或带 fallback 字面量）`,
  );
}
check(!/var\(--muted-fg/.test(scheduleBlock), "schedule 样式不使用未定义的 --muted-fg");
check(!/var\(--panel-bg/.test(scheduleBlock), "schedule 样式不使用未定义的 --panel-bg");

// ---- 3. 行为：js/schedule.js（迷你 DOM + fetch stub）----
const pendingResponses = [];
globalThis.fetch = (url, options) => {
  const entry = pendingResponses.shift();
  if (!entry) {
    return Promise.reject(new Error("unexpected fetch: " + url));
  }
  const [matcher, response] = entry;
  if (typeof matcher === "string" ? !url.includes(matcher) : !matcher(url, options)) {
    return Promise.reject(new Error("unexpected fetch: " + url));
  }
  return Promise.resolve({
    ok: response.ok !== false,
    status: response.status ?? 200,
    json: () => Promise.resolve(response.body),
  });
};

const elements = {};
function makeElement(id) {
  return {
    id,
    innerHTML: "",
    attrs: {},
    listeners: {},
    classList: {
      toggle() {},
      add() {},
      remove() {},
    },
    setAttribute(name, value) {
      this.attrs[name] = value;
    },
    getAttribute(name) {
      return this.attrs[name] ?? null;
    },
    addEventListener(name, handler) {
      this.listeners[name] = handler;
    },
    querySelectorAll() {
      return [];
    },
    querySelector() {
      return null;
    },
  };
}
// getElementById 必须返回同一实例：schedule.js 在 loadSchedule 内部再次
// getElementById('schedule-list')，若每次新建则测试断言读到的是另一个对象。
for (const id of ["schedule-list", "schedule-refresh-btn"]) {
  elements[id] = makeElement(id);
}
globalThis.document = {
  getElementById: (id) => elements[id] ?? makeElement(id),
  createElement: (tag) => makeElement("created-" + tag),
  querySelector: () => null,
  querySelectorAll: () => [],
};
globalThis.localStorage = {
  getItem: () => null,
  setItem: () => {},
};

const showToastMessages = [];
const schedule = await import(
  path.join(webRoot, "js", "schedule.js")
).catch(async () => {
  // file:// 导入需要 URL 形式；回退到文件协议。
  return import("file://" + path.join(webRoot, "js", "schedule.js"));
});
const util = await import("file://" + path.join(webRoot, "js", "util.js")).catch(() => null);

// util.js 的 showToast 在沙盒里可能依赖 DOM；用注入窗口钩子替代断言来源。
globalThis.window = globalThis;
globalThis.__testToast = showToastMessages;

pendingResponses.push([
  "/api/runtime/schedule/catalog",
  {
    body: {
      tasks: [
        {
          id: "schedule-a",
          kind: "daily",
          title: "Morning <review>",
          prompt: "Check the build & report",
          scheduledAt: "2026-10-12T01:00:00.000Z",
          status: "active",
          sessionId: "session-1",
          time: "09:00:00.000",
          timeZone: "Asia/Shanghai",
        },
        {
          id: "schedule-b",
          kind: "at",
          title: "Done task",
          prompt: "old",
          scheduledAt: "2026-10-01T00:00:00.000Z",
          status: "inactive",
          sessionId: "session-1",
          lastDelivery: { deliveredAt: "2026-10-01T00:00:05Z" },
        },
      ],
    },
  },
]);
schedule.loadSchedule(true);
await new Promise((resolve) => setTimeout(resolve, 20));

const rendered = elements["schedule-list"].innerHTML;
check(rendered.includes("schedule-a"), "渲染包含任务 id");
check(rendered.includes("&lt;review&gt;"), "标题 HTML 转义（防注入）");
check(rendered.includes("Check the build &amp; report"), "prompt HTML 转义");
check(rendered.includes("已开启"), "live 徽标");
check(rendered.includes("未运行"), "inactive 徽标");
check(rendered.includes("每天"), "kind 文案");
check(rendered.includes("09:00:00.000"), "daily 时间摘要");
check(rendered.includes("Asia/Shanghai"), "时区展示");

// 首次加载后不再重复请求（loaded 标记）。
schedule.loadSchedule(false);
await new Promise((resolve) => setTimeout(resolve, 10));
check(pendingResponses.length === 0, "loaded 标记阻止重复请求");

console.log(
  failures === 0
    ? "verify-micro-web-schedule: 全部断言通过"
    : `verify-micro-web-schedule: ${failures} 项失败`,
);
process.exit(failures === 0 ? 0 : 1);
