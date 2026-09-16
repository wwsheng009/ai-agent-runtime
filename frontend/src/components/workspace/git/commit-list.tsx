// 提交历史列表（只读）：展示 sha / 主题 / 作者 / 引用，并允许把某个提交设为「对比目标」。
//
// 后端契约（types/runtime/git-browse.ts · GitCommitsResult）：`commits` 已按时间倒序；
// `nextCursor` 为下一页游标（null = 没有下一页），`hasMore` 是服务端结论；翻页由 hook 用游标请求。
//
// 归一化纪律：`authoredAt` 原样透传（解析失败时显示原始字符串，不猜时间）；`refs` 为空则不显示引用行；
// 「设为对比目标」只是把 target 切成 `commit:<sha>`（只读对比），不代表切换分支 / 检出。
//
// 降级判据：提交请求失败 / 仓库不可用 → 可解释空态 + 重试；`hasMore` 但无游标 → 按钮禁用（不静默重发首屏）。

import { useTranslation } from "react-i18next";

import type { GitSnapshot } from "@/hooks/workspace/use-git-changes";
import { formatCommitDate, isCurrentCommitTarget } from "@/lib/git/commit-model";
import { cn } from "@/lib/utils";
import type { GitCommit, GitCommitsResult, GitDiffTarget } from "@/types/runtime/git-browse";

const CHIP = "rounded-chip border border-white/10 px-2 py-0.5 app-text-11 hover:bg-white/6 disabled:opacity-50";

export type GitCommitListProps = {
  snapshot: GitSnapshot<GitCommitsResult>;
  loadingMore: boolean;
  onLoadMore: () => void;
  onRetry: () => void;
  target: GitDiffTarget;
  onUseAsTarget: (sha: string) => void;
};

export function GitCommitList({
  snapshot,
  loadingMore,
  onLoadMore,
  onRetry,
  target,
  onUseAsTarget,
}: GitCommitListProps) {
  const { t } = useTranslation("workspace");
  const result = snapshot.data;

  if (snapshot.status === "error") {
    return (
      <div className="grid gap-1 px-1 py-2" data-testid="git-commit-list-error">
        <p className="app-text-12 text-muted-foreground">
          {snapshot.unavailable
            ? t("panels.git.commits.unavailable")
            : t("panels.git.commits.errorTitle")}
        </p>
        <button className={cn(CHIP, "w-fit")} onClick={onRetry} type="button">
          {t("panels.git.commits.retry")}
        </button>
      </div>
    );
  }

  if (!result) {
    return (
      <p className="px-1 py-2 app-text-12 text-muted-foreground" data-testid="git-commit-list-loading">
        {t("panels.git.commits.loading")}
      </p>
    );
  }

  if (result.commits.length === 0) {
    return (
      <p className="px-1 py-2 app-text-12 text-muted-foreground" data-testid="git-commit-list-empty">
        {t("panels.git.commits.empty")}
      </p>
    );
  }

  return (
    <div className="grid min-h-0 gap-2" data-testid="git-commit-list">
      <span className="app-text-11 font-medium text-muted-foreground">
        {t("panels.git.commits.title")}
      </span>
      <div className="app-scrollbar min-h-0 overflow-auto" role="list">
        {result.commits.map((commit) => (
          <CommitRow
            commit={commit}
            key={commit.sha}
            onUseAsTarget={onUseAsTarget}
            selected={isCurrentCommitTarget(target, commit.sha)}
          />
        ))}
      </div>
      {result.hasMore || loadingMore ? (
        <button
          className={cn(CHIP, "w-fit")}
          disabled={loadingMore || !result.nextCursor}
          onClick={onLoadMore}
          type="button"
        >
          {loadingMore ? t("panels.git.commits.loadingMore") : t("panels.git.commits.loadMore")}
        </button>
      ) : null}
    </div>
  );
}

function CommitRow({
  commit,
  selected,
  onUseAsTarget,
}: {
  commit: GitCommit;
  selected: boolean;
  onUseAsTarget: (sha: string) => void;
}) {
  const { t } = useTranslation("workspace");
  return (
    <div
      aria-label={t("panels.git.commits.optionAria", {
        sha: commit.shortSha,
        subject: commit.subject,
      })}
      className={cn("flex items-start gap-2 px-2 py-1", selected && "bg-white/[0.07]")}
      data-git-commit={commit.sha}
      role="listitem"
    >
      <span className="shrink-0 font-mono app-text-11 text-muted-foreground">{commit.shortSha}</span>
      <span className="min-w-0 flex-1">
        <span className="block truncate app-text-12" title={commit.subject}>
          {commit.subject}
        </span>
        <span className="block truncate app-text-10 text-muted-foreground/70">
          {t("panels.git.commits.byline", {
            author: commit.author,
            date: formatCommitDate(commit.authoredAt),
          })}
        </span>
        {commit.refs.length > 0 ? (
          <span className="block truncate app-text-10 text-sky-200/80">
            {t("panels.git.commits.refs", { refs: commit.refs.join(", ") })}
          </span>
        ) : null}
      </span>
      <button
        className={cn(CHIP, "shrink-0", selected && "border-sky-300/40 text-sky-200")}
        onClick={() => onUseAsTarget(commit.sha)}
        type="button"
      >
        {selected ? t("panels.git.commits.currentTarget") : t("panels.git.commits.useAsTarget")}
      </button>
    </div>
  );
}

