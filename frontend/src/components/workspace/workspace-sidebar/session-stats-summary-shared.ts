// P2-1A：会话统计摘要的纯展示逻辑（与组件分离，便于单测）。
//
// 口径：
//   * 总数恒定展示；其余计数为 0 时不展示（省略而非伪造，不渲染 0 值噪声）；
//   * 标签分布不在此处展开（避免侧栏信息过载），仅供计数摘要。

import type { TFunction } from "i18next";

import type { RuntimeSessionStats } from "@/types/runtime";

export type SessionStatsChip = {
  key: string;
  label: string;
  value: number;
};

/** 会话统计 → 侧栏 chip 列表；total 恒定在首，其余仅非零展示。 */
export function buildSessionStatsChips(
  stats: RuntimeSessionStats,
  t: TFunction<"workspace">,
): SessionStatsChip[] {
  const chips: SessionStatsChip[] = [
    {
      key: "total",
      label: t("sidebar.sessionStats.total", { count: stats.total }),
      value: stats.total,
    },
  ];

  if (stats.active > 0) {
    chips.push({
      key: "active",
      label: t("sidebar.sessionStats.active", { count: stats.active }),
      value: stats.active,
    });
  }
  if (stats.idle > 0) {
    chips.push({
      key: "idle",
      label: t("sidebar.sessionStats.idle", { count: stats.idle }),
      value: stats.idle,
    });
  }
  if (stats.closed > 0) {
    chips.push({
      key: "closed",
      label: t("sidebar.sessionStats.closed", { count: stats.closed }),
      value: stats.closed,
    });
  }
  if (stats.archived > 0) {
    chips.push({
      key: "archived",
      label: t("sidebar.sessionStats.archived", { count: stats.archived }),
      value: stats.archived,
    });
  }
  if (stats.totalMessages > 0) {
    chips.push({
      key: "totalMessages",
      label: t("sidebar.sessionStats.totalMessages", {
        count: stats.totalMessages,
      }),
      value: stats.totalMessages,
    });
  }

  return chips;
}

/** 统计失败时的可读原因（非 Error 兜底 String，避免显示 [object Object] 之外的空白）。 */
export function describeSessionStatsError(error: unknown): string {
  if (error instanceof Error) {
    return error.message;
  }
  if (typeof error === "string") {
    return error;
  }
  return String(error);
}
