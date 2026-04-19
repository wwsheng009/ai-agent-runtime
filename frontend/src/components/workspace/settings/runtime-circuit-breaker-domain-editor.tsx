import { ActivityIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";

import { ConfigFormField } from "./config-form-field";
import { editorControlClassName } from "./editor-control-class";
import { SettingsBadgeList } from "./settings-badge-list";
import { type RuntimeCircuitBreakerConfigSummary } from "./runtime-circuit-breaker-domain-utils";
import { SettingsPanelCard } from "./settings-panel-card";
import { SettingsPanelIcon } from "./settings-panel-icon";
import { SettingsSubsectionCard } from "./settings-subsection-card";

type RuntimeCircuitBreakerDomainEditorProps = {
  config: RuntimeCircuitBreakerConfigSummary;
  onChange: (next: RuntimeCircuitBreakerConfigSummary) => void;
};

export function RuntimeCircuitBreakerDomainEditor({
  config,
  onChange,
}: RuntimeCircuitBreakerDomainEditorProps) {
  function update(patch: Partial<RuntimeCircuitBreakerConfigSummary>) {
    onChange({
      ...config,
      ...patch,
    });
  }

  return (
    <div className="space-y-3">
      <SettingsPanelCard
        title={<span className="text-base">Circuit Breaker 配置</span>}
        icon={
          <SettingsPanelIcon>
            <ActivityIcon size={16} />
          </SettingsPanelIcon>
        }
        description="维护失败阈值、失败率、时间窗口和半开试探参数，避免级联故障。"
        descriptionClassName="mt-1"
        headerAside={
          <SettingsBadgeList>
            <Badge>{`failure ${config.failureThreshold || "--"}`}</Badge>
            <Badge>{`rate ${config.failureRate || "--"}`}</Badge>
            <Badge>{`open ${config.openTimeout || "--"}`}</Badge>
          </SettingsBadgeList>
        }
      />

      <SettingsSubsectionCard
        title="失败判定"
        description="决定何时触发熔断。"
      >
        <div className="grid gap-3 xl:grid-cols-3">
          <ConfigFormField label="failure_threshold">
            <input
              className={editorControlClassName}
              value={config.failureThreshold}
              onChange={(event) =>
                update({ failureThreshold: event.target.value })
              }
            />
          </ConfigFormField>
          <ConfigFormField label="failure_rate">
            <input
              className={editorControlClassName}
              value={config.failureRate}
              onChange={(event) => update({ failureRate: event.target.value })}
            />
          </ConfigFormField>
          <ConfigFormField label="sample_threshold">
            <input
              className={editorControlClassName}
              value={config.sampleThreshold}
              onChange={(event) =>
                update({ sampleThreshold: event.target.value })
              }
            />
          </ConfigFormField>
        </div>
      </SettingsSubsectionCard>

      <SettingsSubsectionCard
        title="时间与恢复"
        description="控制滑动窗口、熔断持续时间和半开试探次数。"
      >
        <div className="grid gap-3 xl:grid-cols-3">
          <ConfigFormField label="window_duration">
            <input
              className={editorControlClassName}
              value={config.windowDuration}
              onChange={(event) =>
                update({ windowDuration: event.target.value })
              }
            />
          </ConfigFormField>
          <ConfigFormField label="open_timeout">
            <input
              className={editorControlClassName}
              value={config.openTimeout}
              onChange={(event) => update({ openTimeout: event.target.value })}
            />
          </ConfigFormField>
          <ConfigFormField label="half_open_max_calls">
            <input
              className={editorControlClassName}
              value={config.halfOpenMaxCalls}
              onChange={(event) =>
                update({ halfOpenMaxCalls: event.target.value })
              }
            />
          </ConfigFormField>
        </div>
      </SettingsSubsectionCard>
    </div>
  );
}
