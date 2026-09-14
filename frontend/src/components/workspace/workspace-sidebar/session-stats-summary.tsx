// P2-1A：侧栏会话统计摘要（GET /api/runtime/sessions/stats）。
//
// 呈现纪律：
//   * 加载中且无历史数据 → 一行轻量「统计加载中」，不渲染骨架数值；
//   * 404/405/501/503 → 「统计不可用」独立行（附状态信息），绝不回落成 0 计数；
//   * 其他失败 → 「统计加载失败」行并保留原因，可手动重试；
//   * 成功 → 计数 chips（total 恒显，其余非零才显）。

import { LoaderCircleIcon, RefreshCwIcon } from "lucide-react";
import { type TFunction } from "i18next";

import type { SessionStatsStatus } from "@/hooks/workspace/use-session-stats";
import type { RuntimeSessionStats } from "@/types/runtime";

import {
  buildSessionStatsChips,
  describeSessionStatsError,
} from "./session-stats-summary-shared";

type WorkspaceSidebarSessionStatsSummaryProps = {
  error: unknown;
  onRefresh: () => void;
  stats: RuntimeSessionStats | null;
  status: SessionStatsStatus;
  t: TFunction<"workspace">;
  unavailable: boolean;
};

export function WorkspaceSidebarSessionStatsSummary({
  error,
  onRefresh,
  stats,
  status,
  t,
  unavailable,
}: WorkspaceSidebarSessionStatsSummaryProps) {
  const loading = status === "loading";

  return (
    <div className="space-y-1.5">
      {stats ? (
        <div
          className="flex flex-wrap items-center gap-1"
          data-testid="session-stats-summary"
        >
          {buildSessionStatsChips(stats, t).map((chip) => (
            <span
              key={chip.key}
              className="rounded-control border border-border bg-surface-soft px-1.5 py-0.5 app-text-10 uppercase tracking-[0.12em] text-muted-foreground"
              data-testid={`session-stats-chip-${chip.key}`}
            >
              {chip.label}
            </span>
          ))}
          <button
            type="button"
            aria-label={t("sidebar.sessionStats.refresh")}
            className="grid size-5 place-items-center rounded-control border border-border bg-surface-soft text-muted-foreground transition hover:text-foreground"
            data-testid="session-stats-refresh"
            disabled={loading}
            onClick={onRefresh}
            title={t("sidebar.sessionStats.refresh")}
          >
            {loading ? (
              <LoaderCircleIcon size={11} className="animate-spin" />
            ) : (
              <RefreshCwIcon size={11} />
            )}
          </button>
        </div>
      ) : null}

      {loading && !stats ? (
        <div
          className="inline-flex items-center gap-1.5 rounded-control border border-border bg-surface-soft px-2 py-1 app-text-10 uppercase tracking-[0.14em] text-muted-foreground"
          data-testid="session-stats-loading"
        >
          <LoaderCircleIcon size={12} className="animate-spin" />
          {t("sidebar.sessionStats.loading")}
        </div>
      ) : null}

      {status === "error" ? (
        <div
          className={
            unavailable
              ? "rounded-[0.75rem] border border-border bg-surface-soft px-2.5 py-2 text-xs leading-5 text-muted-foreground"
              : "rounded-[0.75rem] border border-accent-orange/18 bg-accent-orange/8 px-2.5 py-2 text-xs leading-5 text-muted-foreground"
          }
          data-testid={
            unavailable ? "session-stats-unavailable" : "session-stats-error"
          }
        >
          <div className="flex items-center justify-between gap-2">
            <span className="font-semibold">
              {unavailable
                ? t("sidebar.sessionStats.unavailable")
                : t("sidebar.sessionStats.error")}
            </span>
            <button
              type="button"
              className="shrink-0 underline-offset-2 hover:underline"
              data-testid="session-stats-retry"
              onClick={onRefresh}
            >
              {t("sidebar.sessionStats.retry")}
            </button>
          </div>
          <div className="mt-0.5 break-words">{describeSessionStatsError(error)}</div>
        </div>
      ) : null}
    </div>
  );
}
