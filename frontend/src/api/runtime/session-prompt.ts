// S5：运行时会话「带图提交 prompt」客户端
// （`POST /api/runtime/sessions/{id}/runtime/commands`，`type=submit_prompt`）。
//
// 为什么不是 `/api/agent/chat`：图片附件只在这条通道上被接受。
// 后端（backend/internal/api/runtimeapi/session_runtime_handlers.go:918-963）：
//   * `images` 只接受落在附件目录内的路径（resolveRuntimeUploadImages），
//     目录外路径逐条以 `image_notes` 回报；
//   * 带了 images 却一张都不可用 → 400（`images rejected: …`），**绝不静默降级成纯文本**；
//   * 会话忙 / 等审批 / 等输入 → 202 `{ok, pending, state}`（本轮未执行）；
//   * 完成 → 200 `{result, attached_images?, image_notes?}`；
//   * `attached_images` / `image_notes` 只在带 images 的请求里出现（旧契约不变）。
//
// 归一化纪律：缺 `pending` 视为 false；`image_notes` 非数组 → 抛错（不吞掉后端说明）；
// `attached_images` 缺席 → null（不伪造 0）。

import { RuntimeApiError, buildRuntimeUrl, fetchRuntimeJson } from "./shared";

import type { RuntimeSessionPromptResult } from "@/types/runtime";

/** 会话运行时命令端点（与 `session-turn-control` / `profiles/session-switch` 同路径）。 */
export function sessionRuntimeCommandsPath(sessionId: string): string {
  return `/api/runtime/sessions/${encodeURIComponent(sessionId)}/runtime/commands`;
}

type RawRecord = Record<string, unknown>;

function asRecord(value: unknown): RawRecord | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as RawRecord)
    : null;
}

function readStringArray(value: unknown): string[] {
  if (value === undefined || value === null) {
    return [];
  }
  if (!Array.isArray(value)) {
    throw new Error("runtime prompt payload has non-array `image_notes`");
  }
  return value
    .map((item) => (typeof item === "string" ? item.trim() : ""))
    .filter((item) => item !== "");
}

export function normalizeRuntimeSessionPromptPayload(
  payload: unknown,
): RuntimeSessionPromptResult {
  const record = asRecord(payload);
  if (!record) {
    throw new Error("runtime prompt payload is not an object");
  }
  const attached = record.attached_images;
  return {
    pending: record.pending === true,
    attachedImages:
      typeof attached === "number" && Number.isFinite(attached) && attached >= 0
        ? attached
        : null,
    imageNotes: readStringArray(record.image_notes),
    result: record.result ?? null,
  };
}

/** 去空、去重（保持入参顺序）：重复路径不重复计费，空串不占位。 */
export function normalizeRuntimePromptImages(
  images: readonly string[] | undefined,
): string[] {
  const seen = new Set<string>();
  const normalized: string[] = [];
  for (const raw of images ?? []) {
    const value = raw.trim();
    if (!value || seen.has(value)) {
      continue;
    }
    seen.add(value);
    normalized.push(value);
  }
  return normalized;
}

export type RuntimeSessionPromptOptions = {
  /** 已上传成功的服务端路径（来自 `POST /api/runtime/uploads`）。 */
  images?: readonly string[];
  signal?: AbortSignal;
};

/**
 * 提交一个 prompt；带 `images` 时由后端在发送瞬间把附件挂到本轮消息上。
 * 会话 id / prompt 为空时直接抛错（不发无效请求，也不静默退化）。
 */
export async function submitRuntimeSessionPrompt(
  sessionId: string,
  prompt: string,
  options: RuntimeSessionPromptOptions = {},
): Promise<RuntimeSessionPromptResult> {
  const normalizedSessionId = sessionId.trim();
  if (!normalizedSessionId) {
    throw new Error("session id is required to submit a prompt");
  }
  const normalizedPrompt = prompt.trim();
  if (!normalizedPrompt) {
    throw new Error("prompt is required to submit a session prompt");
  }
  const images = normalizeRuntimePromptImages(options.images);
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(sessionRuntimeCommandsPath(normalizedSessionId)),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        type: "submit_prompt",
        prompt: normalizedPrompt,
        // 无图时不发送该字段：响应因此不含 attached_images / image_notes。
        ...(images.length > 0 ? { images } : {}),
      }),
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
  return normalizeRuntimeSessionPromptPayload(payload);
}

/** 端点不可用（路由缺失 404/405、未实现 501、服务端未就绪 503）。 */
export function isSessionPromptUnavailable(error: unknown): boolean {
  return (
    error instanceof RuntimeApiError &&
    (error.status === 404 ||
      error.status === 405 ||
      error.status === 501 ||
      error.status === 503)
  );
}

/** 400：请求本身不合法（prompt 为空、images 全不可用）。按校验失败呈现。 */
export function isSessionPromptValidationError(error: unknown): boolean {
  return error instanceof RuntimeApiError && error.status === 400;
}

/** 409：会话租约冲突（另一运行时正在驱动该会话），由 `shared` 给可读文案。 */
export function isSessionPromptLeaseConflict(error: unknown): boolean {
  return error instanceof RuntimeApiError && error.status === 409;
}
