// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense } from "react";
import { type ConcurrencyDomain } from "../../domains/concurrency";
import { RuntimeConcurrencyDomainEditor } from "../../lazy-editors";
import { ConfigEditorLoadingCard } from "../../primitives";
import { type ConfigEditorCore } from "../../use-config-core";


export function ConcurrencyModeSection({ core, domain }: { core: ConfigEditorCore; domain: ConcurrencyDomain }) {
  const {
    concurrencyConfig,
    concurrencyProviderLimits,
    t,
  } = core;

  const {
    handleConcurrencyConfigChange,
    handleDeleteConcurrencyProviderLimit,
    handleSaveConcurrencyProviderLimit,
  } = domain;

  return (
    <Suspense
      fallback={
        <ConfigEditorLoadingCard
          label={t("editor.modes.concurrency.label")}
        />
      }
    >
      <RuntimeConcurrencyDomainEditor
        config={concurrencyConfig}
        onChange={handleConcurrencyConfigChange}
        onDeleteProviderLimit={handleDeleteConcurrencyProviderLimit}
        onSaveProviderLimit={handleSaveConcurrencyProviderLimit}
        providerLimits={concurrencyProviderLimits}
      />
    </Suspense>
  );
}
