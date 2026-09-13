// 由 components/workspace/settings/runtime-provider-queue-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { GaugeIcon, TimerIcon } from "lucide-react";

import { ConfigFormField } from "../config-form-field";
import {
  editorControlClassName,
  editorSectionToggleClassName,
} from "../editor-control-class";
import { SettingsPanelCard } from "../settings-panel-card";
import { SettingsPanelIcon } from "../settings-panel-icon";
import { type RuntimeProviderQueueConfigSummary } from "../runtime-provider-queue-domain-utils";

export function RuntimeProviderQueueOverflowHeartbeatSection({
  config,
  onChangeConfig,
  t,
}: {
  config: RuntimeProviderQueueConfigSummary;
  onChangeConfig: (next: RuntimeProviderQueueConfigSummary) => void;
  t: TFunction<"runtimeConfig">;
}) {
  return (
      <div className="grid gap-3 xl:grid-cols-2">
        <SettingsPanelCard
          title="overflow"
          icon={
            <SettingsPanelIcon>
              <GaugeIcon size={16} />
            </SettingsPanelIcon>
          }
          description={t("editor.providerQueue.overflow.description")}
          descriptionClassName="mt-1 text-xs leading-5"
          headerAside={
            <label className={editorSectionToggleClassName}>
              <input
                type="checkbox"
                className="h-4 w-4 accent-accent-primary"
                checked={config.overflowEnabled}
                onChange={(event) =>
                  onChangeConfig({ ...config, overflowEnabled: event.target.checked })
                }
              />
              {t("editor.providerQueue.overflow.enable")}
            </label>
          }
        >
          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField label="overflow.max_attempts">
              <input
                className={editorControlClassName}
                value={config.overflowMaxAttempts}
                onChange={(event) =>
                  onChangeConfig({
                    ...config,
                    overflowMaxAttempts: event.target.value,
                  })
                }
              />
            </ConfigFormField>
            <ConfigFormField label="overflow.strategy">
              <input
                className={editorControlClassName}
                value={config.overflowStrategy}
                onChange={(event) =>
                  onChangeConfig({
                    ...config,
                    overflowStrategy: event.target.value,
                  })
                }
              />
            </ConfigFormField>
          </div>
        </SettingsPanelCard>

        <SettingsPanelCard
          title="wait_heartbeat"
          icon={
            <SettingsPanelIcon>
              <TimerIcon size={16} />
            </SettingsPanelIcon>
          }
          description={t("editor.providerQueue.waitHeartbeat.description")}
          descriptionClassName="mt-1 text-xs leading-5"
          headerAside={
            <label className={editorSectionToggleClassName}>
              <input
                type="checkbox"
                className="h-4 w-4 accent-accent-primary"
                checked={config.waitHeartbeatEnabled}
                onChange={(event) =>
                  onChangeConfig({
                    ...config,
                    waitHeartbeatEnabled: event.target.checked,
                  })
                }
              />
              {t("editor.providerQueue.waitHeartbeat.enable")}
            </label>
          }
        >
          <div className="grid gap-3 xl:grid-cols-3">
            <ConfigFormField label="wait_heartbeat.interval">
              <input
                className={editorControlClassName}
                value={config.waitHeartbeatInterval}
                onChange={(event) =>
                  onChangeConfig({
                    ...config,
                    waitHeartbeatInterval: event.target.value,
                  })
                }
              />
            </ConfigFormField>
            <ConfigFormField label="wait_heartbeat.comment">
              <input
                className={editorControlClassName}
                value={config.waitHeartbeatComment}
                onChange={(event) =>
                  onChangeConfig({
                    ...config,
                    waitHeartbeatComment: event.target.value,
                  })
                }
              />
            </ConfigFormField>
            <ConfigFormField label="wait_heartbeat.max_wait_time">
              <input
                className={editorControlClassName}
                value={config.waitHeartbeatMaxWaitTime}
                onChange={(event) =>
                  onChangeConfig({
                    ...config,
                    waitHeartbeatMaxWaitTime: event.target.value,
                  })
                }
              />
            </ConfigFormField>
          </div>
        </SettingsPanelCard>
      </div>
  );
}
