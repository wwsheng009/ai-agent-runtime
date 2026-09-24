// 精简 Markdown 解析器(粗体/代码块/表格/列表/引用/链接),输出已转义的安全 HTML。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

import { esc } from "./util.js";

// ---- 精简 Markdown 解析器 ----
// 将 Markdown 文本转换为安全的 HTML。
// 支持：**粗体**、~~删除线~~、`行内代码`、```代码块```、# 标题、
//       - 无序列表、1. 有序列表、- [x] 任务列表、> 引用块、
//       | 表格 |、[链接](url)、裸 URL 自动链接
// 注意：所有输入先经 esc() 转义，因此这里匹配的是转义后的实体
// （如 > 为 &gt;），保证输出安全。

// 解析表格行（| a | b |），返回 <table> HTML；无表头分隔行时按普通行渲染。
function renderTableBlock(blockLines) {
  function cells(row) {
    var s = row.trim();
    if (s.charAt(0) === "|") { s = s.slice(1); }
    if (s.charAt(s.length - 1) === "|") { s = s.slice(0, -1); }
    return s.split("|").map(function (c) { return c.trim(); });
  }
  function isSepRow(cs) {
    return cs.length > 0 && cs.every(function (c) {
      return /^:?-{3,}:?$/.test(c);
    });
  }
  var rows = blockLines.map(cells);
  var header = null;
  var body = rows;
  if (rows.length >= 2 && isSepRow(rows[1])) {
    header = rows[0];
    body = rows.slice(2);
  }
  function cellHtml(c, align) {
    var style = align && align !== "" ? ' style="text-align:' + align + '"' : "";
    return "<td" + style + ">" + c + "</td>";
  }
  function alignOf(c) {
    if (/^:.*:$/.test(c)) { return "center"; }
    if (/^:/.test(c)) { return "left"; }
    if (/:$/.test(c)) { return "right"; }
    return "";
  }
  var h = "";
  if (header) {
    var aligns = rows[1].map(alignOf);
    h += "<thead><tr>";
    header.forEach(function (c, i) {
      var style = aligns[i] ? ' style="text-align:' + aligns[i] + '"' : "";
      h += "<th" + style + ">" + c + "</th>";
    });
    h += "</tr></thead>";
  }
  if (body.length) {
    h += "<tbody>";
    body.forEach(function (r) {
      h += "<tr>" + r.map(function (c) { return cellHtml(c); }).join("") + "</tr>";
    });
    h += "</tbody>";
  }
  return "<table>" + h + "</table>";
}

export function renderMarkdown(text) {
  if (!text) return "";
  var html = esc(text);
  // 代码块（```...```）→ <pre> + 复制按钮 + 语言标签 + <code>，
  // 先用占位符暂存，避免后续行级规则（表格/引用/列表/URL）误处理代码内容，
  // 全部处理完成后恢复。复制行为由 #stream-msg 上的事件委托处理。
  var codeBlocks = [];
  html = html.replace(/```(\w*)\n?([\s\S]*?)```/g, function (_, lang, code) {
    var label = lang ? '<span class="lang-label">' + lang + '</span>' : '';
    var block = '<pre><button class="copy-code-btn" type="button" title="复制代码">复制</button>'
      + label + '<code class="lang-' + (lang || 'text') + '">'
      + esc(code.trim()) + '</code></pre>';
    codeBlocks.push(block);
    return "\u0001MDC" + (codeBlocks.length - 1) + "\u0001";
  });
  // 行内代码（`...`）
  html = html.replace(/`([^`]+)`/g, '<code>$1</code>');
  // 删除线（~~text~~）
  html = html.replace(/~~([^~]+)~~/g, '<del>$1</del>');
  // 标题（# ～ ######）
  html = html.replace(/^###### (.+)$/gm, '<h6>$1</h6>');
  html = html.replace(/^##### (.+)$/gm, '<h5>$1</h5>');
  html = html.replace(/^#### (.+)$/gm, '<h4>$1</h4>');
  html = html.replace(/^### (.+)$/gm, '<h3>$1</h3>');
  html = html.replace(/^## (.+)$/gm, '<h2>$1</h2>');
  html = html.replace(/^# (.+)$/gm, '<h1>$1</h1>');
  // 粗体（**...**）
  html = html.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>');
  // 任务列表（- [x] / - [ ]），需在无序列表之前
  html = html.replace(/^[-*] \[([ xX])\] (.+)$/gm, function (_, checked, item) {
    return '<li class="task' + (checked !== " " ? " done" : "") + '">'
      + '<input type="checkbox" disabled' + (checked !== " " ? " checked" : "") + '> '
      + item + '</li>';
  });
  // 无序列表（- 或 * 开头）
  html = html.replace(/^[*-] (.+)$/gm, '<li>$1</li>');
  // 有序列表（1. text）
  html = html.replace(/^(\d+)\. (.+)$/gm, '<li class="li-num">$2</li>');
  // 将连续 <li> 包裹为 <ul> / <ol>（非贪婪，空行中断）
  html = html.replace(/((?:<li[^>]*>.*?<\/li>\n?)+)/g, function (m) {
    if (m.indexOf("<ul>") !== -1 || m.indexOf("<ol>") !== -1) { return m; }
    var isOrdered = m.indexOf('class="li-num"') !== -1;
    return isOrdered ? "<ol>" + m + "</ol>" : "<ul>" + m + "</ul>";
  });
  // 引用块（> 开头，连续行合并）
  html = html.replace(/(^|\n)&gt;(?:[^\n]*)(?:\n&gt;[^\n]*)*/g, function (m) {
    var lines = m.split("\n").map(function (l) {
      return l.replace(/^&gt;/, "").replace(/^ /, "");
    });
    return "\n<blockquote>" + lines.join("<br>") + "</blockquote>";
  });
  // 表格（连续 | 行块；含 |---| 分隔行则渲染表头）
  html = html.replace(/(?:^|\n)(?:\|[^\n]+\|\n?){2,}/g, function (m) {
    var lines = m.split("\n").filter(function (l) { return l.trim() !== ""; });
    // 至少两行且每行都是表格行
    var allRows = lines.every(function (l) {
      var t = l.trim();
      return t.charAt(0) === "|" && t.charAt(t.length - 1) === "|";
    });
    if (!allRows) { return m; }
    return "\n" + renderTableBlock(lines) + "\n";
  });
  // 链接 [text](url)
  html = html.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');
  // 裸 URL 自动链接（排除已生成的 <a href="..."> 属性；剥离尾部标点）
  html = html.replace(/(^|[^"'>])(https?:\/\/[^\s<]+)/g, function (m, pre, url) {
    var clean = url.replace(/[),.;:!?'"，。；：！？」』】》]+$/, "");
    var rest = url.slice(clean.length);
    return pre + '<a href="' + clean + '" target="_blank" rel="noopener">' + clean + '</a>' + rest;
  });
  // 换行转 <br>
  html = html.replace(/\n/g, '<br>');
  // 修复 <li>/<blockquote> 内尾随 <br>
  html = html.replace(/<li><br>/g, '<li>');
  html = html.replace(/<\/li><br>/g, '</li>');
  html = html.replace(/<\/blockquote><br>/g, '</blockquote>');
  // 恢复代码块
  html = html.replace(/\u0001MDC(\d+)\u0001/g, function (_, i) {
    return codeBlocks[+i] || "";
  });
  // 去除块级元素间/周围以及首行/行末的多余 <br>（避免额外渲染空行）。
  // 换行统一转 <br> 后，块级元素（h1~h6/ul/ol/li/p/blockquote/pre/table/…）
  // 自身已换行并带外边距，邻接的 <br> 会额外空一行；<br><br> 在块间则更易
  // 堆出多行空隙，故在块级标签边界与首尾统一清理。代码块 <pre> 已恢复后处理，
  // 其内部换行由 CSS white-space: pre-wrap 承载，不受影响。
  // 注：<br>+ 在正则里表示 <b + 多个 r + >（仅一对 <br>），须写为 (?:<br>)+ 才是
  //     “重复的 <br>”。
  html = html.replace(/(<br>)+((?:<(?:h[1-6]|ul|ol|li|p|blockquote|pre|table|thead|tbody|tr|td|th)\b[^>]*>))/g, "$2");
  html = html.replace(/((?:<\/(?:h[1-6]|ul|ol|li|p|blockquote|pre|table|thead|tbody|tr|td|th)\b[^>]*>))(?:<br>)+/g, "$1");
  html = html.replace(/^(?:<br>)+/, "").replace(/(?:<br>)+$/, "");
  return html;
}

// ---- 消息正文渲染方式（md | txt）----
// 对话区 assistant 气泡右上角的 md|txt 切换用：md = 走上面的精简 Markdown 解析器；
// txt = 原样转义（逐条切换回退用）。assistant 行的默认渲染方式由 chat.js 中的
// DEFAULT_RENDER_MODE 决定（现为 md）——normalizeRenderMode 本身不承担“默认”义务，
// 仅把 "md" 归一为 md、其它一切归一为 text。流式气泡（#stream-msg）固定走
// renderMarkdown，不经这里，避免两处默认值互相漂移。

// 归一化渲染方式：仅 "md" 视为 Markdown，其余（含缺省/非法值）一律 text。
export function normalizeRenderMode(mode) {
  return mode === "md" ? "md" : "text";
}

// 按渲染方式生成消息正文 HTML（安全：两种方式都先经 esc()）。
export function renderMessageBody(text, mode) {
  if (normalizeRenderMode(mode) === "md") { return renderMarkdown(text); }
  return esc(text || "");
}

