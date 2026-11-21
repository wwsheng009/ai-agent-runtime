import { useMemo } from "react";

import { getRuntimeCircuitBreakerConfig } from "../runtime-circuit-breaker-domain-utils";
import {
  getRuntimeConcurrencyConfig,
  listRuntimeConcurrencyProviderLimits,
} from "../runtime-concurrency-domain-utils";
import {
  getRuntimeAuthConfig,
  listRuntimeProviderGroupSummaries,
} from "../runtime-config-domain-utils";
import {
  getRuntimeDefaultProvider,
  listRuntimeProviderSummaries,
} from "../runtime-provider-config-utils";
import {
  getRuntimeRateLimitConfig,
  listRuntimeRateLimitApiKeyLimits,
  listRuntimeRateLimitPathLimits,
} from "../runtime-rate-limit-domain-utils";
import {
  getRuntimeProviderQueueConfig,
  listRuntimeProviderQueueProviders,
} from "../runtime-provider-queue-domain-utils";
import { getRuntimeResourceManagerConfig } from "../runtime-resource-manager-domain-utils";
import { getRuntimeMonitorConfig } from "../runtime-monitor-domain-utils";
import { getRuntimeProxyConfig } from "../runtime-proxy-domain-utils";
import { getRuntimeWebsocketConfig } from "../runtime-websocket-domain-utils";
import {
  getRuntimeRoutingConfig,
  listRuntimeRouteSummaries,
} from "../runtime-routing-domain-utils";
import {
  getRuntimeRetryConfig,
  listRuntimeRetryRules,
} from "../runtime-retry-domain-utils";
import {
  getRuntimeTransformerConfig,
  listRuntimeTransformerModifierSummaries,
} from "../runtime-transformer-domain-utils";
import {
  analyzeRuntimeAgentRoutingConfig,
  getRuntimeAgentRoutingSettings,
} from "../runtime-agent-routing-domain-utils";
import { getRuntimeShellConfig } from "../runtime-shell-domain-utils";

/** Draft-only derivations; loading/document effects stay in useConfigEditorState. */
export function useConfigEditorDerivations(draftParsed: unknown) {
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
  const shellConfig = useMemo(
    () => getRuntimeShellConfig(draftParsed),
    [draftParsed],
  );
  const defaultProvider = useMemo(
    () => getRuntimeDefaultProvider(draftParsed),
    [draftParsed],
  );

  return {
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
    shellConfig,
    defaultProvider,
  };
}
