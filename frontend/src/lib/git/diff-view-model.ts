// Git diff 视图模型：结构化 hunks → 行级虚拟列表可消费的行序列（纯函数，无 React / DOM 依赖）。
//
// 后端契约（frontend/src/types/runtime/git-browse.ts，产自 backend/internal/gitbrowse）：
//   * `GitDiffHunk.lines[].type ∈ {context, add, del, nonewline}`，`oldNo/newNo` 为 null 表示该侧不存在；
//   * `truncated / truncatedReason` 由服务端给出（行数 / 字节上限），前端只负责展示与「客户端渲染预算」；
//   * `parseError` 非空时 hunks 不可信：调用方必须降级展示 `raw`（本模块不产出可取信的行）。
//
// 归一化纪律：
//   * 行号缺失一律保留 null，**不用 0 伪装**（0 会被读成真实行号）；
//   * split 配对只在同一 hunk 内做「相邻 del 段 ↔ add 段按序配对」，跨 hunk 不配对、context 不参与配对；
//   * 折叠区间行数由相邻 hunk 的 old/new 行号区间推导；文件尾部行数服务端未给出 → 保留 null，不猜。
//
// 降级判据：
//   * `hunks` 为空 ≠ 无改动：调用方需结合 `truncated` / `parseError` / 二进制标记判断后再给结论；
//   * 渲染行数超过本地预算时只截断「渲染预算」，不改写 diff 语义，提示文案必须说明是客户端上限。

import type {
  GitDiffHunk,
  GitDiffLine,
} from "@/types/runtime/git-browse";

/** 单文件默认渲染行预算（超过后由「继续加载」追加）。 */
export const DEFAULT_DIFF_ROW_LIMIT = 2000;
/** 每次「继续加载」追加的渲染行数。 */
export const DIFF_ROW_LIMIT_STEP = 2000;
/** 请求上下文的默认行数（与 git diff -U3 对齐）。 */
export const DIFF_CONTEXT_DEFAULT = 3;
/** 单次「展开上下方 N 行」追加的上下文行数。 */
export const DIFF_CONTEXT_EXPAND_STEP = 50;
/** 上下文请求上限（「展开整个文件」用；服务端仍受自身行数/字节上限保护）。 */
export const MAX_DIFF_CONTEXT = 999_999;

export type DiffViewMode = "unified" | "split";

export type DiffCellTone =
  | "add"
  | "del"
  | "context"
  | "nonewline"
  /** 纯行号列（unified 模式的老/新行号列，无内容）。 */
  | "number"
  /** split 模式的缺侧占位（无行号、无内容）。 */
  | "empty";

/** 行内两侧的单格（split 两格 / unified 一格）。 */
export type DiffCell = {
  /** 老行号 / 新行号；null = 该侧不存在（契约禁止用 0 代替）。 */
  lineNo: number | null;
  prefix: "+" | "-" | " " | "";
  text: string;
  tone: DiffCellTone;
};

export type DiffRowKind =
  | "hunk-header"
  | "context"
  | "del"
  | "add"
  | "pair"
  | "nonewline"
  | "gap";

export type DiffGapPosition = "top" | "between" | "bottom";

/** 折叠区间：`hidden*` 为 null 表示服务端未提供该侧行数（文件尾部）→ UI 不得编造数字。 */
export type DiffGap = {
  position: DiffGapPosition;
  hiddenOld: number | null;
  hiddenNew: number | null;
};

export type DiffViewRow = {
  key: string;
  kind: DiffRowKind;
  /** 所属 hunk 下标：行 / 头部指向自身；gap 指向其后的 hunk（top=0，尾部=hunks.length）。 */
  hunkIndex: number;
  /** hunk 头部行携带的原始 header 文本（其余行为空串）。 */
  header: string;
  /**
   * 老/新两侧格子，口径随模式变化（组件不得跨模式混读）：
   * - unified：只承载**行号列**（prefix/text 为空），内容在 `unified`；
   * - split：承载各自的行号 + 前缀 + 文本，`unified` 为 null。
   */
  old: DiffCell | null;
  new: DiffCell | null;
  /** unified 模式渲染的单格；split 模式为 null。 */
  unified: DiffCell | null;
  gap: DiffGap | null;
};

export type DiffRowBuildOptions = {
  mode: DiffViewMode;
  /** 已折叠的 hunk 下标：只输出头部行（正文行不进入渲染预算）。 */
  collapsedHunks?: readonly number[];
  /** 是否输出文件尾部折叠行（行数未知，仅提供「展开更多上下文」入口）。 */
  includeBottomGap?: boolean;
};

export function buildDiffRows(
  hunks: readonly GitDiffHunk[],
  options: DiffRowBuildOptions,
): DiffViewRow[] {
  const collapsed = new Set(options.collapsedHunks ?? []);
  const rows: DiffViewRow[] = [];
  const keys = { seq: 0 };
  const nextKey = () => {
    keys.seq += 1;
    return `diff-row-${keys.seq}`;
  };
  const pushGap = (hunkIndex: number, gap: DiffGap) => {
    rows.push({
      key: nextKey(),
      kind: "gap",
      hunkIndex,
      header: "",
      old: null,
      new: null,
      unified: null,
      gap,
    });
  };

  if (hunks.length === 0) {
    return rows;
  }

  const first = hunks[0];
  const topOld = Math.max(first.oldStart - 1, 0);
  const topNew = Math.max(first.newStart - 1, 0);
  if (topOld > 0 || topNew > 0) {
    pushGap(0, { position: "top", hiddenOld: topOld, hiddenNew: topNew });
  }

  hunks.forEach((hunk, hunkIndex) => {
    rows.push({
      key: nextKey(),
      kind: "hunk-header",
      hunkIndex,
      header: hunk.header,
      old: null,
      new: null,
      unified: null,
      gap: null,
    });
    if (!collapsed.has(hunkIndex)) {
      pushHunkLines(rows, hunk, hunkIndex, options.mode, nextKey);
    }

    const next = hunks[hunkIndex + 1];
    if (!next) {
      return;
    }
    const hiddenOld = Math.max(next.oldStart - (hunk.oldStart + hunk.oldLines), 0);
    const hiddenNew = Math.max(next.newStart - (hunk.newStart + hunk.newLines), 0);
    if (hiddenOld > 0 || hiddenNew > 0) {
      pushGap(hunkIndex + 1, { position: "between", hiddenOld, hiddenNew });
    }
  });

  if (options.includeBottomGap !== false) {
    // 尾部行数服务端不给：只能给 null（文案用「可能还有」），并靠更大 context 重新请求。
    pushGap(hunks.length, { position: "bottom", hiddenOld: null, hiddenNew: null });
  }

  return rows;
}

function pushHunkLines(
  rows: DiffViewRow[],
  hunk: GitDiffHunk,
  hunkIndex: number,
  mode: DiffViewMode,
  nextKey: () => string,
) {
  let pendingDel: GitDiffLine[] = [];
  let pendingAdd: GitDiffLine[] = [];

  const flushUnified = () => {
    for (const line of pendingDel) {
      rows.push(unifiedRow(nextKey(), "del", hunkIndex, line, "del"));
    }
    for (const line of pendingAdd) {
      rows.push(unifiedRow(nextKey(), "add", hunkIndex, line, "add"));
    }
    pendingDel = [];
    pendingAdd = [];
  };

  const flushSplit = () => {
    const count = Math.max(pendingDel.length, pendingAdd.length);
    for (let index = 0; index < count; index += 1) {
      const del = pendingDel[index] ?? null;
      const add = pendingAdd[index] ?? null;
      const kind: DiffRowKind =
        del && add ? "pair" : del ? "del" : "add";
      rows.push({
        key: nextKey(),
        kind,
        hunkIndex,
        header: "",
        old: del ? toCell(del, "del") : emptyCell(),
        new: add ? toCell(add, "add") : emptyCell(),
        unified: null,
        gap: null,
      });
    }
    pendingDel = [];
    pendingAdd = [];
  };

  const flush = mode === "split" ? flushSplit : flushUnified;

  for (const line of hunk.lines) {
    if (line.type === "del") {
      pendingDel.push(line);
      continue;
    }
    if (line.type === "add") {
      pendingAdd.push(line);
      continue;
    }
    flush();
    if (line.type === "nonewline") {
      const cell = toCell(line, "nonewline");
      rows.push(
        mode === "split"
          ? {
              key: nextKey(),
              kind: "nonewline",
              hunkIndex,
              header: "",
              old: cell,
              new: cell,
              unified: null,
              gap: null,
            }
          : unifiedRow(nextKey(), "nonewline", hunkIndex, line, "nonewline"),
      );
      continue;
    }
    const cell = toCell(line, "context");
    rows.push(
      mode === "split"
        ? {
            key: nextKey(),
            kind: "context",
            hunkIndex,
            header: "",
            old: cell,
            new: cell,
            unified: null,
            gap: null,
          }
        : unifiedRow(nextKey(), "context", hunkIndex, line, "context"),
    );
  }
  flush();
}

function unifiedRow(
  key: string,
  kind: DiffRowKind,
  hunkIndex: number,
  line: GitDiffLine,
  tone: DiffCellTone,
): DiffViewRow {
  return {
    key,
    kind,
    hunkIndex,
    header: "",
    old: numberCell(tone === "add" ? null : line.oldNo),
    new: numberCell(tone === "del" ? null : line.newNo),
    unified: toCell(line, tone),
    gap: null,
  };
}

function numberCell(lineNo: number | null): DiffCell {
  return { lineNo, prefix: "", text: "", tone: "number" };
}

function emptyCell(): DiffCell {
  return { lineNo: null, prefix: "", text: "", tone: "empty" };
}

function toCell(line: GitDiffLine, tone: DiffCellTone): DiffCell {
  const prefix: DiffCell["prefix"] =
    tone === "add" ? "+" : tone === "del" ? "-" : tone === "context" ? " " : "";
  const lineNo =
    tone === "add" ? line.newNo : tone === "del" ? line.oldNo : line.oldNo ?? line.newNo;
  return { lineNo, prefix, text: line.text, tone };
}

export type DiffRowSlice = {
  rows: DiffViewRow[];
  /** 因客户端渲染预算被隐藏的行数（> 0 时必须提示并提供「继续加载」）。 */
  hidden: number;
  total: number;
};

/** 只截断渲染预算：不改变行内容与顺序，也不影响服务端截断标记。 */
export function limitDiffRows(
  rows: readonly DiffViewRow[],
  limit: number = DEFAULT_DIFF_ROW_LIMIT,
): DiffRowSlice {
  const safeLimit = Number.isFinite(limit) ? Math.max(1, Math.floor(limit)) : DEFAULT_DIFF_ROW_LIMIT;
  if (rows.length <= safeLimit) {
    return { rows: [...rows], hidden: 0, total: rows.length };
  }
  return {
    rows: rows.slice(0, safeLimit),
    hidden: rows.length - safeLimit,
    total: rows.length,
  };
}

/** 下一次「展开上下文」请求的 context 值（带上限保护；已在上限则返回 null 表示无需再请求）。 */
export function nextDiffContext(
  current: number,
  step: number = DIFF_CONTEXT_EXPAND_STEP,
  max: number = MAX_DIFF_CONTEXT,
): number | null {
  const safeCurrent = Number.isFinite(current) ? Math.max(0, Math.floor(current)) : DIFF_CONTEXT_DEFAULT;
  if (safeCurrent >= max) {
    return null;
  }
  const safeStep = Number.isFinite(step) ? Math.max(1, Math.floor(step)) : DIFF_CONTEXT_EXPAND_STEP;
  return Math.min(safeCurrent + safeStep, max);
}

/** 扩展名 → Prism 语言标识（未列出的扩展名一律回落纯文本，不按内容猜语言）。 */
export const DIFF_LANGUAGE_BY_EXTENSION: Record<string, string> = {
  ts: "typescript",
  tsx: "tsx",
  js: "javascript",
  jsx: "jsx",
  mjs: "javascript",
  cjs: "javascript",
  json: "json",
  md: "markdown",
  css: "css",
  html: "markup",
  htm: "markup",
  xml: "markup",
  go: "go",
  py: "python",
  rs: "rust",
  sh: "bash",
  bash: "bash",
  zsh: "bash",
  ps1: "powershell",
  yml: "yaml",
  yaml: "yaml",
  sql: "sql",
  diff: "diff",
  patch: "diff",
};

/** diff 视图的语法高亮语言；无扩展名 / 未知扩展名回落 "text"（宁可不高亮，不给假高亮）。 */
export function diffLanguageForPath(path: string): string {
  const name = path.split(/[\\/]/).pop() ?? "";
  const ext = name.includes(".") ? name.split(".").pop()!.toLowerCase() : "";
  return DIFF_LANGUAGE_BY_EXTENSION[ext] ?? "text";
}

/**
 * 原始 diff 的物理行数：先归一化 CRLF，再按行切分，末尾空行不计（避免多算一行）。
 * 仅用于展示「已截断，仅显示前 N 行」中的 N，不参与 diff 语义判断。
 */
export function countDiffRawLines(raw: string): number {
  if (raw.length === 0) {
    return 0;
  }
  const lines = raw.replace(/\r\n?/g, "\n").split("\n");
  return lines[lines.length - 1] === "" ? lines.length - 1 : lines.length;
}

/** 「下载 .patch」的文件名：只取 basename 并剔除 Windows 非法字符（不进路径，防目录穿越）。 */
export function patchFileName(path: string): string {
  const name = path.split(/[\\/]/).pop() ?? "";
  // 空路径 / 以分隔符结尾（"src/"）→ 回落通用名，不产出 ".patch"。
  const base = name.trim() || "diff";
  return `${base.replace(/[\\/:*?"<>|]/g, "_")}.patch`;
}

/** hunk 覆盖的原始行数（含两侧），用于折叠态摘要与断言。 */
export function countHunkLines(hunk: GitDiffHunk): number {
  return hunk.lines.length;
}

/** hunk 内增删计数（与 status 的 numstat 独立，用于行内摘要）。 */
export function countHunkChanges(hunk: GitDiffHunk): { additions: number; deletions: number } {
  let additions = 0;
  let deletions = 0;
  for (const line of hunk.lines) {
    if (line.type === "add") {
      additions += 1;
    } else if (line.type === "del") {
      deletions += 1;
    }
  }
  return { additions, deletions };
}
