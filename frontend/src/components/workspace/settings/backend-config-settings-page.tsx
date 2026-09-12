// P0-2：页面壳（state/actions 由 useConfigEditorCore 提供，域处理器与 section 分模块）。

import { SettingsSection } from "./settings-section";

import { useConfigEditorCore } from "./backend-config-settings-page/use-config-core";

import { createProvidersDomain } from "./backend-config-settings-page/domains/providers";
import { createAgentRoutingDomain } from "./backend-config-settings-page/domains/agent-routing";
import { createProviderGroupsDomain } from "./backend-config-settings-page/domains/provider-groups";
import { createNetworkProxyDomain } from "./backend-config-settings-page/domains/network-proxy";
import { createAuthDomain } from "./backend-config-settings-page/domains/auth";
import { createRoutingDomain } from "./backend-config-settings-page/domains/routing";
import { createRateLimitDomain } from "./backend-config-settings-page/domains/rate-limit";
import { createResourceManagerDomain } from "./backend-config-settings-page/domains/resource-manager";
import { createProviderQueueDomain } from "./backend-config-settings-page/domains/provider-queue";
import { createConcurrencyDomain } from "./backend-config-settings-page/domains/concurrency";
import { createRetryDomain } from "./backend-config-settings-page/domains/retry";
import { createTransformerDomain } from "./backend-config-settings-page/domains/transformer";
import { createMonitorDomain } from "./backend-config-settings-page/domains/monitor";
import { createWebsocketDomain } from "./backend-config-settings-page/domains/websocket";
import { createCircuitBreakerDomain } from "./backend-config-settings-page/domains/circuit-breaker";

import { ConfigEditorHeaderSection } from "./backend-config-settings-page/sections/header-section";
import { ConfigImpactSection } from "./backend-config-settings-page/sections/impact-section";
import { ConfigEditorMenuPanel } from "./backend-config-settings-page/sections/menu-panel";
import { ConfigPreviewSection } from "./backend-config-settings-page/sections/preview-section";
import { ConfigUnsavedBar } from "./backend-config-settings-page/sections/unsaved-bar";

import { ProvidersModeSection } from "./backend-config-settings-page/sections/modes/providers";
import { AgentRoutingModeSection } from "./backend-config-settings-page/sections/modes/agentRouting";
import { ProviderGroupsModeSection } from "./backend-config-settings-page/sections/modes/providerGroups";
import { NetworkProxyModeSection } from "./backend-config-settings-page/sections/modes/networkProxy";
import { AuthModeSection } from "./backend-config-settings-page/sections/modes/auth";
import { RoutingModeSection } from "./backend-config-settings-page/sections/modes/routing";
import { RateLimitModeSection } from "./backend-config-settings-page/sections/modes/rateLimit";
import { ResourceManagerModeSection } from "./backend-config-settings-page/sections/modes/resourceManager";
import { ProviderQueueModeSection } from "./backend-config-settings-page/sections/modes/providerQueue";
import { ConcurrencyModeSection } from "./backend-config-settings-page/sections/modes/concurrency";
import { RetryModeSection } from "./backend-config-settings-page/sections/modes/retry";
import { MonitorModeSection } from "./backend-config-settings-page/sections/modes/monitor";
import { WebsocketModeSection } from "./backend-config-settings-page/sections/modes/websocket";
import { CircuitBreakerModeSection } from "./backend-config-settings-page/sections/modes/circuitBreaker";
import { TransformerModeSection } from "./backend-config-settings-page/sections/modes/transformer";
import { SourceModeSection } from "./backend-config-settings-page/sections/modes/source";

export function BackendConfigSettingsPage() {
  const core = useConfigEditorCore();
  const providersDomain = createProvidersDomain(core);
  const agentRoutingDomain = createAgentRoutingDomain(core);
  const providerGroupsDomain = createProviderGroupsDomain(core);
  const networkProxyDomain = createNetworkProxyDomain(core);
  const authDomain = createAuthDomain(core);
  const routingDomain = createRoutingDomain(core);
  const rateLimitDomain = createRateLimitDomain(core);
  const resourceManagerDomain = createResourceManagerDomain(core);
  const providerQueueDomain = createProviderQueueDomain(core);
  const concurrencyDomain = createConcurrencyDomain(core);
  const retryDomain = createRetryDomain(core);
  const transformerDomain = createTransformerDomain(core);
  const monitorDomain = createMonitorDomain(core);
  const websocketDomain = createWebsocketDomain(core);
  const circuitBreakerDomain = createCircuitBreakerDomain(core);

  return (
    <div className="space-y-6">
      <ConfigEditorHeaderSection core={core} />

      <ConfigImpactSection core={core} />

      <SettingsSection
        title={core.t("editor.panels.editorTitle")}
        description={core.t("editor.panels.editorDescription")}
      >
        <div className="grid gap-3 lg:grid-cols-[16rem_minmax(0,1fr)] xl:grid-cols-[17rem_minmax(0,1fr)]">
          <ConfigEditorMenuPanel core={core} />

          <div className="min-w-0 space-y-3">
            {core.mode === "providers" ? (
              <ProvidersModeSection core={core} domain={providersDomain} />
            ) : null}
            {core.mode === "agentRouting" ? (
              <AgentRoutingModeSection core={core} domain={agentRoutingDomain} />
            ) : null}
            {core.mode === "providerGroups" ? (
              <ProviderGroupsModeSection core={core} domain={providerGroupsDomain} />
            ) : null}
            {core.mode === "networkProxy" ? (
              <NetworkProxyModeSection core={core} domain={networkProxyDomain} />
            ) : null}
            {core.mode === "auth" ? (
              <AuthModeSection core={core} domain={authDomain} />
            ) : null}
            {core.mode === "routing" ? (
              <RoutingModeSection core={core} domain={routingDomain} />
            ) : null}
            {core.mode === "rateLimit" ? (
              <RateLimitModeSection core={core} domain={rateLimitDomain} />
            ) : null}
            {core.mode === "resourceManager" ? (
              <ResourceManagerModeSection core={core} domain={resourceManagerDomain} />
            ) : null}
            {core.mode === "providerQueue" ? (
              <ProviderQueueModeSection core={core} domain={providerQueueDomain} />
            ) : null}
            {core.mode === "concurrency" ? (
              <ConcurrencyModeSection core={core} domain={concurrencyDomain} />
            ) : null}
            {core.mode === "retry" ? (
              <RetryModeSection core={core} domain={retryDomain} />
            ) : null}
            {core.mode === "monitor" ? (
              <MonitorModeSection core={core} domain={monitorDomain} />
            ) : null}
            {core.mode === "websocket" ? (
              <WebsocketModeSection core={core} domain={websocketDomain} />
            ) : null}
            {core.mode === "circuitBreaker" ? (
              <CircuitBreakerModeSection core={core} domain={circuitBreakerDomain} />
            ) : null}
            {core.mode === "transformer" ? (
              <TransformerModeSection core={core} domain={transformerDomain} />
            ) : null}
            {core.mode === "source" ? (
              <SourceModeSection core={core} />
            ) : null}
          </div>
        </div>
      </SettingsSection>

      <ConfigPreviewSection core={core} />

      <ConfigUnsavedBar core={core} />
    </div>
  );
}
