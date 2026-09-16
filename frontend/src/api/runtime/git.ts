// P3/P4-1：Git 客户端（读：GET /git/status | /git/diff | /git/commits；写：POST /git/stage）。
//
// 后端契约（backend/internal/gitbrowse）：
//   status : {repo:{root,branch,detached,head,upstream,ahead,behind,is_bare}, clean,
//             staged[], unstaged[], untracked[], conflicts[], warnings[], generated_at}
//   diff   : 见 types/runtime/git-browse.ts 的结构化 hunks 契约
//   commits: {commits:[{sha,short_sha,author,authored_at,subject,refs}], next_cursor, has_more}
//   stage  : {action,files,status,generated_at}（status 与 status 端点同构）
//
// 归一化纪律：
//   * 四类分组缺失 → 按空数组收口；单条记录缺 path → 跳过（计入 warnings 由后端给）；
//   * `insertions/deletions` 非有限数 → -1（二进制在 numstat 中就是 `-`，不用 0 伪装）；
//   * `hunks` 非法结构 → 抛错（**不静默当空 diff**，否则会显示成「文件没改动」）；
//   * `raw`/`parse_error` 缺失 → 空字符串，且 `parse_error` 非空时 UI 必须降级为纯文本；
//   * stage 的 `status` 缺失 → null（由 hook 决定保留旧快照），不伪造空状态。

import {
  RuntimeApiError,
  buildRuntimeUrl,
  buildRuntimeUrlWithQuery,
  fetchRuntimeJson,
} from "./shared";

import type {
  GitCommit,
  GitCommitsRequest,
  GitCommitsResult,
  GitDiffFile,
  GitDiffHunk,
  GitDiffLine,
  GitDiffLineType,
  GitDiffRequest,
  GitDiffResult,
  GitDiffTarget,
  GitFileStatus,
  GitRepoInfo,
  GitScopeRequest,
  GitStageAction,
  GitStageRequest,
  GitStageResult,
  GitStatusResult,
} from "@/types/runtime/git-browse";

export const GIT_STATUS_PATH = "/api/runtime/git/status";
export const GIT_DIFF_PATH = "/api/runtime/git/diff";
export const GIT_COMMITS_PATH = "/api/runtime/git/commits";
export const GIT_STAGE_PATH = "/api/runtime/git/stage";

type RawRecord = Record<string, unknown>;

const LINE_TYPES: readonly GitDiffLineType[] = ["context", "add", "del", "nonewline"];

function asRecord(value: unknown): RawRecord | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as RawRecord)
    : null;
}

function asString(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function asTrimmed(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

/** 行号：`null` 表示该侧不存在（契约禁止用 0 代替，避免语义歧义）。 */
function asLineNo(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function asCount(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : -1;
}

function asOptionalTrimmed(value: unknown): string | undefined {
  const text = asTrimmed(value);
  return text ? text : undefined;
}

function normalizeFileStatus(raw: unknown): GitFileStatus | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const path = asTrimmed(record.path);
  if (!path) {
    return null;
  }
  const from = asOptionalTrimmed(record.from);
  return {
    path,
    status: asTrimmed(record.status) || "?",
    insertions: asCount(record.insertions),
    deletions: asCount(record.deletions),
    binary: record.binary === true,
    ...(from ? { from } : {}),
  };
}

function normalizeGroup(value: unknown): GitFileStatus[] {
  if (!Array.isArray(value)) {
    return [];
  }
  const result: GitFileStatus[] = [];
  for (const item of value) {
    const status = normalizeFileStatus(item);
    if (status) {
      result.push(status);
    }
  }
  return result;
}

function normalizeRepoInfo(raw: unknown): GitRepoInfo | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const root = asTrimmed(record.root);
  if (!root) {
    return null;
  }
  const upstream = asOptionalTrimmed(record.upstream);
  return {
    root,
    branch: asTrimmed(record.branch),
    detached: record.detached === true,
    head: asTrimmed(record.head),
    ahead: Number.isFinite(record.ahead) ? Number(record.ahead) : 0,
    behind: Number.isFinite(record.behind) ? Number(record.behind) : 0,
    isBare: record.is_bare === true,
    ...(upstream ? { upstream } : {}),
  };
}

export function normalizeGitStatusPayload(payload: unknown): GitStatusResult {
  const record = asRecord(payload);
  if (!record) {
    throw new Error("runtime git status payload is not an object");
  }
  const warnings = Array.isArray(record.warnings)
    ? record.warnings.filter((item): item is string => typeof item === "string")
    : [];
  const generatedAt = Number.isFinite(record.generated_at)
    ? Number(record.generated_at)
    : 0;
  return {
    repo: normalizeRepoInfo(record.repo),
    clean: record.clean === true,
    staged: normalizeGroup(record.staged),
    unstaged: normalizeGroup(record.unstaged),
    untracked: normalizeGroup(record.untracked),
    conflicts: normalizeGroup(record.conflicts),
    warnings,
    generatedAt,
  };
}

function normalizeDiffLine(raw: unknown): GitDiffLine | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const typeValue = asTrimmed(record.type) as GitDiffLineType;
  if (!LINE_TYPES.includes(typeValue)) {
    return null;
  }
  return {
    type: typeValue,
    oldNo: asLineNo(record.old_no),
    newNo: asLineNo(record.new_no),
    text: typeof record.text === "string" ? record.text : "",
  };
}

function normalizeHunk(raw: unknown): GitDiffHunk | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const linesRaw = record.lines;
  if (!Array.isArray(linesRaw)) {
    return null;
  }
  const lines: GitDiffLine[] = [];
  for (const item of linesRaw) {
    const line = normalizeDiffLine(item);
    if (line) {
      lines.push(line);
    }
  }
  return {
    header: asString(record.header),
    oldStart: asLineNo(record.old_start) ?? 0,
    oldLines: asLineNo(record.old_lines) ?? 0,
    newStart: asLineNo(record.new_start) ?? 0,
    newLines: asLineNo(record.new_lines) ?? 0,
    lines,
  };
}

function normalizeDiffFile(raw: unknown): GitDiffFile | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const path = asTrimmed(record.path);
  if (!path) {
    return null;
  }
  const oldPath = asOptionalTrimmed(record.old_path);
  const absPath = asOptionalTrimmed(record.abs_path);
  return {
    path,
    status: asTrimmed(record.status) || "M",
    isBinary: record.is_binary === true,
    isSubmodule: record.is_submodule === true,
    ...(oldPath ? { oldPath } : {}),
    ...(absPath ? { absPath } : {}),
  };
}

export function normalizeGitDiffPayload(payload: unknown): GitDiffResult {
  const record = asRecord(payload);
  if (!record) {
    throw new Error("runtime git diff payload is not an object");
  }
  const hunksRaw = record.hunks;
  if (!Array.isArray(hunksRaw)) {
    // 契约要求 hunks 必填；缺失说明后端异常，静默当空会显示成「无改动」。
    throw new Error("runtime git diff payload is missing `hunks`");
  }
  const hunks: GitDiffHunk[] = [];
  for (const item of hunksRaw) {
    const hunk = normalizeHunk(item);
    if (!hunk) {
      throw new Error("runtime git diff payload contains an invalid hunk");
    }
    hunks.push(hunk);
  }
  const targetValue = asString(record.target);
  const target = (targetValue || "working") as GitDiffTarget;
  const whitespace = record.whitespace === "ignore_all" ? "ignore_all" : "show";
  const context = Number.isFinite(record.context) ? Number(record.context) : 3;
  return {
    file: normalizeDiffFile(record.file),
    target,
    context,
    whitespace,
    insertions: asCount(record.insertions),
    deletions: asCount(record.deletions),
    hunks,
    raw: asString(record.raw),
    parseError: asString(record.parse_error),
    truncated: record.truncated === true,
    truncatedReason: asString(record.truncated_reason),
    generatedAt: Number.isFinite(record.generated_at) ? Number(record.generated_at) : 0,
  };
}

function normalizeCommit(raw: unknown): GitCommit | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const sha = asTrimmed(record.sha);
  if (!sha) {
    return null;
  }
  const refs = Array.isArray(record.refs)
    ? record.refs.filter((item): item is string => typeof item === "string")
    : [];
  return {
    sha,
    shortSha: asTrimmed(record.short_sha) || sha.slice(0, 7),
    author: asTrimmed(record.author),
    authoredAt: asTrimmed(record.authored_at),
    subject: asString(record.subject),
    refs,
  };
}

export function normalizeGitCommitsPayload(payload: unknown): GitCommitsResult {
  const record = asRecord(payload);
  if (!record) {
    throw new Error("runtime git commits payload is not an object");
  }
  const commitsRaw = record.commits;
  if (!Array.isArray(commitsRaw)) {
    throw new Error("runtime git commits payload is missing `commits`");
  }
  const commits: GitCommit[] = [];
  for (const item of commitsRaw) {
    const commit = normalizeCommit(item);
    if (commit) {
      commits.push(commit);
    }
  }
  return {
    commits,
    nextCursor: typeof record.next_cursor === "string" && record.next_cursor ? record.next_cursor : null,
    hasMore: record.has_more === true,
  };
}

export type GitRequestOptions = { signal?: AbortSignal };

export async function fetchGitStatus(
  request: GitScopeRequest,
  options: GitRequestOptions = {},
): Promise<GitStatusResult> {
  const scope = request.scope.trim();
  if (!scope) {
    throw new Error("git status requires a scope");
  }
  const url = buildRuntimeUrlWithQuery(GIT_STATUS_PATH, {
    scope,
    path: request.path ?? "",
  });
  const payload = await fetchRuntimeJson<unknown>(url, {
    method: "GET",
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return normalizeGitStatusPayload(payload);
}

export async function fetchGitDiff(
  request: GitDiffRequest,
  options: GitRequestOptions = {},
): Promise<GitDiffResult> {
  const scope = request.scope.trim();
  const file = request.file.trim();
  if (!scope || !file) {
    throw new Error("git diff requires scope and file");
  }
  const url = buildRuntimeUrlWithQuery(GIT_DIFF_PATH, {
    scope,
    path: request.path ?? "",
    file,
    target: request.target ?? "working",
    context: request.context,
    whitespace: request.whitespace,
  });
  const payload = await fetchRuntimeJson<unknown>(url, {
    method: "GET",
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return normalizeGitDiffPayload(payload);
}

export async function fetchGitCommits(
  request: GitCommitsRequest,
  options: GitRequestOptions = {},
): Promise<GitCommitsResult> {
  const scope = request.scope.trim();
  if (!scope) {
    throw new Error("git commits requires a scope");
  }
  const url = buildRuntimeUrlWithQuery(GIT_COMMITS_PATH, {
    scope,
    path: request.path ?? "",
    limit: request.limit,
    cursor: request.cursor ?? undefined,
  });
  const payload = await fetchRuntimeJson<unknown>(url, {
    method: "GET",
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return normalizeGitCommitsPayload(payload);
}

/**
 * 解析 POST /git/stage 的响应。
 *
 * `status` 与 GET /git/status 同构，复用同一个归一化函数；缺失时返回 null，
 * 由调用方保留本地快照（**不**用空状态覆盖，否则会假装「工作区干净」）。
 */
export function normalizeGitStagePayload(payload: unknown): GitStageResult {
  const record = asRecord(payload);
  if (!record) {
    throw new Error("runtime git stage payload is not an object");
  }
  const action: GitStageAction = asTrimmed(record.action) === "unstage" ? "unstage" : "stage";
  const files = Array.isArray(record.files)
    ? record.files
        .map((item) => asTrimmed(item))
        .filter((item): item is string => Boolean(item))
    : [];
  const statusRecord = asRecord(record.status);
  return {
    action,
    files,
    status: statusRecord ? normalizeGitStatusPayload(statusRecord) : null,
    generatedAt: Number.isFinite(record.generated_at) ? Number(record.generated_at) : 0,
  };
}

/**
 * 暂存 / 取消暂存（写操作）。前置校验只做「明显非法」的本地拦截：
 * 空 scope、空文件列表直接抛错，避免把必然 400 的请求打到服务端；
 * 路径越界等安全判定**只信服务端**（前端 normalization 不复制安全规则）。
 */
export async function submitGitStage(
  request: GitStageRequest,
  options: GitRequestOptions = {},
): Promise<GitStageResult> {
  const scope = request.scope.trim();
  if (!scope) {
    throw new Error("git stage requires a scope");
  }
  const files = request.files.map((file) => file.trim()).filter(Boolean);
  if (files.length === 0) {
    throw new Error("git stage requires at least one file");
  }
  const payload = await fetchRuntimeJson<unknown>(buildRuntimeUrl(GIT_STAGE_PATH), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      scope,
      path: request.path ?? "",
      action: request.action,
      files,
    }),
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return normalizeGitStagePayload(payload);
}

/** 仓库缺失（400 repo_not_found）/ git 不可用（503 git_unavailable）→ UI 显示可解释空态。 */
export function isGitRepoMissing(error: unknown): boolean {
  return (
    error instanceof RuntimeApiError &&
    (error.status === 400 || error.status === 404 || error.status === 503)
  );
}
