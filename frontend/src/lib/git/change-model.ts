// Git 变更列表的纯模型（P3-4 分组扁平化 + P4-1 暂存乐观搬移）。
//
// 与 hooks/workspace/use-git-changes.ts 的分工：这一层**只做纯函数**，不碰 React 状态、
// 不发请求、不读时钟；组顺序与 porcelain 语义映射都收敛在此，便于单测（含负例）覆盖。
//
// 后端契约（types/runtime/git-browse.ts · GitFileStatus）：
//   * `status` 是 `git status --porcelain=v2` 的 XY 组合（X=暂存侧，Y=工作区侧，`.` 表示无变化）；
//   * 分组归属由后端给出（staged/unstaged/untracked/conflicts），前端只做展示与乐观镜像。

import type {
  GitChangeGroup,
  GitFileStatus,
  GitStageAction,
  GitStatusResult,
} from "@/types/runtime/git-browse";

export type GitChangeEntry = {
  path: string;
  group: GitChangeGroup;
  file: GitFileStatus;
};

/** 组顺序固定：冲突 → 已暂存 → 未暂存 → 未跟踪（列表渲染与键盘 ↑/↓ 共用同一份顺序）。 */
export const GIT_CHANGE_GROUP_ORDER: readonly GitChangeGroup[] = [
  "conflicts",
  "staged",
  "unstaged",
  "untracked",
];

/** 扁平化变更列表（组内保持后端顺序，不做二次排序）。 */
export function flattenGitChanges(status: GitStatusResult | null): GitChangeEntry[] {
  if (!status) {
    return [];
  }
  return GIT_CHANGE_GROUP_ORDER.flatMap((group) =>
    status[group].map((file) => ({ path: file.path, group, file })),
  );
}

/**
 * 乐观移动后的 porcelain XY 串：只依据「搬去哪一组」，不猜测文件内容。
 *   stage  ：未暂存 → 读 Y 侧字母（`.M` → `M.`）；未跟踪 → `A.`（index 里是新增）；
 *   unstage：已暂存 → 读 X 侧字母（`M.` → `.M`）；`A` 表示「HEAD 里没有」→ 回未跟踪（`??`）。
 */
function movedStatusString(
  status: string,
  action: GitStageAction,
  fromUntracked: boolean,
): string {
  const normalized = status.trim();
  if (action === "stage") {
    if (fromUntracked) {
      return "A.";
    }
    const y = normalized[1];
    return `${y && y !== "." && y !== "?" ? y : "M"}.`;
  }
  const x = normalized[0];
  const letter = x && x !== "." && x !== "?" ? x : "M";
  return letter === "A" ? "??" : `.${letter}`;
}

/**
 * 乐观更新：按 porcelain 语义把文件在组间搬移
 * （stage：未暂存/未跟踪 → 已暂存；unstage：已暂存 → 未暂存，`A` 回未跟踪）。
 *
 * 纪律：
 *   * 冲突组不动 —— `git add` 一个冲突文件等于「宣称冲突已解决」，必须有独立确认，不能搭车；
 *   * 只改分组归属与 `clean`：`repo`/`warnings`/`generatedAt` 等服务端字段原样保留，
 *     等真正响应（或失败回滚）覆盖，不在这里编造；
 *   * 目标组按 path 去重：同一文件可能同时出现在两侧（已暂存 + 未暂存），搬移后只能有一份。
 */
export function applyOptimisticStage(
  status: GitStatusResult,
  files: readonly string[],
  action: GitStageAction,
): GitStatusResult {
  const wanted = new Set(files);
  if (wanted.size === 0) {
    return status;
  }
  const rebuild = (
    staged: GitFileStatus[],
    unstaged: GitFileStatus[],
    untracked: GitFileStatus[],
  ): GitStatusResult => ({
    ...status,
    staged,
    unstaged,
    untracked,
    clean:
      staged.length === 0 &&
      unstaged.length === 0 &&
      untracked.length === 0 &&
      status.conflicts.length === 0,
  });

  if (action === "stage") {
    const moved: GitFileStatus[] = [];
    for (const file of status.unstaged) {
      if (wanted.has(file.path)) {
        moved.push({ ...file, status: movedStatusString(file.status, "stage", false) });
      }
    }
    for (const file of status.untracked) {
      if (wanted.has(file.path)) {
        moved.push({ ...file, status: movedStatusString(file.status, "stage", true) });
      }
    }
    const movedPaths = new Set(moved.map((file) => file.path));
    return rebuild(
      [...moved, ...status.staged.filter((file) => !movedPaths.has(file.path))],
      status.unstaged.filter((file) => !wanted.has(file.path)),
      status.untracked.filter((file) => !wanted.has(file.path)),
    );
  }

  const toUnstaged: GitFileStatus[] = [];
  const toUntracked: GitFileStatus[] = [];
  for (const file of status.staged) {
    if (!wanted.has(file.path)) {
      continue;
    }
    const next = movedStatusString(file.status, "unstage", false);
    if (next === "??") {
      toUntracked.push({ ...file, status: next });
    } else {
      toUnstaged.push({ ...file, status: next });
    }
  }
  const movedPaths = new Set([...toUnstaged, ...toUntracked].map((file) => file.path));
  return rebuild(
    status.staged.filter((file) => !wanted.has(file.path)),
    [...toUnstaged, ...status.unstaged.filter((file) => !movedPaths.has(file.path))],
    [...toUntracked, ...status.untracked.filter((file) => !movedPaths.has(file.path))],
  );
}

// ---------------------------------------------------------------------------
// 展示模型：徽标字母 / 色调类名 / 分组投影 / ↑↓ 选择移动。
//
// 收在同一模块的原因：git 变更列表与文件浏览器树行角标共用同一份映射，
// 分处两地会漂移（颜色、字母、回退口径），也会让组件文件导出非组件（`react-refresh/only-export-components`）。
// ---------------------------------------------------------------------------

/** 徽标色调分类（porcelain 字母的语义归类；异常输入落到 `unknown`）。 */
export type GitStatusTone =
  | "modify"
  | "add"
  | "delete"
  | "rename"
  | "copy"
  | "typechange"
  | "unmerged"
  | "untracked"
  | "unknown";

/** 徽标字母全集：同时是 i18n `list.badge.<letter>` 的键（`untracked` 为独立键）。 */
export const GIT_BADGE_LETTERS = [
  "M",
  "A",
  "D",
  "R",
  "C",
  "T",
  "U",
  "X",
  "untracked",
] as const;
export type GitStatusBadgeLetter = (typeof GIT_BADGE_LETTERS)[number];

export type GitStatusBadge = {
  letter: GitStatusBadgeLetter;
  tone: GitStatusTone;
};

export type GitChangeSection = {
  group: GitChangeGroup;
  files: GitFileStatus[];
};

/** 徽标色调 → 类名；文件浏览器树行角标复用同一映射（避免两处颜色漂移）。 */
export const GIT_STATUS_TONE_CLASS: Record<GitStatusTone, string> = {
  // 文字走 `-fg` 实体色（`-accent` 半透明，只留给边框 / 左轨，当文字色在亮色主题下几乎看不见）。
  add: "text-code-line-inserted-fg border-code-line-inserted-accent/40",
  modify: "text-amber-300 border-amber-300/40",
  delete: "text-code-line-deleted-fg border-code-line-deleted-accent/40",
  rename: "text-sky-300 border-sky-300/40",
  copy: "text-sky-300 border-sky-300/40",
  typechange: "text-violet-300 border-violet-300/40",
  unmerged: "text-code-line-deleted-fg border-code-line-deleted-accent/40",
  untracked: "text-muted-foreground border-white/15",
  unknown: "text-muted-foreground border-white/15",
};

const LETTER_TONE: Record<string, GitStatusTone> = {
  M: "modify",
  A: "add",
  D: "delete",
  R: "rename",
  C: "copy",
  T: "typechange",
  U: "unmerged",
};

/**
 * porcelain XY → 展示徽标。分组决定读哪一侧：staged 读 X、unstaged 读 Y，
 * 冲突组统一用 `U`（未合并），未跟踪组用独立键（`??` 不是 XY 语义）。
 */
export function gitStatusBadge(status: string, group: GitChangeGroup): GitStatusBadge {
  if (group === "conflicts") {
    return { letter: "U", tone: "unmerged" };
  }
  if (group === "untracked") {
    return { letter: "untracked", tone: "untracked" };
  }
  const normalized = status.trim();
  const preferred = group === "staged" ? normalized[0] : normalized[1];
  const fallback = group === "staged" ? normalized[1] : normalized[0];
  const letter = [preferred, fallback].find((item) => item && item !== "." && item !== "?");
  // 非 porcelain 字母（异常输入）统一降级到 "X"，避免把原始字母当 i18n 键渲染出来。
  if (!letter || !(GIT_BADGE_LETTERS as readonly string[]).includes(letter)) {
    return { letter: "X", tone: "unknown" };
  }
  return { letter: letter as GitStatusBadgeLetter, tone: LETTER_TONE[letter] ?? "unknown" };
}

/** 分组（顺序固定；空组保留，由 UI 决定是否隐藏）。 */
export function buildGitChangeSections(status: GitStatusResult | null): GitChangeSection[] {
  if (!status) {
    return [];
  }
  return [
    { group: "conflicts", files: status.conflicts },
    { group: "staged", files: status.staged },
    { group: "unstaged", files: status.unstaged },
    { group: "untracked", files: status.untracked },
  ];
}

/** ↑/↓ 选择移动（在扁平顺序内循环，越界时保持原值）。 */
export function moveGitSelection(
  entries: readonly GitChangeEntry[],
  selectedPath: string | null,
  delta: number,
): string | null {
  if (entries.length === 0) {
    return selectedPath;
  }
  const index = entries.findIndex((entry) => entry.path === selectedPath);
  if (index < 0) {
    // 当前选择不在列表（例如文件已被提交）：↓ 落到第一项，↑ 落到最后一项。
    return entries[delta >= 0 ? 0 : entries.length - 1]?.path ?? selectedPath;
  }
  const next = (index + delta + entries.length) % entries.length;
  return entries[next]?.path ?? selectedPath;
}
