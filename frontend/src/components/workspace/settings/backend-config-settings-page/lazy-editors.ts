// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { lazy } from "react";


export const RuntimeProviderDomainEditor = lazy(() =>
  import("../runtime-provider-domain-editor").then((module) => ({
    default: module.RuntimeProviderDomainEditor,
  })),
);
export const RuntimeProviderGroupsDomainEditor = lazy(() =>
  import("../runtime-provider-groups-domain-editor").then((module) => ({
    default: module.RuntimeProviderGroupsDomainEditor,
  })),
);
export const RuntimeAuthDomainEditor = lazy(() =>
  import("../runtime-auth-domain-editor").then((module) => ({
    default: module.RuntimeAuthDomainEditor,
  })),
);
export const RuntimeRoutingDomainEditor = lazy(() =>
  import("../runtime-routing-domain-editor").then((module) => ({
    default: module.RuntimeRoutingDomainEditor,
  })),
);
export const RuntimeRateLimitDomainEditor = lazy(() =>
  import("../runtime-rate-limit-domain-editor").then((module) => ({
    default: module.RuntimeRateLimitDomainEditor,
  })),
);
export const RuntimeResourceManagerDomainEditor = lazy(() =>
  import("../runtime-resource-manager-domain-editor").then((module) => ({
    default: module.RuntimeResourceManagerDomainEditor,
  })),
);
export const RuntimeProviderQueueDomainEditor = lazy(() =>
  import("../runtime-provider-queue-domain-editor").then((module) => ({
    default: module.RuntimeProviderQueueDomainEditor,
  })),
);
export const RuntimeConcurrencyDomainEditor = lazy(() =>
  import("../runtime-concurrency-domain-editor").then((module) => ({
    default: module.RuntimeConcurrencyDomainEditor,
  })),
);
export const RuntimeRetryDomainEditor = lazy(() =>
  import("../runtime-retry-domain-editor").then((module) => ({
    default: module.RuntimeRetryDomainEditor,
  })),
);
export const RuntimeMonitorDomainEditor = lazy(() =>
  import("../runtime-monitor-domain-editor").then((module) => ({
    default: module.RuntimeMonitorDomainEditor,
  })),
);
export const RuntimeProxyDomainEditor = lazy(() =>
  import("../runtime-proxy-domain-editor").then((module) => ({
    default: module.RuntimeProxyDomainEditor,
  })),
);
export const RuntimeWebsocketDomainEditor = lazy(() =>
  import("../runtime-websocket-domain-editor").then((module) => ({
    default: module.RuntimeWebsocketDomainEditor,
  })),
);
export const RuntimeCircuitBreakerDomainEditor = lazy(() =>
  import("../runtime-circuit-breaker-domain-editor").then((module) => ({
    default: module.RuntimeCircuitBreakerDomainEditor,
  })),
);
export const RuntimeTransformerDomainEditor = lazy(() =>
  import("../runtime-transformer-domain-editor").then((module) => ({
    default: module.RuntimeTransformerDomainEditor,
  })),
);
export const RuntimeAgentRoutingDomainEditor = lazy(() =>
  import("../runtime-agent-routing-domain-editor").then((module) => ({
    default: module.RuntimeAgentRoutingDomainEditor,
  })),
);
