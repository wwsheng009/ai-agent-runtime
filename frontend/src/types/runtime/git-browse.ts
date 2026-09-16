// Git 只读浏览（P3）+ 暂存写操作（P4-1）的运行时类型契约。
//
// 后端契约（backend/internal/gitbrowse + api/skills/git_browse_handlers.go）：
//   GET  /git/status?scope=…&path=…     仓库状态 + 分组变更 + numstat
//   GET  /git/diff?scope=…&path=…&file=…&target=working|staged|commit:<sha>
//   GET  /git/commits?scope=…&path=…&limit=50&cursor=<sha>
//   POST /git/stage  {scope,path,action:stage|unstage,files:[…]} → 更新后的 status 摘要
//
// 归一化纪律：
//   * 状态码沿用 `git status --porcelain=v2` 的 XY 语义（前端只做展示映射）；
//   * `insertions/deletions` 为 -1 表示「行数统计不可用」：numstat 的 `-`（二进制），
//     或未跟踪文件读不到结论（非常规文件 / 超大 / 读取失败，后端同时给 warnings），不用 0 伪装；
//   * 未跟踪文件（target=working）的 diff 由服务端用 `--no-index /dev/null <file>` 合成，
//     `file.status` 为 `A`（空的新文件同样为 `A` 且 hunks 为空 → 前端显示「新增的空文件」）；
//   * `effective_target` 是本次 diff **实际**使用的目标：请求目标对这条路径没有改动、而另一侧
//     有时服务端会回退（`target_fallback=true`），UI 必须据此说明「显示的是哪一侧的改动」；
//   * 结构化 diff 的 `hunks` 非法结构 → 抛错（不静默当空 diff，避免「文件没改动」假象）；
//   * stage 的 `status` 与 GET /git/status 同构，缺失 → null（调用方保留本地快照，不用空状态覆盖）。

export type GitChangeGroup = "staged" | "unstaged" | "untracked" | "conflicts";

export type GitFileStatus = {
  path: string;
  /** 重命名来源（仅 rename 记录带值）。 */
  from?: string;
  /** porcelain v2 的 XY 组合（如 "M." / ".M" / "R." / "??"）。 */
  status: string;
  /** -1 = 统计不可用（二进制 / 未跟踪且读不到结论），不得当作 0。 */
  insertions: number;
  deletions: number;
  binary: boolean;
};

export type GitRepoInfo = {
  root: string;
  branch: string;
  detached: boolean;
  head: string;
  upstream?: string;
  ahead: number;
  behind: number;
  isBare: boolean;
};

export type GitStatusResult = {
  repo: GitRepoInfo | null;
  clean: boolean;
  staged: GitFileStatus[];
  unstaged: GitFileStatus[];
  untracked: GitFileStatus[];
  conflicts: GitFileStatus[];
  /** 无法解析的 porcelain 记录等（不猜测语义，交给 UI 呈现）。 */
  warnings: string[];
  generatedAt: number;
};

export type GitDiffTarget = "working" | "staged" | `commit:${string}`;

export type GitDiffLineType = "context" | "add" | "del" | "nonewline";

export type GitDiffLine = {
  type: GitDiffLineType;
  oldNo: number | null;
  newNo: number | null;
  text: string;
};

export type GitDiffHunk = {
  header: string;
  oldStart: number;
  oldLines: number;
  newStart: number;
  newLines: number;
  lines: GitDiffLine[];
};

export type GitDiffFile = {
  path: string;
  absPath?: string;
  oldPath?: string;
  status: string;
  isBinary: boolean;
  isSubmodule: boolean;
};

export type GitDiffResult = {
  file: GitDiffFile | null;
  target: GitDiffTarget;
  /** 本次 diff 实际使用的目标（可能因「改动在另一侧」而回退）；等于 target 时未回退。 */
  effectiveTarget: GitDiffTarget;
  /** true = effectiveTarget ≠ target：UI 必须说明「显示的是哪一侧的改动」。 */
  targetFallback: boolean;
  context: number;
  whitespace: "show" | "ignore_all";
  insertions: number;
  deletions: number;
  hunks: GitDiffHunk[];
  /** 原始 diff 文本（复制 / 下载 .patch）。 */
  raw: string;
  parseError: string;
  truncated: boolean;
  truncatedReason: string;
  generatedAt: number;
};

export type GitCommit = {
  sha: string;
  shortSha: string;
  author: string;
  authoredAt: string;
  subject: string;
  refs: string[];
};

export type GitCommitsResult = {
  commits: GitCommit[];
  nextCursor: string | null;
  hasMore: boolean;
};

export type GitScopeRequest = {
  scope: string;
  path?: string;
};

export type GitDiffRequest = GitScopeRequest & {
  file: string;
  target?: GitDiffTarget;
  context?: number;
  whitespace?: "show" | "ignore_all";
};

export type GitCommitsRequest = GitScopeRequest & {
  limit?: number;
  cursor?: string | null;
};

/** 写操作只有暂存 / 取消暂存；提交（commit）不在本期范围（规划 §4.6 与 Q4）。 */
export type GitStageAction = "stage" | "unstage";

export type GitStageRequest = GitScopeRequest & {
  action: GitStageAction;
  /** 作用域相对路径（后端会逐个校验越界 / 选项式路径）。 */
  files: string[];
};

export type GitStageResult = {
  /** 服务端**实际生效**的动作（前端请求失败即抛错，不做乐观伪造）。 */
  action: GitStageAction;
  /** 实际生效的作用域相对路径（可能与请求顺序不同）。 */
  files: string[];
  /** 更新后的状态摘要；后端异常时缺失，前端保留本地快照。 */
  status: GitStatusResult | null;
  generatedAt: number;
};
