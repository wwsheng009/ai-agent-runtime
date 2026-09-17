// 批次 7.2：失败模式 Top-N 面板（GET /api/runtime/analytics/errors）。
//
// 契约：{ schema_version, generated_at, patterns: ErrorPattern[] }，
// ErrorPattern = { error_code?, failure_category?, source, count }。
// 空库返回空数组 → 渲染「暂无数据」；点击行切换 `?tab=diagnostics` 并带入失败分类过滤。
// 本文件同时导出三端复用的失败分类/来源标签映射（zh-CN 为 key 真源，en-US 对齐）。

import { listAnalyticsErrors } from "@/api/runtime/analytics";
import { Badge } from "@/components/ui/badge";
import { Select } from "@/components/ui/select";
import type { AnalyticsErrorPattern } from "@/types/runtime";
import type { TFunction } from "i18next";
import { AlertTriangleIcon, RefreshCwIcon } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { formatNumber } from "./format";

/** 失败分类归一表（backend/internal/llm/failure_category.go）→ i18n key。 */
const failureCategoryKeys = {
  provider_error: "observability.failureCategories.providerError",
  rate_limited: "observability.failureCategories.rateLimited",
  timeout: "observability.failureCategories.timeout",
  context_overflow: "observability.failureCategories.contextOverflow",
  tool_error: "observability.failureCategories.toolError",
  budget_exceeded: "observability.failureCategories.budgetExceeded",
  cancelled: "observability.failureCategories.cancelled",
  interrupted: "observability.failureCategories.interrupted",
  unknown: "observability.failureCategories.unknown",
} as const;

type FailureCategoryKey = (typeof failureCategoryKeys)[keyof typeof failureCategoryKeys];

/** 失败模式来源 → i18n key（后端固定 tools|subagents|requests）。 */
const analyticsSourceKeys = {
  tools: "observability.errors.sourceLabels.tools",
  subagents: "observability.errors.sourceLabels.subagents",
  requests: "observability.errors.sourceLabels.requests",
} as const;

type AnalyticsSourceKey = (typeof analyticsSourceKeys)[keyof typeof analyticsSourceKeys];

// 注意：本文件导出 React 组件，helper 一律不导出（react-refresh/only-export-components）。
function failureCategoryKey(category?: string): FailureCategoryKey | null {
  const normalized = (category ?? "").trim().toLowerCase();
  return failureCategoryKeys[normalized as keyof typeof failureCategoryKeys] ?? null;
}

function analyticsSourceKey(source?: string): AnalyticsSourceKey | null {
  const normalized = (source ?? "").trim().toLowerCase();
  return analyticsSourceKeys[normalized as keyof typeof analyticsSourceKeys] ?? null;
}

function failureCategoryLabel(
  t: TFunction<"usageAnalytics">,
  category?: string,
): string {
  const key = failureCategoryKey(category);
  if (key) return t(key);
  return (category ?? "").trim() || t("observability.errors.unknown");
}

function analyticsSourceLabel(
  t: TFunction<"usageAnalytics">,
  source?: string,
): string {
  const key = analyticsSourceKey(source);
  if (key) return t(key);
  return (source ?? "").trim() || t("observability.errors.unknown");
}

function patternLabel(
  t: TFunction<"usageAnalytics">,
  pattern: AnalyticsErrorPattern,
): string {
  const code = (pattern.error_code ?? "").trim();
  const category = (pattern.failure_category ?? "").trim();
  if (code && category) return `${code} · ${failureCategoryLabel(t, category)}`;
  if (code) return code;
  if (category) return failureCategoryLabel(t, category);
  return t("observability.errors.unknown");
}

export function ErrorPatternsPanel({
  sessionId,
  adminToken,
  onDrilldown,
  selectedCategory,
}: {
  sessionId: string;
  adminToken?: string;
  onDrilldown: (pattern: AnalyticsErrorPattern) => void;
  selectedCategory?: string;
}) {
  const { t } = useTranslation("usageAnalytics");
  const [patterns, setPatterns] = useState<AnalyticsErrorPattern[]>([]);
  const [source, setSource] = useState("");
  const [top, setTop] = useState(10);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const raw = await listAnalyticsErrors({
        session: sessionId,
        source: source || undefined,
        top,
        adminToken,
      });
      setPatterns(raw.patterns ?? []);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setLoading(false);
    }
  }, [adminToken, sessionId, source, top]);

  useEffect(() => {
    void load();
  }, [load]);

  const sourceOptions = [
    { value: "", label: t("observability.errors.sourceAll") },
    { value: "tools", label: t("observability.errors.sourceLabels.tools") },
    { value: "subagents", label: t("observability.errors.sourceLabels.subagents") },
    { value: "requests", label: t("observability.errors.sourceLabels.requests") },
  ];
  const topOptions = [10, 20, 50].map((value) => ({
    value: String(value),
    label: String(value),
  }));

  return (
    <section
      aria-labelledby="error-patterns-title"
      className="surface-panel min-w-0 rounded-panel-lg p-3 sm:p-4"
    >
      <div className="mb-3 flex flex-col gap-2 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          <h3 id="error-patterns-title" className="text-sm font-semibold">
            {t("observability.errors.title")}
          </h3>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {t("observability.errors.subtitle")}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Select
            ariaLabel={t("observability.errors.sourceFilter")}
            value={source}
            options={sourceOptions}
            onChange={(value) => setSource(value)}
            triggerClassName="h-8 rounded-field"
          />
          <Select
            ariaLabel={t("observability.errors.topLabel")}
            value={String(top)}
            options={topOptions}
            onChange={(value) => setTop(Number.parseInt(value, 10) || 10)}
            triggerClassName="h-8 rounded-field"
          />
        </div>
      </div>

      {error ? (
        <div
          role="alert"
          className="mb-3 rounded-panel border border-analytics-danger-border bg-analytics-danger-soft px-3 py-2.5 text-sm text-analytics-danger"
        >
          {t("loadError")}: {error}
        </div>
      ) : null}

      {loading && patterns.length === 0 ? (
        <div className="flex items-center justify-center gap-2 py-8 text-sm text-muted-foreground">
          <RefreshCwIcon size={15} className="animate-spin" />
          {t("observability.loading")}
        </div>
      ) : patterns.length === 0 ? (
        <div
          data-testid="error-patterns-empty"
          className="rounded-card border border-border bg-surface-softer px-3 py-8 text-center text-sm text-muted-foreground"
        >
          {t("observability.errors.empty")}
        </div>
      ) : (
        <div className="w-full max-w-full overflow-x-auto rounded-card border border-border">
          <table className="w-full min-w-[560px] border-collapse text-left text-sm">
            <thead className="bg-surface-softer text-xs text-muted-foreground">
              <tr className="border-b border-border">
                <th className="px-3 py-2 font-medium">{t("observability.errors.columns.pattern")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.errors.columns.source")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.errors.columns.count")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.errors.columns.action")}</th>
              </tr>
            </thead>
            <tbody>
              {patterns.map((pattern, index) => {
                const label = patternLabel(t, pattern);
                const selected =
                  Boolean(selectedCategory) &&
                  (pattern.failure_category ?? "").trim() === selectedCategory;
                return (
                  <tr
                    key={`${pattern.source}-${pattern.error_code ?? ""}-${pattern.failure_category ?? ""}-${index}`}
                    className="border-b border-border/70 last:border-b-0 hover:bg-surface-soft-hover"
                  >
                    <td className="px-3 py-2.5">
                      <div className="flex items-start gap-2">
                        <AlertTriangleIcon
                          size={15}
                          className="mt-0.5 shrink-0 text-analytics-warning"
                        />
                        <span className="min-w-0 break-all font-mono text-xs">{label}</span>
                      </div>
                    </td>
                    <td className="px-3 py-2.5">
                      <Badge>{analyticsSourceLabel(t, pattern.source)}</Badge>
                    </td>
                    <td className="px-3 py-2.5 tabular-nums">{formatNumber(pattern.count)}</td>
                    <td className="px-3 py-2.5">
                      <button
                        type="button"
                        aria-pressed={selected}
                        aria-label={t("observability.errors.drilldown", { key: label })}
                        onClick={() => onDrilldown(pattern)}
                        className="rounded-field border border-border px-2 py-1 text-xs transition hover:bg-surface-soft hover:text-foreground"
                      >
                        {t("observability.errors.columns.action")}
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
