// Git diff 视图：诚实渲染分支（解析失败 / 截断 / 二进制 / 行预算）+ unified|split + 上下文展开。
//
// 后端契约（types/runtime/git-browse.ts · GitDiffResult）：`hunks` 来自服务端解析；`raw` 为原始 diff
// 文本（复制 / 下载 .patch 的唯一来源，前端不重新拼接）；`parseError` 非空表示结构化 hunks 不可信；
// `truncated` / `truncatedReason` 为服务端截断结论；二进制结论在 `file.isBinary`，行数统计用 -1 表示不可用。
//
// 归一化纪律：parseError 非空 → 必须降级为纯文本展示 raw，并提示解析失败（**不得**显示成「无改动」）；
// truncated → 显示「已截断，仅显示前 N 行」+ truncated_reason，并提供复制 / 下载；二进制只给结论与统计；
// 行数统计 < 0 时显示「不可用」，不补 0；空白开关只改请求参数（重新取 diff），前端不做本地过滤。
//
// 降级判据：剪贴板 / Blob 下载不可用 → 明示失败原因（copyFailed / downloadFailed），不静默；
// 客户端渲染预算截断只隐藏渲染行，不改写服务端结论，必须给出「继续加载」入口。

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import {
  DIFF_ROW_HEIGHT,
  DiffGapRow,
  DiffLineRow,
  GitDiffHunk,
  RawDiffActions,
  RawDiffPane,
} from "@/components/workspace/git/diff-hunk";
import { VirtualLineList } from "@/components/workspace/diff/virtual-line-list";
import type { GitSnapshot } from "@/hooks/workspace/use-git-changes";
import {
  buildDiffRows,
  countDiffRawLines,
  DIFF_CONTEXT_EXPAND_STEP,
  DIFF_ROW_LIMIT_STEP,
  diffLanguageForPath,
  limitDiffRows,
  type DiffViewMode,
} from "@/lib/git/diff-view-model";
import { cn } from "@/lib/utils";
import type { GitDiffResult } from "@/types/runtime/git-browse";

const CHIP = "rounded-chip border border-white/10 px-2 py-0.5 app-text-11 hover:bg-white/6 disabled:opacity-50";

export type GitDiffViewProps = {
  snapshot: GitSnapshot<GitDiffResult>;
  selectedPath: string | null;
  /** 选中文件已不在当前变更列表（服务端最新结论）→ 必须提示结果可能过期。 */
  stale: boolean;
  targetLabel: string;
  mode: DiffViewMode;
  onModeChange: (mode: DiffViewMode) => void;
  whitespace: "show" | "ignore_all";
  onWhitespaceChange: (value: "show" | "ignore_all") => void;
  canExpandContext: boolean;
  onExpandContext: () => void;
  rowLimit: number;
  onShowMoreRows: () => void;
  onRetry: () => void;
};

export function GitDiffView({
  snapshot,
  selectedPath,
  stale,
  targetLabel,
  mode,
  onModeChange,
  whitespace,
  onWhitespaceChange,
  canExpandContext,
  onExpandContext,
  rowLimit,
  onShowMoreRows,
  onRetry,
}: GitDiffViewProps) {
  const { t } = useTranslation("workspace");
  const result = snapshot.data;
  const [collapsed, setCollapsed] = useState<ReadonlySet<number>>(() => new Set());

  // 切换文件 / 模式 / 对比目标：折叠状态属于「上一个视图」，必须复位（避免折叠错 hunk）。
  const resetKey = `${selectedPath ?? ""}|${mode}|${result?.target ?? ""}`;
  useEffect(() => {
    setCollapsed(new Set());
  }, [resetKey]);

  const rows = useMemo(
    () => buildDiffRows(result?.hunks ?? [], { mode, collapsedHunks: [...collapsed] }),
    [result?.hunks, mode, collapsed],
  );
  const slice = useMemo(() => limitDiffRows(rows, rowLimit), [rows, rowLimit]);
  const expandStep = canExpandContext ? DIFF_CONTEXT_EXPAND_STEP : null;
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

  if (!selectedPath) {
    return <Hint testId="git-diff-empty">{t("panels.git.diff.empty")}</Hint>;
  }

  if (snapshot.status === "error") {
    return (
      <Notice
        detail={errorDetail(snapshot.error)}
        message={snapshot.unavailable ? t("panels.git.diff.unavailable") : t("panels.git.diff.errorTitle")}
        onRetry={onRetry}
        retryLabel={t("panels.git.diff.retry")}
        testId="git-diff-error"
        title={selectedPath}
      />
    );
  }

  if (!result) {
    return <Hint testId="git-diff-loading">{t("panels.git.diff.loading")}</Hint>;
  }

  const binary = result.file?.isBinary === true;
  const raw = result.raw ?? "";
  const hasStats = result.insertions >= 0 && result.deletions >= 0;
  const statsLabel = binary
    ? t("panels.git.diff.fileStatsBinary")
    : hasStats
      ? t("panels.git.diff.fileStats", {
          insertions: String(result.insertions),
          deletions: String(result.deletions),
        })
      : t("panels.git.list.statUnknown");

  return (
    <div className="grid min-h-0 grid-rows-[auto_auto_1fr] gap-2" data-testid="git-diff-view">
      <Toolbar
        canExpandContext={canExpandContext}
        mode={mode}
        onModeChange={onModeChange}
        onToggleWhitespace={() => onWhitespaceChange(whitespace === "ignore_all" ? "show" : "ignore_all")}
        path={result.file?.path ?? selectedPath}
        statsLabel={statsLabel}
        targetLabel={targetLabel}
        whitespace={whitespace}
      />
      <div className="grid gap-1">
        {stale ? <Banner tone="warning" testId="git-diff-stale">{t("panels.git.list.stale")}</Banner> : null}
        {result.truncated ? (
          <Banner
            data={{
              "data-truncated-lines": countDiffRawLines(raw),
              "data-truncated-reason": result.truncatedReason,
            }}
            tone="warning"
            testId="git-diff-truncated"
          >
            <span>
              {t("panels.git.diff.truncatedTitle", { count: countDiffRawLines(raw) })}
            </span>
            <span className="block text-muted-foreground/80">
              {result.truncatedReason
                ? t("panels.git.diff.truncatedReason", { reason: result.truncatedReason })
                : t("panels.git.diff.truncatedNoReason")}
            </span>
            <RawDiffActions path={result.file?.path ?? selectedPath} raw={raw} />
          </Banner>
        ) : null}
        {!canExpandContext ? (
          <Banner tone="muted" testId="git-diff-context-limit">
            {t("panels.git.diff.contextLimit", { count: result.context })}
          </Banner>
        ) : null}
        {result.parseError ? (
          <Banner
            data={{ "data-parse-error": result.parseError }}
            tone="danger"
            testId="git-diff-parse-error"
          >
            <span>{t("panels.git.diff.parseErrorTitle")}</span>
            <span className="block text-muted-foreground/80">
              {t("panels.git.diff.parseErrorBody", { message: result.parseError })}
            </span>
            <RawDiffActions path={result.file?.path ?? selectedPath} raw={raw} />
          </Banner>
        ) : null}
        {binary ? (
          <Banner
            data={{ "data-binary-stats": hasStats ? "numstat" : "unavailable" }}
            tone="muted"
            testId="git-diff-binary"
          >
            <span>{t("panels.git.diff.binaryTitle")}</span>
            <span className="block text-muted-foreground/80">
              {t("panels.git.diff.binaryBody", {
                change: hasStats
                  ? t("panels.git.diff.binarySizeChange", {
                      insertions: String(result.insertions),
                      deletions: String(result.deletions),
                    })
                  : t("panels.git.diff.binarySizeUnknown"),
              })}
            </span>
          </Banner>
        ) : null}
        {!binary && result.file?.isSubmodule ? (
          <Banner tone="muted" testId="git-diff-submodule">
            {t("panels.git.diff.submodule")}
          </Banner>
        ) : null}
        {slice.hidden > 0 ? (
          <Banner
            data={{
              "data-row-limit-shown": slice.rows.length,
              "data-row-limit-total": slice.total,
            }}
            tone="muted"
            testId="git-diff-row-limit"
          >
            <span>
              {t("panels.git.diff.rowLimit", {
                shown: String(slice.rows.length),
                total: String(slice.total),
              })}
            </span>
            <button className={cn(CHIP, "mt-1 w-fit")} onClick={onShowMoreRows} type="button">
              {t("panels.git.diff.loadMoreRows", { count: DIFF_ROW_LIMIT_STEP })}
            </button>
          </Banner>
        ) : null}
      </div>
      {renderBody()}
    </div>
  );

  function renderBody(): ReactNode {
    if (!result) {
      return null;
    }
    if (result.parseError) {
      // 诚实降级：结构化 hunks 不可信，只展示后端原始文本（永不显示「无改动」）。
      return <RawDiffPane raw={result.raw ?? ""} testId="git-diff-raw" />;
    }
    if (result.file?.isBinary || result.file?.isSubmodule) {
      return null;
    }
    if (slice.rows.length === 0) {
      return <Hint testId="git-diff-no-changes">{t("panels.git.diff.noChanges")}</Hint>;
    }
    const language = diffLanguageForPath(result.file?.path ?? selectedPath ?? "");
    return (
      <VirtualLineList
        className="min-h-0 rounded-panel border border-white/8 bg-black/20"
        containerAriaLabel={t("panels.git.ariaLabel")}
        containerRole="rowgroup"
        resetKey={resetKey}
        rowHeight={DIFF_ROW_HEIGHT}
        rowKey={(row) => row.key}
        rows={slice.rows}
        testId="git-diff-rows"
        renderRow={(row) =>
          row.kind === "hunk-header" ? (
            <GitDiffHunk
              collapsed={collapsed.has(row.hunkIndex)}
              expandStep={expandStep}
              hunk={hunkAt(result, row.hunkIndex)}
              hunkIndex={row.hunkIndex}
              onExpandContext={onExpandContext}
              onToggleCollapse={toggleHunk}
            />
          ) : row.kind === "gap" && row.gap ? (
            <DiffGapRow
              expandStep={expandStep}
              gap={row.gap}
              onExpandContext={onExpandContext}
            />
          ) : (
            <DiffLineRow language={language} mode={mode} row={row} />
          )
        }
      />
    );
  }
}

function hunkAt(result: GitDiffResult, hunkIndex: number) {
  const hunk = result.hunks[hunkIndex];
  if (!hunk) {
    // 行模型与 hunks 同源，不应发生；真发生就抛错而不是渲染半截数据。
    throw new Error(`diff 行模型引用了不存在的 hunk：${hunkIndex}`);
  }
  return hunk;
}

function Toolbar({
  path,
  statsLabel,
  targetLabel,
  mode,
  onModeChange,
  whitespace,
  onToggleWhitespace,
  canExpandContext,
}: {
  path: string;
  statsLabel: string;
  targetLabel: string;
  mode: DiffViewMode;
  onModeChange: (mode: DiffViewMode) => void;
  whitespace: "show" | "ignore_all";
  onToggleWhitespace: () => void;
  canExpandContext: boolean;
}) {
  const { t } = useTranslation("workspace");
  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="min-w-0 truncate font-mono app-text-12" title={path}>
        {path}
      </span>
      <span className="shrink-0 app-text-11 text-muted-foreground">{statsLabel}</span>
      <span className="shrink-0 app-text-10 text-muted-foreground/70">{targetLabel}</span>
      <span className="ml-auto flex shrink-0 items-center gap-1" role="radiogroup" aria-label={t("panels.git.diff.modeLabel")}>
        {(["unified", "split"] as const).map((item) => (
          <button
            aria-checked={mode === item}
            className={cn(CHIP, mode === item && "border-sky-300/40 text-sky-200")}
            key={item}
            onClick={() => onModeChange(item)}
            role="radio"
            type="button"
          >
            {item === "unified" ? t("panels.git.diff.modeUnified") : t("panels.git.diff.modeSplit")}
          </button>
        ))}
      </span>
      <button
        aria-label={t("panels.git.diff.whitespaceLabel")}
        aria-pressed={whitespace === "ignore_all"}
        className={cn(CHIP, whitespace === "ignore_all" && "border-amber-300/40 text-amber-200")}
        onClick={onToggleWhitespace}
        title={t("panels.git.diff.whitespaceHint")}
        type="button"
      >
        {whitespace === "ignore_all"
          ? t("panels.git.diff.whitespaceIgnore")
          : t("panels.git.diff.whitespaceShow")}
      </button>
      {!canExpandContext ? null : <span className="sr-only">{t("panels.git.diff.expandContext", { count: DIFF_CONTEXT_EXPAND_STEP })}</span>}
    </div>
  );
}

function Hint({ children, testId }: { children: ReactNode; testId: string }) {
  return (
    <p className="px-1 py-2 app-text-12 text-muted-foreground" data-testid={testId}>
      {children}
    </p>
  );
}

function Banner({
  children,
  tone,
  testId,
  data,
}: {
  children: ReactNode;
  tone: "warning" | "danger" | "muted";
  testId: string;
  /** 诚实契约的可断言证据（原始字段与计数，便于单测直接核对 DOM）。 */
  data?: Record<string, string | number>;
}) {
  return (
    <div
      className={cn(
        "rounded-panel border px-2 py-1.5 app-text-11",
        tone === "warning" && "border-amber-300/40 bg-amber-300/[0.06] text-amber-100",
        tone === "danger" && "border-code-line-deleted-accent/40 bg-code-line-deleted-bg/40 text-foreground",
        tone === "muted" && "border-white/8 bg-white/[0.03] text-muted-foreground",
      )}
      data-testid={testId}
      {...data}
      role={tone === "muted" ? undefined : "status"}
    >
      {children}
    </div>
  );
}

function Notice({
  title,
  message,
  detail,
  onRetry,
  retryLabel,
  testId,
}: {
  title: string;
  message: string;
  detail: string;
  onRetry: () => void;
  retryLabel: string;
  testId: string;
}) {
  return (
    <div className="grid gap-1 px-1 py-2" data-testid={testId}>
      <p className="truncate app-text-12" title={title}>
        {title}
      </p>
      <p className="app-text-12 text-muted-foreground">{message}</p>
      {detail ? (
        <p className="truncate font-mono app-text-10 text-muted-foreground/70" title={detail}>
          {detail}
        </p>
      ) : null}
      <button className={cn(CHIP, "w-fit")} onClick={onRetry} type="button">
        {retryLabel}
      </button>
    </div>
  );
}

function errorDetail(error: unknown): string {
  if (!error) {
    return "";
  }
  return error instanceof Error ? error.message : String(error);
}
