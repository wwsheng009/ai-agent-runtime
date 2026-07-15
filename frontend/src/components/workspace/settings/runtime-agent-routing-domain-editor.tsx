import {
  BotIcon,
  GaugeIcon,
  LoaderCircleIcon,
  PlayIcon,
  RouteIcon,
  TriangleAlertIcon,
  UsersIcon,
} from "lucide-react";
import { type TFunction } from "i18next";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { cn } from "@/lib/utils";
import {
  type RuntimeAgentRoutePreviewResult,
  type RuntimeAgentRoutePreviewTask,
} from "@/types/runtime";

import { ConfigFormField } from "./config-form-field";
import { editorControlClassName } from "./editor-control-class";
import {
  agentRoutingDifficulties,
  analyzeRuntimeAgentRoutingConfig,
  providerModelOptions,
  providerReasoningEffortOptions,
  type AgentRoutingDifficulty,
  type AgentRoutingScope,
  type RuntimeAgentRouteProfile,
  type RuntimeAgentRoutingConfigSummary,
  type RuntimeAgentRouteHealth,
  type RuntimeAgentRouteHealthIssue,
  type RuntimeAgentRoutingSettings,
} from "./runtime-agent-routing-domain-utils";
import { type RuntimeProviderSummary } from "./runtime-provider-config-utils";
import { SettingsInlineToggleCard } from "./settings-inline-toggle-card";
import { SettingsNoticeCard } from "./settings-notice-card";

type RuntimeAgentRoutingDomainEditorProps = {
  onChange: (
    scope: AgentRoutingScope,
    config: RuntimeAgentRoutingConfigSummary,
  ) => void;
  onTeamInheritanceChange: (inherit: boolean) => void;
  onPreviewRoute: (
    scope: AgentRoutingScope,
    task: RuntimeAgentRoutePreviewTask,
  ) => Promise<RuntimeAgentRoutePreviewResult>;
  providers: RuntimeProviderSummary[];
  settings: RuntimeAgentRoutingSettings;
};

const reasoningPolicyValues = ["ignore", "downgrade", "fail"] as const;

export function RuntimeAgentRoutingDomainEditor({
  onChange,
  onPreviewRoute,
  onTeamInheritanceChange,
  providers,
  settings,
}: RuntimeAgentRoutingDomainEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const [scope, setScope] = useState<AgentRoutingScope>("subagents");
  const config = scope === "subagents" ? settings.subagents : settings.teams;
  const inherited = scope === "teams" && settings.teamUsesSubagentRouting;
  const health = useMemo(
    () => analyzeRuntimeAgentRoutingConfig(config, providers),
    [config, providers],
  );
  const enabledProviderCount = providers.filter((provider) => provider.enabled).length;
  const providerOptions = useMemo(() => {
    const names = new Set(
      providers.filter((provider) => provider.enabled).map((provider) => provider.name),
    );
    for (const difficulty of agentRoutingDifficulties) {
      const name = config.levels[difficulty].provider.trim();
      if (name) names.add(name);
    }
    return [...names].sort().map((value) => ({ value, label: value }));
  }, [config.levels, providers]);
  const [previewDifficulty, setPreviewDifficulty] =
    useState<AgentRoutingDifficulty>(config.defaultDifficulty);
  const [previewRole, setPreviewRole] = useState("");
  const [previewGoal, setPreviewGoal] = useState("");
  const [previewResult, setPreviewResult] =
    useState<RuntimeAgentRoutePreviewResult | null>(null);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [isPreviewing, setIsPreviewing] = useState(false);
  const previewRequestID = useRef(0);

  useEffect(() => {
    previewRequestID.current += 1;
    setPreviewResult(null);
    setPreviewError(null);
    setIsPreviewing(false);
  }, [config, previewDifficulty, previewGoal, previewRole, scope]);

  async function runRoutePreview() {
    const requestID = previewRequestID.current + 1;
    previewRequestID.current = requestID;
    setIsPreviewing(true);
    setPreviewError(null);
    try {
      const result = await onPreviewRoute(scope, {
        difficulty: previewDifficulty,
        goal: previewGoal.trim(),
        read_only: true,
        role: previewRole.trim(),
      });
      if (previewRequestID.current !== requestID) return;
      setPreviewResult(result);
    } catch (error) {
      if (previewRequestID.current !== requestID) return;
      setPreviewResult(null);
      setPreviewError(
        error instanceof Error
          ? error.message
          : t("editor.agentRouting.preview.failed"),
      );
    } finally {
      if (previewRequestID.current === requestID) {
        setIsPreviewing(false);
      }
    }
  }

  function updateConfig(
    update: (next: RuntimeAgentRoutingConfigSummary) => void,
  ) {
    const next = cloneRoutingConfig(config);
    update(next);
    onChange(scope, next);
  }

  function updateProfile(
    difficulty: AgentRoutingDifficulty,
    field: "provider" | "model" | "reasoningEffort",
    value: string,
  ) {
    updateConfig((next) => {
      const profile = next.levels[difficulty];
      profile[field] = value;
      if (field === "provider") {
        const provider = providers.find((item) => item.name === value);
        const supported = new Set(provider?.supportedModels ?? []);
        if (
          provider?.defaultModel &&
          (!profile.model || (supported.size > 0 && !supported.has(profile.model)))
        ) {
          profile.model = provider.defaultModel;
        }
      }
    });
  }

  return (
    <div className="space-y-3">
      <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
        <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
          <div className="min-w-0">
            <div className="flex items-center gap-2 text-base font-semibold text-[var(--foreground)]">
              <GaugeIcon size={17} className="text-[var(--accent-primary)]" />
              {t("editor.agentRouting.title")}
            </div>
            <div className="mt-1 text-sm leading-6 text-[var(--muted-foreground)]">
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
          <div className="grid min-w-[16rem] grid-cols-2 rounded-[0.7rem] border border-[var(--border)] bg-[var(--surface-solid)] p-1">
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

      <div className="grid gap-3 md:grid-cols-2">
        <ConfigFormField
          label={t("editor.agentRouting.maxExpertConcurrency.label")}
          description={t("editor.agentRouting.maxExpertConcurrency.description")}
        >
          <input
            className={editorControlClassName}
            disabled={inherited}
            min={0}
            type="number"
            value={config.maxExpertConcurrency}
            onChange={(event) =>
              updateConfig((next) => {
                next.maxExpertConcurrency = event.target.value;
              })
            }
          />
        </ConfigFormField>
        <ConfigFormField
          label={t("editor.agentRouting.reasoningPolicy.label")}
          description={t("editor.agentRouting.reasoningPolicy.description")}
        >
          <Select
            ariaLabel={t("editor.agentRouting.reasoningPolicy.label")}
            disabled={inherited}
            options={reasoningPolicyValues.map((value) => ({
              value,
              label: t(`editor.agentRouting.reasoningPolicies.${value}`),
            }))}
            value={config.unsupportedReasoningPolicy}
            onChange={(value) =>
              updateConfig((next) => {
                next.unsupportedReasoningPolicy = value;
              })
            }
          />
        </ConfigFormField>
      </div>

      <div className="border-t border-[var(--border)] pt-3">
        <div className="flex items-center gap-2 font-semibold text-[var(--foreground)]">
          <RouteIcon size={16} className="text-[var(--accent-primary)]" />
          {t("editor.agentRouting.preview.title")}
        </div>
        <div className="mt-3 grid gap-3 md:grid-cols-2 xl:grid-cols-[10rem_minmax(10rem,0.8fr)_minmax(16rem,1.4fr)_auto] xl:items-end">
          <ConfigFormField label={t("editor.agentRouting.preview.difficulty")}>
            <Select
              ariaLabel={t("editor.agentRouting.preview.difficulty")}
              options={difficultyOptions(t)}
              value={previewDifficulty}
              onChange={(value) =>
                setPreviewDifficulty(value as AgentRoutingDifficulty)
              }
            />
          </ConfigFormField>
          <ConfigFormField label={t("editor.agentRouting.preview.role")}>
            <input
              className={editorControlClassName}
              placeholder={t("editor.agentRouting.preview.rolePlaceholder")}
              value={previewRole}
              onChange={(event) => setPreviewRole(event.target.value)}
            />
          </ConfigFormField>
          <ConfigFormField label={t("editor.agentRouting.preview.goal")}>
            <input
              className={editorControlClassName}
              placeholder={t("editor.agentRouting.preview.goalPlaceholder")}
              value={previewGoal}
              onChange={(event) => setPreviewGoal(event.target.value)}
            />
          </ConfigFormField>
          <Button
            className="w-full xl:w-auto"
            disabled={isPreviewing}
            onClick={() => void runRoutePreview()}
          >
            {isPreviewing ? (
              <LoaderCircleIcon size={15} className="animate-spin" />
            ) : (
              <PlayIcon size={15} />
            )}
            {t(
              isPreviewing
                ? "editor.agentRouting.preview.running"
                : "editor.agentRouting.preview.run",
            )}
          </Button>
        </div>

        {previewError ? (
          <div className="mt-3">
            <SettingsNoticeCard tone="warning-soft">
              {previewError}
            </SettingsNoticeCard>
          </div>
        ) : null}
        {previewResult ? (
          <RoutePreviewResult result={previewResult} t={t} />
        ) : null}
      </div>
    </div>
  );
}

function RoutePreviewResult({
  result,
  t,
}: {
  result: RuntimeAgentRoutePreviewResult;
  t: TFunction<"runtimeConfig">;
}) {
  const decision = result.decision;
  const warnings = decision.warnings ?? [];
  return (
    <div className="mt-3 border-t border-[var(--border)] pt-3">
      <div className="flex flex-wrap items-center gap-1.5">
        <HealthBadge tone={result.routing_enabled ? "ready" : "neutral"}>
          {result.routing_enabled
            ? t("editor.agentRouting.preview.routingEnabled")
            : t("editor.agentRouting.preview.routingDisabled")}
        </HealthBadge>
        <HealthBadge tone="neutral">
          {previewTranslation(
            t,
            "routingSources",
            result.routing_source,
          )}
        </HealthBadge>
        {decision.source ? (
          <HealthBadge tone="neutral">
            {previewTranslation(t, "sources", decision.source)}
          </HealthBadge>
        ) : null}
        {decision.fallback_used ? (
          <HealthBadge tone="warning">
            {t("editor.agentRouting.preview.fallback")}
          </HealthBadge>
        ) : null}
      </div>

      <div className="mt-3 grid gap-x-5 gap-y-3 sm:grid-cols-2 xl:grid-cols-4">
        <PreviewValue
          label={t("editor.agentRouting.preview.provider")}
          value={decision.provider}
        />
        <PreviewValue
          label={t("editor.agentRouting.preview.model")}
          value={decision.model}
        />
        <PreviewValue
          label={t("editor.agentRouting.preview.reasoning")}
          value={decision.reasoning_effort}
        />
        <PreviewValue
          label={t("editor.agentRouting.preview.difficulty")}
          value={
            decision.difficulty
              ? previewTranslation(t, "difficulties", decision.difficulty)
              : ""
          }
        />
      </div>

      <div className="mt-3 text-xs leading-5 text-[var(--muted-foreground)]">
        {t("editor.agentRouting.preview.parent")}: {result.parent.provider || "-"}
        {" / "}
        {result.parent.model || "-"}
        {result.parent.reasoning_effort
          ? ` / ${result.parent.reasoning_effort}`
          : ""}
      </div>

      {warnings.length > 0 ? (
        <div className="mt-3 space-y-1 border-t border-[var(--border)] pt-2.5">
          {warnings.map((warning) => (
            <div
              key={warning}
              className="flex items-start gap-1.5 text-xs leading-5 text-[var(--muted-foreground)]"
            >
              <TriangleAlertIcon size={13} className="mt-1 shrink-0" />
              <span>{previewTranslation(t, "warnings", warning)}</span>
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}

function PreviewValue({ label, value }: { label: string; value?: string }) {
  return (
    <div className="min-w-0">
      <div className="text-xs text-[var(--muted-foreground)]">{label}</div>
      <div className="mt-1 break-words text-sm font-semibold text-[var(--foreground)]">
        {value || "-"}
      </div>
    </div>
  );
}

function previewTranslation(
  t: TFunction<"runtimeConfig">,
  group: "difficulties" | "routingSources" | "sources" | "warnings",
  value: string,
) {
  const key = `editor.agentRouting.preview.${group}.${value}`;
  const translated = t(key as never);
  return translated === key ? value : translated;
}

function RouteHealthBadge({
  health,
  t,
}: {
  health: RuntimeAgentRouteHealth;
  t: TFunction<"runtimeConfig">;
}) {
  if (health.issues.some((issue) => issue.severity === "error")) {
    return (
      <HealthBadge tone="error">
        {t("editor.agentRouting.health.routeError")}
      </HealthBadge>
    );
  }
  if (health.issues.length > 0) {
    return (
      <HealthBadge tone="warning">
        {t("editor.agentRouting.health.routeWarning")}
      </HealthBadge>
    );
  }

  const labelKey =
    health.mode === "configured"
      ? "configured"
      : health.mode === "providerDefault"
        ? "providerDefault"
        : health.mode === "disabled"
          ? "routingDisabled"
          : "parentInherited";
  return (
    <HealthBadge
      tone={
        health.mode === "configured" || health.mode === "providerDefault"
          ? "ready"
          : "neutral"
      }
    >
      {t(`editor.agentRouting.health.${labelKey}`)}
    </HealthBadge>
  );
}

function RouteHealthIssueText({
  health,
  issue,
  profile,
  t,
}: {
  health: RuntimeAgentRouteHealth;
  issue: RuntimeAgentRouteHealthIssue;
  profile: RuntimeAgentRouteProfile;
  t: TFunction<"runtimeConfig">;
}) {
  const message = routeHealthIssueMessage(t, issue, health, profile);
  return (
    <div
      className={cn(
        "flex items-start gap-1.5 text-xs leading-5",
        issue.severity === "error"
          ? "text-[#f5c7b8]"
          : "text-[var(--muted-foreground)]",
      )}
    >
      <TriangleAlertIcon size={13} className="mt-1 shrink-0" />
      <span>{message}</span>
    </div>
  );
}

function routeHealthIssueMessage(
  t: TFunction<"runtimeConfig">,
  issue: RuntimeAgentRouteHealthIssue,
  health: RuntimeAgentRouteHealth,
  profile: RuntimeAgentRouteProfile,
) {
  return t(`editor.agentRouting.health.issues.${issue.code}`, {
    effort: profile.reasoningEffort,
    model: health.effectiveModel || profile.model,
    provider: profile.provider,
    resolvedProvider: health.effectiveProvider,
  });
}

function HealthBadge({
  children,
  tone,
}: {
  children: ReactNode;
  tone: "error" | "neutral" | "ready" | "warning";
}) {
  return (
    <Badge
      className={cn(
        "normal-case tracking-normal",
        tone === "ready"
          ? "border-[#8fd0c6]/24 bg-[#8fd0c6]/10 text-[var(--foreground)]"
          : tone === "error"
            ? "border-[#f59e7d]/38 bg-[#f59e7d]/12 text-[#f5c7b8]"
            : tone === "warning"
              ? "border-[#e7d58c]/28 bg-[#e7d58c]/10 text-[var(--foreground)]"
              : undefined,
      )}
    >
      {children}
    </Badge>
  );
}

function ScopeButton({
  active,
  icon: Icon,
  label,
  onClick,
}: {
  active: boolean;
  icon: typeof BotIcon;
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        "flex min-h-9 items-center justify-center gap-2 rounded-[0.55rem] px-3 text-sm font-medium transition",
        active
          ? "bg-[var(--accent-primary-soft)] text-[var(--foreground)]"
          : "text-[var(--muted-foreground)] hover:bg-[var(--surface-soft)] hover:text-[var(--foreground)]",
      )}
    >
      <Icon size={15} />
      <span>{label}</span>
    </button>
  );
}

function LabeledCell({ children, label }: { children: ReactNode; label: string }) {
  return (
    <div className="min-w-0">
      <div className="mb-1 text-xs font-medium text-[var(--muted-foreground)] lg:hidden">
        {label}
      </div>
      {children}
    </div>
  );
}

function difficultyOptions(t: TFunction<"runtimeConfig">) {
  return agentRoutingDifficulties.map((difficulty) => ({
    value: difficulty,
    label: difficultyLabel(t, difficulty),
  }));
}

function difficultyLabel(
  t: TFunction<"runtimeConfig">,
  difficulty: AgentRoutingDifficulty,
) {
  return t(`editor.agentRouting.difficulties.${difficulty}`);
}

function cloneRoutingConfig(
  config: RuntimeAgentRoutingConfigSummary,
): RuntimeAgentRoutingConfigSummary {
  return {
    ...config,
    raw: { ...config.raw },
    levels: Object.fromEntries(
      agentRoutingDifficulties.map((difficulty) => [
        difficulty,
        { ...config.levels[difficulty] },
      ]),
    ) as RuntimeAgentRoutingConfigSummary["levels"],
  };
}
