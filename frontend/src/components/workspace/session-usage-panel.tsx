// 工作台右侧「会话用量」面板：展示当前会话的 token/请求/轮次/覆盖率摘要，
// 并提供在新窗口打开 /usage/sessions/:sessionId 完整用量明细的链接。
// 只读展示；数据获取与刷新语义见 hooks/workspace/use-session-usage.ts。

import { AlertTriangleIcon, ArrowUpRightIcon, ChartNoAxesCombinedIcon, RefreshCwIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { buttonVariants } from "@/components/ui/button-variants";
import {
  formatUsageDuration,
  formatUsageNumber,
  formatUsageRate,
  formatUsageTimestamp,
  hasSessionUsageData,
  resolveSessionUsageSummary,
  sessionUsageHref,
} from "@/components/workspace/session-usage-panel-shared";
import { useSessionUsage } from "@/hooks/workspace/use-session-usage";
import { cn } from "@/lib/utils";

export type SessionUsagePanelProps = {
  isResponding?: boolean;
  sessionId: string;
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
  className?: string;
};

export function SessionUsagePanel({
  className,
  isResponding = false,
  lastRuntimeEventType,
  runtimeEventCount,
  sessionId,
}: SessionUsagePanelProps) {
  const { t } = useTranslation("workspace");
  const { error, loading, refresh, usage } = useSessionUsage({
    lastRuntimeEventType,
    live: isResponding,
    runtimeEventCount,
    sessionId,
  });
  const summary = resolveSessionUsageSummary(usage);
  const hasData = hasSessionUsageData(summary);
  const detailHref = sessionUsageHref(sessionId);
  const providerLine = [summary?.provider, summary?.model].filter(Boolean).join(" / ");

  const metrics = summary
    ? [
        {
          key: "tokens",
          label: t("usagePanel.metrics.tokens"),
          value: formatUsageNumber(summary.totalTokens),
        },
        {
          key: "requests",
          label: t("usagePanel.metrics.requests"),
          value: formatUsageNumber(summary.llmRequests),
        },
        {
          key: "turns",
          label: t("usagePanel.metrics.turns"),
          value: formatUsageNumber(summary.turns),
        },
        {
          key: "cacheHit",
          label: t("usagePanel.metrics.cacheHit"),
          value: formatUsageRate(summary.cacheHitRatio),
        },
        {
          key: "averageResponse",
          label: t("usagePanel.metrics.averageResponse"),
          value: formatUsageDuration(summary.averageResponseTimeMs),
        },
        {
          key: "coverage",
          label: t("usagePanel.metrics.coverage"),
          value: formatUsageRate(summary.usageCoverage),
        },
      ]
    : [];

  return (
    <section
      aria-label={t("usagePanel.ariaLabel")}
      className={cn(
        "flex min-h-0 flex-col gap-2.5 border-b border-white/8 px-3 py-3",
        className,
      )}
      data-testid="session-usage-panel"
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <h2 className="flex items-center gap-1.5 text-xs font-semibold text-foreground">
            <ChartNoAxesCombinedIcon size={14} className="text-accent-primary" />
            {t("usagePanel.title")}
          </h2>
          <p
            className="mt-1 truncate text-xs text-muted-foreground"
            title={providerLine || t("usagePanel.unknownProvider")}
          >
            {providerLine || t("usagePanel.unknownProvider")}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <Button
            aria-label={t("usagePanel.refresh")}
            className="h-7 w-7 px-0"
            disabled={loading || !sessionId.trim()}
            onClick={refresh}
            size="sm"
            title={t("usagePanel.refresh")}
            variant="ghost"
          >
            <RefreshCwIcon size={14} className={cn(loading && "animate-spin")} />
          </Button>
          <Link
            aria-label={t("usagePanel.openFullReportHint")}
            className={cn(buttonVariants({ size: "sm", variant: "ghost" }), "h-7 gap-1 px-2 text-xs")}
            rel="noopener noreferrer"
            target="_blank"
            title={t("usagePanel.openFullReportHint")}
            to={detailHref}
          >
            {t("usagePanel.openFullReport")}
            <ArrowUpRightIcon size={13} />
          </Link>
        </div>
      </div>

      {error ? (
        <div className="flex items-start gap-2 rounded-card border border-border bg-surface-softer px-2.5 py-2 text-xs text-analytics-danger">
          <AlertTriangleIcon size={14} className="mt-0.5 shrink-0" />
          <div className="min-w-0">
            <div className="font-medium">{t("usagePanel.errorTitle")}</div>
            <div className="mt-0.5 break-words text-muted-foreground">{error}</div>
          </div>
        </div>
      ) : null}

      {!error && loading && !summary ? (
        <div className="flex items-center gap-2 rounded-card border border-border bg-surface-softer px-2.5 py-2 text-xs text-muted-foreground">
          <RefreshCwIcon size={13} className="animate-spin" />
          {t("usagePanel.loading")}
        </div>
      ) : null}

      {!error && !loading && !hasData ? (
        <div className="rounded-card border border-border bg-surface-softer px-2.5 py-2 text-xs text-muted-foreground">
          <div className="font-medium text-foreground">{t("usagePanel.empty")}</div>
          <p className="mt-1 leading-5">{t("usagePanel.emptyHint")}</p>
        </div>
      ) : null}

      {hasData && summary ? (
        <>
          <dl className="grid grid-cols-2 gap-x-3 gap-y-2">
            {metrics.map((metric) => (
              <div key={metric.key} className="min-w-0">
                <dt className="app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
                  {metric.label}
                </dt>
                <dd className="mt-0.5 truncate text-sm font-medium tabular-nums" title={metric.value}>
                  {metric.value}
                </dd>
              </div>
            ))}
          </dl>
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
            {summary.status ? (
              <Badge className="h-5 px-1.5 app-text-10">{summary.status}</Badge>
            ) : null}
            <span>
              {t("usagePanel.quality", {
                coverage: formatUsageRate(summary.usageCoverage),
                quality: summary.usageQuality || t("usagePanel.qualityUnknown"),
              })}
            </span>
            {summary.observedAt ? (
              <span title={summary.observedAt}>
                {t("usagePanel.updatedAt", {
                  time: formatUsageTimestamp(summary.observedAt),
                })}
              </span>
            ) : null}
          </div>
          {summary.partial ? (
            <p className="text-xs text-analytics-warning">{t("usagePanel.partial")}</p>
          ) : null}
        </>
      ) : null}
    </section>
  );
}
