// 变更文件列表：按「冲突 → 已暂存 → 未暂存 → 未跟踪」分组展示 + 状态徽标 + ↑/↓ 切文件
// + 每行 / 每组暂存与取消暂存（P4-1，写操作只发意图，成败由 hook 判定）。
//
// 后端契约（types/runtime/git-browse.ts · GitFileStatus）：
//   * `status` 是 `git status --porcelain=v2` 的 XY 组合（X=暂存侧，Y=工作区侧，`.` 表示无变化）；
//   * `insertions/deletions` 为 -1 表示二进制（numstat 的 `-`），不能用 0 伪装；
//   * `from` 只在重命名记录上存在；`binary` 是后端给定结论，前端不自行探测文件内容。
//
// 归一化纪律：
//   * 分组顺序固定（与 hooks/workspace/use-git-changes.ts 的扁平顺序一致），组内保持后端顺序；
//   * 每行徽标只取**本组对应侧**的字母：staged 读 X，unstaged 读 Y，缺侧（`.`）再回退另一侧；
//   * 统计列只在两个数值都 ≥ 0 时显示；否则显示「行数统计不可用」，不补 0。
//
// 降级判据：
//   * 状态请求失败 / 仓库不可用 → 分组列表整体让位给可解释空态 + 重试；
//   * 过滤只作用于已加载数据，不做远端检索声明（避免「过滤即全量」的假象）；
//   * 写操作在途（stagePending）或该组没有可操作文件时，暂存按钮禁用 —— 不排队、不重放；
//   * 冲突组不提供暂存按钮（`git add` 冲突文件 = 宣称冲突已解决，需另行确认）。

import { CheckIcon, FilterIcon, PlusIcon, UndoIcon } from "lucide-react";
import { useMemo, useState, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";

import type { GitSnapshot } from "@/hooks/workspace/use-git-changes";
import { flattenGitChanges } from "@/hooks/workspace/use-git-changes";
import { describeError } from "@/lib/errors";
import {
  buildGitChangeSections,
  gitStatusBadge,
  GIT_STATUS_TONE_CLASS,
  moveGitSelection,
} from "@/lib/git/change-model";
import { cn } from "@/lib/utils";
import type {
  GitChangeGroup,
  GitFileStatus,
  GitStageAction,
  GitStatusResult,
} from "@/types/runtime/git-browse";

export type GitChangeListProps = {
  status: GitSnapshot<GitStatusResult>;
  selectedPath: string | null;
  onSelect: (path: string) => void;
  onRetry: () => void;
  /** 缺省 = 只读模式（不渲染任何写入口）。 */
  onStage?: (files: string[], action: GitStageAction) => void;
  /** 写操作在途时禁用全部暂存按钮（hook 侧还有单飞兜底）。 */
  stagePending?: boolean;
  /** 在途文件（用于逐行禁用 / 文案切换）。 */
  stagingPaths?: readonly string[];
};

/** 每组的写操作：已暂存 → 取消暂存；未暂存 / 未跟踪 → 暂存；冲突组不提供（需另行确认）。 */
function groupStageAction(group: GitChangeGroup): GitStageAction | null {
  if (group === "staged") {
    return "unstage";
  }
  return group === "unstaged" || group === "untracked" ? "stage" : null;
}

export function GitChangeList({
  status,
  selectedPath,
  onSelect,
  onRetry,
  onStage,
  stagePending = false,
  stagingPaths = [],
}: GitChangeListProps) {
  const { t } = useTranslation("workspace");
  const [filter, setFilter] = useState("");
  const query = filter.trim().toLowerCase();
  const staging = useMemo(() => new Set(stagingPaths), [stagingPaths]);

  const entries = useMemo(() => flattenGitChanges(status.data), [status.data]);
  const sections = useMemo(() => {
    return buildGitChangeSections(status.data)
      .map((section) => ({
        ...section,
        files: section.files.filter(
          (file) => !query || file.path.toLowerCase().includes(query),
        ),
      }))
      .filter((section) => section.files.length > 0);
  }, [status.data, query]);
  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") {
      return;
    }
    event.preventDefault();
    const next = moveGitSelection(
      entries,
      selectedPath,
      event.key === "ArrowDown" ? 1 : -1,
    );
    if (next && next !== selectedPath) {
      onSelect(next);
    }
  };

  return (
    <div className="grid min-h-0 gap-2" data-testid="git-change-list">
      <div className="flex items-center gap-2">
        <span className="app-text-11 font-medium text-muted-foreground">
          {t("panels.git.list.title")}
        </span>
        <span className="app-text-10 text-muted-foreground/70">
          {t("panels.git.list.keyboardHint")}
        </span>
        <span className="ml-auto app-text-10 text-muted-foreground/70">
          {status.data ? t("panels.git.repo.clean") : ""}
        </span>
      </div>
      <label className="flex items-center gap-1.5 rounded-control border border-white/8 bg-white/[0.03] px-2 py-1">
        <FilterIcon className="shrink-0 text-muted-foreground" size={13} />
        <input
          aria-label={t("panels.git.list.filterAriaLabel")}
          className="min-w-0 flex-1 bg-transparent app-text-12 outline-none placeholder:text-muted-foreground/60"
          onChange={(event) => setFilter(event.target.value)}
          placeholder={t("panels.git.list.filterPlaceholder")}
          type="text"
          value={filter}
        />
      </label>
      {status.status === "error" ? (
        <ErrorState
          message={status.unavailable ? t("panels.git.list.unavailable") : t("panels.git.list.errorTitle")}
          onRetry={onRetry}
          detail={describeError(status.error)}
          testId="git-change-list-error"
        />
      ) : status.status === "loading" && !status.data ? (
        <p className="px-1 py-2 app-text-12 text-muted-foreground">{t("panels.git.list.loading")}</p>
      ) : sections.length === 0 ? (
        <p className="px-1 py-2 app-text-12 text-muted-foreground" data-testid="git-change-list-empty">
          {query ? t("panels.git.list.noMatch") : t("panels.git.list.empty")}
          {!query && status.data?.clean ? ` ${t("panels.git.list.emptyClean")}` : ""}
        </p>
      ) : (
        <div
          aria-label={t("panels.git.list.title")}
          className="app-scrollbar min-h-0 overflow-auto outline-none"
          data-testid="git-change-list-rows"
          onKeyDown={handleKeyDown}
          role="listbox"
          tabIndex={0}
        >
          {sections.map((section) => (
            <div key={section.group} data-git-group={section.group}>
              <div className="sticky top-0 z-1 flex items-center gap-2 bg-surface-softer/95 px-2 py-1 app-text-10 uppercase tracking-wide text-muted-foreground backdrop-blur">
                <span>{t(`panels.git.list.groups.${section.group}`)}</span>
                <span className="text-muted-foreground/60">
                  {t("panels.git.list.groupCount", { count: section.files.length })}
                </span>
                {onStage && groupStageAction(section.group) ? (
                  <StageButton
                    action={groupStageAction(section.group) as GitStageAction}
                    className="ml-auto"
                    disabled={stagePending}
                    files={section.files.map((file) => file.path)}
                    label={
                      section.group === "staged"
                        ? t("panels.git.list.unstageAll")
                        : t("panels.git.list.stageAll")
                    }
                    onStage={onStage}
                    title={t("panels.git.list.stageAllHint")}
                  />
                ) : null}
              </div>
              {section.files.map((file) => (
                <ChangeRow
                  file={file}
                  group={section.group}
                  key={`${section.group}:${file.path}`}
                  onStage={onStage}
                  onSelect={onSelect}
                  selected={file.path === selectedPath}
                  stageAction={onStage ? groupStageAction(section.group) : null}
                  stagePending={stagePending || staging.has(file.path)}
                />
              ))}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function ChangeRow({
  file,
  group,
  selected,
  onSelect,
  stageAction,
  stagePending,
  onStage,
}: {
  file: GitFileStatus;
  group: GitChangeGroup;
  selected: boolean;
  onSelect: (path: string) => void;
  stageAction: GitStageAction | null;
  stagePending: boolean;
  onStage?: (files: string[], action: GitStageAction) => void;
}) {
  const { t } = useTranslation("workspace");
  const badge = gitStatusBadge(file.status, group);
  const hasStats = file.insertions >= 0 && file.deletions >= 0;

  return (
    <div
      aria-label={t("panels.git.list.rowAria", {
        path: file.path,
        badge: t(`panels.git.list.badge.${badge.letter}`),
        group: t(`panels.git.list.groups.${group}`),
      })}
      aria-selected={selected}
      className={cn(
        "flex w-full cursor-pointer items-center gap-2 px-2 py-1 text-left hover:bg-white/5",
        selected && "bg-white/[0.07]",
      )}
      data-git-path={file.path}
      onClick={() => onSelect(file.path)}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          onSelect(file.path);
        }
      }}
      role="option"
      tabIndex={selected ? 0 : -1}
    >
      <span
        className={cn(
          "flex size-4 shrink-0 items-center justify-center rounded-[4px] border font-mono app-text-10",
          GIT_STATUS_TONE_CLASS[badge.tone],
        )}
      >
        {badge.letter === "untracked" ? "?" : badge.letter}
      </span>
      <span className="min-w-0 flex-1">
        <span className="flex items-center gap-1">
          {selected ? (
            <CheckIcon className="shrink-0 text-sky-300" size={12} />
          ) : null}
          <span className="min-w-0 truncate font-mono app-text-12" title={file.path}>
            {file.path}
          </span>
        </span>
        {file.from ? (
          <span className="block truncate app-text-10 text-muted-foreground/70">
            {t("panels.git.list.renamedFrom", { path: file.from })}
          </span>
        ) : null}
      </span>
      <span className="shrink-0 font-mono app-text-10 text-muted-foreground/80">
        {file.binary
          ? t("panels.git.list.binary")
          : hasStats
            ? t("panels.git.list.stats", {
                insertions: String(file.insertions),
                deletions: String(file.deletions),
              })
            : t("panels.git.list.statUnknown")}
      </span>
      {stageAction && onStage ? (
        <StageButton
          action={stageAction}
          disabled={stagePending}
          files={[file.path]}
          label={
            stagePending
              ? t("panels.git.list.stagePending")
              : stageAction === "unstage"
                ? t("panels.git.list.unstage")
                : t("panels.git.list.stage")
          }
          onStage={onStage}
          title={t("panels.git.list.stageHint")}
        />
      ) : null}
    </div>
  );
}

/**
 * 写操作按钮（行内 / 组头共用）。要点：
 *   * 点击不冒泡 —— 否则会顺带切换选中文件（行是 role=option，点击即选择）；
 *   * 在途时禁用并显示「暂存中…」文案，不做乐观文案（成败由服务端结论决定）。
 */
function StageButton({
  action,
  files,
  label,
  title,
  disabled,
  className,
  onStage,
}: {
  action: GitStageAction;
  files: string[];
  label: string;
  title: string;
  disabled: boolean;
  className?: string;
  onStage: (files: string[], action: GitStageAction) => void;
}) {
  const Icon = action === "unstage" ? UndoIcon : PlusIcon;
  return (
    <button
      aria-label={`${label} ${files.length > 1 ? files.length : files[0] ?? ""}`.trim()}
      className={cn(
        "flex shrink-0 items-center gap-0.5 rounded-chip border border-white/10 px-1.5 py-0.5 app-text-10 text-muted-foreground hover:bg-white/6 hover:text-foreground disabled:opacity-40 disabled:hover:bg-transparent",
        className,
      )}
      disabled={disabled}
      onClick={(event) => {
        event.stopPropagation();
        onStage(files, action);
      }}
      title={title}
      type="button"
    >
      <Icon size={11} />
      <span>{label}</span>
    </button>
  );
}

function ErrorState({
  message,
  detail,
  onRetry,
  testId,
}: {
  message: string;
  detail: string;
  onRetry: () => void;
  testId: string;
}) {
  const { t } = useTranslation("workspace");
  return (
    <div className="grid gap-1 px-1 py-2" data-testid={testId}>
      <p className="app-text-12 text-muted-foreground">{message}</p>
      {detail ? (
        <p className="truncate font-mono app-text-10 text-muted-foreground/70" title={detail}>
          {detail}
        </p>
      ) : null}
      <button
        className="w-fit rounded-chip border border-white/10 px-2 py-0.5 app-text-11 hover:bg-white/6"
        onClick={onRetry}
        type="button"
      >
        {t("panels.git.list.retry")}
      </button>
    </div>
  );
}

