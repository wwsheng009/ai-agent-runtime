import { type TFunction } from "i18next";

import { Badge } from "@/components/ui/badge";
import { Select } from "@/components/ui/select";

import { editorControlClassName } from "../editor-control-class";
import {
  agentRoutingDifficulties,
  providerModelOptions,
  providerReasoningEffortOptions,
  type AgentRoutingDifficulty,
  type RuntimeAgentRoutingConfigSummary,
  type RuntimeAgentRoutingHealthSummary,
} from "../runtime-agent-routing-domain-utils";
import { type RuntimeProviderSummary } from "../runtime-provider-config-utils";

import { difficultyLabel } from "./format";
import { RouteHealthBadge, RouteHealthIssueText } from "./health-badges";
import { LabeledCell } from "./primitives";

export function RoutingDifficultyTable({
  config,
  health,
  inherited,
  providerOptions,
  providers,
  t,
  updateProfile,
}: {
  config: RuntimeAgentRoutingConfigSummary;
  health: RuntimeAgentRoutingHealthSummary;
  inherited: boolean;
  providerOptions: { value: string; label: string }[];
  providers: RuntimeProviderSummary[];
  t: TFunction<"runtimeConfig">;
  updateProfile: (
    difficulty: AgentRoutingDifficulty,
    field: "provider" | "model" | "reasoningEffort",
    value: string,
  ) => void;
}) {
  return (
    <>
      <div className="overflow-hidden rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)]">
        <div className="hidden grid-cols-[10rem_minmax(10rem,1fr)_minmax(12rem,1.2fr)_10rem] gap-3 border-b border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2 text-xs font-semibold text-[var(--muted-foreground)] lg:grid">
          <div>{t("editor.agentRouting.columns.difficulty")}</div>
          <div>{t("editor.agentRouting.columns.provider")}</div>
          <div>{t("editor.agentRouting.columns.model")}</div>
          <div>{t("editor.agentRouting.columns.reasoning")}</div>
        </div>
        <div className="divide-y divide-[var(--border)]">
          {agentRoutingDifficulties.map((difficulty) => {
            const profile = config.levels[difficulty];
            const routeHealth = health.routes[difficulty];
            const modelOptions = providerModelOptions(
              providers,
              profile.provider,
              profile.model,
            );
            const reasoningEffortOptions = providerReasoningEffortOptions(
              providers,
              profile.provider,
              profile.model,
              profile.reasoningEffort,
            );
            return (
              <div
                key={difficulty}
                className="grid gap-3 px-3 py-3 lg:grid-cols-[10rem_minmax(10rem,1fr)_minmax(12rem,1.2fr)_10rem] lg:items-center"
              >
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="font-semibold text-[var(--foreground)]">
                    {difficultyLabel(t, difficulty)}
                  </span>
                  {difficulty === config.defaultDifficulty ? (
                    <Badge className="normal-case tracking-normal">
                      {t("editor.agentRouting.defaultBadge")}
                    </Badge>
                  ) : null}
                  <RouteHealthBadge health={routeHealth} t={t} />
                </div>
                <LabeledCell label={t("editor.agentRouting.columns.provider")}>
                  <Select
                    ariaLabel={`${difficulty} ${t("editor.agentRouting.columns.provider")}`}
                    disabled={inherited}
                    options={[
                      {
                        value: "",
                        label: t("editor.agentRouting.inheritPlaceholder"),
                      },
                      ...providerOptions,
                    ]}
                    placeholder={t("editor.agentRouting.inheritPlaceholder")}
                    value={profile.provider}
                    onChange={(value) => updateProfile(difficulty, "provider", value)}
                  />
                </LabeledCell>
                <LabeledCell label={t("editor.agentRouting.columns.model")}>
                  {modelOptions.length > 0 ? (
                    <Select
                      ariaLabel={`${difficulty} ${t("editor.agentRouting.columns.model")}`}
                      disabled={inherited}
                      options={[
                        {
                          value: "",
                          label: t("editor.agentRouting.inheritPlaceholder"),
                        },
                        ...modelOptions,
                      ]}
                      placeholder={t("editor.agentRouting.inheritPlaceholder")}
                      value={profile.model}
                      onChange={(value) => updateProfile(difficulty, "model", value)}
                    />
                  ) : (
                    <input
                      className={editorControlClassName}
                      disabled={inherited}
                      placeholder={t("editor.agentRouting.modelPlaceholder")}
                      value={profile.model}
                      onChange={(event) =>
                        updateProfile(difficulty, "model", event.target.value)
                      }
                    />
                  )}
                </LabeledCell>
                <LabeledCell label={t("editor.agentRouting.columns.reasoning")}>
                  <Select
                    ariaLabel={`${difficulty} ${t("editor.agentRouting.columns.reasoning")}`}
                    disabled={inherited}
                    options={[
                      {
                        value: "",
                        label: t("editor.agentRouting.inheritPlaceholder"),
                      },
                      ...reasoningEffortOptions,
                    ]}
                    value={profile.reasoningEffort}
                    onChange={(value) =>
                      updateProfile(difficulty, "reasoningEffort", value)
                    }
                  />
                </LabeledCell>
                {routeHealth.issues.length > 0 ? (
                  <div className="space-y-1 lg:col-span-4">
                    {routeHealth.issues.map((issue) => (
                      <RouteHealthIssueText
                        key={issue.code}
                        health={routeHealth}
                        issue={issue}
                        profile={profile}
                        t={t}
                      />
                    ))}
                  </div>
                ) : null}
              </div>
            );
          })}
        </div>
      </div>
    </>
  );
}
