// F-4b：Artifact Flow 观测面板（§11.2 快照扩展的前端消费面）。
//
// 数据源：/api/runtime/status → runtime.tool_efficiency.artifact_flow
// （getToolEfficiencySnapshot，F-4a）。四块子视图：
//   1. 归档（archives：total / by_layer / by_disposition）
//   2. 截断（truncations：total / by_layer / by_truncated_by + L1/L4 gap 比例）
//   3. 指针提示（pointer_notice：按 reason 分布）
//   4. artifact_read 解引用（deref：total / followup_ratio / miss_by_reason）
//
// 降级矩阵（与 getUsageAnalyticsHealth 同口径）：
//   - 快照为 null（403 / 网络失败 / 缺块）→ 整块不渲染（返回 null），
//     绝不伪造成零计数；由调用方决定是否显示其他提示。
//   - 快照存在但全部计数为 0（空运行时）→ 渲染「暂无数据」空态。
//   - inefficiency_flags 非空 → 顶部警告条列出派生 flag。
//   - l1_l4_gap_ratio ≥ 0.5 → L1/L4 竞争警告（H-1/P0-1 信号）。

import type { AnalyticsToolEfficiencySnapshot } from "@/types/runtime";
import { AlertTriangleIcon, RefreshCwIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { formatNumber, formatPercent } from "./format";

const GAP_RATIO_WARNING_THRESHOLD = 0.5;

export function ArtifactFlowPanel({
  snapshot,
  loading,
}: {
  snapshot: AnalyticsToolEfficiencySnapshot | null;
  loading: boolean;
}) {
  const { t } = useTranslation("usageAnalytics");
  if (loading && !snapshot) {
    return (
      <section
        aria-labelledby="artifact-flow-title"
        data-testid="artifact-flow-loading"
        className="surface-panel min-w-0 rounded-panel-lg p-3 sm:p-4"
      >
        <h3 id="artifact-flow-title" className="text-sm font-semibold">
          {t("observability.artifactFlow.title")}
        </h3>
        <div className="flex items-center justify-center gap-2 py-8 text-sm text-muted-foreground">
          <RefreshCwIcon size={15} className="animate-spin" />
          {t("observability.loading")}
        </div>
      </section>
    );
  }
  // 快照缺失（403 / 网络失败 / 后端未填块）→ 不渲染，不伪造零计数。
  if (!snapshot) {
    return null;
  }

  const flow = snapshot.artifact_flow;
  const archivesTotal = flow.archives.total;
  const truncationsTotal = flow.truncations.total;
  const pointerTotal = Object.values(flow.pointer_notice).reduce(
    (sum, value) => sum + value,
    0,
  );
  const derefTotal = flow.deref.total;
  const allZero =
    archivesTotal === 0 &&
    truncationsTotal === 0 &&
    pointerTotal === 0 &&
    derefTotal === 0;
  const flags = snapshot.inefficiency_flags ?? [];
  const gapRatio = flow.l1_l4_gap_ratio;
  const gapWarning = gapRatio >= GAP_RATIO_WARNING_THRESHOLD;

  return (
    <section
      aria-labelledby="artifact-flow-title"
      data-testid="artifact-flow-panel"
      className="surface-panel min-w-0 rounded-panel-lg p-3 sm:p-4"
    >
      <div className="mb-1 flex flex-wrap items-baseline justify-between gap-x-3">
        <h3 id="artifact-flow-title" className="text-sm font-semibold">
          {t("observability.artifactFlow.title")}
        </h3>
        <span className="text-xs text-muted-foreground tabular-nums">
          {t("observability.artifactFlow.capturedAt", {
            time: formatTimestampValue(snapshot.captured_at),
          })}
        </span>
      </div>
      <p className="mb-3 text-xs text-muted-foreground">
        {t("observability.artifactFlow.subtitle")}
      </p>

      {flags.length > 0 ? (
        <div
          data-testid="artifact-flow-flags"
          className="mb-3 flex items-start gap-2 rounded-panel border border-analytics-warning-border bg-analytics-warning-soft px-3 py-2.5 text-sm text-analytics-warning"
        >
          <AlertTriangleIcon size={16} className="mt-0.5 shrink-0" />
          <div className="min-w-0 flex-1">
            <div className="font-medium">
              {t("observability.artifactFlow.flagsTitle")}
            </div>
            <ul className="mt-1 list-disc pl-4 text-xs">
              {flags.map((flag) => (
                <li key={flag} className="font-mono break-all">
                  {flag}
                </li>
              ))}
            </ul>
          </div>
        </div>
      ) : null}

      {allZero ? (
        <div
          data-testid="artifact-flow-empty"
          className="rounded-card border border-border bg-surface-softer px-3 py-8 text-center text-sm text-muted-foreground"
        >
          {t("observability.artifactFlow.empty")}
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <FlowCard
            testId="artifact-flow-archives"
            title={t("observability.artifactFlow.archives.title")}
            total={archivesTotal}
            rows={[
              ...mapRows(flow.archives.by_layer, "observability.artifactFlow.layerLabels"),
              ...mapRows(flow.archives.by_disposition, "observability.artifactFlow.dispositionLabels"),
            ]}
          />
          <FlowCard
            testId="artifact-flow-truncations"
            title={t("observability.artifactFlow.truncations.title")}
            total={truncationsTotal}
            rows={[
              ...mapRows(flow.truncations.by_layer, "observability.artifactFlow.layerLabels"),
              ...mapRows(flow.truncations.by_truncated_by, "observability.artifactFlow.truncatedByLabels"),
            ]}
            footnote={
              gapRatio > 0
                ? t("observability.artifactFlow.truncations.gapRatio", {
                    ratio: formatPercent(gapRatio),
                  })
                : undefined
            }
            warn={gapWarning}
          />
          <FlowCard
            testId="artifact-flow-pointer"
            title={t("observability.artifactFlow.pointer.title")}
            total={pointerTotal}
            rows={mapRows(flow.pointer_notice, "observability.artifactFlow.pointerReasonLabels")}
          />
          <FlowCard
            testId="artifact-flow-deref"
            title={t("observability.artifactFlow.deref.title")}
            total={derefTotal}
            rows={[
              ...mapRows(flow.deref.miss_by_reason, "observability.artifactFlow.derefReasonLabels"),
            ]}
            footnote={
              derefTotal > 0
                ? t("observability.artifactFlow.deref.followupRatio", {
                    ratio: formatPercent(flow.deref.followup_ratio),
                  })
                : undefined
            }
          />
        </div>
      )}
    </section>
  );
}

function FlowCard({
  title,
  total,
  rows,
  footnote,
  warn,
  testId,
}: {
  title: string;
  total: number;
  rows: { key: string; label: string; count: number; ratio: number | null }[];
  footnote?: string;
  warn?: boolean;
  testId: string;
}) {
  const { t } = useTranslation("usageAnalytics");
  return (
    <div
      data-testid={testId}
      className="min-w-0 rounded-card border border-border p-3"
    >
      <div className="flex flex-wrap items-baseline justify-between gap-x-2">
        <span className="text-xs font-medium text-muted-foreground">{title}</span>
        <span className="text-sm font-semibold tabular-nums">
          {formatNumber(total)}
        </span>
      </div>
      {rows.length === 0 ? (
        <div className="mt-2 text-xs text-muted-foreground">
          {t("observability.artifactFlow.noBreakdown")}
        </div>
      ) : (
        <ul className="mt-2 space-y-1">
          {rows.map((row) => {
            // 已知 label key 走 i18n 映射；未知 key 由 missingKeyHandler 回显原文。
            const label = row.label.includes(".")
              ? (t(row.label as never) as string)
              : row.label;
            return (
              <li
                key={row.key}
                className="flex items-baseline justify-between gap-2 text-xs"
              >
                <span className="min-w-0 truncate font-mono" title={label}>
                  {label}
                </span>
                <span className="shrink-0 tabular-nums text-muted-foreground">
                  {formatNumber(row.count)}
                  {row.ratio !== null ? ` · ${formatPercent(row.ratio)}` : ""}
                </span>
              </li>
            );
          })}
        </ul>
      )}
      {footnote ? (
        <div
          className={`mt-2 text-xs ${warn ? "text-analytics-warning" : "text-muted-foreground"}`}
        >
          {footnote}
        </div>
      ) : null}
    </div>
  );
}

/**
 * 把 backend 计数 map 折叠为面板行：label 走 i18n 已知 key 映射，
 * 未知 key 原样显示（后端标签是通用 reason/layer 词表，不是工具名）。
 * 仅保留非零计数；按计数降序稳定排序。
 */
function mapRows(
  source: Record<string, number>,
  labelPrefix: string,
): { key: string; label: string; count: number; ratio: number | null }[] {
  const total = Object.values(source).reduce((sum, value) => sum + value, 0);
  return Object.entries(source)
    .filter(([, value]) => value !== 0)
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([key, value]) => ({
      key,
      label: labelKey(labelPrefix, key),
      count: value,
      ratio: total > 0 ? value / total : null,
    }));
}

/** 已知 label → i18n key；未知 key 交给 i18n fallback（返回原 key 文本由调用处处理）。 */
function labelKey(prefix: string, key: string): string {
  // 调用方使用 t(key) 时 i18next 找不到 key 会回显 key 本身，
  // 因此这里直接返回拼接 key，由 t() 的 missingKeyHandler 决定展示。
  return `${prefix}.${key}`;
}

function formatTimestampValue(value: string): string {
  const parsed = Date.parse(value);
  if (!Number.isFinite(parsed)) {
    return value;
  }
  return new Date(parsed).toLocaleTimeString();
}
