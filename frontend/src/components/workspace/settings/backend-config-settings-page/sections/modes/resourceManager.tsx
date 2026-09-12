// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense } from "react";
import { type ResourceManagerDomain } from "../../domains/resource-manager";
import { RuntimeResourceManagerDomainEditor } from "../../lazy-editors";
import { ConfigEditorLoadingCard } from "../../primitives";
import { type ConfigEditorCore } from "../../use-config-core";


export function ResourceManagerModeSection({ core, domain }: { core: ConfigEditorCore; domain: ResourceManagerDomain }) {
  const {
    resourceManagerConfig,
    t,
  } = core;

  const {
    handleResourceManagerConfigChange,
  } = domain;

  return (
    <Suspense
      fallback={
        <ConfigEditorLoadingCard
          label={t("editor.modes.resourceManager.label")}
        />
      }
    >
      <RuntimeResourceManagerDomainEditor
        config={resourceManagerConfig}
        onChange={handleResourceManagerConfigChange}
      />
    </Suspense>
  );
}
