// 模型编辑器 DOM 回归测试（jsdom）：用真实 DOM 驱动 provider-editor.js 的
// 模型编辑器，验证「点击模型 → 面板回显 → 编辑 → 保存载荷」全链路。
// 被测：backend/cmd/aicli/commands/web/js/provider-editor.js + web/index.html
//       后端契约：POST /web/api/config/providers 的 model_capabilities 字段
//
// 运行：node backend/scripts/verify-micro-web-model-editor.mjs
// 依赖：frontend/node_modules 里的 jsdom（仓库已装，无需额外安装）。
import { JSDOM } from "../../frontend/node_modules/jsdom/lib/api.js";
import { readFileSync } from "node:fs";
import { fileURLToPath, pathToFileURL } from "node:url";
import { dirname, resolve } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const webDir = resolve(here, "..", "cmd", "aicli", "commands", "web");

let failed = 0;
function check(name, ok, detail) {
  if (ok) { console.log("PASS " + name); return; }
  failed++;
  console.log("FAIL " + name + ": " + JSON.stringify(detail));
}

// ---- 用真实 index.html 片段搭出 provider 弹窗 DOM ----
const html = readFileSync(resolve(webDir, "index.html"), "utf8");
const start = html.indexOf('<form id="config-provider-form"');
const end = html.indexOf("</form>", start);
if (start < 0 || end < 0) { throw new Error("provider form not found in index.html"); }
const formHTML = html.slice(start, end + "</form>".length);

const dom = new JSDOM(`<!doctype html><html><body>
  <div id="config-editor-overlay"><div id="config-editor-modal">
    <div id="config-editor-modal-header"></div>
    <div id="config-editor-title"></div>
    <input id="cfg-provider-original-name">
    ${formHTML}
    <div id="config-editor-resize"></div>
  </div></div>
  <datalist id="cfg-provider-protocol-options"></datalist>
</body></html>`, { pretendToBeVisual: true });

const { window } = dom;
globalThis.window = window;
globalThis.document = window.document;
// Node 24 的 globalThis.navigator 只有 getter，用 defineProperty 覆盖。
Object.defineProperty(globalThis, "navigator", { value: window.navigator, configurable: true });
globalThis.HTMLElement = window.HTMLElement;
globalThis.Node = window.Node;
globalThis.Event = window.Event;
window.HTMLElement.prototype.scrollIntoView = function () {};

// ---- fetch 桩：返回固定配置快照 / 保存响应 ----
const snapshot = {
  config_path: "/tmp/config.yaml",
  default_provider: "alpha",
  chat: { default_provider: "alpha", default_model: "m-a", reasoning_effort: "" },
  providers: [
    {
      name: "alpha", protocol: "openai", enabled: true,
      base_url: "https://api.example.com", api_path: "/v1/chat/completions",
      forward_url: "", default_model: "m-a",
      supported_models: ["m-a", "m-b", "m-c"],
      models: [
        {
          name: "m-a", reasoning_model: true, reasoning_efforts: ["low", "high"],
          reasoning_effort_budgets: { high: 16000 }, default_reasoning_effort: "high",
          compact_reasoning_effort: "low", max_context_tokens: 200000, max_tokens: 32000,
          auto_compact_ratio: 0.85, auto_compact_token_limit: 150000, auto_compact_mode: "aggressive",
          supports_remote_compact: true, replay_reasoning_content: true,
          input_modalities: ["text", "image"],
          native_tools: { image_generation: true, images_generations_api: false },
        },
        { name: "m-b" },
        {
          name: "m-c", reasoning_model: true, reasoning_efforts: ["minimal"],
          replay_reasoning_content: false, input_modalities: ["text"],
        },
      ],
    },
  ],
};

let lastSaveBody = null;
globalThis.fetch = (url, opts) => {
  if (String(url).endsWith("/web/api/config") && !(opts && opts.method === "POST")) {
    return Promise.resolve({ json: () => Promise.resolve(snapshot) });
  }
  if (String(url).endsWith("/web/api/config/providers") && opts && opts.method === "POST") {
    lastSaveBody = JSON.parse(opts.body);
    return Promise.resolve({ json: () => Promise.resolve({ status: "ok", provider: lastSaveBody.name }) });
  }
  if (String(url).endsWith("/web/api/runtime")) {
    return Promise.resolve({ json: () => Promise.resolve({ provider: "alpha", model: "m-a" }) });
  }
  return Promise.resolve({ json: () => Promise.resolve({ status: "error", reason: "unexpected " + url }) });
};

const mod = await import(pathToFileURL(resolve(webDir, "js", "provider-editor.js")).href);
const cfgAdmin = await import(pathToFileURL(resolve(webDir, "js", "config-admin.js")).href);

const $ = (id) => document.getElementById(id);
// loadConfigAdmin() 不返回 promise（fire-and-forget），手动等微任务/宏任务跑完。
const flush = (ms = 20) => new Promise((r) => setTimeout(r, ms));
mod.initProviderEditor();
await cfgAdmin.loadConfigAdmin();
await flush();
mod.openProviderEditor("alpha");

// ---- 1. 模型列表渲染 ----
const listEl = $("cfg-model-list");
const items = listEl.querySelectorAll("[data-model]");
check("model list renders all models", items.length === 3, items.length);
check("model count shown", /\b3\b/.test($("cfg-model-count").textContent), $("cfg-model-count").textContent);

// 未配置的 m-b 显示“未配置”，已配置的 m-a 有摘要 chip。
const mA = listEl.querySelector('[data-model="m-a"]');
const mB = listEl.querySelector('[data-model="m-b"]');
check("configured model marked", mA.classList.contains("configured"), mA.className);
check("unconfigured model shows empty chip",
  /未配置/.test(mB.textContent), mB.textContent);
check("summary chips include context size",
  /ctx\s*200K/.test(mA.textContent) && /out\s*32K/.test(mA.textContent), mA.textContent);
check("summary chip shows replay contract",
  /回传 reasoning/.test(mA.textContent), mA.textContent);

// 旧的“每模型一行内联表单”容器应已不存在。
check("legacy inline editor removed", !$("cfg-model-reasoning-editors"), "still present");

// ---- 2. 点击模型打开面板并回显已保存配置 ----
mA.click();
const panel = $("cfg-model-editor");
check("panel opened on click", /m-a/.test(panel.textContent), panel.textContent.slice(0, 80));
check("panel marks default model", /默认模型/.test(panel.textContent), "no default chip");

function field(model, name) {
  // 重新选中模型后取面板字段当前值
  const item = $("cfg-model-list").querySelector(`[data-model="${model}"]`);
  if (!item.classList.contains("active")) { item.click(); }
  return $("cfg-model-editor").querySelector(`[data-field="${name}"]`);
}
check("context tokens echoed", field("m-a", "max_context_tokens").value === "200000", field("m-a", "max_context_tokens").value);
check("max output echoed", field("m-a", "max_tokens").value === "32000", field("m-a", "max_tokens").value);
check("auto compact ratio echoed", field("m-a", "auto_compact_ratio").value === "0.85", field("m-a", "auto_compact_ratio").value);
check("auto compact limit echoed", field("m-a", "auto_compact_token_limit").value === "150000", field("m-a", "auto_compact_token_limit").value);
check("auto compact mode echoed", field("m-a", "auto_compact_mode").value === "aggressive", field("m-a", "auto_compact_mode").value);
check("reasoning efforts echoed", field("m-a", "reasoning_efforts").value === "low, high", field("m-a", "reasoning_efforts").value);
check("effort budgets echoed", field("m-a", "reasoning_effort_budgets").value === "high: 16000", field("m-a", "reasoning_effort_budgets").value);
check("input modalities echoed", field("m-a", "input_modalities").value === "text, image", field("m-a", "input_modalities").value);
check("replay tri-state true selected", field("m-a", "replay_reasoning_content").value === "true", field("m-a", "replay_reasoning_content").value);
check("native tool image_generation checked", field("m-a", "image_generation").checked === true, "unchecked");
check("native tool images_api unchecked", field("m-a", "images_generations_api").checked === false, "checked");
check("remote compact checked", field("m-a", "supports_remote_compact").checked === true, "unchecked");

// m-c 的 replay_reasoning_content=false 必须与“未声明”区分开。
check("replay tri-state false selected", field("m-c", "replay_reasoning_content").value === "false", field("m-c", "replay_reasoning_content").value);
check("replay tri-state unset for m-b", field("m-b", "replay_reasoning_content").value === "", field("m-b", "replay_reasoning_content").value);

// ---- 3. 编辑上下文大小并保存 ----
const ctx = field("m-a", "max_context_tokens");
ctx.value = "400000";
const out = field("m-a", "max_tokens");
out.value = "64000";
const replay = field("m-a", "replay_reasoning_content");
replay.value = "";
replay.dispatchEvent(new window.Event("change", { bubbles: true }));
$("config-provider-form").dispatchEvent(new window.Event("submit", { cancelable: true, bubbles: true }));
await flush(30);

check("save issued", lastSaveBody !== null, "no POST");
if (lastSaveBody) {
  const caps = lastSaveBody.model_capabilities;
  check("model_capabilities payload used", !!caps && !lastSaveBody.reasoning, Object.keys(lastSaveBody));
  check("edited context tokens submitted", caps["m-a"].max_context_tokens === 400000, caps["m-a"].max_context_tokens);
  check("edited max tokens submitted", caps["m-a"].max_tokens === 64000, caps["m-a"].max_tokens);
  check("preserved reasoning fields submitted",
    caps["m-a"].reasoning_model === true &&
    JSON.stringify(caps["m-a"].reasoning_efforts) === JSON.stringify(["low", "high"]) &&
    caps["m-a"].default_reasoning_effort === "high", caps["m-a"]);
  check("preserved budgets submitted",
    caps["m-a"].reasoning_effort_budgets.high === 16000, caps["m-a"].reasoning_effort_budgets);
  check("preserved auto compact submitted",
    caps["m-a"].auto_compact_ratio === 0.85 && caps["m-a"].auto_compact_token_limit === 150000 &&
    caps["m-a"].auto_compact_mode === "aggressive" && caps["m-a"].supports_remote_compact === true, caps["m-a"]);
  check("preserved native tools submitted",
    caps["m-a"].native_tools.image_generation === true && caps["m-a"].native_tools.images_generations_api === false, caps["m-a"].native_tools);
  check("preserved input modalities submitted",
    JSON.stringify(caps["m-a"].input_modalities) === JSON.stringify(["text", "image"]), caps["m-a"].input_modalities);
  check("cleared replay contract submitted as null",
    caps["m-a"].replay_reasoning_content === null, caps["m-a"].replay_reasoning_content);
  check("replay false preserved for m-c",
    caps["m-c"].replay_reasoning_content === false, caps["m-c"].replay_reasoning_content);
  check("unconfigured model cleared on submit",
    caps["m-b"].max_context_tokens === 0 && caps["m-b"].reasoning_efforts.length === 0, caps["m-b"]);
  check("supported models submitted",
    JSON.stringify(lastSaveBody.supported_models) === JSON.stringify(["m-a", "m-b", "m-c"]), lastSaveBody.supported_models);
}

// ---- 4. 过滤 + 从列表移除 + 设为默认模型 ----
mod.openProviderEditor("alpha");
const filter = $("cfg-model-filter");
filter.value = "m-b";
filter.dispatchEvent(new window.Event("input", { bubbles: true }));
check("filter narrows list",
  $("cfg-model-list").querySelectorAll("[data-model]").length === 1, "count");
check("filter count text", /1 \/ 3/.test($("cfg-model-count").textContent), $("cfg-model-count").textContent);
filter.value = "";
filter.dispatchEvent(new window.Event("input", { bubbles: true }));

$("cfg-model-list").querySelector('[data-model="m-b"]').click();
$("cfg-model-editor").querySelector('[data-action="set-default-model"]').click();
check("set default model action", $("cfg-provider-default-model").value === "m-b", $("cfg-provider-default-model").value);

$("cfg-model-editor").querySelector('[data-action="remove-model"]').click();
check("remove model from list",
  !$("cfg-model-list").querySelector('[data-model="m-b"]') &&
  $("cfg-provider-models").value.indexOf("m-b") < 0, $("cfg-provider-models").value);
check("panel closed after remove",
  /点击左侧任一模型/.test($("cfg-model-editor").textContent), $("cfg-model-editor").textContent.slice(0, 60));

// ---- 5. 手工新增模型后可在编辑器中打开 ----
const ta = $("cfg-provider-models");
ta.value = ta.value + "\nm-new";
ta.dispatchEvent(new window.Event("input", { bubbles: true }));
check("new model appears in list",
  !!$("cfg-model-list").querySelector('[data-model="m-new"]'), "missing");
$("cfg-model-list").querySelector('[data-model="m-new"]').click();
check("new model panel shows empty config",
  field("m-new", "max_context_tokens").value === "" && field("m-new", "replay_reasoning_content").value === "",
  field("m-new", "max_context_tokens").value);
// 编辑新模型后保存应带上它。
field("m-new", "max_context_tokens").value = "8000";
$("config-provider-form").dispatchEvent(new window.Event("submit", { cancelable: true, bubbles: true }));
await flush(30);
check("new model capabilities submitted",
  lastSaveBody && lastSaveBody.model_capabilities["m-new"].max_context_tokens === 8000,
  lastSaveBody && lastSaveBody.model_capabilities["m-new"]);

// ---- 6. 输入模态 chip 多选（取代逗号分隔自由文本框）----
mod.openProviderEditor("alpha");
$("cfg-model-list").querySelector('[data-model="m-a"]').click();

function modChip(code) {
  return $("cfg-model-editor").querySelector(`[data-modality="${code}"]`);
}
function modNote() {
  return $("cfg-model-editor").querySelector("[data-modality-note]").textContent;
}
function clickMod(code) {
  const box = modChip(code);
  box.checked = !box.checked;
  box.dispatchEvent(new window.Event("change", { bubbles: true }));
}

// 词表 5 项全部渲染，已保存值（text, image）对应 chip 处于勾选态。
check("modality chips render full vocabulary",
  ["text", "image", "audio", "video", "file"].every((c) => !!modChip(c)),
  Array.from($("cfg-model-editor").querySelectorAll("[data-modality]"))
    .map((b) => b.getAttribute("data-modality")).join(","));
check("saved modalities checked as chips",
  modChip("text").checked === true && modChip("image").checked === true &&
  modChip("audio").checked === false, `text=${modChip("text").checked} image=${modChip("image").checked}`);

// 点 chip 必须同步隐藏输入（草稿收集器读它），提交载荷随之变化。
clickMod("audio");
check("chip click syncs hidden field",
  field("m-a", "input_modalities").value === "text, image, audio", field("m-a", "input_modalities").value);
check("inert modality flagged in note",
  /audio/.test(modNote()) && /不生效/.test(modNote()), modNote());

// 跨字段约束：image_generation 开着但缺 image 时必须给出警告（images.go 要求
// text 与 image 同时存在才生效）。
clickMod("image");
check("image_generation without image warns",
  /需要同时勾选/.test(modNote()), modNote());
clickMod("image");
check("warning clears when constraint satisfied",
  !/需要同时勾选/.test(modNote()), modNote());

// 全部取消 = 回到「未声明」，而不是写成空字符串。
// 注意优先级：image_generation 仍开着时，警告比「未声明」更可操作，优先显示。
clickMod("text"); clickMod("image"); clickMod("audio");
check("all modalities cleared to undeclared",
  field("m-a", "input_modalities").value === "", JSON.stringify(field("m-a", "input_modalities").value));
check("warning outranks undeclared note while image_generation on",
  /需要同时勾选/.test(modNote()), modNote());

// 关掉 image_generation 后，「未声明」提示才露出来。
const genBox = $("cfg-model-editor").querySelector('[data-field="image_generation"]');
genBox.checked = false;
genBox.dispatchEvent(new window.Event("change", { bubbles: true }));
check("undeclared modalities note", /未声明/.test(modNote()), modNote());
genBox.checked = true;
genBox.dispatchEvent(new window.Event("change", { bubbles: true }));

// 词表外的值（手写配置 / 将来新增的模态）必须原样保留，不能被 chip 组吃掉。
clickMod("text");
const unknownBox = document.createElement("input");
unknownBox.type = "checkbox";
unknownBox.setAttribute("data-modality", "pdf");
unknownBox.checked = true;
$("cfg-model-editor").querySelector(".cfg-modalities").appendChild(unknownBox);
unknownBox.dispatchEvent(new window.Event("change", { bubbles: true }));
check("out-of-vocabulary value preserved",
  field("m-a", "input_modalities").value === "text, pdf", field("m-a", "input_modalities").value);

$("config-provider-form").dispatchEvent(new window.Event("submit", { cancelable: true, bubbles: true }));
await flush(30);
check("chip edits submitted",
  lastSaveBody && JSON.stringify(lastSaveBody.model_capabilities["m-a"].input_modalities) ===
    JSON.stringify(["text", "pdf"]), lastSaveBody && lastSaveBody.model_capabilities["m-a"].input_modalities);

if (failed) { console.log("\n" + failed + " FAILED"); process.exit(1); }
console.log("\nALL PASS");
