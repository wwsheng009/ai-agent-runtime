import { ActivityIcon, RouteIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";

import { ConfigFormField } from "./config-form-field";
import {
  editorControlClassName,
  editorSectionToggleClassName,
} from "./editor-control-class";
import { SettingsBadgeList } from "./settings-badge-list";
import { SettingsMiniToggleCard } from "./settings-mini-toggle-card";
import { SettingsPanelCard } from "./settings-panel-card";
import { SettingsPanelIcon } from "./settings-panel-icon";
import { SettingsSubsectionCard } from "./settings-subsection-card";
import { type RuntimeResourceManagerConfigSummary } from "./runtime-resource-manager-domain-utils";

type RuntimeResourceManagerDomainEditorProps = {
  config: RuntimeResourceManagerConfigSummary;
  onChange: (next: RuntimeResourceManagerConfigSummary) => void;
};

export function RuntimeResourceManagerDomainEditor({
  config,
  onChange,
}: RuntimeResourceManagerDomainEditorProps) {
  const { t } = useTranslation("runtimeConfig");

  function update(patch: Partial<RuntimeResourceManagerConfigSummary>) {
    onChange({
      ...config,
      ...patch,
    });
  }

  return (
    <div className="space-y-3">
      <SettingsPanelCard
        title={<span className="text-base">{t("editor.resourceManager.title")}</span>}
        icon={
          <SettingsPanelIcon>
            <RouteIcon size={16} />
          </SettingsPanelIcon>
        }
        description={t("editor.resourceManager.description")}
        descriptionClassName="mt-1"
        headerAside={
          <SettingsBadgeList>
            <Badge>
              {config.enabled
                ? t("editor.resourceManager.badges.enabledOn")
                : t("editor.resourceManager.badges.enabledOff")}
            </Badge>
            <Badge>
              {config.crossProviderKeySelection
                ? t("editor.resourceManager.badges.crossKeyOn")
                : t("editor.resourceManager.badges.crossKeyOff")}
            </Badge>
            <Badge>
              {config.enableStats
                ? t("editor.resourceManager.badges.statsOn")
                : t("editor.resourceManager.badges.statsOff")}
            </Badge>
          </SettingsBadgeList>
        }
      />

      <div className="grid gap-3 xl:grid-cols-[11rem_minmax(0,1fr)_minmax(0,1fr)]">
        <SettingsMiniToggleCard
          checked={config.enabled}
          description={t("editor.resourceManager.enabledDescription")}
          label="resource_manager.enabled"
          onCheckedChange={(checked) => update({ enabled: checked })}
        />

        <SettingsSubsectionCard title={t("editor.resourceManager.defaultAlgorithm")}>
          <div className="grid gap-3 xl:grid-cols-3">
            <ConfigFormField label="default_group_algorithm">
              <input
                className={editorControlClassName}
                value={config.defaultGroupAlgorithm}
                onChange={(event) =>
                  update({ defaultGroupAlgorithm: event.target.value })
                }
                placeholder="tiered"
              />
            </ConfigFormField>
            <ConfigFormField label="default_provider_algorithm">
              <input
                className={editorControlClassName}
                value={config.defaultProviderAlgorithm}
                onChange={(event) =>
                  update({ defaultProviderAlgorithm: event.target.value })
                }
                placeholder="round_robin"
              />
            </ConfigFormField>
            <ConfigFormField label="default_key_algorithm">
              <input
                className={editorControlClassName}
                value={config.defaultKeyAlgorithm}
                onChange={(event) =>
                  update({ defaultKeyAlgorithm: event.target.value })
                }
                placeholder="health_based"
              />
            </ConfigFormField>
          </div>
        </SettingsSubsectionCard>

        <SettingsMiniToggleCard
          checked={config.crossProviderKeySelection}
          description={t("editor.resourceManager.crossProviderDescription")}
          label="cross_provider_key_selection"
          onCheckedChange={(checked) =>
            update({ crossProviderKeySelection: checked })
          }
        />
      </div>

      <SettingsSubsectionCard
        title="health_check"
        icon={
          <SettingsPanelIcon>
            <ActivityIcon size={16} />
          </SettingsPanelIcon>
        }
        description={t("editor.resourceManager.healthCheck.description")}
        headerAside={
          <label className={editorSectionToggleClassName}>
            <input
              type="checkbox"
              className="h-4 w-4 accent-[var(--accent-primary)]"
              checked={config.healthCheckEnabled}
              onChange={(event) =>
                update({ healthCheckEnabled: event.target.checked })
              }
            />
            {t("editor.resourceManager.healthCheck.enable")}
          </label>
        }
      >
        <div className="grid gap-3 xl:grid-cols-3">
          <ConfigFormField label="health_check.interval">
            <input
              className={editorControlClassName}
              value={config.healthCheckInterval}
              onChange={(event) =>
                update({ healthCheckInterval: event.target.value })
              }
              placeholder="60s"
            />
          </ConfigFormField>
          <ConfigFormField label="health_check.recovery_threshold">
            <input
              className={editorControlClassName}
              value={config.healthCheckRecoveryThreshold}
              onChange={(event) =>
                update({ healthCheckRecoveryThreshold: event.target.value })
              }
              placeholder="0.90"
            />
          </ConfigFormField>
          <SettingsMiniToggleCard
            checked={config.healthCheckAutoRecovery}
            description={t(
              "editor.resourceManager.healthCheck.autoRecoveryDescription",
            )}
            label="health_check.auto_recovery"
            onCheckedChange={(checked) =>
              update({ healthCheckAutoRecovery: checked })
            }
          />
        </div>
      </SettingsSubsectionCard>

      <div className="grid gap-3 xl:grid-cols-[11rem_minmax(0,1fr)]">
        <SettingsMiniToggleCard
          checked={config.enableStats}
          description={t("editor.resourceManager.statsDescription")}
          label="enable_stats"
          onCheckedChange={(checked) => update({ enableStats: checked })}
        />
        <ConfigFormField label="stats_retention">
          <input
            className={editorControlClassName}
            value={config.statsRetention}
            onChange={(event) => update({ statsRetention: event.target.value })}
            placeholder="24h"
          />
        </ConfigFormField>
      </div>
    </div>
  );
}
