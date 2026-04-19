import { isConfigRecord } from "./runtime-provider-config-utils";

export type RuntimeCircuitBreakerConfigSummary = {
  failureRate: string;
  failureThreshold: string;
  halfOpenMaxCalls: string;
  openTimeout: string;
  sampleThreshold: string;
  windowDuration: string;
};

export function getRuntimeCircuitBreakerConfig(
  value: unknown,
): RuntimeCircuitBreakerConfigSummary {
  const circuitBreakerRoot =
    isConfigRecord(value) && isConfigRecord(value.circuit_breaker)
      ? value.circuit_breaker
      : {};

  return {
    failureThreshold: readText(circuitBreakerRoot.failure_threshold),
    failureRate: readText(circuitBreakerRoot.failure_rate),
    sampleThreshold: readText(circuitBreakerRoot.sample_threshold),
    windowDuration: readText(circuitBreakerRoot.window_duration),
    openTimeout: readText(circuitBreakerRoot.open_timeout),
    halfOpenMaxCalls: readText(circuitBreakerRoot.half_open_max_calls),
  };
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
