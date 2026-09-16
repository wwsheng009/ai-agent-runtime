// 工作区右侧栏「Git 变更面」出口（规划文档 §4.6 / 阶段 P3-4、P3-5、P3-7）：只读 diff 浏览。
//
// 后端契约（api/runtime/git.ts）：/git/status、/git/diff、/git/commits 三个只读 GET，作用域用
// `use-git-changes` 解析出的 scope（会话工作目录优先，失败时退到兜底根）；本面只做展示与请求编排。
//
// 归一化纪律：状态 / diff / 提交三份数据各自带 loading / error / unavailable，互不掩盖；对比目标与空白
// 开关都通过 hook 重新请求（服务端结论）；键盘 ↑/↓ 切文件复用列表的扁平顺序（不在 UI 里另排序）。
//
// 降级判据：作用域不可用 → 不发起任何请求，给可解释提示；仓库缺失 / git 不可用 → 各区块独立空态 + 重试；
// P4-1 起提供暂存 / 取消暂存（写操作只发意图，成败由 hook 以服务端结论判定）；**本期不提供提交**。

import { GitBranchIcon, RefreshCwIcon } from "lucide-react";
import { useState, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";

import { GitChangeList } from "@/components/workspace/git/change-list";
import { GitCommitList } from "@/components/workspace/git/commit-list";
import { GitDiffView } from "@/components/workspace/git/diff-view";
import { useGitChanges } from "@/hooks/workspace/use-git-changes";
import { describeError } from "@/lib/errors";
import { moveGitSelection } from "@/lib/git/change-model";
import type { DiffViewMode } from "@/lib/git/diff-view-model";
import { cn } from "@/lib/utils";
import type { GitDiffTarget } from "@/types/runtime/git-browse";

// 统一样式口径：全部走主题 token（border-border / bg-surface / text-muted-foreground），
// 不散落 `white/10`、`sky-300`、`amber-200` 这类「就地发明」的颜色，避免同一面板里
// 三种边框亮度和两种强调色混用。
const CHIP =
  "flex items-center gap-1 rounded-chip border border-border/60 bg-surface/50 px-2 py-0.5 app-text-11 " +
  "text-muted-foreground transition-colors hover:bg-white/6 hover:text-foreground disabled:opacity-40";
const TAB =
  "-mb-px border-b-2 border-transparent px-2.5 py-1 app-text-11 text-muted-foreground transition-colors hover:text-foreground";
const PANE = "min-h-0 overflow-hidden rounded-card border border-border/60 bg-surface/30";

export function GitSurface({
  sessionId,
  workspacePath,
}: {
  sessionId: string;
  workspacePath?: string;
}) {
  const { t } = useTranslation("workspace");
  const [view, setView] = useState<"changes" | "commits">("changes");
  const [mode, setMode] = useState<DiffViewMode>("unified");
  const git = useGitChanges({ sessionId, workspacePath });
  const repo = git.status.data?.repo ?? null;
  const scopeUnknown = sessionId.trim() === "" && (workspacePath ?? "").trim() === "";
  const warnings = git.status.data?.warnings ?? [];

  const targetLabel =
    git.target === "working"
      ? t("panels.git.repo.targetWorking")
      : git.target === "staged"
        ? t("panels.git.repo.targetStaged")
        : t("panels.git.repo.targetCommit", { sha: git.target.slice("commit:".length).slice(0, 7) });

  const useCommitAsTarget = (sha: string) => {
    git.setTarget(`commit:${sha}` as GitDiffTarget);
    setView("changes");
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") {
      return;
    }
    event.preventDefault();
    setView("changes");
    const next = moveGitSelection(
      git.entries,
      git.selectedPath,
      event.key === "ArrowDown" ? 1 : -1,
    );
    if (next && next !== git.selectedPath) {
      git.selectFile(next);
    }
  };

  return (
    <div
      aria-label={t("panels.git.ariaLabel")}
      className="grid h-full min-h-0 grid-rows-[auto_auto_minmax(0,1fr)] gap-2.5 overflow-hidden p-3 outline-none"
      data-testid="git-surface"
      onKeyDown={handleKeyDown}
      role="region"
      tabIndex={0}
    >
      <header className="grid gap-2 rounded-card border border-border/60 bg-surface/40 p-2.5">
        <div className="flex flex-wrap items-center gap-2">
          <span className="flex min-w-0 items-center gap-1.5 font-mono app-text-12">
            <GitBranchIcon aria-hidden className="size-3.5 shrink-0 text-muted-foreground" />
            {repo
              ? repo.detached
                ? t("panels.git.repo.detached")
                : t("panels.git.repo.branch", { branch: repo.branch })
              : t("panels.git.ariaLabel")}
          </span>
          {repo && repo.ahead > 0 ? (
            <span className="app-text-10 text-muted-foreground">
              {t("panels.git.repo.ahead", { count: repo.ahead })}
            </span>
          ) : null}
          {repo && repo.behind > 0 ? (
            <span className="app-text-10 text-muted-foreground">
              {t("panels.git.repo.behind", { count: repo.behind })}
            </span>
          ) : null}
          {git.status.data?.clean ? (
            <span className="app-text-10 text-muted-foreground/70">
              {t("panels.git.repo.clean")}
            </span>
          ) : null}
          {repo ? (
            <span
              className="min-w-0 truncate app-text-10 text-muted-foreground/60"
              title={t("panels.git.repo.root", { path: repo.root })}
            >
              {repo.root}
            </span>
          ) : null}
          <span className="ml-auto flex shrink-0 items-center gap-1">
            {warnings.length > 0 ? (
              <span
                className="rounded-chip border border-accent-gold/40 px-1.5 app-text-10 text-accent-gold"
                title={t("panels.git.repo.warningsTitle")}
              >
                {t("panels.git.repo.warnings", { count: warnings.length })}
              </span>
            ) : null}
            <button
              className={CHIP}
              onClick={() => {
                git.reloadStatus();
                git.reloadCommits();
              }}
              title={t("panels.git.repo.refresh")}
              type="button"
            >
              <RefreshCwIcon aria-hidden className="size-3" />
              {t("panels.git.repo.refresh")}
            </button>
          </span>
        </div>
        <p className="app-text-10 text-muted-foreground/70">{t("panels.git.writeNote")}</p>
        {git.stageError ? (
          <p
            className="flex items-start gap-1 rounded-control border border-accent-gold/40 bg-accent-gold/10 px-2 py-1 app-text-11 text-accent-gold"
            data-testid="git-stage-error"
            role="alert"
          >
            <span className="min-w-0">
              {t("panels.git.writeFailed", { message: describeError(git.stageError) })}
            </span>
            <button
              aria-label={t("panels.git.writeDismiss")}
              className="shrink-0 underline underline-offset-2"
              onClick={git.clearStageError}
              type="button"
            >
              {t("panels.git.writeDismiss")}
            </button>
          </p>
        ) : null}
        {scopeUnknown ? (
          <p
            className="rounded-control border border-accent-gold/40 bg-accent-gold/10 px-2 py-1 app-text-11 text-accent-gold"
            data-testid="git-scope-missing"
            role="status"
          >
            {t("panels.git.scope.missing")}
          </p>
        ) : git.scope === "" ? (
          <p className="app-text-11 text-muted-foreground" data-testid="git-scope-pending">
            {t("panels.git.scope.pending")}
          </p>
        ) : (
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="app-text-10 text-muted-foreground/70">
              {t("panels.git.repo.targetLabel")}
            </span>
            {/* 分段控件：两个互斥目标放同一容器里，避免被当成两个独立开关。 */}
            <span className="inline-flex items-center gap-0.5 rounded-control border border-border/60 bg-surface/50 p-0.5">
              {(
                [
                  ["working", t("panels.git.repo.targetWorking")],
                  ["staged", t("panels.git.repo.targetStaged")],
                ] as const
              ).map(([value, label]) => (
                <button
                  aria-pressed={git.target === value}
                  className={cn(
                    "rounded-[6px] px-2 py-0.5 app-text-11 transition-colors",
                    git.target === value
                      ? "bg-white/8 text-foreground"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                  key={value}
                  onClick={() => git.setTarget(value)}
                  type="button"
                >
                  {label}
                </button>
              ))}
            </span>
            {git.target.startsWith("commit:") ? (
              <>
                <span className="rounded-chip border border-border/60 bg-surface/50 px-1.5 py-0.5 font-mono app-text-11 text-foreground">
                  {targetLabel}
                </span>
                <button
                  className={CHIP}
                  onClick={() => git.setTarget("working")}
                  type="button"
                >
                  {t("panels.git.repo.clearCommit")}
                </button>
              </>
            ) : null}
          </div>
        )}
        {warnings.length > 0 ? (
          <ul className="grid gap-0.5" data-testid="git-warnings">
            {warnings.map((warning, index) => (
              <li
                className="rounded-control bg-accent-gold/8 px-2 py-0.5 font-mono app-text-10 text-accent-gold/90"
                key={`${index}-${warning}`}
              >
                {warning}
              </li>
            ))}
          </ul>
        ) : null}
      </header>

      <div className="flex items-center gap-1 border-b border-border/60" role="tablist">
        {(
          [
            ["changes", "panels.git.views.changes"],
            ["commits", "panels.git.views.commits"],
          ] as const
        ).map(([value, key]) => (
          <button
            aria-selected={view === value}
            className={cn(
              TAB,
              view === value
                ? "border-foreground/50 text-foreground"
                : undefined,
            )}
            key={value}
            onClick={() => setView(value)}
            role="tab"
            type="button"
          >
            {t(key)}
          </button>
        ))}
      </div>

      <div className="grid min-h-0 grid-rows-[minmax(0,40%)_minmax(0,60%)] gap-2">
        {view === "changes" ? (
          <>
            <div className={PANE}>
              <GitChangeList
                onRetry={git.reloadStatus}
                onSelect={git.selectFile}
                onStage={git.stageFiles}
                selectedPath={git.selectedPath}
                stagePending={git.stagePending}
                stagingPaths={git.stagingPaths}
                status={git.status}
              />
            </div>
            <div className={PANE}>
              <GitDiffView
                canExpandContext={git.canExpandContext}
                mode={mode}
                onExpandContext={() => git.expandContext()}
                onModeChange={setMode}
                onRetry={git.retryDiff}
                onShowMoreRows={git.showMoreRows}
                onWhitespaceChange={git.setWhitespace}
                rowLimit={git.rowLimit}
                selectedPath={git.selectedPath}
                snapshot={git.diff}
                stale={git.selectedPath !== null && !git.selectedStillChanged}
                targetLabel={targetLabel}
                whitespace={git.whitespace}
              />
            </div>
          </>
        ) : (
          <div className={cn(PANE, "app-scrollbar overflow-auto")}>
            <GitCommitList
              loadingMore={git.commitsLoadingMore}
              onLoadMore={git.loadMoreCommits}
              onRetry={git.reloadCommits}
              onUseAsTarget={useCommitAsTarget}
              snapshot={git.commits}
              target={git.target}
            />
          </div>
        )}
      </div>
    </div>
  );
}
