// 行为验证：aicli micro web client 左右分栏助手（js/splitpane.js，Node 沙盒，stub document/window/localStorage）。
// 运行：node scripts/verify-micro-web-pane-split.mjs（无需浏览器；见 docs/aicli/web-testing.md 第 4 节）
// 覆盖：折叠 class 只在大屏生效 / 宽度写进 CSS 变量并夹进可用范围 / 端点继续推不抹记忆值 /
//       ←→ Home End 键盘微调与落盘 / 拖拽（body.pane-resizing、收尾落盘、折叠与非主键不起拖、失焦兜底）/
//       双击复位 / 跨断点对账（onApply、onBreakpoint）/ localStorage 记忆恢复与隐私模式降级 /
//       matchMedia 缺失时按窄屏降级 / 元素缺失静默降级 / 把手 aria 数值。
//
// 说明：本脚本只验「助手本身的行为」。两个页签各自的接线与 DOM 骨架（哪些 id、哪些 CSS 变量）
// 由 Go 侧 asset 契约测试钉住（backend/cmd/aicli/commands/web_handlers_pane_wiring_test.go）。
import assert from "node:assert";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const REPO_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const WEB_DIR = path.join(REPO_ROOT, "backend", "cmd", "aicli", "commands", "web", "js");

// ---- DOM stub ----
const elements = {};
function makeClassList() {
  return {
    _set: new Set(),
    contains(c) { return this._set.has(c); },
    add(c) { this._set.add(c); },
    remove(c) { this._set.delete(c); },
    toggle(c, on) { on ? this._set.add(c) : this._set.delete(c); },
  };
}
// 写 CSS 变量时同步左栏的「渲染宽度」：真实浏览器里左栏 flex-basis 就是这个变量，
// 沙盒里必须同样记账，否则 currentWidth() 读不到变化（端点判断会失真）。
let onStyleSet = null;
function makeEl(id) {
  const listeners = {};
  const attrs = {};
  const el = {
    id,
    rect: { width: 0 },
    classList: makeClassList(),
    textContent: "",
    title: "",
    _props: {},
    style: {
      setProperty(k, v) { el._props[k] = v; if (onStyleSet) { onStyleSet(el, k, v); } },
      getPropertyValue(k) { return el._props[k] || ""; },
    },
    setAttribute(k, v) { attrs[k] = String(v); },
    getAttribute(k) { return k in attrs ? attrs[k] : null; },
    addEventListener(type, fn) { (listeners[type] = listeners[type] || []).push(fn); },
    removeEventListener(type, fn) {
      const list = listeners[type] || [];
      const i = list.indexOf(fn);
      if (i >= 0) { list.splice(i, 1); }
    },
    listenerCount(type) { return (listeners[type] || []).length; },
    dispatch(type, event) { (listeners[type] || []).slice().forEach((fn) => fn(event || {})); },
    getBoundingClientRect() { return { width: el.rect.width, height: 0, top: 0, left: 0 }; },
    setPointerCapture() {},
  };
  return el;
}
onStyleSet = (el, k, v) => {
  if (el.id === "files-layout" && k === "--files-side-width") {
    elements["files-side"].rect.width = parseFloat(v) || 0;
  }
};

const documentListeners = {};
const windowListeners = {};
globalThis.document = {
  getElementById: (id) => elements[id] || (elements[id] = makeEl(id)),
  addEventListener(type, fn) { (documentListeners[type] = documentListeners[type] || []).push(fn); },
  removeEventListener(type, fn) {
    const list = documentListeners[type] || [];
    const i = list.indexOf(fn);
    if (i >= 0) { list.splice(i, 1); }
  },
  body: { classList: makeClassList() },
};
function dispatchDocument(type, event) { (documentListeners[type] || []).slice().forEach((fn) => fn(event || {})); }
function dispatchWindow(type, event) { (windowListeners[type] || []).slice().forEach((fn) => fn(event || {})); }
function listenerCount(map, type) { return (map[type] || []).length; }

// matchMedia stub：matches 可切换，change 监听可触发（模拟窗口跨过断点）。
let mqlMatches = true;
const mqlListeners = [];
const matchMediaFn = (query) => ({
  media: query,
  get matches() { return mqlMatches; },
  addEventListener(type, fn) { if (type === "change") { mqlListeners.push(fn); } },
  removeEventListener(type, fn) {
    const i = mqlListeners.indexOf(fn);
    if (i >= 0) { mqlListeners.splice(i, 1); }
  },
});
globalThis.window = {
  matchMedia: matchMediaFn,
  addEventListener(type, fn) { (windowListeners[type] = windowListeners[type] || []).push(fn); },
  removeEventListener(type, fn) {
    const list = windowListeners[type] || [];
    const i = list.indexOf(fn);
    if (i >= 0) { list.splice(i, 1); }
  },
};
function setMatches(on) { mqlMatches = on; mqlListeners.slice().forEach((fn) => fn({ matches: on })); }
function setMatchMediaEnabled(on) {
  if (on) { globalThis.window.matchMedia = matchMediaFn; } else { delete globalThis.window.matchMedia; }
}

// localStorage stub：Map 承载；storageThrows 模拟隐私模式（读写都抛）。
const storage = new Map();
let storageThrows = false;
globalThis.localStorage = {
  getItem(k) { if (storageThrows) { throw new Error("denied"); } return storage.has(k) ? storage.get(k) : null; },
  setItem(k, v) { if (storageThrows) { throw new Error("denied"); } storage.set(k, String(v)); },
  removeItem(k) { if (storageThrows) { throw new Error("denied"); } storage.delete(k); },
};

const { createSplitPane } = await import(pathToFileURL(path.join(WEB_DIR, "splitpane.js")).href);

const WIDTH_KEY = "webFilesSideWidth";
const COLLAPSED_KEY = "webFilesSideCollapsed";

function resetDom() {
  for (const k of Object.keys(elements)) { delete elements[k]; }
  for (const k of Object.keys(documentListeners)) { delete documentListeners[k]; }
  for (const k of Object.keys(windowListeners)) { delete windowListeners[k]; }
  storage.clear();
  storageThrows = false;
  mqlMatches = true;
  mqlListeners.length = 0;
  globalThis.document.body.classList = makeClassList();
  setMatchMediaEnabled(true);
}

// newPane 按「文件」页签的真实参数建一个控制器（layoutWidth 模拟容器宽度）。
function newPane(extra, layoutWidth) {
  const pane = createSplitPane(Object.assign({
    layoutId: "files-layout",
    sideId: "files-side",
    toggleId: "files-side-toggle",
    splitterId: "files-side-splitter",
    widthVar: "--files-side-width",
    widthKey: WIDTH_KEY,
    collapsedKey: COLLAPSED_KEY,
    defaultWidth: 300,
    minWidth: 200,
    maxWidth: 640,
    mainMin: 320,
    step: 24,
    collapseTitle: "折叠文件浏览器",
    expandTitle: "展开文件浏览器",
  }, extra || {}));
  // getElementById 顺带把 stub 元素建出来（真实页面里这几个元素本来就存在）
  globalThis.document.getElementById("files-layout").rect.width = layoutWidth === undefined ? 1200 : layoutWidth;
  globalThis.document.getElementById("files-side").rect.width = 0; // 尚未渲染：currentWidth() 先回落到记忆值
  return pane;
}
const layoutVar = () => elements["files-layout"]._props["--files-side-width"] || "";
const handle = () => elements["files-side-splitter"];
const toggleBtn = () => elements["files-side-toggle"];
const keyEvent = (k) => ({ key: k, prevented: false, preventDefault() { this.prevented = true; } });
const pointerEvent = (extra) => Object.assign({ button: 0, clientX: 500, pointerId: 7, prevented: false, preventDefault() { this.prevented = true; } }, extra || {});

(async () => {
  // 1) 默认状态：大屏、无记忆值 → 300px、展开；按钮文案与把手 aria 同步
  resetDom();
  let pane = newPane();
  pane.init();
  assert.strictEqual(pane.isSplit(), true, "1200px 容器应处于左右分栏");
  assert.strictEqual(pane.wantsSplit(), true, "wantsSplit 只看断点");
  assert.strictEqual(pane.isCollapsed(), false);
  assert.strictEqual(layoutVar(), "300px", "默认宽度应写进 CSS 变量");
  assert.ok(!elements["files-layout"].classList.contains("side-collapsed"));
  assert.strictEqual(toggleBtn().textContent, "«");
  assert.strictEqual(toggleBtn().getAttribute("aria-expanded"), "true");
  assert.strictEqual(toggleBtn().title, "折叠文件浏览器");
  assert.strictEqual(handle().getAttribute("aria-valuenow"), "300");
  assert.strictEqual(handle().getAttribute("aria-valuemin"), "200");
  assert.strictEqual(handle().getAttribute("aria-valuemax"), "640");
  assert.deepStrictEqual(pane.limits(), { min: 200, max: 640 });

  // 2) 记忆恢复：宽度与折叠态都从 localStorage 回来（刷新后沿用）
  resetDom();
  storage.set(WIDTH_KEY, "420");
  storage.set(COLLAPSED_KEY, "1");
  pane = newPane();
  pane.init();
  assert.strictEqual(layoutVar(), "420px", "记忆宽度应生效");
  assert.strictEqual(pane.isCollapsed(), true, "记忆折叠态应生效");
  assert.ok(elements["files-layout"].classList.contains("side-collapsed"));
  assert.strictEqual(toggleBtn().textContent, "»");
  assert.strictEqual(toggleBtn().getAttribute("aria-expanded"), "false");
  assert.strictEqual(toggleBtn().title, "展开文件浏览器");
  assert.strictEqual(handle().getAttribute("aria-valuenow"), "420", "折叠时把手报记忆宽度");

  // 3) 窄屏：折叠记忆不生效（class 不加、按钮仍是「折叠」语义），宽度变量照写
  resetDom();
  storage.set(COLLAPSED_KEY, "1");
  mqlMatches = false;
  pane = newPane();
  pane.init();
  assert.strictEqual(pane.isSplit(), false);
  assert.strictEqual(pane.isCollapsed(), true, "记忆还在，只是窄屏不生效");
  assert.ok(!elements["files-layout"].classList.contains("side-collapsed"));
  assert.strictEqual(layoutVar(), "300px");
  assert.strictEqual(toggleBtn().getAttribute("aria-expanded"), "true");

  // 4) 折叠切换：class 与记忆同步翻转
  resetDom();
  pane = newPane();
  pane.init();
  pane.toggle();
  assert.ok(elements["files-layout"].classList.contains("side-collapsed"));
  assert.strictEqual(storage.get(COLLAPSED_KEY), "1");
  assert.strictEqual(toggleBtn().textContent, "»");
  pane.toggle();
  assert.ok(!elements["files-layout"].classList.contains("side-collapsed"));
  assert.strictEqual(storage.get(COLLAPSED_KEY), "0");
  assert.strictEqual(toggleBtn().textContent, "«");

  // 5) 夹取范围：常量上下限 + 容器变窄时上限自己降（右侧至少留 320）
  resetDom();
  pane = newPane(undefined, 1200);
  pane.init();
  pane.setWidth(2000, true);
  assert.strictEqual(layoutVar(), "640px", "超过 maxWidth 应夹到 640");
  assert.strictEqual(storage.get(WIDTH_KEY), "640");
  pane.setWidth(10, true);
  assert.strictEqual(layoutVar(), "200px", "低于 minWidth 应夹到 200");
  assert.strictEqual(storage.get(WIDTH_KEY), "200");
  elements["files-layout"].rect.width = 800; // 窗口被拉窄
  pane.setWidth(640, true);
  assert.strictEqual(layoutVar(), "480px", "上限应被容器宽度压低（800-320）");
  assert.deepStrictEqual(pane.limits(), { min: 200, max: 480 });
  // 端点继续推：夹完等于当前渲染宽度 → 不落盘、不抹掉用户更宽的偏好
  storage.set(WIDTH_KEY, "555");
  pane.setWidth(9999, true);
  assert.strictEqual(storage.get(WIDTH_KEY), "555", "端点上的空操作不得覆盖记忆值");
  assert.strictEqual(layoutVar(), "480px");

  // 6) 键盘：←→ 每步 24px、Home/End 到两端并落盘；无关按键不响应、不 preventDefault
  resetDom();
  pane = newPane(undefined, 1200);
  pane.init();
  pane.setWidth(300, true);
  handle().dispatch("keydown", keyEvent("ArrowLeft"));
  assert.strictEqual(layoutVar(), "276px");
  assert.strictEqual(storage.get(WIDTH_KEY), "276", "键盘调整应落盘");
  handle().dispatch("keydown", keyEvent("ArrowRight"));
  assert.strictEqual(layoutVar(), "300px");
  handle().dispatch("keydown", keyEvent("Home"));
  assert.strictEqual(layoutVar(), "200px");
  handle().dispatch("keydown", keyEvent("End"));
  assert.strictEqual(layoutVar(), "640px");
  const other = keyEvent("a");
  handle().dispatch("keydown", other);
  assert.strictEqual(other.prevented, false, "无关按键不得 preventDefault");
  assert.strictEqual(layoutVar(), "640px");
  const left = keyEvent("ArrowLeft");
  handle().dispatch("keydown", left);
  assert.strictEqual(left.prevented, true, "方向键应 preventDefault（别让页面横滚）");

  // 7) 拖拽：按下加 body.pane-resizing 并把 move/up 挂到 document；收尾落盘并解绑
  resetDom();
  pane = newPane(undefined, 1200);
  pane.init();
  handle().dispatch("pointerdown", pointerEvent());
  assert.ok(globalThis.document.body.classList.contains("pane-resizing"), "拖拽期间应锁光标/禁选中");
  assert.strictEqual(listenerCount(documentListeners, "pointermove"), 1);
  assert.strictEqual(listenerCount(documentListeners, "pointerup"), 1);
  assert.strictEqual(listenerCount(documentListeners, "pointercancel"), 1);
  assert.strictEqual(listenerCount(windowListeners, "blur"), 1, "失焦兜底应挂上");
  dispatchDocument("pointermove", { clientX: 560, preventDefault() {} });
  assert.strictEqual(layoutVar(), "360px", "按下基准 300 + 位移 60");
  assert.strictEqual(storage.get(WIDTH_KEY), undefined, "拖拽过程不落盘（松手才写）");
  dispatchDocument("pointerup", {});
  assert.strictEqual(storage.get(WIDTH_KEY), "360", "松手落盘一次");
  assert.ok(!globalThis.document.body.classList.contains("pane-resizing"));
  assert.strictEqual(listenerCount(documentListeners, "pointermove"), 0);
  assert.strictEqual(listenerCount(windowListeners, "blur"), 0);
  // 折叠时不响应；只认主指针
  pane.toggle();
  handle().dispatch("pointerdown", pointerEvent());
  assert.ok(!globalThis.document.body.classList.contains("pane-resizing"), "折叠的把手不起拖");
  pane.toggle();
  handle().dispatch("pointerdown", pointerEvent({ button: 2 }));
  assert.ok(!globalThis.document.body.classList.contains("pane-resizing"), "只认主指针（左键）");
  // 拖到窗口外松手：失焦按结束处理，不把拖拽状态卡住
  handle().dispatch("pointerdown", pointerEvent());
  assert.ok(globalThis.document.body.classList.contains("pane-resizing"));
  dispatchWindow("blur", {});
  assert.ok(!globalThis.document.body.classList.contains("pane-resizing"), "窗口失焦应结束拖拽");
  assert.strictEqual(listenerCount(documentListeners, "pointermove"), 0);
  assert.strictEqual(listenerCount(windowListeners, "blur"), 0);

  // 8) 双击复位：回到 defaultWidth 并落盘
  resetDom();
  pane = newPane(undefined, 1200);
  pane.init();
  pane.setWidth(500, true);
  assert.strictEqual(layoutVar(), "500px");
  handle().dispatch("dblclick", {});
  assert.strictEqual(layoutVar(), "300px");
  assert.strictEqual(storage.get(WIDTH_KEY), "300");

  // 9) 跨断点对账：每次变化回调 onApply(split)；窄屏折叠只记状态，回大屏才生效
  resetDom();
  const applied = [];
  pane = newPane({ onApply: (s) => applied.push(s) });
  pane.init();
  assert.deepStrictEqual(applied, [true], "init 即对账一次");
  setMatches(false);
  assert.deepStrictEqual(applied, [true, false], "跨断点应再对账一次");
  assert.strictEqual(pane.isSplit(), false);
  pane.toggle();
  assert.ok(!elements["files-layout"].classList.contains("side-collapsed"), "窄屏不加折叠 class");
  setMatches(true);
  assert.ok(elements["files-layout"].classList.contains("side-collapsed"), "回大屏折叠态生效");
  assert.deepStrictEqual(applied, [true, false, false, true]);
  // onBreakpoint：调用方要抢在 apply 前做一次性动作（如自动选中最后打开的文件）时走它
  resetDom();
  const entered = [];
  pane = newPane({ onBreakpoint: () => { entered.push(pane.wantsSplit()); pane.apply(); } });
  pane.init();
  entered.length = 0;
  setMatches(false);
  assert.deepStrictEqual(entered, [false], "跨断点应走 onBreakpoint 而不是 apply");
  assert.strictEqual(pane.isSplit(), false);

  // 10) 隐私模式：localStorage 读写都抛 → 不崩、本次会话内照样生效
  resetDom();
  storageThrows = true;
  pane = newPane();
  pane.init();
  assert.strictEqual(layoutVar(), "300px", "读记忆失败应退回默认值");
  pane.toggle();
  assert.ok(elements["files-layout"].classList.contains("side-collapsed"), "写记忆失败不影响本次会话");
  pane.setWidth(400, true);
  assert.strictEqual(layoutVar(), "400px");
  storageThrows = false;

  // 11) matchMedia 缺失：按窄屏降级，其余能力照旧
  resetDom();
  setMatchMediaEnabled(false);
  pane = newPane();
  pane.init();
  assert.strictEqual(pane.isSplit(), false);
  assert.strictEqual(pane.wantsSplit(), false);
  assert.strictEqual(layoutVar(), "300px");
  pane.toggle();
  assert.ok(!elements["files-layout"].classList.contains("side-collapsed"));
  setMatchMediaEnabled(true);

  // 12) 元素缺失：静默降级（页面结构变了也不该在控制台炸掉）
  resetDom();
  pane = createSplitPane({
    layoutId: "nope-layout",
    sideId: "nope-side",
    toggleId: "nope-toggle",
    splitterId: "nope-splitter",
    widthKey: WIDTH_KEY,
    collapsedKey: COLLAPSED_KEY,
  });
  pane.init();
  pane.toggle();
  pane.setWidth(400, true);
  assert.strictEqual(pane.isSplit(), true);
  assert.strictEqual(pane.currentWidth(), 400, "拿不到元素时以记忆值为准");
  assert.deepStrictEqual(pane.limits(), { min: 200, max: 640 }, "容器缺失退回常量上限");
  assert.strictEqual(storage.get(WIDTH_KEY), "400");

  console.log("ALL PANE SPLIT TESTS PASSED");
  process.exit(0);
})().catch((err) => { console.error("FAILED:", (err && err.stack) || err); process.exit(1); });
