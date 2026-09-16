// P0：作用域根列表客户端（GET /api/runtime/fs/roots）。
//
// 后端契约（backend/internal/api/skills/fs_browser_handlers.go）：
//   响应体：{ roots: [{scope, kind, name, path, exists, is_git_repo, git_root?, probe_error?}], count }
//   错误体：{error, request_id?}；未注入服务时 503。
//
// 归一化纪律：
//   * `roots` 缺失或非数组 → 抛错（不伪装成「没有根」，避免 UI 误报空态）；
//   * `exists` / `is_git_repo` 缺省 false（探测失败不报错，属既有契约）；
//   * `scope` / `path` 缺失的条目跳过（无作用域无法发起后续请求），并计入 `skipped`。

import { RuntimeApiError, buildRuntimeUrl, fetchRuntimeJson } from "./shared";

import type { FsRoot, FsRootKind } from "@/types/runtime/fs-browser";

export const FS_ROOTS_PATH = "/api/runtime/fs/roots";

type RawRecord = Record<string, unknown>;

function asRecord(value: unknown): RawRecord | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as RawRecord)
    : null;
}

function asString(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

const ROOT_KINDS: readonly FsRootKind[] = ["workspace", "session", "cwd"];

function normalizeRoot(raw: unknown): FsRoot | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const scope = asString(record.scope);
  const path = asString(record.path);
  if (!scope || !path) {
    return null;
  }
  const kindValue = asString(record.kind) as FsRootKind;
  const gitRoot = asString(record.git_root);
  const probeError = asString(record.probe_error);
  return {
    scope,
    kind: ROOT_KINDS.includes(kindValue) ? kindValue : "cwd",
    name: asString(record.name) || scope,
    path,
    exists: record.exists === true,
    isGitRepo: record.is_git_repo === true,
    ...(gitRoot ? { gitRoot } : {}),
    ...(probeError ? { probeError } : {}),
  };
}

export type FsRootsResult = {
  roots: FsRoot[];
  /** 因缺字段被跳过的条目数（UI 可提示「部分根不可用」）。 */
  skipped: number;
};

export function normalizeFsRootsPayload(payload: unknown): FsRootsResult {
  const rootsRaw = asRecord(payload)?.roots;
  if (!Array.isArray(rootsRaw)) {
    throw new Error("runtime fs roots payload is missing `roots`");
  }
  const roots: FsRoot[] = [];
  let skipped = 0;
  for (const item of rootsRaw) {
    const root = normalizeRoot(item);
    if (root) {
      roots.push(root);
    } else {
      skipped += 1;
    }
  }
  return { roots, skipped };
}

export type FsRootsRequestOptions = {
  signal?: AbortSignal;
  /**
   * 会话 id：后端 `fsRequestContext` 接受 `?session_id=` 并把「会话当前工作目录」补成一个
   * `kind: "session"` 根。**不传 = 只拿到注册的工作区根**，文件浏览器就会停在 workspace 根
   * 而不是用户期望的「本会话目录」。
   */
  sessionId?: string | null;
};

export async function fetchFsRoots(
  options: FsRootsRequestOptions = {},
): Promise<FsRootsResult> {
  const payload = await fetchRuntimeJson<unknown>(buildFsRootsUrl(options.sessionId), {
    method: "GET",
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return normalizeFsRootsPayload(payload);
}

/** 空白 sessionId 不落进 URL（后端把空串当「未指定」，显式 `session_id=` 反而不可读）。 */
export function buildFsRootsUrl(sessionId?: string | null): string {
  const trimmed = sessionId?.trim() ?? "";
  const base = buildRuntimeUrl(FS_ROOTS_PATH);
  return trimmed ? `${base}?session_id=${encodeURIComponent(trimmed)}` : base;
}

/**
 * 默认作用域根的选择纪律：**优先「会话目录」根**，其次服务端顺序首根。
 * 不猜路径、不比较大小写；会话根缺失时如实退回首根（调用方仍有 fallback 兜底）。
 */
export function pickDefaultRoot(roots: readonly FsRoot[]): FsRoot | null {
  return roots.find((root) => root.kind === "session") ?? roots[0] ?? null;
}

/** 503/404/501 表示能力未就绪：UI 退化为「只用当前会话工作目录」并提示。 */
export function isFsRootsUnavailable(error: unknown): boolean {
  return (
    error instanceof RuntimeApiError &&
    (error.status === 404 || error.status === 405 || error.status === 501 || error.status === 503)
  );
}
