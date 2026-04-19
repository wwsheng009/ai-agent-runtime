import { describe, expect, it } from "vitest";

import {
  createDefaultRateLimitApiKeyLimit,
  createDefaultRateLimitPathLimit,
  getRuntimeRateLimitConfig,
  listRuntimeRateLimitApiKeyLimits,
  listRuntimeRateLimitPathLimits,
} from "./runtime-rate-limit-domain-utils";

describe("runtime-rate-limit-domain-utils", () => {
  it("reads rate limit root config", () => {
    expect(
      getRuntimeRateLimitConfig({
        rate_limit: {
          enabled: true,
          storage: "memory",
          algorithm: "token_bucket",
          default_limits: {
            qps: 10,
            tpm: 10000,
          },
          global_limits: {
            global_qps: 1000,
            global_tpm: 1000000,
          },
        },
      }),
    ).toMatchObject({
      enabled: true,
      storage: "memory",
      algorithm: "token_bucket",
      defaultQps: "10",
      defaultTpm: "10000",
      globalQps: "1000",
      globalTpm: "1000000",
    });
  });

  it("lists api key and path limit summaries", () => {
    const config = {
      rate_limit: {
        api_key_limits: [
          {
            api_key_pattern: "sk-",
            qps: 10,
            qpd: 100000,
            qpm: 600,
            block_duration: "60s",
            custom: true,
          },
        ],
        path_limits: {
          "/v1/responses": {
            requests_per_minute: 60,
            burst: 15,
            custom: true,
          },
        },
      },
    };

    expect(listRuntimeRateLimitApiKeyLimits(config)[0]).toMatchObject({
      apiKeyPattern: "sk-",
      qps: "10",
      qpd: "100000",
      qpm: "600",
      blockDuration: "60s",
      extraFieldCount: 1,
    });

    expect(listRuntimeRateLimitPathLimits(config)[0]).toMatchObject({
      path: "/v1/responses",
      requestsPerMinute: "60",
      burst: "15",
      extraFieldCount: 1,
    });
  });

  it("creates sensible defaults", () => {
    expect(createDefaultRateLimitApiKeyLimit()).toMatchObject({
      api_key_pattern: "",
      qps: 10,
      qpd: 100000,
      qpm: 600,
      block_duration: "60s",
    });
    expect(createDefaultRateLimitPathLimit()).toMatchObject({
      requests_per_minute: 60,
      burst: 10,
    });
  });
});
