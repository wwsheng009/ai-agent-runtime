// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense } from "react";
import { type ProvidersDomain } from "../../domains/providers";
import { RuntimeProviderDomainEditor } from "../../lazy-editors";
import { ConfigEditorLoadingCard } from "../../primitives";
import { type ConfigEditorCore } from "../../use-config-core";


export function ProvidersModeSection({ core, domain }: { core: ConfigEditorCore; domain: ProvidersDomain }) {
  const {
    defaultProvider,
    providers,
    t,
  } = core;

  const {
    handleApplyProviderAccountFields,
    handleDeleteProvider,
    handleSaveProvider,
    handleSetDefaultProvider,
  } = domain;

  return (
    <Suspense
      fallback={
        <ConfigEditorLoadingCard
          label={t("editor.modes.providers.label")}
        />
      }
    >
      <RuntimeProviderDomainEditor
        defaultProvider={defaultProvider}
        onApplyProviderAccountFields={handleApplyProviderAccountFields}
        onDeleteProvider={handleDeleteProvider}
        onSaveProvider={handleSaveProvider}
        onSetDefaultProvider={handleSetDefaultProvider}
        providers={providers}
      />
    </Suspense>
  );
}
