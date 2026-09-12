// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { ActivityIcon, BotIcon, GaugeIcon, HardDriveDownloadIcon, RefreshCcwIcon, RouteIcon, Settings2Icon, WifiIcon } from "lucide-react";
import { formatBytes, formatTimestamp } from "../format";
import { StatCard } from "../primitives";
import { type ConfigEditorCore } from "../use-config-core";


export function ConfigEditorSummaryGrid({ core }: { core: ConfigEditorCore }) {
  const {
    apiKeyLimits,
    authConfig,
    circuitBreakerConfig,
    concurrencyConfig,
    concurrencyProviderLimits,
    defaultProvider,
    document,
    monitorConfig,
    pathLimits,
    providerGroups,
    providerQueueConfig,
    providerQueueProviders,
    providers,
    rateLimitConfig,
    requestTransformerModifiers,
    resolvedLocale,
    resourceManagerConfig,
    responseTransformerModifiers,
    retryConfig,
    retryRules,
    routes,
    routingConfig,
    serviceError,
    serviceStatus,
    t,
    tCommon,
    transformerConfig,
    websocketConfig,
  } = core;

  return (
    <div className="grid gap-2 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-12">
      <StatCard
        icon={BotIcon}
        label={t("editor.cards.provider")}
        value={`${providers.length}`}
        detail={
          defaultProvider
            ? t("editor.cards.providerDefault", {
                defaultProvider,
              })
            : t("editor.cards.providerEmpty")
        }
      />
      <StatCard
        icon={RouteIcon}
        label={t("editor.cards.providerGroup")}
        value={`${providerGroups.length}`}
        detail={
          providerGroups.length > 0
            ? t("editor.cards.providerGroupSummary", {
                count: providerGroups.reduce(
                  (total, group) => total + group.providerCount,
                  0,
                ),
              })
            : t("editor.cards.providerGroupEmpty")
        }
      />
      <StatCard
        icon={Settings2Icon}
        label={t("editor.cards.auth")}
        value={authConfig.accessAuthEnabled ? tCommon("states.enabled") : tCommon("states.disabled")}
        detail={
          authConfig.adminAuthEnabled
            ? t("editor.cards.authAdminEnabled")
            : t("editor.cards.authAdminDisabled")
        }
      />
      <StatCard
        icon={RouteIcon}
        label={t("editor.cards.routing")}
        value={`${routes.length} 条`}
        detail={
          routingConfig.strategy
            ? t("editor.cards.routingStrategy", {
                strategy: routingConfig.strategy,
              })
            : t("editor.cards.routingEmpty")
        }
      />
      <StatCard
        icon={GaugeIcon}
        label={t("editor.cards.rateLimit")}
        value={
          rateLimitConfig.enabled
            ? tCommon("states.enabled")
            : tCommon("states.disabled")
        }
        detail={
          apiKeyLimits.length > 0 || pathLimits.length > 0
            ? t("editor.cards.rateLimitSummary", {
                apiKeyCount: String(apiKeyLimits.length),
                pathCount: String(pathLimits.length),
              })
            : t("editor.cards.rateLimitEmpty")
        }
      />
      <StatCard
        icon={RouteIcon}
        label={t("editor.cards.resourceManager")}
        value={
          resourceManagerConfig.enabled
            ? tCommon("states.enabled")
            : tCommon("states.disabled")
        }
        detail={
          resourceManagerConfig.enabled
            ? t("editor.cards.resourceManagerSummary", {
                groupAlgorithm:
                  resourceManagerConfig.defaultGroupAlgorithm || "--",
                providerAlgorithm:
                  resourceManagerConfig.defaultProviderAlgorithm || "--",
                keyAlgorithm:
                  resourceManagerConfig.defaultKeyAlgorithm || "--",
              })
            : t("editor.cards.resourceManagerEmpty")
        }
      />
      <StatCard
        icon={GaugeIcon}
        label={t("editor.cards.providerQueue")}
        value={
          providerQueueConfig.enabled
            ? tCommon("states.enabled")
            : tCommon("states.disabled")
        }
        detail={
          providerQueueProviders.length > 0
            ? t("editor.cards.providerQueueSummary", {
                count: providerQueueProviders.length,
                defaultMaxConcurrency:
                  providerQueueConfig.defaultMaxConcurrency || "--",
              })
            : providerQueueConfig.waitHeartbeatEnabled
              ? t("editor.cards.providerQueueHeartbeat", {
                  interval: providerQueueConfig.waitHeartbeatInterval || "--",
                })
              : t("editor.cards.providerQueueEmpty")
        }
      />
      <StatCard
        icon={GaugeIcon}
        label={t("editor.cards.concurrency")}
        value={
          concurrencyConfig.enabled
            ? tCommon("states.enabled")
            : tCommon("states.disabled")
        }
        detail={
          concurrencyProviderLimits.length > 0
            ? t("editor.cards.concurrencySummary", {
                maxConcurrentRequests:
                  concurrencyConfig.maxConcurrentRequests || "--",
                count: concurrencyProviderLimits.length,
              })
            : concurrencyConfig.queueTimeout
              ? t("editor.cards.concurrencyQueueTimeout", {
                  queueTimeout: concurrencyConfig.queueTimeout,
                })
              : t("editor.cards.concurrencyEmpty")
        }
      />
      <StatCard
        icon={RefreshCcwIcon}
        label={t("editor.cards.retry")}
        value={
          retryConfig.enabled
            ? tCommon("states.enabled")
            : tCommon("states.disabled")
        }
        detail={
          retryRules.length > 0
            ? t("editor.cards.retrySummary", {
                count: retryRules.length,
                defaultMaxRetries: retryConfig.defaultMaxRetries || "--",
              })
            : t("editor.cards.retryEmpty")
        }
      />
      <StatCard
        icon={ActivityIcon}
        label={t("editor.cards.monitor")}
        value={
          monitorConfig.enabled
            ? tCommon("states.enabled")
            : tCommon("states.disabled")
        }
        detail={
          monitorConfig.metricsEnabled || monitorConfig.tracingEnabled
            ? t("editor.cards.monitorSummary", {
                metricsState: monitorConfig.metricsEnabled
                  ? tCommon("states.enabled")
                  : tCommon("states.disabled"),
                tracingState: monitorConfig.tracingEnabled
                  ? tCommon("states.enabled")
                  : tCommon("states.disabled"),
              })
            : t("editor.cards.monitorEmpty")
        }
      />
      <StatCard
        icon={WifiIcon}
        label={t("editor.cards.websocket")}
        value={
          websocketConfig.enabled
            ? tCommon("states.enabled")
            : tCommon("states.disabled")
        }
        detail={
          websocketConfig.enabled
            ? t("editor.cards.websocketSummary", {
                responsesState: websocketConfig.responsesIngressEnabled
                  ? tCommon("states.enabled")
                  : tCommon("states.disabled"),
                realtimeState: websocketConfig.realtimeIngressEnabled
                  ? tCommon("states.enabled")
                  : tCommon("states.disabled"),
              })
            : t("editor.cards.websocketEmpty")
        }
      />
      <StatCard
        icon={ActivityIcon}
        label={t("editor.cards.circuitBreaker")}
        value={circuitBreakerConfig.failureThreshold || "--"}
        detail={
          circuitBreakerConfig.openTimeout
            ? t("editor.cards.circuitBreakerSummary", {
                openTimeout: circuitBreakerConfig.openTimeout,
                failureRate: circuitBreakerConfig.failureRate || "--",
              })
            : t("editor.cards.circuitBreakerEmpty")
        }
      />
      <StatCard
        icon={Settings2Icon}
        label={t("editor.cards.transformer")}
        value={
          transformerConfig.httpTransformStageEnabled
            ? tCommon("states.enabled")
            : tCommon("states.disabled")
        }
        detail={
          requestTransformerModifiers.length +
            responseTransformerModifiers.length >
          0
            ? t("editor.cards.transformerSummary", {
                requestCount: String(requestTransformerModifiers.length),
                responseCount: String(responseTransformerModifiers.length),
              })
            : transformerConfig.highPerf
              ? t("editor.cards.transformerHighPerf")
              : t("editor.cards.transformerEmpty")
        }
      />
      <StatCard
        icon={HardDriveDownloadIcon}
        label={t("editor.cards.configFile")}
        value={document ? formatBytes(document.size_bytes) : "--"}
        detail={
          document?.updated_at
            ? t("editor.cards.configFileLoaded", {
                path: document.path,
                timestamp: formatTimestamp(
                  document.updated_at,
                  resolvedLocale,
                ),
              })
            : (document?.path ?? t("editor.cards.configFileEmpty"))
        }
      />
      <StatCard
        icon={RefreshCcwIcon}
        label={t("editor.cards.runtimeServer")}
        value={
          serviceStatus?.running
            ? tCommon("states.online")
            : tCommon("states.offline")
        }
        detail={
          serviceStatus?.listen_addr ||
          serviceError ||
          t("editor.cards.runtimeServerEmpty")
        }
      />
    </div>
  );
}
