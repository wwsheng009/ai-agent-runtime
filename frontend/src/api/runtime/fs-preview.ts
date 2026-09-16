// P1：截断式预览客户端（GET /api/runtime/fs/preview）。
//
// 后端契约（backend/internal/filebrowse/preview.go）：
//   响应体：{ kind: text|image|binary|too_large, path, abs_path, size, mtime, mime,
//            text?, data_base64?, truncated, reason?, limit_bytes? }
//
// 归一化纪律：
//   * `kind` 白名单校验，未知 kind → 抛错（不猜渲染方式）；
//   * `kind=text` 但 `text` 非字符串 → 抛错（与 decode.ts 一致：不做类型伪装）；
//   * `kind=image` 但 `data_base64` 非字符串 → 抛错；
//   * truncated 缺省 false；reason/limit_bytes 缺失时不臆造。

import { RuntimeApiError, buildRuntimeUrlWithQuery, fetchRuntimeJson } from "./shared";

import type { FsPreview, FsPreviewKind, FsPreviewRequest } from "@/types/runtime/fs-browser";

export const FS_PREVIEW_PATH = "/api/runtime/fs/preview";

/** 前端默认上限：与 panels 既有预览口径一致（1MB）。 */
export const FS_PREVIEW_DEFAULT_MAX_BYTES = 1_000_000;

type RawRecord = Record<string, unknown>;

const PREVIEW_KINDS: readonly FsPreviewKind[] = [
  "text",
  "image",
  "binary",
  "too_large",
  "unknown",
];

function asRecord(value: unknown): RawRecord | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as RawRecord)
    : null;
}

function asString(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function asFiniteNumber(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

export function normalizeFsPreviewPayload(payload: unknown): FsPreview {
  const record = asRecord(payload);
  if (!record) {
    throw new Error("runtime fs preview payload is not an object");
  }
  const kindValue = asString(record.kind) as FsPreviewKind;
  if (!PREVIEW_KINDS.includes(kindValue) || kindValue === "unknown") {
    throw new Error(`unsupported fs preview kind: ${kindValue || "<empty>"}`);
  }

  const text = typeof record.text === "string" ? record.text : undefined;
  const dataBase64 =
    typeof record.data_base64 === "string" ? record.data_base64 : undefined;
  if (kindValue === "text" && text === undefined) {
    throw new Error("fs preview kind=text requires `text`");
  }
  if (kindValue === "image" && dataBase64 === undefined) {
    throw new Error("fs preview kind=image requires `data_base64`");
  }

  const size = asFiniteNumber(record.size) ?? -1;
  const mtime = asFiniteNumber(record.mtime);
  const limitBytes = asFiniteNumber(record.limit_bytes);
  const mime = asString(record.mime);
  const absPath = asString(record.abs_path);
  const reason = asString(record.reason);

  return {
    kind: kindValue,
    path: asString(record.path),
    size,
    truncated: record.truncated === true,
    ...(absPath ? { absPath } : {}),
    ...(mtime !== undefined ? { mtime } : {}),
    ...(mime ? { mime } : {}),
    ...(text !== undefined ? { text } : {}),
    ...(dataBase64 !== undefined ? { dataBase64 } : {}),
    ...(reason ? { reason } : {}),
    ...(limitBytes !== undefined ? { limitBytes } : {}),
  };
}

export type FsPreviewRequestOptions = { signal?: AbortSignal };

export async function fetchFsPreview(
  request: FsPreviewRequest,
  options: FsPreviewRequestOptions = {},
): Promise<FsPreview> {
  const scope = request.scope.trim();
  const path = request.path.trim();
  if (!scope || !path) {
    throw new Error("fs preview requires scope and path");
  }
  const url = buildRuntimeUrlWithQuery(FS_PREVIEW_PATH, {
    scope,
    path,
    max_bytes: request.maxBytes,
  });
  const payload = await fetchRuntimeJson<unknown>(url, {
    method: "GET",
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return normalizeFsPreviewPayload(payload);
}

/** 405/501/503：预览端点未就绪 → UI 退化为「仅提示可用下载」。 */
export function isFsPreviewUnavailable(error: unknown): boolean {
  return (
    error instanceof RuntimeApiError &&
    (error.status === 404 || error.status === 405 || error.status === 501 || error.status === 503)
  );
}
