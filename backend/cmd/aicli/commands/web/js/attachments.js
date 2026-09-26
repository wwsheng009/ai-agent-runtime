// 图片附件输入面:三入口(📎 按钮 / 粘贴 / 面板内拖放) + 上传 + 附件轨渲染 + 待发路径。
// aicli micro web client 前端模块(无构建步骤,由 app.js 入口聚合)。
//
// 与后端的分工(docs/plan/web-image-attachment-cross-surface-plan.md §3.2):
//   - 上传:POST /web/api/attachments(multipart/form-data,字段名 file,≤8 份)。服务端只落盘
//     并回 {name,path,bytes,width,height,note,skipped},**不改会话附件列表**——登记发生在
//     发送瞬间(/web/api/input 的 image_paths),所以「上传了但没发出去」的图片不会粘到下一轮。
//   - 本模块只维护「待发轨道」:上传成功的条目带 path,发送时由 composerImagePaths() 取走;
//     sendInput 收到 queued 后调用 clearComposerAttachments() 清空(见 sessions.js)。
//   - 视觉如实:轨道只显示服务端回执里的元信息(文件名 / 尺寸 / 字节数),**不放缩略图**——
//     浏览器读不到本地绝对路径,<img src="<本地路径>"> 只会得到坏图,那是伪造成功。
//     skipped 项与网络/HTTP 错误都写进 #send-status 并在轨上留 attach-failed 行(无 path,
//     不参与发送),绝不落进「已就绪」的样子。
//
// 三入口共用一条上传路径(fileListToArray / collectFiles → uploadFiles),不各自实现一份:
//   ①「📎」按钮 + 隐藏 <input type="file" accept="image/*" multiple>(#attach-btn / #attach-file)
//   ② #prompt 粘贴剪贴板图片(clipboardData)
//   ③ composer 面板内拖放(dragover 阻止默认 → 浏览器不会直接打开图片文件;drop 取 files)

import { promptEl, sendStatusEl } from "./chat.js";
import { apiFetch, esc, showToast } from "./util.js";

var MAX_FILES = 8;                    // 与后端 maxChatWebUploadFiles 同值:超出一律拒收,不做半截上传
var UPLOAD_PATH = "/web/api/attachments";
var UPLOAD_TIMEOUT_MS = 60000;        // 图片可能较大,比默认 12s 放宽(仍兜住连接池占满的无限等待)

// 待发条目:{name, path, bytes, width, height, note, ok}。ok=false 只作失败提示展示,
// 无 path、不参与发送;清空 / 移除只作用于本数组,不碰服务端任何状态。
var entries = [];
var trackEl = null;      // #attachment-track(输入行上方)
var fileInputEl = null;  // #attach-file
var panelEl = null;      // #composer-panel(拖放宿主)
var uploading = false;   // 上传单飞:一批未结束时不叠加第二批(避免轨道顺序与响应顺序错位)

export function composerImagePaths() {
  var out = [];
  for (var i = 0; i < entries.length; i++) {
    if (entries[i].ok && entries[i].path) { out.push(entries[i].path); }
  }
  return out;
}

// 发送成功(/web/api/input 回 queued)后清空轨道:附件已登记进会话,不再属于「待发」。
// 只由 sessions.js 的 sendInput 调用,避免出现第二处清空逻辑。
export function clearComposerAttachments() {
  if (!entries.length) { return; }
  entries = [];
  render();
}

function setStatus(text) {
  if (sendStatusEl) { sendStatusEl.textContent = text; }
}

function formatBytes(n) {
  if (!(n > 0)) { return ""; }
  if (n < 1024) { return n + " B"; }
  if (n < 1024 * 1024) { return (n / 1024).toFixed(1) + " KB"; }
  return (n / (1024 * 1024)).toFixed(1) + " MB";
}

function metaText(entry) {
  var parts = [];
  if (entry.width && entry.height) { parts.push(entry.width + "×" + entry.height); }
  var bytes = formatBytes(entry.bytes);
  if (bytes) { parts.push(bytes); }
  if (entry.note) { parts.push(entry.note); } // 服务端的压缩/跳过说明如实带上
  return parts.length ? parts.join(" · ") : "已就绪";
}

// 轨道渲染:无条目时整轨隐藏(hidden + CSS 的 [hidden] 规则),有条目时逐条给出
// 文件名 / 元信息 / 移除按钮。HTML 一律经 esc() 转义(文件名与 note 都来自服务端回执)。
function render() {
  if (!trackEl) { return; }
  if (!entries.length) {
    trackEl.innerHTML = "";
    trackEl.hidden = true;
    return;
  }
  var html = "";
  for (var i = 0; i < entries.length; i++) {
    var entry = entries[i];
    html += '<span class="attach-item' + (entry.ok ? "" : " attach-failed") + '">' +
      '<span class="attach-name" title="' + esc(entry.name) + '">' + esc(entry.name) + "</span>" +
      '<span class="attach-meta">' + esc(entry.ok ? metaText(entry) : (entry.note || "服务端未接受该文件")) + "</span>" +
      '<button class="attach-remove" type="button" data-attach-remove="' + i + '"' +
      ' title="移除该附件" aria-label="移除 ' + esc(entry.name) + '">×</button>' +
      "</span>";
  }
  trackEl.innerHTML = html;
  trackEl.hidden = false;
}

// 移除按钮的点击目标:从事件目标往上找 data-attach-remove(不依赖 closest,沙盒 DOM 也可用)。
function removeIndexFrom(target) {
  var el = target;
  while (el && typeof el.getAttribute === "function") {
    var raw = el.getAttribute("data-attach-remove");
    if (raw !== null && raw !== undefined) { return Number(raw); }
    el = el.parentNode;
  }
  return -1;
}

function removeAt(idx) {
  if (!(idx >= 0) || idx >= entries.length) { return; }
  var dropped = entries[idx];
  entries.splice(idx, 1);
  render();
  setStatus("已移除附件 " + (dropped && dropped.name ? dropped.name : ""));
}

function onTrackClick(ev) {
  var idx = removeIndexFrom(ev && ev.target);
  if (idx >= 0) { removeAt(idx); }
}

// FileList / 数组 → 普通数组(选择器的 FileList 与拖放的 FileList 形状不一致,先归一)。
function fileListToArray(list) {
  var out = [];
  if (!list) { return out; }
  for (var i = 0; i < list.length; i++) { out.push(list[i]); }
  return out;
}

// 取候选文件:优先 .files(文件选择 / 拖放 / 复制的文件);没有时回退 .items 里的
// image/* 条目——部分浏览器粘贴截图只出现在 items 里(text 条目按纯文本粘贴处理)。
function collectFiles(dataTransfer) {
  if (!dataTransfer) { return []; }
  var files = fileListToArray(dataTransfer.files);
  if (files.length) { return files; }
  var out = [];
  var items = dataTransfer.items;
  if (items && items.length) {
    for (var i = 0; i < items.length; i++) {
      var item = items[i];
      if (!item || item.kind !== "file") { continue; }
      if (!/^image\//i.test(String(item.type || ""))) { continue; }
      var file = typeof item.getAsFile === "function" ? item.getAsFile() : null;
      if (file) { out.push(file); }
    }
  }
  return out;
}

function fileNameOf(file, idx) {
  return file && file.name ? String(file.name) : "图片-" + (idx + 1);
}

// 上传一批文件:统一走 multipart 的 file 字段(多份)。成功只把服务端回执里的 path 记入
// 轨道;skipped / 无 path 的项留在轨上如实说明原因,不产生可用条目。
function uploadFiles(list) {
  var files = fileListToArray(list);
  if (!files.length) { return; }
  if (uploading) {
    setStatus("上一批图片仍在上传,请稍候…");
    return;
  }
  if (files.length > MAX_FILES) {
    reportFailure("一次最多上传 " + MAX_FILES + " 张图片(当前 " + files.length + " 张)");
    return;
  }
  var form = new FormData();
  for (var j = 0; j < files.length; j++) {
    form.append("file", files[j], fileNameOf(files[j], j));
  }
  uploading = true;
  setStatus("正在上传 " + files.length + " 张图片…");
  apiFetch(UPLOAD_PATH, { method: "POST", body: form }, UPLOAD_TIMEOUT_MS)
    .then(function (res) {
      return res.json()
        .catch(function () { return null; })
        .then(function (json) { return { status: res.status, json: json }; });
    })
    .then(function (res) {
      uploading = false;
      if (!res.json || res.json.status !== "ok") {
        var reason = res.json && res.json.reason ? res.json.reason : ("HTTP " + res.status);
        reportFailure("图片上传失败: " + reason);
        return;
      }
      applyUploadResult(res.json);
    })
    .catch(function (err) {
      uploading = false;
      reportFailure("图片上传失败: " + err);
    });
}

// 网络 / HTTP 层失败:只在 #send-status 与 toast 上如实说明,轨上不留「已就绪」的样子。
function reportFailure(message) {
  setStatus(message);
  showToast(message, "error", 6000);
}

// applyUploadResult:回执逐项落地。上一批的失败提示在换批时清掉(只是提示,不是待发项),
// 已成功条目保留——用户可能连续挑几批图片再一起发送。
function applyUploadResult(json) {
  var list = json && json.attachments ? json.attachments : [];
  var kept = [];
  for (var k = 0; k < entries.length; k++) {
    if (entries[k].ok) { kept.push(entries[k]); }
  }
  entries = kept;
  var added = 0;
  var problems = [];
  for (var i = 0; i < list.length; i++) {
    var att = list[i] || {};
    var name = att.name ? String(att.name) : "图片-" + (i + 1);
    var note = att.note ? String(att.note) : "";
    if (att.skipped || !att.path) {
      // skipped / 没有 path = 服务端没有给出可用文件:只留失败行,不参与发送。
      var why = note || "服务端未接受该文件";
      entries.push({ name: name, path: "", note: why, ok: false });
      problems.push(name + ": " + why);
      continue;
    }
    entries.push({
      name: name,
      path: String(att.path),
      bytes: att.bytes || 0,
      width: att.width || 0,
      height: att.height || 0,
      note: note,
      ok: true
    });
    added++;
  }
  render();
  if (!list.length) {
    reportFailure("图片上传失败: 服务端未返回任何附件");
    return;
  }
  if (problems.length) {
    var message = "已跳过 " + problems.length + " 张: " + problems.join(";");
    setStatus(message + (added ? " (已添加 " + added + " 张)" : ""));
    showToast(message, "error", 6000);
    return;
  }
  if (added) { setStatus("已添加 " + added + " 张图片,发送时随消息一起提交"); }
}

// ---- 三入口 ----

function onFilePick() {
  // 选择器给的是 FileList（不是 DataTransfer），直接归一成数组，不走 collectFiles。
  uploadFiles(fileListToArray(fileInputEl ? fileInputEl.files : null));
  // 清空 value:同一个文件再次选择也要触发 change(否则第二次选择静默无效)。
  if (fileInputEl) { fileInputEl.value = ""; }
}

function onPaste(ev) {
  var dataTransfer = ev && ev.clipboardData;
  var files = collectFiles(dataTransfer);
  if (!files.length) { return; } // 纯文本粘贴:交给 textarea 默认行为
  if (ev.preventDefault) { ev.preventDefault(); } // 有图片就不要把文件名当文本插进输入框
  uploadFiles(files);
}

// hasFiles:拖拽会话里是否带着文件。只看 files 长度 / types 里的 "Files",不猜其它类型。
function hasFiles(dataTransfer) {
  if (!dataTransfer) { return false; }
  var files = dataTransfer.files;
  if (files && files.length) { return true; }
  var types = dataTransfer.types;
  if (!types) { return false; }
  if (typeof types.indexOf === "function" && types.indexOf("Files") >= 0) { return true; }
  if (typeof types.contains === "function" && types.contains("Files")) { return true; }
  return false;
}

function setDragActive(on) {
  if (panelEl && panelEl.classList) { panelEl.classList.toggle("attach-dragover", !!on); }
}

function onDragOver(ev) {
  if (!hasFiles(ev && ev.dataTransfer)) { return; }
  // 必须阻止默认:否则浏览器把拖入的图片当导航目标,drop 时直接打开图片文件。
  if (ev.preventDefault) { ev.preventDefault(); }
  if (ev.dataTransfer) {
    try { ev.dataTransfer.dropEffect = "copy"; } catch (e) { /* 只读实现忽略 */ }
  }
  setDragActive(true);
}

function onDragLeave(ev) {
  // 拖到面板内的子元素会冒泡出 dragleave:relatedTarget 仍在面板内就保持高亮。
  var to = ev && ev.relatedTarget;
  if (to && panelEl && typeof panelEl.contains === "function" && panelEl.contains(to)) { return; }
  setDragActive(false);
}

function onDrop(ev) {
  var dataTransfer = ev && ev.dataTransfer;
  setDragActive(false); // 收尾:不论是否真有文件,高亮都不留
  if (!hasFiles(dataTransfer)) { return; }
  if (ev.preventDefault) { ev.preventDefault(); }
  uploadFiles(collectFiles(dataTransfer));
}

function bindDrop() {
  var host = panelEl || trackEl; // 面板内拖放(面板缺失时退到轨道容器,仍不落到页面级)
  if (!host || typeof host.addEventListener !== "function") { return; }
  host.addEventListener("dragover", onDragOver);
  host.addEventListener("dragleave", onDragLeave);
  host.addEventListener("drop", onDrop);
}

export function initComposerAttachments() {
  if (!document || typeof document.getElementById !== "function") { return; }
  trackEl = document.getElementById("attachment-track");
  fileInputEl = document.getElementById("attach-file");
  panelEl = document.getElementById("composer-panel");
  var attachBtn = document.getElementById("attach-btn");
  if (!trackEl || !fileInputEl) { return; } // 页面没有附件轨(旧页面 / 裁剪构建):静默降级
  if (attachBtn) {
    attachBtn.addEventListener("click", function () { fileInputEl.click(); });
  }
  fileInputEl.addEventListener("change", onFilePick);
  if (promptEl && promptEl.addEventListener) { promptEl.addEventListener("paste", onPaste); }
  if (trackEl.addEventListener) { trackEl.addEventListener("click", onTrackClick); }
  bindDrop();
  render();
}
