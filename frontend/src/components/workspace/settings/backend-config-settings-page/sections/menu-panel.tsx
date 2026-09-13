// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { formatRuntimeProxySummary } from "../format";
import { ControlPanel, MenuButton, SummaryPill } from "../primitives";
import { type EditorMode } from "../types";
import { type ConfigEditorCore } from "../use-config-core";


export function ConfigEditorMenuPanel({ core }: { core: ConfigEditorCore }) {
  const {
    apiKeyLimits,
    circuitBreakerConfig,
    concurrencyConfig,
    draftLineCount,
    getAgentRoutingStatusLabel,
    getModeBadge,
    getModeSummary,
    isModeSwitching,
    mode,
    monitorConfig,
    pathLimits,
    previewDocument,
    providerGroups,
    providerQueueConfig,
    providerQueueProviders,
    providers,
    proxyConfig,
    requestTransformerModifiers,
    resourceManagerConfig,
    responseTransformerModifiers,
    retryRules,
    routes,
    switchMode,
    t,
    tCommon,
    translatedModeMenuEntries,
    websocketConfig,
  } = core;

  return (
    <div className="min-w-0 space-y-2.5 lg:sticky lg:top-[8.5rem] lg:self-start">
      <label className="block rounded-[0.9rem] border border-border bg-surface-softer p-3 lg:hidden">
        <span className="block text-sm font-semibold text-foreground">
          {t("editor.panels.modeTitle")}
        </span>
        <span className="mt-1 block text-xs leading-5 text-muted-foreground">
          {t("editor.panels.modeDescription")}
        </span>
        <select
          value={mode}
          disabled={isModeSwitching}
          aria-label={t("editor.panels.modeTitle")}
          onChange={(event) => {
            void switchMode(event.target.value as EditorMode);
          }}
          className="mt-3 h-10 w-full rounded-[0.7rem] border border-border bg-surface-solid px-3 text-sm text-foreground outline-none transition focus:border-accent-primary-border focus:ring-2 focus:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
        >
          {translatedModeMenuEntries.map((entry) => (
            <option key={entry.mode} value={entry.mode}>
              {entry.label}
            </option>
          ))}
        </select>
      </label>

      <div className="hidden lg:block">
        <ControlPanel
          title={t("editor.panels.modeTitle")}
          description={t("editor.panels.modeDescription")}
        >
          <div className="grid gap-2">
            {translatedModeMenuEntries.map((entry) => (
              <MenuButton
                key={entry.mode}
                active={mode === entry.mode}
                badge={getModeBadge(entry.mode)}
                description={entry.description}
                disabled={isModeSwitching}
                icon={entry.icon}
                label={entry.label}
                onClick={() => {
                  void switchMode(entry.mode);
                }}
              />
            ))}
          </div>
        </ControlPanel>
      </div>

      <ControlPanel
        title={t("editor.panels.summaryTitle")}
        description={t("editor.panels.summaryDescription")}
      >
        <div className="flex flex-wrap gap-2">
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
        <div className="mt-2.5 rounded-[0.75rem] border border-border bg-surface-solid px-3 py-2.5 text-sm leading-6 text-muted-foreground">
          {getModeSummary(mode)}
        </div>
      </ControlPanel>
    </div>
  );
}
