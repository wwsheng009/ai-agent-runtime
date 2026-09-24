// 精简 Markdown 解析器(粗体/代码块/表格/列表/引用/链接),输出已转义的安全 HTML。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。
//
// 实现：先按行切「块」，再在块内套行内规则。
//   - 每个块渲染成真正的块级元素（h1~h6 / ul / ol / pre / blockquote / table / p）。
//     块级元素自身即换行，所以顶层块之间用一个 <br> 表示「恰好一个空行」；
//     文档首尾不再额外插 <br>，保证不以空行开头/结尾。
//   - 列表项之间、引用块内的续行之间保持紧凑（不插空行）。
//   - 段落内的软换行保留为 <br>（web 侧既有行为，避免吞掉模型输出的换行）。
//   - 代码块内容只 esc() 一次，内部换行原样保留，交给 CSS white-space: pre-wrap。
// 以上与 TUI 侧 cmd/aicli/ui/markdown 的 SpacingDefault 语义一致：块间一个空行、
// 列表/引用内紧凑、文档首尾无空行。
//
// 为什么不再用「整体把 \n 转 <br>，再用正则回头删多余 <br>」：那种后处理无法区分
// 「块边界上冗余的空行」与「作者确实写下的空行」，会把后者一并删掉（表现为额外的
// 换行被吃掉）；因此改为在解析阶段直接产出正确的块结构，从根上不产生多余空行。

import { esc } from "./util.js";

// ---- 行内规则 ----

// 行内代码先摘成占位符，避免代码里的 ** / ~~ / [ ] 被后续行内规则误处理
// （旧实现先替换 <code>，导致 `**x**` 中的星号仍被当成粗体）。\u0001 是正文里
// 不会出现的控制字符，renderMarkdown 入口会先把它从输入中剔除。
function renderInline(raw) {
  var s = esc(raw);
  var codes = [];
  s = s.replace(/`([^`]+)`/g, function (_, code) {
    codes.push("<code>" + code + "</code>");
    return "\u0001IC" + (codes.length - 1) + "\u0001";
  });
  s = s.replace(/~~([^~]+)~~/g, "<del>$1</del>");
  s = s.replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>");
  s = s.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');
  // 裸 URL 自动链接（排除已生成的 <a href="..."> 属性；剥离尾部标点）
  s = s.replace(/(^|[^"'>])(https?:\/\/[^\s<]+)/g, function (m, pre, url) {
    var clean = url.replace(/[),.;:!?'"，。；：！？」』】》]+$/, "");
    var rest = url.slice(clean.length);
    return pre + '<a href="' + clean + '" target="_blank" rel="noopener">' + clean + "</a>" + rest;
  });
  return s.replace(/\u0001IC(\d+)\u0001/g, function (_, i) { return codes[+i] || ""; });
}

function inlineLine(line) { return renderInline(line); }

// ---- 块级识别规则 ----

var FENCE_OPEN = /^ {0,3}(`{3,}|~{3,})[ \t]*([^\s`]*)/;
var FENCE_CLOSE = /^ {0,3}(`{3,}|~{3,})[ \t]*$/;
var HEADING = /^ {0,3}(#{1,6})(?:[ \t]+(.*))?$/;
var QUOTE_PREFIX = /^ {0,3}> ?/;
var LIST_ITEM = /^ {0,3}([-*+]|\d{1,9}[.)])[ \t]+(.*)$/;
var TABLE_ROW = /^ {0,3}\|.*\|[ \t]*$/;
var BLANK = /^[ \t]*$/;
// 引用块嵌套上限：超过后按纯文本渲染，避免异常输入把递归打穿。
var MAX_QUOTE_DEPTH = 8;

// 行首空白宽度（列表续行按条目内容列对齐用）。
function indentWidth(line) {
  return /^[ \t]*/.exec(line)[0].length;
}

// 去掉代码块内容首尾的空行，但保留行首缩进
// （旧实现用 trim() 会把首行的缩进一起吃掉）。
function trimBlankEdges(lines) {
  var a = 0;
  var b = lines.length;
  while (a < b && BLANK.test(lines[a])) { a++; }
  while (b > a && BLANK.test(lines[b - 1])) { b--; }
  return lines.slice(a, b).join("\n");
}

// ---- 块级渲染 ----

// 代码块：<pre> + 复制按钮 + 语言标签 + <code>。
// 内容取自原文（此处未经 esc），这里只转义一次；换行原样保留，不参与
// 「\n → <br>」转换，由 CSS white-space: pre-wrap 呈现。
// 复制行为由 #conversation 上的事件委托处理（见 app.js）。
function codeBlockHtml(lang, code) {
  var label = lang ? '<span class="lang-label">' + esc(lang) + "</span>" : "";
  return '<pre><button class="copy-code-btn" type="button" title="复制代码">复制</button>'
    + label + '<code class="lang-' + esc(lang || "text") + '">' + esc(code) + "</code></pre>";
}

// 标题：# ～ ######；结尾的闭合 # 串按 CommonMark 去掉（需前置空白）。
function headingHtml(level, title) {
  var text = (title || "").replace(/[ \t]+#+[ \t]*$/, "").replace(/[ \t]+$/, "");
  return "<h" + level + ">" + inlineLine(text) + "</h" + level + ">";
}

// 列表项：条目内的续行用 <br> 紧凑连接（不额外空行）。
// 任务列表（- [x] / - [ ]）仅对无序列表生效，且只看首行。
function listItemHtml(rawText, ordered) {
  var lines = rawText.split("\n");
  var task = ordered ? null : /^\[([ xX])\][ \t]+(.*)$/.exec(lines[0]);
  var head = task ? task[2] : lines[0];
  var body = [head].concat(lines.slice(1)).map(inlineLine).join("<br>");
  if (task) {
    var done = task[1] !== " ";
    return '<li class="task' + (done ? " done" : "") + '">'
      + '<input type="checkbox" disabled' + (done ? " checked" : "") + "> " + body + "</li>";
  }
  return "<li" + (ordered ? ' class="li-num"' : "") + ">" + body + "</li>";
}

// 表格：连续 | 行；第二行是 |---| 分隔行时渲染表头，否则整块按数据行渲染
// （与旧实现一致，避免退化成纯文本）。
function splitCells(row) {
  var s = row.trim();
  if (s.charAt(0) === "|") { s = s.slice(1); }
  if (s.charAt(s.length - 1) === "|") { s = s.slice(0, -1); }
  return s.split("|").map(function (c) { return c.trim(); });
}

function isSeparatorRow(cells) {
  return cells.length > 0 && cells.every(function (c) { return /^:?-{3,}:?$/.test(c); });
}

function alignOf(cell) {
  if (/^:.*:$/.test(cell)) { return "center"; }
  if (/^:/.test(cell)) { return "left"; }
  if (/:$/.test(cell)) { return "right"; }
  return "";
}

function tableHtml(rows) {
  var hasHeader = isSeparatorRow(splitCells(rows[1]));
  var header = hasHeader ? splitCells(rows[0]) : null;
  var aligns = hasHeader ? splitCells(rows[1]).map(alignOf) : [];
  var body = (hasHeader ? rows.slice(2) : rows).map(splitCells);
  var styleAt = function (i) {
    return aligns[i] ? ' style="text-align:' + aligns[i] + '"' : "";
  };
  var h = "";
  if (header) {
    h += "<thead><tr>";
    header.forEach(function (c, i) { h += "<th" + styleAt(i) + ">" + inlineLine(c) + "</th>"; });
    h += "</tr></thead>";
  }
  if (body.length) {
    h += "<tbody>";
    body.forEach(function (r) {
      h += "<tr>" + r.map(function (c, i) {
        return "<td" + styleAt(i) + ">" + inlineLine(c) + "</td>";
      }).join("") + "</tr>";
    });
    h += "</tbody>";
  }
  return "<table>" + h + "</table>";
}

// ---- 块级扫描 ----

// 把行数组切成块并渲染；depth 仅用于引用块递归。
function renderBlocks(lines, depth) {
  var blocks = [];
  var para = [];
  var i = 0;
  var n = lines.length;

  function flushParagraph() {
    if (!para.length) { return; }
    // 段内软换行保留为 <br>：与旧行为一致，不吞模型的换行。
    blocks.push("<p>" + para.map(inlineLine).join("<br>") + "</p>");
    para = [];
  }

  while (i < n) {
    var line = lines[i];

    // 围栏代码块（``` 或 ~~~，可带语言）；未闭合时延续到文末（CommonMark 行为）
    var fence = FENCE_OPEN.exec(line);
    if (fence) {
      flushParagraph();
      var marker = fence[1].charAt(0);
      var fenceLen = fence[1].length;
      var code = [];
      i++;
      while (i < n) {
        var close = FENCE_CLOSE.exec(lines[i]);
        if (close && close[1].charAt(0) === marker && close[1].length >= fenceLen) { i++; break; }
        code.push(lines[i]);
        i++;
      }
      blocks.push(codeBlockHtml(fence[2], trimBlankEdges(code)));
      continue;
    }

    // 空行只是块分隔符，不产出内容：块间空行统一由最后的 join("<br>") 给出，
    // 这样「作者写了几个空行」都收敛为恰好一个空行，也不会重复叠加。
    if (BLANK.test(line)) { flushParagraph(); i++; continue; }

    // 标题
    var heading = HEADING.exec(line);
    if (heading) {
      flushParagraph();
      blocks.push(headingHtml(heading[1].length, heading[2]));
      i++;
      continue;
    }

    // 引用块：连续 > 行（中间空行后仍是 > 则并入同一块），块内再按块解析，
    // 因此多行引用是紧凑的 <br>，而引用内的列表/代码块仍各自成块。
    if (QUOTE_PREFIX.test(line)) {
      flushParagraph();
      var quoted = [];
      while (i < n) {
        if (QUOTE_PREFIX.test(lines[i])) {
          quoted.push(lines[i].replace(QUOTE_PREFIX, ""));
          i++;
        } else if (BLANK.test(lines[i]) && i + 1 < n && QUOTE_PREFIX.test(lines[i + 1])) {
          quoted.push("");
          i++;
        } else {
          break;
        }
      }
      blocks.push("<blockquote>" + (depth < MAX_QUOTE_DEPTH
        ? renderBlocks(quoted, depth + 1)
        : "<p>" + quoted.map(inlineLine).join("<br>") + "</p>") + "</blockquote>");
      continue;
    }

    // 表格：连续 | 行（至少两行）
    if (TABLE_ROW.test(line)) {
      var rows = [];
      var j = i;
      while (j < n && TABLE_ROW.test(lines[j])) { rows.push(lines[j]); j++; }
      if (rows.length >= 2) {
        flushParagraph();
        blocks.push(tableHtml(rows));
        i = j;
        continue;
      }
    }

    // 列表：连续同类型条目（无序 / 有序）。空行结束当前列表（松列表会渲染成
    // 两个列表，中间仍是一个空行，视觉一致且不会多出空行）。
    var item = LIST_ITEM.exec(line);
    if (item) {
      flushParagraph();
      var ordered = /\d/.test(item[1]);
      var items = [];
      while (i < n) {
        var m = LIST_ITEM.exec(lines[i]);
        if (!m || /\d/.test(m[1]) !== ordered) { break; }
        var contentIndent = m[0].length - m[2].length;
        var texts = [m[2]];
        i++;
        // 条目续行：缩进且非空、且不是新的列表项 → 并入当前条目
        while (i < n && !BLANK.test(lines[i]) && !LIST_ITEM.test(lines[i])
          && indentWidth(lines[i]) > 0) {
          texts.push(lines[i].slice(Math.min(indentWidth(lines[i]), contentIndent)));
          i++;
        }
        items.push(listItemHtml(texts.join("\n"), ordered));
      }
      blocks.push((ordered ? "<ol>" : "<ul>") + items.join("") + (ordered ? "</ol>" : "</ul>"));
      continue;
    }

    // 普通段落行
    para.push(line);
    i++;
  }
  flushParagraph();

  // 块级元素自身即换行，故一个 <br> 恰好表示块间那一行空行；
  // 首尾不插 <br>，保证文档不以空行开头/结尾（与 TUI SpacingDefault 一致）。
  return blocks.join("<br>");
}

export function renderMarkdown(text) {
  if (!text) { return ""; }
  var src = String(text)
    .replace(/\r\n?/g, "\n")
    // 去掉控制字符：既是输入清洗，也保证行内代码占位符 \u0001 不会与正文冲突。
    .replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f]/g, "");
  if (!src.trim()) { return ""; }
  return renderBlocks(src.split("\n"), 0);
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
