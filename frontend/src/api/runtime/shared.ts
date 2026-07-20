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

export function isRuntimeApiErrorCode(error: unknown, code: string) {
  return error instanceof RuntimeApiError && error.payload?.code === code;
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
  if (payload?.code === "SESSION_LEASE_CONFLICT") {
    return appendRequestId(buildSessionLeaseConflictMessage(payload), requestId);
  }
  if (payload?.error && payload.error.trim()) {
    return appendRequestId(payload.error.trim(), requestId);
  }
  if (requestId) {
    return `runtime request failed with status ${status} (request_id: ${requestId})`;
  }
  return `runtime request failed with status ${status}`;
}

function buildSessionLeaseConflictMessage(payload: RuntimeErrorPayload) {
  const lease = asRecord(payload.context?.lease);
  if (!lease) {
    return payload.error?.trim() || "This session is currently active in another runtime.";
  }

  const ownerKind = readString(lease.owner_kind);
  const ownerId = readString(lease.owner_id);
  const ownerLabel =
    ownerKind === "aicli-actor"
      ? "a local aicli process"
      : ownerKind === "runtime-server-actor"
        ? "runtime-server"
        : ownerKind || ownerId || "another runtime";
  const pid = readNumber(lease.pid);
  const hostname = readString(lease.hostname);
  const locationParts = [
    pid !== null && pid > 0 ? `PID ${pid}` : "",
    hostname ? `host ${hostname}` : "",
  ].filter(Boolean);
  const location = locationParts.length > 0 ? ` (${locationParts.join(", ")})` : "";
  const suggestedAction = readString(payload.context?.suggested_action);
  const expiresAt = readString(lease.expires_at);

  let message = `This session is currently active in ${ownerLabel}${location}.`;
  if (suggestedAction) {
    message += ` ${capitalizeSentence(suggestedAction)}.`;
  }
  if (expiresAt) {
    message += ` Current lease expiry: ${expiresAt}.`;
  }
  return message;
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

export async function fetchRuntimeJson<T>(
  input: string,
  init?: RequestInit,
): Promise<T> {
  const response = await fetch(input, init);
  if (!response.ok) {
    const payload = await parseErrorPayload(response);
    throw new RuntimeApiError(response.status, payload);
  }
  return (await response.json()) as T;
}
