// 由 components/workspace/settings/runtime-provider-queue-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { GaugeIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";

import { ConfigFormField } from "../config-form-field";
import { editorControlClassName } from "../editor-control-class";
import { SettingsBadgeList } from "../settings-badge-list";
import { SettingsMiniToggleCard } from "../settings-mini-toggle-card";
import { SettingsPanelCard } from "../settings-panel-card";
import { SettingsPanelIcon } from "../settings-panel-icon";
import { SettingsSubsectionCard } from "../settings-subsection-card";
import {
  type RuntimeProviderQueueConfigSummary,
  type RuntimeProviderQueueProviderSummary,
} from "../runtime-provider-queue-domain-utils";

export function RuntimeProviderQueueDefaultsSection({
  config,
  onChangeConfig,
  providers,
  t,
  totalMaxConcurrency,
}: {
  config: RuntimeProviderQueueConfigSummary;
  onChangeConfig: (next: RuntimeProviderQueueConfigSummary) => void;
  providers: RuntimeProviderQueueProviderSummary[];
  t: TFunction<"runtimeConfig">;
  totalMaxConcurrency: number;
}) {
  return (
      <SettingsPanelCard
        title={<span className="text-base">{t("editor.providerQueue.title")}</span>}
        icon={
          <SettingsPanelIcon>
            <GaugeIcon size={16} />
          </SettingsPanelIcon>
        }
        description={t("editor.providerQueue.description")}
        descriptionClassName="mt-1"
        headerAside={
          <SettingsBadgeList>
            <Badge>
              {config.enabled
                ? t("editor.providerQueue.badges.enabledOn")
                : t("editor.providerQueue.badges.enabledOff")}
            </Badge>
            <Badge>
              {t("editor.providerQueue.badges.providerOverrides", {
                count: providers.length,
              })}
            </Badge>
            <Badge>
              {t("editor.providerQueue.badges.maxConcurrencyTotal", {
                total: String(totalMaxConcurrency),
              })}
            </Badge>
          </SettingsBadgeList>
        }
      >
        <div className="grid gap-3 xl:grid-cols-[11rem_minmax(0,1fr)]">
          <SettingsMiniToggleCard
            checked={config.enabled}
            description={t("editor.providerQueue.enabledDescription")}
            label={t("editor.providerQueue.fields.enabled")}
            onCheckedChange={(checked) =>
              onChangeConfig({ ...config, enabled: checked })
            }
          />

          <SettingsSubsectionCard title={t("editor.providerQueue.fields.defaultSlot")}>
            <div className="grid gap-3 xl:grid-cols-4">
              <ConfigFormField label={t("editor.providerQueue.fields.maxConcurrency")}>
                <input
                  className={editorControlClassName}
                  value={config.defaultMaxConcurrency}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      defaultMaxConcurrency: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label={t("editor.providerQueue.fields.queueSize")}>
                <input
                  className={editorControlClassName}
                  value={config.defaultQueueSize}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      defaultQueueSize: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label={t("editor.providerQueue.fields.queueTimeout")}>
                <input
                  className={editorControlClassName}
                  value={config.defaultQueueTimeout}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      defaultQueueTimeout: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label={t("editor.providerQueue.fields.overflowStrategy")}>
                <input
                  className={editorControlClassName}
                  value={config.defaultOverflowStrategy}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      defaultOverflowStrategy: event.target.value,
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
