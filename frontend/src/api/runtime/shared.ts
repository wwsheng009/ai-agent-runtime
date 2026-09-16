import type { RuntimeErrorPayload } from "@/types/runtime";

export class RuntimeApiError extends Error {
  readonly status: number;
  readonly payload: RuntimeErrorPayload | null;

  constructor(status: number, payload: RuntimeErrorPayload | null) {
    super(buildErrorMessage(status, payload));
    this.name = "RuntimeApiError";
    this.status = status;
    this.payload = payload;
  }
}

/**
 * 统一收口两种后端错误体（**这是必修的健壮性边界**）：
 *   * 扁平形状：`{ "error": "消息", "code": "…" }`（skills 既有 handler 多数如此）；
 *   * 嵌套形状：`{ "error": { "code": "…", "message": "…" } }`（/fs/*、/git/*、cache analytics 等）。
 *
 * 曾经的缺陷：直接 `payload.error.trim()`，当 `error` 是对象时抛
 * `payload.error.trim is not a function`，把「后端的真实错误（如 404 repo_not_found）」
 * 变成一条前端 TypeError —— UI 既看不到原因，也无法按 code 分支降级。
 * 这里只做形状归一化：**不猜语义、不覆盖后端码**，缺字段时如实给空串。
 */
export function readErrorEnvelope(payload: RuntimeErrorPayload | null): {
  code: string;
  message: string;
} {
  const flatCode = typeof payload?.code === "string" ? payload.code.trim() : "";
  const raw = payload?.error;
  if (typeof raw === "string") {
    return { code: flatCode, message: raw.trim() };
  }
  const nested = asRecord(raw);
  if (nested) {
    return {
      code: flatCode || readString(nested.code),
      // 兼容 `message` 与嵌套 `error` 两种字段名；两者都缺时给空串，由调用方兜底文案。
      message: readString(nested.message) || readString(nested.error),
    };
  }
  return { code: flatCode, message: "" };
}

export function isRuntimeApiErrorCode(error: unknown, code: string) {
  if (!(error instanceof RuntimeApiError)) {
    return false;
  }
  // 嵌套形状的 code 位于 `error.code`，必须一起看，否则调用方无法按新端点的错误码降级。
  return readErrorEnvelope(error.payload).code === code;
}

export function trimTrailingSlash(value: string) {
  return value.replace(/\/+$/, "");
}

export function getRuntimeBaseUrl() {
  const configured = import.meta.env.VITE_API_BASE_URL;
  if (configured && configured.trim()) {
    return trimTrailingSlash(configured.trim());
  }
  return "";
}

export function buildRuntimeUrl(pathname: string) {
  const base = getRuntimeBaseUrl();
  if (!base) {
    return pathname;
  }
  return `${base}${pathname}`;
}

export function buildRuntimeUrlWithQuery(
  pathname: string,
  searchParams: Record<string, string | number | boolean | undefined>,
) {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(searchParams)) {
    if (value === undefined || value === "") {
      continue;
    }
    params.set(key, String(value));
  }

  const queryString = params.toString();
  if (!queryString) {
    return buildRuntimeUrl(pathname);
  }
  return `${buildRuntimeUrl(pathname)}?${queryString}`;
}

export function buildErrorMessage(
  status: number,
  payload: RuntimeErrorPayload | null,
) {
  const requestId = payload?.request_id?.trim();
  const { code, message } = readErrorEnvelope(payload);
  if (code === "SESSION_LEASE_CONFLICT") {
    return appendRequestId(buildSessionLeaseConflictMessage(payload), requestId);
  }
  if (message) {
    return appendRequestId(message, requestId);
  }
  if (requestId) {
    return `runtime request failed with status ${status} (request_id: ${requestId})`;
  }
  return `runtime request failed with status ${status}`;
}

function buildSessionLeaseConflictMessage(payload: RuntimeErrorPayload | null) {
  const lease = asRecord(payload?.context?.lease);
  if (!lease) {
    return readErrorEnvelope(payload).message || "This session is currently active in another runtime.";
  }

  const ownerKind = readString(lease.owner_kind);
  const ownerId = readString(lease.owner_id);
  const ownerLabel = sessionLeaseOwnerLabel(ownerKind, ownerId);
  const pid = readNumber(lease.pid);
  const hostname = readString(lease.hostname);
  const locationParts = [
    pid !== null && pid > 0 ? `PID ${pid}` : "",
    hostname ? `host ${hostname}` : "",
  ].filter(Boolean);
  const location = locationParts.length > 0 ? ` (${locationParts.join(", ")})` : "";
  const suggestedAction = readString(payload?.context?.suggested_action);
  const expiresAt = readString(lease.expires_at);

  let message = `This session is currently active in ${ownerLabel}${location}.`;
  if (suggestedAction) {
    message += ` ${capitalizeSentence(suggestedAction)}.`;
  }
  if (expiresAt) {
    const remaining = leaseRemainingSeconds(expiresAt);
    if (remaining !== null && remaining > 0) {
      message += ` Current lease expires in about ${Math.max(1, Math.round(remaining))}s (at ${expiresAt}).`;
    } else {
      message += ` Current lease expiry: ${expiresAt}.`;
    }
  }
  return message;
}

/** 占用通道的人读标签：CLI / web / runtime 三分类。 */
function sessionLeaseOwnerLabel(ownerKind: string, ownerId: string): string {
  switch (ownerKind) {
    case "aicli-actor":
      return "CLI (aicli)";
    case "runtime-server-actor":
      return "runtime session actor (command API)";
    case "runtime-server-agent-chat":
      return "web (agent chat)";
    default:
      return ownerKind || ownerId || "another runtime";
  }
}

/** 占用方分类（短标题用）：CLI / web / runtime / other。 */
export function getSessionLeaseConflictTitle(error: unknown): string | undefined {
  if (!isRuntimeApiErrorCode(error, "SESSION_LEASE_CONFLICT")) {
    return undefined;
  }
  const payload = (error as RuntimeApiError).payload;
  const lease = asRecord(payload?.context?.lease);
  const ownerKind = readString(lease?.owner_kind);
  switch (ownerKind) {
    case "aicli-actor":
      return "This session is in use by a CLI session";
    case "runtime-server-actor":
      return "This session is in use by the runtime";
    case "runtime-server-agent-chat":
      return "This session is in use by the web app";
    default:
      return "This session is in use elsewhere";
  }
}

function leaseRemainingSeconds(expiresAt: string): number | null {
  const parsed = Date.parse(expiresAt);
  if (!Number.isFinite(parsed)) {
    return null;
  }
  return (parsed - Date.now()) / 1000;
}

function appendRequestId(message: string, requestId: string | undefined) {
  return requestId ? `${message} (request_id: ${requestId})` : message;
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function readString(value: unknown) {
  return typeof value === "string" ? value.trim() : "";
}

function readNumber(value: unknown) {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function capitalizeSentence(value: string) {
  return value ? `${value[0].toUpperCase()}${value.slice(1)}` : value;
}

export async function parseErrorPayload(response: Response) {
  try {
    return (await response.json()) as RuntimeErrorPayload;
  } catch {
    return null;
  }
}

/** 默认请求超时：避免后端因共享 SQLite 被锁/排队时前端无限等待 "Connecting to runtime…"。 */
export const RUNTIME_FETCH_TIMEOUT_MS = 10_000;

/**
 * 在途 GET 请求合并（inflight dedupe）。
 *
 * 同一 URL 已有一个在途请求时直接复用它的 Promise，不再发出第二个请求。
 * 目的是消掉「同一份大 payload 被并发重复拉取」：实测一次会话页首屏
 * `/api/runtime/models`(560KB)×2、`/api/runtime/sessions`(180KB)×3、
 * `/runtime/events`(380KB)×6，其中相当一部分来自 React StrictMode 双挂载与
 * 多个组件各自取数，而前端没有任何请求缓存/去重层。
 *
 * 只合并「调用方未自带 signal 的 GET」：调用方一旦传入 signal，取消语义必须
 * 独占该请求，共享 Promise 会让一方的 abort 连带影响另一方。请求结束后立即
 * 移出在途表，因此顺序轮询（每轮都期望拿到最新数据）语义不变——只合并重叠
 * 的并发请求，不做 TTL 缓存。
 */
const inflightRuntimeGets = new Map<string, Promise<unknown>>();

function isDedupableRuntimeGet(input: string, init?: RequestInit) {
  if (typeof input !== "string" || input.length === 0) {
    return false;
  }
  if (init?.signal) {
    return false;
  }
  return (init?.method ?? "GET").toUpperCase() === "GET";
}

export async function fetchRuntimeJson<T>(
  input: string,
  init?: RequestInit,
): Promise<T> {
  const dedupeKey = isDedupableRuntimeGet(input, init) ? input : null;
  if (dedupeKey) {
    const existing = inflightRuntimeGets.get(dedupeKey);
    if (existing) {
      return existing as Promise<T>;
    }
  }

  const request = performRuntimeJsonFetch<T>(input, init);
  if (dedupeKey) {
    inflightRuntimeGets.set(dedupeKey, request);
    void request
      .finally(() => {
        if (inflightRuntimeGets.get(dedupeKey) === request) {
          inflightRuntimeGets.delete(dedupeKey);
        }
      })
      // 请求失败由各自的调用方处理；这里只做在途表清理，避免未处理拒绝。
      .catch(() => {});
  }
  return request;
}

async function performRuntimeJsonFetch<T>(
  input: string,
  init?: RequestInit,
): Promise<T> {
  let effectiveInit = init;
  // 调用方未提供自己的 signal 时，加上超时信号；超时后 fetch 立即抛
  // TimeoutError，让 UI 能快速从 "connecting" 进入错误状态，而不是无限旋转。
  if (!init?.signal) {
    effectiveInit = { ...init, signal: AbortSignal.timeout(RUNTIME_FETCH_TIMEOUT_MS) };
  }
  const response = await fetch(input, effectiveInit);
  if (!response.ok) {
    const payload = await parseErrorPayload(response);
    throw new RuntimeApiError(response.status, payload);
  }
  return (await response.json()) as T;
}
