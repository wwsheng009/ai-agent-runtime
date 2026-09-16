// 文件浏览器 × Git 状态角标（规划文档 §4.3 / Q6）：把 `/git/status` 的仓库相对路径映射成
// 「作用域相对路径 → 徽标来源」，供树行显示角标。
//
// 为什么需要映射：git 给的 path 相对**仓库根**，而文件浏览器里的 `FsEntry.path` 相对**作用域根**。
// 两者只有在同一目录时才相等，否则必须按祖先关系补/去前缀；无法建立关系时返回 null（整体跳过角标，
// 而不是猜一个可能错的归属）。
//
// 归一化纪律：
//   * 只处理绝对路径（`E:/…` 或 `/…`）；作用域根缺失（例如降级兜底没有绝对路径）时返回 null；
//   * Windows 盘符路径按大小写不敏感比较，POSIX 路径按大小写敏感（不把 Posix 当成 Windows 猜）；
//   * 同一路径出现在多个分组时按固定优先级取一条（冲突 > 未暂存 > 已暂存 > 未跟踪），不合并成假状态；
//   * 落在作用域之外的 git 变更直接丢弃（不越界映射）。
import { normalizeRelativePath } from "@/lib/file-browser/path-utils";

import type { GitFileStatus, GitChangeGroup } from "@/types/runtime/git-browse";

/** 徽标来源分组（与 `/git/status` 的四类分组一一对应）。 */
export type GitBadgeGroup = Extract<GitChangeGroup, "conflicts" | "staged" | "unstaged" | "untracked">;

export type GitBadgeHit = {
  group: GitBadgeGroup;
  /** porcelain XY 原文（由调用方交给 `gitStatusBadge` 决定字母/色调）。 */
  status: string;
};

export type GitBadgeIndex = ReadonlyMap<string, GitBadgeHit>;

export type GitBadgeInput = {
  /** 作用域绝对根（`FsRoot.path` / 会话工作目录）；缺失或非绝对路径 → 不做映射。 */
  scopeRootPath?: string | null;
  /** `/git/status` 返回的仓库根；缺失（非仓库）→ 不做映射。 */
  repoRoot?: string | null;
  groups: Partial<Record<GitBadgeGroup, readonly GitFileStatus[]>>;
};

/** 分组优先级：工作区待处理的改动优先于已暂存/未跟踪（用户下一步要看的是前者）。 */
const GROUP_PRIORITY: readonly GitBadgeGroup[] = ["conflicts", "unstaged", "staged", "untracked"];

function toPosixAbsolute(path: string): string {
  return path.trim().replace(/\\/g, "/").replace(/\/+$/, "");
}

function isAbsolutePath(path: string): boolean {
  return /^[a-zA-Z]:\//.test(path) || path.startsWith("/");
}

function isCaseInsensitive(path: string): boolean {
  return /^[a-zA-Z]:\//.test(path);
}

/** `parent` 是 `child` 的祖先时返回相对段（相等返回 ""），否则 null。 */
function relativeFrom(parent: string, child: string, caseInsensitive: boolean): string | null {
  const left = caseInsensitive ? parent.toLowerCase() : parent;
  const right = caseInsensitive ? child.toLowerCase() : child;
  if (left === right) {
    return "";
  }
  if (!right.startsWith(`${left}/`)) {
    return null;
  }
  return child.slice(parent.length + 1);
}

export function buildGitBadgeIndex(input: GitBadgeInput): GitBadgeIndex | null {
  const scopeRoot = toPosixAbsolute(input.scopeRootPath ?? "");
  const repoRoot = toPosixAbsolute(input.repoRoot ?? "");
  if (!isAbsolutePath(scopeRoot) || !isAbsolutePath(repoRoot)) {
    return null;
  }
  const caseInsensitive = isCaseInsensitive(repoRoot) && isCaseInsensitive(scopeRoot);
  // 作用域在仓库内 → 去掉 git path 的前缀；仓库在作用域内 → 给 git path 加前缀。
  const scopeUnderRepo = relativeFrom(repoRoot, scopeRoot, caseInsensitive);
  const repoUnderScope = relativeFrom(scopeRoot, repoRoot, caseInsensitive);
  if (scopeUnderRepo === null && repoUnderScope === null) {
    return null;
  }
  const stripPrefix = scopeUnderRepo === null ? null : scopeUnderRepo ? `${scopeUnderRepo}/` : "";
  const addPrefix = repoUnderScope === null ? "" : repoUnderScope ? `${repoUnderScope}/` : "";

  const index = new Map<string, GitBadgeHit>();
  for (const group of GROUP_PRIORITY) {
    for (const file of input.groups[group] ?? []) {
      const gitPath = toPosixAbsolute(file.path).replace(/^\/+/, "");
      if (!gitPath) {
        continue;
      }
      let relative: string;
      if (stripPrefix === null) {
        relative = `${addPrefix}${gitPath}`;
      } else {
        const comparison = caseInsensitive ? gitPath.toLowerCase() : gitPath;
        const prefix = caseInsensitive ? stripPrefix.toLowerCase() : stripPrefix;
        if (prefix && !comparison.startsWith(prefix)) {
          continue;
        }
        relative = gitPath.slice(stripPrefix.length);
      }
      const key = normalizeRelativePath(relative);
      if (!key || index.has(key)) {
        continue;
      }
      index.set(key, { group, status: file.status });
    }
  }
  return index;
}
