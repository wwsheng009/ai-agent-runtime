// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { RuntimeImpactPanel } from "../impact-panel";
import { type ConfigEditorCore } from "../use-config-core";


export function ConfigImpactSection({ core }: { core: ConfigEditorCore }) {
  const {
    impactDocument,
    restartService,
    serviceStatus,
    shouldShowImpactPanel,
  } = core;

  return (
    impactDocument && shouldShowImpactPanel ? (
      <RuntimeImpactPanel
        document={impactDocument}
        serviceRunning={Boolean(serviceStatus?.running)}
        onRestart={() => {
          void restartService();
        }}
      />
    ) : null
  );
}
