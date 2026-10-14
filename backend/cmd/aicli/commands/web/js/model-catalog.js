// 模型目录共享模块:provider→model→reasoning 三级联动的纯逻辑层。
// aicli micro web client 前端模块(无构建步骤,由 app.js 入口聚合)。
//
// 数据源(两种形态,本模块统一归一化):
//   A) GET /api/runtime/models(connectors.js 路由表单使用)
//      { default_provider, default_reasoning_effort, providers: [{
//          name, models: [name...], default_model,
//          model_capabilities: { [modelName]: { reasoning_efforts, default_reasoning_effort, reasoning_model } } }] }
//   B) GET /web/api/runtime(runtime.js composer 选择器使用)
//      { providers: [{ name, models: [name...], default_model, model_details: [{
//          name, reasoning_efforts: [...], default_reasoning_effort, reasoning_model }] }],
//        current: { provider, model, reasoning_effort, reasoning_options } }
//      → B 的 provider 根下没有顶层 default_model 语义差异,归一化时把
//      model_details[i].name 折进 capabilities map,与 A 共用同一套查询函数。
//
// 联动语义(与 React 端 model-panel-model.ts 的 advanceAfter*Select 对齐):
//   1. model 候选 = modelsFor(catalog, 当前 provider);provider 为空 → 目录
//      default_provider;provider 不在目录 → 空候选(仍可手输,不阻塞)。
//   2. provider 变更后 model 值跟随:旧 model 不在新 provider 的候选里 →
//      新 provider 的 default_model(缺省取候选第一个)。
//   3. reasoning 候选 = 当前 provider+model 的 capabilities.reasoning_efforts,
//      缺失回退通用档位;当前值不在候选中时清空(提交前由调用方兜底)。

// findProvider:按名称查 provider 节点;catalog 为空/无 providers 时返回 null。
export function findProvider(catalog, name) {
  var providers = (catalog && catalog.providers) || [];
  for (var i = 0; i < providers.length; i++) {
    var p = providers[i];
    if (p && (p.name || "") === name) { return p; }
  }
  return null;
}

// providerNames:目录内的 provider 名称列表(去掉空名,保序)。
export function providerNames(catalog) {
  var providers = (catalog && catalog.providers) || [];
  var out = [];
  for (var i = 0; i < providers.length; i++) {
    var name = (providers[i] && providers[i].name) || "";
    if (name) { out.push(name); }
  }
  return out;
}

// defaultProviderName:目录顶层 default_provider(A 形态);B 形态(/web/api/runtime)
// 无该字段,回退 current.provider(当前生效 provider 最接近"默认"语义);
// 再回退第一个 provider。
export function defaultProviderName(catalog) {
  var def = (catalog && catalog.default_provider) || "";
  if (def) { return def; }
  var cur = (catalog && catalog.current && catalog.current.provider) || "";
  if (cur) { return cur; }
  var names = providerNames(catalog);
  return names.length ? names[0] : "";
}

// modelCapabilities:从 model_capabilities map(A 形态)取某模型的 capability
// 节点;精确匹配缺失时回退 "*" 通配条目(aicli 目录用 * 表示该 provider
// 任意模型的默认能力面,见 CODEX 等目录);两者都没有返回 null。
function capabilityFromMap(provider, modelName) {
  var caps = provider && provider.model_capabilities;
  if (!caps || typeof caps !== "object") { return null; }
  if (modelName && Object.prototype.hasOwnProperty.call(caps, modelName)) { return caps[modelName]; }
  if (Object.prototype.hasOwnProperty.call(caps, "*")) { return caps["*"]; }
  return null;
}

// modelDetailFromList:从 model_details 数组(B 形态)取某模型的 capability
// 节点;没有该模型条目时返回 null。
function modelDetailFromList(provider, modelName) {
  var details = provider && provider.model_details;
  if (!details || !details.length) { return null; }
  for (var i = 0; i < details.length; i++) {
    var d = details[i];
    if (d && (d.name || "") === modelName) { return d; }
  }
  return null;
}

// modelCapability:统一取某 provider 下某模型的 capability 节点
// (A 形态查 model_capabilities map,B 形态查 model_details 数组)。
export function modelCapability(catalog, providerName, modelName) {
  var p = findProvider(catalog, providerName);
  if (!p) { return null; }
  return capabilityFromMap(p, modelName) || modelDetailFromList(p, modelName);
}

// modelsFor:某 provider 的模型候选。provider 名为空 → 目录默认 provider;
// provider 不在目录 → 返回空数组(调用方保持手输通道)。
export function modelsFor(catalog, providerName) {
  var name = providerName || defaultProviderName(catalog);
  var p = findProvider(catalog, name);
  return p ? ((p.models || []).slice()) : [];
}

// providerDefaultModel:provider 节点的 default_model,缺失回退候选第一个;
// provider 不存在返回空串。
export function providerDefaultModel(catalog, providerName) {
  var name = providerName || defaultProviderName(catalog);
  var p = findProvider(catalog, name);
  if (!p) { return ""; }
  return p.default_model || (p.models && p.models[0]) || "";
}

// GENERIC_REASONING:目录未声明档位时的通用兜底列表。
var GENERIC_REASONING = ["minimal", "low", "medium", "high", "max", "xhigh"];

// reasoningFor:某 provider+model 的 reasoning 候选与默认档位。
// 返回 { options, def }:
//   - options:候选档位列表;目录未声明(reasoning_efforts 空)→ 通用列表;
//   - def:模型默认档位;未声明 → 目录顶层 default_reasoning_effort(A 形态)/
//     空(B 形态无此信息,与旧行为一致由调用方处理)。
export function reasoningFor(catalog, providerName, modelName) {
  var p = findProvider(catalog, providerName);
  var def = (catalog && catalog.default_reasoning_effort) || "";
  var cap = p ? (capabilityFromMap(p, modelName) || modelDetailFromList(p, modelName)) : null;
  if (cap && cap.reasoning_efforts && cap.reasoning_efforts.length > 0) {
    return { options: cap.reasoning_efforts.slice(), def: cap.default_reasoning_effort || def };
  }
  return { options: GENERIC_REASONING.slice(), def: def };
}
