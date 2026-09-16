// P2-1A：用量 / 配额面板（`/usage/stats|ledger|policy`）数据 hook。
//
// 刷新语义：
// 1. 三段数据并行拉取：stats（全局聚合或指定作用域）、policy（配额明细）、ledger（持久化账本）；
// 2. 任一段失败不遮蔽其余段：错误按段暴露；403（缺 admin token）与 503（账本未配置）
//    由 UI 分类提示，不做「假装正常」的降级；
// 3. 作用域、账本筛选、admin token 变化即重新拉取，切换时用 requestId 丢弃在途响应，
//    避免慢响应覆盖新作用域的数据；
// 4. 失败段保留上一次成功数据但错误标记不清除（调用方自行决定呈现口径）。

import { useCallback, useEffect, useRef, useState } from "react";

import { getUsageLedger, getUsagePolicy, getUsageStats } from "@/api/runtime";
import {
  DEFAULT_USAGE_LEDGER_LIMIT,
  type UsageLedgerQuery,
} from "@/api/runtime/usage";
import type {
  UsageLedgerView,
  UsagePolicyDetails,
  UsageScope,
  UsageStatsView,
} from "@/types/runtime";

export type UsageQuotaScope = {
  tenantId?: string;
  projectId?: string;
  userId?: string;
};

export type UsageQuotaLedgerFilters = Pick<
  UsageLedgerQuery,
  "entrypoint" | "skill" | "success" | "since" | "limit"
>;

export type UsageQuotaSectionError = {
  message: string;
  status: number | null;
};

export type UseUsageQuotaOptions = {
  adminToken?: string;
  enabled?: boolean;
  scope?: UsageQuotaScope;
  ledgerFilters?: UsageQuotaLedgerFilters;
};

export type UseUsageQuotaResult = {
  stats: UsageStatsView | null;
  policy: UsagePolicyDetails | null;
  ledger: UsageLedgerView | null;
  /** 最近一次全局响应里的作用域列表；切到具体作用域后仍保留，供选择器回选。 */
  knownScopes: UsageScope[];
  loading: boolean;
  statsError: UsageQuotaSectionError | null;
  policyError: UsageQuotaSectionError | null;
  ledgerError: UsageQuotaSectionError | null;
  refresh: () => void;
};

/** 把 fetch 抛出的错误收敛为「带 status 的分段错误」，供 UI 区分 403 / 503。 */
export function toUsageQuotaSectionError(caught: unknown): UsageQuotaSectionError {
  const message = caught instanceof Error ? caught.message : String(caught);
  const rawStatus = (caught as { status?: unknown } | null | undefined)?.status;
  return {
    message,
    status: typeof rawStatus === "number" && Number.isFinite(rawStatus) ? rawStatus : null,
  };
}

/** 两个用量端点均受 `authorizeUsageAdmin` 限制，403 表示 admin token 缺失或被拒绝。 */
export function isUsageQuotaAdminForbidden(
  error: UsageQuotaSectionError | null | undefined,
): boolean {
  return error?.status === 403;
}

/** 账本端点在后端未配置 usage ledger 时返回 503。 */
export function isUsageQuotaLedgerUnavailable(
  error: UsageQuotaSectionError | null | undefined,
): boolean {
  return error?.status === 503;
}

/**
 * 账本 503 的两种成因语义不同，UI 不能混为一谈：
 * - 配置未启用：`usage ledger not configured`（`skills_runtime.usage_ledger_enabled` 为 false）；
 * - 已启用但初始化失败：`usage ledger unavailable: <reason>`（dsn 为空 / 驱动不是 sqlite /
 *   建表失败 / 目录不可写，由后端启动期降级为 503，服务本身仍可用）。
 * 只有明确出现 `not configured` 才判定为「未启用」，其余 503 一律按「启用但不可用」处理，
 * 避免把初始化失败误报成「开关没开」，让用户少走一圈排查。
 */
export function isUsageQuotaLedgerDisabled(
  error: UsageQuotaSectionError | null | undefined,
): boolean {
  if (!isUsageQuotaLedgerUnavailable(error)) {
    return false;
  }
  return (error?.message ?? "").includes("usage ledger not configured");
}

export function useUsageQuota({
  adminToken,
  enabled = true,
  ledgerFilters,
  scope,
}: UseUsageQuotaOptions = {}): UseUsageQuotaResult {
  const [stats, setStats] = useState<UsageStatsView | null>(null);
  const [policy, setPolicy] = useState<UsagePolicyDetails | null>(null);
  const [ledger, setLedger] = useState<UsageLedgerView | null>(null);
  const [knownScopes, setKnownScopes] = useState<UsageScope[]>([]);
  const [loading, setLoading] = useState(false);
  const [statsError, setStatsError] = useState<UsageQuotaSectionError | null>(null);
  const [policyError, setPolicyError] = useState<UsageQuotaSectionError | null>(null);
  const [ledgerError, setLedgerError] = useState<UsageQuotaSectionError | null>(null);
  const [refreshToken, setRefreshToken] = useState(0);
  const requestIdRef = useRef(0);

  const token = adminToken?.trim() ?? "";
  const tenantId = scope?.tenantId?.trim() ?? "";
  const projectId = scope?.projectId?.trim() ?? "";
  const userId = scope?.userId?.trim() ?? "";
  const entrypoint = ledgerFilters?.entrypoint?.trim() ?? "";
  const skill = ledgerFilters?.skill?.trim() ?? "";
  const success = ledgerFilters?.success;
  const since = ledgerFilters?.since?.trim() ?? "";
  const limit = ledgerFilters?.limit ?? DEFAULT_USAGE_LEDGER_LIMIT;

  const refresh = useCallback(() => {
    setRefreshToken((current) => current + 1);
  }, []);

  useEffect(() => {
    if (!enabled) {
      return;
    }

    const requestId = requestIdRef.current + 1;
    requestIdRef.current = requestId;

    async function load() {
      setLoading(true);
      const [statsResult, policyResult, ledgerResult] = await Promise.allSettled([
        getUsageStats({ tenantId, projectId, userId, adminToken: token }),
        getUsagePolicy({ adminToken: token }),
        getUsageLedger({
          tenantId,
          projectId,
          userId,
          entrypoint,
          skill,
          success,
          since,
          limit,
          adminToken: token,
        }),
      ]);

      if (requestIdRef.current !== requestId) {
        return;
      }

      if (statsResult.status === "fulfilled") {
        setStats(statsResult.value);
        setStatsError(null);
        if (statsResult.value.scopes.length > 0) {
          setKnownScopes(statsResult.value.scopes);
        }
      } else {
        setStatsError(toUsageQuotaSectionError(statsResult.reason));
      }

      if (policyResult.status === "fulfilled") {
        setPolicy(policyResult.value);
        setPolicyError(null);
      } else {
        setPolicyError(toUsageQuotaSectionError(policyResult.reason));
      }

      if (ledgerResult.status === "fulfilled") {
        setLedger(ledgerResult.value);
        setLedgerError(null);
      } else {
        setLedgerError(toUsageQuotaSectionError(ledgerResult.reason));
      }

      setLoading(false);
    }

    void load();
  }, [
    enabled,
    entrypoint,
    limit,
    projectId,
    refreshToken,
    since,
    skill,
    success,
    tenantId,
    token,
    userId,
  ]);

  return {
    stats,
    policy,
    ledger,
    knownScopes,
    loading,
    statsError,
    policyError,
    ledgerError,
    refresh,
  };
}
