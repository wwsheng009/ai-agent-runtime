// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense } from "react";
import { type CircuitBreakerDomain } from "../../domains/circuit-breaker";
import { RuntimeCircuitBreakerDomainEditor } from "../../lazy-editors";
import { ConfigEditorLoadingCard } from "../../primitives";
import { type ConfigEditorCore } from "../../use-config-core";


export function CircuitBreakerModeSection({ core, domain }: { core: ConfigEditorCore; domain: CircuitBreakerDomain }) {
  const {
    circuitBreakerConfig,
    t,
  } = core;

  const {
    handleCircuitBreakerConfigChange,
  } = domain;

  return (
    <Suspense
      fallback={
        <ConfigEditorLoadingCard
          label={t("editor.modes.circuitBreaker.label")}
        />
      }
    >
      <RuntimeCircuitBreakerDomainEditor
        config={circuitBreakerConfig}
        onChange={handleCircuitBreakerConfigChange}
      />
    </Suspense>
  );
}
