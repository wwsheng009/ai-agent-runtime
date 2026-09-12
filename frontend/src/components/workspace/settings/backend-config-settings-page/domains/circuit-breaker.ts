// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { getConfigValueAtPath, setConfigValueAtPath } from "../../runtime-config-editor-utils";
import { type RuntimeCircuitBreakerConfigSummary } from "../../runtime-circuit-breaker-domain-utils";
import { isConfigRecord } from "../../runtime-provider-config-utils";
import { parseLooseScalar } from "../format";
import { type ConfigEditorCore } from "../use-config-core";


export function createCircuitBreakerDomain(core: ConfigEditorCore) {
  const {
    setDraftParsed,
  } = core;

  function handleCircuitBreakerConfigChange(
    nextCircuitBreakerConfig: RuntimeCircuitBreakerConfigSummary,
  ) {
    setDraftParsed((current: unknown) => {
      const currentCircuitBreakerValue = getConfigValueAtPath(current, [
        "circuit_breaker",
      ]);
      const currentCircuitBreaker = isConfigRecord(currentCircuitBreakerValue)
        ? currentCircuitBreakerValue
        : {};

      return setConfigValueAtPath(current, ["circuit_breaker"], {
        ...currentCircuitBreaker,
        failure_threshold: parseLooseScalar(
          nextCircuitBreakerConfig.failureThreshold,
        ),
        failure_rate: parseLooseScalar(nextCircuitBreakerConfig.failureRate),
        sample_threshold: parseLooseScalar(
          nextCircuitBreakerConfig.sampleThreshold,
        ),
        window_duration: nextCircuitBreakerConfig.windowDuration,
        open_timeout: nextCircuitBreakerConfig.openTimeout,
        half_open_max_calls: parseLooseScalar(
          nextCircuitBreakerConfig.halfOpenMaxCalls,
        ),
      });
    });
  }

  return {
    handleCircuitBreakerConfigChange,
  };
}

export type CircuitBreakerDomain = ReturnType<typeof createCircuitBreakerDomain>;
