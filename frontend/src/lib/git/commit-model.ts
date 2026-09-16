// 提交列表的展示模型（P4-2）：纯函数，从 components/workspace/git/commit-list.tsx 抽出。
//
// 纪律：
//   * `commit:<sha>` 目标判定只做字符串比对，不做 sha 前缀猜测（前缀猜测会把「另一个提交」显示成当前目标）；
//   * `authoredAt` 原样透传：能解析才本地化，解析失败显示原始字符串（不猜时间、不显示 NaN）。

import type { GitDiffTarget } from "@/types/runtime/git-browse";

/** `commit:<sha>` 目标判定（比对原样透传，不做 sha 前缀猜测）。 */
export function isCurrentCommitTarget(target: GitDiffTarget, sha: string): boolean {
  return typeof target === "string" && target === `commit:${sha}`;
}

/** 展示用提交时间：可解析时用本地时间，不可解析时原样显示（不猜时间、不显示 NaN）。 */
export function formatCommitDate(value: string): string {
  const trimmed = value.trim();
  if (trimmed === "") {
    return "—";
  }
  const parsed = new Date(trimmed);
  if (Number.isNaN(parsed.getTime())) {
    return trimmed;
  }
  return parsed.toLocaleString();
}
