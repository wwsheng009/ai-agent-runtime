// 调试页签：展示与 aicli /debug 命令一致的状态文档快照。
// 数据源 GET /web/api/status?format=text（后端 BuildChatDebugDisplayText()，
// 与 TUI 侧 /debug 覆盖层共用同一份文档内容）；同路径的 ?format=json 为
// 结构化快照，页签内以「JSON 快照」链接另开标签页查看。
// 刷新策略：进入页签时拉取一次 + 工具栏「⟳ 刷新」手动重拉；快照反映的是
// 拉取时刻的后端状态，不做 SSE 增量刷新。
// aicli micro web client 前端模块（无构建步骤，由 app.js 入口聚合）。

var debugInFlight = false;
var debugSeq = 0;

function debugEl(id) { return document.getElementById(id); }

function setDebugMeta(text) {
  var meta = debugEl("debug-meta");
  if (meta) { meta.textContent = text; }
}

function setDebugOutput(text) {
  var out = debugEl("debug-output");
  if (out) { out.textContent = text; }
}

function capturedAt() {
  try {
    return new Date().toLocaleTimeString("zh-CN", { hour12: false });
  } catch (e) {
    return "";
  }
}

// 拉取并渲染 /debug 状态文档。并发调用只保留最后一次的响应（seq 守卫），
// 避免会话切换等场景下迟到响应覆盖新内容。
export function loadDebugInfo() {
  if (debugInFlight) { return; }
  debugInFlight = true;
  var seq = ++debugSeq;
  setDebugMeta("加载中…");
  fetch("/web/api/status?format=text", { cache: "no-store" })
    .then(function (res) {
      if (!res.ok) { throw new Error("HTTP " + res.status); }
      return res.text();
    })
    .then(function (text) {
      if (seq !== debugSeq) { return; }
      // 仅去掉文档末尾的换行（保留行内空白），再补一个换行便于阅读。
      var body = String(text == null ? "" : text).replace(/[\r\n]+$/, "");
      setDebugOutput(body + "\n");
      setDebugMeta("采集于 " + capturedAt());
    })
    .catch(function (err) {
      if (seq !== debugSeq) { return; }
      setDebugOutput("加载失败：" + (err && err.message ? err.message : String(err)));
      setDebugMeta("加载失败");
    })
    .then(function () {
      debugInFlight = false;
    });
}

export function refreshDebugInfo() { loadDebugInfo(); }
