// P1-6：工具行结构化明细。只读取事件/参数中真实存在的字段，缺失即不渲染，不做默认值补全。
// 数据来源：工具事件白名单（`file_path` / `attempted_args` / 输出元数据）与工具入参本身（P2-1B 之前无行级白名单字段）。

import { type AgentChatStreamChunkPayload } from "@/types/runtime";

import { readFirstNumberValue, readFirstTextValue } from "@/lib/thread-state/history-mapping";

import { resolveToolCardKind, type ToolCardKind } from "./kind";

export type ToolDiffStats = {
  additions?: number;
  removals?: number;
};

export type ToolSegmentDetails = {
  filePath?: string;
  command?: string;
  query?: string;
  url?: string;
  diff?: ToolDiffStats;
  exitCode?: number;
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

/** 从工具事件入参里解析明细；入参是裸路径字符串时按工具种类回退识别。 */
export function parseToolDetailsFromArgsText(
  text: string,
  toolName: string,
): ToolSegmentDetails | undefined {
  const trimmed = text.trim();
  if (!trimmed) {
    return undefined;
  }

  const kind = resolveToolCardKind(toolName);
  const parsed = tryParseRecord(trimmed);
  if (parsed) {
    return compact(extractDetailsFromArgs(parsed, kind));
  }

  const details: ToolSegmentDetails = {};
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

function extractDetailsFromArgs(args: Record<string, unknown>, kind: ToolCardKind): ToolSegmentDetails {
  const details: ToolSegmentDetails = {};
  const filePath = readArgsText(args, filePathKeys(kind));
  if (filePath) {
    details.filePath = filePath;
  } else {
    const patchTarget = readArgsText(args, ["patch", "diff"]).match(PATCH_TARGET_PATTERN)?.[1]?.trim();
    if (patchTarget) {
      details.filePath = patchTarget;
    }
  }

  const command = readArgsText(args, ["command", "cmd", "script"]);
  if (command) {
    details.command = command;
  }
  const query = readArgsText(args, ["query", "pattern", "q", "search"]);
  if (query) {
    details.query = query;
  }
  const url = readArgsText(args, ["url", "uri", "href"]);
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

  const filePath =
    firstText(sources, ["file_path", "filePath", "resolved_path", "original_path"]) ||
    readArgsText(args, filePathKeys(kind));
  if (filePath) {
    details.filePath = filePath;
  } else {
    const patchTarget = readArgsText(args, ["patch", "diff"]).match(PATCH_TARGET_PATTERN)?.[1]?.trim();
    if (patchTarget) {
      details.filePath = patchTarget;
    }
  }

  const command = readArgsText(args, ["command", "cmd", "script"]) || firstText(sources, ["backend_command"]);
  if (command) {
    details.command = command;
  }
  const query = readArgsText(args, ["query", "pattern", "q", "search"]);
  if (query) {
    details.query = query;
  }
  const url = readArgsText(args, ["url", "uri", "href"]);
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

  return compact(details);
}

/** 组件侧兜底：优先用 ingest 阶段的结构化明细，历史/演示数据回退解析 argsSummary。 */
export function resolveToolSegmentDetails(segment: {
  name: string;
  argsSummary?: string;
  details?: ToolSegmentDetails;
}): ToolSegmentDetails | undefined {
  if (segment.details) {
    return segment.details;
  }
  return segment.argsSummary
    ? parseToolDetailsFromArgsText(segment.argsSummary, segment.name)
    : undefined;
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
