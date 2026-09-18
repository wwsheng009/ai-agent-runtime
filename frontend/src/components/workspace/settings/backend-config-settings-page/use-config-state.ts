// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { type RuntimeConfigDocument, type RuntimeServiceStatus, getRuntimeConfigDocument, getRuntimeServiceStatus } from "@/lib/runtime-api";
import { buildConfigLineDiff } from "../runtime-config-diff";
import {
  countConfigIssues,
  validateRuntimeConfigRawDraft,
  validateRuntimeConfigStructuredDraft,
} from "../runtime-config-validation";
import { getRuntimeCircuitBreakerConfig } from "../runtime-circuit-breaker-domain-utils";
import { getRuntimeConcurrencyConfig, listRuntimeConcurrencyProviderLimits } from "../runtime-concurrency-domain-utils";
import { getRuntimeAuthConfig, listRuntimeProviderGroupSummaries } from "../runtime-config-domain-utils";
import { getRuntimeDefaultProvider, listRuntimeProviderSummaries } from "../runtime-provider-config-utils";
import { getRuntimeRateLimitConfig, listRuntimeRateLimitApiKeyLimits, listRuntimeRateLimitPathLimits } from "../runtime-rate-limit-domain-utils";
import { getRuntimeProviderQueueConfig, listRuntimeProviderQueueProviders } from "../runtime-provider-queue-domain-utils";
import { getRuntimeResourceManagerConfig } from "../runtime-resource-manager-domain-utils";
import { getRuntimeMonitorConfig } from "../runtime-monitor-domain-utils";
import { getRuntimeProxyConfig } from "../runtime-proxy-domain-utils";
import { getRuntimeWebsocketConfig } from "../runtime-websocket-domain-utils";
import { getRuntimeRoutingConfig, listRuntimeRouteSummaries } from "../runtime-routing-domain-utils";
import { getRuntimeRetryConfig, listRuntimeRetryRules } from "../runtime-retry-domain-utils";
import { getRuntimeTransformerConfig, listRuntimeTransformerModifierSummaries } from "../runtime-transformer-domain-utils";
import { analyzeRuntimeAgentRoutingConfig, getRuntimeAgentRoutingSettings } from "../runtime-agent-routing-domain-utils";
import { countImpactPaths, formatRuntimeProxySummary } from "./format";
import { modeMenuEntries } from "./mode-registry";
import { type EditorMode } from "./types";


export function useConfigEditorState() {
  const { i18n, t } = useTranslation("runtimeConfig");
  const { t: tCommon } = useTranslation("common");
  const resolvedLocale = i18n.resolvedLanguage?.startsWith("zh")
    ? "zh-CN"
    : "en-US";
  const [document, setDocument] = useState<RuntimeConfigDocument | null>(null);
  const [draftParsed, setDraftParsed] = useState<unknown>(null);
  const [draftRaw, setDraftRaw] = useState("");
  const [previewDocument, setPreviewDocument] =
    useState<RuntimeConfigDocument | null>(null);
  const [mode, setMode] = useState<EditorMode>("providers");
  const [isLoading, setIsLoading] = useState(true);
  const [isSaving, setIsSaving] = useState(false);
  const [isPreviewLoading, setIsPreviewLoading] = useState(false);
  const [isRestarting, setIsRestarting] = useState(false);
  const [isModeSwitching, setIsModeSwitching] = useState(false);
  const [serviceStatus, setServiceStatus] =
    useState<RuntimeServiceStatus | null>(null);
  const [serviceError, setServiceError] = useState<string | null>(null);
  const [statusMessage, setStatusMessage] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const providers = useMemo(
    () => listRuntimeProviderSummaries(draftParsed),
    [draftParsed],
  );
  const agentRoutingSettings = useMemo(
    () => getRuntimeAgentRoutingSettings(draftParsed),
    [draftParsed],
  );
  const agentRoutingHealth = useMemo(() => {
    const subagents = analyzeRuntimeAgentRoutingConfig(
      agentRoutingSettings.subagents,
      providers,
    );
    const teams = agentRoutingSettings.teamUsesSubagentRouting
      ? null
      : analyzeRuntimeAgentRoutingConfig(agentRoutingSettings.teams, providers);
    return {
      errorCount: subagents.errorCount + (teams?.errorCount ?? 0),
      warningCount: subagents.warningCount + (teams?.warningCount ?? 0),
    };
  }, [agentRoutingSettings, providers]);
  const providerGroups = useMemo(
    () => listRuntimeProviderGroupSummaries(draftParsed),
    [draftParsed],
  );
  const proxyConfig = useMemo(
    () => getRuntimeProxyConfig(draftParsed),
    [draftParsed],
  );
  const routes = useMemo(
    () => listRuntimeRouteSummaries(draftParsed),
    [draftParsed],
  );
  const routingConfig = useMemo(
    () => getRuntimeRoutingConfig(draftParsed),
    [draftParsed],
  );
  const rateLimitConfig = useMemo(
    () => getRuntimeRateLimitConfig(draftParsed),
    [draftParsed],
  );
  const resourceManagerConfig = useMemo(
    () => getRuntimeResourceManagerConfig(draftParsed),
    [draftParsed],
  );
  const providerQueueConfig = useMemo(
    () => getRuntimeProviderQueueConfig(draftParsed),
    [draftParsed],
  );
  const providerQueueProviders = useMemo(
    () => listRuntimeProviderQueueProviders(draftParsed),
    [draftParsed],
  );
  const apiKeyLimits = useMemo(
    () => listRuntimeRateLimitApiKeyLimits(draftParsed),
    [draftParsed],
  );
  const pathLimits = useMemo(
    () => listRuntimeRateLimitPathLimits(draftParsed),
    [draftParsed],
  );
  const concurrencyConfig = useMemo(
    () => getRuntimeConcurrencyConfig(draftParsed),
    [draftParsed],
  );
  const concurrencyProviderLimits = useMemo(
    () => listRuntimeConcurrencyProviderLimits(draftParsed),
    [draftParsed],
  );
  const retryConfig = useMemo(
    () => getRuntimeRetryConfig(draftParsed),
    [draftParsed],
  );
  const retryRules = useMemo(
    () => listRuntimeRetryRules(draftParsed),
    [draftParsed],
  );
  const transformerConfig = useMemo(
    () => getRuntimeTransformerConfig(draftParsed),
    [draftParsed],
  );
  const requestTransformerModifiers = useMemo(
    () => listRuntimeTransformerModifierSummaries(draftParsed, "request"),
    [draftParsed],
  );
  const responseTransformerModifiers = useMemo(
    () => listRuntimeTransformerModifierSummaries(draftParsed, "response"),
    [draftParsed],
  );
  const monitorConfig = useMemo(
    () => getRuntimeMonitorConfig(draftParsed),
    [draftParsed],
  );
  const websocketConfig = useMemo(
    () => getRuntimeWebsocketConfig(draftParsed),
    [draftParsed],
  );
  const circuitBreakerConfig = useMemo(
    () => getRuntimeCircuitBreakerConfig(draftParsed),
    [draftParsed],
  );
  const authConfig = useMemo(
    () => getRuntimeAuthConfig(draftParsed),
    [draftParsed],
  );
  const defaultProvider = useMemo(
    () => getRuntimeDefaultProvider(draftParsed),
    [draftParsed],
  );
  // 批次 18（P2-2 子片 1）：草稿校验只读草稿本身，渲染期不触文档、不写存储。
  const draftIssues = useMemo(
    () =>
      mode === "source"
        ? validateRuntimeConfigRawDraft(draftRaw)
        : validateRuntimeConfigStructuredDraft(draftParsed),
    [draftParsed, draftRaw, mode],
  );
  const draftErrorCount = countConfigIssues(draftIssues, "error");
  const draftWarningCount = countConfigIssues(draftIssues, "warning");
  const hasDraftErrors = draftErrorCount > 0;
  const translatedModeMenuEntries = useMemo(
    () =>
      modeMenuEntries.map((entry) => ({
        ...entry,
        description: t(entry.descriptionKey as never) as string,
        label: t(entry.labelKey as never) as string,
      })),
    [t],
  );

  const hasDomainChanges =
    JSON.stringify(document?.parsed ?? null) !==
    JSON.stringify(draftParsed ?? null);
  const hasSourceChanges = draftRaw !== (document?.raw ?? "");
  const hasUnsavedChanges = hasDomainChanges || hasSourceChanges;
  const enabledProviderCount = providers.filter(
    (provider) => provider.enabled,
  ).length;
  const draftLineCount = draftRaw === "" ? 0 : draftRaw.split(/\r?\n/).length;

  function getModeBadge(modeValue: EditorMode) {
    switch (modeValue) {
      case "providers":
        return t("editor.counts.providers", { count: providers.length });
      case "agentRouting":
        return getAgentRoutingStatusLabel();
      case "providerGroups":
        return t("editor.counts.providerGroups", {
          count: providerGroups.length,
        });
      case "networkProxy":
        return formatRuntimeProxySummary(proxyConfig, t, tCommon);
      case "routing":
        return t("editor.counts.routes", { count: routes.length });
      case "rateLimit":
        return t("editor.counts.rules", {
          count: apiKeyLimits.length + pathLimits.length,
        });
      case "resourceManager":
        return resourceManagerConfig.enabled
          ? tCommon("states.enabled")
          : tCommon("states.disabled");
      case "providerQueue":
        return providerQueueConfig.enabled
          ? t("editor.counts.providerQueueProviders", {
              count: providerQueueProviders.length,
            })
          : tCommon("states.disabled");
      case "concurrency":
        return concurrencyConfig.enabled
          ? t("editor.counts.concurrencyProviders", {
              count: concurrencyProviderLimits.length,
            })
          : tCommon("states.disabled");
      case "retry":
        return retryConfig.enabled
          ? t("editor.counts.retryRules", { count: retryRules.length })
          : tCommon("states.disabled");
      case "monitor":
        return monitorConfig.enabled
          ? tCommon("states.enabled")
          : tCommon("states.disabled");
      case "websocket":
        return websocketConfig.enabled
          ? tCommon("states.enabled")
          : tCommon("states.disabled");
      case "circuitBreaker":
        return circuitBreakerConfig.failureThreshold || "--";
      case "transformer":
        return t("editor.counts.transformerModifiers", {
          count:
            requestTransformerModifiers.length +
            responseTransformerModifiers.length,
        });
      case "auth":
        return authConfig.accessAuthEnabled
          ? tCommon("states.enabled")
          : tCommon("states.disabled");
      case "mcp":
        // MCP 面板数据独立于 config document，模式徽标由面板内部自行展示统计。
        return "";
      case "source":
      default:
        return t("editor.counts.lines", { count: draftLineCount });
    }
  }

  function getAgentRoutingStatusLabel() {
    if (agentRoutingHealth.errorCount > 0) {
      return t("editor.agentRouting.health.errorCount", {
        count: agentRoutingHealth.errorCount,
      });
    }
    if (agentRoutingHealth.warningCount > 0) {
      return t("editor.agentRouting.health.warningCount", {
        count: agentRoutingHealth.warningCount,
      });
    }
    return agentRoutingSettings.subagents.enabled ||
      (!agentRoutingSettings.teamUsesSubagentRouting &&
        agentRoutingSettings.teams.enabled)
      ? tCommon("states.enabled")
      : tCommon("states.disabled");
  }

  function getModeSummary(modeValue: EditorMode) {
    switch (modeValue) {
      case "providers":
        return t("editor.summary.providers");
      case "agentRouting":
        return t("editor.summary.agentRouting");
      case "providerGroups":
        return t("editor.summary.groups");
      case "networkProxy":
        return t("editor.summary.proxy");
      case "routing":
        return t("editor.summary.routes");
      case "rateLimit":
        return t("editor.summary.limits");
      case "resourceManager":
        return t("editor.summary.resourceManager");
      case "providerQueue":
        return t("editor.summary.providerQueue");
      case "concurrency":
        return t("editor.summary.concurrency");
      case "retry":
        return t("editor.summary.retry");
      case "monitor":
        return t("editor.summary.monitor");
      case "websocket":
        return t("editor.summary.websocket");
      case "circuitBreaker":
        return t("editor.summary.circuitBreaker");
      case "transformer":
        return t("editor.summary.transformer");
      case "auth":
        return t("editor.modes.auth.description");
      case "mcp":
        return t("editor.summary.mcp");
      case "source":
      default:
        return t("editor.modes.source.label");
    }
  }

  const previewDiff = useMemo(
    () =>
      document && previewDocument
        ? buildConfigLineDiff(document.raw, previewDocument.raw)
        : [],
    [document, previewDocument],
  );
  const previewAdditions = previewDiff.filter(
    (line) => line.type === "add",
  ).length;
  const previewRemovals = previewDiff.filter(
    (line) => line.type === "remove",
  ).length;
  const isPreviewFresh =
    previewDocument == null
      ? false
      : mode === "source"
        ? previewDocument.raw === draftRaw
        : JSON.stringify(previewDocument.parsed ?? null) ===
          JSON.stringify(draftParsed ?? null);
  const impactDocument =
    previewDocument &&
    countImpactPaths(previewDocument.runtime_impact?.changed_paths) > 0
      ? previewDocument
      : document;
  const shouldShowImpactPanel = Boolean(
    impactDocument &&
    countImpactPaths(impactDocument.runtime_impact?.changed_paths) > 0,
  );
  const savedRequiresRestart = Boolean(document?.restart_required);
  const previewRequiresRestart = Boolean(
    previewDocument?.restart_required && isPreviewFresh,
  );
  const canSaveAndRestart = previewRequiresRestart;

  useEffect(() => {
    async function load() {
      setIsLoading(true);
      setError(null);
      try {
        const [nextDocument, nextService] = await Promise.all([
          getRuntimeConfigDocument(),
          getRuntimeServiceStatus().catch((serviceLoadError) => {
            setServiceError(
              serviceLoadError instanceof Error
                ? serviceLoadError.message
                : t("editor.messages.loadServiceStatusFailed"),
            );
            return null;
          }),
        ]);
        setDocument(nextDocument);
        setDraftParsed(nextDocument.parsed);
        setDraftRaw(nextDocument.raw);
        setPreviewDocument(null);
        setServiceStatus(nextService);
      } catch (loadError) {
        setError(
          loadError instanceof Error
            ? loadError.message
            : t("editor.messages.loadBackendConfigFailed"),
        );
      } finally {
        setIsLoading(false);
      }
    }

    void load();
  }, [t]);

  useEffect(() => {
    if (!hasUnsavedChanges) {
      return;
    }

    const handleBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", handleBeforeUnload);
    return () => window.removeEventListener("beforeunload", handleBeforeUnload);
  }, [hasUnsavedChanges]);

  return {
    resolvedLocale,
    document,
    setDocument,
    draftParsed,
    setDraftParsed,
    draftRaw,
    setDraftRaw,
    previewDocument,
    setPreviewDocument,
    mode,
    setMode,
    isLoading,
    setIsLoading,
    isSaving,
    setIsSaving,
    isPreviewLoading,
    setIsPreviewLoading,
    isRestarting,
    setIsRestarting,
    isModeSwitching,
    setIsModeSwitching,
    serviceStatus,
    setServiceStatus,
    serviceError,
    setServiceError,
    statusMessage,
    setStatusMessage,
    error,
    setError,
    providers,
    agentRoutingSettings,
    agentRoutingHealth,
    providerGroups,
    proxyConfig,
    routes,
    routingConfig,
    rateLimitConfig,
    resourceManagerConfig,
    providerQueueConfig,
    providerQueueProviders,
    apiKeyLimits,
    pathLimits,
    concurrencyConfig,
    concurrencyProviderLimits,
    retryConfig,
    retryRules,
    transformerConfig,
    requestTransformerModifiers,
    responseTransformerModifiers,
    monitorConfig,
    websocketConfig,
    circuitBreakerConfig,
    authConfig,
    defaultProvider,
    draftIssues,
    draftErrorCount,
    draftWarningCount,
    hasDraftErrors,
    translatedModeMenuEntries,
    hasDomainChanges,
    hasSourceChanges,
    hasUnsavedChanges,
    enabledProviderCount,
    draftLineCount,
    getModeBadge,
    getAgentRoutingStatusLabel,
    getModeSummary,
    previewDiff,
    previewAdditions,
    previewRemovals,
    isPreviewFresh,
    impactDocument,
    shouldShowImpactPanel,
    savedRequiresRestart,
    previewRequiresRestart,
    canSaveAndRestart,
    t,
    tCommon,
    i18n,
  };
}

export type ConfigEditorState = ReturnType<typeof useConfigEditorState>;
