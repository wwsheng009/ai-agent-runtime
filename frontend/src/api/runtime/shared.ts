import type { RuntimeErrorPayload } from "@/types/runtime";

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
  if (payload?.error && payload.error.trim()) {
    if (requestId) {
      return `${payload.error.trim()} (request_id: ${requestId})`;
    }
    return payload.error.trim();
  }
  if (requestId) {
    return `runtime request failed with status ${status} (request_id: ${requestId})`;
  }
  return `runtime request failed with status ${status}`;
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
    throw new Error(buildErrorMessage(response.status, payload));
  }
  return (await response.json()) as T;
}
