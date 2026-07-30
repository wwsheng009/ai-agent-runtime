import { ActivityIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

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
  const { t } = useTranslation("runtimeConfig");

  function update(patch: Partial<RuntimeCircuitBreakerConfigSummary>) {
    onChange({
      ...config,
      ...patch,
    });
  }

  return (
    <div className="space-y-3">
      <SettingsPanelCard
        title={
          <span className="text-base">{t("editor.circuitBreaker.title")}</span>
        }
        icon={
          <SettingsPanelIcon>
            <ActivityIcon size={16} />
          </SettingsPanelIcon>
        }
        description={t("editor.circuitBreaker.description")}
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
        title={t("editor.circuitBreaker.failure.title")}
        description={t("editor.circuitBreaker.failure.description")}
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
        title={t("editor.circuitBreaker.recovery.title")}
        description={t("editor.circuitBreaker.recovery.description")}
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
