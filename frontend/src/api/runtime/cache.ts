import type {
  CacheCapabilities,
  CacheMessageTrace,
  CacheOverview,
  CacheRequestListResponse,
  CacheRequestRecord,
  CacheRequestsQuery,
} from "@/types/runtime";

import { buildRuntimeUrlWithQuery, fetchRuntimeJson } from "./shared";

// LLM 缓存分析契约客户端（cache.analytics.v1）。
// runtime server 端点：/api/runtime/sessions/{id}/cache/*（session 显式在路径，
// §3.2 W2）；模式对齐 api/runtime/analytics.ts。

type CacheRequestOptions = {
  adminToken?: string;
};

function buildCacheHeaders(adminToken?: string) {
  const token = adminToken?.trim();
  if (!token) {
    return {} as Record<string, string>;
  }
  return {
    Authorization: `Bearer ${token}`,
  } satisfies Record<string, string>;
}

function cacheBasePath(sessionId: string) {
  return `/api/runtime/sessions/${encodeURIComponent(sessionId)}/cache`;
}

export async function getCacheCapabilities(
  sessionId: string,
  options: CacheRequestOptions = {},
): Promise<CacheCapabilities> {
  return fetchRuntimeJson<CacheCapabilities>(
    buildRuntimeUrlWithQuery(`${cacheBasePath(sessionId)}/capabilities`, {}),
    {
      headers: buildCacheHeaders(options.adminToken),
    },
  );
}

export async function getCacheOverview(
  sessionId: string,
  options: CacheRequestOptions = {},
): Promise<CacheOverview> {
  return fetchRuntimeJson<CacheOverview>(
    buildRuntimeUrlWithQuery(`${cacheBasePath(sessionId)}/overview`, {}),
    {
      headers: buildCacheHeaders(options.adminToken),
    },
  );
}

export async function getCacheRequests(
  sessionId: string,
  query: CacheRequestsQuery & CacheRequestOptions = {},
): Promise<CacheRequestListResponse> {
  const { adminToken, ...params } = query;
  return fetchRuntimeJson<CacheRequestListResponse>(
    buildRuntimeUrlWithQuery(`${cacheBasePath(sessionId)}/requests`, { ...params }),
    {
      headers: buildCacheHeaders(adminToken),
    },
  );
}

export async function getCacheRequest(
  sessionId: string,
  llmRequestId: string,
  options: CacheRequestOptions = {},
): Promise<CacheRequestRecord> {
  return fetchRuntimeJson<CacheRequestRecord>(
    buildRuntimeUrlWithQuery(
      `${cacheBasePath(sessionId)}/requests/${encodeURIComponent(llmRequestId)}`,
      {},
    ),
    {
      headers: buildCacheHeaders(options.adminToken),
    },
  );
}

export async function getMessageTrace(
  sessionId: string,
  messageId: string,
  options: CacheRequestOptions = {},
): Promise<CacheMessageTrace> {
  return fetchRuntimeJson<CacheMessageTrace>(
    buildRuntimeUrlWithQuery(
      `${cacheBasePath(sessionId)}/messages/${encodeURIComponent(messageId)}/trace`,
      {},
    ),
    {
      headers: buildCacheHeaders(options.adminToken),
    },
  );
}
