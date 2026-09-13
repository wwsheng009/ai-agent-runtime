import { type TFunction } from "i18next";
import { GaugeIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";

import { ConfigDomainSummaryBadge } from "../config-domain-table";
import { ConfigFormField } from "../config-form-field";
import { editorControlClassName, editorToggleRowClassName } from "../editor-control-class";
import {
  type RuntimeRateLimitApiKeyLimitSummary,
  type RuntimeRateLimitConfigSummary,
  type RuntimeRateLimitPathLimitSummary,
} from "../runtime-rate-limit-domain-utils";
import { SettingsBadgeList } from "../settings-badge-list";
import { SettingsPanelIcon } from "../settings-panel-icon";

export function RuntimeRateLimitOverviewSection({
  apiKeyLimits,
  onChangeConfig,
  pathLimits,
  rateLimitConfig,
  t,
  totalPathBurst,
}: {
  apiKeyLimits: RuntimeRateLimitApiKeyLimitSummary[];
  onChangeConfig: (next: RuntimeRateLimitConfigSummary) => void;
  pathLimits: RuntimeRateLimitPathLimitSummary[];
  rateLimitConfig: RuntimeRateLimitConfigSummary;
  t: TFunction<"runtimeConfig">;
  totalPathBurst: number;
}) {
  return (
    <div className="rounded-panel border border-border bg-surface-softer p-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <SettingsPanelIcon>
            <GaugeIcon size={15} />
          </SettingsPanelIcon>
          <div>
            <div className="text-base font-semibold text-foreground">
              {t("editor.rateLimit.title")}
            </div>
            <div className="mt-1 text-sm text-muted-foreground">
              {t("editor.rateLimit.description")}
            </div>
          </div>
        </div>
        <SettingsBadgeList>
          <ConfigDomainSummaryBadge>
            {rateLimitConfig.enabled
              ? t("editor.rateLimit.status.enabled")
              : t("editor.rateLimit.status.disabled")}
          </ConfigDomainSummaryBadge>
          <ConfigDomainSummaryBadge>
            {t("editor.rateLimit.summary.apiKeyRules", {
              count: apiKeyLimits.length,
            })}
          </ConfigDomainSummaryBadge>
          <ConfigDomainSummaryBadge>
            {t("editor.rateLimit.summary.pathRules", {
              count: pathLimits.length,
            })}
          </ConfigDomainSummaryBadge>
        </SettingsBadgeList>
      </div>

      <div className="mt-3 grid gap-3 xl:grid-cols-[12rem_minmax(0,1fr)_minmax(0,1fr)]">
        <div className="rounded-card border border-border bg-surface-softer p-3">
          <div className="text-[13px] font-semibold text-foreground">
            {t("editor.rateLimit.fields.enabled")}
          </div>
          <div className="mt-1 text-xs leading-5 text-muted-foreground">
            {t("editor.rateLimit.enabledHelp")}
          </div>
          <label className={`mt-3 ${editorToggleRowClassName}`}>
            <span>
              {rateLimitConfig.enabled
                ? t("editor.rateLimit.toggle.enabled")
                : t("editor.rateLimit.toggle.disabled")}
            </span>
            <input
              type="checkbox"
              className="h-4 w-4 accent-accent-primary"
              checked={rateLimitConfig.enabled}
              onChange={(event) =>
                onChangeConfig({
                  ...rateLimitConfig,
                  enabled: event.target.checked,
                })
              }
            />
          </label>
        </div>

        <div className="space-y-3 rounded-card border border-border bg-surface-softer p-3">
          <div className="text-[13px] font-semibold text-foreground">
            {t("editor.rateLimit.basicConfig")}
          </div>
          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField label={t("editor.rateLimit.fields.storage")}>
              <input
                className={editorControlClassName}
                value={rateLimitConfig.storage}
                onChange={(event) =>
                  onChangeConfig({
                    ...rateLimitConfig,
                    storage: event.target.value,
                  })
                }
                placeholder={t("editor.rateLimit.fields.storagePlaceholder")}
              />
            </ConfigFormField>
            <ConfigFormField label={t("editor.rateLimit.fields.algorithm")}>
              <input
                className={editorControlClassName}
                value={rateLimitConfig.algorithm}
                onChange={(event) =>
                  onChangeConfig({
                    ...rateLimitConfig,
                    algorithm: event.target.value,
                  })
                }
                placeholder={t("editor.rateLimit.fields.algorithmPlaceholder")}
              />
            </ConfigFormField>
          </div>
        </div>

        <div className="space-y-3 rounded-card border border-border bg-surface-softer p-3">
          <div className="text-[13px] font-semibold text-foreground">
            {t("editor.rateLimit.summaryTitle")}
          </div>
          <SettingsBadgeList>
            <Badge>{`storage ${rateLimitConfig.storage || "--"}`}</Badge>
            <Badge>{`algorithm ${rateLimitConfig.algorithm || "--"}`}</Badge>
            <Badge>
              {t("editor.rateLimit.summary.burstTotal", {
                total: String(totalPathBurst),
              })}
            </Badge>
          </SettingsBadgeList>
        </div>
      </div>

      <div className="mt-3 grid gap-3 xl:grid-cols-2">
        <div className="rounded-card border border-border bg-surface-softer p-3">
          <div className="mb-3 text-[13px] font-semibold text-foreground">
            {t("editor.rateLimit.fields.defaultLimits")}
          </div>
          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField label={t("editor.rateLimit.fields.qps")}>
              <input
                className={editorControlClassName}
                value={rateLimitConfig.defaultQps}
                onChange={(event) =>
                  onChangeConfig({
                    ...rateLimitConfig,
                    defaultQps: event.target.value,
                  })
                }
              />
            </ConfigFormField>
            <ConfigFormField label={t("editor.rateLimit.fields.tpm")}>
              <input
                className={editorControlClassName}
                value={rateLimitConfig.defaultTpm}
                onChange={(event) =>
                  onChangeConfig({
                    ...rateLimitConfig,
                    defaultTpm: event.target.value,
                  })
                }
              />
            </ConfigFormField>
            <ConfigFormField label={t("editor.rateLimit.fields.dailyTokens")}>
              <input
                className={editorControlClassName}
                value={rateLimitConfig.defaultDailyTokens}
                onChange={(event) =>
                  onChangeConfig({
                    ...rateLimitConfig,
                    defaultDailyTokens: event.target.value,
                  })
                }
              />
            </ConfigFormField>
            <ConfigFormField label={t("editor.rateLimit.fields.monthlyTokens")}>
              <input
                className={editorControlClassName}
                value={rateLimitConfig.defaultMonthlyTokens}
                onChange={(event) =>
                  onChangeConfig({
                    ...rateLimitConfig,
                    defaultMonthlyTokens: event.target.value,
                  })
                }
              />
            </ConfigFormField>
          </div>
        </div>

        <div className="rounded-card border border-border bg-surface-softer p-3">
          <div className="mb-3 text-[13px] font-semibold text-foreground">
            {t("editor.rateLimit.fields.globalLimits")}
          </div>
          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField label={t("editor.rateLimit.fields.globalQps")}>
              <input
                className={editorControlClassName}
                value={rateLimitConfig.globalQps}
                onChange={(event) =>
                  onChangeConfig({
                    ...rateLimitConfig,
                    globalQps: event.target.value,
                  })
                }
              />
            </ConfigFormField>
            <ConfigFormField label={t("editor.rateLimit.fields.globalTpm")}>
              <input
                className={editorControlClassName}
                value={rateLimitConfig.globalTpm}
                onChange={(event) =>
                  onChangeConfig({
                    ...rateLimitConfig,
                    globalTpm: event.target.value,
                  })
                }
              />
            </ConfigFormField>
            <ConfigFormField label={t("editor.rateLimit.fields.globalDailyTokens")}>
              <input
                className={editorControlClassName}
                value={rateLimitConfig.globalDailyTokens}
                onChange={(event) =>
                  onChangeConfig({
                    ...rateLimitConfig,
                    globalDailyTokens: event.target.value,
                  })
                }
              />
            </ConfigFormField>
            <ConfigFormField label={t("editor.rateLimit.fields.globalMonthlyTokens")}>
              <input
                className={editorControlClassName}
                value={rateLimitConfig.globalMonthlyTokens}
                onChange={(event) =>
                  onChangeConfig({
                    ...rateLimitConfig,
                    globalMonthlyTokens: event.target.value,
                  })
                }
              />
            </ConfigFormField>
          </div>
        </div>
      </div>
    </div>
  );
}

