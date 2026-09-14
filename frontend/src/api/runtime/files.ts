// P2-1A：运行时文件读取客户端（POST /api/runtime/fs/read-file）。
//
// 后端契约（backend/internal/api/skills/file_transfer_handlers.go:36-64）：
//   请求体（JSON）：{path}
//     —— 相对路径由后端按运行时进程工作目录解析；响应回传解析后的绝对路径。
//   响应体：{file: {path, data_base64(标准 base64), byte_count}}
//   错误体：{error, request_id?}；未注入 filetransport 服务时 503。
//
// 归一化纪律：
//   * `file` 结构缺失、path 缺失、data_base64 非字符串 → 抛错（不伪装成空文件）；
//   * base64 不可解码 → 抛错（坏载荷不当作空内容）；
//   * byte_count 缺失时按 base64 解出的实际字节数收口（同样来自载荷，不臆造）；
//   * 降级分类只看 HTTP 状态码（isFileReadUnavailable）。
//
// 只读面：本期 UI 只消费读取端点，不提供 write / append（写入交互未开放）。

import { decodeBase64Bytes } from "@/lib/file-preview/decode";

import { RuntimeApiError, buildRuntimeUrl, fetchRuntimeJson } from "./shared";

import type { RuntimeFileReadResult } from "@/types/runtime";

export const FILE_READ_PATH = "/api/runtime/fs/read-file";

/** 预览上限：后端不设上限，由前端收口，避免超大文件拖垮渲染。 */
export const FILE_PREVIEW_MAX_BYTES = 1_000_000;

type RawRecord = Record<string, unknown>;

function asRecord(value: unknown): RawRecord | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as RawRecord)
    : null;
}

export function normalizeFileReadPayload(payload: unknown): RuntimeFileReadResult {
  const file = asRecord(asRecord(payload)?.file);
  if (!file) {
    throw new Error("runtime file read payload is missing `file`");
  }

  const path = typeof file.path === "string" ? file.path.trim() : "";
  if (!path) {
    throw new Error("runtime file read payload is missing `file.path`");
  }

  const dataBase64 = file.data_base64;
  if (typeof dataBase64 !== "string") {
    throw new Error("runtime file read payload is missing `file.data_base64`");
  }
  const decodedBytes = decodeBase64Bytes(dataBase64).length;

  const byteCount =
    typeof file.byte_count === "number" &&
    Number.isFinite(file.byte_count) &&
    file.byte_count >= 0
      ? file.byte_count
      : decodedBytes;

  return { path, dataBase64, byteCount };
}

export type FileReadRequestOptions = {
  signal?: AbortSignal;
};

/** 读取运行时进程可见的本地文件；路径为空时直接抛错（不发无效请求）。 */
export async function readRuntimeFile(
  path: string,
  options: FileReadRequestOptions = {},
): Promise<RuntimeFileReadResult> {
  const trimmed = path.trim();
  if (!trimmed) {
    throw new Error("file path is required");
  }

  const payload = await fetchRuntimeJson<unknown>(buildRuntimeUrl(FILE_READ_PATH), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ path: trimmed }),
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return normalizeFileReadPayload(payload);
}

/**
 * 读取不可用的降级判据：路由缺失（404/405）、未实现（501）、
 * 未注入 filetransport 服务（503）。其余错误（含 500 读盘失败）按真实失败呈现。
 */
export function isFileReadUnavailable(error: unknown): boolean {
  if (!(error instanceof RuntimeApiError)) {
    return false;
  }
  return (
    error.status === 404 ||
    error.status === 405 ||
    error.status === 501 ||
    error.status === 503
  );
}
