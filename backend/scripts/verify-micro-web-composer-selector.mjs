// 行为验证（jsdom）：micro web client composer 的 provider / model 选择器支持搜索过滤。
//
// 运行：node backend/scripts/verify-micro-web-composer-selector.mjs
// 被测：backend/cmd/aicli/commands/web/{index.html,style.css,js/runtime.js}
//
// 覆盖：
//   1. 结构契约：provider 是只读 input + ▼ + popup（原生 select 无法内嵌检索框）；
//      model popup 顶部检索框由 runtime.js 渲染，样式规则齐备
//   2. provider 列表：子串过滤（不区分大小写）、「命中 / 总数」计数、零命中提示、
//      点选提交 /model --provider=… 并带出新 provider 默认模型
//   3. model 列表：检索过滤、检索框 Enter 选中首个可见项、点选提交 /model --model=…、
//      重开重置检索词、Esc 关闭
//   4. 打开弹层即聚焦检索框；popup 打开时在主输入框打字同样过滤列表
//
// 依赖：frontend/node_modules 里的 jsdom（仓库已装，无需额外安装）。
import { JSDOM } from "../../frontend/node_modules/jsdom/lib/api.js";
import { readFileSync } from "node:fs";
import { fileURLToPath, pathToFileURL } from "node:url";
import { dirname, resolve } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const webDir = resolve(here, "..", "cmd", "aicli", "commands", "web");
const html = readFileSync(resolve(webDir, "index.html"), "utf8");
const css = readFileSync(resolve(webDir, "style.css"), "utf8");
const runtimeSrc = readFileSync(resolve(webDir, "js", "runtime.js"), "utf8");

let failed = 0;
function check(name, ok, detail) {
  if (ok) { console.log("PASS " + name); return; }
  failed++;
  console.log("FAIL " + name + (detail === undefined ? "" : ": " + JSON.stringify(detail)));
}

// ---- 1. 静态结构契约 ----
check("index.html: provider 控件是只读 input（原生 select 无法内嵌检索框）",
  /<input id="cfg-provider"[^>]*readonly/.test(html));
check("index.html: provider 提供 ▼ 与 popup 容器",
  html.indexOf('id="cfg-provider-toggle"') >= 0 && html.indexOf('id="cfg-provider-popup"') >= 0);
check("style.css: 检索框 / 命中计数 / 零命中提示样式齐备",
  /\.cfg-popup-filter \{/.test(css) && /\.cfg-popup-count/.test(css) && /\.cfg-popup-no-match/.test(css));
check("runtime.js: provider / model 共用过滤与键盘处理",
  /function applyPopupFilter/.test(runtimeSrc) && /function handlePopupKeydown/.test(runtimeSrc));

// ---- jsdom 环境 + 运行时元数据 fetch 桩 ----
const dom = new JSDOM(html, { pretendToBeVisual: true, url: "http://localhost/" });
const { window } = dom;
globalThis.window = window;
globalThis.document = window.document;
Object.defineProperty(globalThis, "navigator", { value: window.navigator, configurable: true });
globalThis.HTMLElement = window.HTMLElement;
globalThis.Node = window.Node;
globalThis.Event = window.Event;
globalThis.KeyboardEvent = window.KeyboardEvent;
globalThis.localStorage = window.localStorage;
window.HTMLElement.prototype.scrollIntoView = function () {};

const providers = [
  {
    name: "alpha", default_model: "m-a", models: ["m-a", "m-b", "m-c"],
    model_details: [
      { name: "m-a", reasoning_efforts: ["low", "high"], default_reasoning_effort: "high" },
      { name: "m-b" }, { name: "m-c" },
    ],
  },
  {
    name: "beta", default_model: "gpt-4o", models: ["gpt-4o", "gpt-4o-mini", "o3"],
    model_details: [
      { name: "gpt-4o", reasoning_efforts: ["low", "medium", "high"], default_reasoning_effort: "medium" },
      { name: "gpt-4o-mini" }, { name: "o3", reasoning_efforts: ["low", "high"], default_reasoning_effort: "high" },
    ],
  },
  { name: "gamma", default_model: "claude-x", models: ["claude-x"], model_details: [{ name: "claude-x" }] },
];
const current = {
  provider: "alpha", model: "m-a", reasoning_effort: "",
  reasoning_options: ["low", "high"], reasoning_default: "high", reasoning_supported: true,
};
const inputPosts = [];

function applyModelCommand(prompt) {
  const provider = (/--provider=(\S+)/.exec(prompt) || [])[1] || "";
  const model = (/--model=(\S+)/.exec(prompt) || [])[1] || "";
  const reasoning = (/-r=(\S+)/.exec(prompt) || [])[1] || "";
  if (provider && provider !== current.provider) {
    current.provider = provider;
    const p = providers.find((item) => item.name === provider);
    current.model = model || (p ? p.default_model : current.model);
  } else if (model) {
    current.model = model;
  }
  if (/--clear-reasoning/.test(prompt)) { current.reasoning_effort = ""; }
  else if (reasoning) { current.reasoning_effort = reasoning; }
}

function okJSON(payload) {
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(payload) });
}
globalThis.fetch = (url, opts) => {
  const target = String(url);
  if (target.indexOf("/web/api/input") >= 0 && opts && opts.method === "POST") {
    const prompt = JSON.parse(opts.body).prompt;
    inputPosts.push(prompt);
    applyModelCommand(prompt);
    return okJSON({ status: "queued" });
  }
  if (target.indexOf("/web/api/runtime") >= 0) {
    return okJSON({ current: current, providers: providers });
  }
  return okJSON({});
};

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const $ = (id) => window.document.getElementById(id);
const popupVisible = (el) => !!el && el.style.display !== "none";
const visibleItems = (popup, attr) => Array.prototype.filter.call(
  popup.querySelectorAll("[" + attr + "]"),
  (el) => el.style.display !== "none",
);
function typeInto(input, value) {
  input.value = value;
  input.dispatchEvent(new window.Event("input", { bubbles: true }));
}
function press(input, key) {
  input.dispatchEvent(new window.KeyboardEvent("keydown", { key: key, bubbles: true, cancelable: true }));
}

// ---- 2. 初始化 ----
const runtime = await import(pathToFileURL(resolve(webDir, "js", "runtime.js")).href);
runtime.initRuntimeBar();
runtime.loadRuntimeMeta();
await sleep(30);

check("初始化：provider 只读输入框回填当前 provider",
  $("cfg-provider").value === "alpha" && $("cfg-provider").readOnly === true, $("cfg-provider").value);
check("初始化：model 输入框回填当前模型", $("cfg-model").value === "m-a", $("cfg-model").value);

// ---- 3. provider 列表检索 ----
$("cfg-provider-toggle").click();
const pPopup = $("cfg-provider-popup");
const pFilter = pPopup.querySelector("[data-popup-filter]");
check("点 ▼ 打开 provider 列表并渲染检索框", popupVisible(pPopup) && !!pFilter);
check("打开/关闭同步 aria-expanded", $("cfg-provider").getAttribute("aria-expanded") === "true");
check("provider 列表初始全量 + 计数", visibleItems(pPopup, "data-provider").length === 3 &&
  pPopup.querySelector("[data-popup-count]").textContent === "3 项",
  pPopup.querySelector("[data-popup-count]").textContent);
check("打开即聚焦检索框", window.document.activeElement === pFilter);

typeInto(pFilter, "BE");
const pVisible = visibleItems(pPopup, "data-provider");
check("provider 检索不区分大小写且只留命中项",
  pVisible.length === 1 && pVisible[0].getAttribute("data-provider") === "beta",
  pVisible.map((el) => el.getAttribute("data-provider")));
check("provider 计数显示「命中 / 总数」",
  pPopup.querySelector("[data-popup-count]").textContent === "1 / 3");

typeInto(pFilter, "zzz");
check("provider 零命中显示提示而不是空白",
  visibleItems(pPopup, "data-provider").length === 0 &&
  pPopup.querySelector(".cfg-popup-no-match").style.display === "block");

typeInto(pFilter, "");
check("provider 清空检索恢复全量", visibleItems(pPopup, "data-provider").length === 3);

pPopup.querySelector('[data-provider="beta"]').click();
await sleep(60);
check("点选 provider 提交 /model --provider=beta",
  inputPosts.some((prompt) => prompt.indexOf("--provider=beta") >= 0), inputPosts);
check("点选后关闭列表并回填 provider",
  !popupVisible(pPopup) && $("cfg-provider").value === "beta", $("cfg-provider").value);
check("点选后 aria-expanded 复位", $("cfg-provider").getAttribute("aria-expanded") === "false");
check("provider 切换后 model 跟随新 provider 默认模型",
  $("cfg-model").value === "gpt-4o", $("cfg-model").value);

// ---- 4. model 列表检索 ----
$("cfg-model-toggle").click();
const mPopup = $("cfg-model-popup");
const mFilter = mPopup.querySelector("[data-popup-filter]");
check("点 ▼ 打开 model 列表并聚焦检索框", popupVisible(mPopup) && window.document.activeElement === mFilter);
check("model 列表初始全量 + 计数", visibleItems(mPopup, "data-model").length === 3 &&
  mPopup.querySelector("[data-popup-count]").textContent === "3 项",
  mPopup.querySelector("[data-popup-count]").textContent);

typeInto(mFilter, "min");
const mVisible = visibleItems(mPopup, "data-model");
check("model 检索按子串过滤",
  mVisible.length === 1 && mVisible[0].getAttribute("data-model") === "gpt-4o-mini",
  mVisible.map((el) => el.getAttribute("data-model")));

press(mFilter, "Enter");
await sleep(60);
check("检索框 Enter 选中首个可见模型",
  inputPosts.some((prompt) => prompt.indexOf("--model=gpt-4o-mini") >= 0), inputPosts);
check("提交后关闭列表并回填模型",
  !popupVisible(mPopup) && $("cfg-model").value === "gpt-4o-mini", $("cfg-model").value);

$("cfg-model-toggle").click();
check("重开 model 列表检索词归零（全量显示）",
  mPopup.querySelector("[data-popup-filter]").value === "" && visibleItems(mPopup, "data-model").length === 3);
press(mPopup.querySelector("[data-popup-filter]"), "Escape");
check("Esc 关闭 model 列表并把焦点还给输入框",
  !popupVisible(mPopup) && window.document.activeElement === $("cfg-model"));

// ---- 5. popup 打开时主输入框充当检索词 ----
$("cfg-model-toggle").click();
typeInto($("cfg-model"), "o3");
check("popup 打开时在主输入框打字同样过滤列表",
  mPopup.querySelector("[data-popup-filter]").value === "o3" &&
  visibleItems(mPopup, "data-model").length === 1,
  mPopup.querySelector("[data-popup-filter]").value);
press(mPopup.querySelector("[data-popup-filter]"), "Escape");
check("Esc 关闭后 provider 弹层也随全局 Esc 收敛",
  !popupVisible(mPopup) && !popupVisible(pPopup));

console.log("");
console.log(failed === 0 ? "ALL PASS" : failed + " FAILED");
process.exitCode = failed === 0 ? 0 : 1;
