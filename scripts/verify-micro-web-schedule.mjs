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
check(html.includes('id="schedule-filters"'), "index.html 有状态筛选条");
check(html.includes('id="schedule-search"'), "index.html 有任务搜索框");
check(html.includes('id="schedule-filter-target-kind"'), "index.html 有执行方式筛选");
check(html.includes('id="schedule-filter-session"'), "index.html 有会话/子会话筛选");
check(html.includes('id="schedule-filter-kind"'), "index.html 有规则类型筛选");
check(html.includes('id="schedule-filter-date"'), "index.html 有日期筛选");
check(html.includes('id="schedule-filter-reset"'), "index.html 有重置筛选入口");
check(html.includes('id="schedule-form-error-summary"'), "创建表单有可聚焦错误摘要");
check(html.includes('id="schedule-form-clock"'), "创建表单有 daily/weekly 本地时间输入");
check(html.includes('id="schedule-form-target-kind"'), "创建表单有执行上下文选择");
check(html.includes('id="schedule-form-goal"'), "创建表单有作业目标");
check(html.includes('id="schedule-form-workspace"'), "创建表单有目标工作区");
const scheduleJs = readFileSync(path.join(webRoot, "js", "schedule.js"), "utf8");
check(scheduleJs.includes('"/web/api/schedule"'), "schedule.js 使用本地 /web/api/schedule 端点");
check(!scheduleJs.includes('"/api/runtime/schedule/'), "schedule.js 不再请求未挂载的 /api/runtime/schedule");
check(scheduleJs.includes('"/update"'), "schedule.js 暴露编辑保存端点");
check(scheduleJs.includes('action === "pause"') && scheduleJs.includes('action === "resume"'),
  "schedule.js 暴露暂停/恢复端点");
check(scheduleJs.includes("target_kind") && scheduleJs.includes("target_workspace") &&
  scheduleJs.includes("schedule-form-goal"), "schedule.js 构造无会话创建负载");
check(scheduleJs.includes("getCurrentSessionID()"), "无会话创建仍记录创建者会话用于后续管理");
check(scheduleJs.includes('SCHEDULE_API_BASE + "/runs"') &&
  scheduleJs.includes("renderTaskHistory"), "schedule.js 提供运行历史查看");
check(scheduleJs.includes("updateSchedulePreview"), "schedule.js 提供触发时间预览");
check(scheduleJs.includes("schedule-form-clock"), "schedule.js 使用本地时间输入构造 daily/weekly");
check(scheduleJs.includes("applyScheduleView"), "schedule.js 的筛选/搜索只重绘视图");
check(scheduleJs.includes("rebuildSessionFilterOptions") &&
  scheduleJs.includes("createdBySessionId"), "schedule.js 按目标/创建者会话过滤");
check(scheduleJs.includes("taskMatchesDate") && scheduleJs.includes("dateSearchFragments"),
  "schedule.js 支持日期筛选与日期搜索");

// ---- 2. 静态契约：style.css 变量已定义（无未定义的自定义属性——约束见 web-testing.md）----
const css = readFileSync(path.join(webRoot, "style.css"), "utf8");
const defined = new Set();
for (const match of css.matchAll(/(--[a-zA-Z0-9-]+)\s*:/g)) {
  defined.add(match[1]);
}
const scheduleBlock = css.slice(css.indexOf("自动化任务页签"));
for (const match of scheduleBlock.matchAll(/var\((--[a-zA-Z0-9-]+)(\s*,[^)]*)?\)/g)) {
  const token = match[1];
  const fallback = (match[2] || "").trim();
  check(
    defined.has(token) || fallback.startsWith(",") || token === "--danger" || token === "--ok",
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
    json: response.jsonError
      ? () => Promise.reject(new Error("invalid json"))
      : () => Promise.resolve(response.body),
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
  // ui.js 顶层 applyTheme() 需要 documentElement/body；缺它们会让整个脚本在导入期崩溃。
  documentElement: makeElement("documentElement"),
  body: makeElement("body"),
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
  "/web/api/schedule/catalog",
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
          goal: "做一次系统检查",
          plan: ["检查磁盘", "检查内存"],
          targetKind: "headless",
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
        {
          id: "schedule-c",
          kind: "every",
          title: "Paused task",
          prompt: "resume me",
          scheduledAt: "2026-10-12T02:00:00.000Z",
          status: "paused",
          sessionId: "session-1",
          everySeconds: 300,
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
check(rendered.includes("已暂停"), "paused 徽标");
check(rendered.includes("恢复"), "paused 行有恢复动作");
check(rendered.includes("编辑"), "active/paused 行有编辑动作");
check(rendered.includes("再次排程"), "inactive 行有再次排程动作");
check(rendered.includes("运行历史"), "任务行有运行历史入口");
check(rendered.includes("每天"), "kind 文案");
check(rendered.includes("每天 09:00"), "daily 时间摘要（HH:MM 人类可读）");
check(rendered.includes("Asia/Shanghai"), "时区展示");
check(rendered.includes("每 5 分钟"), "every 时间摘要（人类可读）");
// J0/J1：作业目标、计划与执行上下文。
check(rendered.includes("做一次系统检查"), "goal 展示");
check(rendered.includes("检查磁盘") && rendered.includes("检查内存"), "plan 步骤展示");
check(rendered.includes("无人值守"), "headless 执行上下文展示");

// 后端不可用时必须展示后端明细，而不是把非 2xx 当成空目录。
pendingResponses.push([
  "/web/api/schedule/catalog",
  {
    ok: false,
    status: 503,
    body: { error: "schedule_disabled: enable schedule.enabled", code: "schedule_disabled" },
  },
]);
schedule.loadSchedule(true);
await new Promise((resolve) => setTimeout(resolve, 20));
const unavailable = elements["schedule-list"].innerHTML;
check(unavailable.includes("加载失败"), "非 2xx 目录请求显示失败态");
check(unavailable.includes("schedule_disabled"), "目录失败态展示后端错误码");

// 非 JSON 错误体（例如旧路由的 404 纯文本）也必须给出 HTTP 状态，不能静默。
pendingResponses.push([
  "/web/api/schedule/catalog",
  { ok: false, status: 404, jsonError: true },
]);
schedule.loadSchedule(true);
await new Promise((resolve) => setTimeout(resolve, 20));
const notFound = elements["schedule-list"].innerHTML;
check(notFound.includes("HTTP 404"), "非 JSON 错误体显示 HTTP 状态");

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
