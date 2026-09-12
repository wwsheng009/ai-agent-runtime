import { type TFunction } from "i18next";

import { Select } from "@/components/ui/select";

import { ConfigFormField } from "../config-form-field";
import {
  type AgentRoutingDifficulty,
  type AgentRoutingScope,
  type RuntimeAgentRoutingConfigSummary,
  type RuntimeAgentRoutingHealthSummary,
  type RuntimeAgentRoutingSettings,
} from "../runtime-agent-routing-domain-utils";
import { SettingsInlineToggleCard } from "../settings-inline-toggle-card";
import { SettingsNoticeCard } from "../settings-notice-card";

import { difficultyOptions } from "./format";

export function RoutingTogglesSection({
  config,
  enabledProviderCount,
  health,
  inherited,
  onTeamInheritanceChange,
  scope,
  settings,
  t,
  updateConfig,
}: {
  config: RuntimeAgentRoutingConfigSummary;
  enabledProviderCount: number;
  health: RuntimeAgentRoutingHealthSummary;
  inherited: boolean;
  onTeamInheritanceChange: (inherit: boolean) => void;
  scope: AgentRoutingScope;
  settings: RuntimeAgentRoutingSettings;
  t: TFunction<"runtimeConfig">;
  updateConfig: (
    update: (next: RuntimeAgentRoutingConfigSummary) => void,
  ) => void;
}) {
  return (
    <>
      {scope === "teams" ? (
        <SettingsInlineToggleCard
          checked={settings.teamUsesSubagentRouting}
          label={t("editor.agentRouting.inheritTeam.label")}
          description={t("editor.agentRouting.inheritTeam.description")}
          onCheckedChange={onTeamInheritanceChange}
        />
      ) : null}

      <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
        <SettingsInlineToggleCard
          checked={config.enabled}
          label={t("editor.agentRouting.enabled.label")}
          description={t("editor.agentRouting.enabled.description")}
          onCheckedChange={(checked) =>
            updateConfig((next) => {
              next.enabled = checked;
            })
          }
          disabled={inherited}
          className={inherited ? "opacity-60" : undefined}
        />
        <ConfigFormField
          label={t("editor.agentRouting.defaultDifficulty.label")}
          description={t("editor.agentRouting.defaultDifficulty.description")}
        >
          <Select
            ariaLabel={t("editor.agentRouting.defaultDifficulty.label")}
            disabled={inherited}
            options={difficultyOptions(t)}
            value={config.defaultDifficulty}
            onChange={(value) =>
              updateConfig((next) => {
                next.defaultDifficulty = value as AgentRoutingDifficulty;
              })
            }
          />
        </ConfigFormField>
        <SettingsInlineToggleCard
          checked={config.inheritParentWhenMissing}
          label={t("editor.agentRouting.inheritParent.label")}
          description={t("editor.agentRouting.inheritParent.description")}
          onCheckedChange={(checked) =>
            updateConfig((next) => {
              next.inheritParentWhenMissing = checked;
            })
          }
          disabled={inherited}
          className={inherited ? "opacity-60" : undefined}
        />
        <SettingsInlineToggleCard
          checked={config.validateModelCapabilities}
          label={t("editor.agentRouting.validateModels.label")}
          description={t("editor.agentRouting.validateModels.description")}
          onCheckedChange={(checked) =>
            updateConfig((next) => {
              next.validateModelCapabilities = checked;
            })
          }
          disabled={inherited}
          className={inherited ? "opacity-60" : undefined}
        />
      </div>

      {config.enabled && enabledProviderCount === 0 ? (
        <SettingsNoticeCard tone="warning-soft">
          {t("editor.agentRouting.noProviders")}
        </SettingsNoticeCard>
      ) : null}

      {health.errorCount > 0 ? (
        <SettingsNoticeCard tone="warning-soft">
          {t("editor.agentRouting.health.errorsPreventRouting", {
            count: health.errorCount,
          })}
        </SettingsNoticeCard>
      ) : null}
    </>
  );
}
