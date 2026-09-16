// P1-6：工具行结构化明细。只读取事件/参数中真实存在的字段，缺失即不渲染，不做默认值补全。
// 数据来源：工具事件白名单（`file_path` / `attempted_args` / 输出元数据）与工具入参本身（P2-1B 之前无行级白名单字段）。

import { type AgentChatStreamChunkPayload } from "@/types/runtime";

import { readFirstNumberValue, readFirstTextValue } from "@/lib/thread-state/history-mapping";

import { parseToolPatch, patchTextFromArgs, patchTextFromOutput } from "./diff-text";
import { resolveToolCardKind, type ToolCardKind } from "./kind";
import { QUERY_ARG_KEYS, queryTextFromArgs, queryTextFromPreviewRaw } from "./query-text";
import { shellCommandTextFromArgs, shellCommandTextFromPreview } from "./shell-command";

export type ToolDiffStats = {
  additions?: number;
  removals?: number;
};

export type ToolSegmentDetails = {
  filePath?: string;
  /**
   * 目录列举工具（`ls` / `list_dir`）的目标目录：目录不是文件，只作为展示目标，
   * 因此**不写入** `filePath`——否则折叠行会渲染成「打开文件」链接（对目录是死目标）。
   */
  directoryPath?: string;
  command?: string;
  query?: string;
  url?: string;
  diff?: ToolDiffStats;
  exitCode?: number;
  /** 真实补丁文本（行级 diff 视图的数据源）；解析不出来就不保留，面板回落原始文本。 */
  diffText?: string;
  /** 补丁文本已按行边界截断到上限（UI 必须明示，不得假装完整）。 */
  diffTextTruncated?: boolean;
};

const PATCH_TARGET_PATTERN = /^\*\*\* (?:Update|Add|Delete) File: (.+)$/m;
// argsSummary 截断后 patch 文本已无真实换行，退化匹配嵌入在 JSON 字符串里的目标文件行。
const PATCH_TARGET_EMBEDDED_PATTERN = /\*\*\* (?:Update|Add|Delete) File: ([^"\\\n\r]+)/;
const PATCH_FILE_KEYS = ["file_path", "filePath", "file", "path"];
const STRICT_FILE_KEYS = ["file_path", "filePath", "file"];
const FILE_KINDS: ReadonlySet<ToolCardKind> = new Set(["read", "diff", "image"]);

function filePathKeys(kind: ToolCardKind) {
  return FILE_KINDS.has(kind) ? PATCH_FILE_KEYS : STRICT_FILE_KEYS;
}

/**
 * 文件目标键：目录列举类（`list`）没有文件目标——即便事件里带了 `display_file_path`
 * 之类的字段也必须忽略，否则目录会被渲染成「打开文件」死链接。
 */
function fileTargetKeys(kind: ToolCardKind) {
  return kind === "list" ? [] : filePathKeys(kind);
}

/**
 * 目录列举工具的目录键（运行时内置 `ls {path, depth}`、MCP `list_dir {directory}`）。
 * 与文件路径键分开读：目录只能当展示目标，不能当可打开的文件。
 */
const LIST_DIRECTORY_KEYS = ["path", "dir", "directory"];

function directoryPathText(
  kind: ToolCardKind,
  args: Record<string, unknown> | undefined,
  preview: Record<string, string>,
) {
  if (kind !== "list") {
    return "";
  }
  return readArgsText(args, LIST_DIRECTORY_KEYS) || readPreviewText(preview, LIST_DIRECTORY_KEYS);
}

/**
 * 批量文件入参（`files: [{file_path, limit, offset}, …]`，view / write 的批量形态）：
 * 顶层没有单文件键时取**第一个**条目的路径。折叠态是 24px 单行，只承载首要目标，
 * 不伪造 "N 个文件" 之类的汇总；其余条目仍在展开面板的入参文本里。
 */
function firstListedFilePath(args: Record<string, unknown> | undefined, keys: string[]) {
  const listed = args?.files;
  const items = Array.isArray(listed) ? listed : listed === undefined ? [] : [listed];
  for (const item of items) {
    if (isRecord(item)) {
      const path = readFirstTextValue(item, ...keys);
      if (path) {
        return path;
      }
    }
  }
  return "";
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function tryParseRecord(text: string): Record<string, unknown> | undefined {
  const trimmed = text.trim();
  if (!trimmed.startsWith("{")) {
    return undefined;
  }
  try {
    const parsed: unknown = JSON.parse(trimmed);
    return isRecord(parsed) ? parsed : undefined;
  } catch {
    return undefined;
  }
}

function toolPayloadSources(payload: AgentChatStreamChunkPayload) {
  const sources: Record<string, unknown>[] = [];
  for (const candidate of [payload.tool, payload.tool_call, payload.delta, payload.metadata]) {
    if (isRecord(candidate)) {
      sources.push(candidate);
    }
  }
  sources.push(payload as unknown as Record<string, unknown>);
  return sources;
}

function firstText(sources: Record<string, unknown>[], keys: string[]) {
  for (const source of sources) {
    const text = readFirstTextValue(source, ...keys);
    if (text) {
      return text;
    }
  }
  return "";
}

function firstNumber(sources: Record<string, unknown>[], keys: string[]) {
  for (const source of sources) {
    const value = readFirstNumberValue(source, ...keys);
    if (value !== undefined) {
      return value;
    }
  }
  return undefined;
}

function readArgsCandidate(sources: Record<string, unknown>[]) {
  for (const source of sources) {
    for (const key of ["args", "arguments", "input", "params"]) {
      const value = source[key];
      if (isRecord(value)) {
        return value;
      }
      if (typeof value === "string") {
        const parsed = tryParseRecord(value);
        if (parsed) {
          return parsed;
        }
      }
    }
    if (isRecord(source["attempted_args"])) {
      return source["attempted_args"];
    }
  }
  return undefined;
}

/**
 * 取「非 JSON 的入参文本」：实时帧把 `arg_preview` 塞进 `tool.args` /
 * `tool_call.arguments`（后端与前端桥接都这么做），它解析不出 JSON，但正是实时行
 * 唯一的入参来源。JSON 入参不在这里返回——那条路走 `readArgsCandidate`。
 */
function readArgsPreviewCandidate(sources: Record<string, unknown>[]) {
  for (const source of sources) {
    for (const key of ["args", "arguments", "input", "params"]) {
      const value = source[key];
      if (typeof value === "string" && value.trim() && !tryParseRecord(value)) {
        return value.trim();
      }
    }
  }
  return "";
}

function readArgsText(args: Record<string, unknown> | undefined, keys: string[]) {
  return args ? readFirstTextValue(args, ...keys) : "";
}

/** 形似文件路径（非 JSON、非多行、含扩展名或分隔符）时才作为路径使用，避免把整段参数误当真路径。 */
function looksLikePath(text: string) {
  const trimmed = text.trim();
  if (!trimmed || trimmed.length > 200 || trimmed.includes("\n")) {
    return false;
  }
  if (/[{}"']/.test(trimmed)) {
    return false;
  }
  return /[./\\]/.test(trimmed);
}

// 实时帧（`chat.sse.tool_*` / 运行时 `tool_started`）**不带结构化 arguments**：
// 后端 `toolRequestedEventPayload` 只下发 `arg_preview`（`summarizeToolCallArgs`
// 渲染的 `key=value` 键值文本）与 `command_text` / `display_file_path` 等定位字段，
// 完整入参要到回合末证据尾巴才出现。`arg_preview` 与 JSON 无关，JSON.parse 必然失败，
// 因此这里按后端同一套键值格式还原成表，实时行才能和历史行渲染出同一份摘要。
const ARG_PREVIEW_TOKEN_PATTERN = /(?:^|\s)([A-Za-z_][A-Za-z0-9_.-]*)=/g;

/** 解析后端 `arg_preview` 键值文本；值按下一个 `key=` 边界切分（预览有损，仅作兜底）。 */
export function parseArgPreviewText(preview: string): Record<string, string> {
  const text = preview.trim();
  const parsed: Record<string, string> = {};
  if (!text) {
    return parsed;
  }
  const tokens = [...text.matchAll(ARG_PREVIEW_TOKEN_PATTERN)];
  tokens.forEach((token, index) => {
    const key = token[1];
    const valueStart = (token.index ?? 0) + token[0].length;
    const valueEnd =
      index + 1 < tokens.length ? (tokens[index + 1].index ?? text.length) : text.length;
    const value = text.slice(valueStart, valueEnd).trim();
    if (key && value && parsed[key] === undefined) {
      parsed[key] = value;
    }
  });
  return parsed;
}

/** 从预览键值表里按别名顺序取值（大小写不敏感，后端键名沿用工具原始入参拼写）。 */
function readPreviewText(preview: Record<string, string>, keys: string[]) {
  for (const key of keys) {
    const value = preview[key]?.trim();
    if (value) {
      return value;
    }
  }
  for (const [key, value] of Object.entries(preview)) {
    if (keys.some((candidate) => candidate.toLowerCase() === key.toLowerCase())) {
      return value.trim();
    }
  }
  return "";
}

export function countDiffLines(body: string): ToolDiffStats {
  let additions = 0;
  let removals = 0;
  for (const line of body.split(/\r?\n/)) {
    if (line.startsWith("+") && !line.startsWith("+++")) {
      additions += 1;
    } else if (line.startsWith("-") && !line.startsWith("---")) {
      removals += 1;
    }
  }
  return { additions, removals };
}

export function countContentLines(text: string) {
  if (!text) {
    return 0;
  }
  const normalized = text.endsWith("\n") ? text.slice(0, -1) : text;
  return normalized === "" ? 0 : normalized.split(/\r?\n/).length;
}

function diffFromPatch(patch: string): ToolDiffStats | undefined {
  if (!patch.includes("\n+") && !patch.includes("\n-")) {
    return undefined;
  }
  return countDiffLines(patch);
}

function diffFromArgs(args: Record<string, unknown> | undefined): ToolDiffStats | undefined {
  if (!args) {
    return undefined;
  }
  const patch = readArgsText(args, ["patch", "diff"]);
  if (patch) {
    return diffFromPatch(patch);
  }
  const nextText = readArgsText(args, ["new_string", "new_str", "newText", "content", "contents"]);
  const previousText = readArgsText(args, ["old_string", "old_str", "oldText"]);
  if (!nextText && !previousText) {
    return undefined;
  }
  const additions = nextText ? countContentLines(nextText) : undefined;
  const removals = previousText ? countContentLines(previousText) : undefined;
  if (!additions && !removals) {
    return undefined;
  }
  return { additions, removals };
}

function compact(details: ToolSegmentDetails) {
  return Object.keys(details).length > 0 ? details : undefined;
}

/**
 * 保留行级 diff 文本：只有能解析成带行号 hunk 的补丁才写入 details
 * （解析失败 = 保持原样，面板继续展示原始文本，不存半截结构化数据）。
 * 增删统计优先用事件里已有的真实数字，缺失时才用补丁行统计兜底。
 */
function withDiffText(
  details: ToolSegmentDetails,
  args: Record<string, unknown> | undefined,
  outputText: string,
): ToolSegmentDetails {
  const patch = patchTextFromOutput(outputText) ?? patchTextFromArgs(args);
  if (!patch) {
    return details;
  }
  const parsed = parseToolPatch(patch.text);
  if (!parsed.ok) {
    return details;
  }
  details.diffText = patch.text;
  if (patch.truncated) {
    details.diffTextTruncated = true;
  }
  if (!details.diff) {
    details.diff = { additions: parsed.insertions, removals: parsed.deletions };
  }
  return details;
}

/** 从工具事件入参里解析明细；入参是裸路径字符串时按工具种类回退识别。 */
export function parseToolDetailsFromArgsText(
  text: string,
  toolName: string,
  outputText = "",
): ToolSegmentDetails | undefined {
  const trimmed = text.trim();
  if (!trimmed) {
    return undefined;
  }

  const kind = resolveToolCardKind(toolName);
  const parsed = tryParseRecord(trimmed);
  if (parsed) {
    const details = extractDetailsFromArgs(parsed, kind);
    if (kind === "diff") {
      withDiffText(details, parsed, outputText);
    }
    return compact(details);
  }

  // 实时帧的 argsSummary 是后端 `arg_preview` 键值文本（非 JSON）：解析失败时按
  // 同一格式还原，否则实时行拿不到 command / pattern，摘要退化成只剩工具名。
  const preview = parseArgPreviewText(trimmed);
  if (Object.keys(preview).length > 0) {
    const details = extractDetailsFromArgs(undefined, kind, preview);
    if (kind === "diff") {
      withDiffText(details, undefined, outputText);
    }
    const compacted = compact(details);
    if (compacted) {
      return compacted;
    }
  }

  const details: ToolSegmentDetails = {};
  if (kind === "diff") {
    // 入参本身就是补丁正文（没有 JSON 包裹）时同样尝试保留行级文本。
    withDiffText(details, { patch: trimmed }, outputText);
  }
  const patchTarget =
    trimmed.match(PATCH_TARGET_PATTERN)?.[1]?.trim() ??
    trimmed.match(PATCH_TARGET_EMBEDDED_PATTERN)?.[1]?.trim();
  if (patchTarget) {
    details.filePath = patchTarget;
  } else if (FILE_KINDS.has(kind) && looksLikePath(trimmed)) {
    details.filePath = trimmed;
  }
  return compact(details);
}

function extractDetailsFromArgs(
  args: Record<string, unknown> | undefined,
  kind: ToolCardKind,
  preview: Record<string, string> = {},
): ToolSegmentDetails {
  const details: ToolSegmentDetails = {};
  const filePath =
    readArgsText(args, fileTargetKeys(kind)) ||
    // 批量形态（`files` 列表）顶层没有单文件键，取第一个条目的路径。
    firstListedFilePath(args, fileTargetKeys(kind)) ||
    readPreviewText(preview, fileTargetKeys(kind));
  if (filePath) {
    details.filePath = filePath;
  } else {
    const patchTarget = readArgsText(args, ["patch", "diff"]).match(PATCH_TARGET_PATTERN)?.[1]?.trim();
    if (patchTarget) {
      details.filePath = patchTarget;
    }
  }

  const directoryPath = directoryPathText(kind, args, preview);
  if (directoryPath) {
    details.directoryPath = directoryPath;
  }

  const command =
    shellCommandTextFromArgs(args) ||
    readPreviewText(preview, ["command", "cmd", "script"]) ||
    shellCommandTextFromPreview(readPreviewText(preview, ["commands"]));
  if (command) {
    details.command = command;
  }
  const query =
    queryTextFromArgs(args, QUERY_ARG_KEYS) ||
    queryTextFromPreviewRaw(readPreviewText(preview, QUERY_ARG_KEYS));
  if (query) {
    details.query = query;
  }
  const url =
    readArgsText(args, ["url", "uri", "href"]) ||
    readPreviewText(preview, ["url", "uri", "href"]);
  if (url) {
    details.url = url;
  }
  if (kind !== "diff") {
    return details;
  }
  const diff = diffFromArgs(args);
  if (diff) {
    details.diff = diff;
  }
  return details;
}

export function extractToolDetails(
  payload: AgentChatStreamChunkPayload,
  toolName: string,
): ToolSegmentDetails | undefined {
  const sources = toolPayloadSources(payload);
  const args = readArgsCandidate(sources);
  const kind = resolveToolCardKind(toolName);
  const details: ToolSegmentDetails = {};
  // 实时帧的入参预览（`arg_preview` 键值文本 / 桥接塞进 `tool.args` 的同一份文本）。
  const preview = parseArgPreviewText(
    firstText(sources, ["arg_preview"]) || readArgsPreviewCandidate(sources),
  );

  const filePath =
    (kind === "list"
      ? ""
      : firstText(sources, [
          "file_path",
          "filePath",
          "resolved_path",
          "original_path",
          // 后端 `copyToolDisplayFilePath` 只在路径需要独占一行时下发它。
          "display_file_path",
        ])) ||
    readArgsText(args, fileTargetKeys(kind)) ||
    // 批量形态（`files` 列表）顶层没有单文件键，取第一个条目的路径。
    firstListedFilePath(args, fileTargetKeys(kind)) ||
    readPreviewText(preview, fileTargetKeys(kind));
  if (filePath) {
    details.filePath = filePath;
  } else {
    const patchTarget = readArgsText(args, ["patch", "diff"]).match(PATCH_TARGET_PATTERN)?.[1]?.trim();
    if (patchTarget) {
      details.filePath = patchTarget;
    }
  }

  // 实时帧同样只带 `arg_preview`（`path=<目录> depth=<n>`），与历史行走同一套目录解析。
  const directoryPath = directoryPathText(kind, args, preview);
  if (directoryPath) {
    details.directoryPath = directoryPath;
  }

  const command =
    shellCommandTextFromArgs(args) ||
    // `command_text`：后端对 shell 类工具下发的原始命令（实时帧与尾巴都有）。
    firstText(sources, ["command_text", "backend_command"]) ||
    readPreviewText(preview, ["command", "cmd", "script"]) ||
    shellCommandTextFromPreview(readPreviewText(preview, ["commands"]));
  if (command) {
    details.command = command;
  }
  const query =
    queryTextFromArgs(args, QUERY_ARG_KEYS) ||
    queryTextFromPreviewRaw(readPreviewText(preview, QUERY_ARG_KEYS));
  if (query) {
    details.query = query;
  }
  const url =
    readArgsText(args, ["url", "uri", "href"]) ||
    readPreviewText(preview, ["url", "uri", "href"]);
  if (url) {
    details.url = url;
  }
  const exitCode = firstNumber(sources, ["exit_code", "exitCode"]);
  if (exitCode !== undefined) {
    details.exitCode = exitCode;
  }

  const additions = firstNumber(sources, ["additions", "lines_added"]);
  const removals = firstNumber(sources, ["removals", "lines_removed"]);
  if (additions !== undefined || removals !== undefined) {
    details.diff = { additions, removals };
  } else if (kind === "diff") {
    const diff = diffFromArgs(args);
    if (diff) {
      details.diff = diff;
    }
  }

  if (kind === "diff") {
    // render_output 是后端下发的工具输出原文（apply_patch 带 ```diff 围栏）；
    // 取不到再退回 content/output/result，最后退回入参 patch。
    const outputText =
      firstText(sources, ["render_output"]) || firstText(sources, ["content", "output", "result"]);
    withDiffText(details, args, outputText);
  }

  return compact(details);
}

/** 组件侧兜底：优先用 ingest 阶段的结构化明细，历史/演示数据回退解析 argsSummary。 */
export function resolveToolSegmentDetails(segment: {
  name: string;
  argsSummary?: string;
  resultSummary?: string;
  details?: ToolSegmentDetails;
}): ToolSegmentDetails | undefined {
  if (segment.details) {
    return segment.details;
  }
  if (segment.argsSummary) {
    return parseToolDetailsFromArgsText(segment.argsSummary, segment.name, segment.resultSummary ?? "");
  }
  // 没有入参文本时仍可从结果里的 ```diff 围栏恢复行级文本（历史/演示数据的常见形态）。
  return compact(withDiffText({}, undefined, segment.resultSummary ?? ""));
}

/** 工具路径与产物路径的宽松匹配：分隔符归一后的全等或相对/绝对后缀关系。 */
export function matchToolFilePath(artifactPath: string, toolPath: string) {
  const normalize = (value: string) => value.trim().replace(/\\/g, "/").replace(/^\.\//, "");
  const left = normalize(artifactPath);
  const right = normalize(toolPath);
  if (!left || !right) {
    return false;
  }
  return left === right || left.endsWith(`/${right}`) || right.endsWith(`/${left}`);
}
