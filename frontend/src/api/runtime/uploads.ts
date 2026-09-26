// S5：运行时图片附件上传客户端（`POST /api/runtime/uploads`）。
//
// 后端契约（backend/internal/api/runtimeapi/attachment_upload_handlers.go）：
//   请求：multipart/form-data，字段名固定 `file`，一次 ≤8 份；
//   响应：{ok, accepted, attachments:[{name,path,bytes,width,height,note,skipped}]}
//     —— `path` / `bytes` / `width` / `height` / `note` / `skipped` 均带 omitempty；
//   降级：未配置附件目录 → 503 {ok:false,error:"uploads_unavailable",reason:"…"}；
//         缺 file 字段 → 400 {error:"missing_file"}；超过 8 份 → 400 {error:"too_many_files"}。
//
// 归一化纪律（与既有 api/runtime/* 同口径）：
//   * `attachments` 非数组 → 抛错（**不把结构异常伪装成「空上传成功」**）；
//   * 单条缺 `name` → 只丢该条，不整页失败；
//   * 缺省字段按后端 omitempty 语义给 0 / 空串，不臆造体积与尺寸；
//   * 错误分类三段互不混用：端点不可用（404/405/501/503）、校验失败（400/413/415/422）、
//     其余按真实失败（保留 HTTP 状态与后端原文）。

import { RuntimeApiError, buildRuntimeUrl, fetchRuntimeJson } from "./shared";

import type { RuntimeUploadAttachment, RuntimeUploadResult } from "@/types/runtime";

export const RUNTIME_UPLOAD_PATH = "/api/runtime/uploads";

/** 后端 `maxRuntimeUploadFiles`：一次请求最多 8 份；前端按单文件逐个上传，仍按此收口。 */
export const RUNTIME_UPLOAD_MAX_COUNT = 8;

type RawRecord = Record<string, unknown>;

function asRecord(value: unknown): RawRecord | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as RawRecord)
    : null;
}

function readString(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function readNumber(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : 0;
}

export function normalizeRuntimeUploadAttachment(
  raw: unknown,
): RuntimeUploadAttachment | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const name = readString(record.name).trim();
  if (!name) {
    // 身份字段缺失只丢该条（与 skills 目录同口径），不让整次上传变成失败。
    return null;
  }
  const path = readString(record.path).trim();
  const skipped = record.skipped === true;
  if (!path && !skipped) {
    // 既没有路径又没被标记跳过：后端语义不通，按坏条目丢弃而不是伪造「已上传」。
    return null;
  }
  return {
    name,
    path,
    bytes: readNumber(record.bytes),
    width: readNumber(record.width),
    height: readNumber(record.height),
    note: readString(record.note).trim(),
    skipped,
  };
}

export function normalizeRuntimeUploadPayload(payload: unknown): RuntimeUploadResult {
  const record = asRecord(payload);
  if (!record) {
    throw new Error("runtime upload payload is not an object");
  }
  const rawAttachments = record.attachments;
  if (!Array.isArray(rawAttachments)) {
    throw new Error("runtime upload payload is missing `attachments` array");
  }
  const attachments = rawAttachments
    .map(normalizeRuntimeUploadAttachment)
    .filter((item): item is RuntimeUploadAttachment => item !== null);
  const accepted = readNumber(record.accepted);
  return {
    ok: record.ok === true,
    // 后端上报值优先；缺席时按可用路径条目数收口（同样来自载荷，不臆造）。
    accepted: accepted || attachments.filter((item) => item.path !== "").length,
    attachments,
  };
}

export type RuntimeUploadRequestOptions = {
  signal?: AbortSignal;
};

/** 上传单个文件；`file` 为空或体积为 0 时直接抛错（不发无效请求）。 */
export async function uploadRuntimeAttachment(
  file: File,
  options: RuntimeUploadRequestOptions = {},
): Promise<RuntimeUploadResult> {
  if (!file || typeof file.name !== "string" || file.name.trim() === "") {
    throw new Error("runtime upload requires a named file");
  }
  const form = new FormData();
  // 字段名固定 `file`（后端只读 `r.MultipartForm.File["file"]`）。
  form.append("file", file, file.name);
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(RUNTIME_UPLOAD_PATH),
    {
      method: "POST",
      // 不设 Content-Type：交给浏览器补 multipart boundary。
      headers: { Accept: "application/json" },
      body: form,
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
  return normalizeRuntimeUploadPayload(payload);
}

/**
 * 端点不可用：路由缺失（404/405）、未实现（501）、未配置附件目录（503）。
 * 其余错误（含 400 校验失败）不得混入此类。
 */
export function isRuntimeUploadsUnavailable(error: unknown): boolean {
  return (
    error instanceof RuntimeApiError &&
    (error.status === 404 ||
      error.status === 405 ||
      error.status === 501 ||
      error.status === 503)
  );
}

/** 请求本身可解释地不合法：缺字段 / multipart 解析失败 / 超张数 / 载荷过大。 */
export function isRuntimeUploadValidationError(error: unknown): boolean {
  return (
    error instanceof RuntimeApiError &&
    (error.status === 400 ||
      error.status === 413 ||
      error.status === 415 ||
      error.status === 422)
  );
}

export type RuntimeUploadFailure = {
  /** 后端错误码（扁平 `code` 或 `error` 字符串）；无则为空串。 */
  code: string;
  /** 可展示原因：优先 `reason`（503/400 的自解释字段），其次 `error` / `message`。 */
  reason: string;
  status: number | null;
};

/** 把上传失败归一成「可行动原因」，不吞掉后端原文。 */
export function describeRuntimeUploadFailure(error: unknown): RuntimeUploadFailure {
  if (!(error instanceof RuntimeApiError)) {
    return {
      code: "",
      reason: error instanceof Error ? error.message : String(error),
      status: null,
    };
  }
  const payload = asRecord(error.payload);
  const flatError = readString(payload?.error).trim();
  const nested = asRecord(payload?.error);
  const code =
    readString(payload?.code).trim() ||
    (nested ? readString(nested.code).trim() : "") ||
    flatError;
  const reason =
    readString(payload?.reason).trim() ||
    readString(nested?.message).trim() ||
    readString(nested?.error).trim() ||
    (flatError && flatError !== code ? flatError : "") ||
    readString(payload?.message).trim() ||
    error.message;
  return { code, reason, status: error.status };
}
