// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { getConfigValueAtPath, setConfigValueAtPath } from "../../runtime-config-editor-utils";
import { isConfigRecord } from "../../runtime-provider-config-utils";
import { type RuntimeMonitorConfigSummary, normalizeMonitorChannels } from "../../runtime-monitor-domain-utils";
import { parseLooseScalar } from "../format";
import { type ConfigEditorCore } from "../use-config-core";


export function createMonitorDomain(core: ConfigEditorCore) {
  const {
    setDraftParsed,
  } = core;

  function handleMonitorConfigChange(
    nextMonitorConfig: RuntimeMonitorConfigSummary,
  ) {
    setDraftParsed((current: unknown) => {
      const currentMonitorValue = getConfigValueAtPath(current, ["monitor"]);
      const currentMonitor = isConfigRecord(currentMonitorValue)
        ? currentMonitorValue
        : {};
      const currentMetrics = isConfigRecord(currentMonitor.metrics)
        ? currentMonitor.metrics
        : {};
      const currentTracing = isConfigRecord(currentMonitor.tracing)
        ? currentMonitor.tracing
        : {};
      const currentAlert = isConfigRecord(currentMonitor.alert)
        ? currentMonitor.alert
        : {};
      const currentPprof = isConfigRecord(currentMonitor.pprof)
        ? currentMonitor.pprof
        : {};
      const currentMemory = isConfigRecord(currentMonitor.memory)
        ? currentMonitor.memory
        : {};

      return setConfigValueAtPath(current, ["monitor"], {
        ...currentMonitor,
        enabled: nextMonitorConfig.enabled,
        metrics: {
          ...currentMetrics,
          enabled: nextMonitorConfig.metricsEnabled,
          path: nextMonitorConfig.metricsPath,
          aggregation: nextMonitorConfig.metricsAggregation,
        },
        tracing: {
          ...currentTracing,
          enabled: nextMonitorConfig.tracingEnabled,
          sampler: parseLooseScalar(nextMonitorConfig.tracingSampler),
          exporter: nextMonitorConfig.tracingExporter,
          server_addr: nextMonitorConfig.tracingServerAddr,
        },
        alert: {
          ...currentAlert,
          enabled: nextMonitorConfig.alertEnabled,
          webhook_url: nextMonitorConfig.alertWebhookUrl,
          channels: normalizeMonitorChannels(
            nextMonitorConfig.alertChannelsText,
          ),
          min_threshold: parseLooseScalar(nextMonitorConfig.alertMinThreshold),
          severity: nextMonitorConfig.alertSeverity,
        },
        pprof: {
          ...currentPprof,
          enabled: nextMonitorConfig.pprofEnabled,
          listen_addr: nextMonitorConfig.pprofListenAddr,
          gc_interval: nextMonitorConfig.pprofGcInterval,
        },
        memory: {
          ...currentMemory,
          enabled: nextMonitorConfig.memoryEnabled,
          sample_interval: nextMonitorConfig.memorySampleInterval,
          alert_threshold_mb: parseLooseScalar(
            nextMonitorConfig.memoryAlertThresholdMb,
          ),
          leak_threshold_percent: parseLooseScalar(
            nextMonitorConfig.memoryLeakThresholdPercent,
          ),
        },
      });
    });
  }


  return {
    handleMonitorConfigChange,
  };
}

export type MonitorDomain = ReturnType<typeof createMonitorDomain>;
