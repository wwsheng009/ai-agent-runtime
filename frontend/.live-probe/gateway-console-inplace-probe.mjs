// 探针：验证 gateway console 会话板「原位更新」——数据刷新不重建行 DOM，
// 尾部滚动位置不被重置（跟随关闭时），开启跟随时钉到底部。
// 运行：node .live-probe/gateway-console-inplace-probe.mjs（cwd=frontend）
import fs from "node:fs";
import path from "node:path";
import { JSDOM, VirtualConsole } from "jsdom";

const consoleDir = "E:/projects/ai/ai-agent-runtime/backend/cmd/gateway/console";
const html = fs.readFileSync(path.join(consoleDir, "index.html"), "utf8");
const appJs = fs.readFileSync(path.join(consoleDir, "app.js"), "utf8");

const vc = new VirtualConsole();
vc.on("jsdomError", () => {});
vc.on("error", () => {});

const dom = new JSDOM(html, {
  url: "http://localhost:5199/#sessions",
  runScripts: "outside-only",
  pretendToBeVisual: true,
  virtualConsole: vc,
});
const { window } = dom;
const doc = window.document;

const iso = (s) => new Date(Date.now() - s * 1000).toISOString();

const mkSession1 = () => ({
  session_id: "s1", title: "会话一", state: "running", busy: true,
  status_text: "Analyzing", status_started_at: iso(120),
  status_tail: "line1\nline2", updated_at: iso(3), synced_at: iso(3),
  preview: "preview one", last_message: "**bold** 与 `code`", last_message_role: "assistant",
});
const mkSession2 = () => ({
  session_id: "s2", title: "会话二", state: "idle", busy: false,
  status_text: "", status_tail: "", updated_at: iso(60), synced_at: iso(60),
  preview: "", last_message: "", last_message_role: "",
});
const mkNode = (sessions) => ({
  node_id: "n1", name: "Node One", kind: "agent", stale: false,
  capabilities: ["input"], routes: ["/web"], sessions,
});

let nodesPayload = { nodes: [mkNode([mkSession1()])] };

window.fetch = async (url) => {
  const u = String(url);
  if (u.includes("/api/nodes")) {
    return { status: 200, ok: true, json: async () => nodesPayload };
  }
  return { status: 200, ok: true, json: async () => ({}) };
};

class FakeES {
  constructor(url) { this.url = url; this.handlers = {}; FakeES.last = this; }
  addEventListener(t, fn) { (this.handlers[t] = this.handlers[t] || []).push(fn); }
}
window.EventSource = FakeES;

window.eval(appJs);

const tick = (ms = 10) => new Promise((r) => setTimeout(r, ms));
const failures = [];
const assert = (cond, msg) => {
  if (cond) { console.log("ok   - " + msg); return; }
  failures.push(msg);
  console.log("FAIL - " + msg);
};

let card = null;
for (let i = 0; i < 100 && !card; i++) {
  await tick(5);
  card = doc.querySelector('[data-draft-key="n1|s1"]');
}
assert(!!card, "初始卡片已渲染");
if (!card) { console.error("无法继续：卡片未渲染"); dom.window.close(); process.exit(1); }

const cardSel = '[data-draft-key="n1|s1"]';
const tb = card.querySelector(".s-tail-body");

// 给 <pre> 打上可测的滚动度量（jsdom 无布局引擎）
let st = 120, sh = 500, ch = 100;
Object.defineProperty(tb, "scrollHeight", { get: () => sh, configurable: true });
Object.defineProperty(tb, "clientHeight", { get: () => ch, configurable: true });
Object.defineProperty(tb, "scrollTop", { get: () => st, set: (v) => { st = v; }, configurable: true });

assert(tb.textContent.includes("line2"), "初始尾流文本已渲染");
assert(!!tb.querySelector("p"), "尾流默认按 Markdown 渲染（段落 p）");
const lastEl = card.querySelector(".s-last-text");
assert(!!lastEl && !!lastEl.querySelector("strong") && !!lastEl.querySelector("code"),
  "最近消息默认按 Markdown 渲染（strong/code）");

// 模拟用户向上滚动阅读
st = 120;
tb.dispatchEvent(new window.Event("scroll"));
await tick();
const followBtn = card.querySelector("[data-tail-follow]");
assert(followBtn.textContent === "跳到底部", "上滚后跟随按钮切换为「跳到底部」");

const es = FakeES.last;
assert(!!es && Array.isArray(es.handlers.session), "SSE 已订阅（session 通道）");
const emitSession = (session) => {
  for (const fn of es.handlers.session || []) {
    fn({ data: JSON.stringify({ node_id: "n1", session }) });
  }
};

// 数据更新（尾流变长 + 状态文本变化）
const s1b = mkSession1();
s1b.status_tail = "line1\nline2\nline3\nline4";
s1b.status_text = "Writing";
emitSession(s1b);
await tick();

const cardAfter = doc.querySelector(cardSel);
assert(cardAfter === card, "更新后卡片元素身份不变（未重建）");
assert(card.querySelector(".s-tail-body") === tb, "更新后尾流 <pre> 元素身份不变");
assert(tb.textContent.includes("line4"), "尾流文本原位更新");
assert(card.querySelector(".s-status-text").textContent === "Writing", "状态文本原位更新");
assert(st === 120, "跟随关闭时 scrollTop 保持（st=" + st + "，期望 120）");

// 点击「跟随」→ 钉到底部；后续更新持续钉住
followBtn.dispatchEvent(new window.Event("click"));
assert(st === sh, "点击跟随滚到底部（st=" + st + "）");
const s1c = mkSession1();
s1c.status_tail = "line1\nline2\nline3\nline4\nline5";
emitSession(s1c);
await tick();
assert(st === sh, "跟随时更新继续钉底（st=" + st + "）");
assert(card.querySelector(".s-tail-body") === tb, "跟随模式更新后身份仍不变");

// 同节点第二个会话出现 → 生成分组；s1 卡片被移动而非重建
emitSession(mkSession2());
await tick();
const group = doc.querySelector(".session-group");
assert(!!group, "多会话节点生成分组");
assert(group && group.querySelector(cardSel) === card, "s1 卡片移入分组且未重建");
assert(!!group && !!group.querySelector('[data-draft-key="n1|s2"]'), "s2 卡片已创建");
assert(card.querySelector(".s-tail-body") === tb, "移动后尾流元素仍为同一实例");

// 分组形成后继续更新 s1：滚动位置依然保留（sticky 模式已开启则钉底，此处仍为跟随态）
const s1d = mkSession1();
s1d.status_tail = "line1\nline2\nline3\nline4\nline5\nline6";
emitSession(s1d);
await tick();
assert(st === sh, "分组内更新（跟随中）保持钉底");

// Markdown 开关（会话工具栏按钮，默认开）：关闭回落纯文本（保留换行），再次
// 开启恢复富文本；两种模式下尾流元素身份都不变。
const mdBtn = doc.getElementById("md-toggle");
assert(!!mdBtn, "Markdown 开关按钮存在");
mdBtn.dispatchEvent(new window.Event("click"));
await tick();
assert(card.querySelector(".s-tail-body") === tb, "切换 Markdown 后尾流元素身份不变");
assert(!tb.querySelector("p") && tb.textContent.includes("line6"), "关闭 Markdown 后回落纯文本（保留换行）");
mdBtn.dispatchEvent(new window.Event("click"));
await tick();
assert(!!tb.querySelector("p"), "重新开启 Markdown 后恢复渲染");

console.log("\n==== 结果 ====");
if (failures.length) {
  console.log("FAILED: " + failures.length + " 项");
  failures.forEach((f) => console.log(" - " + f));
  dom.window.close();
  process.exit(1);
}
console.log("ALL PASS");
dom.window.close();
process.exit(0);