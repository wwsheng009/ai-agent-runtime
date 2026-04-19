import { ActivityIcon, RouteIcon } from "lucide-react";

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
  function update(patch: Partial<RuntimeResourceManagerConfigSummary>) {
    onChange({
      ...config,
      ...patch,
    });
  }

  return (
    <div className="space-y-3">
      <SettingsPanelCard
        title={<span className="text-base">Resource Manager 配置</span>}
        icon={
          <SettingsPanelIcon>
            <RouteIcon size={16} />
          </SettingsPanelIcon>
        }
        description="维护 LoadBalanceStageV2 的资源选择策略、健康检查和统计保留配置。"
        descriptionClassName="mt-1"
        headerAside={
          <SettingsBadgeList>
            <Badge>{config.enabled ? "资源管理开" : "资源管理关"}</Badge>
            <Badge>
              {config.crossProviderKeySelection ? "跨 Provider Key 开" : "跨 Provider Key 关"}
            </Badge>
            <Badge>{config.enableStats ? "统计开" : "统计关"}</Badge>
          </SettingsBadgeList>
        }
      />

      <div className="grid gap-3 xl:grid-cols-[11rem_minmax(0,1fr)_minmax(0,1fr)]">
        <SettingsMiniToggleCard
          checked={config.enabled}
          description="控制是否启用新的 ResourceManager 选择路径。"
          label="resource_manager.enabled"
          onCheckedChange={(checked) => update({ enabled: checked })}
        />

        <SettingsSubsectionCard title="默认算法">
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
          description="控制是否可跨 Provider 直接选择 Group 内全部 Key。"
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
        description="控制健康探测间隔、自动恢复和恢复阈值。"
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
            启用健康检查
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
            description="控制失败资源是否在成功率恢复后自动回到可用状态。"
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
          description="控制是否保留资源管理统计数据。"
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
