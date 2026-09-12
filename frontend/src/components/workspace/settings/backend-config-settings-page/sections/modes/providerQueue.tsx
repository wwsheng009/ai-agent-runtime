// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense } from "react";
import { type ProviderQueueDomain } from "../../domains/provider-queue";
import { RuntimeProviderQueueDomainEditor } from "../../lazy-editors";
import { ConfigEditorLoadingCard } from "../../primitives";
import { type ConfigEditorCore } from "../../use-config-core";


export function ProviderQueueModeSection({ core, domain }: { core: ConfigEditorCore; domain: ProviderQueueDomain }) {
  const {
    providerQueueConfig,
    providerQueueProviders,
    t,
  } = core;

  const {
    handleDeleteProviderQueueProvider,
    handleProviderQueueConfigChange,
    handleSaveProviderQueueProvider,
  } = domain;

  return (
    <Suspense
      fallback={
        <ConfigEditorLoadingCard
          label={t("editor.modes.providerQueue.label")}
        />
      }
    >
      <RuntimeProviderQueueDomainEditor
        config={providerQueueConfig}
        onChangeConfig={handleProviderQueueConfigChange}
        onDeleteProvider={handleDeleteProviderQueueProvider}
        onSaveProvider={handleSaveProviderQueueProvider}
        providers={providerQueueProviders}
      />
    </Suspense>
  );
}
