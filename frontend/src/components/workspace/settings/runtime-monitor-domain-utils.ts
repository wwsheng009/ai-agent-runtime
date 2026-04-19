import {
  isConfigRecord,
  normalizeStringArrayInput,
} from "./runtime-provider-config-utils";

export type RuntimeMonitorConfigSummary = {
  alertChannelsText: string;
  alertEnabled: boolean;
  alertMinThreshold: string;
  alertSeverity: string;
  alertWebhookUrl: string;
  enabled: boolean;
  memoryAlertThresholdMb: string;
  memoryEnabled: boolean;
  memoryLeakThresholdPercent: string;
  memorySampleInterval: string;
  metricsAggregation: boolean;
  metricsEnabled: boolean;
  metricsPath: string;
  pprofEnabled: boolean;
  pprofGcInterval: string;
  pprofListenAddr: string;
  tracingEnabled: boolean;
  tracingExporter: string;
  tracingSampler: string;
  tracingServerAddr: string;
};

export function getRuntimeMonitorConfig(
  value: unknown,
): RuntimeMonitorConfigSummary {
  const monitorRoot =
    isConfigRecord(value) && isConfigRecord(value.monitor) ? value.monitor : {};
  const metrics = isConfigRecord(monitorRoot.metrics) ? monitorRoot.metrics : {};
  const tracing = isConfigRecord(monitorRoot.tracing) ? monitorRoot.tracing : {};
  const alert = isConfigRecord(monitorRoot.alert) ? monitorRoot.alert : {};
  const pprof = isConfigRecord(monitorRoot.pprof) ? monitorRoot.pprof : {};
  const memory = isConfigRecord(monitorRoot.memory) ? monitorRoot.memory : {};

  return {
    enabled: Boolean(monitorRoot.enabled),
    metricsEnabled: Boolean(metrics.enabled),
    metricsPath: readText(metrics.path),
    metricsAggregation: Boolean(metrics.aggregation),
    tracingEnabled: Boolean(tracing.enabled),
    tracingSampler: readText(tracing.sampler),
    tracingExporter: readText(tracing.exporter),
    tracingServerAddr: readText(tracing.server_addr),
    alertEnabled: Boolean(alert.enabled),
    alertWebhookUrl: readText(alert.webhook_url),
    alertChannelsText: readTextArray(alert.channels).join("\n"),
    alertMinThreshold: readText(alert.min_threshold),
    alertSeverity: readText(alert.severity),
    pprofEnabled: Boolean(pprof.enabled),
    pprofListenAddr: readText(pprof.listen_addr),
    pprofGcInterval: readText(pprof.gc_interval),
    memoryEnabled: Boolean(memory.enabled),
    memorySampleInterval: readText(memory.sample_interval),
    memoryAlertThresholdMb: readText(memory.alert_threshold_mb),
    memoryLeakThresholdPercent: readText(memory.leak_threshold_percent),
  };
}

export function normalizeMonitorChannels(value: string) {
  return normalizeStringArrayInput(value);
}

function readText(value: unknown) {
  if (typeof value === "string") {
    return value;
  }
  if (typeof value === "number") {
    return String(value);
  }
  return "";
}

function readTextArray(value: unknown) {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .map((item) => (typeof item === "string" ? item.trim() : ""))
    .filter(Boolean);
}
