// P2-1A：技能运行统计面板（GET /api/runtime/skills/stats）。
//
// 只渲染后端真实字段：
//   * `mutation_policy` 缺席 → 显示「策略未知」，不按 false 渲染；
//   * `topStatRows` 只按后端 `call_count` 排序，不推算额外指标；
//   * 统计不可用时如实降级，不显示伪造的零值卡片。

import { useTranslation } from "react-i18next";

import type { RuntimeSkillStats } from "@/types/runtime";

import type { SkillsStreamStatus } from "@/hooks/use-skills-market";

import { classifySkillsError, formatDurationMs, formatSuccessRate, policyFlags, topStatRows } from "./shared";

type SkillsStatsPanelProps = {
  stats: RuntimeSkillStats | null;
  status: SkillsStreamStatus;
  error: unknown;
  onRefresh: () => void;
};

export function SkillsStatsPanel({ stats, status, error, onRefresh }: SkillsStatsPanelProps) {
  const { t } = useTranslation("skills");

  return (
    <section
      className="surface-panel min-w-0 rounded-panel border border-border px-3 py-3 sm:px-4"
      aria-label={t("stats.title")}
      data-testid="skills-stats"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-sm font-semibold">{t("stats.title")}</h2>
        <button
          type="button"
          onClick={onRefresh}
          className="text-xs text-muted-foreground underline-offset-2 hover:underline"
        >
          {t("actions.refresh")}
        </button>
      </div>

      {status === "loading" ? (
        <p className="mt-2 text-xs text-muted-foreground" data-testid="skills-stats-loading">
          {t("stats.loading")}
        </p>
      ) : null}

      {status === "error" ? (
        <div
          className="mt-2 rounded-panel border border-analytics-warning-border bg-analytics-warning-soft px-3 py-2 text-xs text-analytics-warning"
          data-testid="skills-stats-error"
        >
          {t(`errors.${classifySkillsError(error)}`)}
        </div>
      ) : null}

      {status === "ready" && stats ? (
        <div className="mt-3 space-y-3">
          <div className="grid gap-2 sm:grid-cols-3">
            <div className="rounded-panel border border-border bg-surface-softer px-3 py-2">
              <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                {t("stats.totalSkills")}
              </div>
              <div className="mt-0.5 text-lg font-semibold tabular-nums" data-testid="skills-stats-total">
                {stats.totalSkills}
              </div>
            </div>
            <div className="rounded-panel border border-border bg-surface-softer px-3 py-2">
              <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                {t("stats.embedding")}
              </div>
              <div className="mt-0.5 text-sm">
                {stats.embeddingEnabled === null
                  ? t("stats.unknown")
                  : stats.embeddingEnabled
                    ? t("stats.embeddingEnabled")
                    : t("stats.embeddingDisabled")}
              </div>
            </div>
            <div className="rounded-panel border border-border bg-surface-softer px-3 py-2">
              <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                {t("stats.skillDirs")}
              </div>
              <div className="mt-0.5 break-all text-xs leading-5" data-testid="skills-stats-dirs">
                {stats.skillDirs.length > 0 ? stats.skillDirs.join(", ") : t("stats.absent")}
              </div>
            </div>
          </div>

          <div>
            <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
              {t("stats.policy")}
            </div>
            {stats.policy ? (
              <div className="mt-1 flex flex-wrap gap-1.5" data-testid="skills-stats-policy">
                {policyFlags(stats.policy).map((flag) => (
                  <span
                    key={flag.key}
                    className={
                      flag.value === null
                        ? "rounded-full border border-border px-2 py-0.5 text-xs text-muted-foreground"
                        : flag.value
                          ? "rounded-full border border-analytics-warning-border bg-analytics-warning-soft px-2 py-0.5 text-xs text-analytics-warning"
                          : "rounded-full border border-border px-2 py-0.5 text-xs text-muted-foreground"
                    }
                  >
                    {t(`stats.policyFlags.${flag.key}`)}
                    {": "}
                    {flag.value === null
                      ? t("stats.unknown")
                      : flag.value
                        ? t("stats.flagOn")
                        : t("stats.flagOff")}
                  </span>
                ))}
              </div>
            ) : (
              <p className="mt-1 text-xs text-muted-foreground" data-testid="skills-stats-policy-unknown">
                {t("stats.policyUnknown")}
              </p>
            )}
          </div>

          {Object.keys(stats.sourceSummary).length > 0 ? (
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                {t("stats.sourceSummary")}
              </div>
              <div className="mt-1 flex flex-wrap gap-1.5">
                {Object.entries(stats.sourceSummary).map(([key, value]) => (
                  <span key={key} className="rounded-full border border-border px-2 py-0.5 text-xs text-muted-foreground">
                    {key || t("stats.unknownSource")}
                    {": "}
                    {value}
                  </span>
                ))}
              </div>
            </div>
          ) : null}

          <div>
            <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
              {t("stats.topSkills")}
            </div>
            {stats.rows.length === 0 ? (
              <p className="mt-1 text-xs text-muted-foreground">{t("stats.noRows")}</p>
            ) : (
              <ul className="mt-1 space-y-1" data-testid="skills-stats-rows">
                {topStatRows(stats.rows).map((row) => (
                  <li
                    key={row.name}
                    className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5 text-xs text-muted-foreground"
                  >
                    <span className="truncate text-foreground">{row.name}</span>
                    <span className="tabular-nums">
                      {t("stats.callCount", { count: row.callCount })}
                      {row.successRate > 0 ? ` · ${t("stats.successRate")} ${formatSuccessRate(row.successRate)}` : ""}
                      {row.avgDurationMs !== null
                        ? ` · ${t("stats.avgDuration")} ${formatDurationMs(row.avgDurationMs)}`
                        : ""}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </div>
      ) : null}
    </section>
  );
}
