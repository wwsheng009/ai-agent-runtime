// apply_patch 展开面板的行级 diff 视图：复用 Git 面板的渲染栈（hunk 头 / 行号 / 增删底色 / 虚拟列表）,
// 纯前端渲染，不发任何请求。
//
// 数据与降级纪律：
//   * 数据源是 ingest 阶段保留的**真实补丁文本**（details.diffText），这里只解析、不改写；
//   * 解析失败时本组件直接不渲染（由面板回落原始文本），绝不编造行号或补行；
//   * 「展开上下文」需要后端重算 diff，本面板没有端点 → expandStep 恒为 null（隐藏入口），
//     hunk 间隙只显示「已隐藏 N 行」；
//   * 行预算超限是**前端切片**：只提示并给「继续加载」，不假装补丁只有这么多行。

import { useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { VirtualLineList } from "@/components/workspace/diff/virtual-line-list";
import {
  ExpandedPreviewDialog,
  ExpandPreviewButton,
} from "@/components/workspace/expanded-preview";
import {
  DIFF_ROW_HEIGHT,
  DIFF_VIEWPORT_FRAME,
  DiffGapRow,
  DiffLineRow,
  GitDiffHunk,
  RawDiffActions,
} from "@/components/workspace/git/diff-hunk";
import {
  buildDiffRows,
  countDiffRawLines,
  DEFAULT_DIFF_ROW_LIMIT,
  DIFF_ROW_LIMIT_STEP,
  diffLanguageForPath,
  limitDiffRows,
  type DiffViewMode,
} from "@/lib/git/diff-view-model";
import { type ToolDiffStats } from "@/lib/tool-row";
import { parseToolPatch } from "@/lib/tool-row/diff-text";
import { cn } from "@/lib/utils";

/** 补丁之外没有第二份上下文来源 → 不提供「展开上下文」（null 会让渲染层隐藏入口）。 */
const EXPAND_STEP_NONE = null;
const NOOP_EXPAND = () => {};
const CHIP = "rounded-chip border border-white/10 px-2 py-0.5 app-text-11 hover:bg-white/6";

// 内容区固定高度 + 独立滚动，避免把整条消息流撑长；外框复用 Git 面板的共享常量——
// 消息流这里**不得**再铺 `bg-black/*` 的整层灰底（历史缺陷：本面板漏改，正文整体发灰）。
const VIEWPORT_CLASS = `h-[320px] ${DIFF_VIEWPORT_FRAME}`;
// 放大面板里的正文视口：高度由弹层给（`flex-1` 吃掉扣除提示条后的剩余高度），行仍走虚拟滚动。
const EXPANDED_VIEWPORT_CLASS = `${DIFF_VIEWPORT_FRAME} flex-1`;

export type ToolRowDiffPanelProps = {
  /** 真实补丁文本（unified diff，已在 ingest 阶段按上限截断）。 */
  patchText: string;
  /** 补丁文本被截断过（ingest 阶段标记，UI 必须明示）。 */
  truncated: boolean;
  /** 事件里的真实增删统计；缺失时用解析结果兜底。 */
  stats?: ToolDiffStats;
  /** 补丁路径缺失时的展示兜底（折叠态工具行已给出的文件路径）。 */
  filePathHint?: string;
};

export function ToolRowDiffPanel({
  patchText,
  truncated,
  stats,
  filePathHint,
}: ToolRowDiffPanelProps) {
  const { t } = useTranslation("workspace");
  const parsed = useMemo(() => parseToolPatch(patchText), [patchText]);
  const [mode, setMode] = useState<DiffViewMode>("unified");
  const [collapsed, setCollapsed] = useState<ReadonlySet<number>>(() => new Set());
  const [activeFile, setActiveFile] = useState(0);
  const [rowLimit, setRowLimit] = useState(DEFAULT_DIFF_ROW_LIMIT);
  /** 放大面板开关：与小窗口共用同一份解析结果 / 模式 / 行预算（放大只换视口尺寸）。 */
  const [expanded, setExpanded] = useState(false);

  const file = parsed.ok ? parsed.files[Math.min(activeFile, parsed.files.length - 1)] : undefined;
  const rows = useMemo(
    () =>
      file
        ? buildDiffRows(file.hunks, {
            mode,
            collapsedHunks: [...collapsed],
            // 补丁不含文件首尾的上下文，尾部 gap 无法展开 → 不渲染。
            includeBottomGap: false,
          })
        : [],
    [file, mode, collapsed],
  );
  const slice = useMemo(() => limitDiffRows(rows, rowLimit), [rows, rowLimit]);

  if (!parsed.ok || !file) {
    return null;
  }

  const pathLabel = file.path || filePathHint || "";
  const language = diffLanguageForPath(pathLabel);
  const additions = stats?.additions ?? parsed.insertions;
  const removals = stats?.removals ?? parsed.deletions;

  const toggleHunk = (hunkIndex: number) => {
    setCollapsed((previous) => {
      const next = new Set(previous);
      if (next.has(hunkIndex)) {
        next.delete(hunkIndex);
      } else {
        next.add(hunkIndex);
      }
      return next;
    });
  };

  // 小窗口（固定 320px）与放大面板（弹层给高度）共用同一份正文：行模型、诚实提示、原始 diff
  // 动作都只有这一份实现——放大不重新解析补丁，也不产生第二个数据源（testId 分开以免同屏撞号）。
  const renderDiffBody = (viewportClassName: string, listTestId: string) => (
    <>
      {slice.hidden > 0 ? (
        <DiffNotice testId="tool-row-diff-row-limit" tone="muted">
          <span>
            {t("panels.git.diff.rowLimit", {
              shown: String(slice.rows.length),
              total: String(slice.total),
            })}
          </span>
          <button
            className={cn(CHIP, "ml-2")}
            onClick={() => setRowLimit((value) => value + DIFF_ROW_LIMIT_STEP)}
            type="button"
          >
            {t("panels.git.diff.loadMoreRows", { count: DIFF_ROW_LIMIT_STEP })}
          </button>
        </DiffNotice>
      ) : null}
      {truncated ? (
        <DiffNotice testId="tool-row-diff-truncated" tone="warning">
          {t("panels.messages.toolRow.diff.truncated", {
            count: countDiffRawLines(patchText),
          })}
        </DiffNotice>
      ) : null}
      {parsed.partial ? (
        <DiffNotice testId="tool-row-diff-partial" tone="warning">
          {t("panels.messages.toolRow.diff.partial")}
        </DiffNotice>
      ) : null}
      <VirtualLineList
        className={viewportClassName}
        containerAriaLabel={t("panels.messages.toolRow.diff.ariaLabel", { path: pathLabel })}
        containerRole="rowgroup"
        resetKey={`${file.path}:${mode}`}
        rowHeight={DIFF_ROW_HEIGHT}
        rowKey={(row) => row.key}
        rows={slice.rows}
        testId={listTestId}
        renderRow={(row) =>
          row.kind === "hunk-header" ? (
            <GitDiffHunk
              collapsed={collapsed.has(row.hunkIndex)}
              expandStep={EXPAND_STEP_NONE}
              hunk={file.hunks[row.hunkIndex]}
              hunkIndex={row.hunkIndex}
              onExpandContext={NOOP_EXPAND}
              onToggleCollapse={toggleHunk}
            />
          ) : row.kind === "gap" && row.gap ? (
            <DiffGapRow
              expandStep={EXPAND_STEP_NONE}
              gap={row.gap}
              onExpandContext={NOOP_EXPAND}
            />
          ) : (
            <DiffLineRow language={language} mode={mode} row={row} />
          )
        }
      />
      <RawDiffActions path={pathLabel} raw={patchText} />
    </>
  );

  return (
    <div
      className="mt-1 grid gap-1.5 rounded-lg bg-surface-soft px-2.5 py-2"
      data-testid="tool-row-diff-panel"
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
          {t("panels.messages.toolRow.diff.label")}
        </span>
        <span
          className="app-text-11 text-code-line-inserted-fg"
          data-testid="tool-row-diff-additions"
        >
          {t("panels.git.diff.additions", { count: additions })}
        </span>
        <span
          className="app-text-11 text-code-line-deleted-fg"
          data-testid="tool-row-diff-deletions"
        >
          {t("panels.git.diff.deletions", { count: removals })}
        </span>
        <span className="ml-auto flex items-center gap-1">
          <span
            aria-label={t("panels.git.diff.modeLabel")}
            className="flex items-center gap-1"
            role="group"
          >
            {(["unified", "split"] as const).map((value) => (
              <button
                aria-pressed={mode === value}
                className={cn(CHIP, mode === value && "bg-white/10 text-foreground")}
                key={value}
                onClick={() => setMode(value)}
                type="button"
              >
                {t(
                  value === "unified"
                    ? "panels.git.diff.modeUnified"
                    : "panels.git.diff.modeSplit",
                )}
              </button>
            ))}
          </span>
          <ExpandPreviewButton
            label={t("panels.messages.toolRow.diff.expand")}
            onClick={() => setExpanded(true)}
            testId="tool-row-diff-expand"
          />
        </span>
      </div>
      {parsed.files.length > 1 ? (
        <div
          aria-label={t("panels.messages.toolRow.diff.filesAriaLabel", {
            count: parsed.files.length,
          })}
          className="flex flex-wrap gap-1"
          data-testid="tool-row-diff-files"
          role="group"
        >
          {parsed.files.map((candidate, index) => (
            <button
              aria-pressed={candidate === file}
              className={cn(CHIP, candidate === file && "bg-white/10 text-foreground")}
              key={`${candidate.path}-${index}`}
              onClick={() => setActiveFile(index)}
              type="button"
            >
              <span className="font-mono">{candidate.path}</span>
              <span className="ml-1.5 text-code-line-inserted-fg">
                {t("panels.git.diff.additions", { count: candidate.insertions })}
              </span>
              <span className="ml-1 text-code-line-deleted-fg">
                {t("panels.git.diff.deletions", { count: candidate.deletions })}
              </span>
            </button>
          ))}
        </div>
      ) : pathLabel ? (
        <span
          className="truncate font-mono app-text-11 text-muted-foreground"
          data-testid="tool-row-diff-path"
        >
          {pathLabel}
        </span>
      ) : null}
      {renderDiffBody(VIEWPORT_CLASS, "tool-row-diff-rows")}

      <ExpandedPreviewDialog
        ariaLabel={t("panels.messages.toolRow.diff.expand")}
        closeLabel={t("panels.preview.close")}
        eyebrow={t("panels.preview.eyebrow")}
        hint={t("panels.preview.hint")}
        onClose={() => setExpanded(false)}
        open={expanded}
        subtitle={`${t("panels.git.diff.additions", { count: additions })} ${t("panels.git.diff.deletions", { count: removals })}`}
        testId="tool-row-diff-expanded"
        title={pathLabel}
      >
        {/* 放大正文：同一份 renderDiffBody，只把视口从固定 320px 换成吃掉弹层剩余高度。 */}
        <div
          className="flex h-full min-h-0 flex-col gap-1.5 px-2.5 py-2"
          data-testid="tool-row-diff-expanded-body"
        >
          {renderDiffBody(EXPANDED_VIEWPORT_CLASS, "tool-row-diff-rows-expanded")}
        </div>
      </ExpandedPreviewDialog>
    </div>
  );
}

function DiffNotice({
  children,
  testId,
  tone,
}: {
  children: ReactNode;
  testId: string;
  tone: "warning" | "muted";
}) {
  return (
    <div
      className={cn(
        "rounded-chip px-2 py-1 app-text-11",
        tone === "warning"
          ? "bg-accent-gold/8 text-accent-orange"
          : "text-muted-foreground/80",
      )}
      data-testid={testId}
    >
      {children}
    </div>
  );
}
