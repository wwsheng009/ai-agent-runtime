// 由 pages/usage-analytics/quota.tsx 机械拆分而来（P0-2 A2 复检处置），仅搬迁不改语义：
// 用量 / 配额面板的常量、负载形状辅助与纯格式函数（不含组件，见 ./quota-atoms）。

import type { UsageQuotaSectionError } from "@/hooks/use-usage-quota";
import type { UsageLedgerRecord, UsageQuotaLevel } from "@/types/runtime";

import { shortID } from "./format";

const nilUUID = "00000000-0000-0000-0000-000000000000";

export const ledgerLimitOptions = [20, 50, 100, 200].map((value) => ({
  value: String(value),
  label: String(value),
}));

export type AppliedLedgerFilters = {
  entrypoint: string;
  skill: string;
  success?: boolean;
  limit: number;
};

export type SectionErrorEntry = {
  key: string;
  label: string;
  error: UsageQuotaSectionError | null;
};

export const defaultLedgerLimit = 50;

export function clampLedgerLimit(raw: string): number {
  const parsed = Number.parseInt(raw, 10);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return defaultLedgerLimit;
  }
  return Math.min(parsed, 200);
}

export function metaText(record: UsageLedgerRecord, key: string): string {
  const value = record.metadata[key];
  return typeof value === "string" ? value : "";
}

export function levelKey(
  level: UsageQuotaLevel,
):
  | "quota.policy.levels.tenant"
  | "quota.policy.levels.project"
  | "quota.policy.levels.user" {
  switch (level) {
    case "project":
      return "quota.policy.levels.project";
    case "user":
      return "quota.policy.levels.user";
    default:
      return "quota.policy.levels.tenant";
  }
}

// 后端 `resolveQuotaForScope` 的 ResolvedFrom 为 default|tenant|project|user。
export function resolvedFromKey(
  value: string,
):
  | "quota.limit.resolved.default"
  | "quota.limit.resolved.tenant"
  | "quota.limit.resolved.project"
  | "quota.limit.resolved.user" {
  switch (value) {
    case "tenant":
      return "quota.limit.resolved.tenant";
    case "project":
      return "quota.limit.resolved.project";
    case "user":
      return "quota.limit.resolved.user";
    default:
      return "quota.limit.resolved.default";
  }
}

export function displayModel(record: UsageLedgerRecord): string {
  const model = record.model_id?.trim() ?? "";
  if (!model || model === nilUUID) {
    return "-";
  }
  return shortID(model);
}
