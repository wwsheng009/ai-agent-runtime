// extract-shadow-calls.mjs — 从 aicli 聊天日志提取真实的 grep/view 调用，
// 生成 Phase1-shadow 重放测量所需的 JSONL 调用集（tool + args + output）。
//
// 用法：
//   node scripts/extract-shadow-calls.mjs <chat-logs 根目录> <workspace 根目录> <输出 JSONL> [最大条数]
//
// 输出每行：{"tool":"grep|view","args":{...},"output":"...","session_id":"..."}
//
// 数据来源：chat.json 的 message_type=tool_result（content.function + content.result）。
// baseline 用 result.output —— 即模型当时实际看到的输出；grep 的参数只有
// result.arg_preview 这一份"渲染后的预览"（runtime 未落原始参数），因此：
//   - pattern/regexp 单模式可还原；
//   - patterns 批量（预览里以 " | " 分隔）无法逐条还原 → 跳过并计数；
//   - path/paths/glob/include 按预览键边界切分，值里若恰好含 " key=" 会切错
//     （罕见，计数里体现不出来；报告需注明该已知折损）。
// view 的 offset/limit 未落库 → 重放时 offset=1、limit 由输出非空行数推导。
import { readFileSync, readdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";

const [logsRoot, workspace, outFile, maxRaw] = process.argv.slice(2);
if (!logsRoot || !workspace || !outFile) {
  console.error("usage: node extract-shadow-calls.mjs <chat-logs-root> <workspace> <out.jsonl> [max]");
  process.exit(2);
}
const maxCalls = Number.parseInt(maxRaw ?? "400", 10);
const targetWorkspace = normalize(resolve(workspace));

function normalize(p) {
  return resolve(p).replaceAll("\\", "/").replace(/\/+$/, "").toLowerCase();
}

function walkChatFiles(dir, out = []) {
  let entries;
  try {
    entries = readdirSync(dir, { withFileTypes: true });
  } catch {
    return out;
  }
  for (const entry of entries) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) {
      walkChatFiles(full, out);
    } else if (entry.isFile() && entry.name === "chat.json") {
      out.push(full);
    }
  }
  return out;
}

function textOf(content) {
  if (typeof content === "string") return content;
  if (Array.isArray(content)) {
    return content.map((part) => (typeof part === "string" ? part : part?.text ?? "")).join("");
  }
  return "";
}

// viewArgs 从 view 输出里还原 offset/limit：输出行形如 "123:\tcontent"，
// 首个行号即 offset，编号行数即 limit（runtime 未落原始参数，这是最优还原）。
function viewArgs(filePath, output) {
  const args = { file_path: filePath };
  let offset = 0;
  let numbered = 0;
  for (const line of output.split("\n")) {
    const m = /^\s*(\d+)[:\t]/.exec(line);
    if (!m) continue;
    numbered++;
    if (offset === 0) offset = Number.parseInt(m[1], 10);
  }
  if (offset > 0) args.offset = offset;
  if (numbered > 0) args.limit = numbered;
  return args;
}

// parseArgPreview 按 " key=" 边界切分 arg_preview。所有键都参与切分，
// 只有白名单键进入返回值。
const PREVIEW_VALUE_KEYS = new Set([
  "pattern", "patterns", "regexp", "path", "paths", "glob", "include", "exclude",
  "literal", "count", "files_with_matches", "case_insensitive", "fixed_strings",
]);
function parseArgPreview(preview) {
  if (!preview) return {};
  const marks = [];
  const re = /(?:^|\s)([a-z_]+)=/g;
  let m;
  while ((m = re.exec(preview)) !== null) {
    marks.push({ key: m[1], valueStart: m.index + m[0].length, boundary: m.index });
  }
  const args = {};
  for (let i = 0; i < marks.length; i++) {
    if (!PREVIEW_VALUE_KEYS.has(marks[i].key)) continue;
    const end = i + 1 < marks.length ? marks[i + 1].boundary : preview.length;
    const value = preview.slice(marks[i].valueStart, end).trim();
    if (value) args[marks[i].key] = value;
  }
  return args;
}

const files = walkChatFiles(logsRoot);
const calls = [];
const stats = {
  chat_files: files.length,
  sessions_in_workspace: 0,
  sessions_skipped_workspace: 0,
  deduplicated: 0,
  skipped_multi_pattern: 0,
  skipped_no_pattern: 0,
  skipped_error: 0,
  skipped_empty_output: 0,
  skipped_unsupported_tool: 0,
};
const seen = new Set();

outer: for (const file of files) {
  let session;
  try {
    session = JSON.parse(readFileSync(file, "utf8"));
  } catch {
    continue;
  }
  const ws = session?.working_directory ?? session?.project_path;
  if (!ws || normalize(ws) !== targetWorkspace) {
    stats.sessions_skipped_workspace++;
    continue;
  }
  stats.sessions_in_workspace++;

  for (const msg of session.messages ?? []) {
    if (msg?.message_type !== "tool_result") continue;
    const tool = String(msg?.content?.function ?? "").toLowerCase();
    if (tool !== "grep" && tool !== "view") {
      stats.skipped_unsupported_tool++;
      continue;
    }
    const result = msg?.content?.result ?? {};
    if (result.ok === false || (typeof result.error === "string" && result.error.trim() !== "")) {
      stats.skipped_error++;
      continue;
    }
    const output = textOf(result.output ?? msg?.content?.output ?? "");
    if (!output.trim()) {
      stats.skipped_empty_output++;
      continue;
    }

    let args;
    if (tool === "grep") {
      const preview = parseArgPreview(result.arg_preview ?? "");
      const pattern = preview.pattern ?? preview.regexp ?? "";
      const patternsValue = preview.patterns ?? "";
      if (pattern) {
        args = { pattern };
      } else if (patternsValue) {
        // 批量 pattern：预览以 " | " 渲染；重放端按数组还原（观察器两条通道都更新了）。
        const parts = patternsValue
          .split(" | ")
          .map((s) => s.trim())
          .filter(Boolean);
        if (parts.length === 1) {
          args = { pattern: parts[0] };
        } else if (parts.length > 1) {
          args = { patterns: parts };
        }
      }
      if (!args) {
        stats.skipped_no_pattern++;
        continue;
      }
      if (preview.path) args.path = preview.path;
      else if (preview.paths) {
        // 多路径必须整组保留（2026-09-29）：观察器已支持 paths 的逐项作用域，
        // 只取首项会让重放把多路径调用折叠成单路径，口径与真实调用不一致。
        const list = preview.paths.split(/\s+/).filter(Boolean);
        if (list.length > 1) args.paths = list;
        else if (list.length === 1) args.path = list[0];
      }
      if (preview.glob) args.glob = preview.glob;
      else if (preview.include) args.include = preview.include;
    } else {
      const filePath = result.file_path ?? result.display_file_path ?? "";
      if (!filePath) {
        stats.skipped_no_pattern++;
        continue;
      }
      args = viewArgs(filePath, output);
    }

    const key = `${tool}\u0000${JSON.stringify(args)}`;
    if (seen.has(key)) {
      stats.deduplicated++;
      continue;
    }
    seen.add(key);
    calls.push({ tool, args, output, session_id: session.session_id ?? "" });
    if (calls.length >= maxCalls) break outer;
  }
}

writeFileSync(outFile, calls.map((c) => JSON.stringify(c)).join("\n") + "\n", "utf8");
console.log(
  JSON.stringify(
    {
      ...stats,
      calls_written: calls.length,
      grep: calls.filter((c) => c.tool === "grep").length,
      view: calls.filter((c) => c.tool === "view").length,
      out: outFile,
    },
    null,
    2,
  ),
);
