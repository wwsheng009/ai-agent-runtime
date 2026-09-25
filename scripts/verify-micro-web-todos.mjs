// 行为验证：aicli micro web client 任务列表浮动面板（composer 上沿，无需浏览器）。
// 运行：node scripts/verify-micro-web-todos.mjs（仓库根目录）
// 覆盖：
//   1. index.html 结构：#todo-panel 在 #composer-panel 内、位于标题行之前（底边与
//      composer 面板上沿衔接）；必需元素与无障碍属性齐全；默认 hidden。
//   2. style.css 不变量：bottom:calc(100% + 1px) 贴附几何、[hidden] 不占位、
//      折叠只收 .todo-body、状态样式（已完成删除线 / 进行中强调）、不让页面让位。
//   3. 接线：sse.js 在 switch 前调用 handleTodoSSEEvent（tool_end 实时 + 会话边界清空）、
//      chat.js 在 screen 响应里 applyTodoReplay、app.js 初始化、后端两条通道的字段名。
//   4. js/todos.js 纯函数：解析裁剪（坏条目丢弃、整组不可用 → null）、计数 / 进度 /
//      当前项 / 展示文案、快照合并（runtime 按 seq 单调、history 只兜底）。
//   5. js/todos.js 面板行为（DOM stub + 动态 import）：无快照隐藏；回放恢复（计数 +
//      进度条 + 逐项状态 + 删除线类名）；实时按 seq 推进（旧序号不回退）；折叠切换与
//      localStorage 记忆；会话切换清空；页面上没有面板时静默降级。
import assert from "node:assert";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const REPO_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const WEB_DIR = path.join(REPO_ROOT, "backend", "cmd", "aicli", "commands", "web");
const INDEX_HTML = fs.readFileSync(path.join(WEB_DIR, "index.html"), "utf8");
const STYLE_CSS = fs.readFileSync(path.join(WEB_DIR, "style.css"), "utf8");
const APP_JS = fs.readFileSync(path.join(WEB_DIR, "app.js"), "utf8");
const SSE_JS = fs.readFileSync(path.join(WEB_DIR, "js", "sse.js"), "utf8");
const CHAT_JS = fs.readFileSync(path.join(WEB_DIR, "js", "chat.js"), "utf8");
const TODOS_JS = fs.readFileSync(path.join(WEB_DIR, "js", "todos.js"), "utf8");
const WEB_SCHEMA_GO = fs.readFileSync(path.join(REPO_ROOT, "backend", "cmd", "aicli", "commands", "web_schema.go"), "utf8");
const SCREEN_GO = fs.readFileSync(path.join(REPO_ROOT, "backend", "cmd", "aicli", "commands", "chat_debug_screen_http.go"), "utf8");

let failures = 0;
function check(name, fn) {
  try {
    fn();
    console.log("  ok  - " + name);
  } catch (err) {
    failures++;
    console.log("  FAIL- " + name + "\n        " + (err && err.message ? err.message : err));
  }
}
async function checkAsync(name, fn) {
  try {
    await fn();
    console.log("  ok  - " + name);
  } catch (err) {
    failures++;
    console.log("  FAIL- " + name + "\n        " + (err && err.message ? err.message : err));
  }
}

// ===========================================================================
// 场景 1：index.html 结构（面板在 composer 面板内、标题行之前）
// ===========================================================================
console.log("[1] index.html 任务列表结构");

// 面板起点必须在 composer-header 之前：底边与 composer 面板上沿衔接，列表在标题行「上面」。
const todoPanelIdx = INDEX_HTML.indexOf('id="todo-panel"');
const composerHeaderIdx = INDEX_HTML.indexOf('id="composer-header"');
const composerPanelIdx = INDEX_HTML.indexOf('id="composer-panel"');
check("#todo-panel 在 #composer-panel 内且位于 #composer-header 之前（贴在面板上沿）", () => {
  assert.ok(todoPanelIdx > 0, "index.html 缺少 #todo-panel");
  assert.ok(composerPanelIdx > 0 && composerPanelIdx < todoPanelIdx, "#todo-panel 应在 #composer-panel 之后");
  assert.ok(composerHeaderIdx > todoPanelIdx, "#todo-panel 必须在 #composer-header 之前（显示在 composer 上沿）");
});
check("折叠按钮固定在标题行最右（标题 / 计数在左，当前任务靠右收尾）", () => {
  const head = INDEX_HTML.slice(INDEX_HTML.indexOf('class="todo-head"'), INDEX_HTML.indexOf('id="todo-body"'));
  assert.ok(head.indexOf('id="todo-toggle"') > 0, "标题行缺少折叠按钮");
  assert.ok(head.indexOf('id="todo-toggle"') > head.indexOf('id="todo-current"'),
    "折叠按钮应排在 #todo-current 之后（贴着标题行右端）");
  assert.ok(head.indexOf('id="todo-progress"') < head.indexOf('id="todo-current"'),
    "计数应在左、当前任务在其右侧（仍在按钮之前）");
  const headRule = STYLE_CSS.match(/#todo-panel\s+\.todo-head\s*\{[\s\S]*?\}/);
  assert.ok(headRule && /padding:\s*4px\s+6px\s+4px\s+8px/.test(headRule[0]),
    "标题行内边距应左 8px（对齐列表）/ 右 6px（贴住右端按钮）");
});
check("面板必需元素与无障碍属性齐全", () => {
  ["todo-toggle", "todo-progress", "todo-current", "todo-progressbar", "todo-progressbar-fill", "todo-list", "todo-body"].forEach((id) => {
    assert.ok(INDEX_HTML.indexOf('id="' + id + '"') >= 0, "缺少 #" + id);
  });
  assert.ok(/id="todo-panel"[^>]*role="region"/.test(INDEX_HTML), "面板缺少 role=region");
  assert.ok(/id="todo-panel"[^>]*aria-label="任务列表"/.test(INDEX_HTML), "面板缺少 aria-label");
  assert.ok(/id="todo-panel"[^>]*hidden/.test(INDEX_HTML), "面板默认应 hidden（无任务不占位）");
  assert.ok(/id="todo-toggle"[^>]*type="button"/.test(INDEX_HTML), "折叠按钮应是按钮");
  assert.ok(/id="todo-toggle"[^>]*aria-expanded="true"/.test(INDEX_HTML), "折叠按钮缺少 aria-expanded");
  assert.ok(/id="todo-toggle"[^>]*aria-controls="todo-body"/.test(INDEX_HTML), "折叠按钮缺少 aria-controls");
  assert.ok(/id="todo-progressbar"[^>]*role="progressbar"/.test(INDEX_HTML), "进度条缺少 role=progressbar");
  assert.ok(/id="todo-progressbar"[\s\S]{0,160}aria-valuenow="0"/.test(INDEX_HTML), "进度条缺少 aria-valuenow");
});

// ===========================================================================
// 场景 2：style.css 不变量
// ===========================================================================
console.log("[2] style.css 贴附与折叠不变量");
check("面板贴在 composer 上沿：absolute + bottom:calc(100% + 1px) + 左右外扩 1px", () => {
  const block = STYLE_CSS.match(/#todo-panel\s*\{[\s\S]*?\}/);
  assert.ok(block, "style.css 缺少 #todo-panel 基础规则");
  const rule = block[0];
  assert.ok(/position:\s*absolute/.test(rule), "面板应为绝对定位子层（随 composer 移动，不参与常规流）");
  assert.ok(/bottom:\s*calc\(100%\s*\+\s*1px\)/.test(rule), "面板底边应贴到 composer 面板外边框上沿（bottom:calc(100% + 1px)）");
  assert.ok(/left:\s*-1px/.test(rule) && /right:\s*-1px/.test(rule), "面板左右应与 composer 外边框对齐");
  assert.ok(/border-bottom:\s*none/.test(rule), "衔接处只保留 composer 的顶边框（面板自身不画底边）");
  assert.ok(/border-radius:\s*10px\s+10px\s+0\s+0/.test(rule), "面板只应有上圆角（下沿与 composer 融合）");
});
check("[hidden] 不占位：面板默认不显示，且不引入任何让位机制", () => {
  assert.ok(/#todo-panel\[hidden\]\s*\{\s*display:\s*none/.test(STYLE_CSS), "缺少 #todo-panel[hidden] { display:none }");
  assert.ok(!/--todo-reserve/.test(STYLE_CSS), "不应引入弹层让位变量");
  assert.ok(!/body[^{]*\{[^}]*todo/i.test(STYLE_CSS), "body 不应为任务面板留白");
});
check("折叠只收 .todo-body（标题行保留计数与当前任务）", () => {
  assert.ok(/#todo-panel\.todo-collapsed\s+\.todo-body\s*\{\s*display:\s*none/.test(STYLE_CSS), "缺少折叠规则");
});
check("状态样式：进行中强调、已完成删除线、待处理弱化", () => {
  assert.ok(/\.todo-status-in-progress\s+\.todo-text\s*\{[^}]*font-weight:\s*600/.test(STYLE_CSS), "进行中应加粗");
  assert.ok(/\.todo-status-completed\s+\.todo-text\s*\{[^}]*text-decoration:\s*line-through/.test(STYLE_CSS), "已完成应有删除线（勾除语义）");
  assert.ok(/\.todo-status-pending\s*\{[^}]*color:/.test(STYLE_CSS), "待处理应有独立配色");
});

// ===========================================================================
// 场景 3：接线（SSE 实时 / screen 回放 / 入口初始化 / 后端字段）
// ===========================================================================
console.log("[3] 接线");
check("sse.js：switch 前调用 handleTodoSSEEvent，且事件名与 tool_end 对齐", () => {
  const callIdx = SSE_JS.indexOf("handleTodoSSEEvent(eventName, data)");
  assert.ok(callIdx > 0, "sse.js 未接任务面板");
  assert.ok(callIdx < SSE_JS.indexOf("switch (eventName)"), "面板更新应在 switch 之前（与分支互不依赖）");
  assert.ok(/import\s*\{\s*handleTodoSSEEvent\s*\}\s*from\s*"\.\/todos\.js"/.test(SSE_JS), "缺少 todos.js 导入");
});
check("chat.js：screen 响应应用 applyTodoReplay(data.todo_snapshot)", () => {
  assert.ok(/import\s*\{\s*applyTodoReplay\s*\}\s*from\s*"\.\/todos\.js"/.test(CHAT_JS), "缺少 todos.js 导入");
  assert.ok(/applyTodoReplay\(data && data\.todo_snapshot\)/.test(CHAT_JS), "refreshScreen 未接回放快照");
  const callIdx = CHAT_JS.indexOf("applyTodoReplay(data && data.todo_snapshot)");
  assert.ok(callIdx < CHAT_JS.indexOf("if (!data || !data.available)"), "回放应在 available 分支之前（空会话也要按快照清/留）");
});
check("app.js 初始化面板", () => {
  assert.ok(/import\s*\{\s*initTodoPanel\s*\}\s*from\s*"\.\/js\/todos\.js"/.test(APP_JS), "app.js 缺少导入");
  assert.ok(/^initTodoPanel\(\);/m.test(APP_JS), "app.js 未调用 initTodoPanel()");
});
check("后端两通道：tool_end 带 todo_snapshot；screen JSON 带 todo_snapshot", () => {
  assert.ok(/data\["todo_snapshot"\]\s*=\s*snapshot/.test(WEB_SCHEMA_GO),
    "web_schema.go 的 tool_end 未挂 todo_snapshot");
  assert.ok(/chatWebTodoSnapshotFromToolPayload\(payload\)/.test(WEB_SCHEMA_GO),
    "web_schema.go 应只提取裁剪快照（不做工具名特判）");
  assert.ok(/snap\.TodoSnapshot\s*=\s*chatWebTodoSnapshotForSession\(\)/.test(SCREEN_GO),
    "chat_debug_screen_http.go 的 web JSON 未挂回放快照");
});

// ===========================================================================
// 场景 4：js/todos.js 纯函数
// ===========================================================================
console.log("[4] js/todos.js 纯函数");
const TODO_KEY = "aicli.web.todos.v1";

// ---- DOM stub（只实现 todos.js 用到的表面） ----
function classListOf(el) { return String(el.attrs.class || "").split(/\s+/).filter(Boolean); }
function setClassList(el, list) { el.attrs.class = list.join(" "); }

function makeEl(tag, id) {
  const el = {
    tagName: String(tag || "div").toUpperCase(),
    id: id || "",
    attrs: id ? { id: id } : {},
    style: {},
    listeners: {},
    childNodes: [],
    hidden: false,
    title: "",
  };
  let text = "";
  Object.defineProperty(el, "textContent", {
    get() { return text; },
    set(value) {
      text = value === undefined || value === null ? "" : String(value);
      if (el.childNodes.length) { setChildren(el, []); }
    },
  });
  el.classList = {
    contains: (c) => classListOf(el).indexOf(c) >= 0,
    add: function () {
      const list = classListOf(el);
      Array.prototype.slice.call(arguments).join(" ").split(/\s+/).filter(Boolean).forEach((c) => {
        if (list.indexOf(c) < 0) { list.push(c); }
      });
      setClassList(el, list);
    },
    remove: function () {
      const drop = Array.prototype.slice.call(arguments).join(" ").split(/\s+/).filter(Boolean);
      setClassList(el, classListOf(el).filter((c) => drop.indexOf(c) < 0));
    },
    toggle: (c, on) => {
      const has = classListOf(el).indexOf(c) >= 0;
      const want = on === undefined ? !has : !!on;
      if (want && !has) { el.classList.add(c); }
      if (!want && has) { el.classList.remove(c); }
    },
  };
  el.getAttribute = (name) => (name in el.attrs ? el.attrs[name] : null);
  el.setAttribute = (name, value) => { el.attrs[name] = String(value); };
  // 与浏览器一致：className ↔ class 属性互通（模块用 className 写条目状态类）。
  Object.defineProperty(el, "className", {
    get() { return el.attrs.class || ""; },
    set(value) { el.attrs.class = String(value == null ? "" : value); },
  });
  el.addEventListener = (type, fn) => { (el.listeners[type] = el.listeners[type] || []).push(fn); };
  el.dispatch = (type, ev) => {
    const event = ev || { type: type };
    if (!event.type) { event.type = type; }
    (el.listeners[type] || []).forEach((fn) => fn(event));
  };
  el.appendChild = (child) => { el.childNodes.push(child); return child; };
  return el;
}
function setChildren(el, list) { el.childNodes = list; }
function findByClass(root, cls) {
  if (classListOf(root).indexOf(cls) >= 0) { return root; }
  for (const child of root.childNodes) {
    const hit = findByClass(child, cls);
    if (hit) { return hit; }
  }
  return null;
}
function findAllByClass(root, cls, out) {
  const acc = out || [];
  if (classListOf(root).indexOf(cls) >= 0) { acc.push(root); }
  for (const child of root.childNodes) { findAllByClass(child, cls, acc); }
  return acc;
}

const elements = {};
["todo-panel", "todo-toggle", "todo-progress", "todo-current", "todo-progressbar", "todo-progressbar-fill", "todo-list"]
  .forEach((id) => { elements[id] = makeEl(id === "todo-list" ? "ul" : "div", id); });
elements["todo-panel"].hidden = true;
const store = {};
globalThis.document = {
  getElementById: (id) => elements[id] || null,
  createElement: (tag) => makeEl(tag),
};
globalThis.localStorage = {
  getItem: (k) => (k in store ? store[k] : null),
  setItem: (k, v) => { store[k] = String(v); },
  removeItem: (k) => { delete store[k]; },
};

const todos = await import(pathToFileURL(path.join(WEB_DIR, "js", "todos.js")).href);

check("normalizeTodoStatus：只认三态契约", () => {
  assert.equal(todos.normalizeTodoStatus("pending"), "pending");
  assert.equal(todos.normalizeTodoStatus(" in_progress "), "in_progress");
  assert.equal(todos.normalizeTodoStatus("completed"), "completed");
  assert.equal(todos.normalizeTodoStatus("running"), "");
  assert.equal(todos.normalizeTodoStatus(null), "");
});
check("parseTodoItems：坏条目丢弃；整组不可用返回 null（不清空面板）", () => {
  const items = todos.parseTodoItems([
    { content: "写面板", status: "in_progress", active_form: "写面板中" },
    { content: "", status: "pending" },
    { content: "状态非法", status: "running" },
    { content: "补样式", status: "pending" },
  ]);
  assert.equal(items.length, 2);
  assert.deepEqual(items[0], { content: "写面板", status: "in_progress", activeForm: "写面板中" });
  assert.equal(items[1].activeForm, "");
  assert.equal(todos.parseTodoItems("nope"), null);
  assert.equal(todos.parseTodoItems([{ content: "坏", status: "?" }]), null);
  assert.deepEqual(todos.parseTodoItems([]), []);
});
check("countTodoItems / todoProgressPercent：进度按已完成 / 总数", () => {
  const items = todos.parseTodoItems([
    { content: "a", status: "completed" },
    { content: "b", status: "completed" },
    { content: "c", status: "in_progress" },
    { content: "d", status: "pending" },
  ]);
  const counts = todos.countTodoItems(items);
  assert.deepEqual(counts, { total: 4, pending: 1, inProgress: 1, completed: 2 });
  assert.equal(todos.todoProgressPercent(items), 50);
  assert.equal(todos.todoProgressPercent([]), 0);
  assert.equal(todos.currentTodoItem(items).content, "c");
  assert.equal(todos.todoItemLabel({ content: "c", activeForm: "处理 c 中" }), "处理 c 中");
  assert.equal(todos.todoItemLabel({ content: "c", activeForm: "" }), "c");
});
check("mergeTodoSnapshot：runtime 按 seq 单调；history 只在没有实时快照时兜底", () => {
  const history = { items: [{ content: "旧", status: "pending" }], source: "history", seq: 0 };
  const live1 = { items: [{ content: "新1", status: "in_progress" }], source: "runtime", seq: 5 };
  const live2 = { items: [{ content: "新2", status: "completed" }], source: "runtime", seq: 9 };
  assert.equal(todos.mergeTodoSnapshot(null, history), history, "空状态由回放兜底");
  assert.equal(todos.mergeTodoSnapshot(history, live1), live1, "实时覆盖回放");
  assert.equal(todos.mergeTodoSnapshot(live2, history), live2, "回放不得覆盖实时");
  assert.equal(todos.mergeTodoSnapshot(live2, live1), live2, "旧序号（重连乱序）不回退");
  assert.equal(todos.mergeTodoSnapshot(live2, null), live2, "空载荷保持现值");
});

// ===========================================================================
// 场景 5：面板行为（init + 渲染 + 折叠 + 会话边界）
// ===========================================================================
console.log("[5] js/todos.js 面板行为");
const panel = elements["todo-panel"];
const list = elements["todo-list"];
const progressEl = elements["todo-progress"];
const currentEl = elements["todo-current"];
const bar = elements["todo-progressbar"];
const fill = elements["todo-progressbar-fill"];
const toggleBtn = elements["todo-toggle"];

check("初始状态：面板 hidden、未渲染任务", () => {
  todos.initTodoPanel();
  assert.equal(panel.hidden, true, "无快照应隐藏");
  assert.equal(list.childNodes.length, 0, "不应预渲染空壳");
  assert.equal(panel.getAttribute("aria-hidden"), "true");
});
check("回放快照：显示面板、计数、进度条与逐项状态", () => {
  const applied = todos.applyTodoReplay({
    items: [
      { content: "实现任务面板", status: "in_progress", active_form: "实现任务面板中" },
      { content: "接线 SSE", status: "pending" },
      { content: "写校验脚本", status: "completed" },
    ],
    session_id: "session-1",
  });
  assert.equal(applied, true);
  assert.equal(panel.hidden, false, "有任务应显示");
  assert.equal(progressEl.textContent, "已完成 1 · 进行中 1 · 待处理 1");
  assert.equal(currentEl.textContent, "实现任务面板中");
  assert.equal(bar.getAttribute("aria-valuemax"), "3");
  assert.equal(bar.getAttribute("aria-valuenow"), "1");
  assert.equal(fill.style.width, "33%");
  assert.equal(list.childNodes.length, 3, "应渲染三条");
  assert.equal(list.childNodes[0].getAttribute("data-status"), "in_progress");
  assert.ok(classListOf(list.childNodes[0]).indexOf("todo-status-in-progress") >= 0);
  assert.ok(classListOf(list.childNodes[2]).indexOf("todo-status-completed") >= 0, "已完成项带删除线样式类");
  const text = findAllByClass(list.childNodes[0], "todo-text")[0];
  assert.equal(text.textContent, "实现任务面板中", "进行中显示执行态文案");
  assert.equal(text.title, "实现任务面板", "title 始终为任务描述");
});
check("全部完成：面板保留（可见勾除结果）并整体降噪", () => {
  todos.applyTodoReplay({
    items: [{ content: "唯一任务", status: "completed" }],
    session_id: "session-1",
  });
  assert.equal(panel.hidden, false);
  assert.equal(currentEl.textContent, "全部完成 ✓");
  assert.equal(progressEl.textContent, "已完成 1");
  assert.ok(classListOf(panel).indexOf("todo-all-done") >= 0);
});
check("实时通道按 seq 推进：旧序号不回退，新序号替换", () => {
  const newer = {
    todo_snapshot: { items: [{ content: "实时任务", status: "in_progress", active_form: "实时任务中" }], session_id: "session-1" },
    _event: { sequence: 42 },
  };
  assert.equal(todos.handleTodoSSEEvent("tool_end", newer), true);
  assert.equal(progressEl.textContent, "进行中 1");
  const stale = {
    todo_snapshot: { items: [{ content: "过期任务", status: "pending" }], session_id: "session-1" },
    _event: { sequence: 41 },
  };
  assert.equal(todos.handleTodoSSEEvent("tool_end", stale), false, "旧序号应被忽略");
  assert.equal(progressEl.textContent, "进行中 1", "面板不应回退");
  assert.equal(todos.getTodoSnapshot().items[0].content, "实时任务");
});
check("回放不覆盖实时快照", () => {
  todos.applyTodoReplay({ items: [{ content: "历史任务", status: "pending" }], session_id: "session-1" });
  assert.equal(todos.getTodoSnapshot().items[0].content, "实时任务");
});
check("同一份回放去重：秒级 refreshScreen 不重复重建列表", () => {
  todos.resetTodoPanel();
  const payload = { items: [{ content: "去重任务", status: "pending" }], session_id: "session-1" };
  assert.equal(todos.applyTodoReplay(payload), true, "首次应写入");
  assert.equal(todos.applyTodoReplay(payload), false, "同内容重复回放应跳过");
  assert.equal(todos.applyTodoReplay({ items: [{ content: "去重任务", status: "completed" }], session_id: "session-1" }), true, "内容变化应更新");
  // 会话切换后即使新会话是同一份列表，也必须能重新显示（指纹已随 reset 清掉）。
  todos.handleTodoSSEEvent("session_switched", {});
  assert.equal(panel.hidden, true);
  assert.equal(todos.applyTodoReplay(payload), true, "切会话后同内容回放仍应生效");
  assert.equal(panel.hidden, false);
});
check("会话切换 / 结束：清空并隐藏（等新会话实时或回放）", () => {
  todos.handleTodoSSEEvent("session_switched", {});
  assert.equal(panel.hidden, true);
  assert.equal(todos.getTodoSnapshot(), null);
  assert.equal(list.childNodes.length, 0);
});
check("折叠切换：写 class + aria-expanded + localStorage 记忆", () => {
  todos.applyTodoReplay({ items: [{ content: "任务 A", status: "pending" }], session_id: "session-1" });
  assert.equal(toggleBtn.getAttribute("aria-expanded"), "true");
  toggleBtn.dispatch("click");
  assert.ok(classListOf(panel).indexOf("todo-collapsed") >= 0, "折叠应写 .todo-collapsed");
  assert.equal(toggleBtn.getAttribute("aria-expanded"), "false");
  assert.equal(toggleBtn.textContent, "▸");
  assert.equal(JSON.parse(store[TODO_KEY]).collapsed, true, "折叠态应落 localStorage");
  toggleBtn.dispatch("click");
  assert.ok(classListOf(panel).indexOf("todo-collapsed") < 0);
  assert.equal(JSON.parse(store[TODO_KEY]).collapsed, false);
});
check("localStorage 回放：重新 init 恢复折叠态", () => {
  toggleBtn.dispatch("click"); // 折叠
  assert.equal(JSON.parse(store[TODO_KEY]).collapsed, true);
  todos.initTodoPanel(); // 重新初始化（模拟页面刷新）
  assert.ok(classListOf(panel).indexOf("todo-collapsed") >= 0, "折叠偏好应被恢复");
  toggleBtn.dispatch("click");
  assert.ok(classListOf(panel).indexOf("todo-collapsed") < 0);
});
check("坏载荷不清空面板（解析失败保持现值）", () => {
  todos.applyTodoReplay({ items: [{}], session_id: "session-1" });
  assert.equal(todos.getTodoSnapshot().items[0].content, "任务 A");
  assert.equal(todos.handleTodoSSEEvent("tool_end", { hello: "world" }), false);
  assert.equal(progressEl.textContent, "待处理 1");
});
check("面板缺失时静默降级（旧页面 / 裁剪构建）", () => {
  const saved = elements["todo-panel"];
  delete elements["todo-panel"];
  todos.initTodoPanel(); // 不应抛异常
  elements["todo-panel"] = saved;
});

check("静态契约：模块不外发 innerHTML / 不解析工具行文本", () => {
  assert.ok(!/innerHTML/.test(TODOS_JS), "todos.js 不应使用 innerHTML（条目文案一律 textContent）");
  assert.ok(!/tool_end\"\]|result_summary|arg_preview/.test(TODOS_JS), "面板只读结构化快照，不解析工具摘要文本");
});

console.log("");
console.log(failures === 0 ? "全部通过" : failures + " 项失败");
process.exit(failures === 0 ? 0 : 1);
