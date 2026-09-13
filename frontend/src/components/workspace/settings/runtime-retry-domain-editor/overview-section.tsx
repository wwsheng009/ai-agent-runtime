// 由 components/workspace/settings/runtime-retry-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { RefreshCcwIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";

import {
  type RuntimeRetryConfigSummary,
  type RuntimeRetryRuleSummary,
} from "../runtime-retry-domain-utils";
import { ConfigFormField } from "../config-form-field";
import { editorControlClassName, editorToggleRowClassName } from "../editor-control-class";
import { SettingsBadgeList } from "../settings-badge-list";
import { SettingsNoticeCard } from "../settings-notice-card";
import { SettingsPanelCard } from "../settings-panel-card";
import { SettingsPanelIcon } from "../settings-panel-icon";
import { SettingsSubsectionCard } from "../settings-subsection-card";

export function RuntimeRetryOverviewSection({
  config,
  enabledRuleCount,
  onChangeConfig,
  rules,
  t,
}: {
  config: RuntimeRetryConfigSummary;
  enabledRuleCount: number;
  onChangeConfig: (next: RuntimeRetryConfigSummary) => void;
  rules: RuntimeRetryRuleSummary[];
  t: TFunction<"runtimeConfig">;
}) {
  return (
      <SettingsPanelCard
        title={<span className="text-base">{t("editor.retry.title")}</span>}
        icon={
          <SettingsPanelIcon>
            <RefreshCcwIcon size={16} />
          </SettingsPanelIcon>
        }
        description={t("editor.retry.description")}
        descriptionClassName="mt-1"
        headerAside={
          <SettingsBadgeList>
            <Badge>
              {config.enabled
                ? t("editor.retry.status.retryOn")
                : t("editor.retry.status.retryOff")}
            </Badge>
            <Badge>
              {t("editor.retry.summary.enabledRules", {
                enabled: String(enabledRuleCount),
                total: String(rules.length),
              })}
            </Badge>
            <Badge>
              {config.enhancedStrategyEnabled
                ? t("editor.retry.status.enhancedOn")
                : t("editor.retry.status.enhancedOff")}
            </Badge>
          </SettingsBadgeList>
        }
      >
        <div className="grid gap-3 xl:grid-cols-[11rem_minmax(0,1fr)_minmax(0,1fr)]">
          <div className="rounded-card border border-border bg-surface-softer p-3">
            <div className="text-sm font-semibold text-foreground">
              {t("editor.retry.fields.enabled")}
            </div>
            <div className="mt-1 text-xs leading-5 text-muted-foreground">
              {t("editor.retry.enabledHelp")}
            </div>
            <label className={`mt-3 ${editorToggleRowClassName}`}>
              <span>
                {config.enabled
                  ? t("editor.retry.toggle.enabled")
                  : t("editor.retry.toggle.disabled")}
              </span>
              <input
                type="checkbox"
                className="h-4 w-4 accent-accent-primary"
                checked={config.enabled}
                onChange={(event) =>
                  onChangeConfig({ ...config, enabled: event.target.checked })
                }
              />
            </label>
          </div>

          <SettingsSubsectionCard title={t("editor.retry.defaultStrategy")}>
            <div className="grid gap-3 xl:grid-cols-3">
              <ConfigFormField label={t("editor.retry.fields.defaultMaxRetries")}>
                <input
                  className={editorControlClassName}
                  value={config.defaultMaxRetries}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      defaultMaxRetries: event.target.value,
                    })
                  }
                  placeholder="3"
                />
              </ConfigFormField>
              <ConfigFormField label={t("editor.retry.fields.defaultRetryDelayMs")}>
                <input
                  className={editorControlClassName}
                  value={config.defaultRetryDelayMs}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      defaultRetryDelayMs: event.target.value,
                    })
                  }
                  placeholder="1000"
                />
              </ConfigFormField>
              <ConfigFormField
                label={t("editor.retry.fields.defaultBackoffMultiplier")}
              >
                <input
                  className={editorControlClassName}
                  value={config.defaultBackoffMultiplier}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      defaultBackoffMultiplier: event.target.value,
                    })
                  }
                  placeholder="2.0"
                />
              </ConfigFormField>
            </div>
          </SettingsSubsectionCard>

          <SettingsSubsectionCard title={t("editor.retry.summaryTitle")}>
            <SettingsBadgeList>
              <Badge>{`default ${config.defaultMaxRetries || "--"}`}</Badge>
              <Badge>{`delay ${config.defaultRetryDelayMs || "--"}ms`}</Badge>
              <Badge>{`backoff ${config.defaultBackoffMultiplier || "--"}`}</Badge>
            </SettingsBadgeList>
          </SettingsSubsectionCard>
        </div>

        <div className="mt-3 grid gap-3 xl:grid-cols-2">
          <SettingsSubsectionCard
            title={t("editor.retry.invalidEncrypted.title")}
            description={t("editor.retry.invalidEncrypted.description")}
            headerAside={
              <input
                type="checkbox"
                className="h-4 w-4 accent-accent-primary"
                checked={config.invalidEncryptedContentStripClientStateOnce}
                onChange={(event) =>
                  onChangeConfig({
                    ...config,
                    invalidEncryptedContentStripClientStateOnce:
                      event.target.checked,
                  })
                }
              />
            }
          >
            <SettingsNoticeCard>
              {config.invalidEncryptedContentStripClientStateOnce
                ? t("editor.retry.invalidEncrypted.enabled")
                : t("editor.retry.invalidEncrypted.disabled")}
            </SettingsNoticeCard>
          </SettingsSubsectionCard>

          <SettingsSubsectionCard
            title={t("editor.retry.enhancedStrategy.title")}
            description={t("editor.retry.enhancedStrategy.description")}
            headerAside={
              <input
                type="checkbox"
                className="h-4 w-4 accent-accent-primary"
                checked={config.enhancedStrategyEnabled}
                onChange={(event) =>
                  onChangeConfig({
                    ...config,
                    enhancedStrategyEnabled: event.target.checked,
                  })
                }
              />
            }
          >
            <div className="grid gap-3 xl:grid-cols-2">
              <ConfigFormField label={t("editor.retry.fields.secondaryThreshold")}>
                <input
                  className={editorControlClassName}
                  value={config.enhancedStrategySecondaryThreshold}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      enhancedStrategySecondaryThreshold: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label={t("editor.retry.fields.fallbackThreshold")}>
                <input
                  className={editorControlClassName}
                  value={config.enhancedStrategyFallbackThreshold}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      enhancedStrategyFallbackThreshold: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label={t("editor.retry.fields.primaryMinScore")}>
                <input
                  className={editorControlClassName}
                  value={config.enhancedStrategyPrimaryMinScore}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      enhancedStrategyPrimaryMinScore: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField
                label={t("editor.retry.fields.secondaryExcludedScore")}
              >
                <input
                  className={editorControlClassName}
                  value={config.enhancedStrategySecondaryExcludedScore}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      enhancedStrategySecondaryExcludedScore: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
            </div>
          </SettingsSubsectionCard>
        </div>
      </SettingsPanelCard>
  );
}
