import type {
  AnalyticsDimensionsResponse,
  AnalyticsSessionUsageDetail,
  AnalyticsSessionsQuery,
  AnalyticsSessionsResponse,
  AnalyticsSummaryQuery,
  AnalyticsSummaryResponse,
} from "@/types/runtime";

import { buildRuntimeUrlWithQuery, fetchRuntimeJson } from "./shared";

type AnalyticsRequestOptions = {
  adminToken?: string;
};

function buildAnalyticsHeaders(adminToken?: string) {
  const token = adminToken?.trim();
  if (!token) {
    return {} as Record<string, string>;
  }
  return {
    Authorization: `Bearer ${token}`,
  } satisfies Record<string, string>;
}

export async function listAnalyticsSessions(
  options: AnalyticsSessionsQuery & AnalyticsRequestOptions = {},
): Promise<AnalyticsSessionsResponse> {
  return fetchRuntimeJson<AnalyticsSessionsResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/analytics/sessions", {
      from: options.from,
      to: options.to,
      provider: options.provider,
      model: options.model,
      directory: options.directory,
      project: options.project,
      status: options.status,
      q: options.q,
      limit: options.limit,
      offset: options.offset,
      max_scan: options.max_scan,
    }),
    {
      headers: buildAnalyticsHeaders(options.adminToken),
    },
  );
}

export async function getAnalyticsSummary(
  options: AnalyticsSummaryQuery & AnalyticsRequestOptions = {},
): Promise<AnalyticsSummaryResponse> {
  return fetchRuntimeJson<AnalyticsSummaryResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/analytics/overview", {
      from: options.from,
      to: options.to,
      provider: options.provider,
      model: options.model,
      directory: options.directory,
      project: options.project,
      status: options.status,
      q: options.q,
      group_by: options.group_by,
      limit: options.limit,
      offset: options.offset,
      max_scan: options.max_scan,
    }),
    {
      headers: buildAnalyticsHeaders(options.adminToken),
    },
  );
}

export async function getAnalyticsDimensions(
  options: Pick<AnalyticsSessionsQuery, "max_scan"> & AnalyticsRequestOptions = {},
): Promise<AnalyticsDimensionsResponse> {
  return fetchRuntimeJson<AnalyticsDimensionsResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/analytics/dimensions", {
      max_scan: options.max_scan,
    }),
    {
      headers: buildAnalyticsHeaders(options.adminToken),
    },
  );
}

export async function getAnalyticsSessionUsage(
  sessionId: string,
  options: AnalyticsRequestOptions = {},
): Promise<AnalyticsSessionUsageDetail> {
  const encoded = encodeURIComponent(sessionId);
  return fetchRuntimeJson<AnalyticsSessionUsageDetail>(
    buildRuntimeUrlWithQuery(`/api/runtime/analytics/sessions/${encoded}`, {}),
    {
      headers: buildAnalyticsHeaders(options.adminToken),
    },
  );
}
