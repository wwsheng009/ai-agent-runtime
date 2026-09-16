// diff 渲染原子：hunk 头部（折叠 / 展开上下文）+ 折叠区间（gap）+ 行内容（unified / split）
// + 解析失败时的原始文本降级面板（raw，可复制 / 下载 .patch）。
//
// 后端契约（types/runtime/git-browse.ts）：行类型仅 context/add/del/nonewline；`oldNo/newNo`
// 为 null 表示该侧不存在（禁用 0 顶替）；`nonewline` 是 `\ No newline at end of file` 标记行；
// hunk 一律来自服务端（前端不自行拼接 diff 文本，避免与服务端结论不一致）。
//
// 归一化纪律：行文本一律走 React 文本节点（无 dangerouslySetInnerHTML），高亮只加 token class；
// split 缺侧显示占位格（不复制对侧文本、不编造行号）；组件不跨模式读数据；
// 行号列**每行只保留一列**（unified 原来是老/新两列，现合并为一列：add→新侧、del→老侧、
// context→新侧；split 每侧各一列）；行号仍进 aria 文案，`oldNo/newNo` 保留在数据模型里。
//
// 降级判据：Prism 语言表未就绪 / 语言不支持 → 回落纯文本行（不阻塞首屏，不显示假高亮）；
// `expandStep === null` 表示上下文已达上限 → 隐藏展开入口，而不是发无效请求；
// 原始文本面板只读 `raw`（复制 / 下载都不发新请求），超出行预算只提示、不静默丢内容。

import { ChevronDownIcon, ChevronRightIcon } from "lucide-react";
import type { TFunction } from "i18next";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  codeHighlightingReady,
  highlightCode,
  isCodeHighlightingReady,
  supportsHighlighting,
  type CodeHighlightSegment,
} from "@/components/ui/code-highlighting";
import {
  countHunkChanges,
  patchFileName,
  type DiffCell,
  type DiffGap,
  type DiffViewMode,
  type DiffViewRow,
} from "@/lib/git/diff-view-model";
import { cn } from "@/lib/utils";
import type { GitDiffHunk } from "@/types/runtime/git-browse";

/** diff 行固定行高（与虚拟列表共享，必须一致）。 */
export const DIFF_ROW_HEIGHT = 22;

/**
 * 差异正文视口外框：Git 面板与消息流 apply_patch 面板**共用同一份类名**。
 *
 * 口径：正文不铺整层灰底——增删语义只由行底色 `-bg` token 承载，外层再叠
 * `bg-black/*` 会让上下文行与留白整体发灰、与宿主面板底色割裂。
 * 高度 / 布局差异由调用方追加自己的 className，外框配色一律复用本常量。
 *
 * 历史缺陷：两处各自复制类名，消息流一侧漏改，长期残留 `bg-black/20` 的整层灰底。
 */
export const DIFF_VIEWPORT_FRAME = "min-h-0 rounded-panel border border-white/8";

const NUMBER_CELL = "select-none px-2 text-right font-mono app-text-11 text-code-line-number";
const TEXT_CELL = "min-w-0 truncate px-2 font-mono app-text-12 whitespace-pre";
const GHOST_BUTTON = "rounded-chip px-1.5 py-0.5 hover:bg-white/6";
const EMPTY_CELL: DiffCell = { lineNo: null, prefix: "", text: "", tone: "empty" };

const TONE_CLASS: Record<DiffCell["tone"], string> = {
  // 底色只负责「这一行变了」，代码文本一律用前景色：语义由底色 + `+`/`-` 标记 + 行 aria 承载。
  // 历史缺陷：把半透明的 `-accent`（左轨色）当文字色 → 「绿字压浅绿底 / 红字压浅红底」无法分辨。
  add: "bg-code-line-inserted-bg text-foreground",
  del: "bg-code-line-deleted-bg text-foreground",
  context: "text-foreground/85",
  nonewline: "text-muted-foreground italic",
  // split 缺侧占位：主题表面色（不再用 `black/10` 的灰块）。
  empty: "bg-surface-softer",
};

/** `+`/`-` 标记色：实体 fg token（`-accent` 是半透明的轨 / 边框色，不得当文字色）。 */
const TONE_MARKER_CLASS: Record<DiffCell["tone"], string> = {
  add: "text-code-line-inserted-fg",
  del: "text-code-line-deleted-fg",
  context: "text-foreground/60",
  nonewline: "text-muted-foreground/70",
  empty: "",
};

export type GitDiffHunkProps = {
  hunk: GitDiffHunk;
  hunkIndex: number;
  collapsed: boolean;
  /** 展开步长（行）；null = 已到上限（隐藏入口，不发无效请求）。 */
  expandStep: number | null;
  onToggleCollapse: (hunkIndex: number) => void;
  onExpandContext: (step: number) => void;
};

/** hunk 头部行：折叠后为该 hunk 唯一可见行（折叠只影响渲染，不改变 diff 语义）。 */
export function GitDiffHunk({
  hunk,
  hunkIndex,
  collapsed,
  expandStep,
  onToggleCollapse,
  onExpandContext,
}: GitDiffHunkProps) {
  const { t } = useTranslation("workspace");
  const { additions, deletions } = countHunkChanges(hunk);
  const toggleLabel = collapsed ? t("panels.git.diff.expandHunk") : t("panels.git.diff.collapseHunk");

  return (
    <div
      aria-label={t("panels.git.diff.hunkAriaLabel", { header: hunk.header })}
      className="flex h-full items-center gap-2 border-y border-border bg-surface-softer px-1 app-text-11 text-muted-foreground"
      data-diff-hunk-header={collapsed ? "collapsed" : "expanded"}
      role="row"
    >
      <span role="cell">
        <button
          aria-expanded={!collapsed}
          aria-label={toggleLabel}
          className="flex size-5 items-center justify-center rounded-chip hover:bg-white/6"
          onClick={() => onToggleCollapse(hunkIndex)}
          title={toggleLabel}
          type="button"
        >
          {collapsed ? <ChevronRightIcon size={13} /> : <ChevronDownIcon size={13} />}
        </button>
      </span>
      <span className="min-w-0 truncate font-mono" role="cell">
        {hunk.header}
      </span>
      {collapsed ? (
        <span className="shrink-0" role="cell">
          {t("panels.git.diff.collapsedLines", { count: hunk.lines.length })}
        </span>
      ) : null}
      <span className="ml-auto flex shrink-0 items-center gap-2" role="cell">
        <span className="text-code-line-inserted-fg">
          {t("panels.git.diff.additions", { count: additions })}
        </span>
        <span className="text-code-line-deleted-fg">
          {t("panels.git.diff.deletions", { count: deletions })}
        </span>
        {expandStep !== null ? (
          <button
            className={GHOST_BUTTON}
            onClick={() => onExpandContext(expandStep)}
            title={t("panels.git.diff.expandContextTitle", { count: expandStep })}
            type="button"
          >
            {t("panels.git.diff.expandContext", { count: expandStep })}
          </button>
        ) : null}
      </span>
    </div>
  );
}

export type DiffGapRowProps = {
  gap: DiffGap;
  /** 展开步长（行）；null = 已到上限。 */
  expandStep: number | null;
  onExpandContext: (step: number) => void;
};

/** 折叠区间行：「展开上方/下方 N 行」= 用更大 context 重新请求（后端不返回全文件）。 */
export function DiffGapRow({ gap, expandStep, onExpandContext }: DiffGapRowProps) {
  const { t } = useTranslation("workspace");
  const hidden =
    gap.hiddenOld === null || gap.hiddenNew === null ? null : Math.max(gap.hiddenOld, gap.hiddenNew);

  return (
    <div
      aria-label={
        hidden === null
          ? t("panels.git.diff.gapAriaUnknown")
          : t("panels.git.diff.gapAriaLabel", { count: hidden })
      }
      className="flex h-full items-center gap-2 border-y border-border/60 bg-surface-softer/60 px-2 app-text-11 text-muted-foreground"
      data-diff-gap={gap.position}
      role="row"
    >
      <span className="min-w-0 truncate" role="cell">
        {hidden === null
          ? t("panels.git.diff.gapUnknown")
          : t("panels.git.diff.gapHidden", { count: hidden })}
      </span>
      {expandStep !== null ? (
        <span className="ml-auto flex shrink-0 items-center gap-1" role="cell">
          <button
            className={GHOST_BUTTON}
            onClick={() => onExpandContext(expandStep)}
            title={t("panels.git.diff.expandAboveTitle", { count: expandStep })}
            type="button"
          >
            {t("panels.git.diff.expandAbove", { count: expandStep })}
          </button>
          <button
            className={GHOST_BUTTON}
            onClick={() => onExpandContext(expandStep)}
            title={t("panels.git.diff.expandBelowTitle", { count: expandStep })}
            type="button"
          >
            {t("panels.git.diff.expandBelow", { count: expandStep })}
          </button>
        </span>
      ) : null}
    </div>
  );
}

export type DiffLineRowProps = { row: DiffViewRow; mode: DiffViewMode; language: string };

/**
 * 单行内容：unified = **一列行号** + 前缀 + 文本；split = 每侧「一列行号 + 前缀 + 文本」。
 * 行号列原来在 unified 是并排两列（老/新），现已合并为一列：add→新侧、del→老侧、context→新侧。
 */
export function DiffLineRow({ row, mode, language }: DiffLineRowProps) {
  const { t } = useTranslation("workspace");
  const highlight = useLineHighlighting(language);
  const ariaLabel = rowAriaLabel(row, t);

  if (mode === "split") {
    const oldCell = row.old ?? EMPTY_CELL;
    const newCell = row.new ?? EMPTY_CELL;
    return (
      <div
        aria-label={ariaLabel}
        className="grid h-full grid-cols-[3rem_minmax(0,1fr)_3rem_minmax(0,1fr)] items-center"
        role="row"
      >
        <NumberCell lineNo={oldCell.lineNo} />
        <Cell content={oldCell} highlight={highlight} language={language} />
        <NumberCell lineNo={newCell.lineNo} />
        <Cell content={newCell} highlight={highlight} language={language} />
      </div>
    );
  }

  const content = row.unified ?? EMPTY_CELL;
  return (
    <div
      aria-label={ariaLabel}
      className="grid h-full grid-cols-[3rem_minmax(0,1fr)] items-center"
      role="row"
    >
      <NumberCell lineNo={content.lineNo} />
      <Cell content={content} highlight={highlight} language={language} />
    </div>
  );
}

/** 行号列（每行只渲染一列）；`null` = 该侧不存在 → 渲染空串，**不补 0**。 */
function NumberCell({ lineNo }: { lineNo: number | null }) {
  return (
    <span className={NUMBER_CELL} role="cell">
      {lineNo === null ? "" : String(lineNo)}
    </span>
  );
}

function Cell({
  content,
  highlight,
  language,
}: {
  content: DiffCell;
  highlight: boolean;
  language: string;
}) {
  return (
    <span
      className={cn(TEXT_CELL, TONE_CLASS[content.tone])}
      data-diff-cell={content.tone}
      role="cell"
    >
      <span className={cn("mr-1 select-none", TONE_MARKER_CLASS[content.tone])}>
        {content.prefix || " "}
      </span>
      <LineText highlight={highlight} language={language} text={content.text} tone={content.tone} />
    </span>
  );
}

/** nonewline 行文本为空时用 i18n 文案兜底（不伪造内容，只兜底展示）。 */
function LineText({
  text,
  language,
  highlight,
  tone,
}: {
  text: string;
  language: string;
  highlight: boolean;
  tone: DiffCell["tone"];
}) {
  const { t } = useTranslation("workspace");
  if (tone === "nonewline" && text.trim() === "") {
    return <>{t("panels.git.diff.noNewlineMarker")}</>;
  }
  if (!highlight || !text) {
    return <>{text}</>;
  }
  return (
    <>
      {highlightLineCached(text, language).map((segment, index) =>
        segment.types.length > 0 ? (
          <span key={`seg-${index}`} className={cn("token", ...segment.types)}>
            {segment.content}
          </span>
        ) : (
          <span key={`seg-${index}`}>{segment.content}</span>
        ),
      )}
    </>
  );
}

/** Prism 就绪 + 语言可高亮；未就绪时先渲染纯文本，就绪后自动重渲染。 */
function useLineHighlighting(language: string) {
  const [ready, setReady] = useState(() => isCodeHighlightingReady());

  useEffect(() => {
    if (ready) {
      return;
    }
    let active = true;
    void codeHighlightingReady.then(() => {
      if (active) {
        setReady(true);
      }
    });
    return () => {
      active = false;
    };
  }, [ready]);

  return ready && supportsHighlighting(language);
}

// 单行高亮的 LRU 缓存：键 = 语言 + 文本（模式切换与滚动往返复用同一行结果）。
const HIGHLIGHT_CACHE_LIMIT = 512;
const highlightCache = new Map<string, CodeHighlightSegment[]>();

function highlightLineCached(text: string, language: string): CodeHighlightSegment[] {
  const key = `${language}\u0000${text}`;
  const cached = highlightCache.get(key);
  if (cached) {
    highlightCache.delete(key);
    highlightCache.set(key, cached);
    return cached;
  }
  const segments = highlightCode(text, language)[0]?.segments ?? [];
  highlightCache.set(key, segments);
  if (highlightCache.size > HIGHLIGHT_CACHE_LIMIT) {
    const oldest = highlightCache.keys().next().value;
    if (oldest !== undefined) {
      highlightCache.delete(oldest);
    }
  }
  return segments;
}

/** 行 aria：增删语义不靠颜色单独承载（读屏必须能分辨 add / del / context / pair）。 */
function rowAriaLabel(row: DiffViewRow, t: TFunction<"workspace">): string {
  const lineNo = (row.unified ?? row.old)?.lineNo ?? row.new?.lineNo ?? 0;
  if (row.kind === "add") {
    return t("panels.git.diff.rowAria.add", { no: String(lineNo) });
  }
  if (row.kind === "del") {
    return t("panels.git.diff.rowAria.del", { no: String(lineNo) });
  }
  if (row.kind === "pair") {
    return t("panels.git.diff.rowAria.pair", {
      oldNo: String(row.old?.lineNo ?? 0),
      newNo: String(row.new?.lineNo ?? 0),
    });
  }
  if (row.kind === "nonewline") {
    return t("panels.git.diff.rowAria.nonewline");
  }
  return t("panels.git.diff.rowAria.context", { no: String(lineNo) });
}

/** 原始 diff 纯文本展示上限（超出只提示，不静默丢内容）。 */
export const RAW_TEXT_LINE_CAP = 400;

const RAW_CHIP =
  "rounded-chip border border-white/10 px-2 py-0.5 app-text-11 hover:bg-white/6 disabled:opacity-50";

/**
 * 解析失败（parse_error）时的诚实降级：只展示后端给的原始 diff 文本，
 * 不渲染任何结构化行、不显示「无改动」；超出展示上限时明示还有多少行未展示。
 */
export function RawDiffPane({ raw, testId }: { raw: string; testId: string }) {
  const { t } = useTranslation("workspace");
  const lines = useMemo(() => raw.replace(/\r\n?/g, "\n").split("\n"), [raw]);
  const shown = lines.slice(0, RAW_TEXT_LINE_CAP);
  return (
    <div className="grid min-h-0 grid-rows-[auto_1fr] gap-1">
      <span className="app-text-10 text-muted-foreground/80">
        {t("panels.git.diff.rawTitle", { count: lines.length, shown: String(shown.length) })}
        {lines.length > shown.length ? ` ${t("panels.git.diff.rawLineCap")}` : ""}
      </span>
      <pre
        className="app-scrollbar min-h-0 select-text overflow-auto rounded-panel border border-white/8 bg-surface-soft/40 p-2 font-mono app-text-12 whitespace-pre"
        data-raw-lines={lines.length}
        data-raw-shown={shown.length}
        data-testid={testId}
      >
        {shown.join("\n")}
      </pre>
    </div>
  );
}

/** 复制 / 下载只以 `raw` 为源（不请求新端点）；失败必须明示原因，不假装成功。 */
export function RawDiffActions({ raw, path }: { raw: string; path: string }) {
  const { t } = useTranslation("workspace");
  const [feedback, setFeedback] = useState<"idle" | "copied" | "copyFailed" | "downloadFailed">("idle");
  const disabled = raw.length === 0;

  return (
    <span className="mt-1 flex flex-wrap items-center gap-1">
      <button
        className={RAW_CHIP}
        disabled={disabled}
        onClick={() => {
          void copyPlainText(raw).then((ok) => setFeedback(ok ? "copied" : "copyFailed"));
        }}
        type="button"
      >
        {t("panels.git.diff.copyRaw")}
      </button>
      <button
        className={RAW_CHIP}
        disabled={disabled}
        onClick={() => {
          if (!downloadTextFile(raw, patchFileName(path), "text/x-patch")) {
            setFeedback("downloadFailed");
          }
        }}
        type="button"
      >
        {t("panels.git.diff.downloadPatch")}
      </button>
      {feedback === "idle" ? null : (
        <span className="app-text-10 text-muted-foreground/80" role="status">
          {feedback === "copied"
            ? t("panels.git.diff.copyDone")
            : feedback === "copyFailed"
              ? t("panels.git.diff.copyFailed")
              : t("panels.git.diff.downloadFailed")}
        </span>
      )}
    </span>
  );
}

/** 剪贴板不可用 / 被拒绝 → 返回 false，由调用方显示失败文案。 */
async function copyPlainText(raw: string): Promise<boolean> {
  try {
    if (!navigator.clipboard?.writeText) {
      return false;
    }
    await navigator.clipboard.writeText(raw);
    return true;
  } catch {
    return false;
  }
}

/** Blob 下载（数据已在前端，不新增端点）；URL / DOM 不可用 → 返回 false。 */
function downloadTextFile(content: string, name: string, mime: string): boolean {
  try {
    const url = URL.createObjectURL(new Blob([content], { type: mime }));
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = name;
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
    setTimeout(() => URL.revokeObjectURL(url), 0);
    return true;
  } catch {
    return false;
  }
}
