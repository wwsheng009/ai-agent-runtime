import { describe, expect, it } from "vitest";

import {
  getRuntimeMonitorConfig,
  normalizeMonitorChannels,
} from "./runtime-monitor-domain-utils";

describe("runtime-monitor-domain-utils", () => {
  it("reads monitor summary for the dedicated form editor", () => {
    expect(
      getRuntimeMonitorConfig({
        monitor: {
          enabled: true,
          metrics: {
            enabled: false,
            path: "/metrics",
            aggregation: true,
          },
          tracing: {
            enabled: true,
            sampler: 0.1,
            exporter: "stdout",
            server_addr: "localhost:4318",
          },
          alert: {
            enabled: true,
            webhook_url: "https://example.com/webhook",
            channels: ["webhook", "slack"],
            min_threshold: 5,
            severity: "warning",
          },
          pprof: {
            enabled: true,
            listen_addr: ":6060",
            gc_interval: "30s",
          },
          memory: {
            enabled: true,
            sample_interval: "15s",
            alert_threshold_mb: 1024,
            leak_threshold_percent: 50,
          },
        },
      }),
    ).toMatchObject({
      enabled: true,
      metricsEnabled: false,
      metricsPath: "/metrics",
      metricsAggregation: true,
      tracingEnabled: true,
      tracingSampler: "0.1",
      tracingExporter: "stdout",
      tracingServerAddr: "localhost:4318",
      alertEnabled: true,
      alertChannelsText: "webhook\nslack",
      alertMinThreshold: "5",
      pprofEnabled: true,
      memoryEnabled: true,
      memoryAlertThresholdMb: "1024",
      memoryLeakThresholdPercent: "50",
    });
  });

  it("normalizes monitor channels input", () => {
    expect(normalizeMonitorChannels("webhook\n slack, email")).toEqual([
      "webhook",
      "slack",
      "email",
    ]);
  });
});
