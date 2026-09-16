// P2：传输客户端（HEAD/GET /api/runtime/fs/download、/fs/upload/*）。
//
// 后端契约（backend/internal/api/skills/fs_transfer_handlers.go）：
//   HEAD /fs/download?scope&path          → Content-Length / ETag / Accept-Ranges
//   GET  /fs/download?scope&path (+Range) → 200 全量 | 206 分片 | 416 越界（Content-Range: bytes */size）
//   POST /fs/upload/init                  → {upload_id, offset, received, chunk_size, expires_at, target}
//   PUT  /fs/upload/{id}/chunk (raw body, Content-Range) → {received, offset} | 409 expected_offset
//   GET  /fs/upload/{id}                  → 续传探测
//   POST /fs/upload/{id}/complete         → {file:{path,size}, action, sha256?, elapsed_ms?}
//   DELETE /fs/upload/{id}                → 清理
//
// 归一化纪律：
//   * 下载探测缺 ETag → `etag: ""` 且 `resumable: false`（不假装可续传）；
//   * 409 + expected_offset → 抛具名 `FsUploadOffsetMismatchError`（重试策略据此分支，
//     不是普通网络错误，禁止盲目重试同一分片）；
//   * 416 → 抛具名 `FsRangeNotSatisfiableError`（UI 作废续传记录并重新探测）。

import { RuntimeApiError, buildRuntimeUrlWithQuery } from "./shared";

import type { RuntimeErrorPayload } from "@/types/runtime";

import type {
  FsConflictPolicy,
  FsDownloadProbe,
  FsUploadChunkResult,
  FsUploadCompleteResult,
  FsUploadInitRequest,
  FsUploadInitResult,
  FsUploadStatus,
} from "@/types/runtime/fs-browser";

export const FS_DOWNLOAD_PATH = "/api/runtime/fs/download";
export const FS_UPLOAD_INIT_PATH = "/api/runtime/fs/upload/init";

/** 分片 offset 不连续：属于「先探测再续传」的可恢复分支。 */
export class FsUploadOffsetMismatchError extends Error {
  readonly expectedOffset: number;

  constructor(expectedOffset: number) {
    super(`upload offset mismatch, expected ${expectedOffset}`);
    this.name = "FsUploadOffsetMismatchError";
    this.expectedOffset = expectedOffset;
  }
}

export class FsRangeNotSatisfiableError extends Error {
  readonly size: number;

  constructor(size: number) {
    super(`range not satisfiable for file of ${size} bytes`);
    this.name = "FsRangeNotSatisfiableError";
    this.size = size;
  }
}

type RawRecord = Record<string, unknown>;

function asRecord(value: unknown): RawRecord | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as RawRecord)
    : null;
}

function asString(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

function asNumber(value: unknown, fallback = -1): number {
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

function buildDownloadUrl(scope: string, path: string): string {
  return buildRuntimeUrlWithQuery(FS_DOWNLOAD_PATH, { scope, path });
}

/** 续传探测：无 `ETag` 视为不可续传（诚实降级，不假设服务端支持 Range）。 */
export async function probeDownload(
  scope: string,
  path: string,
  options: { signal?: AbortSignal } = {},
): Promise<FsDownloadProbe> {
  const response = await fetch(buildDownloadUrl(scope.trim(), path.trim()), {
    method: "HEAD",
    ...(options.signal ? { signal: options.signal } : {}),
  });
  if (!response.ok) {
    throw new RuntimeApiError(response.status, null);
  }
  const length = Number(response.headers.get("Content-Length") ?? "");
  const etag = response.headers.get("ETag") ?? "";
  return {
    size: Number.isFinite(length) && length >= 0 ? length : -1,
    etag,
    acceptRanges: (response.headers.get("Accept-Ranges") ?? "").includes("bytes"),
    contentType: response.headers.get("Content-Type") ?? "application/octet-stream",
  };
}

export type FsDownloadRangeOptions = { signal?: AbortSignal };

/** 顺序分片下载（Tier A/B 共用）；返回的 Blob 由调用方按需拼接/写入。 */
export async function downloadRange(
  scope: string,
  path: string,
  start: number,
  end: number,
  options: FsDownloadRangeOptions = {},
): Promise<{ blob: Blob; etag: string; totalSize: number }> {
  const response = await fetch(buildDownloadUrl(scope.trim(), path.trim()), {
    headers: { Range: `bytes=${start}-${end}` },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  if (response.status === 416) {
    const contentRange = response.headers.get("Content-Range") ?? "";
    const total = Number(contentRange.split("/")[1] ?? "");
    throw new FsRangeNotSatisfiableError(Number.isFinite(total) ? total : -1);
  }
  if (!response.ok) {
    throw new RuntimeApiError(response.status, null);
  }
  const blob = await response.blob();
  const total = Number(response.headers.get("Content-Length") ?? "");
  return {
    blob,
    etag: response.headers.get("ETag") ?? "",
    totalSize: Number.isFinite(total) ? total : blob.size,
  };
}

/** 原生下载入口（Tier C）：直接交给浏览器，不落内存。 */
export function buildNativeDownloadHref(scope: string, path: string): string {
  return buildDownloadUrl(scope.trim(), path.trim());
}

function normalizeUploadTarget(raw: unknown): { path: string; exists: boolean } {
  const record = asRecord(raw);
  return { path: asString(record?.path), exists: record?.exists === true };
}

export function normalizeUploadInitPayload(payload: unknown): FsUploadInitResult {
  const record = asRecord(payload);
  if (!record) {
    throw new Error("upload init payload is not an object");
  }
  const completed = record.completed === true;
  const uploadId = asString(record.upload_id);
  if (!completed && !uploadId) {
    throw new Error("upload init payload is missing `upload_id`");
  }
  const target = normalizeUploadTarget(record.target);
  const deduplicated = record.deduplicated === true;
  const sha256 = asString(asRecord(record.target)?.sha256);
  return {
    uploadId,
    offset: asNumber(record.offset, 0),
    received: asNumber(record.received, 0),
    chunkSize: asNumber(record.chunk_size, 4 * 1024 * 1024),
    expiresAt: asNumber(record.expires_at, 0),
    completed,
    deduplicated,
    targetPath: target.path,
    targetExists: target.exists,
    ...(deduplicated && sha256 ? { sha256 } : {}),
  };
}

export async function initUpload(
  request: FsUploadInitRequest,
  options: { signal?: AbortSignal } = {},
): Promise<FsUploadInitResult> {
  const response = await fetch(buildRuntimeUrlWithQuery(FS_UPLOAD_INIT_PATH, {}), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      scope: request.scope,
      dir: request.dir,
      name: request.name,
      size: request.size,
      sha256: request.sha256,
      chunk_size: request.chunkSize,
      conflict_policy: request.conflictPolicy,
    }),
    ...(options.signal ? { signal: options.signal } : {}),
  });
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    throw new RuntimeApiError(response.status, payload as RuntimeErrorPayload | null);
  }
  return normalizeUploadInitPayload(payload);
}

export type UploadConflictPolicyRequest = FsConflictPolicy;

/** 写一个分片：body 为原始字节（不 base64）；409 转为具名错误供上层重新对齐 offset。 */
export async function putUploadChunk(
  uploadId: string,
  chunk: Blob,
  start: number,
  totalSize: number,
  options: { signal?: AbortSignal; sha256?: string } = {},
): Promise<FsUploadChunkResult> {
  const end = start + chunk.size - 1;
  const response = await fetch(
    buildRuntimeUrlWithQuery(`/api/runtime/fs/upload/${encodeURIComponent(uploadId)}/chunk`, {}),
    {
      method: "PUT",
      headers: {
        "Content-Type": "application/octet-stream",
        "Content-Range": `bytes ${start}-${end}/${totalSize}`,
        ...(options.sha256 ? { "X-Chunk-Sha256": options.sha256 } : {}),
      },
      body: chunk,
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
  const payload: unknown = await response.json().catch(() => null);
  if (response.status === 409) {
    const expected = asNumber(asRecord(payload)?.expected_offset, -1);
    throw new FsUploadOffsetMismatchError(expected);
  }
  if (!response.ok) {
    throw new RuntimeApiError(response.status, payload as RuntimeErrorPayload | null);
  }
  const record = asRecord(payload);
  return {
    received: asNumber(record?.received, -1),
    offset: asNumber(record?.offset, -1),
  };
}

export async function fetchUploadStatus(
  uploadId: string,
  options: { signal?: AbortSignal } = {},
): Promise<FsUploadStatus> {
  const response = await fetch(
    buildRuntimeUrlWithQuery(`/api/runtime/fs/upload/${encodeURIComponent(uploadId)}`, {}),
    { method: "GET", ...(options.signal ? { signal: options.signal } : {}) },
  );
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    throw new RuntimeApiError(response.status, payload as RuntimeErrorPayload | null);
  }
  const record = asRecord(payload);
  const policy = asString(record?.conflict_policy);
  return {
    uploadId,
    received: asNumber(record?.received, 0),
    offset: asNumber(record?.offset, 0),
    chunkSize: asNumber(record?.chunk_size, 4 * 1024 * 1024),
    size: asNumber(record?.size, -1),
    expiresAt: asNumber(record?.expires_at, 0),
    targetPath: asString(record?.target_path ?? asRecord(record?.target)?.path),
    conflictPolicy:
      policy === "overwrite" || policy === "rename" ? (policy as FsConflictPolicy) : "fail",
  };
}

export function normalizeUploadCompletePayload(payload: unknown): FsUploadCompleteResult {
  const record = asRecord(payload);
  const file = asRecord(record?.file);
  const path = asString(file?.path);
  if (!path) {
    throw new Error("upload complete payload is missing `file.path`");
  }
  const action = asString(record?.action);
  const sha256 = asString(record?.sha256);
  const elapsed = record?.elapsed_ms;
  return {
    path,
    size: asNumber(file?.size, -1),
    action:
      action === "create" || action === "overwrite" || action === "rename"
        ? action
        : "unknown",
    ...(sha256 ? { sha256 } : {}),
    ...(typeof elapsed === "number" && Number.isFinite(elapsed)
      ? { elapsedMs: elapsed }
      : {}),
  };
}

export async function completeUpload(
  uploadId: string,
  sha256?: string,
  options: { signal?: AbortSignal } = {},
): Promise<FsUploadCompleteResult> {
  const response = await fetch(
    buildRuntimeUrlWithQuery(
      `/api/runtime/fs/upload/${encodeURIComponent(uploadId)}/complete`,
      {},
    ),
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(sha256 ? { sha256 } : {}),
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    throw new RuntimeApiError(response.status, payload as RuntimeErrorPayload | null);
  }
  return normalizeUploadCompletePayload(payload);
}

export async function abortUpload(
  uploadId: string,
  options: { signal?: AbortSignal } = {},
): Promise<void> {
  const response = await fetch(
    buildRuntimeUrlWithQuery(`/api/runtime/fs/upload/${encodeURIComponent(uploadId)}`, {}),
    { method: "DELETE", ...(options.signal ? { signal: options.signal } : {}) },
  );
  if (!response.ok && response.status !== 404) {
    throw new RuntimeApiError(response.status, null);
  }
}
