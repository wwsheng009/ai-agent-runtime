import { BotIcon, GaugeIcon, UsersIcon } from "lucide-react";
import { type TFunction } from "i18next";

import {
  type AgentRoutingScope,
  type RuntimeAgentRoutingConfigSummary,
  type RuntimeAgentRoutingHealthSummary,
} from "../runtime-agent-routing-domain-utils";
import { HealthBadge, ScopeButton } from "./primitives";

export function RoutingHeaderCard({
  config,
  health,
  inherited,
  scope,
  setScope,
  t,
}: {
  config: RuntimeAgentRoutingConfigSummary;
  health: RuntimeAgentRoutingHealthSummary;
  inherited: boolean;
  scope: AgentRoutingScope;
  setScope: (scope: AgentRoutingScope) => void;
  t: TFunction<"runtimeConfig">;
}) {
  return (
    <>
      <div className="rounded-[0.8rem] border border-border bg-surface-softer p-3">
        <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
          <div className="min-w-0">
            <div className="flex items-center gap-2 text-base font-semibold text-foreground">
              <GaugeIcon size={17} className="text-accent-primary" />
              {t("editor.agentRouting.title")}
            </div>
            <div className="mt-1 text-sm leading-6 text-muted-foreground">
              {t("editor.agentRouting.description")}
            </div>
            <div className="mt-2 flex flex-wrap gap-1.5">
              {inherited ? (
                <HealthBadge tone="neutral">
                  {t("editor.agentRouting.health.teamInherited")}
                </HealthBadge>
              ) : null}
              <HealthBadge tone="ready">
                {t("editor.agentRouting.health.configuredCount", {
                  count: health.configuredCount,
                })}
              </HealthBadge>
              <HealthBadge tone="neutral">
                {t("editor.agentRouting.health.inheritedCount", {
                  count: health.inheritedCount,
                })}
              </HealthBadge>
              {health.errorCount > 0 ? (
                <HealthBadge tone="error">
                  {t("editor.agentRouting.health.errorCount", {
                    count: health.errorCount,
                  })}
                </HealthBadge>
              ) : null}
              {health.warningCount > 0 ? (
                <HealthBadge tone="warning">
                  {t("editor.agentRouting.health.warningCount", {
                    count: health.warningCount,
                  })}
                </HealthBadge>
              ) : null}
              {config.enabled && health.errorCount === 0 && health.warningCount === 0 ? (
                <HealthBadge tone="ready">
                  {t("editor.agentRouting.health.ready")}
                </HealthBadge>
              ) : null}
            </div>
          </div>
          <div className="grid min-w-[16rem] grid-cols-2 rounded-[0.7rem] border border-border bg-surface-solid p-1">
            <ScopeButton
              active={scope === "subagents"}
              icon={BotIcon}
              label={t("editor.agentRouting.scopes.subagents")}
              onClick={() => setScope("subagents")}
            />
            <ScopeButton
              active={scope === "teams"}
              icon={UsersIcon}
              label={t("editor.agentRouting.scopes.teams")}
              onClick={() => setScope("teams")}
            />
          </div>
        </div>
      </div>
    </>
  );
}
