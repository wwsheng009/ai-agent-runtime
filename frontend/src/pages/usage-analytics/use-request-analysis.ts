import { useCallback, useEffect, useState } from "react";

import { listAnalyticsRequests } from "@/api/runtime/analytics";
import {
  toUsageQuotaSectionError,
  type UsageQuotaSectionError,
} from "@/hooks/use-usage-quota";
import type { AnalyticsRequestsQuery, AnalyticsRequestsResponse } from "@/types/runtime";

export type UseRequestAnalysisOptions = AnalyticsRequestsQuery & {
  adminToken?: string;
};

export type UseRequestAnalysisResult = {
  response: AnalyticsRequestsResponse | null;
  loading: boolean;
  error: UsageQuotaSectionError | null;
  refresh: () => void;
};

export function useRequestAnalysis({
  adminToken,
  from,
  to,
  q,
  provider,
  model,
  directory,
  project,
  status,
  success,
  cache_status,
  cache_hit_ratio_min,
  cache_hit_ratio_max,
  cache_hit_ratio_lt,
  cache_hit_ratio_gte,
  limit,
}: UseRequestAnalysisOptions): UseRequestAnalysisResult {
  const [response, setResponse] = useState<AnalyticsRequestsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<UsageQuotaSectionError | null>(null);
  const [refreshToken, setRefreshToken] = useState(0);
  const token = adminToken?.trim() ?? "";

  const refresh = useCallback(() => {
    setRefreshToken((current) => current + 1);
  }, []);

  useEffect(() => {
    let active = true;
    setLoading(true);

    void listAnalyticsRequests({
      from,
      to,
      q,
      provider,
      model,
      directory,
      project,
      status,
      success,
      cache_status,
      cache_hit_ratio_min,
      cache_hit_ratio_max,
      cache_hit_ratio_lt,
      cache_hit_ratio_gte,
      limit,
      adminToken: token,
    })
      .then((value) => {
        if (!active) {
          return;
        }
        setResponse(value);
        setError(null);
      })
      .catch((caught: unknown) => {
        if (!active) {
          return;
        }
        setError(toUsageQuotaSectionError(caught));
      })
      .finally(() => {
        if (active) {
          setLoading(false);
        }
      });

    return () => {
      active = false;
    };
  }, [
    cache_hit_ratio_gte,
    cache_hit_ratio_max,
    cache_hit_ratio_min,
    cache_hit_ratio_lt,
    cache_status,
    directory,
    from,
    limit,
    model,
    project,
    provider,
    q,
    refreshToken,
    status,
    success,
    to,
    token,
  ]);

  return { response, loading, error, refresh };
}
