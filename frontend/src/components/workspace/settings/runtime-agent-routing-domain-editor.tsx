import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  type RuntimeAgentRoutePreviewResult,
  type RuntimeAgentRoutePreviewTask,
} from "@/types/runtime";

import {
  agentRoutingDifficulties,
  analyzeRuntimeAgentRoutingConfig,
  type AgentRoutingDifficulty,
  type AgentRoutingScope,
  type RuntimeAgentRoutingConfigSummary,
  type RuntimeAgentRoutingSettings,
} from "./runtime-agent-routing-domain-utils";
import { cloneRoutingConfig } from "./runtime-agent-routing-domain-editor/format";
import { RoutingDifficultyTable } from "./runtime-agent-routing-domain-editor/routing-difficulty-table";
import { RoutingHeaderCard } from "./runtime-agent-routing-domain-editor/routing-header";
import { RoutingLimitsSection } from "./runtime-agent-routing-domain-editor/routing-limits-section";
import { RoutingPreviewSection } from "./runtime-agent-routing-domain-editor/routing-preview-section";
import { RoutingTogglesSection } from "./runtime-agent-routing-domain-editor/routing-toggles-section";
import { type RuntimeProviderSummary } from "./runtime-provider-config-utils";

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
      <RoutingHeaderCard
        config={config}
        health={health}
        inherited={inherited}
        scope={scope}
        setScope={setScope}
        t={t}
      />

      <RoutingTogglesSection
        config={config}
        enabledProviderCount={enabledProviderCount}
        health={health}
        inherited={inherited}
        onTeamInheritanceChange={onTeamInheritanceChange}
        scope={scope}
        settings={settings}
        t={t}
        updateConfig={updateConfig}
      />

      <RoutingDifficultyTable
        config={config}
        health={health}
        inherited={inherited}
        providerOptions={providerOptions}
        providers={providers}
        t={t}
        updateProfile={updateProfile}
      />

      <RoutingLimitsSection
        config={config}
        inherited={inherited}
        t={t}
        updateConfig={updateConfig}
      />

      <RoutingPreviewSection
        isPreviewing={isPreviewing}
        previewDifficulty={previewDifficulty}
        previewError={previewError}
        previewGoal={previewGoal}
        previewResult={previewResult}
        previewRole={previewRole}
        runRoutePreview={runRoutePreview}
        setPreviewDifficulty={setPreviewDifficulty}
        setPreviewGoal={setPreviewGoal}
        setPreviewRole={setPreviewRole}
        t={t}
      />
    </div>
  );
}
