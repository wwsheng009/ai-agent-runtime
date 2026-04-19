import { describe, expect, it } from "vitest";

import {
  getRuntimeConcurrencyConfig,
  listRuntimeConcurrencyProviderLimits,
} from "./runtime-concurrency-domain-utils";

describe("runtime-concurrency-domain-utils", () => {
  it("reads concurrency summary for the dedicated form editor", () => {
    expect(
      getRuntimeConcurrencyConfig({
        concurrency: {
          enabled: true,
          max_concurrent_requests: 200,
          queue_size: 500,
          queue_timeout: "5s",
        },
      }),
    ).toEqual({
      enabled: true,
      maxConcurrentRequests: "200",
      queueSize: "500",
      queueTimeout: "5s",
    });
  });

  it("lists provider-level concurrency limits", () => {
    expect(
      listRuntimeConcurrencyProviderLimits({
        concurrency: {
          per_provider_limits: {
            nvidia: 100,
            deepseek: 50,
            default: 40,
          },
        },
      }),
    ).toEqual([
      { id: "deepseek", provider: "deepseek", limit: "50" },
      { id: "default", provider: "default", limit: "40" },
      { id: "nvidia", provider: "nvidia", limit: "100" },
    ]);
  });
});
