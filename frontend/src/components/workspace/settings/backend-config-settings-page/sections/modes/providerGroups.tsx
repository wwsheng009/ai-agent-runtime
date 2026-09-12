// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense } from "react";
import { type ProviderGroupsDomain } from "../../domains/provider-groups";
import { RuntimeProviderGroupsDomainEditor } from "../../lazy-editors";
import { ConfigEditorLoadingCard } from "../../primitives";
import { type ConfigEditorCore } from "../../use-config-core";


export function ProviderGroupsModeSection({ core, domain }: { core: ConfigEditorCore; domain: ProviderGroupsDomain }) {
  const {
    providerGroups,
    providers,
    t,
  } = core;

  const {
    handleDeleteProviderGroup,
    handleSaveProviderGroup,
  } = domain;

  return (
    <Suspense
      fallback={
        <ConfigEditorLoadingCard
          label={t("editor.modes.providerGroups.label")}
        />
      }
    >
      <RuntimeProviderGroupsDomainEditor
        groups={providerGroups}
        onDeleteGroup={handleDeleteProviderGroup}
        onSaveGroup={handleSaveProviderGroup}
        providers={providers}
      />
    </Suspense>
  );
}
