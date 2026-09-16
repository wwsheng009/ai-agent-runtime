// apply_patch 展开面板的补丁文本通道：从工具事件/入参里取**真实**补丁文本，再解析成行级 diff。
//
// 契约纪律：
//   * 只解析**带行号**的 unified diff（`@@ -a,b +c,d @@`）。Codex 风格的裸 `@@` 补丁没有行号，
//     这里一律 `ok: false`，由面板回落到原始文本——绝不用 0 / 顺序号编造行号；
//   * 行内容、行号都逐行来自补丁文本本身（增删计数另由实际行统计），不做任何补全；
//   * 截断只发生在**行边界**（宁少整行，不给半行假内容），并回报 `truncated` 供 UI 明示；
//   * 非末尾 hunk 的行数与头部不一致 = 文本已损坏 → 判失败（不渲染半截结构化 diff）；
//     只有末尾 hunk 允许不完整（截断的典型形态），并标记 `partial`。

import type { GitDiffHunk, GitDiffLine } from "@/types/runtime/git-browse";

/** 保留的补丁文本上限（字符）：与文件预览同量级，避免把整份大补丁塞进消息状态。 */
export const TOOL_PATCH_TEXT_LIMIT = 256 * 1024;

export type ToolPatchText = {
  /** 逐行完整的补丁文本（可能已按行边界截断）。 */
  text: string;
  truncated: boolean;
};

export type ToolPatchFile = {
  path: string;
  hunks: GitDiffHunk[];
  insertions: number;
  deletions: number;
};

export type ToolPatchParse =
  | {
      ok: true;
      files: ToolPatchFile[];
      /** 所有文件 hunk 的拼接（行号是各文件内的真实行号，不跨文件重排）。 */
      hunks: GitDiffHunk[];
      insertions: number;
      deletions: number;
      /** 末尾 hunk 行数不足（补丁文本被截断）。 */
      partial: boolean;
    }
  | { ok: false; reason: string };

const HUNK_HEADER = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/;
const FENCE = /```(?:diff|patch)[^\n]*\n([\s\S]*?)```/gi;
// 文件头/文件级别的元数据行（git 会输出；正文行不会长这样，因为都带 +/-/空格标记）。
const METADATA =
  /^(index |new file mode |deleted file mode |old mode |new mode |similarity index |dissimilarity index |rename from |rename to |copy from |copy to |Binary files |GIT binary patch)/;

type MutableHunk = {
  header: string;
  oldStart: number;
  oldLines: number;
  newStart: number;
  newLines: number;
  lines: GitDiffLine[];
};

/** 按行边界截断：只去掉尾部整行，保证保留部分每一行都是原文。 */
function capLines(lines: string[]): ToolPatchText {
  const kept: string[] = [];
  let size = 0;
  for (const line of lines) {
    if (size + line.length + 1 > TOOL_PATCH_TEXT_LIMIT) {
      return { text: kept.join("\n"), truncated: true };
    }
    kept.push(line);
    size += line.length + 1;
  }
  return { text: kept.join("\n"), truncated: false };
}

function stripPathToken(token: string): string {
  let value = token.trim();
  if (value.startsWith('"')) {
    try {
      const parsed: unknown = JSON.parse(value);
      if (typeof parsed === "string") {
        value = parsed;
      }
    } catch {
      value = value.replace(/^"|"$/g, "");
    }
  }
  const tab = value.indexOf("\t");
  if (tab >= 0) {
    value = value.slice(0, tab);
  }
  return value.trim();
}

/** 去掉 `a/` `b/` 前缀；`/dev/null` 原样返回（由调用方决定文件新增/删除）。 */
function normalizeDiffPath(token: string): string {
  const value = stripPathToken(token);
  if (!value || value === "/dev/null") {
    return value;
  }
  if (value.startsWith("a/") || value.startsWith("b/")) {
    return value.slice(2);
  }
  return value;
}

/**
 * 从工具输出文本里提取补丁正文：优先 ```diff 围栏，其次从首个带行号的 `@@` 行往前回退到文件头。
 * 拿不到带行号的 hunk 头就返回 undefined（调用方保持原始文本面板）。
 */
export function patchTextFromOutput(text: string): ToolPatchText | undefined {
  if (!text) {
    return undefined;
  }
  const normalized = text.replace(/\r\n?/g, "\n");
  FENCE.lastIndex = 0;
  let match = FENCE.exec(normalized);
  while (match) {
    const body = match[1] ?? "";
    if (hasNumberedHunk(body)) {
      return capLines(body.replace(/\n$/, "").split("\n"));
    }
    match = FENCE.exec(normalized);
  }

  const lines = normalized.split("\n");
  const hunkIndex = lines.findIndex((line) => HUNK_HEADER.test(line));
  if (hunkIndex < 0) {
    return undefined;
  }
  let start = hunkIndex;
  while (start > 0 && isFileHeaderLine(lines[start - 1])) {
    start -= 1;
  }
  return capLines(lines.slice(start));
}

/** 工具入参里的 `patch` / `diff` 字段（Codex 补丁文本或 unified diff）。 */
export function patchTextFromArgs(args: Record<string, unknown> | undefined): ToolPatchText | undefined {
  if (!args) {
    return undefined;
  }
  for (const key of ["patch", "diff"]) {
    const value = args[key];
    if (typeof value !== "string" || !value.trim()) {
      continue;
    }
    const normalized = value.replace(/\r\n?/g, "\n");
    if (!normalized.includes("@@") && !normalized.includes("*** Begin Patch")) {
      continue;
    }
    return capLines(normalized.replace(/\n$/, "").split("\n"));
  }
  return undefined;
}

/**
 * 输出文本里的 ```diff 围栏已由行级视图承载 → 展示前去掉围栏，避免同一份 diff 出现两次。
 * 只会去掉正文里**有带行号 hunk** 的围栏（解析不了的仍按原始文本留给用户）；
 * 未闭合的围栏（结果被截断）同样处理，截断标记由调用方另行展示。
 */
export function stripRenderedPatch(text: string): string {
  if (!text) {
    return "";
  }
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  const dropped = new Set<number>();
  let index = 0;
  while (index < lines.length) {
    if (!/^```(?:diff|patch)\b/i.test(lines[index])) {
      index += 1;
      continue;
    }
    let close = index + 1;
    while (close < lines.length && !/^```\s*$/.test(lines[close])) {
      close += 1;
    }
    const body = lines.slice(index + 1, close);
    if (body.some((entry) => HUNK_HEADER.test(entry))) {
      for (let cursor = index; cursor <= close && cursor < lines.length; cursor += 1) {
        dropped.add(cursor);
      }
    }
    index = close + 1;
  }
  return lines
    .filter((_, position) => !dropped.has(position))
    .join("\n")
    .replace(/\n{3,}/g, "\n\n")
    .trim();
}

function hasNumberedHunk(text: string): boolean {
  return text.split("\n").some((line) => HUNK_HEADER.test(line));
}

function isFileHeaderLine(line: string): boolean {
  return (
    line.startsWith("diff --git ") ||
    line.startsWith("--- ") ||
    line.startsWith("+++ ") ||
    METADATA.test(line)
  );
}

function hunkIsComplete(hunk: MutableHunk): boolean {
  const oldSide = hunk.lines.filter((line) => line.type === "context" || line.type === "del").length;
  const newSide = hunk.lines.filter((line) => line.type === "context" || line.type === "add").length;
  return oldSide === hunk.oldLines && newSide === hunk.newLines;
}

/**
 * 解析 unified diff 正文。任意一处不符契约即 `ok: false`（含原因），由 UI 展示原始文本。
 */
export function parseToolPatch(text: string): ToolPatchParse {
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  const files: ToolPatchFile[] = [];
  let current: ToolPatchFile | undefined;
  let hunk: MutableHunk | undefined;
  let oldPath = "";
  let insertions = 0;
  let deletions = 0;
  let partial = false;

  /**
   * 结束当前 hunk：`isLast` 为真表示后面没有更多 hunk 了（文本在此结束），
   * 此时行数不足只可能是截断；否则说明补丁文本自身损坏，直接判失败。
   */
  const flushHunk = (isLast: boolean): ToolPatchParse | undefined => {
    if (!hunk) {
      return undefined;
    }
    if (!hunkIsComplete(hunk)) {
      if (!isLast) {
        return { ok: false, reason: "hunk-line-count-mismatch" };
      }
      partial = true;
    }
    hunk = undefined;
    return undefined;
  };

  for (const line of lines) {
    const hunkHeader = HUNK_HEADER.exec(line);
    if (hunkHeader) {
      if (!current) {
        return { ok: false, reason: "hunk-before-file-header" };
      }
      const failure = flushHunk(false);
      if (failure) {
        return failure;
      }
      hunk = {
        header: line.trim(),
        oldStart: Number(hunkHeader[1]),
        oldLines: hunkHeader[2] === undefined ? 1 : Number(hunkHeader[2]),
        newStart: Number(hunkHeader[3]),
        newLines: hunkHeader[4] === undefined ? 1 : Number(hunkHeader[4]),
        lines: [],
      };
      current.hunks.push(hunk);
      continue;
    }
    if (line.startsWith("diff --git ")) {
      const failure = flushHunk(false);
      if (failure) {
        return failure;
      }
      current = undefined;
      continue;
    }
    // `--- ` / `+++ ` 只有在没有未完成 hunk 时才是文件头：hunk 正文里带同样前缀的行是内容。
    if (line.startsWith("--- ") && (!hunk || hunkIsComplete(hunk))) {
      const failure = flushHunk(false);
      if (failure) {
        return failure;
      }
      oldPath = normalizeDiffPath(line.slice(4));
      current = undefined;
      continue;
    }
    if (line.startsWith("+++ ") && (!hunk || hunkIsComplete(hunk))) {
      const failure = flushHunk(false);
      if (failure) {
        return failure;
      }
      const newPath = normalizeDiffPath(line.slice(4));
      const path = newPath === "/dev/null" ? oldPath : newPath;
      if (!path) {
        return { ok: false, reason: "missing-file-path" };
      }
      current = { path, hunks: [], insertions: 0, deletions: 0 };
      files.push(current);
      continue;
    }
    if (!hunk) {
      // 文件头之间的元数据、说明文字一律跳过；本函数只对 hunk 内部的行作断言。
      continue;
    }
    if (line.startsWith("\\")) {
      hunk.lines.push({ type: "nonewline", oldNo: null, newNo: null, text: "" });
      continue;
    }
    if (line.length === 0) {
      // 空行按空上下文行处理（部分工具会省略前导空格，行本身是原文）。
      hunk.lines.push({ type: "context", oldNo: null, newNo: null, text: "" });
      continue;
    }
    const marker = line.slice(0, 1);
    if (marker === "+") {
      hunk.lines.push({ type: "add", oldNo: null, newNo: null, text: line.slice(1) });
      insertions += 1;
      continue;
    }
    if (marker === "-") {
      hunk.lines.push({ type: "del", oldNo: null, newNo: null, text: line.slice(1) });
      deletions += 1;
      continue;
    }
    if (marker === " ") {
      hunk.lines.push({ type: "context", oldNo: null, newNo: null, text: line.slice(1) });
      continue;
    }
    return { ok: false, reason: "unexpected-line-inside-hunk" };
  }

  const failure = flushHunk(true);
  if (failure) {
    return failure;
  }

  if (files.length === 0 || files.every((file) => file.hunks.length === 0)) {
    return { ok: false, reason: "no-hunks" };
  }

  for (const file of files) {
    numberHunks(file);
    file.insertions = countFileLines(file, "add");
    file.deletions = countFileLines(file, "del");
  }

  return {
    ok: true,
    files,
    hunks: files.flatMap((file) => file.hunks),
    insertions,
    deletions,
    partial,
  };
}

/** 行号只能来自 hunk 头 + 逐行推进（缺失即 null，不用 0 顶替）。 */
function numberHunks(file: ToolPatchFile) {
  for (const hunk of file.hunks) {
    let oldNo = hunk.oldStart;
    let newNo = hunk.newStart;
    for (const line of hunk.lines) {
      if (line.type === "context") {
        line.oldNo = oldNo;
        line.newNo = newNo;
        oldNo += 1;
        newNo += 1;
      } else if (line.type === "del") {
        line.oldNo = oldNo;
        line.newNo = null;
        oldNo += 1;
      } else if (line.type === "add") {
        line.oldNo = null;
        line.newNo = newNo;
        newNo += 1;
      }
    }
  }
}

function countFileLines(file: ToolPatchFile, type: GitDiffLine["type"]) {
  return file.hunks.reduce(
    (total, hunk) => total + hunk.lines.filter((line) => line.type === type).length,
    0,
  );
}
