import { describe, expect, it } from "vitest";

import { getRuntimeCircuitBreakerConfig } from "./runtime-circuit-breaker-domain-utils";

describe("runtime-circuit-breaker-domain-utils", () => {
  it("reads circuit breaker summary for the dedicated form editor", () => {
    expect(
      getRuntimeCircuitBreakerConfig({
        circuit_breaker: {
          failure_threshold: 3,
          failure_rate: 0.5,
          sample_threshold: 10,
          window_duration: "10s",
          open_timeout: "60s",
          half_open_max_calls: 1,
        },
      }),
    ).toEqual({
      failureThreshold: "3",
      failureRate: "0.5",
      sampleThreshold: "10",
      windowDuration: "10s",
      openTimeout: "60s",
      halfOpenMaxCalls: "1",
    });
  });
});
