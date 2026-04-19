import { describe, expect, it } from "vitest";

import { getRuntimeResourceManagerConfig } from "./runtime-resource-manager-domain-utils";

describe("runtime-resource-manager-domain-utils", () => {
  it("reads resource manager summary for the dedicated form editor", () => {
    expect(
      getRuntimeResourceManagerConfig({
        resource_manager: {
          enabled: true,
          default_group_algorithm: "tiered",
          default_provider_algorithm: "round_robin",
          default_key_algorithm: "health_based",
          cross_provider_key_selection: true,
          health_check: {
            enabled: true,
            interval: "60s",
            auto_recovery: true,
            recovery_threshold: 0.9,
          },
          enable_stats: true,
          stats_retention: "24h",
        },
      }),
    ).toEqual({
      enabled: true,
      defaultGroupAlgorithm: "tiered",
      defaultProviderAlgorithm: "round_robin",
      defaultKeyAlgorithm: "health_based",
      crossProviderKeySelection: true,
      healthCheckEnabled: true,
      healthCheckInterval: "60s",
      healthCheckAutoRecovery: true,
      healthCheckRecoveryThreshold: "0.9",
      enableStats: true,
      statsRetention: "24h",
    });
  });
});
