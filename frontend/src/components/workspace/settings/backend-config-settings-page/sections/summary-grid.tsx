// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { ActivityIcon, BotIcon, ChevronDownIcon, GaugeIcon, HardDriveDownloadIcon, RefreshCcwIcon, RouteIcon, Settings2Icon, WifiIcon } from "lucide-react";
import { useState } from "react";
import { formatBytes, formatRuntimeProxySummary, formatTimestamp } from "../format";
import { StatCard, SummaryPill } from "../primitives";
import { type ConfigEditorCore } from "../use-config-core";

type SummaryCategory = "providers" | "performance" | "resilience" | "system";

const CATEGORY_ICONS = {
  providers: BotIcon,
  performance: GaugeIcon,
  resilience: RefreshCcwIcon,
  system: Settings2Icon,
} as const;

export function ConfigEditorSummaryGrid({ core }: { core: ConfigEditorCore }) {
  const {
    apiKeyLimits,
    authConfig,
    circuitBreakerConfig,
    concurrencyConfig,
    concurrencyProviderLimits,
    defaultProvider,
    document,
    draftLineCount,
    getAgentRoutingStatusLabel,
    getModeSummary,
    monitorConfig,
    pathLimits,
    previewDocument,
    providerGroups,
    providerQueueConfig,
    providerQueueProviders,
    providers,
    proxyConfig,
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

  const [isExpanded, setIsExpanded] = useState(false);
  const [activeCategory, setActiveCategory] = useState<SummaryCategory>("providers");

  const categoryCards: Record<SummaryCategory, React.ReactNode> = {
    providers: (
      <>
        <StatCard
          icon={BotIcon}
          label={t("editor.cards.provider")}
          value={`${providers.length}`}
          detail={
            defaultProvider
              ? t("editor.cards.providerDefault", { defaultProvider })
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
              ? t("editor.cards.routingStrategy", { strategy: routingConfig.strategy })
              : t("editor.cards.routingEmpty")
          }
        />
      </>
    ),
    performance: (
      <>
        <StatCard
          icon={GaugeIcon}
          label={t("editor.cards.rateLimit")}
          value={rateLimitConfig.enabled ? tCommon("states.enabled") : tCommon("states.disabled")}
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
          value={resourceManagerConfig.enabled ? tCommon("states.enabled") : tCommon("states.disabled")}
          detail={
            resourceManagerConfig.enabled
              ? t("editor.cards.resourceManagerSummary", {
                  groupAlgorithm: resourceManagerConfig.defaultGroupAlgorithm || "--",
                  providerAlgorithm: resourceManagerConfig.defaultProviderAlgorithm || "--",
                  keyAlgorithm: resourceManagerConfig.defaultKeyAlgorithm || "--",
                })
              : t("editor.cards.resourceManagerEmpty")
          }
        />
        <StatCard
          icon={GaugeIcon}
          label={t("editor.cards.providerQueue")}
          value={providerQueueConfig.enabled ? tCommon("states.enabled") : tCommon("states.disabled")}
          detail={
            providerQueueProviders.length > 0
              ? t("editor.cards.providerQueueSummary", {
                  count: providerQueueProviders.length,
                  defaultMaxConcurrency: providerQueueConfig.defaultMaxConcurrency || "--",
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
          value={concurrencyConfig.enabled ? tCommon("states.enabled") : tCommon("states.disabled")}
          detail={
            concurrencyProviderLimits.length > 0
              ? t("editor.cards.concurrencySummary", {
                  maxConcurrentRequests: concurrencyConfig.maxConcurrentRequests || "--",
                  count: concurrencyProviderLimits.length,
                })
              : concurrencyConfig.queueTimeout
                ? t("editor.cards.concurrencyQueueTimeout", {
                    queueTimeout: concurrencyConfig.queueTimeout,
                  })
                : t("editor.cards.concurrencyEmpty")
          }
        />
      </>
    ),
    resilience: (
      <>
        <StatCard
          icon={RefreshCcwIcon}
          label={t("editor.cards.retry")}
          value={retryConfig.enabled ? tCommon("states.enabled") : tCommon("states.disabled")}
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
          value={monitorConfig.enabled ? tCommon("states.enabled") : tCommon("states.disabled")}
          detail={
            monitorConfig.metricsEnabled || monitorConfig.tracingEnabled
              ? t("editor.cards.monitorSummary", {
                  metricsState: monitorConfig.metricsEnabled ? tCommon("states.enabled") : tCommon("states.disabled"),
                  tracingState: monitorConfig.tracingEnabled ? tCommon("states.enabled") : tCommon("states.disabled"),
                })
              : t("editor.cards.monitorEmpty")
          }
        />
        <StatCard
          icon={WifiIcon}
          label={t("editor.cards.websocket")}
          value={websocketConfig.enabled ? tCommon("states.enabled") : tCommon("states.disabled")}
          detail={
            websocketConfig.enabled
              ? t("editor.cards.websocketSummary", {
                  responsesState: websocketConfig.responsesIngressEnabled ? tCommon("states.enabled") : tCommon("states.disabled"),
                  realtimeState: websocketConfig.realtimeIngressEnabled ? tCommon("states.enabled") : tCommon("states.disabled"),
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
      </>
    ),
    system: (
      <>
        <StatCard
          icon={Settings2Icon}
          label={t("editor.cards.transformer")}
          value={transformerConfig.httpTransformStageEnabled ? tCommon("states.enabled") : tCommon("states.disabled")}
          detail={
            requestTransformerModifiers.length + responseTransformerModifiers.length > 0
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
                  timestamp: formatTimestamp(document.updated_at, resolvedLocale),
                })
              : (document?.path ?? t("editor.cards.configFileEmpty"))
          }
        />
        <StatCard
          icon={RefreshCcwIcon}
          label={t("editor.cards.runtimeServer")}
          value={serviceStatus?.running ? tCommon("states.online") : tCommon("states.offline")}
          detail={serviceStatus?.listen_addr || serviceError || t("editor.cards.runtimeServerEmpty")}
        />
      </>
    ),
  };

  const categories: { key: SummaryCategory; label: string }[] = [
    { key: "providers", label: "Providers" },
    { key: "performance", label: "Performance" },
    { key: "resilience", label: "Resilience" },
    { key: "system", label: "System" },
  ];

  return (
    <div className="rounded-panel border border-border bg-surface-softer">
      <button
        type="button"
        onClick={() => setIsExpanded(!isExpanded)}
        className="flex w-full items-center justify-between gap-3 px-3 py-2.5 text-left transition hover:bg-surface-soft"
        aria-expanded={isExpanded}
      >
        <div className="flex items-center gap-2">
          <span className="inline-flex size-6 shrink-0 items-center justify-center rounded-field border border-border bg-surface-solid text-accent-primary">
            <ActivityIcon size={12} />
          </span>
          <span className="text-sm font-semibold text-foreground">
            {t("editor.cards.overviewTitle")}
          </span>
          <span className="text-xs text-muted-foreground">
            {t("editor.cards.overviewCounts", {
              providers: `${providers.length}`,
              routes: `${routes.length}`,
            })}
          </span>
        </div>
        <ChevronDownIcon
          size={16}
          className={`text-muted-foreground transition-transform ${isExpanded ? "rotate-180" : ""}`}
        />
      </button>

      {isExpanded && (
        <div className="border-t border-border px-3 py-3">
          <div className="mb-3 flex gap-1.5 overflow-x-auto">
            {categories.map((cat) => {
              const Icon = CATEGORY_ICONS[cat.key];
              return (
                <button
                  key={cat.key}
                  type="button"
                  onClick={() => setActiveCategory(cat.key)}
                  className={`inline-flex shrink-0 items-center gap-1.5 rounded-control border px-2.5 py-1 text-xs font-medium transition ${
                    activeCategory === cat.key
                      ? "border-accent-primary-border bg-accent-primary-soft text-accent-primary"
                      : "border-border bg-surface-solid text-muted-foreground hover:border-border-strong hover:text-foreground"
                  }`}
                >
                  <Icon size={12} />
                  {cat.label}
                </button>
              );
            })}
          </div>
          <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
            {categoryCards[activeCategory]}
          </div>

          <div className="mt-3 pt-3 border-t border-border">
            <div className="flex flex-wrap gap-1.5">
              <SummaryPill
                label={t("editor.summary.lines")}
                value={`${draftLineCount}`}
              />
              <SummaryPill
                label={t("editor.summary.providers")}
                value={`${providers.length}`}
              />
              <SummaryPill
                label={t("editor.summary.agentRouting")}
                value={getAgentRoutingStatusLabel()}
              />
              <SummaryPill
                label={t("editor.summary.groups")}
                value={`${providerGroups.length}`}
              />
              <SummaryPill
                label={t("editor.summary.proxy")}
                value={formatRuntimeProxySummary(proxyConfig, t, tCommon)}
              />
              <SummaryPill
                label={t("editor.summary.routes")}
                value={`${routes.length}`}
              />
              <SummaryPill
                label={t("editor.summary.limits")}
                value={`${apiKeyLimits.length + pathLimits.length}`}
              />
              <SummaryPill
                label={t("editor.summary.resourceManager")}
                value={
                  resourceManagerConfig.enabled
                    ? tCommon("states.enabled")
                    : tCommon("states.disabled")
                }
              />
              <SummaryPill
                label={t("editor.summary.providerQueue")}
                value={
                  providerQueueConfig.enabled
                    ? `${providerQueueProviders.length}`
                    : tCommon("states.disabled")
                }
              />
              <SummaryPill
                label={t("editor.summary.concurrency")}
                value={
                  concurrencyConfig.enabled
                    ? tCommon("states.enabled")
                    : tCommon("states.disabled")
                }
              />
              <SummaryPill
                label={t("editor.summary.retry")}
                value={`${retryRules.length}`}
              />
              <SummaryPill
                label={t("editor.summary.monitor")}
                value={
                  monitorConfig.enabled
                    ? tCommon("states.enabled")
                    : tCommon("states.disabled")
                }
              />
              <SummaryPill
                label={t("editor.summary.websocket")}
                value={
                  websocketConfig.enabled
                    ? tCommon("states.enabled")
                    : tCommon("states.disabled")
                }
              />
              <SummaryPill
                label={t("editor.summary.circuitBreaker")}
                value={circuitBreakerConfig.failureThreshold || "--"}
              />
              <SummaryPill
                label={t("editor.summary.transformer")}
                value={`${requestTransformerModifiers.length + responseTransformerModifiers.length}`}
              />
              <SummaryPill
                label={t("editor.summary.preview")}
                value={
                  previewDocument
                    ? tCommon("states.generated")
                    : tCommon("states.notGenerated")
                }
              />
            </div>
            <div className="mt-2 rounded-[0.75rem] border border-border bg-surface-solid px-3 py-2 text-xs leading-5 text-muted-foreground">
              {getModeSummary(core.mode)}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
