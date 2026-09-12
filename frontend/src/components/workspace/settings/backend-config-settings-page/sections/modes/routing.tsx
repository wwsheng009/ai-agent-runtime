// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense } from "react";
import { type RoutingDomain } from "../../domains/routing";
import { RuntimeRoutingDomainEditor } from "../../lazy-editors";
import { ConfigEditorLoadingCard } from "../../primitives";
import { type ConfigEditorCore } from "../../use-config-core";


export function RoutingModeSection({ core, domain }: { core: ConfigEditorCore; domain: RoutingDomain }) {
  const {
    providerGroups,
    routes,
    routingConfig,
    t,
  } = core;

  const {
    handleDeleteRoute,
    handleMoveRoute,
    handleRoutingConfigChange,
    handleSaveRoute,
  } = domain;

  return (
    <Suspense
      fallback={
        <ConfigEditorLoadingCard
          label={t("editor.modes.routing.label")}
        />
      }
    >
      <RuntimeRoutingDomainEditor
        availableGroups={providerGroups.map((group) => group.name)}
        onChangeConfig={handleRoutingConfigChange}
        onDeleteRoute={handleDeleteRoute}
        onMoveRoute={handleMoveRoute}
        onSaveRoute={handleSaveRoute}
        routeConfig={routingConfig}
        routes={routes}
      />
    </Suspense>
  );
}
